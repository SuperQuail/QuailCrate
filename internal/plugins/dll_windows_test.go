package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"quailcrate/internal/store"
	v1 "quailcrate/pluginapi/v1"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == NativeWorkerArgument {
		if RunNativeWorker() != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	nativeExecutable = func() (string, error) {
		if exe := os.Getenv("QUAILCRATE_NATIVE_RUNNER"); exe != "" {
			return exe, nil
		}
		return os.Executable()
	}
	os.Exit(m.Run())
}

func TestNativeRuntimeTrustedReleaseFixture(t *testing.T) {
	module := os.Getenv("QUAILCRATE_NATIVE_PROBE")
	if module == "" {
		t.Skip("build native integration fixtures with scripts/build.ps1 -TestNative")
	}
	manifest := v1.Manifest{
		ID: "quailcrate.native-probe", Name: "Native Probe Fixture", Version: "0.1.0", Runtime: "dll", Entry: "native-probe.dll",
		SchemaVersion: 2, HostAPIVersion: 2, ABIVersion: v1.ABIVersionV2, Platform: "windows/amd64",
		Contributes: []string{"commands", "widgets", "appTabs"},
	}
	root := t.TempDir()
	directory := filepath.Join(root, "native-probe")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifestJSON, 0600); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(module)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, manifest.Entry), image, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(image)
	manager := NewManager()
	ctx := context.Background()
	trusted := Discover(root, "", map[string]string{manifest.ID: hex.EncodeToString(digest[:])})
	if len(trusted) != 1 || trusted[0].Err != nil {
		t.Fatalf("invalid trusted fixture %#v", trusted)
	}
	init, err := manager.Enable(ctx, trusted[0], nil, v1.Payload{Locale: "en-US"})
	if err != nil || len(init.Data.Commands) != 1 || init.Data.Commands[0].ID != "push" {
		t.Fatalf("DLL init %#v, error %v", init, err)
	}
	t.Cleanup(func() { manager.Disable(ctx, manifest.ID) })
	result, err := manager.Call(ctx, manifest.ID, "widget_state", v1.Payload{WidgetID: "live", WidgetType: "live", Locale: "en-US"})
	if err != nil || result.Data.View == nil || result.Data.View.Title != "Live 0" {
		t.Fatalf("DLL widget_state %#v, error %v", result, err)
	}
	if err := manager.Disable(ctx, manifest.ID); err != nil {
		t.Fatal(err)
	}
	modified := Discover(root, "", map[string]string{manifest.ID: strings.Repeat("0", 64)})
	if _, err := manager.Enable(ctx, modified[0], nil, v1.Payload{}); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("unverified DLL reported %v", err)
	}
}

