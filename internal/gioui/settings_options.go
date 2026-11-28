package gioui

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

func (f settingsFrame) paragraph(gtx layout.Context, r image.Rectangle, value string, c color.NRGBA, lines int) {
	settingsArea(gtx, r, func(gtx layout.Context) {
		layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			l := material.Label(f.theme, 13, value)
			l.Color, l.Font.Typeface, l.MaxLines = c, f.typeface(), lines
			return l.Layout(gtx)
		})
	})
}

func (f settingsFrame) general(gtx layout.Context) {
	kinds := [...]string{"hotkey", "language", "autostart", "updates"}
	pad := f.contentPadding(gtx)
	f.list(gtx, &f.generalList, len(kinds), func(gtx layout.Context, index int) layout.Dimensions {
		dims := f.paddedCard(gtx, pad, func(gtx layout.Context) layout.Dimensions {
			p, w := gtx.Dp, gtx.Constraints.Max.X
			var cardH int
			switch kinds[index] {
			case "hotkey":
				cardH = f.hotkeyCard(gtx)
			case "language":
				cardH = f.languageCard(gtx)
			case "autostart":
				cardH = f.autostartCard(gtx)
			case "updates":
				cardH = f.updatesCard(gtx)
			}
			return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
		})
		if index == len(kinds)-1 {
			dims.Size.Y += gtx.Dp(12)
		}
		return dims
	})
}
func (f settingsFrame) hotkeyCard(gtx layout.Context) int {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	save := f.apply.Clicked(gtx)
	for {
		evt, ok := f.shortcut.Update(gtx)
		if !ok {
			break
		}
		if _, ok := evt.(widget.SubmitEvent); ok {
			save = true
		}
	}
	if save {
		f.emit(SettingsAction{Kind: ChangeHotkey, Value: strings.TrimSpace(f.shortcut.Text())})
	}
	inputY, cardH := p(13), p(120)
	compact := w < p(570)
	if compact {
		inputY, cardH = p(67), p(155)
	}
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
	right := w - p(270)
	if compact {
		right = w - p(16)
	}
	f.label(gtx, image.Rect(p(18), p(10), right, p(42)), f.heading("快速启动快捷键", "Launcher shortcut"), 16, font.SemiBold, f.p.Text, layout.W)
	f.label(gtx, image.Rect(p(18), p(41), right, p(66)), f.tr("例如 Ctrl+Alt+Space", "For example, Ctrl+Alt+Space"), 13, font.Normal, f.p.Secondary, layout.W)
	editX := w - p(260)
	if compact {
		editX = p(16)
	}
	field := image.Rect(editX, inputY, w-p(102), inputY+p(36))
	f.box(gtx, field, 6, f.p.Background)
	if gtx.Focused(&f.shortcut) {
		settingsStroke(gtx, field, f.radius(6), 1, f.p.Accent)
	}
	settingsArea(gtx, image.Rect(field.Min.X+p(9), field.Min.Y, field.Max.X-p(8), field.Max.Y), func(gtx layout.Context) {
		semantic.DescriptionOp(f.heading("快速启动快捷键", "Launcher shortcut")).Add(gtx.Ops)
		layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			style := material.Editor(f.theme, &f.shortcut, "Alt+Space")
			style.TextSize, style.Font.Typeface, style.Color, style.HintColor = 14, f.typeface(), f.p.Text, f.p.Secondary
			return style.Layout(gtx)
		})
	})
	f.textButton(gtx, image.Rect(w-p(93), inputY, w-p(16), inputY+p(36)), &f.apply, f.tr("应用", "Apply"), true)
	status, bg, fg := f.model.HotkeyStatus, f.p.WarningBg, f.p.Warning
	if status == "" {
		status, bg, fg = f.tr("尚未注册", "Shortcut is not registered"), f.p.Background, f.p.Secondary
		if f.model.HotkeyRegistered {
			status, bg, fg = f.tr("当前快捷键", "Current shortcut")+"   "+f.model.Hotkey, f.p.Selected, f.p.Accent
			if f.model.HighContrast {
				fg = f.p.OnAccent
			}
		}
	}
	info := image.Rect(p(16), cardH-p(44), w-p(16), cardH-p(10))
	settingsFill(gtx, info, f.radius(5), bg)
	f.paragraph(gtx, image.Rect(info.Min.X+p(9), info.Min.Y, info.Max.X-p(8), info.Max.Y), status, fg, 2)

	return cardH
}
func (f settingsFrame) languageCard(gtx layout.Context) int {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	languages := [3]string{"system", "en-US", "zh-CN"}
	labels := [3]string{f.tr("跟随系统", "System default"), "English", "简体中文"}
	widths := [3]int{p(106), p(86), p(88)}
	if !strings.HasPrefix(f.model.Locale, "zh") {
		widths[0] = p(118)
	}
	gap := p(6)
	groupW := widths[0] + widths[1] + widths[2] + gap*2
	inline := w >= p(620)

	cardH := p(76)
	if !inline {
		cardH = p(122)
	}
	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)

	textRight := w - p(16)
	if inline {
		textRight = w - p(16) - groupW - p(16)
	}
	f.label(gtx, image.Rect(p(18), p(12), textRight, p(38)), f.heading("界面语言", "Display language"), 16, font.SemiBold, f.p.Text, layout.W)
	f.label(gtx, image.Rect(p(18), p(40), textRight, p(64)), f.tr("选择设置、菜单和支持翻译的插件使用的语言。", "Changes settings, menus, and plugins that provide translations."), 12.5, font.Normal, f.p.Secondary, layout.W)

	x := w - p(16) - groupW
	btnY := (cardH - p(34)) / 2
	if !inline {
		x = p(16)
		btnY = p(74)
	}
	for i, lang := range languages {
		b := &f.languages[i]
		if b.Clicked(gtx) {
			f.emit(SettingsAction{Kind: ChangeLanguage, Value: lang})
		}
		bw := widths[i]
		if !inline {
			bw = min(bw, (w-p(40))*bw/max(1, groupW))
		}
		f.button(gtx, image.Rect(x, btnY, x+bw, btnY+p(34)), b, labels[i], func(gtx layout.Context) {
			sel := lang == f.model.LanguagePreference
			semantic.RadioButton.Add(gtx.Ops)
			semantic.SelectedOp(sel).Add(gtx.Ops)
			bg, fg := f.p.Surface, f.p.Text
			if sel || b.Hovered() {
				bg = f.p.Selected
			}
			if sel {
				fg = f.p.Accent
			}
			if f.model.HighContrast && bg == f.p.Selected {
				fg = f.p.OnAccent
			}
			f.box(gtx, settingsBounds(gtx), 6, bg)
			f.label(gtx, settingsBounds(gtx), labels[i], 13, font.Medium, fg, layout.Center)
		})
		x += bw + gap
	}
	return cardH
}

