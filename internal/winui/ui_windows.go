package winui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"quailcrate/internal/autostart"
	"quailcrate/internal/gioui"
	"quailcrate/internal/pluginapps"
	apptheme "quailcrate/internal/theme"
	"quailcrate/internal/update"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type Callbacks struct {
	ActivateTray           func(TrayEntry) bool
	PluginApps             pluginapps.Service
	Ready                  func()
	Search                 func(query string) uint64
	OpenResult             func(Result)
	ResultAction           func(Result, ResultMenuAction)
	ChangeHotkey           func(string)
	ChooseLauncher         func(string)
	ChangeLanguage         func(string)
	PreviewAppearance      func(apptheme.Config)
	SaveAppearance         func(apptheme.Config)
	SaveAppBounds          func(map[string]AppWindowBounds)
	TogglePlugin           func(id string, enabled bool)
	RevokePluginPermission func(id, permission string)
	Rescan                 func()
	OpenPluginDir          func()
	InstallPlugin          func()
	UninstallPlugin        func(string)
	OpenPluginHomepage     func(string)
	WidgetEvent            func(pluginID string, event v1.Event)
	MoveWidget             func(pluginID, instanceID string, generation uint64, layout Layout)
	Command                func(pluginID, commandID string)
	SystemChanged          func()
	UpdateSnapshot         func() update.Snapshot
	SubscribeUpdates       func(func(update.Snapshot)) func()
	SetAutoUpdate          func(context.Context, bool) error
	CheckUpdateNow         func() bool
	RequestUpdateRestart   func() bool
	AutostartStatus        func() (autostart.Status, error)
	SetAutostart           func(bool) error
	TrayExitRequested      func()
	Exit                   func()
}

type Result struct {
	ID                                         string
	Generation, Revision                       uint64
	Actions                                    []ResultMenuAction
	Item                                       *v2.ItemRef
	PluginID, CommandID, Title, Subtitle, Icon string
	Args                                       json.RawMessage
}

type ResultMenuAction struct {
	ID, Title, Provider, Service, Method, Version string
	Generation                                    uint64
}

type Layout struct {
	Revision                       uint64 // Orders UI edits and host snapshots within a widget generation.
	Monitor, Anchor                string
	X, Y, Width, Height            int
	Placed                         bool
	Collapsed, Locked, AlwaysOnTop bool
	ExpandedHeight                 int
	IconSize                       int
}

type Widget struct {
	Generation, Revision uint64
	PluginID, ID         string
	Layout               Layout
	View                 v1.View
}

