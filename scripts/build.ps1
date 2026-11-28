[CmdletBinding()]
param([switch]$TestNative, [switch]$Installer, [switch]$VerifyOnly, [switch]$SignedRelease)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$build = Join-Path $root 'build'
$stage = Join-Path $build ('_release-' + [guid]::NewGuid().ToString('N'))
$dist = Join-Path $root 'dist'
$temporary = Join-Path $build 'tmp'

$version = $env:QEGG_VERSION
if (-not $version) {
    $version = '0.0.0-dev'
    $revision = $env:GITHUB_SHA
    if (-not $revision -and (Get-Command git -ErrorAction SilentlyContinue)) {
        $revision = & git -C $root rev-parse --verify --quiet HEAD
    }
    if ($revision -match '^[0-9a-fA-F]{7,64}$') {
        $version += '+' + $revision.Substring(0, 7)
    }
}
if ($version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$') {
    throw 'Invalid QEGG_VERSION.'
}
$signingKey = $env:QUAILCRATE_UPDATE_SIGNING_KEY
if ($SignedRelease -and (-not $Installer -or $VerifyOnly -or -not $env:QEGG_VERSION -or -not $signingKey)) {
    throw 'A signed release requires -Installer, QEGG_VERSION, and QUAILCRATE_UPDATE_SIGNING_KEY.'
}

New-Item -Type Directory -Force -Path $stage, $temporary | Out-Null
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
if (-not $env:GOCACHE) {
    $env:GOCACHE = Join-Path $build 'go-cache'
}
$env:TMP = $temporary
$env:TEMP = $temporary

