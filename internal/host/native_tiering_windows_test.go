package host

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
)

func TestThirdPartyNativeRiskConfirmationAndDigestReconfirmation(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user-plugins")
	pluginDir := filepath.Join(user, "ext-native")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "example.native-risk"
	const manifest = `{
		"schemaVersion": 2,
		"hostApiVersion": 2,
		"id": "example.native-risk",
		"name": "Native Risk Demo",
		"version": "1.0.0",
		"runtime": "dll",
		"entry": "demo.dll",
		"platform": "windows/amd64",
		"abiVersion": 2,
		"contributes": ["commands"],
		"permissions": ["apps.read"]
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "demo.dll"), []byte("dummy-dll-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(root, "bundled"), user, nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	defer func() { h.cancel(); h.stopAll() }()

	var mu sync.Mutex
	var prompts []struct{ title, body string }
	approve := false
	h.confirmDialog = func(title, body string, done func(bool)) {
		mu.Lock()
		prompts = append(prompts, struct{ title, body string }{title, body})
		ok := approve
		mu.Unlock()
		done(ok)
	}

	h.scan()
	h.mu.Lock()
	rec := h.records[id]
	info := rec.info
	initialDigest := rec.pkg.NativeDigest
	h.mu.Unlock()
	if !info.FullSystemAccess || info.Bundled {
		t.Fatalf("third-party DLL metadata = %+v; want FullSystemAccess=true Bundled=false", info)
	}
	wantDataDir := filepath.Join(root, "plugins", id)
	if got := h.pluginDataDir(id); got != wantDataDir {
		t.Fatalf("pluginDataDir(%q) = %q, want %q", id, got, wantDataDir)
	}

	// 1. User declines first-time full-system-access risk prompt.
	h.toggle(id, true)
	mu.Lock()
	count := len(prompts)
	first := prompts[0]
	mu.Unlock()
	if count != 1 || !strings.Contains(first.body, "full Windows user permissions") || !strings.Contains(first.body, "SHA-256:") || !strings.Contains(first.body, "apps.read") {
		t.Fatalf("unexpected risk warning prompt: %+v", first)
	}
	if trust, err := db.NativeTrust(h.ctx, id); err != nil || trust.Exists {
		t.Fatalf("declined prompt should not record trust: %+v, %v", trust, err)
	}
	if pref, err := db.Preference(h.ctx, id); err != nil || pref.Enabled {
		t.Fatalf("declined prompt should not enable plugin: %+v, %v", pref, err)
	}

	// 2. User approves the warning -> trust record saved with matching SHA-256 digest.
	mu.Lock()
	approve = true
	mu.Unlock()
	h.toggle(id, true)
	trust, err := db.NativeTrust(h.ctx, id)
	if err != nil || !trust.Exists || trust.SHA256 != initialDigest {
		t.Fatalf("approved prompt did not persist expected trust digest: got=%+v want=%q err=%v", trust, initialDigest, err)
	}

	// 3. Modifying the plugin directory (adding a companion DLL) blocks auto-enable on rescan
	// and requires re-confirmation explaining the binary files changed.
	if err := os.WriteFile(filepath.Join(pluginDir, "companion.dll"), []byte("new-companion"), 0600); err != nil {
		t.Fatal(err)
	}
	h.scan()
	h.mu.Lock()
	stateAfterChange := h.records[id].info.State
	h.mu.Unlock()
	if stateAfterChange != "authorization" {
		t.Fatalf("expected authorization required after binary change, got state %q", stateAfterChange)
	}

	mu.Lock()
	prompts = nil
	approve = false
	mu.Unlock()
	h.toggle(id, true)
	mu.Lock()
	reprompt := prompts
	mu.Unlock()
	if len(reprompt) != 1 || !strings.Contains(reprompt[0].body, "binary files have changed") {
		t.Fatalf("expected binary-changed reconfirmation prompt, got %+v", reprompt)
	}
}

func TestNativeBackgroundNotifyAndEventPublish(t *testing.T) {
	probe := os.Getenv("QUAILCRATE_NATIVE_PROBE")
	runner := os.Getenv("QUAILCRATE_NATIVE_RUNNER")
	if probe == "" || runner == "" {
		t.Skip("build native integration fixtures with scripts/build.ps1 -TestNative")
	}
	root := t.TempDir()
	user := filepath.Join(root, "user-plugins")
	nativeDir := filepath.Join(user, "native-push")
	luaDir := filepath.Join(user, "lua-listener")
	for _, d := range []string{nativeDir, luaDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	rawProbe, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, "probe.dll"), rawProbe, 0600); err != nil {
		t.Fatal(err)
	}
	const nativeID = "test.native-push"
	const nativeManifest = `{
		"schemaVersion": 2,
		"hostApiVersion": 2,
		"id": "test.native-push",
		"name": "Native Push",
		"version": "1.0.0",
		"runtime": "dll",
		"entry": "probe.dll",
		"platform": "windows/amd64",
		"abiVersion": 2,
		"contributes": ["commands", "widgets", "appTabs"],
		"publishes": ["test.native-push.tick"],
		"permissions": []
	}`
	if err := os.WriteFile(filepath.Join(nativeDir, "manifest.json"), []byte(nativeManifest), 0600); err != nil {
		t.Fatal(err)
	}
	const luaID = "test.lua-listener"
	const luaManifest = `{
		"schemaVersion": 2,
		"hostApiVersion": 2,
		"id": "test.lua-listener",
		"name": "Lua Listener",
		"version": "1.0.0",
		"runtime": "lua",
		"entry": "plugin.lua",
		"contributes": [],
		"listens": ["test.native-push.tick"],
		"permissions": []
	}`
	const luaScript = `return function(req)
  local d = {}
  if req.op == "event" and req.payload.event.type == "plugin.event" and req.payload.event.topic == "test.native-push.tick" then
    d.state = {received = req.payload.event.detail.count}
  end
  return {requestId = req.requestId, ok = true, data = d}
end`
	if err := os.WriteFile(filepath.Join(luaDir, "manifest.json"), []byte(luaManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(luaDir, "plugin.lua"), []byte(luaScript), 0600); err != nil {
		t.Fatal(err)
	}

	_, nativeDigest, err := plugins.ComputeNativeDigest(nativeDir, filepath.Join(nativeDir, "probe.dll"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetNativeTrust(t.Context(), nativeID, nativeDigest, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPreference(t.Context(), nativeID, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPreference(t.Context(), luaID, true, []string{"listen:test.native-push.tick"}); err != nil {
		t.Fatal(err)
	}

	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(root, "bundled"), user, nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	defer func() { h.cancel(); h.stopAll() }()
	h.scan()

	h.mu.Lock()
	rec := h.records[nativeID]
	state := rec.info.State
	detail := rec.info.Detail
	h.mu.Unlock()
	if state != "running" {
		t.Fatalf("native plugin did not start: state=%q detail=%q", state, detail)
	}

	// Open the plugin's app tab and observe updates.
	tab, ok := h.AppCatalog().ResolveTab(nativeID, "panel")
	if !ok {
		t.Fatal("missing panel tab from native push plugin")
	}
	session, err := h.OpenTab(tab.TabRef, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.LoadTab(session); err != nil {
		t.Fatal(err)
	}
	_, changed := h.ObserveApps()

	// Trigger the native command that spawns a background thread pushing widget.refresh + event.publish.
	if err := h.invoke(nativeID, 0, "command", v1.Payload{CommandID: "push"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for app tab change notification from native push")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		nativeSnap, err := db.Snapshot(h.ctx, nativeID)
		if err != nil {
			t.Fatal(err)
		}
		luaSnap, err := db.Snapshot(h.ctx, luaID)
		if err != nil {
			t.Fatal(err)
		}
		var ns struct{ Count int }
		var ls struct{ Received int }
		if json.Unmarshal(nativeSnap.State, &ns) == nil && ns.Count == 1 &&
			json.Unmarshal(luaSnap.State, &ls) == nil && ls.Received == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native push did not propagate: native=%s lua=%s", nativeSnap.State, luaSnap.State)
		}
		time.Sleep(25 * time.Millisecond)
	}

	h.mu.Lock()
	widgets := h.records[nativeID].widgets
	h.mu.Unlock()
	if len(widgets) != 1 || widgets[0].View.Title != "Live 1" {
		t.Fatalf("widget did not refresh from background push: %+v", widgets)
	}
}