type UI struct {
	tray                                                                  []TrayEntry
	cb                                                                    Callbacks
	module, host, launcher, edit, settings                                uintptr
	launcherFont, editProc                                                uintptr
	launcherFrame                                                         launcherSurface
	launcherChrome                                                        popupFrame
	launcherShells                                                        []LauncherShell
	launcherShell                                                         LauncherShell
	launcherLayout                                                        overlayLayout
	launchersReady, launcherRequested                                     bool
	launcherWheelRemainder                                                int
	launcherBrush, launcherBrushColor                                     uintptr
	prevActive                                                            uintptr
	taskbarCreated                                                        uintptr
	shell, desktopHost                                                    uintptr
	desktopMarker                                                         uintptr
	desktopTimer                                                          bool
	monitors                                                              []desktopMonitor
	desktopShown, desktopZDirty, desktopNeedsPlacement, adjustingDesktopZ bool
	hotkeyID, hotkeyMods, hotkeyVK                                        uintptr
	hotkey, hotkeyStatus                                                  string
	languagePreference, locale                                            string
	appearance                                                            apptheme.Config
	launcherQuery, launcherStatus                                         string
	launcherOpen                                                          bool
	launcherRevision                                                      uint64
	results                                                               []Result
	resultIndex, resultScroll                                             int
	launcherDPI                                                           int32
	plugins                                                               []Plugin
	pluginInstall                                                         PluginInstall
	commands                                                              []Command
	appSnapshot                                                           pluginapps.Snapshot
	appWindows                                                            map[string]*pluginAppWindow
	appBounds                                                             map[string]AppWindowBounds
	appsExpanded                                                          bool
	settingsPageID                                                        string
	settingsDefinition                                                    *v2.Settings
	settingsSource                                                        *v2.Settings
	settingsLocale                                                        string
	settingsStyle                                                         settingsVisual
	settingsAA                                                            settingsAntialias
	settingsFields                                                        map[int]uintptr
	gioSettings                                                           *gioSettingsWindow
	widgetRenderer                                                        widgetRenderer
	backdrop                                                              wallpaperBackdrop
	icons                                                                 iconCache
	widgets                                                               map[string]*widgetWindow
	widgetByWindow                                                        map[uintptr]*widgetWindow
	widgetModels                                                          []Widget
	itemDrag                                                              *itemDragState
	editing                                                               bool
	popup                                                                 *textPrompt
	picker                                                                *optionPicker
	menu                                                                  *popupMenu
	dialogQueue                                                           []*queuedDialog
	dialogBusy                                                            bool
	queueMu                                                               sync.Mutex
	queue                                                                 []func()
	postTarget                                                            uintptr
	closed                                                                bool
	stopCh                                                                chan struct{}
	stopOnce                                                              sync.Once
	updateSnapshot                                                        update.Snapshot
	updateSubscribed                                                      bool
	updateUnsubscribe                                                     func()
	updateUnavailable                                                     bool
	updateAutoBusy                                                        bool
	updateCheckPending                                                    bool
	updateCheckBefore                                                     time.Time
	updateRestartPending                                                  bool
	updateActionError                                                     string
	autostartStatus                                                       autostart.Status
	autostartLoaded                                                       bool
	autostartLoading                                                      bool
	autostartSaving                                                       bool
	autostartError                                                        string
	autostartSeq                                                          uint64
	openURL                                                               func(string) error
}

var activeUI *UI

const (
	hostClass     = "QuailCrateHost"
	launcherClass = "QuailCrateLauncher"
	settingsClass = "QuailCrateSettings"
	widgetClass   = "QuailCrateWidget"
	promptClass   = "QuailCratePrompt"
	pickerClass   = "QuailCratePicker"
	menuClass     = "QuailCrateMenu"
	mainTimer     = 1
	leaveTimer    = 3
)

func New(cb Callbacks) *UI {
	return &UI{
		cb:                 cb,
		hotkey:             "Alt+Space",
		languagePreference: "system",
		locale:             "zh-CN",
		appearance:         apptheme.DefaultConfig(),
		settingsPageID:     gioui.PageGeneral,
		appWindows:         make(map[string]*pluginAppWindow),
		appBounds:          make(map[string]AppWindowBounds),
		settingsFields:     make(map[int]uintptr),
		widgets:            make(map[string]*widgetWindow),
		widgetByWindow:     make(map[uintptr]*widgetWindow),
		stopCh:             make(chan struct{}),
	}
}

