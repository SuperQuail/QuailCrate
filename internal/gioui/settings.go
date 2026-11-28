package gioui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"quailcrate/internal/pluginapps"
	"quailcrate/internal/theme"
)

type settingsPage int

const (
	generalPage settingsPage = iota
	appearancePage
	desktopPage
	commandsPage
	pluginsPage
)

var settingsPageIDs = [...]string{PageGeneral, PageAppearance, PageDesktop, PageCommands, PagePlugins}

// SettingsView owns input state across frames. Use it on the window's event loop.
type SettingsView struct {
	extraPage                      string
	extraNav                       map[string]*widget.Clickable
	sectionList                    widget.List
	sidebarList                    widget.List
	sectionListPage                string
	page                           settingsPage
	theme                          *material.Theme
	nav                            [5]widget.Clickable
	appsToggle                     widget.Clickable
	appsExpanded                   bool
	appsExpandedInit               bool
	appsList                       widget.List
	appTabButtons                  map[[2]string]*widget.Clickable
	goPluginsButton                widget.Clickable
	winMin, winMax, winClose       widget.Clickable
	languages                      [3]widget.Clickable
	apply, edit, rescan, directory widget.Clickable
	installPlugin                  widget.Clickable
	generalList                    widget.List
	autostartToggle                widget.Clickable
	autostartRetry                 widget.Clickable
	autoUpdateToggle               widget.Clickable
	checkUpdateButton              widget.Clickable
	restartUpdateButton            widget.Clickable
	releaseLinkButton              widget.Clickable
	shortcut                       widget.Editor
	lastHotkey                     string
	pluginsList, commandsList      widget.List
	appearanceList                 widget.List
	pluginButtons                  map[string]*settingsPluginButtons
	expandedPluginPerms            string
	commandButtons                 map[[2]string]*widget.Clickable

	// Appearance Studio state
	appearanceTarget string
	targetButtons    map[string]*widget.Clickable
	presetDropdown   widget.Clickable
	presetPrev       widget.Clickable
	presetNext       widget.Clickable
	presetList       widget.List
	presetMenuOpen   bool
	presetButtons    []widget.Clickable
	glassButtons     [3]widget.Clickable
	opacitySlider    widget.Float
	opacityDragging  bool
	opacityChips     [5]widget.Clickable
	radiusSlider     widget.Float
	radiusDragging   bool
	radiusChips      [5]widget.Clickable
	iconSizeSlider   widget.Float
	iconSizeDragging bool
	iconSizeChips    [5]widget.Clickable
	titleBarToggle   widget.Clickable
	customToggle     widget.Clickable
	resetComponent   widget.Clickable
	hexEditors       map[theme.ColorSlot]*widget.Editor
	hexApplyButtons  map[theme.ColorSlot]*widget.Clickable
	swatchButtons    map[string]*widget.Clickable
	lastSyncKey      string
}

type settingsPluginButtons struct {
	toggle, copy, permToggle widget.Clickable
	homepage, uninstall      widget.Clickable
	revoke                   map[string]*widget.Clickable
}

func NewSettingsView() *SettingsView    { return &SettingsView{} }
func (s *SettingsView) ShowAppearance() { s.SetPage(PageAppearance) }
func (s *SettingsView) ShowDesktop()    { s.SetPage(PageDesktop) }
func (s *SettingsView) ShowCommands()   { s.SetPage(PageCommands) }
func (s *SettingsView) ShowPlugins()    { s.SetPage(PagePlugins) }
func (s *SettingsView) CurrentPage() string {
	if s.extraPage != "" {
		return s.extraPage
	}
	return settingsPageIDs[s.page]
}
func (s *SettingsView) AppsExpanded() bool {
	return s.appsExpanded
}
func (s *SettingsView) SetAppsExpanded(expanded bool) {
	s.appsExpanded = expanded
	s.appsExpandedInit = true
}
func (s *SettingsView) SetPage(page string) {
	for i, id := range settingsPageIDs {
		if id == page {
			s.extraPage = ""
			s.page = settingsPage(i)
			return
		}
	}
	s.extraPage = page
}

type SettingsPalette struct {
	Background, Surface, Sidebar, Text, Secondary, Line, Accent, Selected, Hover, Weekend, Warning, WarningBg, OnAccent color.NRGBA
}

