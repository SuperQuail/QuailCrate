[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$zip = Join-Path $root 'dist/QuailCrate-windows-amd64.zip'
$setup = Get-Item -LiteralPath (Join-Path $root 'dist/QuailCrate-windows-amd64-setup.exe')
$info = $setup.VersionInfo
$version = $info.ProductVersion
if ($version -notmatch '^(\d+\.\d+\.\d+)(?:[-+].*)?$' -or
    "$($info.FileMajorPart).$($info.FileMinorPart).$($info.FileBuildPart).$($info.FilePrivatePart)" -ne ($Matches[1] + '.0')) {
    throw "Unexpected setup version resource: $version"
}
if ($env:QEGG_VERSION -and $version -cne $env:QEGG_VERSION) { throw 'Setup version differs from the planned release.' }
if (-not $env:QEGG_VERSION -and $env:GITHUB_SHA -and $version -cne ('0.0.0-dev+' + $env:GITHUB_SHA.Substring(0, 7))) {
    throw 'Preview installer version differs from the build commit.'
}
$build = [IO.Directory]::CreateDirectory((Join-Path $root 'build')).FullName
$work = Join-Path $build ('_installer-test-' + [guid]::NewGuid().ToString('N'))
$install = Join-Path $work '安装 QuailCrate'
$uninstaller = Join-Path $install 'Uninstall.exe'
$shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'QuailCrate.lnk'
$keyName = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\QuailCrate'

function Get-Registration([Microsoft.Win32.RegistryView]$View = 'Registry64') {
    $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey('CurrentUser', $View)
    try {
        $key = $base.OpenSubKey($keyName)
        if ($null -ne $key) {
            try {
                [pscustomobject]@{
                    Location = $key.GetValue('InstallLocation')
                    Version = $key.GetValue('DisplayVersion')
                    Uninstall = $key.GetValue('UninstallString')
                }
            } finally { $key.Close() }
        }
    } finally { $base.Close() }
}

function Invoke-Setup([string]$Executable, [string]$Arguments, [switch]$ExpectFailure) {
    $process = Start-Process -FilePath $Executable -ArgumentList $Arguments -WindowStyle Hidden -PassThru -Wait
    if ($ExpectFailure) {
        if ($process.ExitCode -eq 0) { throw 'Upgrade succeeded while the installed executable was locked.' }
    } elseif ($process.ExitCode -ne 0) { throw "$Executable failed with exit code $($process.ExitCode)." }
}

function Assert-Installation([string]$ExpectedVersion) {
    $registration = Get-Registration
    if ($registration.Location -ne $install -or $registration.Version -cne $ExpectedVersion -or
        $registration.Uninstall -ne ('"' + $uninstaller + '"')) {
        throw "Incorrect uninstall registration for $ExpectedVersion."
    }
    if (-not (Test-Path -LiteralPath $shortcut -PathType Leaf)) { throw 'Missing Start menu shortcut.' }
    $link = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut)
    if ($link.TargetPath -ne (Join-Path $install 'QuailCrate.exe') -or $link.WorkingDirectory -ne $install) {
        throw 'Incorrect Start menu shortcut target or working directory.'
    }
}

if ((Get-Registration) -or (Get-Registration 'Registry32') -or (Test-Path -LiteralPath $shortcut)) {
    throw 'The installer smoke test requires a user profile without an existing QuailCrate installation or shortcut.'
}