func (f settingsFrame) desktop(gtx layout.Context) {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	if f.edit.Clicked(gtx) {
		f.emit(SettingsAction{Kind: ToggleEditing, Enabled: !f.model.Editing})
	}
	compact := w < p(530)
	headerHeight := p(104)
	if compact {
		headerHeight = p(138)
	}
	f.box(gtx, image.Rect(0, 0, w, headerHeight), 12, f.p.Surface)
	actionsLeft := w - p(181)
	labelRight := actionsLeft - p(11)
	buttonTop := p(24)
	if compact {
		labelRight = w - p(14)
		actionsLeft = p(16)
		buttonTop = p(86)
	}
	title, desc := f.tr("编辑桌面布局", "Edit desktop layout"), f.tr("拖动组件标题栏即可移动位置，拖动右下角可缩放尺寸，双击标题栏可卷起面板。", "Drag title bars to move widgets, drag bottom-right corner to resize, or double-click title bars to roll up.")
	if f.model.Editing {
		title = f.tr("完成布局编辑", "Finish editing")
		desc = f.tr("正在编辑布局，所有桌面组件均可拖动调整位置。", "Layout editing is active. Drag any desktop widget by its title bar.")
	}
	f.label(gtx, image.Rect(p(18), p(12), labelRight, p(42)), f.heading("桌面布局", "Desktop layout"), 16, font.SemiBold, f.p.Text, layout.W)
	f.paragraph(gtx, image.Rect(p(18), p(44), labelRight, p(88)), desc, f.p.Secondary, 2)
	f.textButton(gtx, image.Rect(actionsLeft, buttonTop, actionsLeft+p(165), buttonTop+p(36)), &f.edit, title, true)
}

func (f settingsFrame) commands(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	commands := f.model.Commands
	count := len(commands)
	if count == 0 {
		cardH := min(h, p(156))
		f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)
		f.label(gtx, image.Rect(p(18), p(24), w-p(18), p(54)), f.heading("暂无可用的插件指令", "No plugin commands available"), 15, font.SemiBold, f.p.Text, layout.Center)
		f.label(gtx, image.Rect(p(18), p(56), w-p(18), p(82)), f.tr("启用提供指令的插件后，相关操作会显示在此处。", "Enable plugins that contribute commands to run them here."), 12.5, font.Normal, f.p.Secondary, layout.Center)
		if f.goPluginsButton.Clicked(gtx) {
			f.SetPage(f.sectionForCard("plugins", PagePlugins))
		}
		btnW := p(148)
		btnX := (w - btnW) / 2
		f.textButton(gtx, image.Rect(btnX, p(96), btnX+btnW, p(130)), &f.goPluginsButton, f.tr("前往插件管理", "Open Plugins"), true)
		return
	}

	rowH := p(58)
	headH := p(46)
	boxBottom := min(h, headH+rowH*count+p(4))
	f.box(gtx, image.Rect(0, 0, w, boxBottom), 12, f.p.Surface)
	f.label(gtx, image.Rect(p(18), p(8), w-p(18), headH), f.heading("可用指令", "Available commands"), 15, font.SemiBold, f.p.Text, layout.W)
	settingsArea(gtx, image.Rect(p(2), headH, w-p(2), boxBottom-p(2)), func(gtx layout.Context) {
		f.list(gtx, &f.commandsList, count, func(gtx layout.Context, index int) layout.Dimensions {
			cmd := commands[index]
			key := [2]string{cmd.PluginID, cmd.ID}
			b := f.commandButtons[key]
			if b == nil {
				b = &widget.Clickable{}
				f.commandButtons[key] = b
			}
			if b.Clicked(gtx) {
				f.emit(SettingsAction{
					Kind:       RunCommand,
					PluginID:   cmd.PluginID,
					CommandID:  cmd.ID,
					Generation: cmd.Generation,
				})
			}
			width := gtx.Constraints.Max.X
			settingsFill(gtx, image.Rect(p(14), 0, width-p(14), max(1, p(1))), 0, f.p.Line)
			source := cmd.PluginName
			if source == "" {
				source = cmd.PluginID
			}
			f.label(gtx, image.Rect(p(16), p(8), width-p(96), p(32)), cmd.Title, 14, font.Medium, f.p.Text, layout.W)
			f.monoLabel(gtx, image.Rect(p(16), p(31), width-p(96), p(50)), source, 11.5, font.Normal, f.p.Secondary, layout.W)
			f.textButton(gtx, image.Rect(width-p(88), p(12), width-p(14), rowH-p(12)), b, f.tr("执行", "Run"), false)
			return layout.Dimensions{Size: image.Pt(width, rowH)}
		})
	})
}