// Run owns the Win32 message loop. Call it from the main goroutine.
func (u *UI) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if activeUI != nil {
		return errors.New("a Windows UI is already running")
	}
	activeUI = u
	defer func() { activeUI = nil }()
	setDpiAwareness.Call(^uintptr(3))
	hr, _, _ := oleInitialize.Call(0)
	if int32(hr) < 0 {
		return fmt.Errorf("OleInitialize: %#08x", uint32(hr))
	}
	defer oleUninitialize.Call()
	u.module, _, _ = getModuleHandle.Call(0)
	if err := u.registerClasses(); err != nil {
		return err
	}
	var err error
	u.host, err = window(hostClass, "鹌鹑工具箱", wsExToolWindow, wsPopup, rect{}, 0, 0, u.module)
	if err != nil {
		return err
	}
	defer u.cleanup()
	u.taskbarCreated, _, _ = registerWindowMessage.Call(uintptr(unsafe.Pointer(wstr("TaskbarCreated"))))
	u.addTray()
	u.queueMu.Lock()
	u.postTarget = u.host
	hasWork := len(u.queue) != 0
	u.queueMu.Unlock()
	if hasWork {
		postMessage.Call(u.host, wmDispatch, 0, 0)
	}
	if u.cb.PluginApps != nil {
		go u.observePluginApps()
	}
	u.startUpdateSubscription()
	u.refreshAutostart()
	if u.cb.Ready != nil {
		u.cb.Ready()
	}

	for {
		var msg message
		ret, _, receiveErr := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			return fmt.Errorf("GetMessage failed (%s)", win32Error(receiveErr))
		case 0:
			return nil
		}
		if u.popup != nil || u.picker != nil {
			// Prompt and picker handle Enter/Esc/navigation directly with IME composition awareness.
		} else if u.ownsSettingControl(msg.HWnd) {
			if msg.Message == wmKeyDown && msg.WParam == vkReturn && u.activateSettingsNav(msg.HWnd) {
				continue
			}
			handled, _, _ := isDialogMessage.Call(u.settings, uintptr(unsafe.Pointer(&msg)))
			if handled != 0 {
				continue
			}
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (u *UI) registerClasses() error {
	cursor, _, _ := loadCursor.Call(0, 32512)
	callback := syscall.NewCallback(windowProcedure)
	chromeCallback := syscall.NewCallback(chromeWindowProcedure)
	for _, name := range []string{hostClass, launcherClass, settingsClass, widgetClass, promptClass, pickerClass, menuClass, chromeClass} {
		proc := callback
		if name == chromeClass {
			proc = chromeCallback
		}
		wc := wndClass{
			Size:  uint32(unsafe.Sizeof(wndClass{})),
			Style: 3 | 0x0008, WndProc: proc, Instance: u.module, Cursor: cursor,
			ClassName: wstr(name),
		}
		if name == settingsClass {
			wc.Background = 16
		}
		if result, _, err := registerClass.Call(uintptr(unsafe.Pointer(&wc))); result == 0 {
			if err != syscall.Errno(1410) {
				return fmt.Errorf("RegisterClassExW %s failed (%s)", name, win32Error(err))
			}
		}
	}
	return nil
}

func windowProcedure(hwnd, msg, wParam, lParam uintptr) uintptr {
	u := activeUI
	if u != nil {
		switch hwnd {
		case u.host:
			if result, ok := u.hostProcedure(msg, wParam, lParam); ok {
				return result
			}
		case u.launcher:
			if result, ok := u.launcherProcedure(msg, wParam, lParam); ok {
				return result
			}
		case u.settings:
			if result, ok := u.settingsProcedure(msg, wParam, lParam); ok {
				return result
			}
		default:
			if u.menu != nil && u.menu.hwnd == hwnd {
				if result, ok := u.menuProcedure(msg, wParam, lParam); ok {
					return result
				}
			} else if u.picker != nil && u.picker.hwnd == hwnd {
				if result, ok := u.pickerProcedure(msg, wParam, lParam); ok {
					return result
				}
			} else if u.popup != nil && u.popup.hwnd == hwnd {
				if result, ok := u.promptProcedure(msg, wParam, lParam); ok {
					return result
				}
			} else if widget := u.widgetByWindow[hwnd]; widget != nil {
				if result, ok := u.widgetProcedure(widget, msg, wParam, lParam); ok {
					return result
				}
			}
		}
	}
	result, _, _ := defWindowProc.Call(hwnd, msg, wParam, lParam)
	return result
}

func (u *UI) hostProcedure(msg, wParam, lParam uintptr) (uintptr, bool) {
	switch msg {
	case wmDispatch:
		u.queueMu.Lock()
		work := u.queue
		u.queue = nil
		u.queueMu.Unlock()
		for _, fn := range work {
			fn()
		}
	case wmHotKey:
		if wParam == u.hotkeyID {
			u.showLauncher()
		}
	case wmTray:
		if lParam == wmLButtonUp || lParam == wmRButtonUp {
			u.openTrayMenu()
		}
		if lParam == wmLButtonDblClk {
			u.showLauncher()
		}
	case wmTimer:
		if wParam == mainTimer && len(u.widgetModels) != 0 {
			u.updateDesktop()
		}
	case wmDisplayChange, wmSettingChange:
		u.repositionWidgets()
		if u.needsSpatialBackdrop() {
			u.refreshWallpaperBackdrop(false)
		}
		if msg == wmSettingChange {
			if u.gioSettings != nil {
				u.refreshSettings()
			}
			for _, aw := range u.appWindows {
				aw.refreshThemeAndLocale(u.locale, u.appearance)
			}
			for _, w := range u.widgets {
				u.presentWidget(w)
			}
			u.applyLauncherAppearance()
		}
	case wmTimeChange:
		if u.cb.SystemChanged != nil {
			u.cb.SystemChanged()
		}
	case wmPowerBroadcast:
		if (wParam == 7 || wParam == 18) && u.cb.SystemChanged != nil {
			u.cb.SystemChanged()
		}
		return 1, true
	case wmClose, wmDestroy:
		if !u.closed {
			postQuitMessage.Call(0)
		}
	default:
		if u.taskbarCreated != 0 && msg == u.taskbarCreated {
			u.addTray()
			u.rebuildWidgets()
		} else {
			return 0, false
		}
	}
	return 0, true
}

func (u *UI) post(fn func()) {
	u.queueMu.Lock()
	if u.closed {
		u.queueMu.Unlock()
		return
	}
	u.queue = append(u.queue, fn)
	target := u.postTarget
	u.queueMu.Unlock()
	if target != 0 {
		postMessage.Call(target, wmDispatch, 0, 0)
	}
}

func (u *UI) Close()        { u.post(func() { postQuitMessage.Call(0) }) }
func (u *UI) ShowLauncher() { u.post(u.showLauncher) }
func (u *UI) ShowSettings() { u.post(u.showSettings) }

func (u *UI) label(chinese, english string) string {
	if strings.HasPrefix(u.locale, "en") {
		return english
	}
	return chinese
}

func (u *UI) SetLanguage(preference, effective string) {
	u.post(func() {
		u.languagePreference, u.locale = preference, effective
		if u.settingsDefinition != nil && u.settingsLocale != u.locale {
			u.settingsLocale = u.locale
			u.settingsDefinition = u.withHostSections(u.settingsSource)
		}
		if u.settings != 0 {
			u.setSettingsFonts(dpiForWindow(u.settings))
		}
		u.refreshSettingsLabels()
		u.refreshSettings()
		for _, aw := range u.appWindows {
			aw.refreshThemeAndLocale(u.locale, u.appearance)
		}
		if u.launcher != 0 {
			u.refreshLauncherLabels()
		}
		for _, widget := range u.widgets {
			u.presentWidget(widget)
		}
		update := u.trayData()
		shellNotify.Call(1, uintptr(unsafe.Pointer(&update)))
	})
}

func (u *UI) SetAppearance(cfg apptheme.Config) {
	u.post(func() {
		u.applyAppearance(cfg)
	})
}

func (u *UI) applyAppearance(cfg apptheme.Config) {
	u.appearance = cfg.Normalize()
	u.refreshSettings()
	for _, aw := range u.appWindows {
		aw.refreshThemeAndLocale(u.locale, u.appearance)
	}
	u.applyLauncherAppearance()
	if u.picker != nil {
		u.syncPickerChrome()
		repaint(u.picker.hwnd)
	}
	if u.popup != nil {
		u.syncPromptChrome()
		repaint(u.popup.hwnd)
	}
	if u.menu != nil {
		u.menu.palette = u.appearance.Resolve(apptheme.ComponentGlobal).Palette
		u.presentContextMenu()
	}
	if u.needsSpatialBackdrop() && u.backdrop.version == 0 {
		u.refreshWallpaperBackdrop(false)
	}
	for _, widget := range u.widgets {
		u.buildWidgetHits(widget)
		u.presentWidget(widget)
	}
}

func (u *UI) observePluginApps() {
	svc := u.cb.PluginApps
	if svc == nil {
		return
	}
	for {
		snap, changed := svc.ObserveApps()
		u.post(func() {
			u.applyAppSnapshot(snap)
		})
		if changed == nil {
			<-u.stopCh
			return
		}
		select {
		case <-u.stopCh:
			return
		case <-changed:
		}
	}
}

func (u *UI) applyAppSnapshot(snap pluginapps.Snapshot) {
	u.appSnapshot = snap.Clone()
	u.refreshSettings()
	for _, aw := range u.appWindows {
		aw.syncSnapshot(u.appSnapshot)
	}
}

func (u *UI) listedCommands() []Command {
	if u.cb.PluginApps != nil {
		out := make([]Command, 0, len(u.appSnapshot.Commands))
		for _, cmd := range u.appSnapshot.Commands {
			out = append(out, Command{
				PluginID:   cmd.PluginID,
				ID:         cmd.ID,
				PluginName: cmd.PluginName,
				Title:      cmd.Title,
				Location:   cmd.Location,
				Generation: cmd.Generation,
			})
		}
		return out
	}
	return u.settingsCommands()
}

func (u *UI) invokeListedCommand(pluginID, commandID string, generation uint64) {
	if u.cb.PluginApps != nil {
		svc := u.cb.PluginApps
		go func() {
			if err := svc.InvokeListedCommand(pluginID, commandID, generation); err != nil && !errors.Is(err, pluginapps.ErrUnavailable) {
				u.Message(u.label("指令执行失败", "Command failed"), err.Error())
			}
		}()
		return
	}
	if u.cb.Command != nil {
		u.cb.Command(pluginID, commandID)
	}
}

func (u *UI) cleanup() {
	u.stopOnce.Do(func() { close(u.stopCh) })
	u.stopUpdateSubscription()
	u.queueMu.Lock()
	u.closed = true
	u.queue = nil
	u.postTarget = 0
	u.queueMu.Unlock()
	if u.menu != nil {
		u.closeContextMenu("")
	}
	u.endItemDrag()
	if u.popup != nil {
		u.closePrompt(false)
	}
	if u.picker != nil {
		u.closePicker(false)
	}
	for _, d := range u.dialogQueue {
		if d.cancel != nil {
			d.cancel()
		}
	}
	u.dialogQueue = nil
	for _, aw := range u.appWindows {
		aw.close()
	}
	for _, widget := range u.widgets {
		u.destroyWidget(widget)
	}
	u.removeTray()
	if u.hotkeyID != 0 {
		unregisterHotKey.Call(u.host, u.hotkeyID)
	}
	u.launcherChrome.release()
	if u.launcher != 0 {
		destroyWindow.Call(u.launcher)
	}
	u.launcherFrame.release()
	if u.settings != 0 {
		destroyWindow.Call(u.settings)
	}
	if u.gioSettings != nil {
		u.gioSettings.close()
	}
	if u.desktopMarker != 0 {
		destroyWindow.Call(u.desktopMarker)
	}
	if u.launcherFont != 0 {
		deleteObject.Call(u.launcherFont)
	}
	if u.launcherBrush != 0 {
		deleteObject.Call(u.launcherBrush)
	}
	u.settingsStyle.release()
	u.settingsAA.close()
	if u.host != 0 {
		destroyWindow.Call(u.host)
	}
	if u.cb.Exit != nil {
		u.cb.Exit()
	}
}

type notifyIconData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	Guid                [16]byte
	BalloonIcon         uintptr
}

