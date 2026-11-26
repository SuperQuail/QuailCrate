package winui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"quailcrate/internal/pluginapps"
)

var (
	shlwapiDLL        = windows.NewLazySystemDLL("shlwapi.dll")
	shCreateMemStream = shlwapiDLL.NewProc("SHCreateMemStream")
	ole32WebView      = windows.NewLazySystemDLL("ole32.dll")
	wvCoInitEx        = ole32WebView.NewProc("CoInitializeEx")
	wvCoTaskMemFree   = ole32WebView.NewProc("CoTaskMemFree")

	wvIIDEnvironment10      = windows.GUID{Data1: 0xee0eb9df, Data2: 0x6f12, Data3: 0x46ce, Data4: [8]byte{0xb5, 0x3f, 0x3f, 0x47, 0xb9, 0xc9, 0x28, 0xe0}}
	wvIIDControllerOptions3 = windows.GUID{Data1: 0xb32b191a, Data2: 0x8998, Data3: 0x57ca, Data4: [8]byte{0xb7, 0xcb, 0xe0, 0x46, 0x17, 0xe4, 0xce, 0x4a}}
	wvIIDController2        = windows.GUID{Data1: 0xc979903e, Data2: 0xd4ca, Data3: 0x4228, Data4: [8]byte{0x92, 0xeb, 0x47, 0xee, 0x3f, 0xa9, 0x6e, 0xab}}
)

const (
	wvCoInitApartmentThreaded = 0x2
	wvPermissionStateDeny     = 2
	wvResourceContextAll      = 0
)

// ErrWebViewUnavailable reports that Microsoft Edge WebView2 Runtime is missing or failed to initialize.
var ErrWebViewUnavailable = errors.New("Microsoft Edge WebView2 Runtime is unavailable")

// webViewCallbacks connects the isolated WebView2 child container to pluginAppWindow.
type webViewCallbacks struct {
	ReadAsset func(uri string) (pluginapps.PageAsset, error)
	OnMessage func(sourceURL, rawJSON string)
	OnReady   func()
	OnError   func(err error)
}

// webViewHost owns one Edge WebView2 environment/controller/webview attached to a parent HWND.
// All public methods must be called on the STA thread owning the parent HWND (e.g. inside window.Run).
type webViewHost struct {
	hwnd          uintptr
	userDataDir   string
	allowedOrigin string
	allowedDocURL string
	initialTheme  json.RawMessage
	initialLocale string
	initialState  json.RawMessage
	initialRev    uint64
	background    color.NRGBA
	cb            webViewCallbacks
	bounds        rect
	visible       bool
	closed        bool
	ready         bool
	documentReady bool

	env        uintptr // *ICoreWebView2Environment
	controller uintptr // *ICoreWebView2Controller
	webview    uintptr // *ICoreWebView2

	// Prevent GC of COM callback structs while registered with WebView2.
	handlers []*wvComHandler
}

type wvComHandler struct {
	vtbl   *wvComVtbl
	refCnt int32
	id     uintptr
}

type wvComVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Invoke         uintptr
}

var (
	wvRegMu      sync.Mutex
	wvRegNext    uintptr = 1
	wvRegMap             = map[uintptr]func(a1, a2 uintptr) uintptr{}
	wvSharedVtbl         = wvComVtbl{
		QueryInterface: syscall.NewCallback(wvQI),
		AddRef:         syscall.NewCallback(wvAddRef),
		Release:        syscall.NewCallback(wvRelease),
		Invoke:         syscall.NewCallback(wvInvoke),
	}
)

