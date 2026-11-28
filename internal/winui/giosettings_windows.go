package winui

import (
	"log"
	"slices"
	"strings"
	"sync/atomic"
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"
	"quailcrate/internal/diagnostic"
	"quailcrate/internal/gioui"
	"quailcrate/internal/pluginapps"
	apptheme "quailcrate/internal/theme"
	v2 "quailcrate/pluginapi/v2"
)

type Plugin = gioui.SettingsPlugin
type Command = gioui.SettingsCommand

func (u *UI) SetSettingsDefinition(def *v2.Settings) {
	u.post(func() {
		if u.settingsDefinition != nil && u.settingsSource == def && u.settingsLocale == u.locale {
			return
		}
		u.settingsSource, u.settingsLocale = def, u.locale
		u.settingsDefinition = u.withHostSections(def)
		u.refreshSettingsLabels()
		u.refreshSettings()
	})
}

func settingsHasCard(sections []v2.SettingsSection, kind string) bool {
	for _, section := range sections {
		for _, card := range section.Cards {
			if card.Type == kind {
				return true
			}
		}
	}
	return false
}

// withHostSections ensures host-managed settings cards (sign-in startup,
// application updates, and plugin recovery) stay accessible even if Workbench
// is disabled or replaced by a third-party settings layout.
func (u *UI) withHostSections(def *v2.Settings) *v2.Settings {
	shell := v2.Settings{
		UIVersion: 2,
		Title:     u.label("鹌鹑工具箱 · 设置", "QuailCrate · Settings"),
	}
	if def != nil {
		shell = *def
		shell.Sections = make([]v2.SettingsSection, len(def.Sections))
		for i, sec := range def.Sections {
			sec.Cards = slices.Clone(sec.Cards)
			shell.Sections[i] = sec
		}
	}

	var missingGeneral []v2.SettingsCard
	if !settingsHasCard(shell.Sections, "autostart") {
		missingGeneral = append(missingGeneral, v2.SettingsCard{
			Type:  "autostart",
			Title: u.label("开机启动", "Start on sign-in"),
		})
	}
	if !settingsHasCard(shell.Sections, "updates") {
		missingGeneral = append(missingGeneral, v2.SettingsCard{
			Type:  "updates",
			Title: u.label("应用更新", "App updates"),
		})
	}
	if len(missingGeneral) > 0 {
		if sec := shell.Section(gioui.PageGeneral); sec != nil {
			sec.Cards = append(sec.Cards, missingGeneral...)
		} else {
			genSection := v2.SettingsSection{
				ID:          gioui.PageGeneral,
				Title:       u.label("常规", "General"),
				Description: u.label("管理开机启动与应用更新。", "Manage sign-in startup and application updates."),
				Icon:        "settings",
				Cards:       missingGeneral,
			}
			if def == nil {
				shell.Sections = append([]v2.SettingsSection{genSection}, shell.Sections...)
			} else {
				shell.Sections = append(shell.Sections, genSection)
			}
		}
	}

	if !settingsHasCard(shell.Sections, "plugins") {
		title := u.label("插件管理", "Plugin management")
		id := gioui.PagePlugins
		for shell.Section(id) != nil {
			id += "-recovery"
		}
		shell.Sections = append(shell.Sections, v2.SettingsSection{
			ID:          id,
			Title:       title,
			Description: u.label("管理已安装的插件，可随时重新启用工作台。", "Manage installed plugins and re-enable Workbench anytime."),
			Icon:        "code",
			Cards:       []v2.SettingsCard{{Type: "plugins", Title: title}},
		})
	}
	return &shell
}

func (u *UI) settingsTitle() string {
	if u.settingsDefinition != nil {
		return u.settingsDefinition.Title
	}
	return u.label("鹌鹑工具箱 · 设置", "QuailCrate · Settings")
}

// The host owns plugin state on the Win32 thread. Gio owns the editor, focus
// and layout on its event loop and only reads immutable model snapshots.
type gioSettingsWindow struct {
	window   app.Window
	model    atomic.Pointer[gioui.SettingsModel]
	hwnd     atomic.Uintptr
	nextPage atomic.Pointer[string]
	closing  atomic.Bool
}

