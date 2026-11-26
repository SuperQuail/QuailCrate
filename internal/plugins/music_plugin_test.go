package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

var (
	testMusicNativeOnce sync.Once
	testMusicDLLPath    string
	testMusicRunnerPath string
	testMusicBuildErr   error
)

func ensureTestMusicNative(t *testing.T) (dllPath, runnerPath string) {
	t.Helper()
	if dll := os.Getenv("QUAILCRATE_MUSIC_DLL"); dll != "" {
		if runner := os.Getenv("QUAILCRATE_NATIVE_RUNNER"); runner != "" {
			return dll, runner
		}
	}
	testMusicNativeOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			testMusicBuildErr = err
			return
		}
		outDir := filepath.Join(root, "build", "test-native-plugins")
		if err := os.MkdirAll(outDir, 0700); err != nil {
			testMusicBuildErr = err
			return
		}
		script := filepath.Join(root, "scripts", "build-native.ps1")
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", script, "-OutputDir", outDir, "-ObjectDir", outDir)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			testMusicBuildErr = fmt.Errorf("build native music plugin: %w\n%s", err, strings.TrimSpace(string(out)))
			return
		}
		testMusicDLLPath = filepath.Join(outDir, "music.dll")
		testMusicRunnerPath = filepath.Join(outDir, "QuailCrateNative.exe")
		_ = os.Setenv("QUAILCRATE_NATIVE_RUNNER", testMusicRunnerPath)
	})
	if testMusicBuildErr != nil {
		t.Fatalf("failed to build music.dll for tests: %v", testMusicBuildErr)
	}
	return testMusicDLLPath, testMusicRunnerPath
}

func stageMusicPackage(t *testing.T) Package {
	t.Helper()
	dllPath, _ := ensureTestMusicNative(t)
	srcDir := filepath.Join("..", "..", "plugins", "music")
	bundleRoot := t.TempDir()
	dstDir := filepath.Join(bundleRoot, "music")
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0600)
	})
	if err != nil {
		t.Fatalf("copy music plugin: %v", err)
	}
	image, err := os.ReadFile(dllPath)
	if err != nil {
		t.Fatalf("read music.dll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "music.dll"), image, 0600); err != nil {
		t.Fatalf("write music.dll: %v", err)
	}
	sum := sha256.Sum256(image)
	digest := hex.EncodeToString(sum[:])
	pkg := (Catalog{TrustedDLLs: map[string]string{MusicDLLID: digest}}).readPackage(bundleRoot, "music", true)
	if pkg.Err != nil {
		t.Fatalf("music package invalid: %v", pkg.Err)
	}
	pkg.DataDir = t.TempDir()
	return pkg
}

func TestMusicPluginManifestInitAndMiniPlayer(t *testing.T) {
	pkg := stageMusicPackage(t)
	if pkg.Manifest.ID != MusicDLLID || pkg.Manifest.SchemaVersion != 2 || pkg.Manifest.HostAPIVersion != 2 || pkg.Manifest.UIVersion != 2 {
		t.Fatalf("unexpected manifest header: %+v", pkg.Manifest)
	}
	if pkg.Manifest.Runtime != "dll" || pkg.Manifest.Entry != "music.dll" || pkg.Manifest.Platform != "windows/amd64" || pkg.Manifest.ABIVersion != v1.ABIVersionV2 {
		t.Fatalf("unexpected native manifest fields: %+v", pkg.Manifest)
	}
	if !slices.Equal(pkg.Manifest.Permissions, []string{}) {
		t.Fatalf("permissions = %v, want empty (self-contained native DLL)", pkg.Manifest.Permissions)
	}

	for _, tc := range []struct {
		locale   string
		tabTitle string
		widget   string
	}{
		{"zh-CN", "音乐库", "迷你播放器"},
		{"en-US", "Music Library", "Mini Player"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			mgr := NewManager()
			ctx := context.Background()
			init, err := mgr.Enable(ctx, pkg, nil, v1.Payload{Locale: tc.locale})
			if err != nil {
				t.Fatalf("enable music plugin (%s): %v", tc.locale, err)
			}
			t.Cleanup(func() { _ = mgr.Disable(ctx, pkg.Manifest.ID) })

			if len(init.Data.AppTabs) != 1 || init.Data.AppTabs[0].ID != "library" ||
				init.Data.AppTabs[0].Title != tc.tabTitle || init.Data.AppTabs[0].Page != "web/index.html" ||
				len(init.Data.AppTabs[0].PageActions) < 20 {
				t.Fatalf("unexpected appTabs: %+v", init.Data.AppTabs)
			}
			if len(init.Data.Widgets) != 1 || init.Data.Widgets[0].ID != "mini-player" ||
				init.Data.Widgets[0].Title != tc.widget || !init.Data.Widgets[0].Default {
				t.Fatalf("unexpected widgets: %+v", init.Data.Widgets)
			}
			if len(init.Data.Commands) < 5 || len(init.Data.Tray) < 4 {
				t.Fatalf("unexpected commands/tray: commands=%d tray=%d", len(init.Data.Commands), len(init.Data.Tray))
			}

			tabRes, err := mgr.CallGeneration(ctx, pkg.Manifest.ID, init.Generation, "tab_state", v1.Payload{TabID: "library", Locale: tc.locale})
			if err != nil {
				t.Fatalf("tab_state: %v", err)
			}
			if len(tabRes.Data.PageState) == 0 {
				t.Fatal("tab_state returned empty pageState")
			}
			if !v2.PageObject(tabRes.Data.PageState, v2.MaxPageStateBytes) {
				t.Fatalf("invalid pageState: %s", tabRes.Data.PageState)
			}

			widgetRes, err := mgr.CallGeneration(ctx, pkg.Manifest.ID, init.Generation, "widget_state", v1.Payload{WidgetID: "mini-player", WidgetType: "mini-player", Locale: tc.locale})
			if err != nil {
				t.Fatalf("widget_state: %v", err)
			}
			view := widgetRes.Data.View
			if err := validateView(view); err != nil {
				t.Fatalf("mini-player view invalid: %v", err)
			}
			if !view.AllowsInput("seek", json.RawMessage("0.42")) {
				t.Fatal("mini-player slider did not accept valid normalized ratio 0.42")
			}
			if view.AllowsInput("seek", json.RawMessage("1.5")) || view.AllowsInput("seek", json.RawMessage(`"bad"`)) {
				t.Fatal("mini-player slider accepted out-of-range or non-numeric input")
			}
			if !view.AllowsTabAction("play_pause", "") || !view.AllowsTabAction("next", "") || !view.AllowsTabAction("prev", "") {
				t.Fatal("mini-player missing transport actions")
			}
		})
	}
}

