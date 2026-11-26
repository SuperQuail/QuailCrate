package gioui

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

func TestSettingsPreviewWallpaperReusesTextures(t *testing.T) {
	var ops op.Ops
	strip := image.Rect(16, 54, 776, 168)
	first := getSettingsPreviewWallpaper(strip.Size(), false)
	blurred := DrawSettingsPreviewWallpaper(&ops, strip, false)
	if first.sharp.Size() != image.Pt(380, 57) || blurred.imageOp.Size() != first.sharp.Size() {
		t.Fatal("preview textures do not match the wallpaper size")
	}
	dark := getSettingsPreviewWallpaper(strip.Size(), true)
	if dark.sharp == first.sharp || dark.backdrop.imageOp == blurred.imageOp {
		t.Fatal("dark preview reused light textures")
	}
	// Component tabs move the strip while keeping its size.
	again := DrawSettingsPreviewWallpaper(&ops, strip.Add(image.Pt(0, 34)), false)
	if again.imageOp != blurred.imageOp || getSettingsPreviewWallpaper(strip.Size(), false).sharp != first.sharp {
		t.Fatal("moving the preview recreated its textures")
	}
	crop := image.Rect(4, 6, 96, 40)
	alpha := SpatialGlassBackdropAlpha(13)
	faded := blurred.fadedTexture(crop, alpha)
	if again.fadedTexture(crop, alpha) != faded || again.fadedTexture(crop, SpatialGlassBackdropAlpha(7)) == faded {
		t.Fatal("translucent preview texture was not keyed by opacity")
	}
	if again.fadedTexture(crop.Add(image.Pt(2, 0)), alpha) == faded {
		t.Fatal("translucent preview reused a different crop")
	}
	resized := getSettingsPreviewWallpaper(strip.Size().Add(image.Pt(100, 0)), false)
	if resized.sharp == first.sharp || resized.backdrop.imageOp == blurred.imageOp {
		t.Fatal("resized preview reused the previous textures")
	}
}

func TestBuildSpatialBackdropBlursAndSaturates(t *testing.T) {
	// Left half muted blue, right half muted rose with a sharp vertical seam.
	src := image.NewRGBA(image.Rect(0, 0, 64, 32))
	leftCol := color.RGBA{R: 70, G: 110, B: 170, A: 255}
	rightCol := color.RGBA{R: 180, G: 90, B: 110, A: 255}
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				src.SetRGBA(x, y, leftCol)
			} else {
				src.SetRGBA(x, y, rightCol)
			}
		}
	}

	out := BuildSpatialBackdrop(src, 6)
	if out == nil || out.Rect.Size() != src.Rect.Size() {
		t.Fatalf("unexpected backdrop size: %v", out)
	}

	// Seam should be smoothly blurred across center pixels (not a hard jump).
	cLeft := out.RGBAAt(30, 16)
	cRight := out.RGBAAt(33, 16)
	diffR := int(cRight.R) - int(cLeft.R)
	if diffR < 0 {
		diffR = -diffR
	}
	if diffR >= 80 {
		t.Fatalf("expected blurred transition across seam, got sharp jump left=%+v right=%+v", cLeft, cRight)
	}

	// Far-left uniform region should be more saturated than input (B - R spread increases with saturate(185%)).
	farLeft := out.RGBAAt(2, 16)
	inSpread := int(leftCol.B) - int(leftCol.R)
	outSpread := int(farLeft.B) - int(farLeft.R)
	if outSpread <= inSpread+30 {
		t.Fatalf("expected saturate(185%%) vibrancy boost: inSpread=%d outSpread=%d out=%+v", inSpread, outSpread, farLeft)
	}
	if farLeft.A != 255 {
		t.Fatalf("expected opaque backdrop pixel, got A=%d", farLeft.A)
	}
}