func SettingsPaletteFromTheme(p theme.Palette) SettingsPalette {
	return SettingsPalette{
		Background: p.Background,
		Surface:    p.Surface,
		Sidebar:    p.Sidebar,
		Text:       p.Text,
		Secondary:  p.Secondary,
		Line:       p.Border,
		Accent:     p.Accent,
		Selected:   p.Selected,
		Hover:      p.Hover,
		Weekend:    p.Weekend,
		Warning:    p.Warning,
		WarningBg:  p.WarningBg,
		OnAccent:   p.OnAccent,
	}
}

func settingsColor(value uint32) color.NRGBA {
	return color.NRGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 255}
}

var defaultSettings = SettingsPaletteFromTheme(theme.DefaultPalette())

type settingsFrame struct {
	cardTitle string
	selection *appearanceSelection
	*SettingsView
	model SettingsModel
	p     SettingsPalette
	emit  func(SettingsAction)
}

func (f settingsFrame) tr(zh, en string) string {
	if strings.HasPrefix(f.model.Locale, "zh") {
		return zh
	}
	return en
}

func (f settingsFrame) isDark() bool {
	return theme.Luminance(f.p.Surface) < 0.45
}

// DisambiguateTabLabels returns display titles for plugin tabs in snapshot order,
// appending the plugin name whenever multiple tabs share the same title.
func DisambiguateTabLabels(tabs []pluginapps.Tab) []string {
	counts := make(map[string]int, len(tabs))
	for _, t := range tabs {
		counts[t.Title]++
	}
	out := make([]string, len(tabs))
	for i, t := range tabs {
		if counts[t.Title] > 1 && t.PluginName != "" {
			out[i] = t.Title + " · " + t.PluginName
		} else {
			out[i] = t.Title
		}
	}
	return out
}