func wvQI(this, riid, ppv uintptr) uintptr {
	if ppv == 0 {
		return 0x80004003 // E_POINTER
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = this
	wvAddRef(this)
	return 0 // S_OK
}

func wvAddRef(this uintptr) uintptr {
	h := (*wvComHandler)(unsafe.Pointer(this))
	return uintptr(atomic.AddInt32(&h.refCnt, 1))
}

func wvRelease(this uintptr) uintptr {
	h := (*wvComHandler)(unsafe.Pointer(this))
	return uintptr(atomic.AddInt32(&h.refCnt, -1))
}

func wvInvoke(this, arg1, arg2 uintptr) uintptr {
	h := (*wvComHandler)(unsafe.Pointer(this))
	wvRegMu.Lock()
	fn := wvRegMap[h.id]
	wvRegMu.Unlock()
	if fn != nil {
		return fn(arg1, arg2)
	}
	return 0
}

func (w *webViewHost) newHandler(fn func(a1, a2 uintptr) uintptr) uintptr {
	wvRegMu.Lock()
	id := wvRegNext
	wvRegNext++
	wvRegMap[id] = fn
	wvRegMu.Unlock()
	h := &wvComHandler{vtbl: &wvSharedVtbl, refCnt: 1, id: id}
	w.handlers = append(w.handlers, h)
	return uintptr(unsafe.Pointer(h))
}

//go:uintptrescapes
func wvComCall(obj uintptr, slot uintptr, args ...uintptr) uintptr {
	if obj == 0 {
		return 0x80004003
	}
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + slot*unsafe.Sizeof(uintptr(0))))
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, obj)
	callArgs = append(callArgs, args...)
	r, _, _ := syscall.SyscallN(fn, callArgs...)
	return r
}

func wvComRelease(obj *uintptr) {
	if obj != nil && *obj != 0 {
		wvComCall(*obj, 2) // IUnknown::Release
		*obj = 0
	}
}

func wvQueryInterface(obj uintptr, iid *windows.GUID) uintptr {
	var result uintptr
	if hr := wvComCall(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&result))); int32(hr) < 0 {
		return 0
	}
	return result
}

func wvUTF16String(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	defer wvCoTaskMemFree.Call(ptr)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr)))
}

func utf16Ptr(s string) uintptr {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return 0
	}
	return uintptr(unsafe.Pointer(p))
}

var (
	wvLoaderMu   sync.Mutex
	wvLoaderInit bool
	wvLoaderProc *windows.LazyProc
	wvLoaderKind string // "loader" or "internal"
	wvLoaderErr  error
)

func resolveWebView2Proc() (*windows.LazyProc, string, error) {
	wvLoaderMu.Lock()
	defer wvLoaderMu.Unlock()
	if wvLoaderInit {
		return wvLoaderProc, wvLoaderKind, wvLoaderErr
	}
	wvLoaderInit = true

	// 1) Try standard WebView2Loader.dll if bundled beside the executable or on PATH.
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "WebView2Loader.dll")
		if _, statErr := os.Stat(cand); statErr == nil {
			dll := windows.NewLazyDLL(cand)
			proc := dll.NewProc("CreateCoreWebView2EnvironmentWithOptions")
			if proc.Find() == nil {
				wvLoaderProc, wvLoaderKind = proc, "loader"
				return wvLoaderProc, wvLoaderKind, nil
			}
		}
	}

	// 2) Locate Microsoft Edge WebView2 Runtime's EmbeddedBrowserWebView.dll via registry & standard paths.
	dllPath := findEmbeddedWebViewDLL()
	if dllPath == "" {
		wvLoaderErr = ErrWebViewUnavailable
		return nil, "", wvLoaderErr
	}
	dll := windows.NewLazyDLL(dllPath)
	proc := dll.NewProc("CreateWebViewEnvironmentWithOptionsInternal")
	if err := proc.Find(); err != nil {
		wvLoaderErr = fmt.Errorf("%w: %v", ErrWebViewUnavailable, err)
		return nil, "", wvLoaderErr
	}
	wvLoaderProc, wvLoaderKind = proc, "internal"
	return wvLoaderProc, wvLoaderKind, nil
}

