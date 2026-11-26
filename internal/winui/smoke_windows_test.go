//go:build interactive && windows

package winui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"quailcrate/internal/gioui"
	"quailcrate/internal/pluginapps"
	v1 "quailcrate/pluginapi/v1"
)

func TestDesktopSurfaceInteractive(t *testing.T) {
	var ui *UI
	completed := make(chan error, 1)
	events := make(chan v1.Event, 1)
	ui = New(Callbacks{
		Ready: func() {
			go func() {
				completed <- exerciseDesktop(t, ui, events)
				ui.Close()
			}()
		},
		WidgetEvent: func(_ string, event v1.Event) { events <- event },
	})
	if err := ui.Run(); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func exerciseDesktop(t *testing.T, ui *UI, events <-chan v1.Event) error {
	w := Widget{
		PluginID: "sample.interactive", ID: "only",
		Layout: Layout{Width: 270, Height: 160},
		View:   v1.View{Title: "QuailCrate desktop smoke", Controls: []v1.Control{{ID: "ok", Type: "button", Text: "OK"}, {ID: "drop", Type: "list", Text: "Drop something", Drop: true}}},
	}
	ui.SetWidgets([]Widget{w})
	ui.SetLanguage("en-US", "en-US")
	ui.SetPlugins([]Plugin{{ID: "example.settings", Name: "Example", Description: "Example description", Runtime: "lua", State: "failed", Status: "Load failed", Detail: "Sample error", CanToggle: true}})
	ui.SetCommands([]Command{{PluginID: "example.settings", ID: "edit", Title: "Example desktop command", Location: "desktop-edit"}})
	if err := exerciseGioSettings(ui); err != nil {
		return err
	}
	// The legacy dialog is still available on systems without a Gio graphics backend.
	ui.post(func() {
		ui.settingsStyle.page = settingsGeneralPage
		ui.showNativeSettings()
	})
	shell := testLauncherShell()
	ui.SetLaunchers([]LauncherShell{shell}, shell)
	ui.ShowLauncher()
	err := onUI(ui, func() error {
		widget := ui.widgets[widgetKey(w.PluginID, w.ID)]
		if widget == nil || widget.drop == nil || len(widget.hits) != 1 || len(widget.dropZones) != 1 {
			return fmt.Errorf("widget HWND, button hit area or OLE drop zone unavailable")
		}
		if ui.launcher == 0 || ui.edit == 0 || ui.settings == 0 || currentText(ui.settings) != "QuailCrate · Settings" {
			return fmt.Errorf("launcher, settings or language labels unavailable")
		}
		ui.closeLauncher(false)
		if ui.settingsStyle.page != settingsGeneralPage || currentText(ui.settingsFields[settingsNavGeneral]) != "General" {
			return fmt.Errorf("default page or localized navigation unavailable")
		}
		sendMessage.Call(ui.settingsFields[settingsNavDesktop], 0x00f5, 0, 0) // BM_CLICK
		if ui.settingsStyle.page != settingsDesktopPage {
			return fmt.Errorf("Desktop page unavailable")
		}
		sendMessage.Call(ui.settingsFields[settingsNavCommands], 0x00f5, 0, 0) // BM_CLICK
		if ui.settingsStyle.page != settingsCommandsPage || listSelection(ui.settingsFields[settingsCommandList]) != 0 {
			return fmt.Errorf("Commands page or plugin command unavailable")
		}
		postMessage.Call(ui.settingsFields[settingsNavPlugins], wmKeyDown, vkReturn, 0)
		return nil
	})
	if err != nil {
		return err
	}
	err = waitForUI(ui, "keyboard navigation to the localized Plugins page", func() bool {
		return ui.settingsStyle.page == settingsPluginsPage && currentText(ui.settingsFields[settingsNavPlugins]) == "Plugins"
	})
	if err != nil {
		return err
	}
	err = onUI(ui, func() error {
		if visible, _, _ := isWindowVisible.Call(ui.settingsFields[settingsCopyDiagnostics]); visible == 0 {
			return fmt.Errorf("selected plugin diagnostics unavailable")
		}
		return nil
	})
	if err != nil {
		return err
	}
	ui.SetLanguage("zh-CN", "zh-CN")
	err = onUI(ui, func() error {
		if ui.settingsStyle.fontLocale != "zh-CN" || currentText(ui.settingsFields[settingsNavPlugins]) != "插件" {
			return fmt.Errorf("settings did not update its typeface and labels after changing language")
		}
		if err := checkSettingsFrame(ui); err != nil {
			return err
		}
		widget := ui.widgets[widgetKey(w.PluginID, w.ID)]
		button := widget.hits[0].area
		buttonPoint := uintptr(uint16(button.Left+2)) | uintptr(uint16(button.Top+2))<<16
		sendMessage.Call(widget.hwnd, wmLButtonDown, 1, buttonPoint)
		sendMessage.Call(widget.hwnd, wmLButtonUp, 0, buttonPoint)
		return nil
	})
	if err != nil {
		return err
	}
	select {
	case event := <-events:
		if event.Type != "widget.action" || event.WidgetID != w.ID || event.ControlID != "ok" {
			return fmt.Errorf("unexpected widget action %+v", event)
		}
	case <-time.After(time.Second):
		return fmt.Errorf("widget button did not emit an action")
	}

	if err := onUI(ui, func() error {
		widget := ui.widgets[widgetKey(w.PluginID, w.ID)]
		uLayout := widget.model.Layout
		original := windowRect(widget.hwnd)
		var saved Layout
		ui.cb.MoveWidget = func(pluginID, widgetID string, _ uint64, l Layout) {
			if pluginID == w.PluginID && widgetID == w.ID {
				saved = l
			}
		}
		ui.setEditing(true)
		defer ui.setEditing(false)
		const titlePoint = 8 | 8<<16
		sendMessage.Call(widget.hwnd, wmLButtonDown, 1, titlePoint)
		if !widget.dragging {
			return fmt.Errorf("the Gio widget title did not capture a layout drag")
		}
		// Simulate cursor travel without moving the user's pointer.
		dx := int32(45)
		if original.Right+dx > monitorOf(widget.hwnd).Work.Right {
			dx = -dx
		}
		grab := widget.mouseStart
		widget.mouseStart = cursorPosition()
		widget.mouseStart.X -= dx
		sendMessage.Call(widget.hwnd, wmMouseMove, 1, titlePoint)
		widget.mouseStart = grab
		sendMessage.Call(widget.hwnd, wmLButtonUp, 0, titlePoint)
		if !saved.Placed || saved.Monitor == "" || saved == uLayout || saved != ui.widgetModels[0].Layout || windowRect(widget.hwnd) == original {
			return fmt.Errorf("widget did not commit its dragged position on release: %+v", saved)
		}
		return nil
	}); err != nil {
		return err
	}

	var ordinary uintptr
	err = onUI(ui, func() error {
		widget := ui.widgets[widgetKey(w.PluginID, w.ID)]
		r := windowRect(widget.hwnd)
		var err error
		ordinary, err = window(hostClass, "QuailCrate smoke foreground window", 0, wsCaption|wsSysMenu|0x00020000, r, 0, 0, ui.module)
		if err != nil {
			return err
		}
		showWindow.Call(ordinary, swShow)
		setForegroundWindow.Call(ordinary)
		setWindowPos.Call(ordinary, 0, 0, 0, 0, 0, zFlags)
		return nil
	})
	if err != nil {
		return err
	}
	defer onUI(ui, func() error { destroyWindow.Call(ordinary); return nil })
	var hasShell bool
	if err = onUI(ui, func() error {
		shell, _, _ := getShellWindow.Call()
		hasShell = shell != 0
		if hasShell {
			// The old desktop host can remain valid after Explorer moves the icon view.
			ui.desktopHost = ordinary
			ui.updateDesktop()
			if expected := desktopIconsHost(shell); ui.desktopHost != expected {
				return fmt.Errorf("desktop host remained %#x after its icon view moved, want %#x", ui.desktopHost, expected)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = waitForWindowCover(ui, w, ordinary, true); err != nil {
		return fmt.Errorf("cover with ordinary window: %w", err)
	}
	if hasShell {
		err = onUI(ui, func() error { pressShowDesktop(); return nil })
		if err != nil {
			return err
		}
		shownErr := waitForWindowCover(ui, w, ordinary, false)
		if shownErr == nil {
			shownErr = exerciseDesktopRecovery(ui, w, ordinary)
		}
		restoreErr := onUI(ui, func() error { pressShowDesktop(); return nil })
		if shownErr != nil {
			return fmt.Errorf("show desktop: %w", shownErr)
		}
		if restoreErr != nil {
			return restoreErr
		}
		if err = waitForWindowCover(ui, w, ordinary, true); err != nil {
			return fmt.Errorf("restore ordinary window: %w", err)
		}
	} else {
		t.Log("Win+D was not tested because Explorer is unavailable on this desktop")
	}

	return onUI(ui, func() error {
		old := ui.widgets[widgetKey(w.PluginID, w.ID)]
		if ui.taskbarCreated == 0 {
			return fmt.Errorf("TaskbarCreated event unavailable")
		}
		ui.hostProcedure(ui.taskbarCreated, 0, 0)
		replacement := ui.widgets[widgetKey(w.PluginID, w.ID)]
		if replacement == nil || replacement == old || replacement.drop == nil {
			return fmt.Errorf("widget not rebuilt after TaskbarCreated")
		}
		return nil
	})
}

func exerciseDesktopRecovery(ui *UI, w Widget, ordinary uintptr) error {
	if err := onUI(ui, func() error {
		widget := ui.widgets[widgetKey(w.PluginID, w.ID)]
		// Simulate the shell hiding and lowering a widget after Show Desktop is detected.
		ui.adjustingDesktopZ = true
		ok, _, err := setWindowPos.Call(widget.hwnd, hwndBottom, 0, 0, 0, 0, zFlags)
		ui.adjustingDesktopZ = false
		if ok == 0 {
			return fmt.Errorf("lower widget for recovery check: %w", err)
		}
		showWindow.Call(widget.hwnd, swHide)
		return nil
	}); err != nil {
		return err
	}
	return waitForWindowCover(ui, w, ordinary, false)
}

func waitForUI(ui *UI, name string, ready func() bool) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var found bool
		if err := onUI(ui, func() error { found = ready(); return nil }); err != nil {
			return err
		}
		if found {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", name)
}

func exerciseGioSettings(ui *UI) error {
	ui.ShowSettings()
	var initial uintptr
	if err := waitForUI(ui, "Gio settings window", func() bool {
		if ui.gioSettings != nil {
			initial = ui.gioSettings.hwnd.Load()
		}
		return initial != 0 && currentText(initial) == "QuailCrate · Settings"
	}); err != nil {
		return err
	}
	ui.post(func() { ui.setSettingsPage(settingsPluginsPage) })
	ui.SetLanguage("zh-CN", "zh-CN")
	if err := waitForUI(ui, "Gio navigation and language refresh", func() bool {
		s := ui.gioSettings
		return s != nil && s.hwnd.Load() == initial && currentText(initial) == "鹌鹑工具箱 · 设置" &&
			s.nextPage.Load() == nil && len(s.model.Load().Plugins) == 1
	}); err != nil {
		return err
	}
	postMessage.Call(initial, wmClose, 0, 0)
	if err := waitForUI(ui, "Gio settings to close on the plugins page", func() bool {
		return ui.gioSettings == nil && ui.settingsPageID == gioui.PagePlugins && ui.settingsStyle.page == settingsPluginsPage
	}); err != nil {
		return err
	}
	ui.SetLanguage("en-US", "en-US")
	ui.ShowSettings()
	var reopened uintptr
	if err := waitForUI(ui, "Gio settings to reopen", func() bool {
		if ui.gioSettings != nil {
			reopened = ui.gioSettings.hwnd.Load()
		}
		return reopened != 0 && currentText(reopened) == "QuailCrate · Settings"
	}); err != nil {
		return err
	}
	postMessage.Call(reopened, wmClose, 0, 0)
	return waitForUI(ui, "reopened Gio window to close", func() bool { return ui.gioSettings == nil })
}

func checkSettingsFrame(ui *UI) error {
	bounds := windowRect(ui.settings)
	dpi := dpiForWindow(ui.settings)
	if hit := ui.settingsHitTest(point{bounds.Left + scale(72, dpi), bounds.Top + scale(24, dpi)}); hit != 2 {
		return fmt.Errorf("settings title bar returned hit %d instead of the window caption", hit)
	}
	ui.activateSettingsCaption(settingsCaptionMaximize)
	if !ui.settingsMaximized() || currentText(ui.settingsFields[settingsCaptionMaximize]) != ui.label("还原", "Restore") {
		return fmt.Errorf("settings maximize did not update the native window and its Restore action")
	}
	ui.activateSettingsCaption(settingsCaptionMaximize)
	if ui.settingsMaximized() {
		return fmt.Errorf("settings Restore did not unmaximize the native window")
	}
	ui.activateSettingsCaption(settingsCaptionMinimize)
	if result, _, _ := settingsIsIconic.Call(ui.settings); result == 0 {
		return fmt.Errorf("settings Minimize did not minimize the native window")
	}
	ui.showSettings()
	ui.activateSettingsCaption(settingsCaptionClose)
	if result, _, _ := isWindowVisible.Call(ui.settings); result != 0 {
		return fmt.Errorf("settings Close did not hide the native window")
	}
	return nil
}

func waitForWindowCover(ui *UI, w Widget, ordinary uintptr, covered bool) error {
	deadline := time.Now().Add(4 * time.Second)
	windowFromPoint := user32.NewProc("WindowFromPoint")
	dwmGetWindowAttribute := settingsDwm.NewProc("DwmGetWindowAttribute")
	for {
		err := onUI(ui, func() error {
			widget := ui.widgets[widgetKey(w.PluginID, w.ID)]
			if widget == nil {
				return fmt.Errorf("widget no longer exists")
			}
			r := windowRect(widget.hwnd)
			p := point{X: r.Left + r.width()/2, Y: r.Top + r.height()/2}
			if covered {
				style, _, _ := getWindowLong.Call(ordinary, gwlStyle)
				var cloaked uint32
				const dwmwaCloaked = 14
				dwmGetWindowAttribute.Call(ordinary, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
				if style&wsMinimized == 0 && cloaked == 0 && windowRect(ordinary).contains(p) && visibleWindowAbove(ordinary, widget.hwnd) {
					return nil
				}
				return fmt.Errorf("ordinary window %#x does not cover widget %#x (style=%#x, cloaked=%d, desktop shown=%t)", ordinary, widget.hwnd, style, cloaked, ui.desktopShown)
			}
			hit, _, _ := windowFromPoint.Call(*(*uintptr)(unsafe.Pointer(&p)))
			if hit != widget.hwnd {
				foreground, _, _ := getForegroundWindow.Call()
				return fmt.Errorf("point hits %#x, want widget %#x (foreground=%#x, widget topmost=%t, desktop=%#x, desktop shown=%t)", hit, widget.hwnd, foreground, isTopmost(widget.hwnd), ui.desktopHost, ui.desktopShown)
			}
			return nil
		})
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func pressShowDesktop() {
	press := user32.NewProc("keybd_event")
	const win = 0x5b
	const d = 0x44
	const up = 2
	press.Call(win, 0, 0, 0)
	press.Call(d, 0, 0, 0)
	press.Call(d, 0, up, 0)
	press.Call(win, 0, up, 0)
}

func onUI(ui *UI, check func() error) error {
	result := make(chan error, 1)
	ui.post(func() { result <- check() })
	select {
	case err := <-result:
		return err
	case <-time.After(6 * time.Second):
		return fmt.Errorf("Win32 message loop did not respond")
	}
}

type fakePluginAppsService struct {
	mu          sync.Mutex
	snap        pluginapps.Snapshot
	changed     chan struct{}
	seq         uint64
	sessions    map[string]*fakeTabSession
	failNext    bool
	count       int
	invokedCmd  string
	dialogDone  chan pluginapps.DialogResult
	dialogCalls atomic.Int32
}

type fakeTabSession struct {
	session pluginapps.Session
	ctx     context.Context
	cancel  context.CancelFunc
	dialogs pluginapps.DialogHandler
	rev     uint64
}

func newFakePluginAppsService() *fakePluginAppsService {
	return &fakePluginAppsService{
		changed:    make(chan struct{}),
		sessions:   make(map[string]*fakeTabSession),
		dialogDone: make(chan pluginapps.DialogResult, 4),
		snap: pluginapps.Snapshot{
			Tabs: []pluginapps.Tab{
				{TabRef: pluginapps.TabRef{PluginID: "alpha", TabID: "overview", Generation: 1}, PluginName: "Alpha", Title: "Overview"},
				{TabRef: pluginapps.TabRef{PluginID: "alpha", TabID: "counter", Generation: 1}, PluginName: "Alpha", Title: "Counter"},
				{TabRef: pluginapps.TabRef{PluginID: "beta", TabID: "home", Generation: 1}, PluginName: "Beta", Title: "Home"},
			},
			Commands: []pluginapps.Command{
				{PluginID: "alpha", ID: "ping", PluginName: "Alpha", Title: "Ping Alpha", Location: "settings", Generation: 1},
			},
		},
	}
}

func (f *fakePluginAppsService) publish(snap pluginapps.Snapshot) {
	f.mu.Lock()
	f.snap = snap
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakePluginAppsService) ObserveApps() (pluginapps.Snapshot, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap.Clone(), f.changed
}

func (f *fakePluginAppsService) InvokeListedCommand(pluginID, commandID string, generation uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invokedCmd = fmt.Sprintf("%s:%s:%d", pluginID, commandID, generation)
	return nil
}

func (f *fakePluginAppsService) OpenTab(ref pluginapps.TabRef, dialogs pluginapps.DialogHandler) (pluginapps.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tab, ok := f.snap.ResolveTab(ref.PluginID, ref.TabID)
	if !ok || tab.TabRef != ref {
		return pluginapps.Session{}, pluginapps.ErrUnavailable
	}
	if prev := f.sessions[ref.PluginID]; prev != nil {
		if prev.session.TabRef == ref {
			return prev.session, nil
		}
		prev.cancel()
	}
	f.seq++
	ctx, cancel := context.WithCancel(context.Background())
	sess := pluginapps.Session{TabRef: ref, ID: f.seq}
	f.sessions[ref.PluginID] = &fakeTabSession{session: sess, ctx: ctx, cancel: cancel, dialogs: dialogs}
	return sess, nil
}

func (f *fakePluginAppsService) LoadTab(session pluginapps.Session) (pluginapps.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.sessions[session.PluginID]
	if cur == nil || cur.session != session {
		return pluginapps.Page{}, pluginapps.ErrUnavailable
	}
	if f.failNext {
		f.failNext = false
		return pluginapps.Page{}, errors.New("temporary load failure")
	}
	cur.rev++
	return pluginapps.Page{
		PageRef: pluginapps.PageRef{Session: session, Revision: cur.rev},
		View: v1.View{
			Title: fmt.Sprintf("%s/%s#%d", session.PluginID, session.TabID, f.count),
			Controls: []v1.Control{
				{ID: "inc", Type: "button", Text: "+1"},
				{ID: "ask", Type: "button", Text: "Prompt"},
			},
		},
	}, nil
}

func (f *fakePluginAppsService) ActOnTab(page pluginapps.PageRef, controlID, _ string) error {
	f.mu.Lock()
	cur := f.sessions[page.PluginID]
	if cur == nil || cur.session != page.Session || cur.rev != page.Revision {
		f.mu.Unlock()
		return pluginapps.ErrUnavailable
	}
	if controlID == "inc" {
		f.count++
		f.mu.Unlock()
		return nil
	}
	if controlID == "ask" && cur.dialogs != nil {
		ctx, dialogs := cur.ctx, cur.dialogs
		f.mu.Unlock()
		dialogs(ctx, v1.Action{Type: "ui.promptText", Title: "Enter note"}, func(res pluginapps.DialogResult) {
			f.dialogCalls.Add(1)
			f.dialogDone <- res
		})
		return nil
	}
	f.mu.Unlock()
	return nil
}

func (f *fakePluginAppsService) CloseTab(session pluginapps.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur := f.sessions[session.PluginID]; cur != nil && cur.session == session {
		cur.cancel()
		delete(f.sessions, session.PluginID)
	}
}

const testPackagedAppURL = "https://packaged-app-test.quailcrate.invalid/index.html"

type fakePackagedAppService struct {
	*fakePluginAppsService
	ctx          context.Context
	load         <-chan struct{}
	serve        <-chan struct{}
	requested    chan<- error
	inspect      func() error
	firstPageURL string
}

func (f *fakePackagedAppService) LoadTab(session pluginapps.Session) (pluginapps.Page, error) {
	select {
	case <-f.ctx.Done():
		return pluginapps.Page{}, f.ctx.Err()
	case <-f.load:
	}
	page, err := f.fakePluginAppsService.LoadTab(session)
	if err != nil {
		return pluginapps.Page{}, err
	}
	url := testPackagedAppURL
	if page.Revision == 1 && f.firstPageURL != "" {
		url = f.firstPageURL
	}
	return pluginapps.Page{PageRef: page.PageRef, Document: &pluginapps.Document{URL: url}}, nil
}

func (f *fakePackagedAppService) ReadPageAsset(_ pluginapps.Session, uri string) (pluginapps.PageAsset, error) {
	if uri != testPackagedAppURL {
		return pluginapps.PageAsset{}, fmt.Errorf("unexpected packaged asset %q", uri)
	}
	select {
	case f.requested <- f.inspect():
	default:
	}
	select {
	case <-f.ctx.Done():
		return pluginapps.PageAsset{}, f.ctx.Err()
	case <-f.serve:
	}
	return pluginapps.PageAsset{ContentType: "text/html; charset=utf-8", Body: []byte("<!doctype html><title>Packaged App</title><p>Ready</p>")}, nil
}

func (f *fakePackagedAppService) SendPageMessage(pluginapps.PageRef, string, []byte) (pluginapps.PageReply, error) {
	return pluginapps.PageReply{}, errors.New("unexpected packaged page message")
}

func (f *fakePackagedAppService) SendPageUserMessage(pluginapps.PageRef, string, []byte) (pluginapps.PageReply, error) {
	return pluginapps.PageReply{}, errors.New("unexpected packaged page user message")
}

func (f *fakePackagedAppService) SubmitTabValue(pluginapps.PageRef, string, json.RawMessage) error {
	return errors.New("unexpected packaged page value submission")
}

func TestPackagedAppWindowShowsAfterNavigation(t *testing.T) {
	exercisePackagedAppWindow(t, "")
}

func TestPackagedAppWindowShowsNavigationErrorAndRetries(t *testing.T) {
	exercisePackagedAppWindow(t, "https://[")
}

func exercisePackagedAppWindow(t *testing.T, firstPageURL string) {
	t.Helper()
	if _, _, err := resolveWebView2Proc(); err != nil {
		t.Skipf("Edge WebView2 Runtime not installed: %v", err)
	}
	load, serve := make(chan struct{}), make(chan struct{})
	requested := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc := &fakePackagedAppService{
		fakePluginAppsService: newFakePluginAppsService(), ctx: ctx,
		load: load, serve: serve, requested: requested, firstPageURL: firstPageURL,
	}
	ref := pluginapps.TabRef{PluginID: "packaged-app-test", TabID: "home", Generation: 1}
	svc.snap = pluginapps.Snapshot{Tabs: []pluginapps.Tab{{TabRef: ref, PluginName: "Packaged App", Title: "Home", Page: "web/index.html"}}}
	dwmGetWindowAttribute := settingsDwm.NewProc("DwmGetWindowAttribute")
	onScreen := func(hwnd uintptr) bool {
		visible, _, _ := isWindowVisible.Call(hwnd)
		if visible == 0 {
			return false
		}
		const dwmwaCloaked = 14
		var cloaked uint32
		dwmGetWindowAttribute.Call(hwnd, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
		return cloaked == 0
	}
	var ui *UI
	exercise := func() error {
		if err := waitForUI(ui, "packaged app catalog", func() bool { return len(ui.appSnapshot.Tabs) == 1 }); err != nil {
			return err
		}
		ui.post(func() { ui.openPluginAppTab(ref) })
		var w *pluginAppWindow
		if err := waitForUI(ui, "packaged app HWND", func() bool {
			w = ui.appWindows[ref.PluginID]
			return w != nil && w.hwnd.Load() != 0
		}); err != nil {
			return err
		}
		hwnd := w.hwnd.Load()
		if visible, _, _ := isWindowVisible.Call(hwnd); visible != 0 {
			return errors.New("packaged app window was visible before loading its page")
		}
		svc.inspect = func() error { return checkPackagedAppWebViewBackground(w) }
		close(load)
		if firstPageURL != "" {
			if err := waitForUI(ui, "visible packaged app navigation error", func() bool {
				m := w.model.Load()
				return onScreen(hwnd) && m.State == gioui.AppStateError && m.Error != "" && !m.WebViewActive
			}); err != nil {
				return fmt.Errorf("%w (model: %+v)", err, w.model.Load())
			}
			w.enqueue(appWindowJob{kind: appJobReload})
		}
		select {
		case err := <-requested:
			if err != nil {
				return err
			}
		case <-time.After(10 * time.Second):
			return fmt.Errorf("WebView did not request the packaged HTML (model: %+v)", w.model.Load())
		}
		if visible, _, _ := isWindowVisible.Call(hwnd); firstPageURL == "" && visible != 0 {
			return errors.New("packaged app window was visible before its HTML response")
		}
		const frameStyles = wsCaption | wsSysMenu | wsThickFrame | wsMinimizeBox | wsMaximizeBox
		if style, _, _ := getWindowLong.Call(hwnd, gwlStyle); style&frameStyles != frameStyles {
			return fmt.Errorf("packaged app frame before HTML response has style %#x, want %#x flags", style, frameStyles)
		}
		close(serve)
		if err := waitForUI(ui, "visible packaged app after navigation", func() bool {
			return onScreen(hwnd) && w.model.Load().WebViewActive
		}); err != nil {
			return fmt.Errorf("%w (model: %+v)", err, w.model.Load())
		}
		ui.post(w.close)
		return waitForUI(ui, "closed packaged app", func() bool { return ui.appWindows[ref.PluginID] == nil })
	}
	completed := make(chan error, 1)
	ui = New(Callbacks{
		PluginApps: svc,
		Ready: func() {
			go func() {
				completed <- exercise()
				cancel()
				ui.Close()
			}()
		},
	})
	cacheDir := t.TempDir()
	t.Cleanup(func() {
		// WebView2 can keep the profile open briefly after closing the window.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := os.RemoveAll(cacheDir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	ui.SetIconCacheDir(cacheDir)
	if err := ui.Run(); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

// Read the controller's color on its STA thread before the HTML response is served.
func checkPackagedAppWebViewBackground(w *pluginAppWindow) error {
	w.mu.Lock()
	wv := w.webView
	w.mu.Unlock()
	if wv == nil || wv.controller == 0 {
		return errors.New("packaged app has no WebView2 controller")
	}
	controller2 := wvQueryInterface(wv.controller, &wvIIDController2)
	if controller2 == 0 {
		return errors.New("could not query ICoreWebView2Controller2")
	}
	defer wvComRelease(&controller2)
	var argb struct{ A, R, G, B uint8 }
	if hr := wvComCall(controller2, 26, uintptr(unsafe.Pointer(&argb))); int32(hr) < 0 {
		return fmt.Errorf("read WebView2 default background: HRESULT 0x%08X", uint32(hr))
	}
	got := color.NRGBA{R: argb.R, G: argb.G, B: argb.B, A: argb.A}
	if want := w.model.Load().Palette.Background; got != want {
		return fmt.Errorf("WebView2 background before HTML response = %v, want %v", got, want)
	}
	return nil
}

func TestPluginAppWindowsInteractive(t *testing.T) {
	svc := newFakePluginAppsService()
	var savedBounds atomic.Pointer[map[string]AppWindowBounds]
	var ui *UI
	completed := make(chan error, 1)
	ui = New(Callbacks{
		PluginApps: svc,
		SaveAppBounds: func(b map[string]AppWindowBounds) {
			savedBounds.Store(&b)
		},
		Ready: func() {
			go func() {
				completed <- exercisePluginAppWindows(ui, svc, &savedBounds)
				ui.Close()
			}()
		},
	})
	if err := ui.Run(); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func exercisePluginAppWindows(ui *UI, svc *fakePluginAppsService, savedBounds *atomic.Pointer[map[string]AppWindowBounds]) error {
	// Wait for ObserveApps initial snapshot.
	if err := waitForUI(ui, "initial app snapshot", func() bool {
		return len(ui.appSnapshot.Tabs) == 3 && len(ui.listedCommands()) == 1
	}); err != nil {
		return err
	}

	// Open alpha/overview and beta/home side-by-side.
	ui.post(func() {
		ui.openPluginAppTab(pluginapps.TabRef{PluginID: "alpha", TabID: "overview", Generation: 1})
		ui.openPluginAppTab(pluginapps.TabRef{PluginID: "beta", TabID: "home", Generation: 1})
	})
	var alphaHWND, betaHWND uintptr
	if err := waitForUI(ui, "side-by-side plugin app windows ready", func() bool {
		a, b := ui.appWindows["alpha"], ui.appWindows["beta"]
		if a == nil || b == nil {
			return false
		}
		alphaHWND, betaHWND = a.hwnd.Load(), b.hwnd.Load()
		ma, mb := a.model.Load(), b.model.Load()
		return alphaHWND != 0 && betaHWND != 0 && alphaHWND != betaHWND &&
			ma != nil && ma.State == gioui.AppStateReady && ma.Page != nil &&
			mb != nil && mb.State == gioui.AppStateReady && mb.Page != nil
	}); err != nil {
		return err
	}

	// Re-opening alpha with another tab reuses the same HWND and switches tab.
	ui.post(func() {
		ui.openPluginAppTab(pluginapps.TabRef{PluginID: "alpha", TabID: "counter", Generation: 1})
	})
	if err := waitForUI(ui, "alpha switch to counter tab", func() bool {
		a := ui.appWindows["alpha"]
		if a == nil || a.hwnd.Load() != alphaHWND {
			return false
		}
		m := a.model.Load()
		return m != nil && m.ActiveTabID == "counter" && m.State == gioui.AppStateReady && m.Page != nil && m.Page.TabID == "counter"
	}); err != nil {
		return err
	}

	// Trigger click action off UI thread and verify refreshed content.
	var pageRef pluginapps.PageRef
	if err := onUI(ui, func() error {
		pageRef = ui.appWindows["alpha"].model.Load().Page.PageRef
		ui.appWindows["alpha"].enqueue(appWindowJob{kind: appJobAct, page: pageRef, controlID: "inc"})
		return nil
	}); err != nil {
		return err
	}
	if err := waitForUI(ui, "alpha counter increment", func() bool {
		m := ui.appWindows["alpha"].model.Load()
		return m != nil && m.Page != nil && m.Page.View.Title == "alpha/counter#1"
	}); err != nil {
		return err
	}

	// Trigger prompt dialog from alpha window, verify owner == alphaHWND, then cancel via tab switch!
	if err := onUI(ui, func() error {
		pageRef = ui.appWindows["alpha"].model.Load().Page.PageRef
		ui.appWindows["alpha"].enqueue(appWindowJob{kind: appJobAct, page: pageRef, controlID: "ask"})
		return nil
	}); err != nil {
		return err
	}
	if err := waitForUI(ui, "prompt owned by alpha window", func() bool {
		return ui.popup != nil && ui.popup.owner == alphaHWND
	}); err != nil {
		return err
	}
	// Switching alpha tab cancels the previous session ctx and must dismiss the open prompt!
	ui.post(func() {
		ui.openPluginAppTab(pluginapps.TabRef{PluginID: "alpha", TabID: "overview", Generation: 1})
	})
	select {
	case res := <-svc.dialogDone:
		if !res.Canceled {
			return fmt.Errorf("expected canceled dialog result on tab switch, got %+v", res)
		}
	case <-time.After(4 * time.Second):
		return fmt.Errorf("dialog did not cancel on tab switch")
	}
	if err := waitForUI(ui, "prompt dismissed after cancel", func() bool {
		return ui.popup == nil
	}); err != nil {
		return err
	}

	// Verify InvokeListedCommand routing via UI.
	ui.post(func() {
		ui.invokeListedCommand("alpha", "ping", 1)
	})
	if err := waitForUI(ui, "listed command invoked", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.invokedCmd == "alpha:ping:1"
	}); err != nil {
		return err
	}

	// Simulate LoadTab error and verify AppStateError -> AppRetry recovery.
	svc.mu.Lock()
	svc.failNext = true
	svc.mu.Unlock()
	if err := onUI(ui, func() error {
		ui.appWindows["alpha"].enqueue(appWindowJob{kind: appJobReload})
		return nil
	}); err != nil {
		return err
	}
	if err := waitForUI(ui, "alpha enters error state", func() bool {
		m := ui.appWindows["alpha"].model.Load()
		return m != nil && m.State == gioui.AppStateError && m.Error != ""
	}); err != nil {
		return err
	}
	if err := onUI(ui, func() error {
		ui.appWindows["alpha"].enqueue(appWindowJob{kind: appJobReload})
		return nil
	}); err != nil {
		return err
	}
	if err := waitForUI(ui, "alpha recovers after retry", func() bool {
		m := ui.appWindows["alpha"].model.Load()
		return m != nil && m.State == gioui.AppStateReady && m.Page != nil
	}); err != nil {
		return err
	}

	// Simulate language switch (generation 2 with localized title) and verify rebind by stable ID.
	svc.publish(pluginapps.Snapshot{
		Tabs: []pluginapps.Tab{
			{TabRef: pluginapps.TabRef{PluginID: "alpha", TabID: "overview", Generation: 2}, PluginName: "阿尔法", Title: "概览"},
			{TabRef: pluginapps.TabRef{PluginID: "alpha", TabID: "counter", Generation: 2}, PluginName: "阿尔法", Title: "计数器"},
		},
	})
	if err := waitForUI(ui, "alpha rebound to gen 2 and beta auto-closed", func() bool {
		a := ui.appWindows["alpha"]
		if a == nil || ui.appWindows["beta"] != nil {
			return false
		}
		m := a.model.Load()
		return m != nil && m.PluginName == "阿尔法" && m.State == gioui.AppStateReady &&
			m.Page != nil && m.Page.Generation == 2 && m.Page.TabID == "overview"
	}); err != nil {
		return err
	}

	// Removing alpha closes its window and persists bounds.
	svc.publish(pluginapps.Snapshot{})
	if err := waitForUI(ui, "alpha closed when removed from catalog", func() bool {
		return len(ui.appWindows) == 0 && savedBounds.Load() != nil
	}); err != nil {
		return err
	}
	if calls := svc.dialogCalls.Load(); calls != 1 {
		return fmt.Errorf("expected dialog completion called once, got %d", calls)
	}
	return nil
}