func (s *SettingsView) Layout(gtx layout.Context, m SettingsModel, emit func(SettingsAction)) layout.Dimensions {
	if s.theme == nil {
		s.theme = material.NewTheme()
		s.theme.Shaper = NewTextShaper()
		s.shortcut.SingleLine, s.shortcut.Submit = true, true
		s.shortcut.MaxLen = 64
		s.pluginsList.Axis, s.commandsList.Axis, s.appearanceList.Axis, s.appsList.Axis, s.presetList.Axis, s.generalList.Axis =
			layout.Vertical, layout.Vertical, layout.Vertical, layout.Vertical, layout.Vertical, layout.Vertical
		s.presetButtons = make([]widget.Clickable, len(theme.Presets()))
		s.pluginButtons = make(map[string]*settingsPluginButtons)
		s.commandButtons = make(map[[2]string]*widget.Clickable)
		s.appTabButtons = make(map[[2]string]*widget.Clickable)
		s.extraNav = make(map[string]*widget.Clickable)
		s.sectionList.Axis = layout.Vertical
		s.sidebarList.Axis = layout.Vertical
		s.targetButtons = make(map[string]*widget.Clickable)
		s.hexEditors = make(map[theme.ColorSlot]*widget.Editor)
		s.hexApplyButtons = make(map[theme.ColorSlot]*widget.Clickable)
		s.swatchButtons = make(map[string]*widget.Clickable)
		s.appearanceTarget = theme.ComponentGlobal
	}
	if !s.appsExpandedInit {
		s.appsExpanded = m.AppsExpanded
		s.appsExpandedInit = true
	}
	if m.Hotkey != s.lastHotkey {
		s.shortcut.SetText(m.Hotkey)
		s.lastHotkey = m.Hotkey
	}
	if emit == nil {
		emit = func(SettingsAction) {}
	}
	if m.Appearance.Preset == "" {
		m.Appearance = theme.DefaultConfig()
	} else {
		m.Appearance = m.Appearance.Normalize()
	}
	f := settingsFrame{SettingsView: s, model: m, p: defaultSettings, emit: emit}
	if m.Palette != nil {
		f.p = *m.Palette
	} else {
		f.p = SettingsPaletteFromTheme(m.Appearance.Resolve(theme.ComponentSettings).Palette)
	}
	width, height := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	sections := f.sections()
	if m.Definition != nil && m.Definition.Section(s.CurrentPage()) == nil {
		s.SetPage(sections[0].ID)
	}
	for _, section := range sections {
		for f.navButton(section.ID).Clicked(gtx) {
			s.SetPage(section.ID)
		}
	}
	paint.Fill(gtx.Ops, theme.WithAlpha(f.p.Background, 255))
	side := gtx.Dp(196)
	if width < gtx.Dp(850) {
		side = gtx.Dp(156)
	}
	if width < gtx.Dp(660) {
		side = gtx.Dp(62)
	}
	lineW := max(1, gtx.Dp(1))
	settingsFill(gtx, image.Rect(0, 0, side, height), 0, theme.WithAlpha(f.p.Sidebar, 255))
	settingsArea(gtx, image.Rect(0, 0, side, height), f.sidebar)
	contentTop := 0
	if m.CustomTitleBar {
		barH := gtx.Dp(36)
		contentTop = barH
		settingsArea(gtx, image.Rect(side, 0, width, barH), f.titleBar)
	}
	settingsArea(gtx, image.Rect(side, contentTop, width, height), f.content)
	settingsFill(gtx, image.Rect(side-lineW, 0, side, height), 0, theme.WithAlpha(f.p.Line, 255))
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

func (f settingsFrame) titleBar(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	if f.winMin.Clicked(gtx) {
		f.emit(SettingsAction{Kind: WindowMinimize})
	}
	if f.winMax.Clicked(gtx) {
		f.emit(SettingsAction{Kind: WindowToggleMaximize})
	}
	if f.winClose.Clicked(gtx) {
		f.emit(SettingsAction{Kind: WindowClose})
	}

	btnW := p(42)
	controlsLeft := max(0, w-btnW*3)

	if controlsLeft > 0 {
		dragClip := clip.Rect(image.Rect(0, 0, controlsLeft, h)).Push(gtx.Ops)
		system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
		dragClip.Pop()
		section := [...]string{"GENERAL", "APPEARANCE", "DESKTOP", "COMMANDS", "PLUGINS"}[f.page]
		if declared := f.model.Definition.Section(f.CurrentPage()); declared != nil {
			section = declared.Title
		}
		f.monoLabel(gtx, image.Rect(p(24), 0, controlsLeft-p(8), h), "QUAILCRATE // "+section, 10.5, font.Medium, f.p.Secondary, layout.W)
	}

	maxIcon := "win-max"
	maxLabel := f.tr("最大化", "Maximize")
	if f.model.Maximized {
		maxIcon = "win-restore"
		maxLabel = f.tr("还原", "Restore")
	}
	winButtons := [3]struct {
		btn    *widget.Clickable
		icon   string
		label  string
		danger bool
	}{
		{&f.winMin, "win-min", f.tr("最小化", "Minimize"), false},
		{&f.winMax, maxIcon, maxLabel, false},
		{&f.winClose, "win-close", f.tr("关闭", "Close"), true},
	}
	for i, wb := range winButtons {
		r := image.Rect(controlsLeft+i*btnW, 0, controlsLeft+(i+1)*btnW, h)
		f.button(gtx, r, wb.btn, wb.label, func(gtx layout.Context) {
			bounds := settingsBounds(gtx)
			ink := f.p.Secondary
			if wb.btn.Hovered() || wb.btn.Pressed() {
				if wb.danger {
					settingsFill(gtx, bounds, 0, f.p.Warning)
					ink = f.p.OnAccent
				} else {
					settingsFill(gtx, bounds, 0, f.p.Hover)
					ink = f.p.Text
				}
			}
			icSize := p(14)
			icR := image.Rect((bounds.Dx()-icSize)/2, (bounds.Dy()-icSize)/2, (bounds.Dx()+icSize)/2, (bounds.Dy()+icSize)/2)
			DrawIcon(gtx, icR, wb.icon, ink)
		})
	}
	settingsFill(gtx, image.Rect(0, h-max(1, p(1)), w, h), 0, theme.Blend(f.p.Background, f.p.Line, 0.65))
}

// settingsArea gives a child local pixel coordinates and clips both drawing and input.
func settingsArea(gtx layout.Context, r image.Rectangle, render func(layout.Context)) {
	if r.Empty() {
		return
	}
	transform := op.Offset(r.Min).Push(gtx.Ops)
	defer transform.Pop()
	area := clip.Rect{Max: r.Size()}.Push(gtx.Ops)
	defer area.Pop()
	gtx.Constraints = layout.Exact(r.Size())
	render(gtx)
}

func settingsFill(gtx layout.Context, r image.Rectangle, radius unit.Dp, c color.NRGBA) {
	if r.Empty() || c.A == 0 {
		return
	}
	rad := min(gtx.Dp(radius), r.Dx()/2, r.Dy()/2)
	if rad <= 0 {
		paint.FillShape(gtx.Ops, c, clip.Rect(r).Op())
		return
	}
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, rad).Op(gtx.Ops))
}

