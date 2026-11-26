package winui

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"
	"quailcrate/internal/gioui"
	"quailcrate/internal/pluginapps"
	apptheme "quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

// AppWindowBounds remembers an independent plugin window's placement across reopen.
type AppWindowBounds struct {
	Monitor string `json:"monitor,omitempty"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Placed  bool   `json:"placed"`
}

type appJobKind int

const (
	appJobOpenTab appJobKind = iota
	appJobReload
	appJobAct
	appJobSubmitValue
	appJobPageMessage
	appJobSyncSnapshot
)

// Gio's Windows driver creates a hidden HWND and uses SW_HIDE for an
// unrecognized mode. Keep it hidden until the initial HTML page is ready.
const appWindowHiddenMode app.WindowMode = 0xff

var appDwmFlush = settingsDwm.NewProc("DwmFlush")

type appWindowJob struct {
	kind       appJobKind
	tab        pluginapps.TabRef
	page       pluginapps.PageRef
	controlID  string
	itemID     string
	inputValue json.RawMessage
	sourceURL  string
	rawMessage []byte
	snapshot   pluginapps.Snapshot
}

type pluginAppWindow struct {
	ui       *UI
	pluginID string
	window   app.Window
	hwnd     atomic.Uintptr
	closing  atomic.Bool
	model    atomic.Pointer[gioui.AppWindowModel]

	mu           sync.Mutex
	locale       string
	appearance   apptheme.Config
	pluginName   string
	tabs         []pluginapps.Tab
	activeTabID  string
	generation   uint64
	session      pluginapps.Session
	state        gioui.AppWindowState
	errText      string
	page         *pluginapps.Page
	lastBounds   AppWindowBounds
	webView      *webViewHost
	webViewDoc   string
	webViewReady bool

	jobs       chan appWindowJob
	workerStop chan struct{}
	stopOnce   sync.Once
	workerDone chan struct{}
}

func pluginTabsFromSnapshot(snap pluginapps.Snapshot, pluginID string) (string, []pluginapps.Tab) {
	var tabs []pluginapps.Tab
	var name string
	for _, t := range snap.Tabs {
		if t.PluginID == pluginID {
			tabs = append(tabs, t)
			if name == "" {
				name = t.PluginName
			}
		}
	}
	if name == "" {
		name = pluginID
	}
	return name, tabs
}

func (u *UI) openPluginAppTab(ref pluginapps.TabRef) {
	if u.cb.PluginApps == nil || ref.PluginID == "" {
		return
	}
	pluginName, tabs := pluginTabsFromSnapshot(u.appSnapshot, ref.PluginID)
	if len(tabs) == 0 {
		return
	}
	// Resolve target tab in snapshot if TabID was empty or generation was stale.
	target, ok := u.appSnapshot.ResolveTab(ref.PluginID, ref.TabID)
	if !ok {
		target = tabs[0]
	}

	if existing := u.appWindows[ref.PluginID]; existing != nil {
		if h := existing.hwnd.Load(); h != 0 {
			if visible, _, _ := isWindowVisible.Call(h); visible != 0 {
				if iconic, _, _ := settingsIsIconic.Call(h); iconic != 0 {
					settingsShowAsync.Call(h, 9) // SW_RESTORE
				}
				setForegroundWindow.Call(h)
			}
		}
		existing.enqueue(appWindowJob{kind: appJobOpenTab, tab: target.TabRef})
		return
	}

	w := &pluginAppWindow{
		ui:          u,
		pluginID:    ref.PluginID,
		locale:      u.locale,
		appearance:  u.appearance.Normalize(),
		pluginName:  pluginName,
		tabs:        slices.Clone(tabs),
		activeTabID: target.TabID,
		generation:  target.Generation,
		state:       gioui.AppStateLoading,
		jobs:        make(chan appWindowJob, 64),
		workerStop:  make(chan struct{}),
		workerDone:  make(chan struct{}),
	}
	u.appWindows[ref.PluginID] = w
	w.publishModel()

	bounds, dipW, dipH := u.initialAppWindowBounds(ref.PluginID, len(u.appWindows)-1)
	w.window.Option(
		app.Title(w.windowTitle()),
		app.Decorated(false),
		app.Size(unit.Dp(dipW), unit.Dp(dipH)),
		app.MinSize(unit.Dp(min(360, dipW)), unit.Dp(min(260, dipH))),
	)
	deferShow := target.Page != ""
	if deferShow {
		w.window.Option(appWindowHiddenMode.Option())
	}
	go w.workerLoop()
	go w.run(bounds, deferShow)
	w.enqueue(appWindowJob{kind: appJobOpenTab, tab: target.TabRef})
}

func (u *UI) initialAppWindowBounds(pluginID string, cascadeIndex int) (rect, int32, int32) {
	u.refreshMonitors()
	if saved, ok := u.appBounds[pluginID]; ok && saved.Placed && saved.Width > 0 && saved.Height > 0 {
		m := u.monitorFor(saved.Monitor)
		work := m.info.Work
		if work.width() > 0 && work.height() > 0 {
			dpi := m.dpi
			if dpi == 0 {
				dpi = 96
			}
			pxW := min(scale(int32(saved.Width), dpi), work.width())
			pxH := min(scale(int32(saved.Height), dpi), work.height())
			pxX := work.Left + scale(int32(saved.X), dpi)
			pxY := work.Top + scale(int32(saved.Y), dpi)
			clamped := clampWidget(rect{pxX, pxY, pxX + pxW, pxY + pxH}, work)
			return clamped, max(1, unscale(clamped.width(), dpi)), max(1, unscale(clamped.height(), dpi))
		}
	}
	foreground, _, _ := getForegroundWindow.Call()
	work := monitorOf(foreground).Work
	dpi := dpiForWindow(foreground)
	if work.width() <= 0 || work.height() <= 0 {
		work = monitorOfPoint(cursorPosition()).Work
		dpi = 96
	}
	dipW := max(1, min(int32(980), unscale(work.width(), dpi)-32))
	dipH := max(1, min(int32(660), unscale(work.height(), dpi)-48))
	pxW := min(scale(dipW, dpi), work.width())
	pxH := min(scale(dipH, dpi), work.height())
	offset := scale(int32((cascadeIndex%6)*32), dpi)
	x := work.Left + max(0, (work.width()-pxW)/2) + offset
	y := work.Top + max(0, (work.height()-pxH)/2) + offset
	clamped := clampWidget(rect{x, y, x + pxW, y + pxH}, work)
	return clamped, dipW, dipH
}

func (w *pluginAppWindow) windowTitle() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	title := w.pluginName
	if title == "" {
		title = w.pluginID
	}
	if len(w.tabs) == 1 && w.tabs[0].Title != "" && w.tabs[0].Title != title {
		title += " · " + w.tabs[0].Title
	}
	return title
}

func (w *pluginAppWindow) publishModel() {
	w.mu.Lock()
	cfg := w.appearance.Normalize()
	var pal gioui.SettingsPalette
	highContrast := isSystemHighContrast()
	if highContrast {
		p := highContrastPalette()
		pal = gioui.SettingsPalette{
			Background: gioColor(p.background), Surface: gioColor(p.surface), Sidebar: gioColor(p.surface),
			Text: gioColor(p.text), Secondary: gioColor(p.secondary), Line: gioColor(p.border), Accent: gioColor(p.accent),
			Selected: gioColor(p.selected), Hover: gioColor(p.selected), Weekend: gioColor(p.accent),
			Warning: gioColor(p.warning), WarningBg: gioColor(p.surface), OnAccent: gioColor(p.white),
		}
	} else {
		pal = gioui.SettingsPaletteFromTheme(cfg.Resolve(apptheme.ComponentSettings).Palette)
	}
	var pageCopy *pluginapps.Page
	if w.page != nil {
		cp := *w.page
		pageCopy = &cp
	}
	m := &gioui.AppWindowModel{
		Locale:          w.locale,
		PluginID:        w.pluginID,
		PluginName:      w.pluginName,
		Tabs:            slices.Clone(w.tabs),
		ActiveTabID:     w.activeTabID,
		State:           w.state,
		Error:           w.errText,
		Page:            pageCopy,
		Palette:         pal,
		CornerRadius:    cfg.EffectiveCornerRadius(),
		HasCornerRadius: true,
		TitleAccentBar:  cfg.TitleAccentBar,
		HighContrast:    highContrast,
		CustomTitleBar:  true,
		WebViewActive:   w.webViewReady && pageCopy != nil && pageCopy.Document != nil && w.webViewDoc == pageCopy.Document.URL,
	}
	w.model.Store(m)
	w.mu.Unlock()
	w.window.Invalidate()
}

func (w *pluginAppWindow) themePayloadJSON() json.RawMessage {
	w.mu.Lock()
	cfg := w.appearance.Normalize()
	w.mu.Unlock()
	p := cfg.Resolve(apptheme.ComponentSettings).Palette
	payload := map[string]any{
		"background": apptheme.FormatHex(p.Background),
		"surface":    apptheme.FormatHex(p.Surface),
		"sidebar":    apptheme.FormatHex(p.Sidebar),
		"border":     apptheme.FormatHex(p.Border),
		"text":       apptheme.FormatHex(p.Text),
		"secondary":  apptheme.FormatHex(p.Secondary),
		"accent":     apptheme.FormatHex(p.Accent),
		"onAccent":   apptheme.FormatHex(p.OnAccent),
		"weekend":    apptheme.FormatHex(p.Weekend),
		"warning":    apptheme.FormatHex(p.Warning),
		"warningBg":  apptheme.FormatHex(p.WarningBg),
		"selected":   apptheme.FormatHex(p.Selected),
		"hover":      apptheme.FormatHex(p.Hover),
		"isDark":     p.IsDark(),
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func (w *pluginAppWindow) webViewContentRect(hwnd uintptr, tabCount int) rect {
	cr := clientRect(hwnd)
	dpi := dpiForWindow(hwnd)
	topDip := int32(36)
	if tabCount > 1 {
		topDip += 40
	}
	topPx := scale(topDip, dpi)
	if topPx > cr.Bottom {
		topPx = cr.Bottom
	}
	return rect{Left: 0, Top: topPx, Right: cr.Right, Bottom: cr.Bottom}
}

func (w *pluginAppWindow) syncWebViewOnUIThread() {
	hwnd := w.hwnd.Load()
	if hwnd == 0 || w.closing.Load() {
		return
	}
	w.mu.Lock()
	page := w.page
	state := w.state
	tabCount := len(w.tabs)
	locale := w.locale
	curWV := w.webView
	curDoc := w.webViewDoc
	w.mu.Unlock()

	if state != gioui.AppStateReady || page == nil || page.Document == nil {
		if curWV != nil {
			curWV.close()
			w.mu.Lock()
			if w.webView == curWV {
				w.webView = nil
				w.webViewDoc = ""
				w.webViewReady = false
			}
			w.mu.Unlock()
		}
		return
	}

	doc := page.Document
	bounds := w.webViewContentRect(hwnd, tabCount)

	// Same document URL: just push updated revision/state + resize bounds (no reload).
	if curWV != nil && curDoc == doc.URL {
		curWV.setBounds(bounds, true)
		curWV.updateState(page.Revision, doc.State)
		return
	}

	// New document URL or session: replace existing WebView cleanly.
	if curWV != nil {
		curWV.close()
	}
	origin := ""
	if u, err := url.Parse(doc.URL); err == nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	session := page.Session
	pageSvc, _ := w.ui.cb.PluginApps.(pluginapps.PageService)
	if pageSvc == nil {
		w.mu.Lock()
		w.state = gioui.AppStateError
		w.errText = "host does not expose packaged page service"
		w.webView = nil
		w.webViewDoc = ""
		w.webViewReady = false
		w.mu.Unlock()
		w.publishModel()
		return
	}
	var userData string
	if cacheDir := w.ui.cacheDirectory(); cacheDir != "" {
		userData = filepath.Join(cacheDir, "webview", w.pluginID)
	} else {
		baseCache, _ := os.UserCacheDir()
		userData = filepath.Join(baseCache, "QuailCrate", "webview", w.pluginID)
	}
	wv := &webViewHost{
		hwnd:          hwnd,
		userDataDir:   userData,
		allowedOrigin: origin,
		allowedDocURL: doc.URL,
		initialTheme:  w.themePayloadJSON(),
		initialLocale: locale,
		initialState:  doc.State,
		initialRev:    page.Revision,
		background:    w.model.Load().Palette.Background,
		bounds:        bounds,
		visible:       true,
	}
	current := func() bool {
		// The caller holds w.mu. A displaced view must never update the window.
		return !w.closing.Load() && w.webView == wv && w.state == gioui.AppStateReady &&
			w.page != nil && w.page.Session == session && w.page.Document != nil &&
			w.page.Document.URL == doc.URL
	}
	wv.cb = webViewCallbacks{
		ReadAsset: func(uri string) (pluginapps.PageAsset, error) {
			return pageSvc.ReadPageAsset(session, uri)
		},
		OnMessage: func(sourceURL, rawJSON string) {
			w.mu.Lock()
			var ref pluginapps.PageRef
			if w.page != nil {
				ref = w.page.PageRef
			}
			w.mu.Unlock()
			if ref.Session != session {
				return
			}
			w.enqueue(appWindowJob{
				kind:       appJobPageMessage,
				page:       ref,
				sourceURL:  sourceURL,
				rawMessage: []byte(rawJSON),
			})
		},
		OnReady: func() {
			w.mu.Lock()
			if !current() {
				w.mu.Unlock()
				return
			}
			w.webViewReady = true
			w.mu.Unlock()
			w.publishModel()
		},
		OnError: func(err error) {
			w.mu.Lock()
			if !current() {
				w.mu.Unlock()
				return
			}
			w.state = gioui.AppStateError
			w.errText = err.Error()
			w.webViewDoc = ""
			w.webViewReady = false
			w.mu.Unlock()
			w.publishModel()
		},
	}
	w.mu.Lock()
	w.webView = wv
	w.webViewDoc = doc.URL
	w.webViewReady = false
	w.mu.Unlock()

	if err := wv.start(); err != nil {
		wv.cb.OnError(err)
		wv.close()
		w.mu.Lock()
		if w.webView == wv {
			w.webView = nil
			w.webViewDoc = ""
			w.webViewReady = false
		}
		w.mu.Unlock()
	}
}

func (w *pluginAppWindow) refreshThemeAndLocale(locale string, cfg apptheme.Config) {
	w.mu.Lock()
	w.locale = locale
	w.appearance = cfg.Normalize()
	w.mu.Unlock()
	w.publishModel()
	themeRaw := w.themePayloadJSON()
	if w.hwnd.Load() != 0 && !w.closing.Load() {
		w.window.Run(func() {
			w.mu.Lock()
			wv := w.webView
			w.mu.Unlock()
			if wv != nil {
				wv.setBackgroundColor(w.model.Load().Palette.Background)
				wv.updateThemeAndLocale(themeRaw, locale)
			}
		})
	}
}

func (w *pluginAppWindow) enqueue(job appWindowJob) {
	if w.closing.Load() {
		return
	}
	select {
	case w.jobs <- job:
	case <-w.workerStop:
	}
}

func (w *pluginAppWindow) stopWorker() {
	w.stopOnce.Do(func() { close(w.workerStop) })
}

func (w *pluginAppWindow) captureBounds() {
	hwnd := w.hwnd.Load()
	if hwnd == 0 || !validWindow(hwnd) {
		return
	}
	if iconic, _, _ := settingsIsIconic.Call(hwnd); iconic != 0 {
		return
	}
	if zoomed, _, _ := settingsIsZoomed.Call(hwnd); zoomed != 0 {
		return
	}
	r := windowRect(hwnd)
	if r.width() <= 0 || r.height() <= 0 {
		return
	}
	monHandle, _, _ := monitorFromWindow.Call(hwnd, 2)
	mon := describeMonitor(monHandle)
	dpi := mon.dpi
	if dpi == 0 {
		dpi = dpiForWindow(hwnd)
	}
	bounds := AppWindowBounds{
		Monitor: mon.id,
		X:       int(unscale(r.Left-mon.info.Work.Left, dpi)),
		Y:       int(unscale(r.Top-mon.info.Work.Top, dpi)),
		Width:   int(unscale(r.width(), dpi)),
		Height:  int(unscale(r.height(), dpi)),
		Placed:  true,
	}
	w.mu.Lock()
	w.lastBounds = bounds
	w.mu.Unlock()
}

func (w *pluginAppWindow) close() {
	w.captureBounds()
	w.closing.Store(true)
	w.stopWorker()
	if h := w.hwnd.Load(); h != 0 {
		postMessage.Call(h, wmClose, 0, 0)
	}
}

func (w *pluginAppWindow) syncSnapshot(snap pluginapps.Snapshot) {
	w.enqueue(appWindowJob{kind: appJobSyncSnapshot, snapshot: snap})
}

func (w *pluginAppWindow) workerLoop() {
	defer close(w.workerDone)
	defer func() {
		w.mu.Lock()
		session := w.session
		w.session = pluginapps.Session{}
		w.mu.Unlock()
		if session.ID != 0 && w.ui.cb.PluginApps != nil {
			w.ui.cb.PluginApps.CloseTab(session)
		}
	}()
	for {
		select {
		case <-w.workerStop:
			return
		case job := <-w.jobs:
			w.handleJob(job)
		}
	}
}

func (w *pluginAppWindow) handleJob(job appWindowJob) {
	if w.closing.Load() || w.ui.cb.PluginApps == nil {
		return
	}
	switch job.kind {
	case appJobOpenTab:
		w.openAndLoad(job.tab)
	case appJobReload:
		w.mu.Lock()
		session := w.session
		tabID := w.activeTabID
		gen := w.generation
		w.mu.Unlock()
		if session.ID != 0 {
			w.loadSession(session)
		} else {
			w.openAndLoad(pluginapps.TabRef{PluginID: w.pluginID, TabID: tabID, Generation: gen})
		}
	case appJobAct:
		w.actOnPage(job.page, job.controlID, job.itemID)
	case appJobSubmitValue:
		w.submitTabValue(job.page, job.controlID, job.inputValue)
	case appJobPageMessage:
		w.sendPageMessage(job.page, job.sourceURL, job.rawMessage)
	case appJobSyncSnapshot:
		w.applySnapshot(job.snapshot)
	}
}

func (w *pluginAppWindow) submitTabValue(pageRef pluginapps.PageRef, controlID string, value json.RawMessage) {
	w.mu.Lock()
	current := w.session
	w.mu.Unlock()
	if current != pageRef.Session || w.closing.Load() {
		return
	}
	pageSvc, ok := w.ui.cb.PluginApps.(pluginapps.PageService)
	if !ok {
		return
	}
	if err := pageSvc.SubmitTabValue(pageRef, controlID, value); err != nil {
		if errors.Is(err, pluginapps.ErrUnavailable) {
			w.reconcileUnavailable()
			return
		}
		return
	}
	w.loadSession(current)
}

func (w *pluginAppWindow) sendPageMessage(pageRef pluginapps.PageRef, sourceURL string, raw []byte) {
	w.mu.Lock()
	current := w.session
	if w.page != nil && w.page.Session == pageRef.Session {
		pageRef = w.page.PageRef
	}
	w.mu.Unlock()
	if current != pageRef.Session || w.closing.Load() {
		return
	}
	pageSvc, ok := w.ui.cb.PluginApps.(pluginapps.PageService)
	if !ok {
		return
	}
	// Parse requestId so even rejection sends a correlated error back to the page Promise.
	var probe struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(raw, &probe)

	reply, err := pageSvc.SendPageMessage(pageRef, sourceURL, raw)
	if w.closing.Load() {
		return
	}
	if err != nil {
		if errors.Is(err, pluginapps.ErrUnavailable) {
			w.loadSession(current)
		}
		if probe.RequestID != "" && w.hwnd.Load() != 0 {
			errMsg := err.Error()
			w.window.Run(func() {
				w.mu.Lock()
				wv := w.webView
				w.mu.Unlock()
				if wv != nil {
					wv.postJSON(map[string]any{"type": "reply", "requestId": probe.RequestID, "error": errMsg})
				}
			})
		}
		return
	}
	if w.hwnd.Load() != 0 {
		w.window.Run(func() {
			w.mu.Lock()
			wv := w.webView
			w.mu.Unlock()
			if wv != nil {
				wv.postJSON(map[string]any{
					"type":      "reply",
					"requestId": reply.RequestID,
					"revision":  reply.Revision,
					"value":     reply.Value,
				})
			}
		})
	}
	w.loadSession(current)
}

func (w *pluginAppWindow) openAndLoad(ref pluginapps.TabRef) {
	svc := w.ui.cb.PluginApps
	w.mu.Lock()
	switching := w.session.TabRef != ref
	w.activeTabID = ref.TabID
	w.generation = ref.Generation
	if switching || w.state != gioui.AppStateReady {
		w.state = gioui.AppStateLoading
		w.errText = ""
		if switching {
			w.page = nil
		}
	}
	w.mu.Unlock()
	w.publishModel()

	session, err := svc.OpenTab(ref, w.handleDialog)
	if errors.Is(err, pluginapps.ErrUnavailable) {
		snap, _ := svc.ObserveApps()
		name, tabs := pluginTabsFromSnapshot(snap, w.pluginID)
		if len(tabs) == 0 {
			w.close()
			return
		}
		target := tabs[0].TabRef
		if resolved, ok := snap.ResolveTab(w.pluginID, ref.TabID); ok {
			target = resolved.TabRef
		}
		w.mu.Lock()
		w.pluginName = name
		w.tabs = slices.Clone(tabs)
		w.activeTabID = target.TabID
		w.generation = target.Generation
		w.mu.Unlock()
		session, err = svc.OpenTab(target, w.handleDialog)
	}
	if err != nil {
		if errors.Is(err, pluginapps.ErrUnavailable) {
			w.reconcileUnavailable()
			return
		}
		w.mu.Lock()
		w.state = gioui.AppStateError
		w.errText = err.Error()
		w.page = nil
		w.mu.Unlock()
		w.publishModel()
		return
	}

	w.mu.Lock()
	w.session = session
	w.activeTabID = session.TabID
	w.generation = session.Generation
	w.mu.Unlock()
	w.loadSession(session)
}

func (w *pluginAppWindow) loadSession(session pluginapps.Session) {
	svc := w.ui.cb.PluginApps
	page, err := svc.LoadTab(session)
	w.mu.Lock()
	current := w.session
	w.mu.Unlock()
	if current != session || w.closing.Load() {
		return
	}
	if err != nil {
		if errors.Is(err, pluginapps.ErrUnavailable) {
			w.reconcileUnavailable()
			return
		}
		w.mu.Lock()
		if w.session == session {
			w.state = gioui.AppStateError
			w.errText = err.Error()
			w.page = nil
		}
		w.mu.Unlock()
		w.publishModel()
		return
	}
	w.mu.Lock()
	if w.session == session {
		w.state = gioui.AppStateReady
		w.errText = ""
		w.page = &page
	}
	w.mu.Unlock()
	w.publishModel()
	if w.hwnd.Load() != 0 && !w.closing.Load() {
		w.window.Run(func() { w.syncWebViewOnUIThread() })
	}
}

func (w *pluginAppWindow) actOnPage(pageRef pluginapps.PageRef, controlID, itemID string) {
	w.mu.Lock()
	current := w.session
	w.mu.Unlock()
	if current != pageRef.Session || w.closing.Load() {
		return
	}
	err := w.ui.cb.PluginApps.ActOnTab(pageRef, controlID, itemID)
	if w.closing.Load() {
		return
	}
	if err != nil {
		if errors.Is(err, pluginapps.ErrUnavailable) {
			w.reconcileUnavailable()
			return
		}
		w.ui.MessageFor(context.Background(), w.hwnd.Load(),
			w.ui.label("插件操作失败", "Plugin operation failed"), err.Error())
		return
	}
	w.loadSession(current)
}

func (w *pluginAppWindow) applySnapshot(snap pluginapps.Snapshot) {
	name, tabs := pluginTabsFromSnapshot(snap, w.pluginID)
	if len(tabs) == 0 {
		w.close()
		return
	}
	w.mu.Lock()
	w.pluginName = name
	w.tabs = slices.Clone(tabs)
	curGen := w.generation
	curTabID := w.activeTabID
	curSession := w.session
	w.mu.Unlock()

	target, found := snap.ResolveTab(w.pluginID, curTabID)
	if !found {
		target = tabs[0]
	}
	if curSession.ID == 0 || curGen != target.Generation || !found {
		w.openAndLoad(target.TabRef)
		return
	}
	w.publishModel()
	w.loadSession(curSession)
}

func (w *pluginAppWindow) reconcileUnavailable() {
	if w.closing.Load() || w.ui.cb.PluginApps == nil {
		return
	}
	snap, _ := w.ui.cb.PluginApps.ObserveApps()
	name, tabs := pluginTabsFromSnapshot(snap, w.pluginID)
	if len(tabs) == 0 {
		w.close()
		return
	}
	w.mu.Lock()
	w.pluginName = name
	w.tabs = slices.Clone(tabs)
	tabID := w.activeTabID
	w.mu.Unlock()
	target := tabs[0].TabRef
	if resolved, ok := snap.ResolveTab(w.pluginID, tabID); ok {
		target = resolved.TabRef
	}
	w.openAndLoad(target)
}

func (w *pluginAppWindow) handleDialog(ctx context.Context, action v1.Action, complete func(pluginapps.DialogResult)) {
	var once sync.Once
	finish := func(res pluginapps.DialogResult) {
		if complete != nil {
			once.Do(func() { complete(res) })
		}
	}
	if ctx != nil && ctx.Err() != nil {
		finish(pluginapps.DialogResult{Canceled: true})
		return
	}
	owner := w.hwnd.Load()
	switch action.Type {
	case "ui.promptText":
		w.ui.PromptTextFor(ctx, owner, action.Title, action.Value, func(value string, ok bool) {
			if !ok {
				finish(pluginapps.DialogResult{Canceled: true})
				return
			}
			finish(pluginapps.DialogResult{Value: value})
		})
	case "ui.pickOptions":
		w.ui.PickOptionsFor(ctx, owner, action.Title, action.Options, action.Multiple, func(items []string, ok bool) {
			finish(pluginapps.DialogResult{Items: items, Canceled: !ok})
		})
	case "ui.pickPaths":
		w.ui.PickPathsFor(ctx, owner, action.Title, action.Extensions, action.AllowDirectories, action.DirectoriesOnly, action.Multiple, func(paths []string, ok bool) {
			if !ok {
				finish(pluginapps.DialogResult{Canceled: true})
				return
			}
			finish(pluginapps.DialogResult{Paths: paths})
		})
	case "ui.message":
		w.ui.MessageFor(ctx, owner, action.Title, action.Value)
	default:
		finish(pluginapps.DialogResult{Error: "unsupported dialog action: " + action.Type})
	}
}

func (w *pluginAppWindow) run(initialBounds rect, deferShow bool) {
	var ops op.Ops
	view := gioui.NewAppWindowView()
	var lastTitle string
	var lastBorder uintptr
	var lastDark int32 = -1
	var maximized bool

	send := func(a gioui.AppWindowAction) {
		switch a.Kind {
		case gioui.AppWindowMinimize:
			w.window.Perform(system.ActionMinimize)
		case gioui.AppWindowToggleMaximize:
			if maximized {
				w.window.Perform(system.ActionUnmaximize)
			} else {
				w.window.Perform(system.ActionMaximize)
			}
		case gioui.AppWindowClose:
			w.captureBounds()
			w.window.Perform(system.ActionClose)
		case gioui.AppSelectTab:
			w.enqueue(appWindowJob{kind: appJobOpenTab, tab: a.Tab})
		case gioui.AppActControl:
			w.enqueue(appWindowJob{kind: appJobAct, page: a.Page, controlID: a.ControlID, itemID: a.ItemID})
		case gioui.AppSubmitValue:
			w.enqueue(appWindowJob{kind: appJobSubmitValue, page: a.Page, controlID: a.ControlID, inputValue: a.InputValue})
		case gioui.AppRetry:
			w.enqueue(appWindowJob{kind: appJobReload})
		}
	}

	for {
		switch e := w.window.Event().(type) {
		case app.ConfigEvent:
			maximized = e.Config.Mode == app.Maximized
			w.captureBounds()
			if hwnd := w.hwnd.Load(); hwnd != 0 && lastDark >= 0 {
				borderCopy, darkCopy := lastBorder, lastDark == 1
				w.window.Run(func() {
					applySettingsDWMFrame(hwnd, borderCopy, darkCopy)
					w.mu.Lock()
					wv := w.webView
					tabCount := len(w.tabs)
					w.mu.Unlock()
					if wv != nil {
						wv.setBounds(w.webViewContentRect(hwnd, tabCount), true)
					}
				})
			}
		case app.Win32ViewEvent:
			w.hwnd.Store(e.HWND)
			if e.HWND == 0 {
				continue
			}
			if w.closing.Load() {
				postMessage.Call(e.HWND, wmClose, 0, 0)
				continue
			}
			m := w.model.Load()
			isDark := !m.HighContrast && apptheme.Luminance(m.Palette.Surface) < 0.45
			borderCol := colorRef(m.Palette.Line)
			if m.HighContrast {
				borderCol = 0xffffffff
			}
			lastBorder = borderCol
			lastDark = 0
			if isDark {
				lastDark = 1
			}
			w.window.Run(func() {
				flags := uintptr(swpNoZOrder | swpNoActivate)
				if deferShow {
					// Gio's hidden mode strips the normal frame style. Restore
					// it before applying our DWM margins and rendering a frame.
					style, _, _ := getWindowLong.Call(e.HWND, gwlStyle)
					setWindowLong.Call(e.HWND, gwlStyle, style|wsCaption|wsSysMenu|wsThickFrame|wsMinimizeBox|wsMaximizeBox)
					flags |= settingsFrameChanged
				}
				applySettingsDWMFrame(e.HWND, borderCol, isDark)
				if initialBounds.width() > 0 && initialBounds.height() > 0 {
					setWindowPos.Call(e.HWND, 0, coord(initialBounds.Left), coord(initialBounds.Top),
						coord(initialBounds.width()), coord(initialBounds.height()), flags)
				}
				w.syncWebViewOnUIThread()
			})
			w.captureBounds()
		case app.FrameEvent:
			model := w.model.Load()
			m := *model
			m.Maximized = maximized
			w.captureBounds()
			title := w.windowTitle()
			isDark := !m.HighContrast && apptheme.Luminance(m.Palette.Surface) < 0.45
			borderCol := colorRef(m.Palette.Line)
			if m.HighContrast {
				borderCol = 0xffffffff
			}
			darkInt := int32(0)
			if isDark {
				darkInt = 1
			}
			if lastTitle != title || lastBorder != borderCol || lastDark != darkInt {
				lastTitle = title
				lastBorder = borderCol
				lastDark = darkInt
				w.window.Run(func() {
					hwnd := w.hwnd.Load()
					setText(hwnd, title)
					applySettingsDWMFrame(hwnd, borderCol, isDark)
				})
			}
			view.Layout(app.NewContext(&ops, e), m, send)
			e.Frame(&ops)
			if deferShow && !m.Loading() && w.model.Load() == model && w.hwnd.Load() != 0 && !w.closing.Load() {
				deferShow = false
				w.window.Run(func() {
					if !w.closing.Load() {
						showLoadedAppWindow(w.hwnd.Load())
					}
				})
			}
		case app.DestroyEvent:
			w.closing.Store(true)
			w.mu.Lock()
			wv := w.webView
			w.webView = nil
			w.webViewReady = false
			w.mu.Unlock()
			if wv != nil {
				wv.close()
			}
			w.stopWorker()
			<-w.workerDone
			w.mu.Lock()
			saved := w.lastBounds
			w.mu.Unlock()
			w.ui.post(func() {
				if w.ui.appWindows[w.pluginID] == w {
					delete(w.ui.appWindows, w.pluginID)
				}
				if saved.Placed {
					w.ui.appBounds[w.pluginID] = saved
					if w.ui.cb.SaveAppBounds != nil {
						w.ui.cb.SaveAppBounds(maps.Clone(w.ui.appBounds))
					}
				}
			})
			return
		}
	}
}

func showLoadedAppWindow(hwnd uintptr) {
	// The opening transition can expose the desktop around the freshly drawn frame.
	const transitionsForcedDisabled = 3 // DWMWA_TRANSITIONS_FORCEDISABLED
	disabled := uint32(1)
	hr, _, _ := settingsDwmAttribute.Call(hwnd, transitionsForcedDisabled, uintptr(unsafe.Pointer(&disabled)), unsafe.Sizeof(disabled))
	showWindow.Call(hwnd, swShow)
	if int32(hr) >= 0 {
		appDwmFlush.Call()
		disabled = 0
		settingsDwmAttribute.Call(hwnd, transitionsForcedDisabled, uintptr(unsafe.Pointer(&disabled)), unsafe.Sizeof(disabled))
	}
}

func (u *UI) SetAppBounds(bounds map[string]AppWindowBounds) {
	cloned := maps.Clone(bounds)
	u.post(func() {
		if cloned == nil {
			u.appBounds = make(map[string]AppWindowBounds)
		} else {
			u.appBounds = cloned
		}
	})
}

func (u *UI) isAppWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	for _, w := range u.appWindows {
		if w.hwnd.Load() == hwnd {
			return true
		}
	}
	return false
}

func (u *UI) openAppsPopupMenu(pos point) {
	pal := u.appearance.Resolve(apptheme.ComponentGlobal).Palette
	tabs := u.appSnapshot.Tabs
	if len(tabs) == 0 {
		return
	}
	labels := gioui.DisambiguateTabLabels(tabs)
	items := make([]gioui.MenuItem, len(tabs))
	for i, tab := range tabs {
		ic := tab.Icon
		if ic == "" {
			ic = "apps"
		}
		items[i] = gioui.MenuItem{
			ID:    strings.Join([]string{tab.PluginID, tab.TabID}, "\x00"),
			Label: labels[i],
			Icon:  ic,
		}
	}
	u.showContextMenu(pos, pal, items, func(id string) {
		for i, item := range items {
			if item.ID == id && i < len(tabs) {
				u.openPluginAppTab(tabs[i].TabRef)
				return
			}
		}
	})
}
