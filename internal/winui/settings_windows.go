package winui

import (
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"quailcrate/internal/gioui"
)

const (
	settingsNavGeneral = 200 + iota
	settingsNavDesktop
	settingsNavCommands
	settingsNavPlugins
	settingsNavApps
	settingsAppList
	settingsHotkeyInput
	settingsSaveHotkey
	settingsLanguageSelect
	settingsEdit
	settingsCommandList
	settingsRunCommand
	settingsGoPlugins
	settingsPluginList
	settingsRescan
	settingsOpenDir
	settingsCopyDiagnostics
	settingsCaptionMinimize
	settingsCaptionMaximize
	settingsCaptionClose
)

const (
	wmSettingsMeasureItem     = 0x002c
	wmSettingsDrawItem        = 0x002b
	wmSettingsControlColor    = 0x0134 // WM_CTLCOLORLISTBOX
	wmSettingsMinMax          = 0x0024
	wmSettingsEraseBackground = 0x0014
	wsThickFrame              = 0x00040000
	wsMinimizeBox             = 0x00020000
	wsMaximizeBox             = 0x00010000
	wsClipChildren            = 0x02000000
	ownerDrawButton           = 0x000b
	ownerDrawList             = wsTabStop | wsVScroll | 0x0151 // NOTIFY | OWNERDRAWFIXED | HASSTRINGS | NOINTEGRALHEIGHT
	extendedClientEdge        = 0x0200
	settingsTitleHeight       = 48
	settingsPluginRowHeight   = 60
	settingsCommandRowHeight  = 46
	settingsAppRowHeight      = 32
)

type settingsPage int

const (
	settingsGeneralPage settingsPage = iota
	settingsDesktopPage
	settingsCommandsPage
	settingsPluginsPage
)

func (p settingsPage) id() string {
	switch p {
	case settingsDesktopPage:
		return gioui.PageDesktop
	case settingsCommandsPage:
		return gioui.PageCommands
	case settingsPluginsPage:
		return gioui.PagePlugins
	default:
		return gioui.PageGeneral
	}
}

func normalizeSettingsPageID(id string) string {
	switch id {
	case gioui.PageAppearance, gioui.PageDesktop, gioui.PageCommands, gioui.PagePlugins:
		return id
	default:
		return gioui.PageGeneral
	}
}

func nativePageForID(id string) settingsPage {
	switch id {
	case gioui.PageDesktop:
		return settingsDesktopPage
	case gioui.PageCommands:
		return settingsCommandsPage
	case gioui.PagePlugins:
		return settingsPluginsPage
	default:
		return settingsGeneralPage
	}
}

type settingsControl struct {
	id        int
	page      settingsPage // A negative page represents persistent sidebar controls.
	class     string
	style, ex uintptr
}

