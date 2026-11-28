#Requires -Version 7.0
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repo = 'SuperQuail/QuailCrate'
$secretName = 'QUAILCRATE_UPDATE_SIGNING_KEY'

if (-not $IsWindows) { throw 'Run this script using PowerShell 7 on Windows.' }
$gh = (Get-Command gh.exe -CommandType Application -ErrorAction Stop).Path
$keyDirectory = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.quailcrate'
$keyPath = Join-Path $keyDirectory 'update-signing-key.b64'
$sourceRoot = Split-Path -Parent $PSScriptRoot
if ([IO.Path]::GetFullPath($keyPath).StartsWith(
    [IO.Path]::GetFullPath($sourceRoot).TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'The signing key must be stored outside the source repository.'
}

function Assert-SigningSecretAbsent {
    $names = & $gh secret list --repo $repo --app actions --json name
    if ($LASTEXITCODE -ne 0) {
        throw "Could not list Actions secrets for $repo. Check your access with gh auth status."
    }
    foreach ($entry in (ConvertFrom-Json -InputObject ($names -join "`n"))) {
        if ($entry.name -eq $secretName) {
            throw "$secretName already exists in $repo. Keep using the original signing key."
        }
    }
}

function New-SigningKeyFile([string]$Directory, [string]$Destination) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    try {
        $acl = [Security.AccessControl.FileSecurity]::new()
        $acl.SetAccessRuleProtection($true, $false)
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
            $identity.User, [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.AccessControlType]::Allow))
    } finally { $identity.Dispose() }

    $temporary = Join-Path $Directory ('.signing-key-' + [Guid]::NewGuid().ToString('N'))
    $created = $false
    $seed = [byte[]]::new(32)
    $encoded = $null
    try {
        $random = [Security.Cryptography.RandomNumberGenerator]::Create()
        try { $random.GetBytes($seed) } finally { $random.Dispose() }
        $encoded = [Text.Encoding]::ASCII.GetBytes([Convert]::ToBase64String($seed))

        $file = [IO.FileSystemAclExtensions]::Create(
            [IO.FileInfo]::new($temporary), [IO.FileMode]::CreateNew,
            [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::None,
            4096, [IO.FileOptions]::None, $acl)
        $created = $true
        try {
            $file.Write($encoded, 0, $encoded.Length)
            $file.Flush($true)
        } finally { $file.Dispose() }
        [IO.File]::Move($temporary, $Destination)
    } finally {
        [Array]::Clear($seed, 0, $seed.Length)
        if ($null -ne $encoded) { [Array]::Clear($encoded, 0, $encoded.Length) }
        if ($created) { [IO.File]::Delete($temporary) }
    }
}

Assert-SigningSecretAbsent
$directory = [IO.Directory]::CreateDirectory($keyDirectory)
if ($directory.Attributes -band [IO.FileAttributes]::ReparsePoint) {
    throw "The key directory must be an ordinary directory. Found $keyDirectory"
}
if (-not (Test-Path -LiteralPath $keyPath)) {
    New-SigningKeyFile $keyDirectory $keyPath
}

$keyFile = Get-Item -LiteralPath $keyPath -Force
if ($keyFile.PSIsContainer -or ($keyFile.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
    throw "The signing key must be an ordinary file. Found $keyPath"
}
$signingSeed = [IO.File]::ReadAllText($keyPath).Trim()
if ($signingSeed -cnotmatch '^[A-Za-z0-9+/]{43}=$') {
    throw "The existing key file does not contain a Base64-encoded 32-byte seed. Check $keyPath"
}

Write-Host "Signing key file: $keyPath"
Assert-SigningSecretAbsent
try {
    $signingSeed | & $gh secret set $secretName --repo $repo --app actions
    if ($LASTEXITCODE -ne 0) {
        throw "GitHub upload failed. The key is saved at $keyPath and will be reused on retry."
    }
} finally { Remove-Variable signingSeed }
Write-Host "Configured $secretName in GitHub Actions for $repo."