func settingsStroke(gtx layout.Context, r image.Rectangle, radius unit.Dp, width unit.Dp, c color.NRGBA) {
	if r.Empty() || c.A == 0 {
		return
	}
	w := max(1, gtx.Dp(width))
	inset := r.Inset(max(1, w/2))
	if inset.Empty() {
		inset = r
	}
	rad := min(max(0, gtx.Dp(radius)-w/2), inset.Dx()/2, inset.Dy()/2)
	shape := clip.UniformRRect(inset, rad).Path(gtx.Ops)
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: shape, Width: float32(w)}.Op())
}

func settingsBounds(gtx layout.Context) image.Rectangle {
	return image.Rectangle{Max: gtx.Constraints.Max}
}

func (f settingsFrame) radius(baseDp int) unit.Dp {
	return unit.Dp(theme.ScaleRadius(f.model.Appearance.EffectiveCornerRadius(), baseDp))
}

func (f settingsFrame) box(gtx layout.Context, r image.Rectangle, radius unit.Dp, bg color.NRGBA) {
	scaled := f.radius(int(radius))
	settingsFill(gtx, r, scaled, bg)
	settingsStroke(gtx, r, scaled, 1, theme.WithAlpha(f.p.Line, 210))
}

func (f settingsFrame) label(gtx layout.Context, r image.Rectangle, value string, size unit.Sp, weight font.Weight, c color.NRGBA, align layout.Direction) {
	f.textLabel(gtx, r, value, size, weight, f.typeface(), c, align)
}

func (f settingsFrame) monoLabel(gtx layout.Context, r image.Rectangle, value string, size unit.Sp, weight font.Weight, c color.NRGBA, align layout.Direction) {
	f.textLabel(gtx, r, value, size, weight, f.monoTypeface(), c, align)
}

func (f settingsFrame) textLabel(gtx layout.Context, r image.Rectangle, value string, size unit.Sp, weight font.Weight, tf font.Typeface, c color.NRGBA, align layout.Direction) {
	settingsArea(gtx, r, func(gtx layout.Context) {
		align.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			l := material.Label(f.theme, size, value)
			l.Color, l.Font.Weight, l.MaxLines = c, weight, 1
			l.Font.Typeface = tf
			return l.Layout(gtx)
		})
	})
}

func (f settingsFrame) typeface() font.Typeface {
	if strings.HasPrefix(f.model.Locale, "zh") {
		return "Microsoft YaHei UI, Segoe UI Variable Text, PingFang SC, Noto Sans CJK SC, sans-serif"
	}
	return "Segoe UI Variable Text, Segoe UI, Helvetica Neue, Noto Sans, sans-serif"
}

func (f settingsFrame) monoTypeface() font.Typeface {
	return "Cascadia Mono, JetBrains Mono, Consolas, Microsoft YaHei UI, monospace"
}

func (f settingsFrame) button(gtx layout.Context, r image.Rectangle, b *widget.Clickable, desc string, draw func(layout.Context)) {
	settingsArea(gtx, r, func(gtx layout.Context) {
		b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.Button.Add(gtx.Ops)
			semantic.DescriptionOp(desc).Add(gtx.Ops)
			draw(gtx)
			if gtx.Focused(b) {
				shape := clip.UniformRRect(settingsBounds(gtx).Inset(gtx.Dp(2)), gtx.Dp(f.radius(6))).Path(gtx.Ops)
				paint.FillShape(gtx.Ops, f.p.Accent, clip.Stroke{Path: shape, Width: float32(max(1, gtx.Dp(2)))}.Op())
			}
			return layout.Dimensions{Size: gtx.Constraints.Max}
		})
	})
}

func (f settingsFrame) textButton(gtx layout.Context, r image.Rectangle, b *widget.Clickable, title string, primary bool) {
	f.textButtonWithDescription(gtx, r, b, title, title, primary, false)
}

