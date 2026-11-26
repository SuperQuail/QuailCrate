package gioui

import (
	"encoding/json"
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"quailcrate/internal/pluginapps"
	"quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

type AppWindowState string

const (
	AppStateLoading AppWindowState = "loading"
	AppStateReady   AppWindowState = "ready"
	AppStateError   AppWindowState = "error"
)

// AppWindowModel is an immutable snapshot rendered by a plugin's independent window.
type AppWindowModel struct {
	Locale, PluginID, PluginName string
	Tabs                         []pluginapps.Tab
	ActiveTabID                  string
	State                        AppWindowState
	Error                        string
	Page                         *pluginapps.Page
	Palette                      SettingsPalette
	CornerRadius                 int
	HasCornerRadius              bool
	TitleAccentBar               bool
	HighContrast                 bool
	CustomTitleBar               bool
	Maximized                    bool
	WebViewActive                bool
}

// Loading reports whether the tab or its HTML document is still loading.
func (m AppWindowModel) Loading() bool {
	return m.State == AppStateLoading ||
		(m.State == AppStateReady && m.Page != nil && m.Page.Document != nil && !m.WebViewActive)
}

type AppWindowActionKind string

const (
	AppSelectTab            AppWindowActionKind = "select-tab"
	AppActControl           AppWindowActionKind = "act-control"
	AppSubmitValue          AppWindowActionKind = "submit-value"
	AppRetry                AppWindowActionKind = "retry"
	AppWindowMinimize       AppWindowActionKind = "minimize"
	AppWindowToggleMaximize AppWindowActionKind = "toggle-maximize"
	AppWindowClose          AppWindowActionKind = "close"
)

type AppWindowAction struct {
	Kind       AppWindowActionKind
	Tab        pluginapps.TabRef
	Page       pluginapps.PageRef
	ControlID  string
	ItemID     string
	InputValue json.RawMessage
}

// AppWindowView owns cross-frame Gio widget state for one plugin app window.
type AppWindowView struct {
	theme                    *material.Theme
	winMin, winMax, winClose widget.Clickable
	retryBtn                 widget.Clickable
	tabBarList               widget.List
	bodyList                 widget.List
	tabButtons               map[string]*widget.Clickable
	controlButtons           map[string]*widget.Clickable
	controlSliders           map[string]*widget.Float
	sliderDragging           map[string]bool
	itemButtons              map[[2]string]*widget.Clickable
	controlLists             map[string]*widget.List
}

func NewAppWindowView() *AppWindowView { return &AppWindowView{} }

func (v *AppWindowView) Layout(gtx layout.Context, m AppWindowModel, emit func(AppWindowAction)) layout.Dimensions {
	if v.theme == nil {
		v.theme = material.NewTheme()
		v.theme.Shaper = NewTextShaper()
		v.tabBarList.Axis = layout.Horizontal
		v.bodyList.Axis = layout.Vertical
		v.tabButtons = make(map[string]*widget.Clickable)
		v.controlButtons = make(map[string]*widget.Clickable)
		v.controlSliders = make(map[string]*widget.Float)
		v.sliderDragging = make(map[string]bool)
		v.itemButtons = make(map[[2]string]*widget.Clickable)
		v.controlLists = make(map[string]*widget.List)
	}
	if emit == nil {
		emit = func(AppWindowAction) {}
	}
	p := m.Palette
	if p.Background.A == 0 {
		p = defaultSettings
	}
	f := appFrame{AppWindowView: v, model: m, p: p, emit: emit}
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	paint.Fill(gtx.Ops, theme.WithAlpha(p.Background, 255))

	top := 0
	if m.CustomTitleBar {
		barH := gtx.Dp(36)
		settingsArea(gtx, image.Rect(0, 0, w, barH), f.titleBar)
		top = barH
	}
	if len(m.Tabs) > 1 {
		tabH := gtx.Dp(40)
		settingsArea(gtx, image.Rect(0, top, w, top+tabH), f.tabBar)
		top += tabH
	}
	pad := gtx.Dp(18)
	if w < gtx.Dp(480) {
		pad = gtx.Dp(12)
	}
	settingsArea(gtx, image.Rect(pad, top+gtx.Dp(12), max(pad, w-pad), max(top+gtx.Dp(12), h-gtx.Dp(14))), f.body)
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

type appFrame struct {
	*AppWindowView
	model AppWindowModel
	p     SettingsPalette
	emit  func(AppWindowAction)
}

func (f appFrame) tr(zh, en string) string {
	if strings.HasPrefix(f.model.Locale, "zh") {
		return zh
	}
	return en
}

func (f appFrame) typeface() font.Typeface {
	if strings.HasPrefix(f.model.Locale, "zh") {
		return "Microsoft YaHei UI, Segoe UI Variable Text, PingFang SC, Noto Sans CJK SC, sans-serif"
	}
	return "Segoe UI Variable Text, Segoe UI, Helvetica Neue, Noto Sans, sans-serif"
}

func (f appFrame) monoTypeface() font.Typeface {
	return "Cascadia Mono, JetBrains Mono, Consolas, Microsoft YaHei UI, monospace"
}

func (f appFrame) label(gtx layout.Context, r image.Rectangle, value string, size unit.Sp, weight font.Weight, mono bool, col color.NRGBA, align layout.Direction) {
	if r.Empty() || value == "" {
		return
	}
	tf := f.typeface()
	if mono {
		tf = f.monoTypeface()
	}
	settingsArea(gtx, r, func(gtx layout.Context) {
		align.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			l := material.Label(f.theme, size, value)
			l.Color, l.Font.Weight, l.Font.Typeface, l.MaxLines = col, weight, tf, 1
			return l.Layout(gtx)
		})
	})
}

