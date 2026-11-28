package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quailcrate/internal/plugininstall"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
)

func newInstallHost(t *testing.T) *Host {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(root, "bundled"), filepath.Join(root, "user-plugins"), nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	t.Cleanup(func() { h.stopAll(); h.cancel(); db.Close() })
	return h
}

func installFixture(t *testing.T, dir string, m v1.Manifest, entry []byte) plugins.Package {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{"manifest.json": body, m.Entry: entry} {
		if err = os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := plugins.InspectUserPackage(dir)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func installStage(pkg plugins.Package) plugininstall.StagedPlugin {
	return plugininstall.StagedPlugin{
		Package: pkg, Source: plugininstall.SourceSpec{URL: "https://github.com/example/test", Owner: "example", Repo: "test"},
		RefOrTag: "v" + pkg.Manifest.Version, AssetName: "test.qcplugin.zip",
	}
}

func installInfo(h *Host, id string) winui.Plugin {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.records[id].info
}

func TestGitHubInstallUpgradeAndUninstallLua(t *testing.T) {
	h := newInstallHost(t)
	const id = "example.github-counter"
	m := serviceManifest(id, "", "")
	m.Homepage = "https://example.com/plugin"
	m.Contributes = []string{"commands"}
	const script = `return function(req)
		local data = {}
		if req.op == "init" then data.commands = {{id = "run", title = "Run"}} end
		if req.op == "command" then data.state = { kept = req.payload.state.kept, handler = %q } end
		return {requestId = req.requestId, ok = true, data = data}
	end`
	stage := func(m v1.Manifest) plugininstall.StagedPlugin {
		return installStage(installFixture(t, filepath.Join(t.TempDir(), "stage"), m, []byte(fmt.Sprintf(script, m.Version))))
	}
	// A manually deleted earlier copy may still have an enabled preference.
	if err := h.db.SetPreference(t.Context(), id, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.commitPluginInstall(stage(m)); err != nil {
		t.Fatal(err)
	}
	info := installInfo(h, id)
	if info.State != "disabled" || !info.CanUninstall || info.SourceRepo != "example/test" || info.Version != "1.0.0" || info.HomepageURL != m.Homepage {
		t.Fatalf("fresh install settings = %+v", info)
	}
	if pref, err := h.db.Preference(t.Context(), id); err != nil || pref.Exists {
		t.Fatalf("fresh install preference = %+v, %v", pref, err)
	}
	var confirmed string
	h.confirmDialog = func(title, body string, done func(bool)) {
		confirmed = title + " " + body
		done(true)
	}
	h.toggle(id, true)
	if generation := h.manager.Generation(id); generation == 0 || !strings.Contains(confirmed, "Enable") {
		t.Fatalf("explicit first enable generation = %d, prompt = %q", generation, confirmed)
	}
	oldGeneration := h.manager.Generation(id)
	changes := store.Mutations{State: json.RawMessage(`{"kept":42}`), Create: []store.Widget{{ID: "counter", Type: "counter", Width: 210, Height: 90}}}
	if err := h.db.Apply(t.Context(), id, changes); err != nil {
		t.Fatal(err)
	}
	private := h.pluginDataDir(id)
	dataDirs := []string{private, h.files.PrivateDataDir(id), h.network.PluginSessionDir(id)}
	for _, dir := range dataDirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m.Version = "1.1.0"
	if err := h.commitPluginInstall(stage(m)); err != nil {
		t.Fatal(err)
	}
	newGeneration := h.manager.Generation(id)
	if newGeneration <= oldGeneration || installInfo(h, id).State != "running" {
		t.Fatalf("running plugin was not reloaded: old=%d new=%d info=%+v", oldGeneration, newGeneration, installInfo(h, id))
	}
	if err := h.invoke(id, oldGeneration, "command", v1.Payload{CommandID: "run"}); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("old plugin generation accepted a command: %v", err)
	}
	if err := h.invoke(id, newGeneration, "command", v1.Payload{CommandID: "run"}); err != nil {
		t.Fatal(err)
	}
	snap, err := h.db.Snapshot(t.Context(), id)
	var state struct {
		Kept    int
		Handler string
	}
	if err != nil || json.Unmarshal(snap.State, &state) != nil || state.Kept != 42 || state.Handler != "1.1.0" || len(snap.Widgets) != 1 || snap.Widgets[0].Width != 210 {
		t.Fatalf("upgrade lost data or used the old script: %+v, state = %s, err = %v", snap, snap.State, err)
	}
	for _, dir := range dataDirs {
		if data, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(data) != "private" {
			t.Fatalf("upgrade lost private data in %s: %s, %v", dir, data, err)
		}
	}
	m.Version = "1.2.0"
	missing := stage(m)
	missing.Package.Dir = filepath.Join(t.TempDir(), "missing-stage")
	if err := h.commitPluginInstall(missing); err == nil || installInfo(h, id).Version != "1.1.0" || !h.manager.Active(id) {
		t.Fatalf("failed upgrade did not restore old package and runtime: error = %v, info = %+v", err, installInfo(h, id))
	}
	newGeneration = h.manager.Generation(id)
	h.confirmDialog = func(title, body string, done func(bool)) {
		confirmed = title + " " + body
		done(false)
	}
	m.Version = "1.0.0"
	if err := h.commitPluginInstall(stage(m)); !errors.Is(err, errPluginDeclined) || !strings.Contains(confirmed, "v1.1.0 with v1.0.0") || h.manager.Generation(id) != newGeneration {
		t.Fatalf("declined downgrade: err = %v, prompt = %q, generation = %d", err, confirmed, h.manager.Generation(id))
	}
	if meta, err := h.db.InstalledPlugin(t.Context(), id); err != nil || meta.InstalledVersion != "1.1.0" || meta.RefOrTag != "v1.1.0" {
		t.Fatalf("source was not updated: %+v, %v", meta, err)
	}
	h.confirmDialog = func(title, body string, done func(bool)) {
		confirmed = title + " " + body
		done(true)
	}
	h.UninstallPlugin(id)
	if h.pluginInstall.failed || !strings.Contains(confirmed, "local data and state") || h.manager.Active(id) || h.records[id] != nil {
		t.Fatalf("uninstall did not finish: status = %+v, prompt = %q", h.pluginInstall, confirmed)
	}
	for _, dir := range append(dataDirs, filepath.Join(h.userDir, id)) {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("uninstall left %s: %v", dir, err)
		}
	}
	if snap, err := h.db.Snapshot(t.Context(), id); err != nil || snap.Initialized || len(snap.Widgets) != 0 {
		t.Fatalf("uninstall left plugin state or widgets: %+v, %v", snap, err)
	}
	if pref, err := h.db.Preference(t.Context(), id); err != nil || pref.Exists {
		t.Fatalf("uninstall left plugin preferences: %+v, %v", pref, err)
	}
	if meta, err := h.db.InstalledPlugin(t.Context(), id); err != nil || meta.ID != "" {
		t.Fatalf("uninstall left plugin install source: %+v, %v", meta, err)
	}
}

func TestGitHubLuaUpgradeOnlyPromptsForNewGrants(t *testing.T) {
	h := newInstallHost(t)
	m := serviceManifest("example.github-grants", "", "")
	const script = `return function(req) return {requestId = req.requestId, ok = true} end`
	stage := func() plugininstall.StagedPlugin {
		return installStage(installFixture(t, filepath.Join(t.TempDir(), "stage"), m, []byte(script)))
	}
	m.Permissions = []string{"network.http"}
	if err := h.commitPluginInstall(stage()); err != nil {
		t.Fatal(err)
	}
	h.confirmDialog = func(_, _ string, done func(bool)) { done(true) }
	h.toggle(m.ID, true)
	m.Version = "1.1.0"
	m.Permissions = nil
	if err := h.commitPluginInstall(stage()); err != nil {
		t.Fatal(err)
	}
	if info := installInfo(h, m.ID); info.State != "running" || len(info.GrantedPermissions) != 0 {
		t.Fatalf("removed grants should not block a previously enabled plugin: %+v", info)
	}
	m.Version = "1.2.0"
	m.Permissions = []string{"network.http"}
	if err := h.commitPluginInstall(stage()); err != nil {
		t.Fatal(err)
	}
	if info := installInfo(h, m.ID); info.State != "authorization" || h.manager.Active(m.ID) || len(info.GrantedPermissions) != 0 {
		t.Fatalf("new grant was not held for authorization: %+v", info)
	}
}

func TestThirdPartyNativeGitHubUpgradeRequiresReconfirmation(t *testing.T) {
	h := newInstallHost(t)
	const id = "example.github-native"
	m := v1.Manifest{
		ID: id, Name: "Native install test", Version: "1.0.0", Runtime: "dll", Entry: "probe.dll", Platform: "windows/amd64",
		SchemaVersion: 2, HostAPIVersion: 2, ABIVersion: 2, Contributes: []string{"commands", "widgets", "appTabs"},
	}
	entry := []byte("native hash fixture")
	probe := os.Getenv("QUAILCRATE_NATIVE_PROBE")
	hasRunner := probe != "" && os.Getenv("QUAILCRATE_NATIVE_RUNNER") != ""
	if hasRunner {
		var err error
		entry, err = os.ReadFile(probe)
		if err != nil {
			t.Fatal(err)
		}
	}
	old := installFixture(t, filepath.Join(t.TempDir(), "first"), m, entry)
	if err := h.commitPluginInstall(installStage(old)); err != nil {
		t.Fatal(err)
	}
	if info := installInfo(h, id); info.State != "disabled" || !info.FullSystemAccess {
		t.Fatalf("fresh native install = %+v", info)
	}
	if err := h.db.SetPreference(t.Context(), id, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SetNativeTrust(t.Context(), id, old.NativeDigest, 1); err != nil {
		t.Fatal(err)
	}
	if hasRunner {
		h.scan()
		if info := installInfo(h, id); info.State != "running" || !h.manager.Active(id) {
			t.Fatalf("native fixture did not start: %+v", info)
		}
	}
	m.Version = "2.0.0"
	changed := installFixture(t, filepath.Join(t.TempDir(), "second"), m, entry)
	if err := os.WriteFile(filepath.Join(changed.Dir, "extra.dll"), []byte("changed binary package"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := plugins.InspectUserPackage(changed.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.commitPluginInstall(installStage(changed)); err != nil {
		t.Fatal(err)
	}
	info := installInfo(h, id)
	if info.State != "authorization" || info.Enabled || h.manager.Active(id) || changed.NativeDigest == old.NativeDigest {
		t.Fatalf("new native binary bypassed trust gate: %+v", info)
	}
	if trust, err := h.db.NativeTrust(t.Context(), id); err != nil || trust.SHA256 != old.NativeDigest {
		t.Fatalf("install modified native trust: %+v, %v", trust, err)
	}
	var warning string
	h.confirmDialog = func(_, body string, done func(bool)) { warning = body; done(false) }
	h.toggle(id, true)
	if !strings.Contains(warning, "binary files have changed") || h.manager.Active(id) {
		t.Fatalf("new binary did not prompt again: %q", warning)
	}
}

func TestGitHubUpgradeAndUninstallManualPackage(t *testing.T) {
	h := newInstallHost(t)
	m := serviceManifest("example.manual", "", "")
	const script = `return function(req) return {requestId = req.requestId, ok = true} end`
	first := installFixture(t, filepath.Join(h.userDir, "downloaded-folder"), m, []byte(script))
	h.scan()
	h.confirmDialog = func(_, _ string, done func(bool)) { done(true) }
	h.UninstallPlugin(m.ID)
	if _, err := os.Stat(first.Dir); !errors.Is(err, os.ErrNotExist) || h.pluginInstall.failed {
		t.Fatalf("manual uninstall failed = %v, status = %+v", err, h.pluginInstall)
	}
	first = installFixture(t, filepath.Join(h.userDir, "downloaded-folder"), m, []byte(script))
	m.Version = "1.0.1"
	newPkg := installFixture(t, filepath.Join(t.TempDir(), "new"), m, []byte(script))
	if err := h.commitPluginInstall(installStage(newPkg)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manual package still present after upgrade: %v", err)
	}
	if info := installInfo(h, m.ID); info.Version != m.Version || info.SourceRepo != "example/test" {
		t.Fatalf("manual package upgrade not reflected: %+v", info)
	}
}

func TestGitHubCanRepairIncompleteManualPackage(t *testing.T) {
	h := newInstallHost(t)
	m := serviceManifest("example.incomplete", "", "")
	const script = `return function(req) return {requestId = req.requestId, ok = true} end`
	broken := installFixture(t, filepath.Join(h.userDir, m.ID), m, []byte(script))
	if err := os.Remove(broken.Entry); err != nil {
		t.Fatal(err)
	}
	h.scan()
	if len(h.issues) != 1 || h.issues[0].ID != m.ID || !h.issues[0].CanUninstall {
		t.Fatalf("incomplete plugin has no uninstall action: %+v", h.issues)
	}
	stage := installStage(installFixture(t, filepath.Join(t.TempDir(), "repair"), m, []byte(script)))
	if err := h.commitPluginInstall(stage); err != nil {
		t.Fatal(err)
	}
	if info := installInfo(h, m.ID); info.State != "disabled" || info.Version != m.Version {
		t.Fatalf("repaired plugin not found: %+v", info)
	}
}

func TestGitHubInstallRejectsBundledPluginID(t *testing.T) {
	h := newInstallHost(t)
	m := serviceManifest("example.bundled", "", "")
	const script = `return function(req) return {requestId = req.requestId, ok = true} end`
	installFixture(t, filepath.Join(h.bundledDir, "bundled-folder"), m, []byte(script))
	stage := installStage(installFixture(t, filepath.Join(t.TempDir(), "stage"), m, []byte(script)))
	if err := h.commitPluginInstall(stage); !errors.Is(err, errBundledPlugin) {
		t.Fatalf("bundled collision = %v", err)
	}
}

func TestPluginUninstallRechecksPackageAfterConfirmation(t *testing.T) {
	h := newInstallHost(t)
	m := serviceManifest("example.confirmed", "", "")
	const script = `return function(req) return {requestId = req.requestId, ok = true} end`
	installed := installFixture(t, filepath.Join(h.userDir, m.ID), m, []byte(script))
	h.scan()
	h.confirmDialog = func(_, _ string, done func(bool)) {
		m.ID = "example.replaced"
		installFixture(t, installed.Dir, m, []byte(script))
		done(true)
	}
	if _, err := h.uninstallPlugin(installed.Manifest.ID); !errors.Is(err, errPluginChanged) {
		t.Fatalf("changed plugin folder = %v", err)
	}
	if _, err := os.Stat(installed.Entry); err != nil {
		t.Fatalf("replacement package was removed: %v", err)
	}
}