Push-Location $root
# Only the signing executable receives key material. Package builds and tests run without it.
Remove-Item Env:QUAILCRATE_UPDATE_SIGNING_KEY -ErrorAction SilentlyContinue
try {
    $publicKey = ''
    if ($SignedRelease) {
        $signer = "$stage-sign.exe"
        & go.exe build -mod=readonly -trimpath -o $signer ./cmd/quailcrate-update-release
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the update signing tool.' }
        $publicKey = $signingKey | & $signer public-key
        if ($LASTEXITCODE -ne 0 -or $publicKey -cnotmatch '^[A-Za-z0-9+/]{43}=$') {
            throw 'Could not derive the update signing public key.'
        }
    }
    & (Join-Path $PSScriptRoot 'build-native.ps1') -OutputDir $stage
    $runner = Join-Path $stage 'QuailCrateNative.exe'
    if (-not $VerifyOnly) {
        Copy-Item -LiteralPath (Join-Path $root 'plugins') -Destination $stage -Recurse
        Copy-Item -LiteralPath (Join-Path $root 'pluginapi') -Destination $stage -Recurse
        Copy-Item -LiteralPath (Join-Path $root 'examples') -Destination $stage -Recurse
        Copy-Item -LiteralPath (Join-Path $root 'README.md') -Destination $stage
        Copy-Item -LiteralPath (Join-Path $root 'README.CN.md') -Destination $stage
        $runnerHash = (Get-FileHash -LiteralPath $runner -Algorithm SHA256).Hash.ToLowerInvariant()
        $commonFlags = "-H windowsgui -s -w -X main.releaseVersion=$version -X main.releaseUpdatePublicKey=$publicKey"
        $helper = Join-Path $stage 'QuailCrateUpdate.exe'
        & go.exe build -mod=readonly -trimpath -ldflags $commonFlags -o $helper ./cmd/quailcrate-update
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the QuailCrate update helper.' }
        $helperHash = (Get-FileHash -LiteralPath $helper -Algorithm SHA256).Hash.ToLowerInvariant()
        $ldflags = "$commonFlags -X main.releaseUpdateHelperSHA256=$helperHash -X quailcrate/internal/plugins.nativeRunnerSHA256=$runnerHash"
        & go.exe build -mod=readonly -trimpath -ldflags $ldflags -o (Join-Path $stage 'QuailCrate.exe') ./cmd/quailcrate
        if ($LASTEXITCODE -ne 0) {
            throw "QuailCrate build failed with exit code $LASTEXITCODE."
        }
    }
    if ($TestNative -or $VerifyOnly) {
        if (-not (Get-Command cl.exe -ErrorAction SilentlyContinue) -or
            ($env:VSCMD_ARG_TGT_ARCH -and $env:VSCMD_ARG_TGT_ARCH -notin 'x64', 'amd64')) {
            $locator = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
            if (-not (Test-Path -LiteralPath $locator)) {
                throw 'Visual Studio 2022 with C++ x64 build tools is required for native tests.'
            }
            $installation = & $locator -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
            if (-not $installation) {
                throw 'Visual Studio C++ x64 build tools were not found.'
            }
            $developerShell = Join-Path $installation 'Common7/Tools/Launch-VsDevShell.ps1'
            & $developerShell -Arch amd64 -HostArch amd64 -SkipAutomaticLocation
        }
        $probe = Join-Path $build 'native-probe.dll'
        $companion = Join-Path $build 'native-companion.dll'
        $include = Join-Path $root 'pluginapi/v1'
        $probeSource = Join-Path $root 'internal/plugins/testdata/native_probe.c'
        $companionSource = Join-Path $root 'internal/plugins/testdata/native_companion.c'
        & cl.exe /nologo /W4 /WX /O2 /LD /MT /utf-8 "/I$include" "/Fo$build/native-probe.obj" $probeSource /link /MACHINE:X64 "/OUT:$probe" "/IMPLIB:$build/native-probe.lib"
        if ($LASTEXITCODE -ne 0) {
            throw "Native probe fixture compilation failed with exit code $LASTEXITCODE."
        }
        & cl.exe /nologo /W4 /WX /O2 /LD /MT /utf-8 "/Fo$build/native-companion.obj" $companionSource /link /MACHINE:X64 "/OUT:$companion" "/IMPLIB:$build/native-companion.lib"
        if ($LASTEXITCODE -ne 0) {
            throw "Native companion fixture compilation failed with exit code $LASTEXITCODE."
        }
        $env:QUAILCRATE_NATIVE_PROBE = $probe
        $env:QUAILCRATE_NATIVE_COMPANION = $companion
        $env:QUAILCRATE_NATIVE_RUNNER = $runner
        $testArgs = @('./...')
        if (-not ($Installer -or $VerifyOnly)) {
            $testArgs = @('-p', '1', '-run', '^Test(Native|ThirdPartyNative)', './internal/plugins', './internal/host')
        }
        # QuailEgg captures command output; keep the original assertions available on failure.
        $logDirectory = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $build }
        $testLog = Join-Path $logDirectory 'quailcrate-go-test.log'
        & go.exe test -mod=readonly -count=1 -v @testArgs 2>&1 | Tee-Object -FilePath $testLog
        if ($LASTEXITCODE -ne 0) {
            $testExit = $LASTEXITCODE
            if ($env:GITHUB_STEP_SUMMARY) {
                Add-Content -LiteralPath $env:GITHUB_STEP_SUMMARY -Encoding UTF8 -Value '### Go test output',''
                Get-Content -LiteralPath $testLog | ForEach-Object { '    ' + $_ } |
                    Add-Content -LiteralPath $env:GITHUB_STEP_SUMMARY -Encoding UTF8
            }
            $failed = Select-String -LiteralPath $testLog -Pattern '^\s*--- FAIL:|^FAIL\s+\S' |
                Select-Object -First 10 -ExpandProperty Line
            throw ("Go tests failed with exit code $testExit. See $testLog." + [Environment]::NewLine + ($failed -join [Environment]::NewLine))
        }
    }
    if ($VerifyOnly) { return }
    if ($SignedRelease) {
        $signingKey | & $signer portable -stage $stage -version $version
        if ($LASTEXITCODE -ne 0) { throw 'Could not sign the portable file inventory.' }
    }
    New-Item -Type Directory -Force -Path $dist | Out-Null
    $zip = Join-Path $dist 'QuailCrate-windows-amd64.zip'
    $builtZip = "$stage.zip"
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $builtZip
    if ((Get-Item -LiteralPath $builtZip).Length -le 0) { throw 'Release ZIP is empty.' }
    if ($Installer) {
        $setup = Join-Path $dist 'QuailCrate-windows-amd64-setup.exe'
        $builtSetup = "$stage.exe"
        & (Join-Path $PSScriptRoot 'installer/build-installer.ps1') -Stage $stage -Output $builtSetup -Version $version
        if ((Get-Item -LiteralPath $builtSetup).Length -le 0) { throw 'Release installer is empty.' }
        if ($SignedRelease) {
            $metadata = New-Item -Type Directory -Path "$stage-metadata"
            $signingKey | & $signer release -version $version -setup $builtSetup -zip $builtZip -out $metadata.FullName
            if ($LASTEXITCODE -ne 0) { throw 'Could not sign the release update manifest.' }
        }
        Move-Item -LiteralPath $builtSetup -Destination $setup -Force
        Write-Output "Installer: $setup"
    }
    Move-Item -LiteralPath $builtZip -Destination $zip -Force
    foreach ($name in 'update-manifest.json', 'update-manifest.sig') {
        $destination = Join-Path $dist $name
        if ($SignedRelease) { Move-Item -LiteralPath (Join-Path $metadata.FullName $name) -Destination $destination -Force }
        else { [IO.File]::Delete($destination) }
    }
    Write-Output "Stage: $stage"
    Write-Output "Release ZIP: $zip"
} finally {
    if ($null -ne $signingKey) { $env:QUAILCRATE_UPDATE_SIGNING_KEY = $signingKey }
    Pop-Location
}
