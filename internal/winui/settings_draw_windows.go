package winui

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"quailcrate/internal/gioui"
)

var (
	settingsDwm              = windows.NewLazySystemDLL("dwmapi.dll")
	settingsDwmAttribute     = settingsDwm.NewProc("DwmSetWindowAttribute")
	settingsIsIconic         = user32.NewProc("IsIconic")
	settingsDrawFocusRect    = user32.NewProc("DrawFocusRect")
	settingsRoundRect        = gdi32.NewProc("RoundRect")
	settingsStockObject      = gdi32.NewProc("GetStockObject")
	settingsSetBkColor       = gdi32.NewProc("SetBkColor")
	settingsOpenClipboard    = user32.NewProc("OpenClipboard")
	settingsCloseClipboard   = user32.NewProc("CloseClipboard")
	settingsEmptyClipboard   = user32.NewProc("EmptyClipboard")
	settingsSetClipboardData = user32.NewProc("SetClipboardData")
	settingsGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	settingsGlobalFree       = kernel32.NewProc("GlobalFree")
	settingsGlobalLock       = kernel32.NewProc("GlobalLock")
	settingsGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
)

type settingsMinMax struct {
	Reserved, MaxSize, MaxPosition, MinTrack, MaxTrack point
}

type settingsDrawItem struct {
	ControlType, ControlID, Item, Action, State uint32
	Window, DC                                  uintptr
	Area                                        rect
	Data                                        uintptr
}

type settingsMeasureItem struct {
	ControlType, ControlID, Item, Width, Height uint32
	Data                                        uintptr
}

const (
	drawSelected    = 0x0001
	drawDisabled    = 0x0004
	drawFocus       = 0x0010
	drawNoFocusRect = 0x0200
)

func (u *UI) settingsListBrush(dc uintptr) uintptr {
	p := theme()
	s := &u.settingsStyle
	if s.surfaceBrush == 0 || s.surfaceColor != p.surface {
		if s.surfaceBrush != 0 {
			deleteObject.Call(s.surfaceBrush)
		}
		s.surfaceBrush, _, _ = createSolidBrush.Call(p.surface)
		s.surfaceColor = p.surface
	}
	settingsSetBkColor.Call(dc, p.surface)
	setTextColor.Call(dc, p.text)
	return s.surfaceBrush
}

func settingsHighContrast(p palette) bool {
	return p.surface == sysColor(5) && p.selected == sysColor(13)
}

func settingsTitleColor(p palette) uintptr {
	if settingsHighContrast(p) {
		return p.surface
	}
	return p.sidebar
}

func settingsSidebarColor(p palette) uintptr {
	if settingsHighContrast(p) {
		return p.surface
	}
	return p.sidebar
}

func settingsRoundFill(hdc uintptr, r rect, diameter int32, color uintptr) {
	if r.width() <= 0 || r.height() <= 0 {
		return
	}
	if diameter <= 1 {
		fill(hdc, r, color)
		return
	}
	if activeUI != nil && activeUI.settingsAA.fill(hdc, r, diameter, color) {
		return
	}
	brush, _, _ := createSolidBrush.Call(color)
	pen, _, _ := settingsStockObject.Call(8) // NULL_PEN
	oldBrush, _, _ := selectObject.Call(hdc, brush)
	oldPen, _, _ := selectObject.Call(hdc, pen)
	settingsRoundRect.Call(hdc, coord(r.Left), coord(r.Top), coord(r.Right), coord(r.Bottom), coord(diameter), coord(diameter))
	selectObject.Call(hdc, oldPen)
	selectObject.Call(hdc, oldBrush)
	deleteObject.Call(brush)
}

func settingsInset(r rect, n int32) rect {
	return rect{r.Left + n, r.Top + n, r.Right - n, r.Bottom - n}
}

func settingsCard(hdc uintptr, r rect, dpi int32, p palette) {
	diameter := scale(20, dpi)
	settingsRoundFill(hdc, r, diameter, p.border)
	settingsRoundFill(hdc, settingsInset(r, 1), diameter-2, p.surface)
}

