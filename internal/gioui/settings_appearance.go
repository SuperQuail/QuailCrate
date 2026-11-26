package gioui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"quailcrate/internal/theme"
)

func (f settingsFrame) appearanceTargets() []SettingsAppearanceTarget {
	if len(f.model.AppearanceTargets) > 0 {
		return f.model.AppearanceTargets
	}
	return []SettingsAppearanceTarget{
		{ID: theme.ComponentGlobal, LabelZH: "全局默认", LabelEN: "Global", SupportsOpacity: true},
		{ID: theme.ComponentCalendar, LabelZH: "桌面月历", LabelEN: "Calendar", SupportsOpacity: true},
		{ID: theme.ComponentGroups, LabelZH: "桌面分组", LabelEN: "Groups", SupportsOpacity: true},
		{ID: theme.ComponentLauncher, LabelZH: "快速启动", LabelEN: "Launcher", SupportsOpacity: true},
		{ID: theme.ComponentSettings, LabelZH: "设置面板", LabelEN: "Settings", SupportsOpacity: false},
	}
}

func (f settingsFrame) emitAppearance(cfg theme.Config, persist bool) {
	encoded, err := theme.MarshalConfig(cfg)
	if err != nil {
		return
	}
	kind := PreviewAppearance
	if persist {
		kind = SaveAppearance
	}
	f.emit(SettingsAction{Kind: kind, Value: encoded})
}

func (f settingsFrame) syncAppearanceInputs(target string, resolved theme.Resolved) {
	key := fmt.Sprintf("%s|%d|%d|%d|%t|%s|%s|%s|%s|%s|%s|%s",
		target, resolved.Opacity, resolved.CornerRadius, resolved.IconSize, resolved.TitleAccentBar,
		theme.FormatHex(resolved.Palette.Background),
		theme.FormatHex(resolved.Palette.Surface),
		theme.FormatHex(resolved.Palette.Border),
		theme.FormatHex(resolved.Palette.Text),
		theme.FormatHex(resolved.Palette.Secondary),
		theme.FormatHex(resolved.Palette.Accent),
		theme.FormatHex(resolved.Palette.Weekend),
	)
	if f.lastSyncKey == key {
		return
	}
	f.lastSyncKey = key
	if !f.opacityDragging {
		f.opacitySlider.Value = float32(resolved.Opacity) / 100
	}
	if !f.radiusDragging {
		f.radiusSlider.Value = float32(resolved.CornerRadius) / float32(theme.MaxCornerRadius)
	}
	if !f.iconSizeDragging {
		f.iconSizeSlider.Value = float32(resolved.IconSize-theme.MinIconSize) / float32(theme.MaxIconSize-theme.MinIconSize)
	}
	for _, slot := range theme.EditableSlots {
		ed := f.hexEditors[slot]
		if ed == nil {
			ed = &widget.Editor{SingleLine: true, Submit: true, MaxLen: 9}
			f.hexEditors[slot] = ed
		}
		ed.SetText(theme.FormatHex(resolved.Palette.SlotColor(slot)))
	}
}

func (f settingsFrame) appearance(gtx layout.Context) {
	kinds := [...]string{"target", "presets", "opacity", "radius", "iconSize", "colors"}
	pad := f.contentPadding(gtx)
	f.list(gtx, &f.appearanceList, len(kinds), func(gtx layout.Context, index int) layout.Dimensions {
		dims := f.paddedCard(gtx, pad, func(gtx layout.Context) layout.Dimensions {
			return f.appearanceCard(gtx, kinds[index])
		})
		if index == len(kinds)-1 {
			dims.Size.Y += gtx.Dp(12)
		}
		return dims
	})
}

type appearanceSelection struct {
	targets  []SettingsAppearanceTarget
	current  SettingsAppearanceTarget
	resolved theme.Resolved
}

func (f settingsFrame) selectAppearanceTarget(gtx layout.Context) *appearanceSelection {
	targets := f.appearanceTargets()
	current := targets[0]
	for _, t := range targets {
		if b := f.targetButtons[t.ID]; b != nil && b.Clicked(gtx) {
			f.appearanceTarget = t.ID
			f.presetMenuOpen = false
			f.lastSyncKey = ""
		}
		if t.ID == f.appearanceTarget {
			current = t
		}
	}
	f.appearanceTarget = current.ID
	resolved := f.model.Appearance.Resolve(current.ID)
	f.syncAppearanceInputs(current.ID, resolved)
	return &appearanceSelection{targets: targets, current: current, resolved: resolved}
}

func (f *settingsFrame) appearanceCard(gtx layout.Context, kind string) layout.Dimensions {
	// A workbench can place the target picker after other appearance cards.
	// Consume its input before measuring or painting the first card of this frame.
	if f.selection == nil {
		f.selection = f.selectAppearanceTarget(gtx)
	}
	current, resolved := f.selection.current, f.selection.resolved
	cfg := f.model.Appearance
	switch kind {
	case "target":
		return f.appearanceTargetAndPreviewCard(gtx, f.selection.targets, current, cfg, resolved)
	case "presets":
		return f.appearancePresetsCard(gtx, current, cfg, resolved)
	case "opacity":
		return f.appearanceOpacityCard(gtx, current, cfg, resolved)
	case "radius":
		return f.appearanceRadiusCard(gtx, cfg, resolved)
	case "iconSize":
		return f.appearanceIconSizeCard(gtx, cfg, resolved)
	default:
		return f.appearanceColorsCard(gtx, current, cfg, resolved)
	}
}
func (f settingsFrame) appearanceTargetAndPreviewCard(gtx layout.Context, targets []SettingsAppearanceTarget, current SettingsAppearanceTarget, cfg theme.Config, resolved theme.Resolved) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	isGlobal := current.ID == theme.ComponentGlobal
	ov := cfg.Components[current.ID]

	// Handle component override toggle / reset buttons.
	if !isGlobal {
		if f.customToggle.Clicked(gtx) {
			cfg = cfg.Clone()
			ov.Custom = !ov.Custom
			cfg.Components[current.ID] = ov
			f.lastSyncKey = ""
			f.emitAppearance(cfg, true)
		}
		if f.resetComponent.Clicked(gtx) {
			cfg = cfg.Clone()
			delete(cfg.Components, current.ID)
			f.lastSyncKey = ""
			f.emitAppearance(cfg, true)
		}
	}

	cardH := p(182)
	if !isGlobal {
		cardH = p(216)
	}
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)

	// Target component tabs row.
	tabX, tabY := p(14), p(12)
	tabH := p(30)
	for _, t := range targets {
		b := f.targetButtons[t.ID]
		if b == nil {
			b = &widget.Clickable{}
			f.targetButtons[t.ID] = b
		}
		label := f.tr(t.LabelZH, t.LabelEN)
		if t.ID != theme.ComponentGlobal && cfg.Components[t.ID].Custom {
			label += " ●"
		}
		tabW := p(96)
		if w < p(560) {
			tabW = max(p(68), (w-p(28)-p(6)*(len(targets)-1))/max(1, len(targets)))
		}
		if tabX+tabW > w-p(12) {
			break
		}
		active := t.ID == current.ID
		f.button(gtx, image.Rect(tabX, tabY, tabX+tabW, tabY+tabH), b, label, func(gtx layout.Context) {
			bg, border, ink := f.p.Background, f.p.Line, f.p.Secondary
			if active {
				bg, border, ink = f.p.Selected, f.p.Accent, f.p.Accent
				if f.model.HighContrast {
					ink = f.p.OnAccent
				}
			} else if b.Hovered() {
				bg, ink = f.p.Hover, f.p.Text
			}
			tabR := f.radius(7)
			settingsFill(gtx, settingsBounds(gtx), tabR, bg)
			settingsStroke(gtx, settingsBounds(gtx), tabR, 1, border)
			f.label(gtx, settingsBounds(gtx), label, 12, font.Medium, ink, layout.Center)
		})
		tabX += tabW + p(6)
	}

	previewTop := p(54)
	if !isGlobal {
		rowY := p(50)
		modeText := f.tr("当前跟随全局配色 · 修改下方预设/透明度/色值将自动启用独立定制", "Inheriting global theme · Editing below enables custom override")
		toggleTitle := f.tr("开启独立配色", "Enable override")
		if ov.Custom {
			modeText = f.tr("已启用该组件的独立配色与透明度覆盖", "Component custom override active")
			toggleTitle = f.tr("切回跟随全局", "Use global")
		}
		f.label(gtx, image.Rect(p(16), rowY, w-p(214), rowY+p(28)), modeText, 12, font.Normal, f.p.Secondary, layout.W)
		f.textButton(gtx, image.Rect(w-p(208), rowY, w-p(100), rowY+p(28)), &f.customToggle, toggleTitle, !ov.Custom)
		f.textButton(gtx, image.Rect(w-p(92), rowY, w-p(14), rowY+p(28)), &f.resetComponent, f.tr("重置", "Reset"), false)
		previewTop = p(88)
	}

	// Live wallpaper-through preview strip.
	preview := image.Rect(p(14), previewTop, w-p(14), cardH-p(14))
	f.drawWallpaperPreview(gtx, preview, current, resolved)

	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}

