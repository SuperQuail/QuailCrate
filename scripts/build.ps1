[CmdletBinding()]
param([switch]$TestNative, [switch]$Installer)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$build = Join-Path $root 'build'
$stage = Join-Path $build ('_release-' + [guid]::NewGuid().ToString('N'))
$dist = Join-Path $root 'dist'
$temporary = Join-Path $build 'tmp'

New-Item -Type Directory -Force -Path $stage, $dist, $temporary | Out-Null
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
if (-not $env:GOCACHE) {
    $env:GOCACHE = Join-Path $build 'go-cache'
}
$env:TMP = $temporary
$env:TEMP = $temporary

Push-Location $root
try {
    $musicWeb = Join-Path $root 'web/music'
    $freshFrontend = $Installer -or $env:CI -in 'true', '1'
    $canBuildFrontend = (Test-Path -LiteralPath (Join-Path $musicWeb 'package.json')) -and (Get-Command pnpm -ErrorAction SilentlyContinue)
    if ($freshFrontend -and -not $canBuildFrontend) {
        throw 'The release build requires pnpm and web/music/package.json.'
    }
    if ($canBuildFrontend) {
        & pnpm --dir $musicWeb install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) {
            throw "Music web frontend pnpm install failed with exit code $LASTEXITCODE."
        }
        & pnpm --dir $musicWeb build
        if ($LASTEXITCODE -ne 0) {
            throw "Music web frontend pnpm build failed with exit code $LASTEXITCODE."
        }
    }
    Copy-Item -LiteralPath (Join-Path $root 'plugins') -Destination $stage -Recurse
    Copy-Item -LiteralPath (Join-Path $root 'pluginapi') -Destination $stage -Recurse
    Copy-Item -LiteralPath (Join-Path $root 'examples') -Destination $stage -Recurse
    Copy-Item -LiteralPath (Join-Path $root 'README.md') -Destination $stage
    Copy-Item -LiteralPath (Join-Path $root 'README.CN.md') -Destination $stage
    & (Join-Path $PSScriptRoot 'build-native.ps1') -OutputDir $stage -ObjectDir $build
    $musicDll = Join-Path $stage 'plugins/music/music.dll'
    Move-Item -LiteralPath (Join-Path $stage 'music.dll') -Destination $musicDll -Force
    $musicHash = (Get-FileHash -LiteralPath $musicDll -Algorithm SHA256).Hash.ToLowerInvariant()
    $runner = Join-Path $stage 'QuailCrateNative.exe'
    $runnerHash = (Get-FileHash -LiteralPath $runner -Algorithm SHA256).Hash.ToLowerInvariant()
    $ldflags = "-H windowsgui -s -w -X main.musicDLLSHA256=$musicHash -X quailcrate/internal/plugins.nativeRunnerSHA256=$runnerHash"
    & go.exe build -mod=readonly -trimpath -ldflags $ldflags -o (Join-Path $stage 'QuailCrate.exe') ./cmd/quailcrate
    if ($LASTEXITCODE -ne 0) {
        throw "QuailCrate build failed with exit code $LASTEXITCODE."
    }
    if ($TestNative) {
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
        $bvidTestExe = Join-Path $build 'music-bvid-test.exe'
        $bvidTestSource = Join-Path $root 'internal/plugins/testdata/music_bvid_test.cpp'
        $musicInclude = Join-Path $root 'plugins/music/native'
        & cl.exe /nologo /W4 /WX /std:c++17 /EHsc /O2 /MT /utf-8 "/I$musicInclude" "/Fo$build/music-bvid-test.obj" $bvidTestSource /link /MACHINE:X64 "/OUT:$bvidTestExe"
        if ($LASTEXITCODE -ne 0) {
            throw "Music BVID vector test compilation failed with exit code $LASTEXITCODE."
        }
        $env:QUAILCRATE_NATIVE_PROBE = $probe
        $env:QUAILCRATE_NATIVE_COMPANION = $companion
        $env:QUAILCRATE_NATIVE_RUNNER = $runner
        $env:QUAILCRATE_MUSIC_DLL = $musicDll
        $env:QUAILCRATE_MUSIC_BVID_TEST = $bvidTestExe
        if ($Installer) {
            & go.exe test -mod=readonly -count=1 -v ./...
        } else {
            & go.exe test -mod=readonly -count=1 -v -p 1 -run '^Test(Native|ThirdPartyNative|Music)' ./internal/plugins ./internal/host
        }
        if ($LASTEXITCODE -ne 0) {
            throw "Go tests failed with exit code $LASTEXITCODE."
        }
    }
    $zip = Join-Path $dist 'QuailCrate-windows-amd64.zip'
    $builtZip = "$stage.zip"
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $builtZip
    if ((Get-Item -LiteralPath $builtZip).Length -le 0) { throw 'Release ZIP is empty.' }
    if ($Installer) {
        $version = $env:QEGG_VERSION
        if (-not $version) {
            $version = '0.0.0-dev'
            $revision = $env:GITHUB_SHA
            if (-not $revision -and (Get-Command git -ErrorAction SilentlyContinue)) {
                $revision = & git rev-parse --verify --quiet HEAD
            }
            if ($revision -match '^[0-9a-fA-F]{7,64}$') {
                $version += '+' + $revision.Substring(0, 7)
            }
        }
        $setup = Join-Path $dist 'QuailCrate-windows-amd64-setup.exe'
        $builtSetup = "$stage.exe"
        & (Join-Path $PSScriptRoot 'installer/build-installer.ps1') -Stage $stage -Output $builtSetup -Version $version
        if ((Get-Item -LiteralPath $builtSetup).Length -le 0) { throw 'Release installer is empty.' }
        Move-Item -LiteralPath $builtSetup -Destination $setup -Force
        Write-Output "Installer: $setup"
    }
    Move-Item -LiteralPath $builtZip -Destination $zip -Force
    Write-Output "Stage: $stage"
    Write-Output "Release ZIP: $zip"
} finally {
    Pop-Location
}
