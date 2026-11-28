//go:build interactive

package gioui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"quailcrate/internal/theme"
	v2 "quailcrate/pluginapi/v2"
)

func TestSettingsDrawsLocalizedText(t *testing.T) {
	size := image.Pt(780, 480)
	window, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skipf("offscreen renderer unavailable: %v", err)
	}
	defer window.Release()
	var router input.Router
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(size), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	view := NewSettingsView()
	view.ShowPlugins()
	var previous []byte
	for _, locale := range []string{"zh-CN", "en-US"} {
		gtx.Reset()
		view.Layout(gtx, SettingsModel{Locale: locale}, nil)
		if err := window.Frame(gtx.Ops); err != nil {
			t.Fatal(err)
		}
		router.Frame(gtx.Ops)
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := window.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		// Sample background from top-right corner and count high-contrast title glyph pixels.
		bg := img.RGBAAt(size.X-5, 5)
		var glyphs []byte
		ink := 0
		for y := 0; y < 56; y++ {
			for x := 170; x < 350; x++ {
				c := img.RGBAAt(x, y)
				glyphs = append(glyphs, c.G)
				diff := int(c.G) - int(bg.G)
				if diff < 0 {
					diff = -diff
				}
				if diff > 60 {
					ink++
				}
			}
		}
		if ink < 70 {
			t.Fatalf("%s title had only %d contrasting pixels", locale, ink)
		}
		if bytes.Equal(previous, glyphs) {
			t.Fatal("changing the language did not redraw the title")
		}
		previous = glyphs
	}
}