func findEmbeddedWebViewDLL() string {
	const clientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	regChecks := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + clientGUID},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + clientGUID},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + clientGUID},
	}
	for _, rc := range regChecks {
		k, err := registry.OpenKey(rc.root, rc.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		loc, _, errLoc := k.GetStringValue("location")
		pv, _, errPV := k.GetStringValue("pv")
		k.Close()
		if errLoc == nil && loc != "" {
			if errPV == nil && pv != "" {
				p := filepath.Join(loc, pv, "EBWebView", "x64", "EmbeddedBrowserWebView.dll")
				if _, st := os.Stat(p); st == nil {
					return p
				}
			}
			if found := latestEmbeddedInAppDir(loc); found != "" {
				return found
			}
		}
	}

	var roots []string
	if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
		roots = append(roots, filepath.Join(pf86, "Microsoft", "EdgeWebView", "Application"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		roots = append(roots, filepath.Join(pf, "Microsoft", "EdgeWebView", "Application"))
	}
	if local := os.Getenv("LocalAppData"); local != "" {
		roots = append(roots, filepath.Join(local, "Microsoft", "EdgeWebView", "Application"))
	}
	for _, dir := range roots {
		if found := latestEmbeddedInAppDir(dir); found != "" {
			return found
		}
	}
	return ""
}

func latestEmbeddedInAppDir(appDir string) string {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return ""
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dll := filepath.Join(appDir, e.Name(), "EBWebView", "x64", "EmbeddedBrowserWebView.dll")
		if _, st := os.Stat(dll); st == nil {
			candidates = append(candidates, dll)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	return candidates[len(candidates)-1]
}

// start initializes the Edge WebView2 environment & controller asynchronously on the calling STA thread.
func (w *webViewHost) start() error {
	wvCoInitEx.Call(0, wvCoInitApartmentThreaded)
	proc, kind, err := resolveWebView2Proc()
	if err != nil {
		return err
	}
	if w.userDataDir != "" {
		_ = os.MkdirAll(w.userDataDir, 0o700)
	}
	envHandler := w.newHandler(func(hr, env uintptr) uintptr {
		if w.closed {
			return 0
		}
		if int32(hr) < 0 || env == 0 {
			if w.cb.OnError != nil {
				w.cb.OnError(fmt.Errorf("%w (env hr=0x%08X)", ErrWebViewUnavailable, uint32(hr)))
			}
			return 0
		}
		wvComCall(env, 1) // AddRef
		w.env = env
		ctrlHandler := w.newHandler(func(hr2, ctrl uintptr) uintptr {
			if w.closed {
				return 0
			}
			if int32(hr2) < 0 || ctrl == 0 {
				if w.cb.OnError != nil {
					w.cb.OnError(fmt.Errorf("%w (controller hr=0x%08X)", ErrWebViewUnavailable, uint32(hr2)))
				}
				return 0
			}
			wvComCall(ctrl, 1) // AddRef
			w.controller = ctrl
			w.onControllerReady()
			return 0
		})
		hrCreate := w.createController(ctrlHandler)
		if int32(hrCreate) < 0 && w.cb.OnError != nil {
			w.cb.OnError(fmt.Errorf("%w (create controller hr=0x%08X)", ErrWebViewUnavailable, uint32(hrCreate)))
		}
		return 0
	})

	udPtr := utf16Ptr(w.userDataDir)
	var hr uintptr
	if kind == "loader" {
		hr, _, _ = proc.Call(0, udPtr, 0, envHandler)
	} else {
		// CreateWebViewEnvironmentWithOptionsInternal(checkRunning=true, runtimeType=0, userDataFolder, envOptions=nil, handler)
		hr, _, _ = proc.Call(1, 0, udPtr, 0, envHandler)
	}
	if int32(hr) < 0 {
		return fmt.Errorf("%w (start hr=0x%08X)", ErrWebViewUnavailable, uint32(hr))
	}
	return nil
}

func (w *webViewHost) createController(handler uintptr) uintptr {
	env10 := wvQueryInterface(w.env, &wvIIDEnvironment10)
	if env10 == 0 {
		return wvComCall(w.env, 3, w.hwnd, handler) // CreateCoreWebView2Controller
	}
	defer wvComRelease(&env10)

	var options uintptr
	hr := wvComCall(env10, 20, uintptr(unsafe.Pointer(&options))) // CreateCoreWebView2ControllerOptions
	if int32(hr) < 0 || options == 0 {
		return wvComCall(w.env, 3, w.hwnd, handler)
	}
	defer wvComRelease(&options)

	options3 := wvQueryInterface(options, &wvIIDControllerOptions3)
	if options3 == 0 {
		return wvComCall(w.env, 3, w.hwnd, handler)
	}
	defer wvComRelease(&options3)

	// Set the color during creation so even the first compositor surface uses it.
	if hr := wvComCall(options3, 10, wvBackgroundColor(w.background)); int32(hr) < 0 { // put_DefaultBackgroundColor
		return wvComCall(w.env, 3, w.hwnd, handler)
	}
	return wvComCall(env10, 21, w.hwnd, options, handler) // CreateCoreWebView2ControllerWithOptions
}

func wvBackgroundColor(c color.NRGBA) uintptr {
	// COREWEBVIEW2_COLOR is four bytes (A, R, G, B), passed by value on x64.
	return colorRef(c)<<8 | 0xff
}

func (w *webViewHost) setBackgroundColor(c color.NRGBA) {
	w.background = c
	controller2 := wvQueryInterface(w.controller, &wvIIDController2)
	if controller2 != 0 {
		wvComCall(controller2, 27, wvBackgroundColor(c)) // put_DefaultBackgroundColor
		wvComRelease(&controller2)
	}
}

func (w *webViewHost) onControllerReady() {
	wvComCall(w.controller, 4, 0) // put_IsVisible(false)
	w.setBackgroundColor(w.background)
	var wv uintptr
	// ICoreWebView2Controller::get_CoreWebView2 (slot 25)
	if hr := wvComCall(w.controller, 25, uintptr(unsafe.Pointer(&wv))); int32(hr) < 0 || wv == 0 {
		if w.cb.OnError != nil {
			w.cb.OnError(fmt.Errorf("%w (get_CoreWebView2 hr=0x%08X)", ErrWebViewUnavailable, uint32(hr)))
		}
		return
	}
	w.webview = wv

	// Harden ICoreWebView2Settings per pluginapi/v2/PAGES.CN.md.
	var settings uintptr
	if hr := wvComCall(w.webview, 3, uintptr(unsafe.Pointer(&settings))); int32(hr) >= 0 && settings != 0 {
		wvComCall(settings, 4, 1)  // put_IsScriptEnabled(true)
		wvComCall(settings, 6, 1)  // put_IsWebMessageEnabled(true)
		wvComCall(settings, 8, 0)  // put_AreDefaultScriptDialogsEnabled(false)
		wvComCall(settings, 10, 0) // put_IsStatusBarEnabled(false)
		wvComCall(settings, 12, 0) // put_AreDevToolsEnabled(false)
		wvComCall(settings, 14, 0) // put_AreDefaultContextMenusEnabled(false)
		wvComCall(settings, 16, 0) // put_AreHostObjectsAllowed(false)
		wvComCall(settings, 18, 0) // put_IsZoomControlEnabled(false)
		wvComCall(settings, 20, 0) // put_IsBuiltInErrorPageEnabled(false)
		wvComRelease(&settings)
	}

	var token int64
	// 1) Intercept ALL resource requests; serve ONLY the session's virtual origin from ReadPageAsset.
	wvComCall(w.webview, 57, utf16Ptr("*"), wvResourceContextAll) // AddWebResourceRequestedFilter
	resHandler := w.newHandler(func(_, args uintptr) uintptr {
		w.handleWebResourceRequested(args)
		return 0
	})
	wvComCall(w.webview, 55, resHandler, uintptr(unsafe.Pointer(&token))) // add_WebResourceRequested

	// 2) Block navigation to anything other than the exact active session Document.URL.
	var navigationID uint64
	var navigationStarted bool
	navHandler := w.newHandler(func(_, args uintptr) uintptr {
		var uriPtr uintptr
		wvComCall(args, 3, uintptr(unsafe.Pointer(&uriPtr))) // get_Uri
		uri := wvUTF16String(uriPtr)
		if uri != w.allowedDocURL {
			wvComCall(args, 8, 1) // put_Cancel(true)
		} else if !navigationStarted {
			hr := wvComCall(args, 9, uintptr(unsafe.Pointer(&navigationID))) // get_NavigationId
			if int32(hr) < 0 {
				wvComCall(args, 8, 1) // put_Cancel(true)
				if w.cb.OnError != nil {
					w.cb.OnError(fmt.Errorf("reading page navigation ID: HRESULT 0x%08X", uint32(hr)))
				}
				return 0
			}
			navigationStarted = true
		}
		return 0
	})
	if hr := wvComCall(w.webview, 7, navHandler, uintptr(unsafe.Pointer(&token))); int32(hr) < 0 {
		if w.cb.OnError != nil {
			w.cb.OnError(fmt.Errorf("registering page navigation start: HRESULT 0x%08X", uint32(hr)))
		}
		return
	}

	navCompleteHandler := w.newHandler(func(_, args uintptr) uintptr {
		if w.closed || w.documentReady || !navigationStarted {
			return 0
		}
		var id uint64
		hr := wvComCall(args, 5, uintptr(unsafe.Pointer(&id))) // get_NavigationId
		if int32(hr) < 0 {
			if w.cb.OnError != nil {
				w.cb.OnError(fmt.Errorf("reading completed page navigation ID: HRESULT 0x%08X", uint32(hr)))
			}
			return 0
		}
		if id != navigationID {
			return 0
		}
		var success int32
		hr = wvComCall(args, 3, uintptr(unsafe.Pointer(&success))) // get_IsSuccess
		if int32(hr) < 0 || success == 0 {
			if w.cb.OnError != nil {
				var status int32
				wvComCall(args, 4, uintptr(unsafe.Pointer(&status))) // get_WebErrorStatus
				w.cb.OnError(fmt.Errorf("packaged page navigation failed (WebView2 status %d, HRESULT 0x%08X)", status, uint32(hr)))
			}
			return 0
		}
		w.documentReady = true
		w.applyBoundsAndVisibility()
		if w.cb.OnReady != nil {
			w.cb.OnReady()
		}
		return 0
	})
	if hr := wvComCall(w.webview, 15, navCompleteHandler, uintptr(unsafe.Pointer(&token))); int32(hr) < 0 {
		if w.cb.OnError != nil {
			w.cb.OnError(fmt.Errorf("registering page navigation completion: HRESULT 0x%08X", uint32(hr)))
		}
		return
	}

	// 3) Block all child frame navigations.
	frameNavHandler := w.newHandler(func(_, args uintptr) uintptr {
		wvComCall(args, 8, 1) // put_Cancel(true)
		return 0
	})
	wvComCall(w.webview, 17, frameNavHandler, uintptr(unsafe.Pointer(&token))) // add_FrameNavigationStarting

	// 4) Block all new windows / popups.
	newWinHandler := w.newHandler(func(_, args uintptr) uintptr {
		wvComCall(args, 6, 1) // put_Handled(true)
		return 0
	})
	wvComCall(w.webview, 44, newWinHandler, uintptr(unsafe.Pointer(&token))) // add_NewWindowRequested

	// 5) Deny all native browser permissions (camera, microphone, geolocation, etc.).
	permHandler := w.newHandler(func(_, args uintptr) uintptr {
		wvComCall(args, 7, wvPermissionStateDeny) // put_State(DENY)
		return 0
	})
	wvComCall(w.webview, 23, permHandler, uintptr(unsafe.Pointer(&token))) // add_PermissionRequested

	// 6) Receive JSON messages from the top-level document and forward to PageService.
	msgHandler := w.newHandler(func(_, args uintptr) uintptr {
		var srcPtr, jsonPtr uintptr
		wvComCall(args, 3, uintptr(unsafe.Pointer(&srcPtr)))  // get_Source
		wvComCall(args, 4, uintptr(unsafe.Pointer(&jsonPtr))) // get_WebMessageAsJson
		sourceURL := wvUTF16String(srcPtr)
		rawJSON := wvUTF16String(jsonPtr)
		if sourceURL == w.allowedDocURL && w.cb.OnMessage != nil {
			w.cb.OnMessage(sourceURL, rawJSON)
		}
		return 0
	})
	wvComCall(w.webview, 34, msgHandler, uintptr(unsafe.Pointer(&token))) // add_WebMessageReceived

	// 7) Inject window.quailcrate bridge bootstrap before any page script executes.
	bootstrap := buildPageBridgeBootstrap(w.initialRev, w.initialState, w.initialTheme, w.initialLocale)
	wvComCall(w.webview, 27, utf16Ptr(bootstrap), 0) // AddScriptToExecuteOnDocumentCreated

	w.ready = true
	w.applyBoundsAndVisibility()
	if w.allowedDocURL != "" {
		// Navigate. OnReady runs when this navigation completes successfully.
		if hr := wvComCall(w.webview, 5, utf16Ptr(w.allowedDocURL)); int32(hr) < 0 && w.cb.OnError != nil {
			w.cb.OnError(fmt.Errorf("opening packaged page: HRESULT 0x%08X", uint32(hr)))
		}
	}
}

func (w *webViewHost) handleWebResourceRequested(args uintptr) {
	if w.env == 0 || args == 0 {
		return
	}
	var req uintptr
	if hr := wvComCall(args, 3, uintptr(unsafe.Pointer(&req))); int32(hr) < 0 || req == 0 {
		return
	}
	defer wvComRelease(&req)

	var uriPtr uintptr
	wvComCall(req, 3, uintptr(unsafe.Pointer(&uriPtr))) // get_Uri
	uri := wvUTF16String(uriPtr)

	status := uintptr(403)
	reason := "Forbidden"
	var body []byte
	headersStr := "Content-Type: text/plain; charset=utf-8\r\nCache-Control: no-store"

	if w.allowedOrigin != "" && strings.HasPrefix(uri, w.allowedOrigin+"/") && w.cb.ReadAsset != nil {
		if asset, err := w.cb.ReadAsset(uri); err == nil {
			status = 200
			reason = "OK"
			body = asset.Body
			hdrs := asset.Headers()
			keys := make([]string, 0, len(hdrs))
			for k := range hdrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			lines := make([]string, 0, len(keys))
			for _, k := range keys {
				lines = append(lines, k+": "+hdrs[k])
			}
			headersStr = strings.Join(lines, "\r\n")
		} else {
			status = 404
			reason = "Not Found"
		}
	}

	var stream uintptr
	if len(body) > 0 {
		stream, _, _ = shCreateMemStream.Call(uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)))
	} else {
		var empty byte
		stream, _, _ = shCreateMemStream.Call(uintptr(unsafe.Pointer(&empty)), 0)
	}
	if stream != 0 {
		defer wvComRelease(&stream)
	}

	var resp uintptr
	// ICoreWebView2Environment::CreateWebResourceResponse (slot 4)
	if hr := wvComCall(w.env, 4, stream, status, utf16Ptr(reason), utf16Ptr(headersStr), uintptr(unsafe.Pointer(&resp))); int32(hr) >= 0 && resp != 0 {
		wvComCall(args, 5, resp) // put_Response
		wvComRelease(&resp)
	}
}

