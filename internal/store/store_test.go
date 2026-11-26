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