func (f settingsFrame) drawWallpaperPreview(gtx layout.Context, r image.Rectangle, target SettingsAppearanceTarget, res theme.Resolved) {
	p := gtx.Dp
	frameR := f.radius(10)
	// Paint the smooth theme-adaptive studio wallpaper clipped inside the preview frame,
	// returning its matching blurred+saturated backdrop for authentic Spatial Glass.
	clipArea := clip.UniformRRect(r, p(frameR)).Push(gtx.Ops)
	backdrop := DrawSettingsPreviewWallpaper(gtx.Ops, r, res.IsDark)
	clipArea.Pop()
	frameBorder := theme.WithAlpha(f.p.Line, 210)
	if !res.IsDark {
		frameBorder = color.NRGBA{R: 15, G: 23, B: 42, A: 28}
	}
	settingsStroke(gtx, r, frameR, 1, frameBorder)

	// Mini translucent component card floating on top of the studio wallpaper.
	cardW := min(p(256), max(p(174), r.Dx()*46/100))
	card := image.Rect(r.Min.X+p(14), r.Min.Y+p(11), r.Min.X+p(14)+cardW, r.Max.Y-p(11))
	alpha := uint8(res.Opacity * 255 / 100)
	if !target.SupportsOpacity {
		alpha = 255
	}
	pal := res.Palette
	glassStyle := theme.NormalizeGlassStyle(res.GlassStyle)
	cardR := unit.Dp(theme.ScaleRadius(res.CornerRadius, 10))
	pillR := unit.Dp(theme.ScaleRadius(res.CornerRadius, 5))
	barR := unit.Dp(theme.ScaleRadius(res.CornerRadius, 2))
	if target.SupportsOpacity && glassStyle == theme.GlassSmoked && alpha > 0 {
		spatialCardR := p(unit.Dp(theme.ScaleRadius(res.CornerRadius, 14)))
		localCard := card.Sub(r.Min)
		if backdrop.Image != nil {
			sx := backdrop.Source.Dx() / float32(r.Dx())
			sy := backdrop.Source.Dy() / float32(r.Dy())
			backdrop.Source = SpatialRect{
				Min: f32.Pt(float32(localCard.Min.X)*sx, float32(localCard.Min.Y)*sy),
				Max: f32.Pt(float32(localCard.Max.X)*sx, float32(localCard.Max.Y)*sy),
			}
		}
		stripClip := clip.UniformRRect(r, p(frameR)).Push(gtx.Ops)
		stripOff := op.Offset(r.Min).Push(gtx.Ops)
		DrawSpatialGlassOps(gtx.Ops, SpatialGlassSpec{
			CanvasSize:   r.Size(),
			Frame:        localCard,
			Radius:       spatialCardR,
			DPI:          max(96, int(math.Round(float64(gtx.Metric.PxPerDp)*96))),
			Surface:      pal.Surface,
			Opacity:      res.Opacity,
			SurfaceAlpha: alpha,
			IsDark:       res.IsDark,
			Backdrop:     &backdrop,
		})
		stripOff.Pop()
		stripClip.Pop()
	} else {
		settingsFill(gtx, card, cardR, theme.WithAlpha(pal.Surface, alpha))
		switch {
		case target.SupportsOpacity && glassStyle == theme.GlassMinimal:
			// Minimal Float: borderless translucent surface.
		case target.SupportsOpacity && glassStyle == theme.GlassWireframe:
			borderCol := theme.WithAlpha(pal.Accent, 130)
			if alpha > 0 {
				borderCol = theme.WithAlpha(theme.Blend(pal.Border, pal.Accent, 0.45), uint8(min(255, max(120, int(alpha)+75))))
			}
			settingsStroke(gtx, card, cardR, 1, borderCol)
			arm, t := p(8), max(2, p(2))
			bc := theme.WithAlpha(pal.Accent, 210)
			settingsFill(gtx, image.Rect(card.Min.X, card.Min.Y, card.Min.X+arm, card.Min.Y+t), 0, bc)
			settingsFill(gtx, image.Rect(card.Min.X, card.Min.Y, card.Min.X+t, card.Min.Y+arm), 0, bc)
			settingsFill(gtx, image.Rect(card.Max.X-arm, card.Min.Y, card.Max.X, card.Min.Y+t), 0, bc)
			settingsFill(gtx, image.Rect(card.Max.X-t, card.Min.Y, card.Max.X, card.Min.Y+arm), 0, bc)
			settingsFill(gtx, image.Rect(card.Min.X, card.Max.Y-t, card.Min.X+arm, card.Max.Y), 0, bc)
			settingsFill(gtx, image.Rect(card.Min.X, card.Max.Y-arm, card.Min.X+t, card.Max.Y), 0, bc)
			settingsFill(gtx, image.Rect(card.Max.X-arm, card.Max.Y-t, card.Max.X, card.Max.Y), 0, bc)
			settingsFill(gtx, image.Rect(card.Max.X-t, card.Max.Y-arm, card.Max.X, card.Max.Y), 0, bc)
		default:
			borderAlpha := uint8(min(255, max(70, int(alpha)+48)))
			if alpha == 0 {
				borderAlpha = 0
			}
			settingsStroke(gtx, card, cardR, 1, theme.WithAlpha(pal.Border, borderAlpha))
		}
	}

	titleX := card.Min.X + p(12)
	if res.TitleAccentBar {
		settingsFill(gtx, image.Rect(card.Min.X+p(12), card.Min.Y+p(11), card.Min.X+p(15), card.Min.Y+p(25)), barR, pal.Accent)
		titleX = card.Min.X + p(22)
	}
	title := f.tr("2026 年 11 月", "November 2026")
	switch target.ID {
	case theme.ComponentGroups:
		title = f.tr("工作分组", "Workspace")
	case theme.ComponentLauncher:
		title = "> search app..."
	case theme.ComponentSettings:
		title = "QUAILCRATE"
	}
	f.label(gtx, image.Rect(titleX, card.Min.Y+p(8), card.Max.X-p(46), card.Min.Y+p(27)), title, 12, font.SemiBold, pal.Text, layout.W)
	f.monoLabel(gtx, image.Rect(card.Max.X-p(44), card.Min.Y+p(9), card.Max.X-p(12), card.Min.Y+p(26)), "W48", 9.5, font.Medium, pal.Secondary, layout.E)

	// Mini calendar strip with comfortable vertical rhythm.
	rowY := card.Min.Y + p(37)
	days := []string{"23", "24", "25", "26", "27", "28", "29"}
	cellW := (card.Dx() - p(24)) / len(days)
	for i, d := range days {
		cell := image.Rect(card.Min.X+p(12)+i*cellW, rowY, card.Min.X+p(12)+(i+1)*cellW, rowY+p(24))
		ink := pal.Text
		if i == 2 {
			settingsFill(gtx, cell.Inset(p(1)), pillR, pal.Accent)
			ink = pal.OnAccent
		} else if i >= 5 {
			ink = pal.Weekend
		}
		f.monoLabel(gtx, cell, d, 10.5, font.Medium, ink, layout.Center)
	}
	footerText := fmt.Sprintf("D329/365 · %d%%", res.Opacity)
	if !target.SupportsOpacity {
		footerText = "D329/365 · SOLID"
	}
	f.monoLabel(gtx, image.Rect(card.Min.X+p(12), card.Max.Y-p(23), card.Max.X-p(12), card.Max.Y-p(7)), footerText, 9.5, font.Normal, pal.Secondary, layout.W)

	// Right-side theme-adaptive frosted 2x2 spec readout tiles (always legible on dark & light wallpapers).
	metaLeft := card.Max.X + p(14)
	metaRight := r.Max.X - p(14)
	if metaRight-metaLeft >= p(176) {
		tileGap := p(8)
		tileW := (metaRight - metaLeft - tileGap) / 2
		tileH := (card.Dy() - tileGap) / 2
		tileR := f.radius(8)

		scrimBg := color.NRGBA{R: 11, G: 16, B: 28, A: 118}
		scrimBorder := color.NRGBA{R: 255, G: 255, B: 255, A: 36}
		scrimLabel := color.NRGBA{R: 186, G: 202, B: 226, A: 225}
		scrimVal := color.NRGBA{R: 244, G: 248, B: 255, A: 255}
		dotBorder := color.NRGBA{R: 255, G: 255, B: 255, A: 72}
		if !res.IsDark {
			scrimBg = color.NRGBA{R: 255, G: 255, B: 255, A: 176}
			scrimBorder = color.NRGBA{R: 15, G: 23, B: 42, A: 24}
			dotBorder = color.NRGBA{R: 15, G: 23, B: 42, A: 64}
			scrimLabel = color.NRGBA{R: 76, G: 90, B: 112, A: 235}
			scrimVal = color.NRGBA{R: 20, G: 28, B: 44, A: 255}
		}

		opacityVal := fmt.Sprintf("%d%%", res.Opacity)
		if res.Opacity == 0 && target.SupportsOpacity {
			opacityVal = f.tr("0% 纯悬浮", "0% FLOAT")
		} else if !target.SupportsOpacity {
			opacityVal = "100%"
		}

		type specTile struct {
			label    string
			value    string
			swatch   color.NRGBA
			hasDot   bool
		}
		tiles := [4]specTile{
			{label: f.tr("不透明度", "OPACITY"), value: opacityVal},
			{label: f.tr("圆角 · 图标", "RADIUS · ICON"), value: fmt.Sprintf("%dpx · %dpx", res.CornerRadius, res.IconSize)},
			{label: f.tr("强调色", "ACCENT"), value: theme.FormatHex(pal.Accent), swatch: pal.Accent, hasDot: true},
			{label: f.tr("表面色", "SURFACE"), value: theme.FormatHex(pal.Surface), swatch: pal.Surface, hasDot: true},
		}
		for i, t := range tiles {
			col, row := i%2, i/2
			x0 := metaLeft + col*(tileW+tileGap)
			y0 := card.Min.Y + row*(tileH+tileGap)
			tr := image.Rect(x0, y0, x0+tileW, y0+tileH)
			settingsFill(gtx, tr, tileR, scrimBg)
			settingsStroke(gtx, tr, tileR, 1, scrimBorder)
			f.label(gtx, image.Rect(tr.Min.X+p(10), tr.Min.Y+p(5), tr.Max.X-p(8), tr.Min.Y+p(19)), t.label, 9.5, font.Medium, scrimLabel, layout.W)
			valLeft := tr.Min.X + p(10)
			if t.hasDot {
				dotR := image.Rect(valLeft, tr.Max.Y-p(19), valLeft+p(10), tr.Max.Y-p(9))
				settingsFill(gtx, dotR, f.radius(3), t.swatch)
				settingsStroke(gtx, dotR, f.radius(3), 1, dotBorder)
				valLeft += p(15)
			}
			f.monoLabel(gtx, image.Rect(valLeft, tr.Max.Y-p(22), tr.Max.X-p(8), tr.Max.Y-p(5)), t.value, 11, font.SemiBold, scrimVal, layout.W)
		}
	}
}

