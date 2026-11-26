package host

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func TestTrayClickChecksActiveRegistration(t *testing.T) {
	item := v2.TrayItem{ID: "open", Title: "Find", Intent: "launcher.open"}
	entry := winui.TrayEntry{PluginID: "sample.ui", Generation: 2, Item: item}
	h := &Host{records: map[string]*record{"sample.ui": {generation: 2, registration: v1.Data{Tray: []v2.TrayItem{item}}}}}
	if !h.activateTray(entry) {
		t.Fatal("active tray item was rejected")
	}
	entry.Item.Intent = "desktop.toggleEdit"
	if h.activateTray(entry) {
		t.Fatal("changed intent was accepted")
	}
	entry.Item = item
	h.records[entry.PluginID].generation++
	if h.activateTray(entry) {
		t.Fatal("stale tray click reached a replacement plugin")
	}
	delete(h.records, entry.PluginID)
	if h.activateTray(entry) {
		t.Fatal("disabled plugin accepted tray click")
	}
}

func TestTrayCommandRunsOnOwningPlugin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plugins", "counter")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const manifest = `{"id":"example.tray","name":"Tray","version":"1.0.0","schemaVersion":2,"hostApiVersion":2,"uiVersion":2,"runtime":"lua","entry":"plugin.lua","contributes":["commands","tray"]}`
	const script = `return function(req)
		local data = {}
		if req.op == "init" then
			data.commands = {{id="run", title="Run"}}
			data.tray = {{id="go", title="Go", intent="command", command="run"}}
		elseif req.op == "command" and req.payload.commandId == "run" then
			data.state = {clicked=true}
		end
		return {requestId=req.requestId, ok=true, data=data}
	end`
	for name, contents := range map[string]string{"manifest.json": manifest, "plugin.lua": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := New(db, slog.New(slog.DiscardHandler), "", filepath.Dir(dir), nil)
	h.ui = winui.New(winui.Callbacks{})
	defer func() { h.stopAll(); h.cancel() }()
	const id = "example.tray"
	if err := db.SetPreference(h.ctx, id, true, nil); err != nil {
		t.Fatal(err)
	}
	h.scan()
	rec := h.records[id]
	if rec == nil || len(rec.registration.Tray) != 1 {
		t.Fatal("missing registered tray entry")
	}
	entry := winui.TrayEntry{PluginID: id, Generation: rec.generation, Item: rec.registration.Tray[0]}
	if !h.activateTray(entry) {
		t.Fatal("click rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := db.Snapshot(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if string(snapshot.State) == `{"clicked":true}` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not update plugin state: %s", snapshot.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.toggle(id, false)
	if h.activateTray(entry) {
		t.Fatal("disabled command remained active")
	}
}