func settingsParagraph(hdc uintptr, r rect, value string, f, color uintptr) {
	old, _, _ := selectObject.Call(hdc, f)
	setBkMode.Call(hdc, 1)
	setTextColor.Call(hdc, color)
	drawText.Call(hdc, uintptr(unsafe.Pointer(wstr(value))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x10|0x800|0x40000)
	selectObject.Call(hdc, old)
}

func (u *UI) paintSettings() {
	var ps paintStruct
	hdc, _, _ := beginPaint.Call(u.settings, uintptr(unsafe.Pointer(&ps)))
	defer endPaint.Call(u.settings, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	m := u.settingsMetrics()
	s, p := u.settingsStyle, theme()
	fill(hdc, clientRect(u.settings), p.background)
	fill(hdc, m.screen(rect{0, settingsTitleHeight, m.sidebar, m.height}), settingsSidebarColor(p))
	fill(hdc, m.screen(rect{m.sidebar, settingsTitleHeight, m.sidebar + 1, m.height}), p.border)
	u.paintSettingsTitle(hdc, m, p)

	var title, subtitle string
	switch s.page {
	case settingsDesktopPage:
		title = u.label("桌面", "Desktop")
		subtitle = u.label("调整桌面组件的布局位置与锁定编辑状态。", "Arrange desktop widgets and manage layout editing.")
	case settingsCommandsPage:
		title = u.label("指令", "Commands")
		subtitle = u.label("运行已启用插件提供的公开桌面与设置指令。", "Run public commands contributed by enabled plugins.")
	case settingsPluginsPage:
		title = u.label("插件", "Plugins")
		subtitle = u.label("管理鹌鹑工具箱的插件。", "Manage QuailCrate plugins.")
	default:
		title = u.label("常规", "General")
		subtitle = u.label("设置快速启动快捷键和界面语言。", "Choose the launcher shortcut and display language.")
	}
	if section := u.settingsDefinition.Section(s.page.id()); section != nil {
		title, subtitle = section.Title, section.Description
	}
	text(hdc, m.screen(rect{m.left, 72, m.right, 116}), title, s.heading, p.text, 0)
	text(hdc, m.screen(rect{m.left, 117, m.right, 141}), subtitle, s.small, p.secondary, 0)

	switch s.page {
	case settingsDesktopPage:
		u.paintDesktopSettings(hdc, m, p)
	case settingsCommandsPage:
		u.paintCommandsSettings(hdc, m, p)
	case settingsPluginsPage:
		u.paintPluginSettings(hdc, m, p)
	default:
		u.paintGeneralSettings(hdc, m, p)
	}
}

func (u *UI) paintSettingsTitle(hdc uintptr, m settingsMetrics, p palette) {
	fill(hdc, m.screen(rect{0, 0, m.width, settingsTitleHeight}), settingsTitleColor(p))
	fill(hdc, m.screen(rect{0, settingsTitleHeight - 1, m.width, settingsTitleHeight}), p.border)
	mark := m.screen(rect{15, 14, 37, 36})
	settingsRoundFill(hdc, mark, scale(12, m.dpi), p.accent)
	// The toolbox mark uses the same accent and high-contrast colors as the controls.
	for _, line := range []rect{{21, 20, 31, 21}, {20, 22, 32, 23}, {20, 23, 21, 31}, {31, 23, 32, 31}, {21, 30, 31, 31}, {24, 24, 28, 26}} {
		fill(hdc, m.screen(line), p.white)
	}
	title := u.label("鹌鹑工具箱设置", "QuailCrate Settings")
	if u.settingsDefinition != nil {
		title = u.settingsDefinition.Title
	}
	text(hdc, m.screen(rect{46, 0, m.width - 3*settingsCaptionWidth, settingsTitleHeight}), title, u.settingsStyle.bold, p.text, 0)
}

func (u *UI) paintGeneralSettings(hdc uintptr, m settingsMetrics, p palette) {
	s := u.settingsStyle
	card := m.shortcutCard()
	settingsCard(hdc, m.screen(card), m.dpi, p)
	text(hdc, m.screen(rect{card.Left + 18, card.Top + 16, card.Right - 285, card.Top + 46}), u.label("快速启动快捷键", "Launcher shortcut"), s.bold, p.text, 0)
	text(hdc, m.screen(rect{card.Left + 18, card.Top + 47, card.Right - 16, card.Top + 72}), u.label("例如  Ctrl+Alt+Space", "For example  Ctrl+Alt+Space"), s.small, p.secondary, 0)
	status := m.screen(rect{card.Left + 16, card.Bottom - 38, card.Right - 16, card.Bottom - 12})
	bg, fg := p.selected, p.accent
	if u.hotkeyStatus != "" {
		bg, fg = settingsWarningColors(p)
	}
	if settingsHighContrast(p) && u.hotkeyStatus == "" {
		fg = p.white
	}
	settingsRoundFill(hdc, status, scale(9, m.dpi), bg)
	text(hdc, settingsInset(status, scale(8, m.dpi)), u.shortcutStatus(), s.small, fg, 0)

	language := m.languageCard()
	settingsCard(hdc, m.screen(language), m.dpi, p)
	text(hdc, m.screen(rect{language.Left + 18, language.Top + 16, language.Right - 225, language.Top + 44}), u.label("界面语言", "Display language"), s.bold, p.text, 0)
	settingsParagraph(hdc, m.screen(rect{language.Left + 18, language.Top + 45, language.Right - 228, language.Bottom - 12}),
		u.label("选择设置、菜单和支持翻译的插件使用的语言。", "Changes settings, menus, and plugins that provide translations."), s.small, p.secondary)
}

func (u *UI) paintDesktopSettings(hdc uintptr, m settingsMetrics, p palette) {
	s := u.settingsStyle
	card := m.layoutCard()
	settingsCard(hdc, m.screen(card), m.dpi, p)
	text(hdc, m.screen(rect{card.Left + 18, card.Top + 13, card.Right - 203, card.Top + 41}), u.label("桌面布局", "Desktop layout"), s.bold, p.text, 0)
	status := u.label("进入编辑模式后，拖动组件标题栏调整位置。", "Enter edit mode to move widgets by their title bars.")
	if u.editing {
		status = u.label("布局编辑中，拖动组件标题栏即可移动。", "Layout editing is active. Drag a widget by its title bar.")
	}
	settingsParagraph(hdc, m.screen(rect{card.Left + 18, card.Top + 43, card.Right - 204, card.Bottom - 8}), status, s.small, p.secondary)
}

func (u *UI) paintCommandsSettings(hdc uintptr, m settingsMetrics, p palette) {
	s := u.settingsStyle
	cmds := m.commandsCard()
	settingsCard(hdc, m.screen(cmds), m.dpi, p)
	text(hdc, m.screen(rect{cmds.Left + 17, cmds.Top + 12, cmds.Right - 18, cmds.Top + 39}),
		u.label("可用指令", "Available commands"), s.bold, p.text, 0)
}

func (u *UI) paintPluginSettings(hdc uintptr, m settingsMetrics, p palette) {
	s := u.settingsStyle
	geometry := u.pluginBounds(m)
	table := geometry.table
	running := 0
	for _, plugin := range u.plugins {
		if plugin.State == "running" {
			running++
		}
	}
	badge := m.screen(rect{m.right - 99, 108, m.right, 136})
	badgeTextColor := p.accent
	if settingsHighContrast(p) {
		badgeTextColor = p.white
	}
	settingsRoundFill(hdc, badge, scale(14, m.dpi), p.selected)
	dot := m.screen(rect{m.right - 90, 119, m.right - 83, 126})
	settingsRoundFill(hdc, dot, scale(7, m.dpi), badgeTextColor)
	runningText := fmt.Sprintf(u.label("%d 项运行中", "%d running"), running)
	text(hdc, m.screen(rect{m.right - 77, 108, m.right - 6, 136}), runningText, s.small, badgeTextColor, 0)
	settingsCard(hdc, m.screen(table), m.dpi, p)
	header := m.screen(rect{table.Left + 1, table.Top + 1, table.Right - 1, table.Top + 35})
	settingsRoundFill(hdc, header, scale(22, m.dpi), settingsSidebarColor(p))
	fill(hdc, m.screen(rect{table.Left + 1, table.Top + 18, table.Right - 1, table.Top + 35}), settingsSidebarColor(p))
	available := min(header.Right, header.Left+clientRect(u.settingsFields[settingsPluginList]).width())
	columns := pluginColumns(rect{header.Left, header.Top, available, header.Bottom}, m.dpi)
	for _, col := range []struct {
		r      rect
		zh, en string
		align  uintptr
	}{
		{rect{header.Left + scale(13, m.dpi), columns.name.Top, columns.name.Right, columns.name.Bottom}, "插件", "Plugin", 0},
		{columns.runtime, "运行时", "Runtime", 0},
		{columns.status, "状态", "Status", 0},
		{columns.enabled, "启用", "Enabled", 1},
	} {
		text(hdc, col.r, u.label(col.zh, col.en), s.small, p.secondary, col.align)
	}
	fill(hdc, m.screen(rect{table.Left + 1, table.Top + 34, table.Right - 1, table.Top + 35}), p.border)

	item := u.settingsDiagnostic()
	if item.Detail == "" {
		return
	}
	bg, fg := settingsWarningColors(p)
	if item.State != "failed" {
		bg, fg = p.background, p.secondary
	}
	r := m.screen(geometry.details)
	settingsRoundFill(hdc, r, scale(12, m.dpi), bg)
	label := geometry.details
	label.Left += 12
	label.Right = geometry.copy.Left - 8
	text(hdc, m.screen(label), item.Detail, s.small, fg, 0)
}

func settingsWarningColors(p palette) (uintptr, uintptr) {
	if settingsHighContrast(p) {
		return p.surface, p.text
	}
	return rgb(255, 242, 228), p.warning
}

type settingsPluginColumns struct {
	name, runtime, status, enabled rect
}

func pluginColumns(row rect, dpi int32) settingsPluginColumns {
	px := func(value int32) int32 { return scale(value, dpi) }
	runtimeWidth, statusWidth, toggleWidth := px(95), px(136), px(70)
	if row.width() < px(450) {
		runtimeWidth, statusWidth, toggleWidth = px(62), px(98), px(65)
	}
	statusRight := row.Right - toggleWidth
	runtimeRight := statusRight - statusWidth
	nameRight := runtimeRight - runtimeWidth
	withX := func(left, right int32) rect { return rect{left, row.Top, right, row.Bottom} }
	return settingsPluginColumns{
		name:    withX(row.Left+px(57), max(row.Left+px(61), nameRight-px(10))),
		runtime: withX(nameRight, runtimeRight-px(6)),
		status:  withX(runtimeRight, statusRight-px(5)),
		enabled: withX(statusRight, row.Right-px(6)),
	}
}

func (u *UI) drawSettingsItem(item *settingsDrawItem) {
	if item.DC == 0 {
		return
	}
	s, p := u.settingsStyle, theme()
	if s.dpi == 0 {
		s.dpi = dpiForWindow(u.settings)
	}
	switch item.ControlID {
	case settingsNavGeneral, settingsNavDesktop, settingsNavCommands, settingsNavPlugins, settingsNavApps, settingsSaveHotkey, settingsEdit, settingsRunCommand, settingsGoPlugins, settingsRescan, settingsOpenDir, settingsCopyDiagnostics:
		u.drawSettingsButton(item, s, p)
	case settingsCaptionMinimize, settingsCaptionMaximize, settingsCaptionClose:
		u.drawSettingsCaptionButton(item, s, p)
	case settingsPluginList:
		u.drawSettingsPlugin(item, s, p)
	case settingsCommandList:
		u.drawSettingsCommand(item, s, p)
	case settingsAppList:
		u.drawSettingsAppTab(item, s, p)
	}
	if item.State&drawFocus != 0 && item.State&drawNoFocusRect == 0 {
		focus := settingsInset(item.Area, scale(3, s.dpi))
		settingsDrawFocusRect.Call(item.DC, uintptr(unsafe.Pointer(&focus)))
	}
}

func (u *UI) drawSettingsButton(item *settingsDrawItem, s settingsVisual, p palette) {
	area, hdc := item.Area, item.DC
	id := int(item.ControlID)
	disabled, pressed := item.State&drawDisabled != 0, item.State&drawSelected != 0
	title := currentText(item.Window)
	if id >= settingsNavGeneral && id <= settingsNavPlugins {
		fill(hdc, area, settingsSidebarColor(p))
		selected := s.page == settingsPage(id-settingsNavGeneral)
		fg, f := p.text, s.body
		if selected || pressed {
			selectedColor := p.selected
			settingsRoundFill(hdc, area, scale(17, s.dpi), selectedColor)
			fg, f = p.accent, s.bold
			if settingsHighContrast(p) {
				fg = p.white
			}
		}
		if selected {
			bar := rect{area.Left, area.Top + scale(9, s.dpi), area.Left + scale(3, s.dpi), area.Bottom - scale(9, s.dpi)}
			settingsRoundFill(hdc, bar, scale(3, s.dpi), p.accent)
		}
		icons := []string{"\ue713", "\ue7f4", "\ue756", "{}"}
		iconFont := s.icon
		if id == settingsNavPlugins {
			iconFont = s.bold
		}
		text(hdc, rect{area.Left + scale(12, s.dpi), area.Top, area.Left + scale(33, s.dpi), area.Bottom}, icons[id-settingsNavGeneral], iconFont, fg, 1)
		text(hdc, rect{area.Left + scale(42, s.dpi), area.Top, area.Right - scale(6, s.dpi), area.Bottom}, title, f, fg, 0)
		return
	}
	if id == settingsNavApps {
		fill(hdc, area, settingsSidebarColor(p))
		fg := p.text
		if pressed {
			settingsRoundFill(hdc, area, scale(17, s.dpi), p.selected)
			fg = p.accent
			if settingsHighContrast(p) {
				fg = p.white
			}
		}
		chev := "▸"
		if u.appsExpanded {
			chev = "▾"
		}
		label := title
		if n := len(u.appSnapshot.Tabs); n > 0 {
			label = fmt.Sprintf("%s (%d)", title, n)
		}
		text(hdc, rect{area.Left + scale(12, s.dpi), area.Top, area.Left + scale(33, s.dpi), area.Bottom}, "\ue80a", s.icon, fg, 1)
		text(hdc, rect{area.Left + scale(42, s.dpi), area.Top, area.Right - scale(22, s.dpi), area.Bottom}, label, s.body, fg, 0)
		text(hdc, rect{area.Right - scale(20, s.dpi), area.Top, area.Right - scale(4, s.dpi), area.Bottom}, chev, s.small, p.secondary, 1)
		return
	}
	base := p.surface
	if id == settingsRescan || id == settingsOpenDir {
		base = p.background
	}
	if id == settingsCopyDiagnostics {
		diagnostic := u.settingsDiagnostic()
		bg, fg := settingsWarningColors(p)
		if diagnostic.State != "failed" {
			bg, fg = p.background, p.secondary
		}
		if pressed {
			bg = p.selected
		}
		fill(hdc, area, bg)
		text(hdc, settingsInset(area, scale(5, s.dpi)), title, s.small, fg, 2)
		return
	}
	fill(hdc, area, base)
	primary := id == settingsSaveHotkey || id == settingsRunCommand || id == settingsEdit
	border, bg, fg := p.border, p.surface, p.text
	if primary && !disabled {
		border, bg, fg = p.accent, p.accent, p.white
	} else if disabled {
		bg, fg = p.background, p.secondary
	}
	if pressed && !disabled {
		bg, fg = p.selected, p.accent
		if settingsHighContrast(p) {
			fg = p.white
		}
	}
	radius := scale(10, s.dpi)
	settingsRoundFill(hdc, area, radius, border)
	settingsRoundFill(hdc, settingsInset(area, 1), radius, bg)
	text(hdc, settingsInset(area, scale(6, s.dpi)), title, s.bold, fg, 1)
}

func (u *UI) drawSettingsCaptionButton(item *settingsDrawItem, s settingsVisual, p palette) {
	bg, fg := settingsTitleColor(p), p.secondary
	glyph := "\ue921"
	switch item.ControlID {
	case settingsCaptionMaximize:
		glyph = "\ue922"
		if u.settingsMaximized() {
			glyph = "\ue923"
		}
	case settingsCaptionClose:
		glyph = "\ue8bb"
	}
	if item.State&(drawSelected|0x0040) != 0 {
		bg, fg = p.selected, p.text
		if item.ControlID == settingsCaptionClose && !settingsHighContrast(p) {
			bg, fg = rgb(196, 43, 28), p.white
		}
		if settingsHighContrast(p) {
			fg = p.white
		}
	}
	fill(item.DC, item.Area, bg)
	text(item.DC, item.Area, glyph, s.icon, fg, 1)
}

func (u *UI) drawSettingsPlugin(item *settingsDrawItem, s settingsVisual, p palette) {
	r, hdc := item.Area, item.DC
	if item.Item == ^uint32(0) {
		fill(hdc, r, p.surface)
		return
	}
	if int(item.Item) >= len(u.plugins) {
		fill(hdc, r, p.surface)
		text(hdc, settingsInset(r, scale(16, s.dpi)), u.label("尚未发现插件", "No plugins found"), s.small, p.secondary, 0)
		return
	}
	plugin := u.plugins[item.Item]
	columns := pluginColumns(r, s.dpi)
	bg, fg, muted := p.surface, p.text, p.secondary
	if item.State&drawSelected != 0 {
		bg = rgb(239, 247, 242)
		if settingsHighContrast(p) {
			bg = p.selected
			fg, muted = p.white, p.white
		}
	}
	fill(hdc, r, bg)
	fill(hdc, rect{r.Left, r.Bottom - 1, r.Right, r.Bottom}, p.border)
	drawSettingsPluginIcon(hdc, r, plugin.Icon, s, p)
	name, description := columns.name, columns.name
	name.Top += scale(2, s.dpi)
	if plugin.Description != "" {
		name.Bottom = r.Top + scale(35, s.dpi)
		name.Top += scale(6, s.dpi)
		description.Top = name.Bottom - scale(2, s.dpi)
		description.Bottom = r.Bottom - scale(7, s.dpi)
		text(hdc, description, plugin.Description, s.small, muted, 0)
	}
	text(hdc, name, plugin.Name, s.bold, fg, 0)

	runtime := strings.ToUpper(plugin.Runtime)
	if runtime == "LUA" {
		runtime = "Lua"
	}
	if runtime != "" {
		badge := rect{columns.runtime.Left, r.Top + scale(18, s.dpi), columns.runtime.Left + scale(36, s.dpi), r.Bottom - scale(18, s.dpi)}
		badgeBorder, badgeFill, badgeInk := p.border, p.background, p.secondary
		if plugin.FullSystemAccess {
			badgeFill, badgeInk = settingsWarningColors(p)
			badgeBorder = badgeInk
		}
		settingsRoundFill(hdc, badge, scale(8, s.dpi), badgeBorder)
		settingsRoundFill(hdc, settingsInset(badge, 1), scale(8, s.dpi), badgeFill)
		text(hdc, badge, runtime, s.small, badgeInk, 1)
	}

	statusColor := muted
	switch plugin.State {
	case "running":
		statusColor = p.accent
	case "failed":
		statusColor = p.warning
	case "authorization-required":
		statusColor = p.warning
	}
	if settingsHighContrast(p) && item.State&drawSelected != 0 {
		statusColor = p.white
	}
	label := columns.status
	if plugin.State == "failed" {
		warning := rect{label.Left, label.Top, label.Left + scale(13, s.dpi), label.Bottom}
		text(hdc, warning, "△", s.small, statusColor, 1)
		label.Left += scale(18, s.dpi)
	} else {
		dot := rect{label.Left, r.Top + (r.height()-scale(7, s.dpi))/2, label.Left + scale(7, s.dpi), r.Top + (r.height()+scale(7, s.dpi))/2}
		settingsRoundFill(hdc, dot, scale(7, s.dpi), statusColor)
		label.Left += scale(12, s.dpi)
	}
	text(hdc, label, plugin.Status, s.small, statusColor, 0)

	dpi := s.dpi
	toggle := rect{
		columns.enabled.Left + (columns.enabled.width()-scale(35, dpi))/2, r.Top + (r.height()-scale(19, dpi))/2,
		columns.enabled.Left + (columns.enabled.width()+scale(35, dpi))/2, r.Top + (r.height()+scale(19, dpi))/2,
	}
	track, thumb := rgb(124, 144, 137), p.secondary
	if settingsHighContrast(p) {
		track = p.border
	}
	if plugin.Enabled {
		track, thumb = p.accent, p.white
	}
	if item.State&drawSelected != 0 && settingsHighContrast(p) {
		track, thumb = p.text, p.surface
	}
	if !plugin.CanToggle {
		track, thumb = p.border, p.surface
	}
	settingsRoundFill(hdc, toggle, scale(19, dpi), track)
	if !plugin.Enabled {
		settingsRoundFill(hdc, settingsInset(toggle, 1), scale(17, dpi), p.surface)
	}
	circle := rect{toggle.Left + scale(3, dpi), toggle.Top + scale(3, dpi), toggle.Left + scale(16, dpi), toggle.Bottom - scale(3, dpi)}
	if plugin.Enabled {
		shift := toggle.width() - scale(19, dpi)
		circle.Left += shift
		circle.Right += shift
	}
	settingsRoundFill(hdc, circle, scale(14, dpi), thumb)
}

func drawSettingsPluginIcon(hdc uintptr, row rect, icon string, s settingsVisual, p palette) {
	glyph, fontHandle := "{}", s.body
	bg, fg := rgb(230, 237, 240), rgb(49, 83, 92)
	switch icon {
	case "search":
		glyph, fontHandle = "\ue721", s.icon
		bg, fg = rgb(230, 241, 255), rgb(43, 101, 150)
	case "folder":
		glyph, fontHandle = "\ue8b7", s.icon
		bg, fg = rgb(248, 234, 212), rgb(132, 81, 31)
	case "calendar":
		glyph, fontHandle = "\ue787", s.icon
		bg, fg = p.selected, p.accent
	case "image":
		glyph, fontHandle = "\ue91b", s.icon
		bg, fg = rgb(237, 233, 251), rgb(98, 78, 141)
	}
	if settingsHighContrast(p) {
		bg, fg = p.surface, p.text
	}
	size := scale(33, s.dpi)
	x, y := row.Left+scale(13, s.dpi), row.Top+(row.height()-size)/2
	bounds := rect{x, y, x + size, y + size}
	settingsRoundFill(hdc, bounds, scale(15, s.dpi), bg)
	text(hdc, bounds, glyph, fontHandle, fg, 1)
}

func (u *UI) drawSettingsCommand(item *settingsDrawItem, s settingsVisual, p palette) {
	r, hdc := item.Area, item.DC
	bg, fg, muted := p.surface, p.text, p.secondary
	if item.State&drawSelected != 0 {
		bg = p.selected
		if settingsHighContrast(p) {
			fg, muted = p.white, p.white
		}
	}
	fill(hdc, r, bg)
	if item.Item == ^uint32(0) {
		return
	}
	commands := u.listedCommands()
	if int(item.Item) >= len(commands) {
		r.Left += scale(8, s.dpi)
		r.Right -= scale(8, s.dpi)
		text(hdc, r, u.label("暂无可用指令", "No commands available"), s.body, p.secondary, 0)
		return
	}
	cmd := commands[item.Item]
	fill(hdc, rect{r.Left, r.Bottom - 1, r.Right, r.Bottom}, p.border)
	r.Left += scale(10, s.dpi)
	r.Right -= scale(10, s.dpi)
	if cmd.PluginName != "" {
		top := rect{r.Left, r.Top + scale(4, s.dpi), r.Right, r.Top + scale(25, s.dpi)}
		bot := rect{r.Left, r.Top + scale(24, s.dpi), r.Right, r.Bottom - scale(4, s.dpi)}
		text(hdc, top, cmd.Title, s.bold, fg, 0)
		text(hdc, bot, cmd.PluginName, s.small, muted, 0)
		return
	}
	text(hdc, r, cmd.Title, s.body, fg, 0)
}

func (u *UI) drawSettingsAppTab(item *settingsDrawItem, s settingsVisual, p palette) {
	r, hdc := item.Area, item.DC
	bg, fg := settingsSidebarColor(p), p.text
	if item.State&drawSelected != 0 {
		bg = p.selected
		fg = p.accent
		if settingsHighContrast(p) {
			fg = p.white
		}
	}
	fill(hdc, r, bg)
	if item.Item == ^uint32(0) {
		return
	}
	tabs := u.appSnapshot.Tabs
	label := u.label("暂无插件应用", "No plugin apps")
	if int(item.Item) < len(tabs) {
		label = gioui.DisambiguateTabLabels(tabs)[item.Item]
	} else {
		fg = p.secondary
	}
	r.Left += scale(10, s.dpi)
	r.Right -= scale(6, s.dpi)
	text(hdc, r, label, s.small, fg, 0)
}

func copySettingsDiagnostics(owner uintptr, content string) error {
	value, err := syscall.UTF16FromString(strings.ReplaceAll(content, "\x00", "�"))
	if err != nil {
		return fmt.Errorf("Prepare diagnostics: %w", err)
	}
	memory, _, systemErr := settingsGlobalAlloc.Call(2, uintptr(len(value))*2)
	if memory == 0 {
		return fmt.Errorf("Allocate clipboard data (%s)", win32Error(systemErr))
	}
	defer func() {
		if memory != 0 {
			settingsGlobalFree.Call(memory)
		}
	}()
	address, _, systemErr := settingsGlobalLock.Call(memory)
	if address == 0 {
		return fmt.Errorf("Lock clipboard data (%s)", win32Error(systemErr))
	}
	copy(unsafe.Slice(nativePointer[uint16](address), len(value)), value)
	settingsGlobalUnlock.Call(memory)
	if result, _, systemErr := settingsOpenClipboard.Call(owner); result == 0 {
		return fmt.Errorf("Open clipboard (%s)", win32Error(systemErr))
	}
	defer settingsCloseClipboard.Call()
	if result, _, systemErr := settingsEmptyClipboard.Call(); result == 0 {
		return fmt.Errorf("Clear clipboard (%s)", win32Error(systemErr))
	}
	if result, _, systemErr := settingsSetClipboardData.Call(13, memory); result == 0 {
		return fmt.Errorf("Write clipboard data (%s)", win32Error(systemErr))
	}
	memory = 0 // The clipboard owns the memory after SetClipboardData succeeds.
	return nil
}
