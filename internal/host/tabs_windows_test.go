package host

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
)

func newTabHost(t *testing.T) *Host {
	t.Helper()
	root := t.TempDir()
	const manifest = `{"schemaVersion":1,"id":"test.tabs","name":"Tabs","version":"1.0.0","runtime":"lua","entry":"plugin.lua","hostApiVersion":1,"contributes":["appTabs","commands"]}`
	const script = `
local transientTitle
return function(req)
    local p, data = req.payload, {}
    if req.op == "init" then
        local title = p.locale == "zh-CN" and "首页" or "Home"
        data.appTabs = {{id="main", title=title}, {id="other", title="Other"}}
        data.commands = {{id="rename", title="Rename", location="settings"}}
    elseif req.op == "command" then
        transientTitle = "Renamed"
    elseif req.op == "tab_state" then
        data.view = {title=transientTitle or p.tabId, controls={
            {type="button",id="prompt",text="Prompt"},
            {type="list",id="entries",items={{id="first",text="First"}}}
        }}
    elseif req.op == "event" then
        local e = p.event
        if e.type == "tab.action" and e.controlId == "prompt" then
            data.actions = {{type="ui.promptText", id="edit", title="Edit"}}
        elseif e.type == "action.result" and not e.canceled then
            assert(p.tabId == "main" and e.tabId == p.tabId and e.previous.controlId == "prompt")
            data.state = {value=e.value, count=((p.state or {}).count or 0) + 1}
        elseif e.type == "tab.action" then
            data.state = {value=e.itemId}
        end
    end
    return {requestId=req.requestId, ok=true, data=data}
end`
	dir := filepath.Join(root, "user", "tabs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"manifest.json": manifest, "plugin.lua": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(root, "bundled"), filepath.Dir(dir), nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	t.Cleanup(func() { h.stopAll(); h.cancel() })
	if err := db.SetPreference(h.ctx, "test.tabs", true, nil); err != nil {
		t.Fatal(err)
	}
	h.scan()
	return h
}

func openTestTab(t *testing.T, h *Host, id string, dialogs pluginapps.DialogHandler) pluginapps.Session {
	t.Helper()
	tab, ok := h.AppCatalog().ResolveTab("test.tabs", id)
	if !ok {
		t.Fatalf("missing tab %q", id)
	}
	session, err := h.OpenTab(tab.TabRef, dialogs)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestTabSessionsAndViewRevisions(t *testing.T) {
	h := newTabHost(t)
	session := openTestTab(t, h, "main", nil)
	if again := openTestTab(t, h, "main", nil); again != session {
		t.Fatal("opening the same tab created another session")
	}
	h.scan()
	if again := openTestTab(t, h, "main", nil); again != session {
		t.Fatal("rescan replaced a healthy instance")
	}
	page, err := h.LoadTab(session)
	if err != nil {
		t.Fatal(err)
	}
	page.View.Controls[1].Items[0].ID = "caller-injected"
	if err := h.ActOnTab(page.PageRef, "entries", "caller-injected"); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("detached view modified click validation: %v", err)
	}
	newPage, err := h.LoadTab(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ActOnTab(page.PageRef, "entries", "first"); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("old page accepted: %v", err)
	}
	if err := h.ActOnTab(newPage.PageRef, "entries", "first"); err != nil {
		t.Fatal(err)
	}
	if err := h.ActOnTab(newPage.PageRef, "entries", "first"); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("accepted a second click on an invalidated view: %v", err)
	}
	page, err = h.LoadTab(session)
	if err != nil {
		t.Fatal(err)
	}
	_, pending := h.ObserveApps()
	if err := h.InvokeListedCommand(session.PluginID, "rename", session.Generation); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pending:
	default:
		t.Fatal("runtime-only change outside the window did not notify the active tab")
	}
	if err := h.ActOnTab(page.PageRef, "entries", "first"); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("click accepted after a command changed the view: %v", err)
	}
	if page, err = h.LoadTab(session); err != nil || page.View.Title != "Renamed" {
		t.Fatalf("runtime-only change was lost: %q, %v", page.View.Title, err)
	}
	other := openTestTab(t, h, "other", nil)
	h.CloseTab(session)
	if _, err := h.LoadTab(other); err != nil {
		t.Fatalf("stale close affected current tab: %v", err)
	}
	h.CloseTab(other)
	reopened := openTestTab(t, h, "main", nil)
	if reopened == session {
		t.Fatal("reopened session reused old ID")
	}
	catalog, changed := h.ObserveApps()
	h.changeLanguage("zh-CN")
	localized, ok := h.AppCatalog().ResolveTab("test.tabs", "main")
	if !ok || localized.Title != "首页" || localized.Generation == reopened.Generation || catalog.Tabs[0].Title != "Home" {
		t.Fatalf("incorrect localized catalog: %+v", localized)
	}
	select {
	case <-changed:
	default:
		t.Fatal("language change did not notify the observer")
	}
	if _, err := h.OpenTab(reopened.TabRef, nil); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("old locale instance accepted: %v", err)
	}
	h.toggle("test.tabs", false)
	if tabs := h.AppCatalog().Tabs; len(tabs) != 0 {
		t.Fatalf("disabled plugin still present: %+v", tabs)
	}
}

func TestTabDialogCompletionKeepsOrigin(t *testing.T) {
	h := newTabHost(t)
	type pending struct {
		ctx      context.Context
		complete func(pluginapps.DialogResult)
	}
	dialogs := make(chan pending, 1)
	handler := func(ctx context.Context, action v1.Action, complete func(pluginapps.DialogResult)) {
		if action.Type == "ui.promptText" && action.ID == "edit" {
			dialogs <- pending{ctx, complete}
		}
	}
	session := openTestTab(t, h, "main", handler)
	prompt := func() pending {
		t.Helper()
		page, err := h.LoadTab(session)
		if err != nil {
			t.Fatal(err)
		}
		if err = h.ActOnTab(page.PageRef, "prompt", ""); err != nil {
			t.Fatal(err)
		}
		select {
		case d := <-dialogs:
			return d
		case <-time.After(3 * time.Second):
			t.Fatal("missing dialog")
			return pending{}
		}
	}
	d := prompt()
	_, updated := h.ObserveApps()
	d.complete(pluginapps.DialogResult{Value: "first"})
	d.complete(pluginapps.DialogResult{Value: "duplicate"})
	select {
	case <-updated:
	case <-time.After(3 * time.Second):
		t.Fatal("dialog result failed to update tab")
	}
	d = prompt()
	h.mu.Lock()
	stale := h.tabs[session.PluginID]
	h.mu.Unlock()
	h.CloseTab(session)
	reopened := openTestTab(t, h, "main", handler)
	if d.ctx.Err() == nil || session == reopened {
		t.Fatal("closed window still owns a live dialog")
	}
	d.complete(pluginapps.DialogResult{Value: "late"})
	// Exercise the delayed result path synchronously as well, so the check
	// doesn't depend on when the asynchronous UI completion is scheduled.
	h.resumeTab(stale, v1.Event{
		Type: "action.result", TabID: "main", ActionID: "edit", Value: "late",
		Previous: &v1.Event{Type: "tab.action", TabID: "main", ControlID: "prompt"},
	})
	snapshot, err := h.db.Snapshot(h.ctx, "test.tabs")
	if err != nil || string(snapshot.State) != `{"count":1,"value":"first"}` {
		t.Fatalf("wrong dialog result: %s, err %v", snapshot.State, err)
	}
}
