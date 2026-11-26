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

function Get-ShellLinkInterface {
    $type = 'QuailCrate.IShellLinkW' -as [type]
    if ($type) { return $type }
    $assembly = [Reflection.Emit.AssemblyBuilder]::DefineDynamicAssembly(
        (New-Object Reflection.AssemblyName 'QuailCrateShellLink'),
        [Reflection.Emit.AssemblyBuilderAccess]::Run
    )
    $module = $assembly.DefineDynamicModule('QuailCrateShellLink')
    $interface = $module.DefineType(
        'QuailCrate.IShellLinkW',
        [Reflection.TypeAttributes]::Public -bor [Reflection.TypeAttributes]::Interface -bor
            [Reflection.TypeAttributes]::Abstract -bor [Reflection.TypeAttributes]::Import
    )
    $interface.SetCustomAttribute((New-Object Reflection.Emit.CustomAttributeBuilder(
        [Runtime.InteropServices.ComImportAttribute].GetConstructor([type[]]@()), @()
    )))
    $interface.SetCustomAttribute((New-Object Reflection.Emit.CustomAttributeBuilder(
        [Runtime.InteropServices.GuidAttribute].GetConstructor([type[]]@([string])),
        @('000214F9-0000-0000-C000-000000000046')
    )))
    $interface.SetCustomAttribute((New-Object Reflection.Emit.CustomAttributeBuilder(
        [Runtime.InteropServices.InterfaceTypeAttribute].GetConstructor([type[]]@([Runtime.InteropServices.ComInterfaceType])),
        @([Runtime.InteropServices.ComInterfaceType]::InterfaceIsIUnknown)
    )))
    $lpwstr = New-Object Reflection.Emit.CustomAttributeBuilder(
        [Runtime.InteropServices.MarshalAsAttribute].GetConstructor([type[]]@([Runtime.InteropServices.UnmanagedType])),
        @([Runtime.InteropServices.UnmanagedType]::LPWStr)
    )
    $methodAttrs = [Reflection.MethodAttributes]::Public -bor [Reflection.MethodAttributes]::Abstract -bor
        [Reflection.MethodAttributes]::Virtual -bor [Reflection.MethodAttributes]::HideBySig -bor
        [Reflection.MethodAttributes]::NewSlot
    $slots = @(
        @('GetPath',             @([Text.StringBuilder], [int], [IntPtr], [uint32]), $true,  $true),
        @('GetIDList',           @([IntPtr].MakeByRefType()),                        $false, $false),
        @('SetIDList',           @([IntPtr]),                                        $false, $false),
        @('GetDescription',      @([Text.StringBuilder], [int]),                     $true,  $true),
        @('SetDescription',      @([string]),                                        $false, $true),
        @('GetWorkingDirectory', @([Text.StringBuilder], [int]),                     $true,  $true),
        @('SetWorkingDirectory', @([string]),                                        $false, $true),
        @('GetArguments',        @([Text.StringBuilder], [int]),                     $true,  $true),
        @('SetArguments',        @([string]),                                        $false, $true),
        @('GetHotkey',           @([int16].MakeByRefType()),                         $false, $false),
        @('SetHotkey',           @([int16]),                                         $false, $false),
        @('GetShowCmd',          @([int].MakeByRefType()),                           $false, $false),
        @('SetShowCmd',          @([int]),                                           $false, $false),
        @('GetIconLocation',     @([Text.StringBuilder], [int], [int].MakeByRefType()), $true, $true),
        @('SetIconLocation',     @([string], [int]),                                 $false, $true),
        @('SetRelativePath',     @([string], [uint32]),                              $false, $true),
        @('Resolve',             @([IntPtr], [uint32]),                              $false, $false),
        @('SetPath',             @([string]),                                        $false, $true)
    )
    foreach ($slot in $slots) {
        $method = $interface.DefineMethod($slot[0], $methodAttrs, [void], [type[]]$slot[1])
        if ($slot[3]) {
            $paramAttr = if ($slot[2]) { [Reflection.ParameterAttributes]::Out } else { [Reflection.ParameterAttributes]::In }
            $param = $method.DefineParameter(1, $paramAttr, 'p1')
            $param.SetCustomAttribute($lpwstr)
        }
    }
    return $interface.CreateType()
}

function Get-ShortcutTarget([string]$Path) {
    $iface = Get-ShellLinkInterface
    $persist = [Runtime.InteropServices.ComTypes.IPersistFile]
    $com = [Activator]::CreateInstance([Type]::GetTypeFromCLSID([Guid]'00021401-0000-0000-C000-000000000046'))
    try {
        $persist.GetMethod('Load').Invoke($com, [object[]]@($Path, 0))
        $target = [Text.StringBuilder]::new(32768)
        $iface.GetMethod('GetPath').Invoke($com, [object[]]@($target, $target.Capacity, [IntPtr]::Zero, [uint32]0))
        return $target.ToString()
    } finally {
        [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($com)
    }
}

function Save-Shortcut([string]$Path, [string]$TargetPath, [string]$WorkingDirectory) {
    $iface = Get-ShellLinkInterface
    $persist = [Runtime.InteropServices.ComTypes.IPersistFile]
    $com = [Activator]::CreateInstance([Type]::GetTypeFromCLSID([Guid]'00021401-0000-0000-C000-000000000046'))
    try {
        $iface.GetMethod('SetPath').Invoke($com, [object[]]@($TargetPath))
        $iface.GetMethod('SetWorkingDirectory').Invoke($com, [object[]]@($WorkingDirectory))
        $persist.GetMethod('Save').Invoke($com, [object[]]@($Path, $false))
    } finally {
        [void][Runtime.InteropServices.Marshal]::FinalReleaseComObject($com)
    }
}

function Write-SetupError($ErrorRecord) {
    if (-not $env:QUAILCRATE_SETUP_LOG) { return }
    try {
        $logPath = [IO.Path]::GetFullPath($env:QUAILCRATE_SETUP_LOG)
        Assert-OrdinaryPath $logPath
        [IO.File]::WriteAllText($logPath, ($ErrorRecord | Out-String), [Text.UTF8Encoding]::new($false))
    } catch {}
}

try {
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

    Assert-OrdinaryPath $shortcut
    $hasShortcut = [IO.File]::Exists($shortcut)
    $ownsShortcut = $false
    if ($hasShortcut -and $hadInstallation) {
        try {
            $ownsShortcut = (Get-ShortcutTarget $shortcut) -eq $app
        } catch {
            $ex = $_.Exception
            if ($ex -isnot [Runtime.InteropServices.COMException] -and
                $ex.InnerException -isnot [Runtime.InteropServices.COMException]) { throw }
        }
    }
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
                    Save-Shortcut $tempShortcut $app $destination
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
} catch {
    Write-SetupError $_
    throw
}