func TestSettingsPluginsPermissionDrawerVisual(t *testing.T) {
	size := image.Pt(1040, 680)
	window, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skipf("offscreen renderer unavailable: %v", err)
	}
	defer window.Release()

	cfg := theme.DefaultConfig()
	cfg.Preset = "nordic-porcelain"
	pal := SettingsPaletteFromTheme(cfg.Resolve(theme.ComponentSettings).Palette)
	m := SettingsModel{
		Locale:         "zh-CN",
		CustomTitleBar: true,
		Appearance:     cfg,
		Palette:        &pal,
		Plugins: []SettingsPlugin{
			{
				ID: "quailcrate.desktop-groups", Name: "桌面分组",
				Description: "桌面快捷启动中心：支持游戏、网页、开发项目、应用与多标签分组。",
				Icon: "folder", Runtime: "lua", State: "running", Status: "运行中", Enabled: true, CanToggle: true,
				RequestedPermissions: []string{"paths.open", "apps.read", "apps.launch", "urls.open", "service:quailcrate.items/open"},
				GrantedPermissions:   []string{"paths.open", "apps.read", "apps.launch", "urls.open", "service:quailcrate.items/open"},
			},
			{
				ID: "quailcrate.launcher", Name: "应用快速启动",
				Description: "查找并启动已安装的应用。",
				Icon: "search", Runtime: "lua", State: "running", Status: "运行中", Enabled: true, CanToggle: true,
				RequestedPermissions: []string{"apps.read", "apps.launch"},
				GrantedPermissions:   []string{"apps.read", "apps.launch"},
			},
			{
				ID: "quailcrate.music", Name: "音乐",
				Description: "支持歌单管理、Bilibili 扫码登录与 BV/分P/收藏夹音频播放，附带桌面迷你播放器。",
				Icon: "music", Runtime: "lua", State: "running", Status: "运行中", Enabled: true, CanToggle: true,
				RequestedPermissions: []string{"network.http", "audio.play"},
				GrantedPermissions:   []string{"network.http", "audio.play"},
			},
			{
				ID: "quailcrate.calendar", Name: "桌面月历",
				Description: "桌面月历与日期工具。",
				Icon: "calendar", Runtime: "lua", State: "running", Status: "运行中", Enabled: true, CanToggle: true,
			},
			{
				ID: "quailcrate.wallpaper", Name: "系统壁纸",
				Description: "将本地图片设为 Windows 桌面壁纸。",
				Icon: "image", Runtime: "lua", State: "running", Status: "运行中", Enabled: true, CanToggle: true,
				RequestedPermissions: []string{"wallpaper.set"},
				GrantedPermissions:   []string{"wallpaper.set"},
			},
		},
	}

	var router input.Router
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(size), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	view := NewSettingsView()
	view.ShowPlugins()

	render := func(name string) {
		gtx.Reset()
		view.Layout(gtx, m, nil)
		if err := window.Frame(gtx.Ops); err != nil {
			t.Fatal(err)
		}
		router.Frame(gtx.Ops)
		outDir := os.Getenv("QUAILCRATE_RENDER_PLUGINS_OUT")
		if outDir == "" {
			return
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := window.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		_ = os.MkdirAll(outDir, 0o700)
		f, err := os.Create(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_ = png.Encode(f, img)
	}

	render("plugins-collapsed.png")
	view.pluginButtons["quailcrate.music"].permToggle.Click()
	render("plugins-expanded.png")
}

func TestSettingsGeneralAutostartAndUpdatesVisual(t *testing.T) {
	outDir := os.Getenv("QUAILCRATE_RENDER_GENERAL_OUT")
	render := func(name string, size image.Point, m SettingsModel) {
		window, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skipf("offscreen renderer unavailable: %v", err)
		}
		defer window.Release()
		var router input.Router
		gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(size), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
		view := NewSettingsView()
		view.SetPage(PageGeneral)
		view.Layout(gtx, m, nil)
		if err := window.Frame(gtx.Ops); err != nil {
			t.Fatal(err)
		}
		router.Frame(gtx.Ops)
		if outDir == "" {
			return
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := window.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		_ = os.MkdirAll(outDir, 0o700)
		f, err := os.Create(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_ = png.Encode(f, img)
	}

	darkCfg := theme.DefaultConfig()
	darkPal := SettingsPaletteFromTheme(darkCfg.Resolve(theme.ComponentSettings).Palette)
	render("general-zh-dark-dev.png", image.Pt(1040, 680), SettingsModel{
		Locale:             "zh-CN",
		LanguagePreference: "zh-CN",
		CustomTitleBar:     true,
		Appearance:         darkCfg,
		Palette:            &darkPal,
		Hotkey:             "Alt+Space",
		HotkeyRegistered:   true,
		Autostart: SettingsAutostart{
			Loaded:    true,
			Available: false,
		},
		Update: SettingsUpdate{
			CurrentVersion: "0.0.0-dev",
			Channel:        "dev",
			Phase:          "idle",
		},
	})

	render("general-zh-dark-downloading.png", image.Pt(1040, 820), SettingsModel{
		Locale:             "zh-CN",
		LanguagePreference: "zh-CN",
		CustomTitleBar:     true,
		Appearance:         darkCfg,
		Palette:            &darkPal,
		Hotkey:             "Alt+Space",
		HotkeyRegistered:   true,
		Autostart: SettingsAutostart{
			Loaded:    true,
			Available: true,
			Enabled:   true,
		},
		Update: SettingsUpdate{
			CurrentVersion:  "0.1.0-alpha.1",
			TargetVersion:   "0.1.0-alpha.2",
			Channel:         "alpha",
			AutoUpdate:      true,
			LastCheckedAt:   time.Date(2026, 11, 27, 21, 15, 0, 0, time.Local),
			DownloadedBytes: 11 * 1024 * 1024,
			TotalBytes:      18 * 1024 * 1024,
			CanInstall:      true,
			ReleaseURL:      "https://github.com/SuperQuail/QuailCrate/releases/tag/v0.1.0-alpha.2",
			Phase:           "downloading",
		},
	})

	lightCfg := theme.DefaultConfig()
	lightCfg.Preset = "nordic-porcelain"
	lightPal := SettingsPaletteFromTheme(lightCfg.Resolve(theme.ComponentSettings).Palette)
	render("general-en-light-manual.png", image.Pt(1040, 840), SettingsModel{
		Locale:             "en-US",
		LanguagePreference: "en-US",
		CustomTitleBar:     true,
		Appearance:         lightCfg,
		Palette:            &lightPal,
		Hotkey:             "Alt+Space",
		HotkeyRegistered:   true,
		Autostart: SettingsAutostart{
			Loaded:    true,
			Available: true,
			Conflict:  true,
		},
		Update: SettingsUpdate{
			CurrentVersion: "0.1.0-alpha.1",
			TargetVersion:  "0.1.0-alpha.2",
			Channel:        "alpha",
			AutoUpdate:     false,
			LastCheckedAt:  time.Date(2026, 11, 27, 21, 15, 0, 0, time.Local),
			CanInstall:     false,
			ReleaseURL:     "https://github.com/SuperQuail/QuailCrate/releases/tag/v0.1.0-alpha.2",
			Phase:          "available",
		},
	})

	hostOnlyDef := &v2.Settings{
		UIVersion: 2,
		Title:     "鹌鹑工具箱 · 设置",
		Sections: []v2.SettingsSection{
			{
				ID:          PageGeneral,
				Title:       "常规",
				Description: "管理开机启动与应用更新。",
				Icon:        "settings",
				Cards: []v2.SettingsCard{
					{Type: "autostart", Title: "开机启动"},
					{Type: "updates", Title: "应用更新"},
				},
			},
			{
				ID:          PagePlugins,
				Title:       "插件管理",
				Description: "管理已安装的插件，可随时重新启用工作台。",
				Icon:        "code",
				Cards:       []v2.SettingsCard{{Type: "plugins", Title: "插件管理"}},
			},
		},
	}
	render("general-zh-host-only-compact.png", image.Pt(640, 760), SettingsModel{
		Definition:     hostOnlyDef,
		Locale:         "zh-CN",
		CustomTitleBar: true,
		Appearance:     darkCfg,
		Palette:        &darkPal,
		Autostart: SettingsAutostart{
			Loaded: true,
			Error:  "read_failed",
		},
		Update: SettingsUpdate{
			CurrentVersion:       "0.1.0-alpha.1",
			TargetVersion:        "0.1.0-alpha.2",
			Channel:              "alpha",
			AutoUpdate:           true,
			LastCheckedAt:        time.Date(2026, 11, 27, 21, 15, 0, 0, time.Local),
			CanInstall:           true,
			ReleaseURL:           "https://github.com/SuperQuail/QuailCrate/releases/tag/v0.1.0-alpha.2",
			Phase:                "failed",
			ErrorCode:            "install_busy",
			LastInstallErrorCode: "install_busy",
		},
	})
}