func TestNativeWorkerFullPermissionsAndIsolation(t *testing.T) {
	module := os.Getenv("QUAILCRATE_NATIVE_PROBE")
	if module == "" {
		t.Skip("build the Windows integration fixtures with scripts/build.ps1 -TestNative")
	}
	exe := os.Getenv("QUAILCRATE_NATIVE_RUNNER")
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Copy the probe (and companion DLL when present) into an isolated temp plugin directory
	// so we also verify in-place companion resolution and clean file-lock release on shutdown.
	pluginDir := filepath.Join(t.TempDir(), "probe-plugin")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	entryName := filepath.Base(module)
	rawProbe, err := os.ReadFile(module)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, entryName), rawProbe, 0600); err != nil {
		t.Fatal(err)
	}
	companionName := ""
	if companion := os.Getenv("QUAILCRATE_NATIVE_COMPANION"); companion != "" {
		rawComp, err := os.ReadFile(companion)
		if err != nil {
			t.Fatal(err)
		}
		companionName = filepath.Base(companion)
		if err := os.WriteFile(filepath.Join(pluginDir, companionName), rawComp, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pluginDataDir := filepath.Join(t.TempDir(), "plugin-data", "test.external-native")
	start := func() *nativeProcess {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		p, err := openNativeProcess(ctx, Package{
			Dir:     pluginDir,
			DataDir: pluginDataDir,
			Manifest: v1.Manifest{
				ID: "test.external-native", Entry: entryName,
			},
		}, exe)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := p.Close(); err != nil {
				t.Errorf("clean up native worker: %v", err)
			}
		})
		return p
	}
	worker := start()
	dataDir := t.TempDir()
	db, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	const saved = `{"month":"2026-11"}`
	const provider = "test.external-native"
	if err := db.Apply(context.Background(), provider, store.Mutations{Initialize: true, State: json.RawMessage(saved)}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPreference(context.Background(), provider, true, []string{"apps.read"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(t.TempDir(), "user-file.txt")
	if err := os.WriteFile(userFile, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 8*time.Second)
	defer stop()

	// 1. Full file read & write access under current user permissions.
	for _, request := range []string{"read:" + userFile, "writ:" + userFile} {
		result, err := worker.Call(ctx, []byte(request))
		if err != nil || string(result) != "0" {
			t.Fatalf("full-permission file probe(%q) returned %q, %v; want 0", request, result, err)
		}
	}

	// 2. Full network access (loopback TCP connection succeeds).
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	acceptedCh := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.AcceptTCP()
		if acceptErr == nil {
			conn.Close()
		}
		acceptedCh <- acceptErr
	}()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	code, err := worker.Call(ctx, []byte("loop:"+port))
	if err != nil || string(code) != "0" {
		t.Fatalf("full-permission loopback probe returned %q, %v; want 0", code, err)
	}
	if acceptErr := <-acceptedCh; acceptErr != nil {
		t.Fatalf("host listener did not receive connection from native DLL: %v", acceptErr)
	}

	// 3. Win32k / User32 API calls succeed (disableWin32k removed).
	if res, err := worker.Call(ctx, []byte("user32")); err != nil || string(res) != "0" {
		t.Fatalf("user32 probe returned %q, %v; want 0", res, err)
	}

	// 4. Child CLI process spawning succeeds (ActiveProcessLimit=1 removed).
	if res, err := worker.Call(ctx, []byte("child")); err != nil || string(res) != "0" {
		t.Fatalf("child process probe returned %q, %v; want 0", res, err)
	}

	// 5. Plugin directory and data directory environment variables are exposed.
	if got, err := worker.Call(ctx, []byte("env:QUAILCRATE_PLUGIN_DIR")); err != nil || !strings.EqualFold(string(got), pluginDir) {
		t.Fatalf("QUAILCRATE_PLUGIN_DIR = %q, %v; want %q", got, err, pluginDir)
	}
	if got, err := worker.Call(ctx, []byte("env:QUAILCRATE_PLUGIN_DATA_DIR")); err != nil || !strings.EqualFold(string(got), pluginDataDir) {
		t.Fatalf("QUAILCRATE_PLUGIN_DATA_DIR = %q, %v; want %q", got, err, pluginDataDir)
	}
	if got, err := worker.Call(ctx, []byte("env:QUAILCRATE_PLUGIN_ID")); err != nil || string(got) != "test.external-native" {
		t.Fatalf("QUAILCRATE_PLUGIN_ID = %q, %v", got, err)
	}

	// 6. Companion DLL in the same plugin folder loads cleanly.
	if companionName != "" {
		if got, err := worker.Call(ctx, []byte("comp:"+companionName)); err != nil || string(got) != "42" {
			t.Fatalf("companion DLL probe returned %q, %v; want 42", got, err)
		}
	}

	// 7. Asynchronous host push notification via quailcrate_bind_host from a background thread.
	const pushed = `{"type":"widget.refresh","widgetId":"live"}`
	if got, err := worker.Call(ctx, []byte("push:"+pushed)); err != nil || string(got) != "0" {
		t.Fatalf("push probe returned %q, %v; want 0", got, err)
	}
	select {
	case msg := <-worker.Notifications():
		if string(msg) != pushed {
			t.Fatalf("received async notification %q, want %q", msg, pushed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async notification from native DLL")
	}

	// 8. Process-level crash/hang isolation still holds between independent plugin workers.
	peer := start()
	caller, obsolete := context.WithTimeout(ctx, time.Second)
	instance := plugin{ctx: ctx, pkg: Package{Manifest: v1.Manifest{Runtime: "dll"}}}
	execution, release := instance.executionContext(caller)
	time.AfterFunc(20*time.Millisecond, obsolete)
	result, err := peer.Call(execution, []byte("brief"))
	release()
	if err != nil || string(result) != "0" || caller.Err() != context.Canceled {
		t.Fatalf("replaced query terminated healthy DLL: result %q, error %v, caller %v", result, err, caller.Err())
	}
	if _, err := peer.Call(ctx, []byte("alive")); err != nil {
		t.Fatalf("subsequent native query failed: %v", err)
	}
	deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := worker.Call(deadline, []byte("wait")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung DLL returned %v", err)
	}
	select {
	case <-worker.Done():
	default:
		t.Fatal("hung DLL process still running")
	}
	if _, err := peer.Call(ctx, []byte("alive")); err != nil {
		t.Fatalf("timeout stopped another plugin: %v", err)
	}
	if _, err := peer.Call(ctx, []byte("exit")); err == nil {
		t.Fatal("expected process exit error")
	}
	fresh := start()
	if _, err := fresh.Call(ctx, []byte("alive")); err != nil {
		t.Fatalf("could not restart DLL after crash: %v", err)
	}
	// Closing the worker releases OS locks on the in-place plugin DLL immediately.
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatalf("plugin DLL files remained locked after worker shutdown: %v", err)
	}

	db, err = store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := db.Snapshot(ctx, provider)
	if err != nil || !snapshot.Initialized || string(snapshot.State) != saved {
		t.Fatalf("state after worker crash: %#v, %v", snapshot, err)
	}
	pref, err := db.Preference(ctx, provider)
	if err != nil || !pref.Enabled || len(pref.Grants) != 1 || pref.Grants[0] != "apps.read" {
		t.Fatalf("permissions after worker crash: %#v, %v", pref, err)
	}
}

func TestNativeFrames(t *testing.T) {
	for _, kind := range []byte{nativeFrameRequest, nativeFrameResponse, nativeFrameNotify} {
		var encoded bytes.Buffer
		message := []byte(`{"op":"init"}`)
		if err := writeNativeFrame(&encoded, kind, message); err != nil {
			t.Fatalf("write kind %d: %v", kind, err)
		}
		gotKind, decoded, err := readNativeFrame(&encoded)
		if err != nil || gotKind != kind || !bytes.Equal(decoded, message) {
			t.Fatalf("round trip kind=%d: got kind=%d payload=%q err=%v", kind, gotKind, decoded, err)
		}
	}
	for _, header := range [][]byte{
		{0, 1, 0, 0, 0},            // invalid kind 0
		{9, 1, 0, 0, 0},            // invalid kind 9
		{1, 0, 0, 0, 0},            // zero length
		{1, 1, 0, 64, 0},           // > 4 MiB
		{2, 0xff, 0xff, 0xff, 0xff}, // huge length
	} {
		if _, _, err := readNativeFrame(bytes.NewReader(header)); err == nil {
			t.Fatalf("accepted invalid header %x", header)
		}
	}
	if _, _, err := readNativeFrame(bytes.NewReader([]byte{1, 2, 0, 0, 0, 'x'})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated payload returned %v", err)
	}
	if err := RunNativeWorker(); err == nil {
		t.Fatal("worker entry accepted missing entry path")
	}
}

func TestNativeDiscoveryTrustTiers(t *testing.T) {
	root := t.TempDir()
	manifest := fixtureManifest()
	manifest.Runtime, manifest.Entry = "dll", "entry.dll"
	manifest.Platform, manifest.ABIVersion = "windows/amd64", 1
	writeFixture(t, root, "external", manifest, "fixture")
	manifest.ID = "quailcrate.reserved"
	writeFixture(t, root, "reserved", manifest, "fixture")
	for _, directory := range []string{"external", "reserved"} {
		if err := os.Rename(filepath.Join(root, directory, "main.lua"), filepath.Join(root, directory, manifest.Entry)); err != nil {
			t.Fatal(err)
		}
	}
	local := Discover("", root, nil)
	if len(local) != 2 || local[0].Err != nil || local[0].dllHash != "" || local[0].EntrySHA256 == "" || local[0].NativeDigest != local[0].EntrySHA256 || local[1].Err == nil {
		t.Fatalf("unexpected third-party discovery: %#v", local)
	}
	// Adding a companion DLL changes NativeDigest while keeping EntrySHA256 identical.
	if err := os.WriteFile(filepath.Join(root, "external", "helper.dll"), []byte("companion-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	localWithCompanion := Discover("", root, nil)
	if localWithCompanion[0].EntrySHA256 != local[0].EntrySHA256 || localWithCompanion[0].NativeDigest == local[0].NativeDigest {
		t.Fatalf("companion DLL did not update NativeDigest: before=%+v after=%+v", local[0], localWithCompanion[0])
	}
	bundle := Discover(root, "", nil)
	if len(bundle) != 2 || bundle[0].Err == nil || bundle[1].Err == nil {
		t.Fatal("bundled DLLs require release digests")
	}
}

func TestNativeNotificationValidation(t *testing.T) {
	manifest := v1.Manifest{
		ID:        "example.native",
		Publishes: []string{"example.native.tick"},
	}
	init := v1.Data{
		Widgets: []v1.WidgetDefinition{{ID: "live", Title: "Live", Width: 200, Height: 120}},
		AppTabs: []v1.AppTabDefinition{{ID: "main", Title: "Main"}},
	}
	valid := []v1.Notification{
		{Type: "widget.refresh", WidgetID: "live"},
		{Type: "widget.refresh", State: json.RawMessage(`{"ok":true}`)},
		{Type: "page.notify", TabID: "main", Detail: json.RawMessage(`{"n":1}`)},
		{Type: "event.publish", Topic: "example.native.tick", Args: json.RawMessage(`{"n":1}`)},
		{Type: "state.set", State: json.RawMessage(`{"count":2}`)},
	}
	for _, note := range valid {
		if err := validateNotification(manifest, init, note); err != nil {
			t.Fatalf("rejected valid notification %+v: %v", note, err)
		}
	}
	invalid := []v1.Notification{
		{Type: "unknown"},
		{Type: "widget.refresh", WidgetID: "bad id!"},
		{Type: "page.notify", TabID: "missing"},
		{Type: "event.publish", Topic: "example.native.undeclared"},
		{Type: "state.set"},
		{Type: "state.set", State: json.RawMessage(`{invalid`)},
	}
	for _, note := range invalid {
		if err := validateNotification(manifest, init, note); err == nil {
			t.Fatalf("accepted invalid notification %+v", note)
		}
	}
}
