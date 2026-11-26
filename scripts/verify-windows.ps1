[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'build.ps1') -TestNative -Installer
& (Join-Path $PSScriptRoot 'test-installer.ps1')