func (f appFrame) paragraph(gtx layout.Context, r image.Rectangle, value string, size unit.Sp, col color.NRGBA, lines int) {
	if r.Empty() || value == "" {
		return
	}
	settingsArea(gtx, r, func(gtx layout.Context) {
		layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			l := material.Label(f.theme, size, value)
			l.Color, l.Font.Typeface, l.MaxLines = col, f.typeface(), lines
			return l.Layout(gtx)
		})
	})
}

func (f appFrame) radius(baseDp int) unit.Dp {
	r := theme.DefaultCornerRadius
	if f.model.HasCornerRadius {
		r = f.model.CornerRadius
	}
	return unit.Dp(theme.ScaleRadius(r, baseDp))
}

func (f appFrame) button(gtx layout.Context, r image.Rectangle, b *widget.Clickable, desc string, draw func(layout.Context)) {
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

func (f appFrame) titleBar(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	settingsFill(gtx, settingsBounds(gtx), 0, theme.WithAlpha(f.p.Sidebar, 255))
	if f.winMin.Clicked(gtx) {
		f.emit(AppWindowAction{Kind: AppWindowMinimize})
	}
	if f.winMax.Clicked(gtx) {
		f.emit(AppWindowAction{Kind: AppWindowToggleMaximize})
	}
	if f.winClose.Clicked(gtx) {
		f.emit(AppWindowAction{Kind: AppWindowClose})
	}

	btnW := p(40)
	controlsLeft := max(0, w-btnW*3)
	if controlsLeft > 0 {
		dragClip := clip.Rect(image.Rect(0, 0, controlsLeft, h)).Push(gtx.Ops)
		system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
		dragClip.Pop()

		titleX := p(16)
		if f.model.TitleAccentBar {
			settingsFill(gtx, image.Rect(p(14), p(11), p(17), h-p(11)), f.radius(2), f.p.Accent)
			titleX = p(24)
		}
		title := f.model.PluginName
		if title == "" {
			title = f.model.PluginID
		}
		if len(f.model.Tabs) == 1 && f.model.Tabs[0].Title != "" && f.model.Tabs[0].Title != title {
			title += "  //  " + f.model.Tabs[0].Title
		}
		f.label(gtx, image.Rect(titleX, 0, controlsLeft-p(8), h), title, 12.5, font.SemiBold, false, f.p.Text, layout.W)
	}

	maxIcon := "win-max"
	if f.model.Maximized {
		maxIcon = "win-restore"
	}
	btns := [3]struct {
		b      *widget.Clickable
		icon   string
		desc   string
		danger bool
	}{
		{&f.winMin, "win-min", f.tr("最小化", "Minimize"), false},
		{&f.winMax, maxIcon, f.tr("最大化", "Maximize"), false},
		{&f.winClose, "win-close", f.tr("关闭", "Close"), true},
	}
	for i, wb := range btns {
		r := image.Rect(controlsLeft+i*btnW, 0, controlsLeft+(i+1)*btnW, h)
		f.button(gtx, r, wb.b, wb.desc, func(gtx layout.Context) {
			bounds := settingsBounds(gtx)
			ink := f.p.Secondary
			if wb.b.Hovered() || wb.b.Pressed() {
				if wb.danger {
					settingsFill(gtx, bounds, 0, f.p.Warning)
					ink = f.p.OnAccent
				} else {
					settingsFill(gtx, bounds, 0, f.p.Hover)
					ink = f.p.Text
				}
			}
			ic := p(14)
			DrawIcon(gtx, image.Rect((bounds.Dx()-ic)/2, (bounds.Dy()-ic)/2, (bounds.Dx()+ic)/2, (bounds.Dy()+ic)/2), wb.icon, ink)
		})
	}
	settingsFill(gtx, image.Rect(0, h-max(1, p(1)), w, h), 0, theme.WithAlpha(f.p.Line, 255))
}

func (f appFrame) tabBar(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	settingsFill(gtx, settingsBounds(gtx), 0, theme.WithAlpha(f.p.Sidebar, 255))
	settingsArea(gtx, image.Rect(p(12), p(4), max(p(12), w-p(12)), h-max(1, p(1))), func(gtx layout.Context) {
		style := material.List(f.theme, &f.tabBarList)
		style.AnchorStrategy = material.Overlay
		style.Layout(gtx, len(f.model.Tabs), func(gtx layout.Context, i int) layout.Dimensions {
			tab := f.model.Tabs[i]
			btn := f.tabButtons[tab.TabID]
			if btn == nil {
				btn = &widget.Clickable{}
				f.tabButtons[tab.TabID] = btn
			}
			if btn.Clicked(gtx) {
				f.emit(AppWindowAction{Kind: AppSelectTab, Tab: tab.TabRef})
			}
			active := tab.TabID == f.model.ActiveTabID
			tabW := max(p(84), min(p(180), p(36)+len([]rune(tab.Title))*p(11)))
			tabH := gtx.Constraints.Max.Y
			f.button(gtx, image.Rect(0, 0, tabW-p(6), tabH), btn, tab.Title, func(gtx layout.Context) {
				b := settingsBounds(gtx)
				ink := f.p.Secondary
				weight := font.Normal
				tabR := f.radius(7)
				if active {
					settingsFill(gtx, b, tabR, f.p.Selected)
					settingsFill(gtx, image.Rect(p(8), b.Max.Y-max(2, p(2)), b.Max.X-p(8), b.Max.Y), f.radius(1), f.p.Accent)
					ink, weight = f.p.Accent, font.SemiBold
					if f.model.HighContrast {
						ink = f.p.OnAccent
					}
				} else if btn.Hovered() {
					settingsFill(gtx, b, tabR, f.p.Hover)
					ink = f.p.Text
				}
				f.label(gtx, b.Inset(p(6)), tab.Title, 12.5, weight, false, ink, layout.Center)
			})
			return layout.Dimensions{Size: image.Pt(tabW, tabH)}
		})
	})
	settingsFill(gtx, image.Rect(0, h-max(1, p(1)), w, h), 0, theme.WithAlpha(f.p.Line, 255))
}

func (f appFrame) body(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	if f.model.Loading() {
		f.label(gtx, settingsBounds(gtx), f.tr("正在加载插件视图…", "Loading plugin view…"), 14, font.Medium, false, f.p.Secondary, layout.Center)
		return
	}
	if f.model.State == AppStateError {
		cardH := min(h, p(176))
		r := image.Rect(0, 0, w, cardH)
		cardR := f.radius(12)
		settingsFill(gtx, r, cardR, f.p.Surface)
		settingsStroke(gtx, r, cardR, 1, f.p.Warning)
		f.label(gtx, image.Rect(p(18), p(16), w-p(18), p(44)), f.tr("无法加载此页面", "Could not load this page"), 16, font.SemiBold, false, f.p.Warning, layout.W)
		errText := f.model.Error
		if errText == "" {
			errText = f.tr("插件暂时不可用，请重试。", "The plugin is temporarily unavailable. Please try again.")
		}
		f.paragraph(gtx, image.Rect(p(18), p(48), w-p(18), p(110)), errText, 13, f.p.Secondary, 3)
		if f.retryBtn.Clicked(gtx) {
			f.emit(AppWindowAction{Kind: AppRetry})
		}
		f.button(gtx, image.Rect(p(18), p(120), p(122), p(154)), &f.retryBtn, f.tr("重试", "Retry"), func(gtx layout.Context) {
			b := settingsBounds(gtx)
			settingsFill(gtx, b, f.radius(7), f.p.Accent)
			f.label(gtx, b, f.tr("重试", "Retry"), 13, font.SemiBold, false, f.p.OnAccent, layout.Center)
		})
		return
	}
	if f.model.WebViewActive && f.model.State == AppStateReady && f.model.Page != nil && f.model.Page.Document != nil {
		// Child WebView2 container covers this body region directly.
		return
	}
	if f.model.Page == nil {
		f.label(gtx, settingsBounds(gtx), f.tr("暂无可显示内容", "Nothing to display"), 13, font.Normal, false, f.p.Secondary, layout.Center)
		return
	}

	view := f.model.Page.View
	top := 0
	if view.Title != "" {
		f.label(gtx, image.Rect(0, 0, w, p(34)), view.Title, 20, font.Bold, false, f.p.Text, layout.W)
		top = p(40)
	}
	if view.Root != nil {
		settingsArea(gtx, image.Rect(0, top, w, max(top, h)), func(gtx layout.Context) { f.layoutTree(gtx, view.LayoutTree().Body) })
		return
	}
	blocks := view.LayoutTree().Body.Children
	if len(blocks) == 0 {
		f.label(gtx, image.Rect(0, top, w, h), f.tr("此页面没有声明控件", "This page has no controls"), 13, font.Normal, false, f.p.Secondary, layout.Center)
		return
	}

	settingsArea(gtx, image.Rect(0, top, w, max(top, h)), func(gtx layout.Context) {
		style := material.List(f.theme, &f.bodyList)
		style.AnchorStrategy = material.Overlay
		style.Indicator.MinorWidth = 4
		style.Indicator.Color = f.p.Secondary
		style.Indicator.HoverColor = f.p.Accent
		style.Layout(gtx, len(blocks), func(gtx layout.Context, idx int) layout.Dimensions {
			blk := blocks[idx]
			switch blk.Type {
			case "row":
				return f.layoutButtonRow(gtx, blk.Children)
			case "text":
				return f.layoutTextControl(gtx, blk)
			case "list":
				return f.layoutListControl(gtx, blk)
			case "grid":
				return f.layoutGridControl(gtx, blk)
			default:
				return layout.Dimensions{}
			}
		})
	})
}

func (f appFrame) layoutTextControl(gtx layout.Context, c v1.Control) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	cardH := p(42)
	r := f.radius(8)
	settingsFill(gtx, image.Rect(0, 0, w, cardH), r, f.p.Surface)
	settingsStroke(gtx, image.Rect(0, 0, w, cardH), r, 1, f.p.Line)
	f.paragraph(gtx, image.Rect(p(14), p(8), w-p(14), cardH-p(8)), c.Text, 13.5, f.p.Text, 2)
	return layout.Dimensions{Size: image.Pt(w, cardH+p(10))}
}