func (u *UI) trayData() notifyIconData {
	icon, _, _ := loadIcon.Call(0, 32512)
	n := notifyIconData{
		Size:   uint32(unsafe.Sizeof(notifyIconData{})),
		Window: u.host, ID: 1, Flags: 7, Callback: wmTray, Icon: icon,
	}
	copy(n.Tip[:], syscall.StringToUTF16(u.label("鹌鹑工具箱", "QuailCrate")))
	return n
}

func (u *UI) addTray() {
	n := u.trayData()
	if result, _, _ := shellNotify.Call(0, uintptr(unsafe.Pointer(&n))); result == 0 {
		log.Printf("tray icon unavailable; Explorer will retry via TaskbarCreated")
	}
}

func (u *UI) removeTray() {
	n := u.trayData()
	shellNotify.Call(2, uintptr(unsafe.Pointer(&n)))
}

func (u *UI) RegisterHotkey(value string, done func(error)) {
	u.post(func() {
		mods, key, err := parseHotkey(value)
		if err == nil && (u.hotkeyID == 0 || mods != u.hotkeyMods || key != u.hotkeyVK) {
			id := uintptr(0x510)
			if u.hotkeyID == id {
				id++
			}
			result, _, systemErr := registerHotKey.Call(u.host, id, mods|0x4000, key)
			if result == 0 {
				if systemErr == syscall.Errno(1409) {
					err = errors.New("Shortcut is already in use")
				} else {
					err = fmt.Errorf("Shortcut registration failed (%s)", win32Error(systemErr))
				}
			} else {
				if u.hotkeyID != 0 {
					unregisterHotKey.Call(u.host, u.hotkeyID)
				}
				u.hotkeyID, u.hotkeyMods, u.hotkeyVK = id, mods, key
			}
		}
		if err == nil {
			u.hotkey = value
			u.hotkeyStatus = ""
		} else {
			u.hotkeyStatus = err.Error()
			if u.hotkeyID != 0 {
				u.hotkeyStatus += ". Still using " + u.hotkey
			}
		}
		u.refreshSettings()
		if done != nil {
			done(err)
		}
	})
}