func TestSpatialGlassChromeGeometryAndDarkLightParity(t *testing.T) {
	size := image.Pt(240, 180)
	frame := image.Rect(6, 2, 234, 168)

	for _, tc := range []struct {
		name   string
		isDark bool
	}{
		{"dark", true},
		{"light", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shadow, rim := renderSpatialGlassChrome(spatialChromeKey{
				canvasSize:   size,
				frame:        frame,
				radius:       18,
				dpi:          96,
				surfaceAlpha: 199,
				isDark:       tc.isDark,
			})
			if shadow == nil || rim == nil {
				t.Fatalf("expected both shadow and rim images")
			}

			// Deep interior must stay strictly transparent on both shadow & rim (no shadow bleed!).
			centerShadow := shadow.RGBAAt(120, 90)
			centerRim := rim.RGBAAt(120, 90)
			if centerShadow.A != 0 || centerRim.A != 0 {
				t.Fatalf("interior must be transparent: shadow=%+v rim=%+v", centerShadow, centerRim)
			}

			// Below the card bottom edge, the downward elevation shadow must be visible and fade to 0 at window edge.
			underCard := shadow.RGBAAt(120, frame.Max.Y+2)
			atBottomEdge := shadow.RGBAAt(120, size.Y-1)
			if underCard.A == 0 {
				t.Fatalf("expected soft elevation shadow beneath card bottom edge")
			}
			if atBottomEdge.A >= underCard.A {
				t.Fatalf("expected shadow to feather toward window edge: under=%d edge=%d", underCard.A, atBottomEdge.A)
			}

			// Top border (frame.Min.Y) and top inner specular glow (frame.Min.Y+1) must both be visible & white-tinted.
			topBorder := rim.RGBAAt(120, frame.Min.Y)
			topGlow := rim.RGBAAt(120, frame.Min.Y+1)
			bottomInner := rim.RGBAAt(120, frame.Max.Y-2)
			if topBorder.A == 0 || topGlow.A == 0 {
				t.Fatalf("expected frosted top border and inner glow: border=%+v glow=%+v", topBorder, topGlow)
			}
			if bottomInner.A != 0 {
				t.Fatalf("top specular inner glow should not appear on bottom interior edge: %+v", bottomInner)
			}
		})
	}
}

type spatialCanvasSpy struct {
	widgetPaintRecord
	spatialCalls []SpatialGlassSpec
}

func (s *spatialCanvasSpy) DrawSpatialGlass(spec SpatialGlassSpec) {
	s.spatialCalls = append(s.spatialCalls, spec)
}

func TestWidgetLayoutUsesSpatialGlassOnlyForFirstStyle(t *testing.T) {
	p := theme.DefaultPalette()
	base := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 78, Translucent: true, HasCornerRadius: true, CornerRadius: theme.DefaultCornerRadius,
	}
	view := v1.View{Title: "Calendar", Controls: []v1.Control{{ID: "ok", Type: "button", Text: "OK"}}}
	bdImg := image.NewRGBA(image.Rect(0, 0, 64, 64))
	bd := &SpatialBackdrop{Image: bdImg, Source: SpatialRect{Max: f32.Pt(64, 64)}}

	// First glass option (GlassSmoked) invokes DrawSpatialGlass with backdrop & elevated state.
	smokedSpy := &spatialCanvasSpy{widgetPaintRecord: widgetPaintRecord{labels: make(map[string]image.Rectangle)}}
	smokedColors := base
	smokedColors.GlassStyle = theme.GlassSmoked
	LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(300, 220), Colors: smokedColors, Backdrop: bd, Elevated: true}, smokedSpy)
	if len(smokedSpy.spatialCalls) != 1 {
		t.Fatalf("expected 1 DrawSpatialGlass call for GlassSmoked, got %d", len(smokedSpy.spatialCalls))
	}
	call := smokedSpy.spatialCalls[0]
	if call.Backdrop != bd || !call.Elevated || call.Radius != 18 || call.Frame.Min.X <= 0 || call.Frame.Max.Y >= 220 {
		t.Fatalf("unexpected spatial glass spec: %+v", call)
	}

	// Terminal Wireframe and Minimal Float must NOT invoke DrawSpatialGlass.
	for _, style := range []string{theme.GlassWireframe, theme.GlassMinimal} {
		spy := &spatialCanvasSpy{widgetPaintRecord: widgetPaintRecord{labels: make(map[string]image.Rectangle)}}
		cols := base
		cols.GlassStyle = style
		LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(300, 220), Colors: cols, Backdrop: bd}, spy)
		if len(spy.spatialCalls) != 0 {
			t.Fatalf("style %q should not invoke DrawSpatialGlass, got %d calls", style, len(spy.spatialCalls))
		}
	}

	// 0% opacity pure float skips spatial glass chrome and preserves alpha=1 hit-test shell.
	zeroSpy := &spatialCanvasSpy{widgetPaintRecord: widgetPaintRecord{labels: make(map[string]image.Rectangle)}}
	zeroColors := smokedColors
	zeroColors.Opacity = 0
	LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(300, 220), Colors: zeroColors, Backdrop: bd}, zeroSpy)
	if len(zeroSpy.spatialCalls) != 0 || len(zeroSpy.alphas) == 0 || zeroSpy.alphas[0] != 1 {
		t.Fatalf("0%% opacity should bypass spatial chrome and keep alpha=1 shell: spatial=%d alphas=%v", len(zeroSpy.spatialCalls), zeroSpy.alphas)
	}
}