func (f appFrame) layoutButtonRow(gtx layout.Context, buttons []v1.Control) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	gap := p(8)
	btnH := p(36)
	n := len(buttons)
	avail := w - gap*(n-1)
	for i, ctrl := range buttons {
		btn := f.controlButtons[ctrl.ID]
		if btn == nil {
			btn = &widget.Clickable{}
			f.controlButtons[ctrl.ID] = btn
		}
		if btn.Clicked(gtx) {
			f.emit(AppWindowAction{Kind: AppActControl, Page: f.model.Page.PageRef, ControlID: ctrl.ID})
		}
		x0 := i*gap + i*avail/n
		x1 := i*gap + (i+1)*avail/n
		r := image.Rect(x0, 0, x1, btnH)
		f.button(gtx, r, btn, ctrl.Text, func(gtx layout.Context) {
			b := settingsBounds(gtx)
			bg, border, ink := f.p.Surface, f.p.Line, f.p.Text
			if btn.Hovered() || btn.Pressed() {
				bg, border, ink = f.p.Selected, f.p.Accent, f.p.Accent
				if f.model.HighContrast {
					ink = f.p.OnAccent
				}
			}
			btnR := f.radius(8)
			settingsFill(gtx, b, btnR, bg)
			settingsStroke(gtx, b, btnR, 1, border)
			f.label(gtx, b.Inset(p(4)), ctrl.Text, 13, font.Medium, false, ink, layout.Center)
		})
	}
	return layout.Dimensions{Size: image.Pt(w, btnH+p(10))}
}

