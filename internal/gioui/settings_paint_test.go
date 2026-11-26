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