func (f settingsFrame) appearancePresetsCard(gtx layout.Context, current SettingsAppearanceTarget, cfg theme.Config, _ theme.Resolved) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	presets := theme.Presets()
	if len(f.presetButtons) < len(presets) {
		grown := make([]widget.Clickable, len(presets))
		copy(grown, f.presetButtons)
		f.presetButtons = grown
	}

	activePresetID := cfg.Preset
	if current.ID != theme.ComponentGlobal {
		if ov, ok := cfg.Components[current.ID]; ok && ov.Custom && ov.Preset != "" {
			activePresetID = ov.Preset
		}
	}
	activeIdx := 0
	for i, pr := range presets {
		if pr.ID == activePresetID {
			activeIdx = i
			break
		}
	}
	activePreset := presets[activeIdx]

	applyPreset := func(presetID string) {
		if current.ID == theme.ComponentGlobal {
			cfg.Preset = presetID
			cfg.GlobalColors = theme.PartialPalette{}
		} else {
			cfg = cfg.Clone()
			ov := cfg.Components[current.ID]
			ov.Custom = true
			ov.Preset = presetID
			ov.Colors = theme.PartialPalette{}
			cfg.Components[current.ID] = ov
		}
		f.lastSyncKey = ""
		f.emitAppearance(cfg, true)
	}

	// Handle dropdown toggle and quick prev/next step buttons.
	if f.presetDropdown.Clicked(gtx) {
		f.presetMenuOpen = !f.presetMenuOpen
	}
	if len(presets) > 1 {
		if f.presetPrev.Clicked(gtx) {
			prevIdx := (activeIdx - 1 + len(presets)) % len(presets)
			applyPreset(presets[prevIdx].ID)
			activeIdx = prevIdx
			activePreset = presets[activeIdx]
		}
		if f.presetNext.Clicked(gtx) {
			nextIdx := (activeIdx + 1) % len(presets)
			applyPreset(presets[nextIdx].ID)
			activeIdx = nextIdx
			activePreset = presets[activeIdx]
		}
	}

	// Build grouped rows when the dropdown listbox is open.
	type dropdownRow struct {
		isHeader  bool
		headerZH  string
		headerEN  string
		presetIdx int
	}
	var rows []dropdownRow
	if f.presetMenuOpen {
		groups := []struct {
			id, zh, en string
		}{
			{"neutral", "旗舰深色 · 高级中性", "MODERN NEUTRAL DARK"},
			{"mood", "氛围深色 · 艺术与自然", "ATMOSPHERIC DARK"},
			{"light", "通透浅色 · 纸感与瓷白", "CRISP MODERN LIGHT"},
		}
		for _, g := range groups {
			hasAny := false
			for i, pr := range presets {
				if pr.Group == g.id {
					if !hasAny {
						rows = append(rows, dropdownRow{isHeader: true, headerZH: g.zh, headerEN: g.en})
						hasAny = true
					}
					rows = append(rows, dropdownRow{presetIdx: i})
				}
			}
		}
	}

	triggerY := p(46)
	triggerH := p(44)
	menuGap := 0
	menuH := 0
	if f.presetMenuOpen && len(rows) > 0 {
		menuGap = p(8)
		menuH = p(286)
	}
	styleY := triggerY + triggerH + menuGap + menuH + p(12)
	cardH := styleY + p(34) + p(14)

	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
	f.label(gtx, image.Rect(p(16), p(12), w-p(116), p(36)), f.heading("主题配色与玻璃材质", "Theme palette & glass material"), 15, font.SemiBold, f.p.Text, layout.W)
	f.monoLabel(gtx, image.Rect(w-p(112), p(14), w-p(16), p(34)), fmt.Sprintf(f.tr("%02d / %02d 套", "%02d / %02d"), activeIdx+1, len(presets)), 11, font.Medium, f.p.Secondary, layout.E)

	// Theme dropdown trigger bar + compact prev/next steppers.
	stepW := p(32)
	stepGap := p(6)
	dropRight := w - p(16) - (stepW*2 + stepGap*2)
	dropRect := image.Rect(p(16), triggerY, max(p(180), dropRight), triggerY+triggerH)
	dropR := f.radius(9)

	f.button(gtx, dropRect, &f.presetDropdown, f.tr("选择内置主题配色", "Select theme preset"), func(gtx layout.Context) {
		bounds := settingsBounds(gtx)
		bg := f.p.Background
		border := f.p.Line
		bw := unit.Dp(1)
		if f.presetMenuOpen {
			border = f.p.Accent
			bw = 1.5
		} else if f.presetDropdown.Hovered() {
			bg = f.p.Hover
			border = theme.Blend(f.p.Line, f.p.Accent, 0.55)
		}
		settingsFill(gtx, bounds, dropR, bg)
		settingsStroke(gtx, bounds, dropR, bw, border)

		// Mini 4-swatch theme capsule on the left.
		capRect := image.Rect(p(10), p(9), p(74), bounds.Max.Y-p(9))
		settingsFill(gtx, capRect, f.radius(6), activePreset.Palette.Background)
		settingsStroke(gtx, capRect, f.radius(6), 1, theme.WithAlpha(activePreset.Palette.Border, 210))
		dots := [4]color.NRGBA{
			activePreset.Palette.Surface,
			activePreset.Palette.Accent,
			activePreset.Palette.Weekend,
			activePreset.Palette.Text,
		}
		for j, dcol := range dots {
			dx := capRect.Min.X + p(6) + j*p(14)
			dy := (capRect.Min.Y + capRect.Max.Y - p(10)) / 2
			dr := image.Rect(dx, dy, dx+p(10), dy+p(10))
			settingsFill(gtx, dr, f.radius(3), dcol)
		}

		// Right-aligned chevron + tag pill + mode badge.
		chevR := image.Rect(bounds.Max.X-p(28), (bounds.Max.Y-p(14))/2, bounds.Max.X-p(12), (bounds.Max.Y+p(14))/2)
		chevIcon := "chevron-down"
		if f.presetMenuOpen {
			chevIcon = "chevron-up"
		}
		DrawIcon(gtx, chevR, chevIcon, f.p.Secondary)

		textRight := bounds.Max.X - p(34)
		if bounds.Dx() >= p(320) {
			tagRect := image.Rect(bounds.Max.X-p(86), p(12), bounds.Max.X-p(36), bounds.Max.Y-p(12))
			settingsFill(gtx, tagRect, f.radius(5), theme.WithAlpha(f.p.Accent, 34))
			f.monoLabel(gtx, tagRect, activePreset.Tag, 9.5, font.Bold, f.p.Accent, layout.Center)

			modeRect := image.Rect(bounds.Max.X-p(132), p(12), bounds.Max.X-p(92), bounds.Max.Y-p(12))
			modeLabel := f.tr("深色", "DARK")
			if !activePreset.Palette.IsDark() {
				modeLabel = f.tr("浅色", "LIGHT")
			}
			settingsFill(gtx, modeRect, f.radius(5), f.p.Surface)
			settingsStroke(gtx, modeRect, f.radius(5), 1, theme.WithAlpha(f.p.Line, 180))
			f.label(gtx, modeRect, modeLabel, 10, font.Medium, f.p.Secondary, layout.Center)
			textRight = modeRect.Min.X - p(8)
		}

		// Theme display title + English subtitle.
		title := fmt.Sprintf("%02d · %s", activeIdx+1, f.tr(activePreset.NameZH, activePreset.Name))
		if strings.HasPrefix(f.model.Locale, "zh") {
			f.label(gtx, image.Rect(p(84), p(5), textRight, p(24)), title, 13, font.SemiBold, f.p.Text, layout.W)
			f.monoLabel(gtx, image.Rect(p(84), p(23), textRight, p(39)), activePreset.Name, 10, font.Normal, f.p.Secondary, layout.W)
		} else {
			f.label(gtx, image.Rect(p(84), 0, textRight, bounds.Max.Y), title, 13, font.SemiBold, f.p.Text, layout.W)
		}
	})

	// Compact previous / next theme buttons beside the dropdown.
	stepBtns := [2]struct {
		btn  *widget.Clickable
		icon string
		desc string
		x0   int
	}{
		{&f.presetPrev, "chevron-left", f.tr("上一个主题", "Previous theme"), dropRect.Max.X + stepGap},
		{&f.presetNext, "chevron-right", f.tr("下一个主题", "Next theme"), dropRect.Max.X + stepGap*2 + stepW},
	}
	for _, sb := range stepBtns {
		sr := image.Rect(sb.x0, triggerY, sb.x0+stepW, triggerY+triggerH)
		f.button(gtx, sr, sb.btn, sb.desc, func(gtx layout.Context) {
			b := settingsBounds(gtx)
			bg, border, ink := f.p.Background, f.p.Line, f.p.Secondary
			if sb.btn.Hovered() || sb.btn.Pressed() {
				bg = f.p.Hover
				border = theme.Blend(f.p.Line, f.p.Accent, 0.55)
				ink = f.p.Text
			}
			settingsFill(gtx, b, dropR, bg)
			settingsStroke(gtx, b, dropR, 1, border)
			ic := image.Rect((b.Dx()-p(14))/2, (b.Dy()-p(14))/2, (b.Dx()+p(14))/2, (b.Dy()+p(14))/2)
			DrawIcon(gtx, ic, sb.icon, ink)
		})
	}

	// Expanded scrollable dropdown listbox containing all 15 grouped themes.
	if f.presetMenuOpen && len(rows) > 0 {
		menuRect := image.Rect(p(16), triggerY+triggerH+menuGap, w-p(16), triggerY+triggerH+menuGap+menuH)
		menuR := f.radius(10)
		settingsFill(gtx, menuRect, menuR, f.p.Background)
		settingsStroke(gtx, menuRect, menuR, 1, theme.Blend(f.p.Line, f.p.Accent, 0.45))

		innerRect := menuRect.Inset(p(6))
		settingsArea(gtx, innerRect, func(gtx layout.Context) {
			f.list(gtx, &f.presetList, len(rows), func(gtx layout.Context, rowIdx int) layout.Dimensions {
				row := rows[rowIdx]
				rw := gtx.Constraints.Max.X
				if row.isHeader {
					rh := p(24)
					if rowIdx > 0 {
						settingsFill(gtx, image.Rect(p(8), p(2), rw-p(12), p(2)+max(1, p(1))), 0, theme.WithAlpha(f.p.Line, 140))
					}
					f.monoLabel(gtx, image.Rect(p(10), p(5), rw-p(12), rh), f.tr(row.headerZH, row.headerEN), 9.5, font.SemiBold, f.p.Secondary, layout.W)
					return layout.Dimensions{Size: image.Pt(rw, rh)}
				}

				prIdx := row.presetIdx
				preset := presets[prIdx]
				b := &f.presetButtons[prIdx]
				if b.Clicked(gtx) {
					applyPreset(preset.ID)
					f.presetMenuOpen = false
				}
				rh := p(40)
				selected := preset.ID == activePreset.ID
				itemRect := image.Rect(p(2), p(1), rw-p(12), rh-p(1))
				f.button(gtx, itemRect, b, f.tr(preset.NameZH, preset.Name), func(gtx layout.Context) {
					bounds := settingsBounds(gtx)
					rowR := f.radius(7)
					if selected {
						settingsFill(gtx, bounds, rowR, f.p.Selected)
						settingsFill(gtx, image.Rect(p(2), p(8), p(5), bounds.Max.Y-p(8)), f.radius(2), f.p.Accent)
					} else if b.Hovered() {
						settingsFill(gtx, bounds, rowR, f.p.Hover)
					}

					// Mini theme swatch capsule rendered with THAT preset's own palette.
					swRect := image.Rect(p(12), p(7), p(68), bounds.Max.Y-p(7))
					settingsFill(gtx, swRect, f.radius(5), preset.Palette.Background)
					settingsStroke(gtx, swRect, f.radius(5), 1, theme.WithAlpha(preset.Palette.Border, 220))
					swDots := [3]color.NRGBA{preset.Palette.Surface, preset.Palette.Accent, preset.Palette.Weekend}
					for j, sc := range swDots {
						dx := swRect.Min.X + p(6) + j*p(15)
						dy := (swRect.Min.Y + swRect.Max.Y - p(10)) / 2
						settingsFill(gtx, image.Rect(dx, dy, dx+p(10), dy+p(10)), f.radius(3), sc)
					}

					rightEdge := bounds.Max.X - p(10)
					if selected {
						ck := image.Rect(rightEdge-p(16), (bounds.Max.Y-p(14))/2, rightEdge-p(2), (bounds.Max.Y+p(14))/2)
						DrawIcon(gtx, ck, "check", f.p.Accent)
						rightEdge -= p(24)
					}
					if bounds.Dx() >= p(300) {
						tagR := image.Rect(rightEdge-p(46), p(10), rightEdge, bounds.Max.Y-p(10))
						settingsFill(gtx, tagR, f.radius(4), theme.WithAlpha(preset.Palette.Accent, 36))
						f.monoLabel(gtx, tagR, preset.Tag, 9, font.Bold, preset.Palette.Accent, layout.Center)
						rightEdge = tagR.Min.X - p(8)
					}

					ink := f.p.Text
					wt := font.Medium
					if selected {
						ink = f.p.Accent
						wt = font.SemiBold
					}
					labelStr := fmt.Sprintf("%02d · %s", prIdx+1, f.tr(preset.NameZH, preset.Name))
					if strings.HasPrefix(f.model.Locale, "zh") {
						f.label(gtx, image.Rect(p(78), p(3), rightEdge, p(21)), labelStr, 12.5, wt, ink, layout.W)
						f.monoLabel(gtx, image.Rect(p(78), p(20), rightEdge, p(35)), preset.Name, 9.5, font.Normal, f.p.Secondary, layout.W)
					} else {
						f.label(gtx, image.Rect(p(78), 0, rightEdge, bounds.Max.Y), labelStr, 12.5, wt, ink, layout.W)
					}
				})
				return layout.Dimensions{Size: image.Pt(rw, rh)}
			})
		})
	}

	// Glass surface style selector row (Spatial Glass / Terminal HUD / Minimal Float).
	styles := [3]struct {
		id, zh, en string
	}{
		{theme.GlassSmoked, "空间毛玻璃", "Spatial Glass"},
		{theme.GlassWireframe, "终端准星线框", "Terminal HUD"},
		{theme.GlassMinimal, "极简无框悬浮", "Minimal Float"},
	}
	sGap := p(8)
	sW := (w - p(32) - sGap*2) / 3
	activeGlass := theme.NormalizeGlassStyle(cfg.GlassStyle)
	for i, st := range styles {
		b := &f.glassButtons[i]
		if b.Clicked(gtx) {
			cfg.GlassStyle = st.id
			f.lastSyncKey = ""
			f.emitAppearance(cfg, true)
		}
		x0 := p(16) + i*(sW+sGap)
		r := image.Rect(x0, styleY, x0+sW, styleY+p(34))
		active := activeGlass == st.id
		f.button(gtx, r, b, f.tr(st.zh, st.en), func(gtx layout.Context) {
			bg, border, ink := f.p.Background, f.p.Line, f.p.Secondary
			if active {
				bg, border, ink = f.p.Selected, f.p.Accent, f.p.Accent
				if f.model.HighContrast {
					ink = f.p.OnAccent
				}
			} else if b.Hovered() {
				bg, ink = f.p.Hover, f.p.Text
			}
			glassR := f.radius(7)
			settingsFill(gtx, settingsBounds(gtx), glassR, bg)
			settingsStroke(gtx, settingsBounds(gtx), glassR, 1, border)
			f.label(gtx, settingsBounds(gtx), f.tr(st.zh, st.en), 12, font.Medium, ink, layout.Center)
		})
	}
	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}