func (f appFrame) layoutListControl(gtx layout.Context, c v1.Control) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	listR := f.radius(10)
	if len(c.Items) == 0 {
		emptyH := p(64)
		settingsFill(gtx, image.Rect(0, 0, w, emptyH), listR, f.p.Surface)
		settingsStroke(gtx, image.Rect(0, 0, w, emptyH), listR, 1, f.p.Line)
		msg := c.Text
		if msg == "" {
			msg = f.tr("暂无条目", "No items")
		}
		f.label(gtx, image.Rect(p(12), 0, w-p(12), emptyH), msg, 13, font.Normal, false, f.p.Secondary, layout.Center)
		return layout.Dimensions{Size: image.Pt(w, emptyH+p(12))}
	}

	rowH := p(48)
	listH := min(p(280), rowH*len(c.Items)+p(4))
	settingsFill(gtx, image.Rect(0, 0, w, listH), listR, f.p.Surface)
	settingsStroke(gtx, image.Rect(0, 0, w, listH), listR, 1, f.p.Line)

	lst := f.controlLists[c.ID]
	if lst == nil {
		lst = &widget.List{List: layout.List{Axis: layout.Vertical}}
		f.controlLists[c.ID] = lst
	}
	settingsArea(gtx, image.Rect(p(2), p(2), w-p(2), listH-p(2)), func(gtx layout.Context) {
		style := material.List(f.theme, lst)
		style.AnchorStrategy = material.Overlay
		style.Indicator.MinorWidth = 4
		style.Indicator.Color = f.p.Secondary
		style.Indicator.HoverColor = f.p.Accent
		style.Layout(gtx, len(c.Items), func(gtx layout.Context, i int) layout.Dimensions {
			item := c.Items[i]
			key := [2]string{c.ID, item.ID}
			btn := f.itemButtons[key]
			if btn == nil {
				btn = &widget.Clickable{}
				f.itemButtons[key] = btn
			}
			if btn.Clicked(gtx) {
				f.emit(AppWindowAction{Kind: AppActControl, Page: f.model.Page.PageRef, ControlID: c.ActionID(), ItemID: item.ID})
			}
			rw := gtx.Constraints.Max.X
			if i > 0 {
				settingsFill(gtx, image.Rect(p(12), 0, rw-p(12), max(1, p(1))), 0, f.p.Line)
			}
			f.button(gtx, image.Rect(p(2), p(2), rw-p(2), rowH-p(2)), btn, item.Text, func(gtx layout.Context) {
				b := settingsBounds(gtx)
				ink, sub := f.p.Text, f.p.Secondary
				switch item.State {
				case "error":
					ink, sub = f.p.Warning, f.p.Warning
				case "muted":
					ink = f.p.Secondary
				}
				rowR := f.radius(7)
				if item.State == "current" {
					settingsFill(gtx, b, rowR, f.p.Selected)
					settingsFill(gtx, image.Rect(p(2), p(8), p(5), b.Max.Y-p(8)), f.radius(2), f.p.Accent)
					if f.model.HighContrast {
						ink, sub = f.p.OnAccent, f.p.OnAccent
					}
				} else if btn.Hovered() {
					settingsFill(gtx, b, rowR, f.p.Hover)
				}
				if item.Hint != "" {
					f.label(gtx, image.Rect(p(14), p(4), b.Max.X-p(12), p(24)), item.Text, 13.5, font.Medium, false, ink, layout.W)
					f.label(gtx, image.Rect(p(14), p(23), b.Max.X-p(12), b.Max.Y-p(2)), item.Hint, 11.5, font.Normal, true, sub, layout.W)
				} else {
					f.label(gtx, image.Rect(p(14), 0, b.Max.X-p(12), b.Max.Y), item.Text, 13.5, font.Medium, false, ink, layout.W)
				}
			})
			return layout.Dimensions{Size: image.Pt(rw, rowH)}
		})
	})
	return layout.Dimensions{Size: image.Pt(w, listH+p(12))}
}