var settingsControls = []settingsControl{
	{settingsNavGeneral, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsNavDesktop, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsNavCommands, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsNavPlugins, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsNavApps, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsAppList, -1, "LISTBOX", ownerDrawList, 0},
	{settingsHotkeyInput, settingsGeneralPage, "EDIT", wsTabStop | 0x0080, extendedClientEdge},
	{settingsSaveHotkey, settingsGeneralPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsLanguageSelect, settingsGeneralPage, "COMBOBOX", wsTabStop | wsVScroll | 3, 0},
	{settingsEdit, settingsDesktopPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsCommandList, settingsCommandsPage, "LISTBOX", ownerDrawList, 0},
	{settingsRunCommand, settingsCommandsPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsGoPlugins, settingsCommandsPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsPluginList, settingsPluginsPage, "LISTBOX", ownerDrawList, 0},
	{settingsRescan, settingsPluginsPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsOpenDir, settingsPluginsPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsCopyDiagnostics, settingsPluginsPage, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsCaptionMinimize, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsCaptionMaximize, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
	{settingsCaptionClose, -1, "BUTTON", wsTabStop | ownerDrawButton, 0},
}

type settingsVisual struct {
	page                             settingsPage
	dpi                              int32
	fontLocale                       string
	body, bold, small, heading, icon uintptr
	surfaceBrush, surfaceColor       uintptr
	pluginListProc                   uintptr
	pressedSwitch                    int
}

func (s *settingsVisual) releaseFonts() {
	for _, f := range []uintptr{s.body, s.bold, s.small, s.heading, s.icon} {
		if f != 0 {
			deleteObject.Call(f)
		}
	}
}

func (s *settingsVisual) release() {
	s.releaseFonts()
	if s.surfaceBrush != 0 {
		deleteObject.Call(s.surfaceBrush)
	}
}

// showNativeSettings keeps settings available if the graphics driver cannot run Gio.
func (u *UI) showNativeSettings() {
	if u.settings == 0 {
		if err := u.createSettings(); err != nil {
			u.Message("Settings window failed", err.Error())
			return
		}
	}
	u.refreshSettings()
	visible := uintptr(swShow)
	if iconified, _, _ := settingsIsIconic.Call(u.settings); iconified != 0 {
		visible = 9
	} // SW_RESTORE
	showWindow.Call(u.settings, visible)
	setForegroundWindow.Call(u.settings)
}

func (u *UI) ownsSettingControl(hwnd uintptr) bool {
	if u.settings == 0 {
		return false
	}
	if u.settings == hwnd {
		return true
	}
	for _, handle := range u.settingsFields {
		if handle == hwnd {
			return true
		}
	}
	return false
}

func (u *UI) createSettings() error {
	foreground, _, _ := getForegroundWindow.Call()
	m := monitorOf(foreground)
	width, height := min(int32(1032), m.Work.width()), min(int32(654), m.Work.height())
	left, top := m.Work.Left+(m.Work.width()-width)/2, m.Work.Top+(m.Work.height()-height)/2
	bounds := rect{left, top, left + width, top + height}
	h, err := window(settingsClass, "", wsExControlParent, wsCaption|wsSysMenu|wsThickFrame|wsMinimizeBox|wsMaximizeBox|wsClipChildren, bounds, 0, 0, u.module)
	if err != nil {
		return err
	}
	u.settings = h
	u.settingsStyle.pressedSwitch = -1
	u.configureSettingsFrame()
	for _, spec := range settingsControls {
		field, err := window(spec.class, "", spec.ex, spec.style|wsChild, rect{}, h, uintptr(spec.id), u.module)
		if err != nil {
			destroyWindow.Call(h)
			u.settings = 0
			clear(u.settingsFields)
			return err
		}
		u.settingsFields[spec.id] = field
	}
	list := u.settingsFields[settingsPluginList]
	u.settingsStyle.pluginListProc, _, _ = setWindowLong.Call(list, ^uintptr(3), settingsPluginListCallback)
	sendMessage.Call(u.settingsFields[settingsHotkeyInput], 0x00c5, 64, 0)
	setText(u.settingsFields[settingsHotkeyInput], u.hotkey)
	dpi := dpiForWindow(h)
	width, height = min(scale(1032, dpi), m.Work.width()), min(scale(654, dpi), m.Work.height())
	left, top = m.Work.Left+(m.Work.width()-width)/2, m.Work.Top+(m.Work.height()-height)/2
	move(h, rect{left, top, left + width, top + height})
	u.refreshSettingsLabels()
	u.layoutSettings()
	u.setSettingsPage(u.settingsStyle.page)
	return nil
}

func (u *UI) currentSettingsPageID() string {
	return u.normalizeSettingsPage(u.settingsPageID)
}

func (u *UI) normalizeSettingsPage(id string) string {
	if u.settings != 0 && u.gioSettings == nil {
		return normalizeSettingsPageID(id)
	}
	if def := u.settingsDefinition; def != nil {
		if def.Section(id) != nil {
			return id
		}
		return def.Sections[0].ID
	}
	return normalizeSettingsPageID(id)
}

func (u *UI) setSettingsPage(page settingsPage) {
	u.setSettingsPageID(page.id())
}

func (u *UI) setSettingsPageID(id string) {
	id = u.normalizeSettingsPage(id)
	u.settingsPageID = id
	page := nativePageForID(id)
	u.settingsStyle.page = page
	if s := u.gioSettings; s != nil {
		s.setPageID(id)
		return
	}
	if u.settings == 0 || len(u.settingsFields) != len(settingsControls) {
		return
	}
	commands := u.listedCommands()
	for _, spec := range settingsControls {
		if spec.id == settingsNavApps || spec.id == settingsAppList {
			continue
		}
		visible := uintptr(swHide)
		if spec.id == settingsGoPlugins {
			if page == settingsCommandsPage && len(commands) == 0 {
				visible = swShowNoActivate
			}
		} else if spec.page < 0 || spec.page == page {
			visible = swShowNoActivate
		}
		showWindow.Call(u.settingsFields[spec.id], visible)
		if spec.page < 0 {
			repaint(u.settingsFields[spec.id])
		}
	}
	u.refreshNativeAppsVisibility()
	u.refreshPluginSelection()
}

func (u *UI) setSettingsFonts(dpi int32) {
	s := &u.settingsStyle
	if s.dpi == dpi && s.fontLocale == u.locale {
		return
	}
	previous := *s
	bodyFamily, headingFamily := settingsFontFamilies(u.locale)
	s.dpi = dpi
	s.fontLocale = u.locale
	s.body = makeSettingsFont(bodyFamily, dpi, 14, 400)
	s.bold = makeSettingsFont(bodyFamily, dpi, 14, 600)
	s.small = makeSettingsFont(bodyFamily, dpi, 13, 400)
	s.heading = makeSettingsFont(headingFamily, dpi, 30, 700)
	s.icon = makeSettingsFont("Segoe MDL2 Assets", dpi, 16, 400)
	for _, field := range u.settingsFields {
		sendMessage.Call(field, wmSetFont, s.body, 1)
	}
	previous.releaseFonts()
	sendMessage.Call(u.settingsFields[settingsCommandList], 0x01a0, 0, coord(scale(settingsCommandRowHeight, dpi)))
	sendMessage.Call(u.settingsFields[settingsPluginList], 0x01a0, 0, coord(scale(settingsPluginRowHeight, dpi)))
	sendMessage.Call(u.settingsFields[settingsAppList], 0x01a0, 0, coord(scale(settingsAppRowHeight, dpi)))
}

type settingsMetrics struct {
	dpi, width, height, sidebar, left, right int32
}

func (u *UI) settingsMetrics() settingsMetrics {
	dpi := dpiForWindow(u.settings)
	r := clientRect(u.settings)
	width, height := unscale(r.width(), dpi), unscale(r.height(), dpi)
	sidebar := min(int32(186), max(int32(120), width/4))
	return settingsMetrics{dpi, width, height, sidebar, sidebar + 26, width - 27}
}

func (m settingsMetrics) screen(r rect) rect {
	return rect{scale(r.Left, m.dpi), scale(r.Top, m.dpi), scale(r.Right, m.dpi), scale(r.Bottom, m.dpi)}
}

func (m settingsMetrics) shortcutCard() rect { return rect{m.left, 148, m.right, 270} }
func (m settingsMetrics) languageCard() rect { return rect{m.left, 284, m.right, 372} }
func (m settingsMetrics) layoutCard() rect   { return rect{m.left, 148, m.right, 232} }
func (m settingsMetrics) commandsCard() rect {
	return rect{m.left, 148, m.right, max(int32(310), m.height-24)}
}

type settingsPluginBounds struct {
	table, list, details, copy, rescan, folder rect
}

func (u *UI) pluginBounds(m settingsMetrics) settingsPluginBounds {
	const top = 154
	headEnd := int32(top + 35)
	listTarget := headEnd + settingsPluginRowHeight*int32(max(1, len(u.plugins)))
	diagnosticHeight := int32(0)
	if u.settingsDiagnostic().Detail != "" {
		diagnosticHeight = 54
	}
	listEnd := max(headEnd+settingsPluginRowHeight, min(listTarget, m.height-62-diagnosticHeight))
	tableEnd := listEnd + diagnosticHeight
	if diagnosticHeight == 0 {
		tableEnd += 3
	}
	return settingsPluginBounds{
		table:   rect{m.left, top, m.right, tableEnd},
		list:    rect{m.left + 1, headEnd, m.right - 1, listEnd},
		details: rect{m.left + 13, listEnd + 3, m.right - 13, tableEnd - 10},
		copy:    rect{m.right - 165, listEnd + 7, m.right - 20, tableEnd - 14},
		rescan:  rect{m.right - 254, tableEnd + 12, m.right - 172, tableEnd + 44},
		folder:  rect{m.right - 162, tableEnd + 12, m.right, tableEnd + 44},
	}
}

func (u *UI) layoutSettings() {
	if len(u.settingsFields) != len(settingsControls) {
		return
	}
	m := u.settingsMetrics()
	u.setSettingsFonts(m.dpi)
	card, cmds, plugins := m.shortcutCard(), m.commandsCard(), u.pluginBounds(m)
	places := []struct {
		id   int
		area rect
	}{
		{settingsNavGeneral, rect{12, 71, m.sidebar - 11, 110}},
		{settingsNavDesktop, rect{12, 114, m.sidebar - 11, 153}},
		{settingsNavCommands, rect{12, 157, m.sidebar - 11, 196}},
		{settingsNavPlugins, rect{12, 200, m.sidebar - 11, 239}},
		{settingsNavApps, rect{12, 243, m.sidebar - 11, 282}},
		{settingsAppList, rect{16, 286, m.sidebar - 11, max(320, m.height-16)}},
		{settingsHotkeyInput, rect{card.Right - 270, card.Top + 22, card.Right - 115, card.Top + 54}},
		{settingsSaveHotkey, rect{card.Right - 103, card.Top + 21, card.Right - 16, card.Top + 55}},
		{settingsLanguageSelect, rect{m.right - 215, 309, m.right - 16, 428}},
		{settingsEdit, rect{m.right - 197, 172, m.right - 16, 208}},
		{settingsCommandList, rect{cmds.Left + 14, cmds.Top + 49, cmds.Right - 14, cmds.Bottom - 54}},
		{settingsRunCommand, rect{cmds.Right - 114, cmds.Bottom - 44, cmds.Right - 14, cmds.Bottom - 11}},
		{settingsGoPlugins, rect{cmds.Left + 14, cmds.Bottom - 44, cmds.Left + 154, cmds.Bottom - 11}},
		{settingsOpenDir, plugins.folder},
		{settingsRescan, plugins.rescan},
		{settingsPluginList, plugins.list},
		{settingsCopyDiagnostics, plugins.copy},
		{settingsCaptionMinimize, m.captionButton(0)},
		{settingsCaptionMaximize, m.captionButton(1)},
		{settingsCaptionClose, m.captionButton(2)},
	}
	navOrder := u.nativeNavOrder()
	for _, p := range places {
		r := p.area
		if p.id >= settingsNavGeneral && p.id <= settingsNavPlugins {
			height := r.height()
			r.Top = 71 + 43*int32(slices.Index(navOrder, p.id))
			r.Bottom = r.Top + height
		}
		r.Right = max(r.Left+1, r.Right)
		r.Bottom = max(r.Top+1, r.Bottom)
		move(u.settingsFields[p.id], m.screen(r))
	}
	u.refreshNativeAppsVisibility()
	repaint(u.settings)
}

var supportedPreferences = []string{"system", "en-US", "zh-CN"}

func (u *UI) nativeNavOrder() []int {
	var out []int
	if def := u.settingsDefinition; def != nil {
		for _, section := range def.Sections {
			for id := settingsNavGeneral; id <= settingsNavPlugins; id++ {
				if settingsPage(id-settingsNavGeneral).id() == section.ID {
					out = append(out, id)
				}
			}
		}
	}
	for id := settingsNavGeneral; id <= settingsNavPlugins; id++ {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (u *UI) refreshSettingsLabels() {
	if u.settings == 0 {
		return
	}
	setText(u.settings, u.settingsTitle())
	for _, label := range []struct {
		id     int
		zh, en string
	}{
		{settingsNavGeneral, "常规", "General"},
		{settingsNavDesktop, "桌面", "Desktop"},
		{settingsNavCommands, "指令", "Commands"},
		{settingsNavPlugins, "插件", "Plugins"},
		{settingsNavApps, "应用", "Apps"},
		{settingsSaveHotkey, "应用", "Apply"},
		{settingsRescan, "重新扫描", "Rescan"},
		{settingsOpenDir, "打开插件目录", "Open plugins folder"},
		{settingsRunCommand, "执行", "Run"},
		{settingsGoPlugins, "前往插件管理", "Open Plugins"},
		{settingsCopyDiagnostics, "复制诊断信息", "Copy diagnostics"},
		{settingsCaptionMinimize, "最小化", "Minimize"},
		{settingsCaptionClose, "关闭", "Close"},
	} {
		value := u.label(label.zh, label.en)
		if label.id >= settingsNavGeneral && label.id <= settingsNavPlugins {
			if section := u.settingsDefinition.Section(settingsPage(label.id - settingsNavGeneral).id()); section != nil {
				value = section.Title
			}
		} else if label.id == settingsNavApps && u.settingsDefinition != nil && u.settingsDefinition.AppsTitle != "" {
			value = u.settingsDefinition.AppsTitle
		}
		setText(u.settingsFields[label.id], value)
	}
	combo := u.settingsFields[settingsLanguageSelect]
	sendMessage.Call(combo, 0x014b, 0, 0)
	index := 0
	for i, label := range []string{u.label("跟随系统", "System default"), "English", "简体中文"} {
		sendMessage.Call(combo, 0x0143, 0, uintptr(unsafe.Pointer(wstr(label))))
		if supportedPreferences[i] == u.languagePreference {
			index = i
		}
	}
	sendMessage.Call(combo, 0x014e, uintptr(index), 0)
	u.refreshSettingsCaption()
}

func (u *UI) SetPlugins(plugins []Plugin) {
	plugins = slices.Clone(plugins)
	u.post(func() {
		prior := u.selectedPluginID()
		u.plugins = plugins
		u.refreshSettings()
		if prior != "" {
			for i, plugin := range plugins {
				if plugin.ID == prior {
					setListSelection(u.settingsFields[settingsPluginList], i)
					u.refreshPluginSelection()
					break
				}
			}
		}
	})
}

func (u *UI) SetCommands(commands []Command) {
	commands = slices.Clone(commands)
	u.post(func() { u.commands = commands; u.refreshSettings() })
}

func (u *UI) settingsCommands() []Command {
	var commands []Command
	for _, cmd := range u.commands {
		if cmd.Location == "settings" || cmd.Location == "desktop-edit" {
			commands = append(commands, cmd)
		}
	}
	return commands
}

func (u *UI) activateSettingsNav(window uintptr) bool {
	for id := settingsNavGeneral; id <= settingsNavPlugins; id++ {
		if u.settingsFields[id] == window {
			u.setSettingsPage(settingsPage(id - settingsNavGeneral))
			return true
		}
	}
	if u.settingsFields[settingsNavApps] == window {
		u.toggleNativeAppsMenu()
		return true
	}
	if u.settingsFields[settingsAppList] == window {
		u.openSelectedNativeAppTab()
		return true
	}
	return false
}

func (u *UI) toggleNativeAppsMenu() {
	if len(u.appSnapshot.Tabs) == 0 {
		return
	}
	m := u.settingsMetrics()
	if m.sidebar < 140 {
		r := windowRect(u.settingsFields[settingsNavApps])
		u.openAppsPopupMenu(point{r.Right, r.Top})
		return
	}
	u.appsExpanded = !u.appsExpanded
	u.refreshNativeAppsVisibility()
	repaint(u.settingsFields[settingsNavApps])
}

func (u *UI) refreshNativeAppsVisibility() {
	menu, list := uintptr(swHide), uintptr(swHide)
	if len(u.appSnapshot.Tabs) > 0 && (u.settingsDefinition == nil || u.settingsDefinition.AppsTitle != "") {
		menu = swShowNoActivate
		if u.appsExpanded && u.settingsMetrics().sidebar >= 140 {
			list = swShowNoActivate
		}
	}
	showWindow.Call(u.settingsFields[settingsNavApps], menu)
	showWindow.Call(u.settingsFields[settingsAppList], list)
}

func (u *UI) openSelectedNativeAppTab() {
	tabs := u.appSnapshot.Tabs
	i := listSelection(u.settingsFields[settingsAppList])
	if i >= 0 && i < len(tabs) {
		u.openPluginAppTab(tabs[i].TabRef)
	}
}

func listSelection(h uintptr) int {
	result, _, _ := sendMessage.Call(h, 0x0188, 0, 0)
	return int(result)
}

func setListSelection(h uintptr, selection int) { sendMessage.Call(h, 0x0186, uintptr(selection), 0) }

func addListRow(h uintptr, value string) {
	sendMessage.Call(h, 0x0180, 0, uintptr(unsafe.Pointer(wstr(value))))
}

func (u *UI) selectedPluginID() string {
	if u.settings == 0 {
		return ""
	}
	index := listSelection(u.settingsFields[settingsPluginList])
	if index >= 0 && index < len(u.plugins) {
		return u.plugins[index].ID
	}
	return ""
}

func (u *UI) settingsDiagnostic() Plugin {
	index := listSelection(u.settingsFields[settingsPluginList])
	if index >= 0 && index < len(u.plugins) && u.plugins[index].Detail != "" {
		return u.plugins[index]
	}
	for _, plugin := range u.plugins {
		if plugin.State == "failed" && plugin.Detail != "" {
			return plugin
		}
	}
	return Plugin{}
}

func (u *UI) refreshSettings() {
	if s := u.gioSettings; s != nil {
		s.model.Store(u.settingsModel())
		s.window.Invalidate()
		return
	}
	if u.settings == 0 {
		return
	}
	pluginList := u.settingsFields[settingsPluginList]
	pluginSelection := listSelection(pluginList)
	sendMessage.Call(pluginList, 0x0184, 0, 0)
	for _, plugin := range u.plugins {
		state := u.label("已禁用", "Disabled")
		if plugin.Enabled {
			state = u.label("已启用", "Enabled")
		}
		addListRow(pluginList, plugin.Name+" · "+plugin.Description+" · "+strings.ToUpper(plugin.Runtime)+" · "+plugin.Status+" · "+state)
	}
	if len(u.plugins) == 0 {
		addListRow(pluginList, u.label("尚未发现插件", "No plugins found"))
	} else if pluginSelection >= 0 {
		setListSelection(pluginList, min(pluginSelection, len(u.plugins)-1))
	}
	commandList := u.settingsFields[settingsCommandList]
	commandSelection := listSelection(commandList)
	sendMessage.Call(commandList, 0x0184, 0, 0)
	commands := u.listedCommands()
	for _, cmd := range commands {
		row := cmd.Title
		if cmd.PluginName != "" {
			row += " · " + cmd.PluginName
		}
		addListRow(commandList, row)
	}
	if len(commands) > 0 {
		setListSelection(commandList, max(0, min(commandSelection, len(commands)-1)))
	} else {
		addListRow(commandList, u.label("暂无可用指令", "No commands available"))
	}
	enableWindow.Call(u.settingsFields[settingsRunCommand], boolParam(len(commands) > 0))
	if u.settingsStyle.page == settingsCommandsPage {
		showGo := uintptr(swHide)
		if len(commands) == 0 {
			showGo = swShowNoActivate
		}
		showWindow.Call(u.settingsFields[settingsGoPlugins], showGo)
	}
	appList := u.settingsFields[settingsAppList]
	appSelection := listSelection(appList)
	sendMessage.Call(appList, 0x0184, 0, 0)
	tabs := u.appSnapshot.Tabs
	if len(tabs) > 0 {
		for _, label := range gioui.DisambiguateTabLabels(tabs) {
			addListRow(appList, label)
		}
		setListSelection(appList, max(0, min(appSelection, len(tabs)-1)))
	}
	u.refreshNativeAppsVisibility()
	repaint(u.settingsFields[settingsNavApps])
	button := u.label("编辑桌面布局", "Edit desktop layout")
	if u.editing {
		button = u.label("结束布局编辑", "Finish editing layout")
	}
	setText(u.settingsFields[settingsEdit], button)
	u.refreshPluginSelection()
}

func boolParam(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func (u *UI) refreshPluginSelection() {
	copyAllowed := u.settingsDiagnostic().Detail != ""
	copyButton := u.settingsFields[settingsCopyDiagnostics]
	enableWindow.Call(copyButton, boolParam(copyAllowed))
	if u.settingsStyle.page == settingsPluginsPage {
		u.layoutSettings()
		visible := uintptr(swHide)
		if copyAllowed {
			visible = swShowNoActivate
		}
		showWindow.Call(copyButton, visible)
	}
	repaint(u.settings)
}

func (u *UI) toggleSettingsPlugin(i int) {
	if i >= 0 && i < len(u.plugins) && u.plugins[i].CanToggle && u.cb.TogglePlugin != nil {
		p := u.plugins[i]
		u.cb.TogglePlugin(p.ID, !p.Enabled)
	}
}

var settingsPluginListCallback = syscall.NewCallback(settingsPluginListProcedure)

func settingsPluginListProcedure(hwnd, msg, wParam, lParam uintptr) uintptr {
	u := activeUI
	if u == nil {
		result, _, _ := defWindowProc.Call(hwnd, msg, wParam, lParam)
		return result
	}
	s := &u.settingsStyle
	switch msg {
	case wmLButtonDown:
		s.pressedSwitch = u.hitSettingsSwitch(hwnd, lParam)
	case wmLButtonUp:
		index := u.hitSettingsSwitch(hwnd, lParam)
		result, _, _ := callWindowProc.Call(s.pluginListProc, hwnd, msg, wParam, lParam)
		if index >= 0 && index == s.pressedSwitch {
			u.toggleSettingsPlugin(index)
		}
		s.pressedSwitch = -1
		return result
	case wmKeyDown:
		if wParam == vkSpace {
			u.toggleSettingsPlugin(listSelection(hwnd))
			return 0
		}
	}
	result, _, _ := callWindowProc.Call(s.pluginListProc, hwnd, msg, wParam, lParam)
	return result
}

func (u *UI) hitSettingsSwitch(hwnd, lParam uintptr) int {
	p := mousePoint(lParam)
	r := clientRect(hwnd)
	left := r.Right - scale(63, dpiForWindow(hwnd))
	if p.X < left || !r.contains(p) {
		return -1
	}
	index, _, _ := sendMessage.Call(hwnd, 0x01a9, 0, lParam) // LB_ITEMFROMPOINT
	if high(index) != 0 || low(index) >= len(u.plugins) {
		return -1
	}
	var row rect
	result, _, _ := sendMessage.Call(hwnd, 0x0198, uintptr(low(index)), uintptr(unsafe.Pointer(&row)))
	if int32(result) < 0 || !row.contains(p) {
		return -1
	}
	return low(index)
}

func (u *UI) settingsProcedure(msg, wParam, lParam uintptr) (uintptr, bool) {
	if result, handled := u.settingsFrameProcedure(msg, wParam, lParam); handled {
		return result, true
	}
	switch msg {
	case wmClose:
		showWindow.Call(u.settings, swHide)
		u.settingsAA.close()
	case wmSize:
		if wParam != 1 {
			u.layoutSettings()
			u.refreshSettingsCaption()
		} // SIZE_MINIMIZED
	case wmDpiChanged:
		move(u.settings, *nativePointer[rect](lParam))
		u.layoutSettings()
	case wmSettingsMinMax:
		info := nativePointer[settingsMinMax](lParam)
		dpi := dpiForWindow(u.settings)
		m := monitorOf(u.settings)
		info.MinTrack = point{min(scale(680, dpi), m.Work.width()), min(scale(450, dpi), m.Work.height())}
	case wmSettingsEraseBackground:
		return 1, true
	case wmPaint:
		u.paintSettings()
	case wmSettingChange:
		repaint(u.settings)
	case wmSettingsControlColor:
		return u.settingsListBrush(wParam), true
	case wmSettingsMeasureItem:
		item := nativePointer[settingsMeasureItem](lParam)
		switch item.ControlID {
		case settingsPluginList:
			item.Height = uint32(scale(settingsPluginRowHeight, dpiForWindow(u.settings)))
		case settingsAppList:
			item.Height = uint32(scale(settingsAppRowHeight, dpiForWindow(u.settings)))
		default:
			item.Height = uint32(scale(settingsCommandRowHeight, dpiForWindow(u.settings)))
		}
		return 1, true
	case wmSettingsDrawItem:
		u.drawSettingsItem(nativePointer[settingsDrawItem](lParam))
		return 1, true
	case wmCommand:
		return u.settingsCommand(low(wParam), high(wParam))
	default:
		return 0, false
	}
	return 0, true
}

func (u *UI) settingsCommand(id, notification int) (uintptr, bool) {
	switch id {
	case settingsNavGeneral, settingsNavDesktop, settingsNavCommands, settingsNavPlugins:
		u.setSettingsPage(settingsPage(id - settingsNavGeneral))
	case settingsNavApps:
		u.toggleNativeAppsMenu()
	case settingsAppList:
		if notification == 2 {
			u.openSelectedNativeAppTab()
		}
	case settingsGoPlugins:
		u.setSettingsPage(settingsPluginsPage)
	case settingsSaveHotkey:
		if u.cb.ChangeHotkey != nil {
			u.cb.ChangeHotkey(strings.TrimSpace(currentText(u.settingsFields[settingsHotkeyInput])))
		}
	case settingsLanguageSelect:
		if notification == 1 && u.cb.ChangeLanguage != nil {
			index, _, _ := sendMessage.Call(u.settingsFields[settingsLanguageSelect], 0x0147, 0, 0)
			if index < uintptr(len(supportedPreferences)) {
				u.cb.ChangeLanguage(supportedPreferences[index])
			}
		}
	case settingsPluginList:
		u.refreshPluginSelection()
	case settingsRescan:
		if u.cb.Rescan != nil {
			u.cb.Rescan()
		}
	case settingsOpenDir:
		if u.cb.OpenPluginDir != nil {
			u.cb.OpenPluginDir()
		}
	case settingsRunCommand, settingsCommandList:
		if id == settingsCommandList && notification != 2 {
			return 0, true
		}
		commands := u.listedCommands()
		i := listSelection(u.settingsFields[settingsCommandList])
		if i >= 0 && i < len(commands) {
			cmd := commands[i]
			u.invokeListedCommand(cmd.PluginID, cmd.ID, cmd.Generation)
		}
	case settingsEdit:
		u.setEditing(!u.editing)
	case settingsCopyDiagnostics:
		if p := u.settingsDiagnostic(); p.Detail != "" {
			if err := copySettingsDiagnostics(u.settings, p.ID+"\r\n"+p.Status+"\r\n"+p.Detail); err != nil {
				u.Message("Copy diagnostics failed", err.Error())
			}
		}
	case settingsCaptionMinimize, settingsCaptionMaximize, settingsCaptionClose:
		u.activateSettingsCaption(id)
	default:
		return 0, false
	}
	return 0, true
}