func (f settingsFrame) appearanceOpacityCard(gtx layout.Context, current SettingsAppearanceTarget, cfg theme.Config, resolved theme.Resolved) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	if !current.SupportsOpacity {
		cardH := p(66)
		f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
		f.label(gtx, image.Rect(p(16), p(10), w-p(16), p(34)), f.heading("背景透明度", "Background opacity"), 15, font.SemiBold, f.p.Text, layout.W)
		f.label(gtx, image.Rect(p(16), p(36), w-p(16), p(56)), f.tr("设置面板保持 100% 不透明以确保各项配置清晰易读。", "Settings window stays at 100% opacity for clear legibility."), 12, font.Normal, f.p.Secondary, layout.W)
		return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
	}

	minPct := 0
	if current.ID == theme.ComponentLauncher {
		minPct = theme.MinLauncherOpacity
	}
	applyOpacity := func(pct int, persist bool) {
		pct = max(minPct, min(100, pct))
		if current.ID == theme.ComponentGlobal {
			cfg.WidgetOpacity = pct
		} else {
			cfg = cfg.Clone()
			ov := cfg.Components[current.ID]
			ov.Custom = true
			v := pct
			ov.Opacity = &v
			cfg.Components[current.ID] = ov
		}
		f.opacitySlider.Value = float32(pct) / 100
		f.emitAppearance(cfg, persist)
	}

	cardH := p(134)
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
	f.label(gtx, image.Rect(p(16), p(10), w-p(110), p(34)), f.heading("背景透明度（透视桌面壁纸）", "Background opacity (wallpaper visibility)"), 15, font.SemiBold, f.p.Text, layout.W)
	f.label(gtx, image.Rect(p(16), p(34), w-p(110), p(52)), f.tr("仅淡化组件底色，文字与高亮保持 100% 锐利清晰；0% 为纯悬浮贴壁纸。", "Fades only the panel background while keeping text crisp; 0% floats directly over wallpaper."), 11.5, font.Normal, f.p.Secondary, layout.W)

	// Right-aligned live percentage pill.
	badge := image.Rect(w-p(94), p(14), w-p(16), p(40))
	badgeR := f.radius(7)
	settingsFill(gtx, badge, badgeR, f.p.Selected)
	settingsStroke(gtx, badge, badgeR, 1, theme.WithAlpha(f.p.Accent, 120))
	f.monoLabel(gtx, badge, fmt.Sprintf("%d%%", resolved.Opacity), 13, font.Bold, f.p.Accent, layout.Center)

	// Interactive Gio Slider.
	sliderArea := image.Rect(p(16), p(56), w-p(16), p(84))
	wasDragging := f.opacityDragging
	settingsArea(gtx, sliderArea, func(gtx layout.Context) {
		sl := material.Slider(f.theme, &f.opacitySlider)
		sl.Color = f.p.Accent
		sl.Layout(gtx)
	})
	f.opacityDragging = f.opacitySlider.Dragging()
	if f.opacitySlider.Update(gtx) {
		pct := int(math.Round(float64(f.opacitySlider.Value) * 100))
		if pct < minPct {
			pct = minPct
			f.opacitySlider.Value = float32(pct) / 100
		}
		applyOpacity(pct, !f.opacityDragging)
	} else if wasDragging && !f.opacityDragging {
		pct := int(math.Round(float64(f.opacitySlider.Value) * 100))
		applyOpacity(pct, true)
	}

	// Quick opacity preset chips.
	chips := [5]struct {
		pct    int
		zh, en string
	}{
		{0, "0% 纯悬浮", "0% Float"},
		{35, "35% 通透", "35% Airy"},
		{65, "65% 平衡", "65% Glass"},
		{85, "85% 深邃", "85% Deep"},
		{100, "100% 实色", "100% Solid"},
	}
	if current.ID == theme.ComponentLauncher {
		chips = [5]struct {
			pct    int
			zh, en string
		}{
			{45, "45%", "45%"},
			{65, "65%", "65%"},
			{80, "80%", "80%"},
			{94, "94% 默认", "94% Default"},
			{100, "100% 实色", "100% Solid"},
		}
	}
	gap := p(6)
	chipW := (w - p(32) - gap*4) / 5
	for i, chip := range chips {
		b := &f.opacityChips[i]
		if b.Clicked(gtx) {
			applyOpacity(chip.pct, true)
		}
		x0 := p(16) + i*(chipW+gap)
		r := image.Rect(x0, p(92), x0+chipW, p(120))
		active := resolved.Opacity == chip.pct
		f.button(gtx, r, b, f.tr(chip.zh, chip.en), func(gtx layout.Context) {
			bg, border, ink := f.p.Background, f.p.Line, f.p.Secondary
			if active {
				bg, border, ink = f.p.Selected, f.p.Accent, f.p.Accent
				if f.model.HighContrast {
					ink = f.p.OnAccent
				}
			} else if b.Hovered() {
				bg, ink = f.p.Hover, f.p.Text
			}
			chipR := f.radius(6)
			settingsFill(gtx, settingsBounds(gtx), chipR, bg)
			settingsStroke(gtx, settingsBounds(gtx), chipR, 1, border)
			f.monoLabel(gtx, settingsBounds(gtx), f.tr(chip.zh, chip.en), 11, font.Medium, ink, layout.Center)
		})
	}
	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}

