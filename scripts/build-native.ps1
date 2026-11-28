[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDir)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if ($OutputDir -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2})' -or -not [IO.Directory]::Exists($OutputDir)) {
    throw "Expected an existing absolute build directory: $OutputDir"
}
if (-not (Get-Command go.exe -ErrorAction SilentlyContinue)) {
    throw 'Go 1.27 or later is required.'
}
Push-Location -LiteralPath $root
try {
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