func (f appFrame) layoutGridControl(gtx layout.Context, c v1.Control) layout.Dimensions {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	cols := max(1, min(16, c.Columns))
	rows := (len(c.Items) + cols - 1) / cols
	if rows == 0 {
		return layout.Dimensions{}
	}
	cellH := p(36)
	pad := p(8)
	cardH := rows*cellH + pad*2
	gridR := f.radius(10)
	cellR := f.radius(7)
	settingsFill(gtx, image.Rect(0, 0, w, cardH), gridR, f.p.Surface)
	settingsStroke(gtx, image.Rect(0, 0, w, cardH), gridR, 1, f.p.Line)

	innerW := w - pad*2
	for j, item := range c.Items {
		row, col := j/cols, j%cols
		x0 := pad + innerW*col/cols
		x1 := pad + innerW*(col+1)/cols
		y0 := pad + row*cellH
		y1 := y0 + cellH
		cell := image.Rect(x0, y0, x1, y1)
		if isGridHeaderState(item.State) {
			ink := f.p.Secondary
			if item.State == "header-weekend" {
				ink = f.p.Weekend
			}
			f.label(gtx, cell, item.Text, 11.5, font.SemiBold, true, ink, layout.Center)
			continue
		}
		key := [2]string{c.ID, item.ID}
		btn := f.itemButtons[key]
		if btn == nil {
			btn = &widget.Clickable{}
			f.itemButtons[key] = btn
		}
		if btn.Clicked(gtx) {
			f.emit(AppWindowAction{Kind: AppActControl, Page: f.model.Page.PageRef, ControlID: c.ActionID(), ItemID: item.ID})
		}
		f.button(gtx, cell.Inset(p(2)), btn, item.Text, func(gtx layout.Context) {
			b := settingsBounds(gtx)
			ink := f.p.Text
			weight := font.Normal
			switch item.State {
			case "current":
				settingsFill(gtx, b, cellR, f.p.Accent)
				ink, weight = f.p.OnAccent, font.Bold
			case "selected":
				settingsFill(gtx, b, cellR, f.p.Selected)
				settingsStroke(gtx, b, cellR, 1, f.p.Accent)
				ink, weight = f.p.Accent, font.SemiBold
			case "weekend":
				ink = f.p.Weekend
			case "muted":
				ink = f.p.Secondary
			case "error":
				ink = f.p.Warning
			}
			if btn.Hovered() && item.State != "current" {
				settingsFill(gtx, b, cellR, f.p.Hover)
			}
			f.label(gtx, b, item.Text, 13, weight, true, ink, layout.Center)
		})
	}
	return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
}

