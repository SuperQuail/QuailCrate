[CmdletBinding()]
param([switch]$SignedRelease)

$ErrorActionPreference = 'Stop'
$signingKey = $env:QUAILCRATE_UPDATE_SIGNING_KEY
try {
    & (Join-Path $PSScriptRoot 'build.ps1') -TestNative -Installer -SignedRelease:$SignedRelease
    Remove-Item Env:QUAILCRATE_UPDATE_SIGNING_KEY -ErrorAction SilentlyContinue
    & (Join-Path $PSScriptRoot 'test-installer.ps1')
} finally {
    if ($null -ne $signingKey) { $env:QUAILCRATE_UPDATE_SIGNING_KEY = $signingKey }
}
