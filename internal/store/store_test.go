package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestWidgetStateCommitAndRollback(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial := json.RawMessage(`{"group":"Work"}`)
	if err := db.Apply(ctx, "example.groups", Mutations{
		Initialize: true, State: initial, Create: []Widget{{ID: "work", Type: "group", Width: 280, Height: 360}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Apply(ctx, "example.groups", Mutations{
		State:  json.RawMessage(`{"group":"Personal"}`),
		Create: []Widget{{ID: "personal", Type: "group"}, {ID: "work", Type: "group"}},
	}); err == nil {
		t.Fatal("duplicate widget should cause the transaction to roll back")
	}
	if err := db.Move(ctx, "example.groups", Widget{ID: "work", Monitor: "DISPLAY2", X: -20, Y: 100, Width: 280, Height: 360, Placed: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPreference(ctx, "example.groups", false, []string{"paths.open"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "interface.language", "zh-CN"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.Snapshot(ctx, "example.groups")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Initialized || string(snapshot.State) != string(initial) || len(snapshot.Widgets) != 1 {
		t.Fatalf("unexpected persisted snapshot: %+v", snapshot)
	}
	if w := snapshot.Widgets[0]; w.ID != "work" || !w.Placed || w.Monitor != "DISPLAY2" || w.X != -20 {
		t.Fatalf("unexpected persisted layout: %+v", w)
	}
	other, err := reopened.Snapshot(ctx, "example.other")
	if err != nil || other.Initialized || len(other.Widgets) != 0 {
		t.Fatalf("plugin state is not isolated: %+v, %v", other, err)
	}
	preference, err := reopened.Preference(ctx, "example.groups")
	if err != nil || !preference.Exists || preference.Enabled || len(preference.Grants) != 1 || preference.Grants[0] != "paths.open" {
		t.Fatalf("unexpected persisted preferences: %+v, %v", preference, err)
	}
	language, err := reopened.Setting(ctx, "interface.language", "system")
	if err != nil || language != "zh-CN" {
		t.Fatalf("unexpected persisted language: %q, %v", language, err)
	}
}

func TestStoreMigratesV1ToV2AndPersistsWindowFlags(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Create a legacy v1 schema database directly.
	rawDB, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	const v1Schema = `
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE plugin_preferences (id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, grants TEXT NOT NULL);
CREATE TABLE plugin_state (id TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE TABLE widgets (
	plugin TEXT NOT NULL, id TEXT NOT NULL, type TEXT NOT NULL,
	monitor TEXT NOT NULL DEFAULT '', x INTEGER NOT NULL DEFAULT 0,
	y INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0, placed INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (plugin, id)
);
INSERT INTO plugin_state(id, state) VALUES('quailcrate.desktop-groups', '{}');
INSERT INTO widgets(plugin, id, type, x, y, width, height, placed) VALUES('quailcrate.desktop-groups', 'group-1', 'group', 12, 24, 396, 276, 1);
PRAGMA user_version = 1;`
	if _, err := rawDB.Exec(v1Schema); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	rawDB.Close()

	// Reopening via Store.Open must migrate v1 -> v2 seamlessly.
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open v1 db failed: %v", err)
	}
	snap, err := db.Snapshot(ctx, "quailcrate.desktop-groups")
	if err != nil || len(snap.Widgets) != 1 || snap.Widgets[0].X != 12 {
		db.Close()
		t.Fatalf("migrated snapshot = %+v, err = %v", snap, err)
	}
	if err := db.Move(ctx, "quailcrate.desktop-groups", Widget{
		ID: "group-1", Monitor: "DISPLAY1", X: 40, Y: 60, Width: 420, Height: 300, Placed: true,
		Collapsed: true, Locked: true, AlwaysOnTop: true, ExpandedHeight: 300, IconSize: 52,
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snap2, err := reopened.Snapshot(ctx, "quailcrate.desktop-groups")
	if err != nil || len(snap2.Widgets) != 1 {
		t.Fatalf("reopened snapshot = %+v, err = %v", snap2, err)
	}
	w := snap2.Widgets[0]
	if !w.Collapsed || !w.Locked || !w.AlwaysOnTop || w.ExpandedHeight != 300 || w.IconSize != 52 {
		t.Fatalf("window flags or icon size not persisted: %+v", w)
	}
}

func TestStoreMigratesV2ToV3AndPersistsIconSize(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	rawDB, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	const v2Schema = `
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE plugin_preferences (id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, grants TEXT NOT NULL);
CREATE TABLE plugin_state (id TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE TABLE widgets (
	plugin TEXT NOT NULL, id TEXT NOT NULL, type TEXT NOT NULL,
	monitor TEXT NOT NULL DEFAULT '', x INTEGER NOT NULL DEFAULT 0,
	y INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0, placed INTEGER NOT NULL DEFAULT 0,
	collapsed INTEGER NOT NULL DEFAULT 0, locked INTEGER NOT NULL DEFAULT 0,
	hover_expand INTEGER NOT NULL DEFAULT 0, expanded_height INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (plugin, id)
);
INSERT INTO plugin_state(id, state) VALUES('quailcrate.desktop-groups', '{}');
INSERT INTO widgets(plugin, id, type, x, y, width, height, placed, collapsed, hover_expand) VALUES('quailcrate.desktop-groups', 'group-2', 'group', 20, 30, 396, 276, 1, 1, 1);
PRAGMA user_version = 2;`
	if _, err := rawDB.Exec(v2Schema); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	rawDB.Close()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open v2 db failed: %v", err)
	}
	defer db.Close()
	snap, err := db.Snapshot(ctx, "quailcrate.desktop-groups")
	if err != nil || len(snap.Widgets) != 1 || !snap.Widgets[0].Collapsed || snap.Widgets[0].IconSize != 0 || snap.Widgets[0].AlwaysOnTop {
		t.Fatalf("v2->v3 snapshot = %+v, err = %v", snap, err)
	}
	w := snap.Widgets[0]
	w.IconSize = 28
	if err := db.Move(ctx, "quailcrate.desktop-groups", w); err != nil {
		t.Fatal(err)
	}
	snap2, err := db.Snapshot(ctx, "quailcrate.desktop-groups")
	if err != nil || len(snap2.Widgets) != 1 || snap2.Widgets[0].IconSize != 28 {
		t.Fatalf("updated v3 iconSize = %+v, err = %v", snap2, err)
	}
}

func TestStoreMigratesV3ToV4AndPersistsNativeTrust(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	rawDB, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	const v3Schema = `
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE plugin_preferences (id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, grants TEXT NOT NULL);
CREATE TABLE plugin_state (id TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE TABLE widgets (
	plugin TEXT NOT NULL, id TEXT NOT NULL, type TEXT NOT NULL,
	monitor TEXT NOT NULL DEFAULT '', x INTEGER NOT NULL DEFAULT 0,
	y INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0, placed INTEGER NOT NULL DEFAULT 0,
	collapsed INTEGER NOT NULL DEFAULT 0, locked INTEGER NOT NULL DEFAULT 0,
	hover_expand INTEGER NOT NULL DEFAULT 0, expanded_height INTEGER NOT NULL DEFAULT 0,
	icon_size INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (plugin, id)
);
PRAGMA user_version = 3;`
	if _, err := rawDB.Exec(v3Schema); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	rawDB.Close()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open v3 db failed: %v", err)
	}
	trust, err := db.NativeTrust(ctx, "example.native")
	if err != nil || trust.Exists {
		db.Close()
		t.Fatalf("initial NativeTrust = %+v, err = %v", trust, err)
	}
	if err := db.SetNativeTrust(ctx, "example.native", "abc123", 1700000000); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	trust, err = reopened.NativeTrust(ctx, "example.native")
	if err != nil || !trust.Exists || trust.SHA256 != "abc123" || trust.TrustedAt != 1700000000 {
		t.Fatalf("reopened NativeTrust = %+v, err = %v", trust, err)
	}
	if err := reopened.SetNativeTrust(ctx, "example.native", "def456", 1700000100); err != nil {
		t.Fatal(err)
	}
	trust, err = reopened.NativeTrust(ctx, "example.native")
	if err != nil || trust.SHA256 != "def456" || trust.TrustedAt != 1700000100 {
		t.Fatalf("updated NativeTrust = %+v, err = %v", trust, err)
	}
	if err := reopened.DeleteNativeTrust(ctx, "example.native"); err != nil {
		t.Fatal(err)
	}
	trust, err = reopened.NativeTrust(ctx, "example.native")
	if err != nil || trust.Exists {
		t.Fatalf("deleted NativeTrust = %+v, err = %v", trust, err)
	}
}

func TestStoreMigratesV5ToV6(t *testing.T) {
	dir := t.TempDir()
	rawDB, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	const v5Schema = `
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE plugin_preferences (id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, grants TEXT NOT NULL);
CREATE TABLE plugin_state (id TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE TABLE native_plugin_trust (id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, trusted_at INTEGER NOT NULL);
CREATE TABLE widgets (
	plugin TEXT NOT NULL, id TEXT NOT NULL, type TEXT NOT NULL,
	monitor TEXT NOT NULL DEFAULT '', x INTEGER NOT NULL DEFAULT 0,
	y INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0, placed INTEGER NOT NULL DEFAULT 0,
	collapsed INTEGER NOT NULL DEFAULT 0, locked INTEGER NOT NULL DEFAULT 0,
	always_on_top INTEGER NOT NULL DEFAULT 0, expanded_height INTEGER NOT NULL DEFAULT 0,
	icon_size INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (plugin, id)
);
INSERT INTO plugin_state(id, state) VALUES('example.native', '{"count":4}');
INSERT INTO native_plugin_trust(id, sha256, trusted_at) VALUES('example.native', 'abc123', 1700000000);
INSERT INTO widgets(plugin, id, type, x, placed, always_on_top) VALUES('example.native', 'main', 'group', 37, 1, 1);
PRAGMA user_version = 5;`
	if _, err := rawDB.Exec(v5Schema); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var version int
	if err := db.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("database version = %d, err = %v", version, err)
	}
	if installed, err := db.AllInstalledPlugins(ctx); err != nil || len(installed) != 0 {
		t.Fatalf("initial installed plugins = %+v, err = %v", installed, err)
	}
	if trust, err := db.NativeTrust(ctx, "example.native"); err != nil || !trust.Exists || trust.SHA256 != "abc123" {
		t.Fatalf("migrated native trust = %+v, err = %v", trust, err)
	}
	snap, err := db.Snapshot(ctx, "example.native")
	if err != nil || !snap.Initialized || string(snap.State) != `{"count":4}` || len(snap.Widgets) != 1 {
		t.Fatalf("migrated snapshot = %+v, err = %v", snap, err)
	}
	if widget := snap.Widgets[0]; widget.X != 37 || !widget.Placed || !widget.AlwaysOnTop {
		t.Fatalf("migrated widget = %+v", widget)
	}
}

func TestInstalledPluginMetadataAndFreshAuthorization(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const id = "example.installed"
	if absent, err := db.InstalledPlugin(ctx, id); err != nil || absent.ID != "" {
		t.Fatalf("absent metadata = %+v, err = %v", absent, err)
	}
	first := InstalledPlugin{
		ID: id, SourceURL: "https://github.com/example/plugin", RepoOwner: "example", RepoName: "plugin",
		RefOrTag: "v1.0.0", AssetName: "plugin.qcplugin.zip", InstalledVersion: "1.0.0", InstalledAt: 1700000000,
	}
	if err := db.SetInstalledPlugin(ctx, first); err != nil {
		t.Fatal(err)
	}
	if actual, err := db.InstalledPlugin(ctx, id); err != nil || actual != first {
		t.Fatalf("installed metadata = %+v, err = %v", actual, err)
	}
	if err := db.SetPreference(ctx, id, true, []string{"paths.open"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNativeTrust(ctx, id, "abc123", 1700000000); err != nil {
		t.Fatal(err)
	}
	const directoryGrants = `[{"id":"shared-folder"}]`
	if err := db.SetSetting(ctx, DirectoryGrantsSettingKey(id), directoryGrants); err != nil {
		t.Fatal(err)
	}
	if err := db.Apply(ctx, id, Mutations{State: json.RawMessage(`{"count":4}`), Create: []Widget{{ID: "main", Type: "group"}}}); err != nil {
		t.Fatal(err)
	}
	second := InstalledPlugin{
		ID: id, SourceURL: "https://github.com/example2/plugin2/tree/main", RepoOwner: "example2", RepoName: "plugin2",
		RefOrTag: "main", InstalledVersion: "2.0.0", InstalledAt: 1700000100,
	}
	if err := db.SetInstalledPlugin(ctx, second); err != nil {
		t.Fatal(err)
	}
	if list, err := db.AllInstalledPlugins(ctx); err != nil || len(list) != 1 || list[id] != second {
		t.Fatalf("updated metadata = %+v, err = %v", list, err)
	}
	if pref, err := db.Preference(ctx, id); err != nil || !pref.Exists || !pref.Enabled {
		t.Fatalf("upgrade preferences = %+v, err = %v", pref, err)
	}
	if trust, err := db.NativeTrust(ctx, id); err != nil || !trust.Exists || trust.SHA256 != "abc123" {
		t.Fatalf("upgrade native trust = %+v, err = %v", trust, err)
	}
	if grants, err := db.Setting(ctx, DirectoryGrantsSettingKey(id), ""); err != nil || grants != directoryGrants {
		t.Fatalf("upgrade directory grants = %q, err = %v", grants, err)
	}
	if err := db.SetNewInstalledPlugin(ctx, first); err != nil {
		t.Fatal(err)
	}
	if actual, err := db.InstalledPlugin(ctx, id); err != nil || actual != first {
		t.Fatalf("fresh metadata = %+v, err = %v", actual, err)
	}
	if pref, err := db.Preference(ctx, id); err != nil || pref.Exists {
		t.Fatalf("fresh preferences = %+v, err = %v", pref, err)
	}
	if trust, err := db.NativeTrust(ctx, id); err != nil || trust.Exists {
		t.Fatalf("fresh native trust = %+v, err = %v", trust, err)
	}
	if grants, err := db.Setting(ctx, DirectoryGrantsSettingKey(id), ""); err != nil || grants != "" {
		t.Fatalf("fresh directory grants = %q, err = %v", grants, err)
	}
	if snap, err := db.Snapshot(ctx, id); err != nil || !snap.Initialized || string(snap.State) != `{"count":4}` || len(snap.Widgets) != 1 {
		t.Fatalf("fresh snapshot = %+v, err = %v", snap, err)
	}
}

func TestPurgePluginIsAtomicAndIsolated(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const id, other = "example.installed", "example.other"
	const directoryGrants = `[{"id":"shared-folder"}]`
	for _, plugin := range []string{id, other} {
		if err := db.SetInstalledPlugin(ctx, InstalledPlugin{ID: plugin, SourceURL: "https://github.com/example/plugin", InstalledVersion: "1.0.0"}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetPreference(ctx, plugin, true, []string{"paths.open"}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetNativeTrust(ctx, plugin, "abc123", 1700000000); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSetting(ctx, DirectoryGrantsSettingKey(plugin), directoryGrants); err != nil {
			t.Fatal(err)
		}
		if err := db.Apply(ctx, plugin, Mutations{State: json.RawMessage(`{"count":4}`), Create: []Widget{{ID: "main", Type: "group"}}}); err != nil {
			t.Fatal(err)
		}
	}
	assertPresence := func(plugin string, present bool) {
		t.Helper()
		if meta, err := db.InstalledPlugin(ctx, plugin); err != nil || (meta.ID == plugin) != present {
			t.Fatalf("%s metadata = %+v, err = %v", plugin, meta, err)
		}
		if pref, err := db.Preference(ctx, plugin); err != nil || pref.Exists != present {
			t.Fatalf("%s preferences = %+v, err = %v", plugin, pref, err)
		}
		if trust, err := db.NativeTrust(ctx, plugin); err != nil || trust.Exists != present {
			t.Fatalf("%s native trust = %+v, err = %v", plugin, trust, err)
		}
		if grants, err := db.Setting(ctx, DirectoryGrantsSettingKey(plugin), ""); err != nil || (grants == directoryGrants) != present {
			t.Fatalf("%s directory grants = %q, err = %v", plugin, grants, err)
		}
		if snap, err := db.Snapshot(ctx, plugin); err != nil || snap.Initialized != present || (len(snap.Widgets) > 0) != present {
			t.Fatalf("%s snapshot = %+v, err = %v", plugin, snap, err)
		}
	}
	if _, err := db.db.Exec(`CREATE TRIGGER fail_widget_delete BEFORE DELETE ON widgets
		BEGIN SELECT RAISE(ABORT, 'widget removal failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgePlugin(ctx, id); err == nil {
		t.Fatal("expected purge to roll back when widget removal fails")
	}
	assertPresence(id, true)
	if _, err := db.db.Exec("DROP TRIGGER fail_widget_delete"); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgePlugin(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertPresence(id, false)
	assertPresence(other, true)
}