func (f settingsFrame) appearanceRadiusCard(gtx layout.Context, cfg theme.Config, resolved theme.Resolved) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	applyRadius := func(radius int, persist bool) {
		radius = max(theme.MinCornerRadius, min(theme.MaxCornerRadius, radius))
		cfg.SetCornerRadius(radius)
		f.radiusSlider.Value = float32(radius) / float32(theme.MaxCornerRadius)
		f.emitAppearance(cfg, persist)
	}

	if f.titleBarToggle.Clicked(gtx) {
		cfg.TitleAccentBar = !cfg.TitleAccentBar
		f.lastSyncKey = ""
		f.emitAppearance(cfg, true)
	}

	cardH := p(184)
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
	f.label(gtx, image.Rect(p(16), p(10), w-p(110), p(34)), f.heading("全局圆角与装饰", "Global corners & accents"), 15, font.SemiBold, f.p.Text, layout.W)
	f.label(gtx, image.Rect(p(16), p(34), w-p(110), p(52)), f.tr("左右拖动滑块实时调整桌面组件、快速启动、弹窗、菜单与控件的圆角；0px 为纯直角。", "Drag left or right to tune rounding across widgets, launcher, dialogs, menus, and controls; 0px is sharp."), 11.5, font.Normal, f.p.Secondary, layout.W)

	// Right-aligned live corner radius pill.
	badge := image.Rect(w-p(94), p(14), w-p(16), p(40))
	badgeR := f.radius(7)
	settingsFill(gtx, badge, badgeR, f.p.Selected)
	settingsStroke(gtx, badge, badgeR, 1, theme.WithAlpha(f.p.Accent, 120))
	f.monoLabel(gtx, badge, fmt.Sprintf("%d px", resolved.CornerRadius), 13, font.Bold, f.p.Accent, layout.Center)

	// Interactive Gio horizontal slider.
	sliderArea := image.Rect(p(16), p(56), w-p(16), p(84))
	wasDragging := f.radiusDragging
	settingsArea(gtx, sliderArea, func(gtx layout.Context) {
		sl := material.Slider(f.theme, &f.radiusSlider)
		sl.Color = f.p.Accent
		sl.Layout(gtx)
	})
	f.radiusDragging = f.radiusSlider.Dragging()
	if f.radiusSlider.Update(gtx) {
		rad := int(math.Round(float64(f.radiusSlider.Value) * float64(theme.MaxCornerRadius)))
		applyRadius(rad, !f.radiusDragging)
	} else if wasDragging && !f.radiusDragging {
		rad := int(math.Round(float64(f.radiusSlider.Value) * float64(theme.MaxCornerRadius)))
		applyRadius(rad, true)
	}

	// Quick corner radius preset chips.
	chips := [5]struct {
		rad    int
		zh, en string
	}{
		{0, "0px 直角", "0px Sharp"},
		{4, "4px 利落", "4px Crisp"},
		{8, "8px 微圆", "8px Subtle"},
		{12, "12px 标准", "12px Default"},
		{18, "18px 柔和", "18px Soft"},
	}
	gap := p(6)
	chipW := (w - p(32) - gap*4) / 5
	chipR := f.radius(6)
	for i, chip := range chips {
		b := &f.radiusChips[i]
		if b.Clicked(gtx) {
			applyRadius(chip.rad, true)
		}
		x0 := p(16) + i*(chipW+gap)
		r := image.Rect(x0, p(92), x0+chipW, p(120))
		active := resolved.CornerRadius == chip.rad
		f.button(gtx, r, b, f.tr(chip.zh, chip.en), func(gtx layout.Context) {
			bg, border, ink := f.p.Background, f.p.Line, f.p.Secondary
			if active {
				bg, border, ink = f.p.Selected, f.p.Accent, f.p.Accent
				if f.model.HighContrast {
					ink = f.p.OnAccent
				}
			} else if b.Hovered() {
				bg, ink = f.p.Hover, f.p.Text
			}
			settingsFill(gtx, settingsBounds(gtx), chipR, bg)
			settingsStroke(gtx, settingsBounds(gtx), chipR, 1, border)
			f.monoLabel(gtx, settingsBounds(gtx), f.tr(chip.zh, chip.en), 11, font.Medium, ink, layout.Center)
		})
	}

	// Divider + Title accent bar toggle row.
	settingsFill(gtx, image.Rect(p(16), p(130), w-p(16), p(130)+max(1, p(1))), 0, f.p.Line)
	f.label(gtx, image.Rect(p(16), p(136), w-p(92), p(156)), f.tr("标题栏左侧装饰竖线", "Title bar accent bar"), 13, font.Medium, f.p.Text, layout.W)
	f.label(gtx, image.Rect(p(16), p(156), w-p(92), p(174)), f.tr("在桌面组件、弹窗和插件窗口标题左侧显示主题色竖条。", "Show a colored vertical bar beside widget, dialog, and app window titles."), 11.5, font.Normal, f.p.Secondary, layout.W)
	toggleLabel := f.tr("标题栏左侧装饰竖线", "Title bar accent bar")
	f.button(gtx, image.Rect(w-p(74), p(136), w-p(16), p(176)), &f.titleBarToggle, toggleLabel, func(gtx layout.Context) {
		semantic.Switch.Add(gtx.Ops)
		semantic.SelectedOp(resolved.TitleAccentBar).Add(gtx.Ops)
		mid := gtx.Constraints.Max.Div(2)
		track := image.Rect(mid.X-p(18), mid.Y-p(10), mid.X+p(18), mid.Y+p(10))
		knob := f.p.OnAccent
		at := track.Max.X - p(10)
		if !resolved.TitleAccentBar {
			settingsFill(gtx, track, 10, f.p.Secondary)
			settingsFill(gtx, track.Inset(max(1, p(1))), 9, f.p.Surface)
			knob, at = f.p.Secondary, track.Min.X+p(10)
		} else {
			settingsFill(gtx, track, 10, f.p.Accent)
		}
		settingsFill(gtx, image.Rect(at-p(6), mid.Y-p(6), at+p(6), mid.Y+p(6)), 6, knob)
	})
	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}

