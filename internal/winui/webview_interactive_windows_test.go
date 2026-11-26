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

// TestWebView2LoadsRealMusicPluginWebApp loads the actual packaged plugins/music/web/
// assets (index.html, styles.css, qrcode.js, app.js) under strict PageAsset CSP headers
// inside the real Edge WebView2 Runtime and confirms app.js boots and sends library.refresh.
func TestWebView2LoadsRealMusicPluginWebApp(t *testing.T) {
	if _, _, err := resolveWebView2Proc(); err != nil {
		t.Skipf("Edge WebView2 Runtime not installed: %v", err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	webDir := filepath.Join("..", "..", "plugins", "music", "web")
	snapshotRaw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "music", "fixtures", "snapshot-success.json"))
	if err != nil {
		t.Fatalf("read music snapshot fixture: %v", err)
	}

	const origin = "https://music-real-1.quailcrate.invalid"
	const docURL = origin + "/index.html"
	mimeByExt := map[string]string{
		".html": "text/html; charset=utf-8",
		".css":  "text/css; charset=utf-8",
		".js":   "text/javascript; charset=utf-8",
	}

	module, _, _ := getModuleHandle.Call(0)
	clsName := "QuailCrateMusicWebViewTest"
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
	hwnd, err := window(clsName, "Music WebView2 Smoke", wsExToolWindow, wsPopup, rect{0, 0, 980, 660}, 0, 0, module)
	if err != nil {
		t.Fatalf("create window: %v", err)
	}
	defer destroyWindow.Call(hwnd)

	userDataDir, err := os.MkdirTemp("", "qc-wv2-music-*")
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

	var gotRefresh bool
	var loadedFiles []string
	var wvErr error
	var wv *webViewHost
	wv = &webViewHost{
		hwnd:          hwnd,
		userDataDir:   userDataDir,
		allowedOrigin: origin,
		allowedDocURL: docURL,
		initialTheme:  json.RawMessage(`{"background":"#1E1E2E","surface":"#242436","sidebar":"#181825","border":"#313244","text":"#CDD6F4","secondary":"#9399B2","accent":"#CBA6F7","onAccent":"#11111B","warning":"#F38BA8","warningBg":"#3A232D","selected":"#312A46","hover":"#2A2A3E","isDark":true}`),
		initialLocale: "zh-CN",
		initialState:  snapshotRaw,
		initialRev:    1,
		bounds:        rect{0, 0, 980, 660},
		visible:       true,
		cb: webViewCallbacks{
			ReadAsset: func(uri string) (pluginapps.PageAsset, error) {
				assetRel, err := pluginapps.AssetPath(origin, uri)
				if err != nil {
					return pluginapps.PageAsset{}, err
				}
				body, err := os.ReadFile(filepath.Join(webDir, filepath.FromSlash(assetRel)))
				if err != nil {
					return pluginapps.PageAsset{}, err
				}
				loadedFiles = append(loadedFiles, assetRel)
				ct := mimeByExt[filepath.Ext(assetRel)]
				if ct == "" {
					ct = "application/octet-stream"
				}
				return pluginapps.PageAsset{ContentType: ct, Body: body}, nil
			},
			OnMessage: func(sourceURL, rawJSON string) {
				var env struct {
					RequestID string `json:"requestId"`
					Operation string `json:"operation"`
				}
				if json.Unmarshal([]byte(rawJSON), &env) == nil && env.Operation == "library.refresh" {
					gotRefresh = true
					wv.postJSON(map[string]any{
						"type":      "reply",
						"requestId": env.RequestID,
						"revision":  1,
						"value":     map[string]any{"ok": true},
					})
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
		t.Fatalf("start music webview: %v", err)
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
			wvErr = fmt.Errorf("timed out waiting for music app.js library.refresh (loaded=%v)", loadedFiles)
			break
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}

	if wvErr != nil {
		t.Fatal(wvErr)
	}
	if !gotRefresh {
		t.Fatalf("music web app did not emit library.refresh (loaded=%v)", loadedFiles)
	}
	for _, want := range []string{"index.html", "assets/app.js", "assets/app.css"} {
		found := false
		for _, got := range loadedFiles {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("music web app did not load %q; loaded=%v", want, loadedFiles)
		}
	}
}