func (f appFrame) layoutTree(gtx layout.Context, root v1.Control) {
	dp := func(n int) int { return gtx.Dp(unit.Dp(n)) }
	width, viewport := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	height := max(viewport, dp(treeHeight(root)))
	f.bodyList.List.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		canvas := GioWidgetCanvas{Context: gtx, Shaper: f.theme.Shaper, Typeface: f.typeface(), MonoTypeface: f.monoTypeface()}
		w := widgetLayout{WidgetOptions: WidgetOptions{DPI: dp(96), Colors: WidgetColors{
			Background: f.p.Background, Surface: f.p.Surface, Ink: f.p.Text, Secondary: f.p.Secondary, Border: f.p.Line, Accent: f.p.Accent,
			Selection: f.p.Selected, Hover: f.p.Hover, Weekend: f.p.Weekend, Warning: f.p.Warning, OnAccent: f.p.OnAccent, OnSelection: f.p.Text,
			CornerRadius: f.model.CornerRadius, HasCornerRadius: f.model.HasCornerRadius,
		}}, canvas: canvas, surfaceAlpha: 255}
		treeLeaves(root, image.Rect(0, 0, width, height), dp, func(c v1.Control, r image.Rectangle) {
			if c.Type == "list" {
				settingsArea(gtx, r, func(gtx layout.Context) { f.layoutListControl(gtx, c) })
				return
			}
			if c.Type == "slider" {
				fl := f.controlSliders[c.ID]
				if fl == nil {
					fl = &widget.Float{Value: float32(c.Value)}
					f.controlSliders[c.ID] = fl
				}
				wasDrag := f.sliderDragging[c.ID]
				if !wasDrag {
					fl.Value = float32(c.Value)
				}
				settingsArea(gtx, r, func(gtx layout.Context) {
					sl := material.Slider(f.theme, fl)
					sl.Color = f.p.Accent
					sl.Layout(gtx)
				})
				nowDrag := fl.Dragging()
				f.sliderDragging[c.ID] = nowDrag
				changed := fl.Update(gtx)
				if (wasDrag && !nowDrag) || (changed && !nowDrag) {
					v := max(0.0, min(1.0, float64(fl.Value)))
					f.emit(AppWindowAction{
						Kind:       AppSubmitValue,
						Page:       f.model.Page.PageRef,
						ControlID:  c.ActionID(),
						InputValue: json.RawMessage(strconv.FormatFloat(v, 'f', 4, 64)),
					})
				}
				return
			}
			w.geometry.Hits = w.geometry.Hits[:0]
			w.drawLeaf(c, r)
			for _, hit := range w.geometry.Hits {
				desc := c.Label
				if desc == "" {
					desc = c.Text
				}
				var b *widget.Clickable
				if hit.ItemID == "" {
					b = f.controlButtons[c.ID]
					if b == nil {
						b = &widget.Clickable{}
						f.controlButtons[c.ID] = b
					}
				} else {
					item := c.Items[hit.Index]
					desc = item.Label
					if desc == "" {
						desc = item.Text
					}
					key := [2]string{c.ID, hit.ItemID}
					b = f.itemButtons[key]
					if b == nil {
						b = &widget.Clickable{}
						f.itemButtons[key] = b
					}
				}
				if b.Clicked(gtx) {
					f.emit(AppWindowAction{Kind: AppActControl, Page: f.model.Page.PageRef, ControlID: c.ActionID(), ItemID: hit.ItemID})
				}
				f.button(gtx, hit.Area, b, desc, func(gtx layout.Context) {
					if b.Hovered() {
						settingsStroke(gtx, settingsBounds(gtx), 5, 1, f.p.Accent)
					}
				})
			}
		})
		return layout.Dimensions{Size: image.Pt(width, height)}
	})
}