func (f settingsFrame) textButtonWithDescription(gtx layout.Context, r image.Rectangle, b *widget.Clickable, title, description string, primary, disabled bool) {
	btnR := f.radius(7)
	if disabled {
		settingsArea(gtx, r, func(gtx layout.Context) {
			semantic.Button.Add(gtx.Ops)
			semantic.DescriptionOp(description).Add(gtx.Ops)
			semantic.EnabledOp(false).Add(gtx.Ops)
			settingsFill(gtx, settingsBounds(gtx), btnR, f.p.Background)
			settingsStroke(gtx, settingsBounds(gtx), btnR, 1, f.p.Line)
			f.label(gtx, settingsBounds(gtx), title, 13, font.Medium, f.p.Secondary, layout.Center)
		})
		return
	}
	f.button(gtx, r, b, description, func(gtx layout.Context) {
		c := f.p.Text
		if primary {
			bg := f.p.Accent
			if b.Hovered() || b.Pressed() {
				bg = theme.Blend(f.p.Accent, f.p.Text, 0.14)
			}
			settingsFill(gtx, settingsBounds(gtx), btnR, bg)
			c = f.p.OnAccent
		} else {
			bg := f.p.Surface
			border := f.p.Line
			if b.Hovered() || b.Pressed() {
				bg = f.p.Hover
				border = theme.Blend(f.p.Line, f.p.Accent, 0.50)
			}
			settingsFill(gtx, settingsBounds(gtx), btnR, bg)
			settingsStroke(gtx, settingsBounds(gtx), btnR, 1, border)
		}
		f.label(gtx, settingsBounds(gtx), title, 13, font.Medium, c, layout.Center)
	})
}

func (f settingsFrame) navTitles() [5]string {
	return [5]string{
		f.tr("常规", "General"),
		f.tr("外观", "Appearance"),
		f.tr("桌面", "Desktop"),
		f.tr("指令", "Commands"),
		f.tr("插件", "Plugins"),
	}
}

func (f settingsFrame) sidebar(gtx layout.Context) {
	minimum := gtx.Dp(76 + unit.Dp(40*len(f.sections())))
	if f.model.hasAppsMenu() {
		minimum += gtx.Dp(40)
		if f.appsExpanded && gtx.Constraints.Max.X >= gtx.Dp(84) {
			minimum += gtx.Dp(90)
		}
	}
	if minimum <= gtx.Constraints.Max.Y {
		f.sidebarBody(gtx)
		return
	}
	f.sidebarList.Layout(gtx, 1, func(inner layout.Context, _ int) layout.Dimensions {
		inner.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, minimum))
		f.sidebarBody(inner)
		return layout.Dimensions{Size: inner.Constraints.Max}
	})
}