$expected = @{}
Add-Type -AssemblyName System.IO.Compression.FileSystem
$archive = [IO.Compression.ZipFile]::OpenRead($zip)
try {
    foreach ($entry in $archive.Entries) {
        $relative = $entry.FullName.Replace('/', '\')
        if ($relative.EndsWith('\')) { continue }
        $stream = $entry.Open()
        try { $expected.Add($relative, (Get-FileHash -InputStream $stream -Algorithm SHA256).Hash) }
        finally { $stream.Dispose() }
    }
} finally { $archive.Dispose() }
$requiredFiles = @('QuailCrate.exe', 'QuailCrateNative.exe', 'plugins\music\music.dll',
    'plugins\music\web\index.html', 'plugins\music\web\assets\app.js', 'plugins\music\web\assets\app.css',
    'plugins\launcher\plugin.lua', 'pluginapi\v1\quailcrate_plugin.h', 'examples\lua-widget\manifest.json')
foreach ($required in $requiredFiles) {
    if (-not $expected.ContainsKey($required)) { throw "Missing release ZIP entry: $required" }
}

$userFiles = @{
    'data\plugins\smoke-test\state.json' = '{"keep":true}'
    'plugins\music\user-note.txt' = 'Keep this user file.'
}
$retired = 'retired-component.txt'
try {
    $fixture = Join-Path $work 'fixture'
    foreach ($relative in 'QuailCrate.exe', 'QuailCrateNative.exe', 'plugins\music\music.dll', $retired) {
        $path = Join-Path $fixture $relative
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path)) | Out-Null
        [IO.File]::WriteAllText($path, "Old package: $relative")
    }
    $oldSetup = Join-Path $work 'fixture-setup.exe'
    $oldVersion = '0.0.0-smoke.fixture'
    & (Join-Path $PSScriptRoot 'installer/build-installer.ps1') -Stage $fixture -Output $oldSetup -Version $oldVersion
    # NSIS reads everything after the final /D= as the destination, including spaces.
    Invoke-Setup $oldSetup "/S /D=$install"
    Assert-Installation $oldVersion
    if (-not [IO.File]::Exists((Join-Path $install $retired))) { throw 'Missing retired fixture file.' }
    foreach ($relative in $userFiles.Keys) {
        $path = Join-Path $install $relative
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($path)) | Out-Null
        [IO.File]::WriteAllText($path, $userFiles[$relative])
    }

    $locked = [IO.File]::Open((Join-Path $install 'QuailCrate.exe'), 'Open', 'Read', 'Read')
    try { Invoke-Setup $setup.FullName '/S' -ExpectFailure } finally { $locked.Dispose() }
    Assert-Installation $oldVersion
    # With no /D override, an upgrade must find the previous installation.
    Invoke-Setup $setup.FullName '/S'
    Assert-Installation $version
    $matched = 0
    foreach ($file in Get-ChildItem -LiteralPath $install -Recurse -File -Force) {
        $relative = $file.FullName.Substring($install.Length + 1)
        if ($relative -eq 'Uninstall.exe' -or $userFiles.ContainsKey($relative)) { continue }
        if (-not $expected.ContainsKey($relative)) { throw "Unexpected file after upgrade: $relative" }
        if ((Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash -ne $expected[$relative]) {
            throw "Installed file differs from the release ZIP: $relative"
        }
        $matched++
    }
    if ($matched -ne $expected.Count) { throw "Installed $matched of $($expected.Count) release ZIP files." }
    Invoke-Setup $uninstaller '/S'
    if ((Get-Registration) -or (Test-Path -LiteralPath $shortcut)) {
        throw 'Uninstall left a registry entry or Start menu shortcut.'
    }
    foreach ($relative in $userFiles.Keys) {
        if ([IO.File]::ReadAllText((Join-Path $install $relative)) -cne $userFiles[$relative]) {
            throw "User content changed: $relative"
        }
    }
    $remaining = @(Get-ChildItem -LiteralPath $install -Recurse -File -Force)
    if ($remaining.Count -ne $userFiles.Count) { throw 'Uninstall left unexpected application files.' }
    Write-Output "Installer smoke test passed for $version. Matched $matched release ZIP files."
} finally {
    # Clean up only an installation created by this test. Keep a failed uninstall available for diagnosis.
    if ((Get-Registration).Location -eq $install -and [IO.File]::Exists($uninstaller)) {
        Invoke-Setup $uninstaller '/S'
    }
    if ((Get-Registration).Location -ne $install -and [IO.Directory]::Exists($work)) {
        $workPath = (Resolve-Path -LiteralPath $work).ProviderPath
        if ([IO.Path]::GetDirectoryName($workPath) -ne $build -or
            [IO.Path]::GetFileName($workPath) -notmatch '^_installer-test-[0-9a-f]{32}$') {
            throw "Unexpected smoke test cleanup location: $workPath"
        }
        Remove-Item -LiteralPath $workPath -Recurse -Force
    }
}