func (u *UI) appearanceTargets() []gioui.SettingsAppearanceTarget {
	targets := []gioui.SettingsAppearanceTarget{
		{ID: apptheme.ComponentGlobal, LabelZH: "全局默认", LabelEN: "Global", SupportsOpacity: true},
		{ID: apptheme.ComponentCalendar, LabelZH: "桌面月历", LabelEN: "Calendar", SupportsOpacity: true},
		{ID: apptheme.ComponentGroups, LabelZH: "桌面分组", LabelEN: "Groups", SupportsOpacity: true},
		{ID: apptheme.ComponentLauncher, LabelZH: "快速启动", LabelEN: "Launcher", SupportsOpacity: true},
		{ID: apptheme.ComponentSettings, LabelZH: "设置面板", LabelEN: "Settings", SupportsOpacity: false},
	}
	seen := map[string]bool{
		apptheme.ComponentGlobal:   true,
		apptheme.ComponentCalendar: true,
		apptheme.ComponentGroups:   true,
		apptheme.ComponentLauncher: true,
		apptheme.ComponentSettings: true,
	}
	for _, w := range u.widgetModels {
		if seen[w.PluginID] {
			continue
		}
		seen[w.PluginID] = true
		label := w.View.Title
		if label == "" {
			label = w.PluginID
		}
		targets = append(targets, gioui.SettingsAppearanceTarget{
			ID: w.PluginID, LabelZH: label, LabelEN: label, SupportsOpacity: true,
		})
	}
	return targets
}

func (u *UI) settingsModel() *gioui.SettingsModel {
	cfg := u.appearance.Normalize()
	m := &gioui.SettingsModel{
		Definition: u.settingsDefinition,
		Locale:     u.locale, LanguagePreference: u.languagePreference,
		Hotkey: u.hotkey, HotkeyStatus: u.hotkeyStatus,
		HotkeyRegistered: u.hotkeyID != 0, Editing: u.editing,
		CustomTitleBar:    true,
		AppsExpanded:      u.appsExpanded,
		Appearance:        cfg,
		AppearanceTargets: u.appearanceTargets(),
		Plugins:           u.plugins,
		Commands:          u.listedCommands(),
		AppTabs:           u.appSnapshot.Tabs,
		Update:            u.settingsUpdateModel(),
		Autostart:         u.settingsAutostartModel(),
	}
	if isSystemHighContrast() {
		p := highContrastPalette()
		m.HighContrast = true
		m.Palette = &gioui.SettingsPalette{
			Background: gioColor(p.background), Surface: gioColor(p.surface), Sidebar: gioColor(p.surface),
			Text: gioColor(p.text), Secondary: gioColor(p.secondary), Line: gioColor(p.border), Accent: gioColor(p.accent),
			Selected: gioColor(p.selected), Hover: gioColor(p.selected), Weekend: gioColor(p.accent),
			Warning: gioColor(p.warning), WarningBg: gioColor(p.surface), OnAccent: gioColor(p.white),
		}
	} else {
		pal := gioui.SettingsPaletteFromTheme(cfg.Resolve(apptheme.ComponentSettings).Palette)
		m.Palette = &pal
	}
	return m
}

func (u *UI) showSettings() {
	u.startUpdateSubscription()
	u.refreshAutostart()
	if u.settings != 0 {
		u.showNativeSettings()
		return
	}
	if s := u.gioSettings; s != nil {
		if h := s.hwnd.Load(); h != 0 {
			if iconic, _, _ := settingsIsIconic.Call(h); iconic != 0 {
				settingsShowAsync.Call(h, 9) // SW_RESTORE
			}
			setForegroundWindow.Call(h)
		}
		return
	}
	s := new(gioSettingsWindow)
	u.gioSettings = s
	s.model.Store(u.settingsModel())
	s.setPageID(u.currentSettingsPageID())
	foreground, _, _ := getForegroundWindow.Call()
	work := monitorOf(foreground).Work
	dpi := dpiForWindow(foreground)
	// Frameless Gio window with custom Geek HUD titlebar + Windows 11 rounded corners.
	width := unit.Dp(max(1, min(1040, unscale(work.width(), dpi)-16)))
	height := unit.Dp(max(1, min(680, unscale(work.height(), dpi)-32)))
	s.window.Option(
		app.Title(u.settingsTitle()),
		app.Decorated(false),
		app.Size(width, height),
		app.MinSize(min(620, width), min(420, height)),
	)
	outerWidth := min(scale(1040, dpi), work.width())
	outerHeight := min(scale(680, dpi), work.height())
	x := work.Left + (work.width()-outerWidth)/2
	y := work.Top + (work.height()-outerHeight)/2
	go s.run(u, rect{x, y, x + outerWidth, y + outerHeight})
}