func parseHotkey(input string) (uintptr, uintptr, error) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(input)), "+")
	var mods uintptr
	for _, part := range parts[:len(parts)-1] {
		switch strings.TrimSpace(part) {
		case "ALT":
			mods |= 1
		case "CTRL", "CONTROL":
			mods |= 2
		case "SHIFT":
			mods |= 4
		case "WIN", "WINDOWS":
			mods |= 8
		default:
			return 0, 0, errors.New("Use Alt, Ctrl, Shift or Win as a modifier")
		}
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	switch key {
	case "SPACE", "ENTER", "ESC", "ESCAPE", "TAB":
		if mods == 0 {
			return 0, 0, errors.New("Space, Enter, Escape and Tab shortcuts require a modifier")
		}
		keys := map[string]uintptr{"SPACE": vkSpace, "ENTER": vkReturn, "ESC": vkEscape, "ESCAPE": vkEscape, "TAB": vkTab}
		return mods, keys[key], nil
	}
	if len(key) == 1 && ((key[0] >= 'A' && key[0] <= 'Z') || (key[0] >= '0' && key[0] <= '9')) {
		if mods == 0 {
			return 0, 0, errors.New("Letter and number shortcuts require a modifier")
		}
		return mods, uintptr(key[0]), nil
	}
	if strings.HasPrefix(key, "F") {
		n, err := strconv.Atoi(key[1:])
		if err == nil && n >= 1 && n <= 24 {
			return mods, uintptr(0x6f + n), nil
		}
	}
	return 0, 0, errors.New("Enter a valid shortcut, for example Ctrl+Alt+Space")
}

func (u *UI) shortcutStatus() string {
	if u.hotkeyStatus != "" {
		return u.hotkeyStatus
	}
	if u.hotkeyID == 0 {
		return u.label("尚未注册", "Shortcut is not registered")
	}
	return u.label("当前快捷键", "Current shortcut") + "  " + u.hotkey
}

func (u *UI) dialogOwner() uintptr {
	focused, _, _ := getForegroundWindow.Call()
	if u.widgetByWindow[focused] != nil || u.isAppWindow(focused) {
		return focused
	}
	if u.gioSettings != nil {
		if h := u.gioSettings.hwnd.Load(); h != 0 {
			return h
		}
	}
	if u.settings != 0 {
		if visible, _, _ := isWindowVisible.Call(u.settings); visible != 0 {
			return u.settings
		}
	}
	return u.host
}

func (u *UI) emitSearch() {
	if u.cb.Search != nil {
		u.launcherRevision = u.cb.Search(u.launcherQuery)
	}
}

func (u *UI) notifyLauncherFocused(start time.Time) {
	if focus, _, _ := getFocus.Call(); focus == u.edit {
		log.Printf("launcher focused elapsed_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
	}
}