func (f settingsFrame) sidebarBody(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y

	// Geek HUD brand mark at top of sidebar (also draggable in frameless mode).
	if f.model.CustomTitleBar {
		brandDrag := clip.Rect(image.Rect(0, 0, w, p(60))).Push(gtx.Ops)
		system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
		brandDrag.Pop()
	}
	narrow := w < p(84)
	if !narrow {
		settingsFill(gtx, image.Rect(p(16), p(20), p(19), p(44)), 1.5, f.p.Accent)
		f.monoLabel(gtx, image.Rect(p(26), p(16), w-p(12), p(36)), "QUAILCRATE", 13, font.Bold, f.p.Text, layout.W)
		f.monoLabel(gtx, image.Rect(p(26), p(34), w-p(12), p(49)), "HUD // CONFIG", 10, font.Medium, f.p.Secondary, layout.W)
	} else {
		f.monoLabel(gtx, image.Rect(0, p(18), w, p(46)), "QC", 13, font.Bold, f.p.Accent, layout.Center)
	}
	settingsFill(gtx, image.Rect(p(12), p(60), w-p(12), p(60)+max(1, p(1))), 0, f.p.Line)

	y := p(70)
	for _, section := range f.sections() {
		title := section.Title
		b := f.navButton(section.ID)
		f.button(gtx, image.Rect(p(10), y, w-p(10), y+p(36)), b, title, func(gtx layout.Context) {
			bw, bh := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
			c, weight := f.p.Secondary, font.Normal
			if f.CurrentPage() == section.ID {
				settingsFill(gtx, settingsBounds(gtx), 8, f.p.Selected)
				settingsStroke(gtx, settingsBounds(gtx), 8, 1, theme.WithAlpha(f.p.Accent, 90))
				settingsFill(gtx, image.Rect(0, (bh-p(18))/2, p(3), (bh+p(18))/2), 1.5, f.p.Accent)
				c, weight = f.p.Accent, font.SemiBold
				if f.model.HighContrast {
					c = f.p.OnAccent
				}
			} else if b.Hovered() {
				settingsFill(gtx, settingsBounds(gtx), 8, f.p.Hover)
				c = f.p.Text
			}
			x := p(12)
			if bw < p(64) {
				x = (bw - p(18)) / 2
			}
			settingsIcon(gtx, image.Rect(x, (bh-p(18))/2, x+p(18), (bh+p(18))/2), section.Icon, c)
			if bw >= p(64) {
				f.label(gtx, image.Rect(p(40), 0, bw-p(4), bh), title, 13.5, weight, c, layout.W)
			}
		})
		y += p(40)
	}

	if !f.model.hasAppsMenu() {
		return
	}
	appsTitle := f.tr("应用", "Apps")
	if def := f.model.Definition; def != nil {
		appsTitle = def.AppsTitle
	}
	appsRow := image.Rect(p(10), y, w-p(10), y+p(36))
	if f.appsToggle.Clicked(gtx) {
		if narrow {
			f.emit(SettingsAction{Kind: OpenAppsMenu})
		} else {
			f.appsExpanded = !f.appsExpanded
			f.emit(SettingsAction{Kind: ToggleAppsExpanded, Enabled: f.appsExpanded})
		}
	}
	f.button(gtx, appsRow, &f.appsToggle, appsTitle, func(gtx layout.Context) {
		bw, bh := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
		c := f.p.Secondary
		if f.appsToggle.Hovered() {
			settingsFill(gtx, settingsBounds(gtx), f.radius(8), f.p.Hover)
			c = f.p.Text
		}
		x := p(12)
		if bw < p(64) {
			x = (bw - p(18)) / 2
		}
		DrawIcon(gtx, image.Rect(x, (bh-p(18))/2, x+p(18), (bh+p(18))/2), "apps", c)
		if bw < p(64) {
			return
		}
		badge := image.Rect(bw-p(46), (bh-p(18))/2, bw-p(26), (bh+p(18))/2)
		settingsFill(gtx, badge, f.radius(5), f.p.Selected)
		f.monoLabel(gtx, badge, fmt.Sprintf("%d", len(f.model.AppTabs)), 10, font.Bold, f.p.Accent, layout.Center)
		f.label(gtx, image.Rect(p(40), 0, max(p(40), badge.Min.X-p(4)), bh), appsTitle, 13.5, font.Normal, c, layout.W)
		chev := "chevron-right"
		if f.appsExpanded {
			chev = "chevron-down"
		}
		DrawIcon(gtx, image.Rect(bw-p(20), (bh-p(14))/2, bw-p(6), (bh+p(14))/2), chev, f.p.Secondary)
	})
	y += p(40)

	if !narrow && f.appsExpanded && y < h-p(16) {
		tabs := f.model.AppTabs
		listRect := image.Rect(p(10), y, w-p(10), h-p(10))
		settingsArea(gtx, listRect, func(gtx layout.Context) {
			labels := DisambiguateTabLabels(tabs)
			f.list(gtx, &f.appsList, len(tabs), func(gtx layout.Context, index int) layout.Dimensions {
				tab := tabs[index]
				key := [2]string{tab.PluginID, tab.TabID}
				btn := f.appTabButtons[key]
				if btn == nil {
					btn = &widget.Clickable{}
					f.appTabButtons[key] = btn
				}
				if btn.Clicked(gtx) {
					f.emit(SettingsAction{
						Kind:       OpenAppTab,
						PluginID:   tab.PluginID,
						CommandID:  tab.TabID,
						Generation: tab.Generation,
					})
				}
				rw, rh := gtx.Constraints.Max.X, p(30)
				f.button(gtx, image.Rect(p(12), p(1), rw-p(2), rh-p(1)), btn, labels[index], func(gtx layout.Context) {
					bw, bh := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
					ink := f.p.Secondary
					if btn.Hovered() || btn.Pressed() {
						settingsFill(gtx, settingsBounds(gtx), f.radius(6), f.p.Hover)
						ink = f.p.Text
					}
					icName := tab.Icon
					if icName == "" {
						icName = "apps"
					}
					DrawIcon(gtx, image.Rect(p(8), (bh-p(13))/2, p(21), (bh+p(13))/2), icName, ink)
					f.label(gtx, image.Rect(p(26), 0, bw-p(4), bh), labels[index], 12, font.Normal, ink, layout.W)
				})
				return layout.Dimensions{Size: image.Pt(rw, rh)}
			})
		})
	}
}