func (w *webViewHost) setBounds(r rect, visible bool) {
	w.bounds = r
	w.visible = visible
	w.applyBoundsAndVisibility()
}

func (w *webViewHost) applyBoundsAndVisibility() {
	if w.closed || w.controller == 0 {
		return
	}
	rc := w.bounds
	if rc.Right < rc.Left {
		rc.Right = rc.Left
	}
	if rc.Bottom < rc.Top {
		rc.Bottom = rc.Top
	}
	vis := uintptr(0)
	if w.visible && w.documentReady && rc.width() > 0 && rc.height() > 0 {
		vis = 1
	}
	// On Windows x64 ABI, RECT (16 bytes) is passed by pointer to put_Bounds (slot 6).
	wvComCall(w.controller, 6, uintptr(unsafe.Pointer(&rc)))
	wvComCall(w.controller, 4, vis) // put_IsVisible
}

func (w *webViewHost) updateState(rev uint64, state json.RawMessage) {
	w.initialRev = rev
	w.initialState = state
	if w.ready {
		w.postJSON(map[string]any{
			"type":     "state",
			"revision": rev,
			"state":    state,
		})
	}
}

func (w *webViewHost) updateThemeAndLocale(themeRaw json.RawMessage, locale string) {
	w.initialTheme = themeRaw
	w.initialLocale = locale
	if w.ready {
		w.postJSON(map[string]any{"type": "theme", "theme": themeRaw})
		w.postJSON(map[string]any{"type": "locale", "locale": locale})
	}
}