var (
	settingsShowAsync      = user32.NewProc("ShowWindowAsync")
	settingsDwmExtendFrame = settingsDwm.NewProc("DwmExtendFrameIntoClientArea")
)

type dwmMargins struct{ Left, Right, Top, Bottom int32 }

func applySettingsDWMFrame(hwnd uintptr, borderColor uintptr, isDark bool) {
	if hwnd == 0 {
		return
	}
	// Override Gio's {-1,-1,-1,-1} full-client glass sheet with a 1px top margin:
	// keeps native Windows 11 drop shadow & rounded corners without DWM black flash on drag/resize.
	margins := dwmMargins{Top: 1}
	settingsDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	corner := uint32(2) // DWMWCP_ROUND
	settingsDwmAttribute.Call(hwnd, 33, uintptr(unsafe.Pointer(&corner)), 4)
	darkVal := uint32(0)
	if isDark {
		darkVal = 1
	}
	settingsDwmAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&darkVal)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	borderVal := uint32(borderColor)
	settingsDwmAttribute.Call(hwnd, 34, uintptr(unsafe.Pointer(&borderVal)), 4) // DWMWA_BORDER_COLOR
}

func (s *gioSettingsWindow) setPageID(id string) {
	if id == "" {
		id = gioui.PageGeneral
	}
	v := id
	s.nextPage.Store(&v)
	s.window.Invalidate()
}

func (u *UI) showAppearanceSettings() {
	u.setSettingsPageID(gioui.PageAppearance)
	u.showSettings()
}

func (s *gioSettingsWindow) close() {
	s.closing.Store(true)
	if h := s.hwnd.Load(); h != 0 {
		postMessage.Call(h, wmClose, 0, 0)
	}
}

func (s *gioSettingsWindow) run(u *UI, initialBounds rect) {
	var ops op.Ops
	var view gioui.SettingsView
	var lastTitle string
	var renderedDefinition *v2.Settings
	var lastBorder uintptr
	var lastDark int32 = -1
	var maximized bool
	send := func(a gioui.SettingsAction) {
		switch a.Kind {
		case gioui.WindowMinimize:
			s.window.Perform(system.ActionMinimize)
			return
		case gioui.WindowToggleMaximize:
			if maximized {
				s.window.Perform(system.ActionUnmaximize)
			} else {
				s.window.Perform(system.ActionMaximize)
			}
			return
		case gioui.WindowClose:
			s.window.Perform(system.ActionClose)
			return
		}
		def := renderedDefinition
		u.post(func() {
			if u.gioSettings == s && u.settingsDefinition == def {
				u.handleGioSettingsAction(a)
			}
		})
	}
	for {
		switch e := s.window.Event().(type) {
		case app.ConfigEvent:
			maximized = e.Config.Mode == app.Maximized
			if hwnd := s.hwnd.Load(); hwnd != 0 && lastDark >= 0 {
				borderCopy, darkCopy := lastBorder, lastDark == 1
				s.window.Run(func() {
					applySettingsDWMFrame(hwnd, borderCopy, darkCopy)
				})
			}
		case app.Win32ViewEvent:
			s.hwnd.Store(e.HWND)
			if e.HWND == 0 {
				continue
			}
			if s.closing.Load() {
				postMessage.Call(e.HWND, wmClose, 0, 0)
				continue
			}
			m := s.model.Load()
			res := m.Appearance.Resolve(apptheme.ComponentSettings)
			borderCol := colorRef(res.Palette.Border)
			if m.HighContrast {
				borderCol = 0xffffffff
			}
			lastBorder = borderCol
			lastDark = 0
			if res.IsDark {
				lastDark = 1
			}
			s.window.Run(func() {
				applySettingsDWMFrame(e.HWND, borderCol, res.IsDark)
				if initialBounds.width() > 0 && initialBounds.height() > 0 {
					setWindowPos.Call(e.HWND, 0, coord(initialBounds.Left), coord(initialBounds.Top),
						coord(initialBounds.width()), coord(initialBounds.height()), swpNoZOrder|swpNoActivate)
				}
			})
		case app.FrameEvent:
			m := *s.model.Load()
			renderedDefinition = m.Definition
			m.Maximized = maximized
			res := m.Appearance.Resolve(apptheme.ComponentSettings)
			borderCol := colorRef(res.Palette.Border)
			if m.HighContrast {
				borderCol = 0xffffffff
			}
			darkInt := int32(0)
			if res.IsDark {
				darkInt = 1
			}
			title := "QuailCrate · Settings"
			if strings.HasPrefix(m.Locale, "zh") {
				title = "鹌鹑工具箱 · 设置"
			}
			if m.Definition != nil {
				title = m.Definition.Title
			}
			if lastTitle != title || lastBorder != borderCol || lastDark != darkInt {
				lastTitle = title
				lastBorder = borderCol
				lastDark = darkInt
				isDark := darkInt == 1
				s.window.Run(func() {
					hwnd := s.hwnd.Load()
					setText(hwnd, title)
					applySettingsDWMFrame(hwnd, borderCol, isDark)
				})
			}
			if req := s.nextPage.Swap(nil); req != nil {
				view.SetPage(*req)
			}
			view.Layout(app.NewContext(&ops, e), m, send)
			e.Frame(&ops)
		case app.DestroyEvent:
			page := view.CurrentPage()
			expanded := view.AppsExpanded()
			u.post(func() {
				if u.gioSettings != s {
					return
				}
				u.gioSettings = nil
				u.appsExpanded = expanded
				u.settingsPageID = u.normalizeSettingsPage(page)
				u.settingsStyle.page = nativePageForID(u.settingsPageID)
				if e.Err != nil && !s.closing.Load() {
					log.Printf("Gio settings unavailable, using native settings: %s", diagnostic.English(e.Err))
					u.showNativeSettings()
				}
			})
			return
		}
	}
}