func buildMusicTestExecutable(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	exe := filepath.Join(outDir, name+".exe")
	const psCmd = `
$ErrorActionPreference = 'Stop'
if (-not (Get-Command cl.exe -ErrorAction SilentlyContinue) -or ($env:VSCMD_ARG_TGT_ARCH -and $env:VSCMD_ARG_TGT_ARCH -notin 'x64','amd64')) {
  $locator = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
  $inst = & $locator -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
  & (Join-Path $inst 'Common7/Tools/Launch-VsDevShell.ps1') -Arch amd64 -HostArch amd64 -SkipAutomaticLocation | Out-Null
}
$name = $env:QUAILCRATE_TEST_NAME
$output = Join-Path $env:QUAILCRATE_TEST_OUTPUT $name
& cl.exe /nologo /W4 /WX /std:c++17 /EHsc /O2 /MT /utf-8 /Iplugins/music/native "/Fo$output.obj" "internal/plugins/testdata/$name.cpp" /link /MACHINE:X64 "/OUT:$output.exe"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
`
	build := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psCmd)
	build.Dir = root
	build.Env = append(build.Environ(), "QUAILCRATE_TEST_NAME="+name, "QUAILCRATE_TEST_OUTPUT="+outDir)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile %s.cpp: %v\n%s", name, err, strings.TrimSpace(string(out)))
	}
	return exe
}

func TestMusicPluginMD5BVAndWBIVectors(t *testing.T) {
	exe := os.Getenv("QUAILCRATE_MUSIC_BVID_TEST")
	if exe == "" {
		exe = buildMusicTestExecutable(t, "music_bvid_test")
	}
	out, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("music_bvid_test failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}

func TestMusicDownloadedAACContinuesDuringProgressCallback(t *testing.T) {
	exe := buildMusicTestExecutable(t, "music_aac_playback_test")
	fixture, err := filepath.Abs(filepath.Join("..", "pluginaudio", "testdata", "silence-fragmented.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(exe, fixture).CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 77 {
		t.Skip("Windows has no audio output device")
	}
	if err != nil {
		t.Fatalf("AAC playback test: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestMusicDownloadedPlaybackDuringFeederStall(t *testing.T) {
	exe := buildMusicTestExecutable(t, "music_playback_stall_test")
	fixture := filepath.Join(t.TempDir(), "local.wav")
	if err := os.WriteFile(fixture, makeTestWAV(3), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, path, mode string) {
		t.Helper()
		out, err := exec.Command(exe, path, mode).CombinedOutput()
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 77 {
			t.Skip("Windows has no available audio output device")
		}
		if err != nil {
			t.Fatalf("local playback: %v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	t.Run("device volume", func(t *testing.T) { run(t, fixture, "device") })
	t.Run("software volume", func(t *testing.T) { run(t, fixture, "software") })
	t.Run("restart error WAV", func(t *testing.T) { run(t, fixture, "fail-restart") })
	t.Run("restart error AAC", func(t *testing.T) {
		aac, err := filepath.Abs(filepath.Join("..", "pluginaudio", "testdata", "silence-fragmented.m4a"))
		if err != nil {
			t.Fatal(err)
		}
		run(t, aac, "fail-restart")
	})
}

func TestMusicPluginA1FixturesValid(t *testing.T) {
	fixturesDir := filepath.Join("..", "..", "plugins", "music", "fixtures")
	for _, name := range []string{"snapshot-success.json", "snapshot-empty.json", "snapshot-error.json"} {
		raw, err := os.ReadFile(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !v2.PageObject(raw, v2.MaxPageStateBytes) {
			t.Fatalf("fixture %s failed PageObject validation", name)
		}
	}
	msgRaw, err := os.ReadFile(filepath.Join(fixturesDir, "messages-sample.json"))
	if err != nil {
		t.Fatalf("read messages-sample.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(msgRaw, &parsed); err != nil {
		t.Fatalf("messages-sample.json invalid JSON: %v", err)
	}
	for _, key := range []string{"pageToLua", "luaToPageReply", "hostToLua", "nativeControlToLua"} {
		if _, ok := parsed[key]; !ok {
			t.Fatalf("messages-sample.json missing section %q", key)
		}
	}
}