func (f settingsFrame) contentPadding(gtx layout.Context) int {
	if gtx.Constraints.Max.X < gtx.Dp(540) {
		return gtx.Dp(14)
	}
	return gtx.Dp(26)
}

// paddedCard insets a list item horizontally by pad on both sides while letting
// the enclosing widget.List span the full right pane so its scrollbar sits flush
// against the window's right edge rather than overlapping the cards.
func (f settingsFrame) paddedCard(gtx layout.Context, pad int, render func(layout.Context) layout.Dimensions) layout.Dimensions {
	w := gtx.Constraints.Max.X
	innerW := max(0, w-pad*2)
	inner := gtx
	inner.Constraints = layout.Constraints{
		Min: image.Pt(innerW, 0),
		Max: image.Pt(innerW, gtx.Constraints.Max.Y),
	}
	off := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
	dims := render(inner)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, dims.Size.Y)}
}

func (f settingsFrame) content(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	pad := f.contentPadding(gtx)
	title := f.navTitles()[f.page]
	subtitle := [...]string{
		f.tr("配置快速启动快捷键、界面语言、开机启动与应用更新。", "Configure launcher shortcut, display language, sign-in startup, and updates."),
		f.tr("切换内置主题配色，为每个组件定制独立外观与壁纸透明穿透。", "Pick built-in themes and customize per-component colors and wallpaper opacity."),
		f.tr("调整桌面组件的布局位置与锁定编辑状态。", "Arrange desktop widgets and manage layout editing."),
		f.tr("运行已启用插件提供的公开桌面与设置指令。", "Run public commands contributed by enabled plugins."),
		f.tr("管理运行中的 Lua / Native 插件及其诊断状态。", "Manage Lua and native plugins and inspect diagnostics."),
	}[f.page]
	section := f.model.Definition.Section(f.CurrentPage())
	if section != nil {
		title, subtitle = section.Title, section.Description
	}
	shift := 0
	if h < p(550) {
		shift = p(14)
	}
	f.label(gtx, image.Rect(pad, p(16)-shift, w-pad, p(62)-shift), title, 28, font.Bold, f.p.Text, layout.W)
	subRight := w - pad
	hasPlugins := f.page == pluginsPage && section == nil
	if section != nil {
		for _, card := range section.Cards {
			if card.Type == "plugins" {
				hasPlugins = true
				break
			}
		}
	}
	if hasPlugins && w-pad*2 >= p(420) {
		subRight = w - pad - p(114)
		f.runningBadge(gtx, image.Rect(w-pad-p(106), p(56)-shift, w-pad, p(82)-shift))
	}
	f.label(gtx, image.Rect(pad, p(58)-shift, subRight, p(84)-shift), subtitle, 12.5, font.Normal, f.p.Secondary, layout.W)
	bodyTop := p(94) - shift
	if section != nil {
		settingsArea(gtx, image.Rect(0, bodyTop, w, h), func(gtx layout.Context) {
			f.declaredCards(gtx, *section)
		})
		return
	}
	if f.page == generalPage {
		settingsArea(gtx, image.Rect(0, bodyTop, w, h), f.general)
		return
	}
	if f.page == appearancePage {
		settingsArea(gtx, image.Rect(0, bodyTop, w, h), f.appearance)
		return
	}
	settingsArea(gtx, image.Rect(pad, bodyTop, w-pad, h-p(12)), func(gtx layout.Context) {
		switch f.page {
		case desktopPage:
			f.desktop(gtx)
		case commandsPage:
			f.commands(gtx)
		case pluginsPage:
			f.plugins(gtx)
		}
	})
}
