[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Stage,
    [Parameter(Mandatory)][string]$Output,
    [Parameter(Mandatory)][string]$Version,
    [string]$MakeNSIS
)

$ErrorActionPreference = 'Stop'
$stagePath = (Get-Item -LiteralPath $Stage).FullName
$outputPath = [IO.Path]::GetFullPath($Output)
if (-not [IO.Directory]::Exists($stagePath) -or
    $outputPath.StartsWith(($stagePath.TrimEnd('\') + '\'), [StringComparison]::OrdinalIgnoreCase)) {
    throw 'The installer output must be outside the staged payload.'
}
if ($Version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$') {
    throw "Invalid release version: $Version"
}
$versionParts = $Matches[1], $Matches[2], $Matches[3]
if ($Matches[4] -match '(?:^|\.)0[0-9]+(?:\.|$)') { throw "Invalid prerelease version: $Version" }
foreach ($part in $versionParts) {
    if ([decimal]$part -gt 65535) { throw 'Windows version components must fit in 16 bits.' }
}
$fileVersion = ($versionParts -join '.') + '.0'
foreach ($required in 'QuailCrate.exe', 'QuailCrateNative.exe', 'plugins\launcher\plugin.lua') {
    if (-not [IO.File]::Exists((Join-Path $stagePath $required))) { throw "Missing staged file: $required" }
}
if ((Test-Path -LiteralPath (Join-Path $stagePath 'data')) -or
    (Test-Path -LiteralPath (Join-Path $stagePath 'Uninstall.exe'))) {
    throw 'The staged payload contains a reserved installer or user-data path.'
}
if (([IO.File]::GetAttributes($stagePath) -band [IO.FileAttributes]::ReparsePoint) -or
    (Get-ChildItem -LiteralPath $stagePath -Recurse -Force -Attributes ReparsePoint | Select-Object -First 1)) {
    throw 'The staged payload contains a reparse point.'
}
if (-not $MakeNSIS) {
    $compiler = Get-Command makensis.exe -ErrorAction SilentlyContinue
    if ($compiler) { $MakeNSIS = $compiler.Source }
    else { $MakeNSIS = Join-Path ${env:ProgramFiles(x86)} 'NSIS\makensis.exe' }
}
if (-not [IO.File]::Exists($MakeNSIS)) { throw 'NSIS 3 is required to build the Windows installer.' }

$outputDir = [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($outputPath))
$temporaryOutput = Join-Path $outputDir.FullName ('setup-' + [guid]::NewGuid().ToString('N') + '.exe')
try {
    & $MakeNSIS /V2 /WX /INPUTCHARSET UTF8 "/DSTAGE=$stagePath" "/DOUTPUT=$temporaryOutput" `
        "/DVERSION=$Version" "/DFILEVERSION=$fileVersion" (Join-Path $PSScriptRoot 'setup.nsi')
    if ($LASTEXITCODE -ne 0 -or -not [IO.File]::Exists($temporaryOutput)) {
        throw "NSIS failed to create the Windows installer (exit $LASTEXITCODE)."
    }
    Move-Item -LiteralPath $temporaryOutput -Destination $outputPath -Force
} finally {
    [IO.File]::Delete($temporaryOutput)
}