func TestSpatialGlassHeadlessGPURender(t *testing.T) {
	dir := os.Getenv("QUAILCRATE_VERIFY_DIR")
	if dir == "" {
		return
	}
	// 1) Synthesize a vibrant sharp wallpaper (with crisp diagonal stripes + glowing orbs)
	// so the blur(36px) saturate(185%) frosted contrast against sharp background is unmistakable.
	wallW, wallH := 920, 420
	wall := image.NewRGBA(image.Rect(0, 0, wallW, wallH))
	for y := 0; y < wallH; y++ {
		for x := 0; x < wallW; x++ {
			fx, fy := float64(x)/float64(wallW), float64(y)/float64(wallH)
			r := 14.0 + 38.0*fx + 18.0*fy
			g := 18.0 + 26.0*(1-fx) + 32.0*fy
			b := 38.0 + 52.0*fx + 44.0*(1-fy)
			// Vivid cyan orb behind left card
			d1 := math.Hypot(float64(x)-220, float64(y)-180) / 180.0
			if d1 < 1 {
				w := (1 - d1*d1)
				w *= w
				r = r*(1-w) + 56*w
				g = g*(1-w) + 189*w
				b = b*(1-w) + 248*w
			}
			// Vivid rose/magenta orb behind center-left
			d2 := math.Hypot(float64(x)-420, float64(y)-260) / 190.0
			if d2 < 1 {
				w := (1 - d2*d2)
				w *= w
				r = r*(1-w) + 236*w
				g = g*(1-w) + 72*w
				b = b*(1-w) + 153*w
			}
			// Vivid emerald/teal orb behind right card
			d3 := math.Hypot(float64(x)-730, float64(y)-190) / 190.0
			if d3 < 1 {
				w := (1 - d3*d3)
				w *= w
				r = r*(1-w) + 52*w
				g = g*(1-w) + 211*w
				b = b*(1-w) + 153*w
			}
			// Crisp thin geometric lines on wallpaper to make the blur unmistakably visible
			if (x+y)%28 == 0 || (x-y+1000)%44 == 0 {
				r = math.Min(255, r+55)
				g = math.Min(255, g+55)
				b = math.Min(255, b+65)
			}
			wall.SetRGBA(x, y, color.RGBA{R: clampByte255(r), G: clampByte255(g), B: clampByte255(b), A: 255})
		}
	}

	// Downsample 1/4 + BuildSpatialBackdrop(r=9 => blur(36px) saturate(185%)).
	smallW, smallH := wallW/4, wallH/4
	small := image.NewRGBA(image.Rect(0, 0, smallW, smallH))
	for y := 0; y < smallH; y++ {
		for x := 0; x < smallW; x++ {
			small.SetRGBA(x, y, wall.RGBAAt(x*4+2, y*4+2))
		}
	}
	blurred := BuildSpatialBackdrop(small, 9)

	// Render two widgets (Dark Tokyo Night + Light custom surface) over the sharp wallpaper.
	wSize := image.Pt(360, 290)
	win, err := headless.NewWindow(wSize.X, wSize.Y)
	if err != nil {
		t.Skipf("headless GPU unavailable: %v", err)
	}
	defer win.Release()
	shaper := NewTextShaper()

	compositeOver := func(dst *image.RGBA, src *image.RGBA, ox, oy int) {
		for y := 0; y < src.Rect.Dy(); y++ {
			for x := 0; x < src.Rect.Dx(); x++ {
				sp := src.RGBAAt(x, y)
				if sp.A == 0 {
					continue
				}
				dp := dst.RGBAAt(ox+x, oy+y)
				inv := 255 - uint32(sp.A)
				r := min(255, uint32(sp.R)+(uint32(dp.R)*inv+127)/255)
				g := min(255, uint32(sp.G)+(uint32(dp.G)*inv+127)/255)
				b := min(255, uint32(sp.B)+(uint32(dp.B)*inv+127)/255)
				dst.SetRGBA(ox+x, oy+y, color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
			}
		}
	}

	renderCard := func(ox, oy int, pal theme.Palette, opacity int, elevated bool, title string) {
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Constraints: layout.Exact(wSize),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		}
		padL, padT, padR, padB := SpatialGlassPadding(wSize, 96, true, uint8(opacity*255/100))
		fx0, fy0 := ox+padL, oy+padT
		fx1, fy1 := ox+wSize.X-padR, oy+wSize.Y-padB
		bd := &SpatialBackdrop{
			Image: blurred,
			Source: SpatialRect{
				Min: f32.Pt(float32(fx0)/4, float32(fy0)/4),
				Max: f32.Pt(float32(fx1)/4, float32(fy1)/4),
			},
		}
		colors := WidgetColors{
			Background: pal.Background, Surface: pal.Surface, Ink: pal.Text, Secondary: pal.Secondary,
			Border: pal.Border, Accent: pal.Accent, Selection: pal.Selected, Hover: pal.Hover,
			Weekend: pal.Weekend, Warning: pal.Warning, OnAccent: pal.OnAccent, OnSelection: pal.Text,
			Opacity: opacity, Translucent: true, GlassStyle: theme.GlassSmoked,
			CornerRadius: theme.DefaultCornerRadius, HasCornerRadius: true,
		}
		view := v1.View{
			Title: title,
			Controls: []v1.Control{{
				ID: "days", Type: "grid", Columns: 7,
				Items: []v1.Item{
					{ID: "h1", Text: "一", State: "header"}, {ID: "h2", Text: "二", State: "header"},
					{ID: "h3", Text: "三", State: "header"}, {ID: "h4", Text: "四", State: "header"},
					{ID: "h5", Text: "五", State: "header"}, {ID: "h6", Text: "六", State: "header-weekend"},
					{ID: "h7", Text: "日", State: "header-weekend"},
					{ID: "d23", Text: "23"}, {ID: "d24", Text: "24"}, {ID: "d25", Text: "25"},
					{ID: "d26", Text: "26", State: "current"}, {ID: "d27", Text: "27"},
					{ID: "d28", Text: "28", State: "weekend"}, {ID: "d29", Text: "29", State: "weekend"},
					{ID: "d30", Text: "30"}, {ID: "d1", Text: "1", State: "muted"}, {ID: "d2", Text: "2", State: "muted"},
					{ID: "d3", Text: "3", State: "muted"}, {ID: "d4", Text: "4", State: "muted"},
					{ID: "d5", Text: "5", State: "muted"}, {ID: "d6", Text: "6", State: "muted"},
				},
			}},
		}
		LayoutWidget(view, WidgetOptions{DPI: 96, Size: wSize, Colors: colors, Backdrop: bd, Elevated: elevated}, GioWidgetCanvas{
			Context:      gtx,
			Shaper:       shaper,
			Typeface:     font.Typeface("Microsoft YaHei UI, Segoe UI, sans-serif"),
			MonoTypeface: font.Typeface("Cascadia Mono, Consolas, monospace"),
		})
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		shot := image.NewRGBA(image.Rectangle{Max: wSize})
		if err := win.Screenshot(shot); err != nil {
			t.Fatal(err)
		}
		compositeOver(wall, shot, ox, oy)
	}

	renderCard(60, 65, theme.PresetByID("nord-mono").Palette, 78, false, "2026 年 11 月 · 空间毛玻璃")
	lightCfg := theme.DefaultConfig()
	lightCfg.GlobalColors = theme.PartialPalette{
		Background: "#EEF1F6", Surface: "#FFFFFF", Border: "#CBD5E1",
		Text: "#0F172A", Secondary: "#475569", Accent: "#3B82F6", Weekend: "#F43F5E",
	}
	renderCard(490, 65, lightCfg.Resolve(theme.ComponentGlobal).Palette, 78, false, "浅色空间毛玻璃")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "spatial_glass_widgets.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, wall); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// 2) Render Settings -> Appearance page (closed & expanded theme dropdown) to verify layout.
	sSize := image.Pt(980, 680)
	sWin, err := headless.NewWindow(sSize.X, sSize.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer sWin.Release()
	var router input.Router
	view := NewSettingsView()
	view.ShowAppearance()
	cfg := theme.DefaultConfig()
	cfg.Preset = "catppuccin-mocha"
	cfg.GlassStyle = theme.GlassSmoked
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Now: time.Now(),
		Constraints: layout.Exact(sSize), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
	}
	view.Layout(gtx, SettingsModel{Locale: "zh-CN", Appearance: cfg}, nil)
	if err := sWin.Frame(gtx.Ops); err != nil {
		t.Fatal(err)
	}
	sImg := image.NewRGBA(image.Rectangle{Max: sSize})
	if err := sWin.Screenshot(sImg); err != nil {
		t.Fatal(err)
	}
	sf, err := os.Create(filepath.Join(dir, "settings_appearance_preview.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(sf, sImg); err != nil {
		sf.Close()
		t.Fatal(err)
	}
	sf.Close()

	// Also render expanded dropdown state so we can visually inspect all grouped themes.
	view.presetMenuOpen = true
	gtx.Reset()
	view.Layout(gtx, SettingsModel{Locale: "zh-CN", Appearance: cfg}, nil)
	if err := sWin.Frame(gtx.Ops); err != nil {
		t.Fatal(err)
	}
	openImg := image.NewRGBA(image.Rectangle{Max: sSize})
	if err := sWin.Screenshot(openImg); err != nil {
		t.Fatal(err)
	}
	of, err := os.Create(filepath.Join(dir, "settings_appearance_dropdown_open.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(of, openImg); err != nil {
		of.Close()
		t.Fatal(err)
	}
	of.Close()

	// Also render light theme (Alabaster Mist at 13% opacity) to verify the light preview card & edge scrollbar.
	lightPreviewCfg := theme.DefaultConfig()
	lightPreviewCfg.Preset = "alabaster-mist"
	lightPreviewCfg.WidgetOpacity = 13
	lightPreviewCfg.GlassStyle = theme.GlassSmoked
	view.presetMenuOpen = false
	gtx.Reset()
	view.Layout(gtx, SettingsModel{Locale: "zh-CN", Appearance: lightPreviewCfg}, nil)
	if err := sWin.Frame(gtx.Ops); err != nil {
		t.Fatal(err)
	}
	lightImg := image.NewRGBA(image.Rectangle{Max: sSize})
	if err := sWin.Screenshot(lightImg); err != nil {
		t.Fatal(err)
	}
	lf, err := os.Create(filepath.Join(dir, "settings_appearance_light.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(lf, lightImg); err != nil {
		lf.Close()
		t.Fatal(err)
	}
	lf.Close()
}
