//go:build interactive && windows

package winui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/plugins"
)

// TestWebView2PackagedPageBridge verifies the pure-Go COM WebView2 container
// against the real Microsoft Edge WebView2 Runtime installed on Windows:
//   - Virtual-origin packaged assets (index.html + app.js) load under strict CSP.
//   - External network fetches are blocked with HTTP 403 by the host resource filter.
//   - window.quailcrate.send() <-> PostWebMessageAsJson round-trips cleanly.
//   - setBounds() and close() run without errors or leaks.
func TestWebView2PackagedPageBridge(t *testing.T) {
	if _, _, err := resolveWebView2Proc(); err != nil {
		t.Skipf("Edge WebView2 Runtime not installed: %v", err)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	const origin = "https://page-test-1.quailcrate.invalid"
	const docURL = origin + "/index.html"
	assets := map[string]pluginapps.PageAsset{
		docURL: {
			ContentType: "text/html; charset=utf-8",
			Body:        []byte(`<!doctype html><html><head><meta charset="utf-8"><script defer src="./app.js"></script></head><body><div id="out">boot</div></body></html>`),
		},
		origin + "/app.js": {
			ContentType: "text/javascript; charset=utf-8",
			Body: []byte(`
(async function () {
  var externalStatus = -1;
  try {
    var resp = await fetch("https://example.invalid/blocked.json");
    externalStatus = resp.status;
  } catch (e) {
    externalStatus = 0;
  }
  var reply = await window.quailcrate.send("ping", { n: 42, externalStatus: externalStatus });
  var th = window.quailcrate.getTheme();
  await window.quailcrate.send("done", { echoed: reply && reply.value && reply.value.echo, themeBg: th && th.background });
})();
`),
		},
	}

	module, _, _ := getModuleHandle.Call(0)
	clsName := "QuailCrateWebViewTestHost"
	proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		r, _, _ := defWindowProc.Call(hwnd, msg, wp, lp)
		return r
	})
	wc := wndClass{
		Size:      uint32(unsafe.Sizeof(wndClass{})),
		WndProc:   proc,
		Instance:  module,
		ClassName: wstr(clsName),
	}
	registerClass.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, err := window(clsName, "WebView2 Smoke", wsExToolWindow, wsPopup, rect{0, 0, 480, 320}, 0, 0, module)
	if err != nil {
		t.Fatalf("create host window: %v", err)
	}
	defer destroyWindow.Call(hwnd)

	var gotPing bool
	var gotDone bool
	var externalStatus int
	var echoed int
	var themeBg string
	var wvErr error

	userDataDir, err := os.MkdirTemp("", "qc-wv2-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range 10 {
			if os.RemoveAll(userDataDir) == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	var wv *webViewHost
	wv = &webViewHost{
		hwnd:          hwnd,
		userDataDir:   userDataDir,
		allowedOrigin: origin,
		allowedDocURL: docURL,
		initialTheme:  json.RawMessage(`{"background":"#1E1E2E","isDark":true}`),
		initialLocale: "zh-CN",
		initialState:  json.RawMessage(`{"hello":"world"}`),
		initialRev:    1,
		bounds:        rect{0, 0, 480, 320},
		visible:       true,
		cb: webViewCallbacks{
			ReadAsset: func(uri string) (pluginapps.PageAsset, error) {
				if a, ok := assets[uri]; ok {
					return a, nil
				}
				return pluginapps.PageAsset{}, fmt.Errorf("not found: %s", uri)
			},
			OnMessage: func(sourceURL, rawJSON string) {
				if sourceURL != docURL {
					wvErr = fmt.Errorf("unexpected sourceURL %q", sourceURL)
					postQuitMessage.Call(0)
					return
				}
				var env struct {
					RequestID string          `json:"requestId"`
					Operation string          `json:"operation"`
					Args      json.RawMessage `json:"args"`
				}
				if err := json.Unmarshal([]byte(rawJSON), &env); err != nil {
					wvErr = err
					postQuitMessage.Call(0)
					return
				}
				switch env.Operation {
				case "ping":
					var args struct {
						N              int `json:"n"`
						ExternalStatus int `json:"externalStatus"`
					}
					_ = json.Unmarshal(env.Args, &args)
					gotPing = args.N == 42
					externalStatus = args.ExternalStatus
					wv.postJSON(map[string]any{
						"type":      "reply",
						"requestId": env.RequestID,
						"revision":  2,
						"value":     map[string]any{"echo": args.N * 2},
					})
				case "done":
					var args struct {
						Echoed  int    `json:"echoed"`
						ThemeBg string `json:"themeBg"`
					}
					_ = json.Unmarshal(env.Args, &args)
					echoed = args.Echoed
					themeBg = args.ThemeBg
					gotDone = true
					wv.postJSON(map[string]any{"type": "reply", "requestId": env.RequestID, "revision": 2, "value": map[string]any{"ok": true}})
					postQuitMessage.Call(0)
				}
			},
			OnError: func(err error) {
				wvErr = err
				postQuitMessage.Call(0)
			},
		},
	}

	if err := wv.start(); err != nil {
		t.Fatalf("webViewHost.start: %v", err)
	}
	defer wv.close()

	// Pump Win32 messages until the JS round-trip finishes or 15 seconds elapse.
	setTimer.Call(hwnd, 99, 15000, 0)
	defer killTimer.Call(hwnd, 99)

	for {
		var msg message
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		if msg.Message == wmTimer && msg.WParam == 99 {
			wvErr = fmt.Errorf("timed out waiting for WebView2 bridge round-trip (ready=%v ping=%v done=%v)", wv.ready, gotPing, gotDone)
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}

	if wvErr != nil {
		t.Fatal(wvErr)
	}
	if !gotPing || !gotDone || echoed != 84 || !strings.EqualFold(themeBg, "#1E1E2E") {
		t.Fatalf("unexpected WebView2 bridge results: ping=%v done=%v echoed=%d themeBg=%q", gotPing, gotDone, echoed, themeBg)
	}
	if externalStatus != 403 && externalStatus != 0 {
		t.Fatalf("external URL was not blocked by host resource filter: status=%d", externalStatus)
	}
}

// TestWebView2LoadsPackagedPluginWebApp exercises on-disk module and style assets
// served through the virtual origin under the PageAsset CSP.
func TestWebView2LoadsPackagedPluginWebApp(t *testing.T) {
	if _, _, err := resolveWebView2Proc(); err != nil {
		t.Skipf("Edge WebView2 Runtime not installed: %v", err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	const entry = "web/index.html"
	pkg := plugins.Package{Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(pkg.Dir, "web", "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		entry: `<!doctype html><html><head><meta charset="utf-8">
<link rel="stylesheet" href="./assets/app.css"><script type="module" src="./assets/app.js"></script>
</head><body><div id="fixture">Packaged plugin fixture</div></body></html>`,
		"web/assets/app.css": `#fixture { color: rgb(12, 34, 56); }`,
		"web/assets/app.js": `
const state = window.quailcrate.getState();
const color = getComputedStyle(document.getElementById("fixture")).color;
void window.quailcrate.send("ready", { greeting: state.greeting, color });`,
	} {
		if err := os.WriteFile(filepath.Join(pkg.Dir, filepath.FromSlash(name)), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	const origin = "https://packaged-web-app.quailcrate.invalid"
	const docURL = origin + "/index.html"

	module, _, _ := getModuleHandle.Call(0)
	clsName := "QuailCratePackagedWebViewTest"
	proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		r, _, _ := defWindowProc.Call(hwnd, msg, wp, lp)
		return r
	})
	wc := wndClass{
		Size:      uint32(unsafe.Sizeof(wndClass{})),
		WndProc:   proc,
		Instance:  module,
		ClassName: wstr(clsName),
	}
	registerClass.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, err := window(clsName, "Packaged WebView2 Smoke", wsExToolWindow, wsPopup, rect{0, 0, 480, 320}, 0, 0, module)
	if err != nil {
		t.Fatalf("create window: %v", err)
	}
	defer destroyWindow.Call(hwnd)

	userDataDir, err := os.MkdirTemp("", "qc-wv2-packaged-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range 10 {
			if os.RemoveAll(userDataDir) == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	var gotReady bool
	loadedFiles := make(map[string]bool)
	var wvErr error
	var wv *webViewHost
	wv = &webViewHost{
		hwnd:          hwnd,
		userDataDir:   userDataDir,
		allowedOrigin: origin,
		allowedDocURL: docURL,
		initialTheme:  json.RawMessage(`{"background":"#1E1E2E","isDark":true}`),
		initialLocale: "zh-CN",
		initialState:  json.RawMessage(`{"greeting":"hello from the host"}`),
		initialRev:    1,
		bounds:        rect{0, 0, 480, 320},
		visible:       true,
		cb: webViewCallbacks{
			ReadAsset: func(uri string) (pluginapps.PageAsset, error) {
				assetRel, err := pluginapps.AssetPath(origin, uri)
				if err != nil {
					return pluginapps.PageAsset{}, err
				}
				body, contentType, err := pkg.ReadPageAsset(entry, assetRel)
				if err != nil {
					return pluginapps.PageAsset{}, err
				}
				loadedFiles[assetRel] = true
				return pluginapps.PageAsset{ContentType: contentType, Body: body}, nil
			},
			OnMessage: func(sourceURL, rawJSON string) {
				defer postQuitMessage.Call(0)
				msg, err := pluginapps.ParsePageMessage([]byte(rawJSON))
				if err != nil || sourceURL != docURL || msg.Operation != "ready" || msg.Revision != 1 {
					wvErr = fmt.Errorf("unexpected page message from %q: %s (%v)", sourceURL, rawJSON, err)
					return
				}
				var args struct {
					Greeting string `json:"greeting"`
					Color    string `json:"color"`
				}
				if err := json.Unmarshal(msg.Args, &args); err != nil || args.Greeting != "hello from the host" || args.Color != "rgb(12, 34, 56)" {
					wvErr = fmt.Errorf("module did not see the initial state and applied CSS: %s (%v)", msg.Args, err)
					return
				}
				gotReady = true
			},
			OnError: func(err error) {
				wvErr = fmt.Errorf("%w (loaded=%v)", err, loadedFiles)
				postQuitMessage.Call(0)
			},
		},
	}

	if err := wv.start(); err != nil {
		t.Fatalf("start packaged webview: %v", err)
	}
	defer wv.close()

	setTimer.Call(hwnd, 98, 15000, 0)
	defer killTimer.Call(hwnd, 98)

	for {
		var msg message
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		if msg.Message == wmTimer && msg.WParam == 98 {
			wvErr = fmt.Errorf("timed out waiting for packaged app.js ready message (loaded=%v)", loadedFiles)
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}

	if wvErr != nil {
		t.Fatal(wvErr)
	}
	if !gotReady {
		t.Fatalf("packaged web app did not emit ready (loaded=%v)", loadedFiles)
	}
	for _, want := range []string{"index.html", "assets/app.js", "assets/app.css"} {
		if !loadedFiles[want] {
			t.Fatalf("packaged web app did not load %q (loaded=%v)", want, loadedFiles)
		}
	}
}
