package host

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
)

func TestInvokeListedCommand(t *testing.T) {
	const id = "test.commands"
	const script = `
return function(request)
    local data = {}
    if request.op == "init" then
        data.commands = {
            {id="run", title="Run", location="desktop-edit"},
            {id="fail", title="Fail", location="settings"},
            {id="search", title="Search", location="search"}
        }
    elseif request.op == "command" then
        if request.payload.commandId == "fail" then
            return {requestId=request.requestId, ok=false, error={code="failed", message="test failure"}}
        end
        local args = request.payload.args or {}
        data.state = { last=args.value or request.payload.commandId }
    end
    return {requestId=request.requestId, ok=true, data=data}
end`
	dir := t.TempDir()
	entry := filepath.Join(dir, "plugin.lua")
	if err := os.WriteFile(entry, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := New(db, slog.New(slog.DiscardHandler), "", "", nil)
	t.Cleanup(h.cancel)
	h.ui = winui.New(winui.Callbacks{})
	rec := &record{
		info: winui.Plugin{ID: id, Name: "Commands"},
		pkg: plugins.Package{Dir: dir, Entry: entry, Manifest: v1.Manifest{
			ID: id, Name: "Commands", Version: "1.0.0", SchemaVersion: v1.SchemaVersion,
			HostAPIVersion: v1.Version, Runtime: "lua", Entry: "plugin.lua", Contributes: []string{"commands"},
		}},
	}
	h.records[id] = rec
	h.enable(rec, nil)
	t.Cleanup(func() { h.disable(rec) })

	catalog := h.AppCatalog()
	cmd, ok := catalog.ResolveCommand(id, "run")
	if !ok {
		t.Fatalf("missing registered command, status = %+v", rec.info)
	}
	if err := h.InvokeListedCommand(id, cmd.ID, cmd.Generation); err != nil {
		t.Fatal(err)
	}
	assertState := func(want string) {
		t.Helper()
		snapshot, err := db.Snapshot(h.ctx, id)
		if err != nil || string(snapshot.State) != want {
			t.Fatalf("persisted state = %s, err = %v, want %s", snapshot.State, err, want)
		}
	}
	assertState(`{"last":"run"}`)
	var failure *v1.APIError
	if err := h.InvokeListedCommand(id, "fail", cmd.Generation); !errors.As(err, &failure) || failure.Message != "test failure" {
		t.Fatalf("plugin error = %v", err)
	}
	h.command(id, "search", json.RawMessage(`{"value":"search result"}`))
	assertState(`{"last":"search result"}`)

	h.disable(rec)
	if err := h.InvokeListedCommand(id, cmd.ID, cmd.Generation); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("disabled command = %v", err)
	}
	h.enable(rec, nil)
	current, ok := h.AppCatalog().ResolveCommand(id, cmd.ID)
	if !ok || current.Generation == cmd.Generation {
		t.Fatalf("replacement registration = %+v", current)
	}
	for _, test := range []struct {
		command    string
		generation uint64
	}{
		{cmd.ID, cmd.Generation},
		{cmd.ID, 0},
		{"unknown", current.Generation},
		{"search", current.Generation},
	} {
		if err := h.InvokeListedCommand(id, test.command, test.generation); !errors.Is(err, ErrInvocationUnavailable) {
			t.Errorf("invoke %q at %d = %v", test.command, test.generation, err)
		}
	}
	assertState(`{"last":"search result"}`)
}

func TestLauncherSearchIncludesAndExecutesPublicCommands(t *testing.T) {
	const id = "test.cmdsearch"
	const script = `
return function(request)
    local data = {}
    if request.op == "init" then
        data.commands = {
            {id="set_wall", title="设置系统壁纸", location="settings"},
            {id="new_group", title="新建桌面分组", location="desktop-edit"},
            {id="internal_search", title="内部搜索指令", location="search"}
        }
    elseif request.op == "command" then
        data.state = { ran=request.payload.commandId }
    end
    return {requestId=request.requestId, ok=true, data=data}
end`
	dir := t.TempDir()
	entry := filepath.Join(dir, "plugin.lua")
	if err := os.WriteFile(entry, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := New(db, slog.New(slog.DiscardHandler), "", "", nil)
	t.Cleanup(h.cancel)
	h.ui = winui.New(winui.Callbacks{})
	h.indexReady = true
	rec := &record{
		info: winui.Plugin{ID: id, Name: "壁纸工具", Icon: "image"},
		pkg: plugins.Package{Dir: dir, Entry: entry, Manifest: v1.Manifest{
			ID: id, Name: "壁纸工具", Icon: "image", Version: "1.0.0", SchemaVersion: v1.SchemaVersion,
			HostAPIVersion: v1.Version, Runtime: "lua", Entry: "plugin.lua", Contributes: []string{"commands"},
		}},
	}
	h.records[id] = rec
	h.enable(rec, nil)
	t.Cleanup(func() { h.disable(rec) })

	run := func(q string) []winui.Result {
		h.mu.Lock()
		h.searchID++
		rev := h.searchID
		h.mu.Unlock()
		h.runSearch(h.ctx, rev, q)
		h.mu.Lock()
		defer h.mu.Unlock()
		out := make([]winui.Result, len(h.visibleResults))
		copy(out, h.visibleResults)
		return out
	}

	// Prefix "/" lists all public commands (and excludes location="search").
	all := run("/")
	if len(all) != 2 {
		t.Fatalf("prefix '/' returned %d commands (%+v), want 2", len(all), all)
	}
	// Prefix ">" also lists all public commands.
	if got := run(">"); len(got) != 2 {
		t.Fatalf("prefix '>' returned %d commands, want 2", len(got))
	}
	// Prefix with filter narrows to matching command only.
	filtered := run("/分组")
	if len(filtered) != 1 || filtered[0].CommandID != "new_group" {
		t.Fatalf("prefix '/分组' = %+v, want new_group", filtered)
	}
	// Mixed search (no prefix) finds public command directly by keyword or plugin name.
	mixed := run("壁纸")
	if len(mixed) == 0 || mixed[0].CommandID != "set_wall" || mixed[0].Icon != "image" {
		t.Fatalf("mixed search '壁纸' = %+v, want set_wall with icon image", mixed)
	}
	// Activating the search result invokes the plugin command and persists its state.
	h.activateResult(mixed[0], nil)
	snapshot, err := db.Snapshot(h.ctx, id)
	if err != nil || string(snapshot.State) != `{"ran":"set_wall"}` {
		t.Fatalf("persisted state after launcher command = %s, err = %v", snapshot.State, err)
	}
}
