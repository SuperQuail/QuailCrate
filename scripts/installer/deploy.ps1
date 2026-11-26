# Embedded in the NSIS installer and uninstaller. Runs under Windows PowerShell 5.1.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Install', 'Uninstall')][string]$Operation,
    [Parameter(Mandatory)][string]$InstallDir,
    [Parameter(Mandatory)][string]$Version
)

$ErrorActionPreference = 'Stop'
$keyName = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\QuailCrate'
$payload = Join-Path $PSScriptRoot 'payload'
$installing = $Operation -eq 'Install'

function Assert-OrdinaryPath([string]$Path) {
    while ($Path) {
        try {
            if ([IO.File]::GetAttributes($Path) -band [IO.FileAttributes]::ReparsePoint) {
                throw "The installation path contains a reparse point: $Path"
            }
        } catch [IO.FileNotFoundException] {
            # The new payload may introduce a file or directory.
        } catch [IO.DirectoryNotFoundException] {}
        $Path = [IO.Path]::GetDirectoryName($Path)
    }
}

function Get-OwnedPath([string]$Relative) {
    $parts = $Relative.Split('\')
    if ($parts[0] -eq 'data') { throw 'The application data directory is reserved.' }
    foreach ($part in $parts) {
        if (-not $part -or $part -match '[ .]$' -or
            $part -match '^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(\.|$)' -or
            $part.IndexOfAny([IO.Path]::GetInvalidFileNameChars()) -ge 0) {
            throw "Invalid package file name: $Relative"
        }
    }
    $path = [IO.Path]::Combine($destination, $Relative)
    Assert-OrdinaryPath $path
    return $path
}

function Remove-EmptyParents([string[]]$RelativeFiles) {
    foreach ($relative in $RelativeFiles) {
        $dir = [IO.Path]::GetDirectoryName([IO.Path]::Combine($destination, $relative))
        while ($dir -and $dir -ne $destination -and [IO.Directory]::Exists($dir)) {
            if ([IO.Directory]::GetFileSystemEntries($dir).Length) { break }
            [IO.Directory]::Delete($dir)
            $dir = [IO.Path]::GetDirectoryName($dir)
        }
    }
}

function New-PayloadDirectory([string]$Path) {
    $missing = [Collections.Generic.Stack[string]]::new()
    while (-not [IO.Directory]::Exists($Path)) {
        $missing.Push($Path)
        $Path = [IO.Path]::GetDirectoryName($Path)
        if (-not $Path) { throw 'The selected drive or share is unavailable.' }
    }
    while ($missing.Count) {
        $Path = $missing.Pop()
        [IO.Directory]::CreateDirectory($Path) | Out-Null
        $createdDirectories.Add($Path)
    }
}

if ($InstallDir -notmatch '^(?:[A-Za-z]:\\|\\\\[^?.\\][^\\]*\\[^\\]+\\)' -or
    $InstallDir.Contains('/')) {
    throw 'Choose an absolute installation directory on a drive or network share.'
}
$destination = [IO.Path]::GetFullPath($InstallDir).TrimEnd('\')
if ($destination -eq [IO.Path]::GetPathRoot($InstallDir).TrimEnd('\')) {
    throw 'Choose an application folder below the drive root.'
}
Assert-OrdinaryPath $destination
$app = Join-Path $destination 'QuailCrate.exe'
$uninstaller = Join-Path $destination 'Uninstall.exe'
$programs = [Environment]::GetFolderPath('Programs')
$shortcut = Join-Path $programs 'QuailCrate.lnk'
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$mutex = New-Object Threading.Mutex($false, "Global\QuailCrate.Setup.$sid")
$registry = [Microsoft.Win32.RegistryKey]::OpenBaseKey('CurrentUser', 'Registry64')
$held = $false
try {
    try { $held = $mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $held = $true }
    if (-not $held) { throw 'Another QuailCrate setup is running.' }
    $registered = $registry.OpenSubKey($keyName)
    $hadInstallation = $null -ne $registered
    $old = @()
    $oldValues = @{}
    if ($registered) {
        try {
            if ($registered.GetValue('InstallLocation') -ne $destination) {
                throw 'Use the registered installation folder, or uninstall the existing copy first.'
            }
            $old = $registered.GetValue('OwnedFiles')
            if (-not $old -or $old -notcontains 'QuailCrate.exe') { throw 'Invalid installed file list.' }
            foreach ($name in $registered.GetValueNames()) {
                $oldValues[$name] = @($registered.GetValueKind($name), $registered.GetValue($name))
            }
        } finally { $registered.Close() }
    } elseif (-not $installing) {
        throw 'This folder is not the registered installation.'
    }

    $previous = @{}
    foreach ($relative in $old) {
        $path = Get-OwnedPath $relative
        $previous[$relative] = $path
        if ([IO.Directory]::Exists($path)) { throw "Expected an installed file at $path" }
        if ([IO.File]::Exists($path)) {
            # In particular, the running host/worker cannot be opened for writing.
            [IO.File]::Open($path, 'Open', 'ReadWrite', 'None').Dispose()
        }
    }
    $incoming = @{}
    if ($installing) {
        foreach ($file in Get-ChildItem -LiteralPath $payload -File -Recurse -Force) {
            $relative = $file.FullName.Substring($payload.Length + 1)
            $path = Get-OwnedPath $relative
            if ([IO.Directory]::Exists($path) -or
                ([IO.File]::Exists($path) -and -not $previous.ContainsKey($relative))) {
                throw "A file or folder already occupies the package path: $path"
            }
            $incoming[$relative] = $file.FullName
        }
        if (-not $incoming.ContainsKey('Uninstall.exe') -or -not $incoming.ContainsKey('QuailCrate.exe')) {
            throw 'The installer payload is incomplete.'
        }
    }

    $shell = New-Object -ComObject WScript.Shell
    Assert-OrdinaryPath $shortcut
    $hasShortcut = [IO.File]::Exists($shortcut)
    $ownsShortcut = $hasShortcut -and $hadInstallation -and $shell.CreateShortcut($shortcut).TargetPath -eq $app
    $writeShortcut = -not $hasShortcut -or $ownsShortcut
    $suffix = '.quailcrate-' + [guid]::NewGuid().ToString('N')
    $backups = @{}
    $created = [Collections.Generic.List[string]]::new()
    $createdDirectories = [Collections.Generic.List[string]]::new()
    $registryTouched = $false
    try {
        $owned = @($previous.Values)
        if ($ownsShortcut) { $owned += $shortcut }
        foreach ($path in $owned) {
            if ([IO.File]::Exists($path)) {
                $backup = $path + $suffix
                [IO.File]::Move($path, $backup)
                $backups[$path] = $backup
            }
        }
        foreach ($relative in $incoming.Keys) {
            $path = [IO.Path]::Combine($destination, $relative)
            New-PayloadDirectory ([IO.Path]::GetDirectoryName($path))
            $temporary = $path + $suffix + '.new'
            try {
                [IO.File]::Copy($incoming[$relative], $temporary)
                [IO.File]::Move($temporary, $path)
                $created.Add($path)
            } finally { [IO.File]::Delete($temporary) }
        }
        if ($installing) {
            if ($writeShortcut) {
                [IO.Directory]::CreateDirectory($programs) | Out-Null
                $tempShortcut = Join-Path $programs ($suffix + '.lnk')
                try {
                    $link = $shell.CreateShortcut($tempShortcut)
                    $link.TargetPath = $app
                    $link.WorkingDirectory = $destination
                    $link.Save()
                    [IO.File]::Move($tempShortcut, $shortcut)
                    $created.Add($shortcut)
                } finally { [IO.File]::Delete($tempShortcut) }
            }
            $registryTouched = $true
            $key = $registry.CreateSubKey($keyName)
            try {
                $key.SetValue('DisplayName', 'QuailCrate')
                $key.SetValue('DisplayVersion', $Version)
                $key.SetValue('DisplayIcon', $app)
                $key.SetValue('Publisher', 'SuperQuail')
                $key.SetValue('InstallLocation', $destination)
                $key.SetValue('UninstallString', ('"' + $uninstaller + '"'))
                $key.SetValue('QuietUninstallString', ('"' + $uninstaller + '" /S'))
                $key.SetValue('NoModify', 1, 'DWord')
                $key.SetValue('NoRepair', 1, 'DWord')
                $key.SetValue('OwnedFiles', [string[]]$incoming.Keys, 'MultiString')
            } finally { $key.Close() }
        } else {
            $registry.DeleteSubKey($keyName)
        }
    } catch {
        foreach ($path in $created) { [IO.File]::Delete($path) }
        # An old file may have become a directory in the new payload.
        # Remove those newly created directories before restoring old files.
        $createdDirectories.Reverse()
        foreach ($dir in $createdDirectories) {
            if ([IO.Directory]::GetFileSystemEntries($dir).Length -eq 0) { [IO.Directory]::Delete($dir) }
        }
        foreach ($path in $backups.Keys) { [IO.File]::Move($backups[$path], $path) }
        if ($registryTouched) {
            $registry.DeleteSubKey($keyName, $false)
            if ($oldValues.Count) {
                $key = $registry.CreateSubKey($keyName)
                try {
                    foreach ($name in $oldValues.Keys) {
                        $key.SetValue($name, $oldValues[$name][1], $oldValues[$name][0])
                    }
                } finally { $key.Close() }
            }
        }
        throw
    }
    foreach ($backup in $backups.Values) { [IO.File]::Delete($backup) }
    Remove-EmptyParents $old
    if (-not $installing -and [IO.Directory]::Exists($destination) -and
        [IO.Directory]::GetFileSystemEntries($destination).Length -eq 0) {
        [IO.Directory]::Delete($destination)
    }
} finally {
    if ($held) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
    $registry.Close()
}