func (f settingsFrame) appearanceIconSizeCard(gtx layout.Context, cfg theme.Config, resolved theme.Resolved) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	span := float64(theme.MaxIconSize - theme.MinIconSize)
	applyIconSize := func(size int, persist bool) {
		size = max(theme.MinIconSize, min(theme.MaxIconSize, size))
		cfg.SetIconSize(size)
		f.iconSizeSlider.Value = float32(size-theme.MinIconSize) / float32(theme.MaxIconSize-theme.MinIconSize)
		f.emitAppearance(cfg, persist)
	}

	metrics := ComputeIconMetrics(resolved.IconSize)
	cardH := p(134)
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)

	headRight := w - p(110)
	if w >= p(500) {
		headRight = w - p(214)
		listPill := image.Rect(w-p(204), p(16), w-p(100), p(38))
		pillR := f.radius(6)
		settingsFill(gtx, listPill, pillR, f.p.Background)
		settingsStroke(gtx, listPill, pillR, 1, f.p.Line)
		f.monoLabel(gtx, listPill, fmt.Sprintf(f.tr("列表 %dpx", "LIST %dpx"), metrics.ListIconDp), 11, font.Medium, f.p.Secondary, layout.Center)
	}
	f.label(gtx, image.Rect(p(16), p(10), headRight, p(34)), f.heading("桌面图标尺寸", "Desktop icon size"), 15, font.SemiBold, f.p.Text, layout.W)
	f.label(gtx, image.Rect(p(16), p(34), headRight, p(52)), f.tr("设置全局默认图标尺寸；在单个桌面分组上按住 Ctrl + 鼠标滚轮可独立缩放该分组，列表图标自动联动。", "Sets the global default icon size; hold Ctrl + scroll on a desktop group to scale that group independently."), 11.5, font.Normal, f.p.Secondary, layout.W)

	// Right-aligned live icon size pill.
	badge := image.Rect(w-p(94), p(14), w-p(16), p(40))
	badgeR := f.radius(7)
	settingsFill(gtx, badge, badgeR, f.p.Selected)
	settingsStroke(gtx, badge, badgeR, 1, theme.WithAlpha(f.p.Accent, 120))
	f.monoLabel(gtx, badge, fmt.Sprintf("%d px", resolved.IconSize), 13, font.Bold, f.p.Accent, layout.Center)

	// Interactive Gio horizontal slider.
	sliderArea := image.Rect(p(16), p(56), w-p(16), p(84))
	wasDragging := f.iconSizeDragging
	settingsArea(gtx, sliderArea, func(gtx layout.Context) {
		sl := material.Slider(f.theme, &f.iconSizeSlider)
		sl.Color = f.p.Accent
		sl.Layout(gtx)
	})
	f.iconSizeDragging = f.iconSizeSlider.Dragging()
	if f.iconSizeSlider.Update(gtx) {
		sz := theme.MinIconSize + int(math.Round(float64(f.iconSizeSlider.Value)*span))
		applyIconSize(sz, !f.iconSizeDragging)
	} else if wasDragging && !f.iconSizeDragging {
		sz := theme.MinIconSize + int(math.Round(float64(f.iconSizeSlider.Value)*span))
		applyIconSize(sz, true)
	}

	// Quick icon size preset chips.
	chips := [5]struct {
		size   int
		zh, en string
	}{
		{22, "22px 极简", "22px Micro"},
		{28, "28px 小图标", "28px Small"},
		{40, "40px 标准", "40px Medium"},
		{52, "52px 大图标", "52px Large"},
		{66, "66px 超大", "66px XL"},
	}
	gap := p(6)
	chipW := (w - p(32) - gap*4) / 5
	chipR := f.radius(6)
	for i, chip := range chips {
		b := &f.iconSizeChips[i]
		if b.Clicked(gtx) {
			applyIconSize(chip.size, true)
		}
		x0 := p(16) + i*(chipW+gap)
		r := image.Rect(x0, p(92), x0+chipW, p(120))
		active := resolved.IconSize == chip.size
		f.button(gtx, r, b, f.tr(chip.zh, chip.en), func(gtx layout.Context) {
			bg, border, ink := f.p.Background, f.p.Line, f.p.Secondary
			if active {
				bg, border, ink = f.p.Selected, f.p.Accent, f.p.Accent
				if f.model.HighContrast {
					ink = f.p.OnAccent
				}
			} else if b.Hovered() {
				bg, ink = f.p.Hover, f.p.Text
			}
			settingsFill(gtx, settingsBounds(gtx), chipR, bg)
			settingsStroke(gtx, settingsBounds(gtx), chipR, 1, border)
			f.monoLabel(gtx, settingsBounds(gtx), f.tr(chip.zh, chip.en), 11, font.Medium, ink, layout.Center)
		})
	}
	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}