func (u *UI) handleGioSettingsAction(a gioui.SettingsAction) {
	switch a.Kind {
	case gioui.ChangeHotkey:
		if u.cb.ChangeHotkey != nil {
			u.cb.ChangeHotkey(strings.TrimSpace(a.Value))
		}
	case gioui.ChangeLanguage:
		if u.cb.ChangeLanguage != nil {
			u.cb.ChangeLanguage(a.Value)
		}
	case gioui.PreviewAppearance:
		cfg := apptheme.ParseConfig(a.Value)
		u.applyAppearance(cfg)
		if u.cb.PreviewAppearance != nil {
			u.cb.PreviewAppearance(cfg)
		}
	case gioui.SaveAppearance:
		cfg := apptheme.ParseConfig(a.Value)
		u.applyAppearance(cfg)
		if u.cb.SaveAppearance != nil {
			u.cb.SaveAppearance(cfg)
		}
	case gioui.ToggleEditing:
		u.setEditing(a.Enabled)
	case gioui.TogglePlugin:
		for _, p := range u.plugins {
			if p.ID == a.PluginID && p.CanToggle && p.Enabled != a.Enabled && u.cb.TogglePlugin != nil {
				u.cb.TogglePlugin(p.ID, a.Enabled)
				return
			}
		}
	case gioui.RevokePermission:
		if u.cb.RevokePluginPermission != nil && a.PluginID != "" && a.Value != "" {
			u.cb.RevokePluginPermission(a.PluginID, a.Value)
		}
	case gioui.RunCommand:
		for _, cmd := range u.listedCommands() {
			if cmd.PluginID == a.PluginID && cmd.ID == a.CommandID {
				gen := a.Generation
				if gen == 0 {
					gen = cmd.Generation
				}
				u.invokeListedCommand(cmd.PluginID, cmd.ID, gen)
				return
			}
		}
	case gioui.ToggleAppsExpanded:
		u.appsExpanded = a.Enabled
		u.refreshSettings()
	case gioui.OpenAppTab:
		u.openPluginAppTab(pluginapps.TabRef{
			PluginID:   a.PluginID,
			TabID:      a.CommandID,
			Generation: a.Generation,
		})
	case gioui.OpenAppsMenu:
		u.openAppsPopupMenu(cursorPosition())
	case gioui.Rescan:
		if u.cb.Rescan != nil {
			u.cb.Rescan()
		}
	case gioui.OpenPluginDir:
		if u.cb.OpenPluginDir != nil {
			u.cb.OpenPluginDir()
		}
	case gioui.CopyDiagnostics:
		if err := copySettingsDiagnostics(u.dialogOwner(), a.Value); err != nil {
			u.Message("Copy diagnostics failed", err.Error())
		}
	case gioui.ToggleAutoUpdate:
		u.setAutoUpdate(a.Enabled)
	case gioui.CheckUpdateNow:
		u.checkUpdateNow()
	case gioui.RestartUpdate:
		u.requestUpdateRestart()
	case gioui.OpenUpdateRelease:
		u.openUpdateRelease(a.Value)
	case gioui.ToggleAutostart:
		u.setAutostart(a.Enabled)
	case gioui.RefreshAutostart:
		u.refreshAutostart()
	}
}
