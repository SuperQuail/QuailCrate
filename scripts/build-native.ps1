[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$OutputDir,
    [string]$ObjectDir
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $ObjectDir) {
    $ObjectDir = $OutputDir
}
foreach ($directory in @($OutputDir, $ObjectDir)) {
    if ($directory -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2})' -or -not [IO.Directory]::Exists($directory)) {
        throw "Expected an existing absolute build directory: $directory"
    }
}
if (-not (Get-Command go.exe -ErrorAction SilentlyContinue)) {
    throw 'Go 1.27 or later is required.'
}
if (-not (Get-Command cl.exe -ErrorAction SilentlyContinue) -or
    ($env:VSCMD_ARG_TGT_ARCH -and $env:VSCMD_ARG_TGT_ARCH -notin 'x64', 'amd64')) {
    $locator = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
    if (-not (Test-Path -LiteralPath $locator)) {
        throw 'Visual Studio 2022 with the C++ x64 build tools is required.'
    }
    $installation = & $locator -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
    if (-not $installation) {
        throw 'Visual Studio C++ x64 build tools were not found.'
    }
    $developerShell = Join-Path $installation 'Common7/Tools/Launch-VsDevShell.ps1'
    & $developerShell -Arch amd64 -HostArch amd64 -SkipAutomaticLocation
}

Push-Location -LiteralPath $root
try {
    $include = Join-Path $root 'pluginapi/v1'

    $musicSource = Join-Path $root 'plugins/music/native/music_plugin.cpp'
    $musicInclude = Join-Path $root 'plugins/music/native'
    $musicObject = Join-Path $ObjectDir 'music.obj'
    $musicLib = Join-Path $ObjectDir 'music.lib'
    $musicDll = Join-Path $OutputDir 'music.dll'
    & cl.exe /nologo /W4 /WX /std:c++17 /EHsc /O2 /LD /MT /utf-8 "/I$include" "/I$musicInclude" "/Fo$musicObject" $musicSource /link /MACHINE:X64 "/OUT:$musicDll" "/IMPLIB:$musicLib" winhttp.lib crypt32.lib mfplat.lib mfreadwrite.lib mfuuid.lib ole32.lib winmm.lib
    if ($LASTEXITCODE -ne 0) {
        throw "Native music plugin compilation failed with exit code $LASTEXITCODE."
    }

    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $runner = Join-Path $OutputDir 'QuailCrateNative.exe'
    & go.exe build -mod=readonly -trimpath -ldflags '-s -w' -o $runner ./cmd/quailcrate-native
    if ($LASTEXITCODE -ne 0) {
        throw "Native runner compilation failed with exit code $LASTEXITCODE."
    }
} finally {
    Pop-Location
}