func (w *webViewHost) postJSON(payload any) {
	if w.closed || !w.ready || w.webview == 0 {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	wvComCall(w.webview, 32, utf16Ptr(string(data))) // PostWebMessageAsJson
}

func (w *webViewHost) close() {
	if w.closed {
		return
	}
	w.closed = true
	w.ready = false
	if w.controller != 0 {
		wvComCall(w.controller, 24) // Close
	}
	wvComRelease(&w.webview)
	wvComRelease(&w.controller)
	wvComRelease(&w.env)

	wvRegMu.Lock()
	for _, h := range w.handlers {
		delete(wvRegMap, h.id)
	}
	wvRegMu.Unlock()
	w.handlers = nil
}

// buildPageBridgeBootstrap constructs the small document-start script that binds
// window.quailcrate to window.chrome.webview.postMessage with request-ID correlation
// and live revision/state/theme/locale updates.
func buildPageBridgeBootstrap(rev uint64, stateJSON, themeJSON json.RawMessage, locale string) string {
	if len(stateJSON) == 0 {
		stateJSON = json.RawMessage(`{}`)
	}
	if len(themeJSON) == 0 {
		themeJSON = json.RawMessage(`{}`)
	}
	locBytes, _ := json.Marshal(locale)
	return fmt.Sprintf(`(function(){
  if (window.quailcrate) return;
  let _rev = %d;
  let _state = %s;
  let _theme = %s;
  let _locale = %s;
  let _seq = 0;
  const _pending = new Map();
  const _stateListeners = new Set();
  const _themeListeners = new Set();

  if (window.chrome && window.chrome.webview) {
    window.chrome.webview.addEventListener('message', function(ev) {
      const msg = ev && ev.data;
      if (!msg || typeof msg !== 'object') return;
      if (msg.type === 'state') {
        if (typeof msg.revision === 'number' && msg.revision > 0) _rev = msg.revision;
        if (msg.state !== undefined) _state = msg.state;
        _stateListeners.forEach(function(fn){ try { fn({ revision: _rev, state: _state }); } catch(_){} });
      } else if (msg.type === 'reply') {
        const p = _pending.get(msg.requestId);
        if (p) {
          _pending.delete(msg.requestId);
          if (msg.error) p.reject(new Error(msg.error));
          else p.resolve({ requestId: msg.requestId, revision: msg.revision, value: msg.value });
        }
      } else if (msg.type === 'theme') {
        _theme = msg.theme;
        _themeListeners.forEach(function(fn){ try { fn(_theme); } catch(_){} });
      } else if (msg.type === 'locale') {
        _locale = msg.locale || _locale;
      }
    });
  }

  window.quailcrate = {
    getRevision: function(){ return _rev; },
    getState: function(){ return _state; },
    getTheme: function(){ return _theme; },
    getLocale: function(){ return _locale; },
    onState: function(fn){ _stateListeners.add(fn); return function(){ _stateListeners.delete(fn); }; },
    onTheme: function(fn){ _themeListeners.add(fn); return function(){ _themeListeners.delete(fn); }; },
    send: function(operation, args) {
      return new Promise(function(resolve, reject) {
        if (!window.chrome || !window.chrome.webview) {
          reject(new Error('WebView bridge unavailable'));
          return;
        }
        _seq++;
        const requestId = 'req-' + _seq;
        _pending.set(requestId, { resolve: resolve, reject: reject });
        window.chrome.webview.postMessage({
          requestId: requestId,
          revision: _rev,
          operation: String(operation || ''),
          args: (args && typeof args === 'object') ? args : {}
        });
      });
    }
  };
})();`, rev, string(stateJSON), string(themeJSON), string(locBytes))
}