func (f settingsFrame) appearanceColorsCard(gtx layout.Context, current SettingsAppearanceTarget, cfg theme.Config, resolved theme.Resolved) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	slots := []struct {
		slot   theme.ColorSlot
		zh, en string
	}{
		{theme.SlotBackground, "底层背景", "Background"},
		{theme.SlotSurface, "面板表面", "Surface"},
		{theme.SlotBorder, "描边线条", "Border"},
		{theme.SlotText, "主文字", "Primary Ink"},
		{theme.SlotSecondary, "次级文字", "Muted Ink"},
		{theme.SlotAccent, "强调高亮", "Accent"},
		{theme.SlotWeekend, "周末强调", "Weekend"},
	}
	rowH := p(46)
	cardH := p(50) + len(slots)*rowH + p(10)
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
	f.label(gtx, image.Rect(p(16), p(12), w-p(16), p(36)), f.heading("组件自定义配色（支持 Hex 输入与快捷点选）", "Custom component palette (Hex input & quick swatches)"), 15, font.SemiBold, f.p.Text, layout.W)

	applySlotHex := func(slot theme.ColorSlot, hex string) {
		col, ok := theme.ParseHex(hex)
		if !ok {
			return
		}
		formatted := theme.FormatHex(col)
		if current.ID == theme.ComponentGlobal {
			cfg.GlobalColors.Set(slot, formatted)
		} else {
			cfg = cfg.Clone()
			ov := cfg.Components[current.ID]
			ov.Custom = true
			ov.Colors.Set(slot, formatted)
			cfg.Components[current.ID] = ov
		}
		f.lastSyncKey = ""
		f.emitAppearance(cfg, true)
	}

	for i, item := range slots {
		y := p(46) + i*rowH
		if i > 0 {
			settingsFill(gtx, image.Rect(p(16), y, w-p(16), y+max(1, p(1))), 0, f.p.Line)
		}
		col := resolved.Palette.SlotColor(item.slot)
		swatch := image.Rect(p(16), y+p(11), p(40), y+p(35))
		swatchR := f.radius(6)
		settingsFill(gtx, swatch, swatchR, col)
		settingsStroke(gtx, swatch, swatchR, 1, f.p.Line)

		f.label(gtx, image.Rect(p(48), y+p(5), p(156), y+p(25)), f.tr(item.zh, item.en), 12.5, font.Medium, f.p.Text, layout.W)
		f.monoLabel(gtx, image.Rect(p(48), y+p(24), p(156), y+p(40)), strings.ToUpper(string(item.slot)), 9.5, font.Normal, f.p.Secondary, layout.W)

		ed := f.hexEditors[item.slot]
		if ed == nil {
			ed = &widget.Editor{SingleLine: true, Submit: true, MaxLen: 9}
			ed.SetText(theme.FormatHex(col))
			f.hexEditors[item.slot] = ed
		}
		applyBtn := f.hexApplyButtons[item.slot]
		if applyBtn == nil {
			applyBtn = &widget.Clickable{}
			f.hexApplyButtons[item.slot] = applyBtn
		}
		submitted := applyBtn.Clicked(gtx)
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				submitted = true
			}
		}
		if submitted {
			applySlotHex(item.slot, ed.Text())
		}

		// Hex editor field
		field := image.Rect(p(158), y+p(9), p(246), y+p(37))
		f.box(gtx, field, 6, f.p.Background)
		if gtx.Focused(ed) {
			settingsStroke(gtx, field, f.radius(6), 1, f.p.Accent)
		}
		settingsArea(gtx, field.Inset(p(4)), func(gtx layout.Context) {
			layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.Y = 0
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				st := material.Editor(f.theme, ed, "#CBA6F7")
				st.TextSize = 12
				st.Font.Typeface = f.monoTypeface()
				st.Color, st.HintColor = f.p.Text, f.p.Secondary
				return st.Layout(gtx)
			})
		})
		f.textButton(gtx, image.Rect(p(252), y+p(9), p(300), y+p(37)), applyBtn, f.tr("应用", "Set"), false)

		// Quick-pick swatches on the right
		swatches := theme.QuickSwatches(item.slot)
		sx := p(314)
		for j, hex := range swatches {
			if sx+p(20) > w-p(14) {
				break
			}
			key := string(item.slot) + ":" + hex
			sb := f.swatchButtons[key]
			if sb == nil {
				sb = &widget.Clickable{}
				f.swatchButtons[key] = sb
			}
			if sb.Clicked(gtx) {
				applySlotHex(item.slot, hex)
			}
			sc, _ := theme.ParseHex(hex)
			sr := image.Rect(sx, y+p(13), sx+p(20), y+p(33))
			active := strings.EqualFold(theme.FormatHex(col), hex)
			f.button(gtx, sr, sb, fmt.Sprintf("%s %d %s", item.slot, j, hex), func(gtx layout.Context) {
				b := settingsBounds(gtx)
				rChip := f.radius(5)
				settingsFill(gtx, b, rChip, sc)
				borderCol := f.p.Line
				bw := unit.Dp(1)
				if active || sb.Hovered() {
					borderCol = f.p.Accent
					bw = 2
				}
				settingsStroke(gtx, b, rChip, bw, borderCol)
			})
			sx += p(25)
		}
	}
	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}
