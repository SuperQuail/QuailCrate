package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
)

func TestToolboxThroughPluginLifecycle(t *testing.T) {
	const id = "test.toolbox"
	const manifest = `{"id":"test.toolbox","name":"Tools","version":"1.0.0","schemaVersion":2,"hostApiVersion":2,"entry":"plugin.lua","runtime":"lua","permissions":["network.http","network.local","audio.play"],"contributes":["commands","appTabs"]}`
	const script = `
return function(r)
  local p, d = r.payload, {}
  if r.op == "init" then
    d.commands = {{id="run",title="Run"},{id="wait",title="Wait"},{id="play",title="Play"}}
    d.appTabs = {{id="main",title="Main"}}
  elseif r.op == "tab_state" then
    d.view = {title="Tools",controls={{type="button",id="start",text="Start"}}}
  elseif r.op == "command" and p.commandId == "play" then
    d.actions = {{type="audio.play",id="play",args={url=p.args.url}}}
  elseif r.op == "command" then
    assert(p.unixTime > 0)
    d.actions = {{type="http.request",id=p.commandId,args={url=p.args.url,responseType="json"}}}
  elseif r.op == "event" then
    local e = p.event
    if e.type == "tab.action" then
      d.actions = {{type="http.request",id="tab",args={url=p.state.waitURL,scope="page"}}}
    elseif e.type == "action.result" and e.actionId == "play" then
      p.state.sessionId = e.detail.sessionId
      d.state = p.state
    elseif e.type == "audio.error" then
      assert(p.state.sessionId == e.detail.sessionId and e.actionId == p.state.sessionId)
      p.state.audioError = e.code
      d.state = p.state
    elseif e.type == "action.result" and e.actionId == "run" then
      assert(e.status == "ok" and e.detail.status == 200 and e.detail.json.n == 17)
      d.state = {waitURL=e.detail.json.waitURL}
      d.actions = {{type="files.write",id="save",args={path="saved",body="17"}}}
    elseif e.type == "action.result" and e.actionId == "save" then
      assert(e.status == "ok")
      d.actions = {{type="files.read",id="read",args={path="saved"}}}
    elseif e.type == "action.result" and e.actionId == "read" then
      assert(e.detail.body == "17")
      d.actions = {{type="timer.after",id="clock",args={delayMs=10}}}
    elseif e.type == "timer.fired" then
      assert(e.actionId == "clock")
      d.state = {ready=true,waitURL=p.state.waitURL}
    else
      d.state = {unexpected=e.type,from=e.actionId}
    end
  end
  return {requestId=r.requestId,ok=true,data=d}
end`
	root := t.TempDir()
	user := filepath.Join(root, "plugins")
	dir := filepath.Join(user, "probe")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"manifest.json": manifest, "plugin.lua": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{}, 2)
	canceled := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wait" {
			started <- struct{}{}
			<-r.Context().Done()
			canceled <- struct{}{}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"n": 17, "waitURL": "http://" + r.Host + "/wait"})
	}))
	defer server.Close()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(root, "bundled"), user, nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	defer func() { h.cancel(); h.stopAll() }()
	if err = db.SetPreference(h.ctx, id, true, []string{"network.http", "network.local", "audio.play"}); err != nil {
		t.Fatal(err)
	}
	h.scan()
	call := func(command, url string) {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"url": url})
		if err := h.invoke(id, 0, "command", v1.Payload{CommandID: command, Args: args}); err != nil {
			t.Fatal(err)
		}
	}
	call("run", server.URL)
	deadline := time.Now().Add(4 * time.Second)
	for {
		snap, err := db.Snapshot(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var state struct{ Ready bool }
		if json.Unmarshal(snap.State, &state) == nil && state.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no file/timer result: %s", snap.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	wait := func(ch <-chan struct{}, why string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(4 * time.Second):
			t.Fatal(why)
		}
	}
	// A JSON document is intentionally not playable. A fast decoder failure
	// must still follow the receipt which told Lua the new playback session ID.
	call("play", server.URL)
	deadline = time.Now().Add(4 * time.Second)
	for {
		snap, err := db.Snapshot(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var state struct{ AudioError string }
		if json.Unmarshal(snap.State, &state) == nil && state.AudioError == "playback_failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing correlated audio failure: %s", snap.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	ref, ok := h.AppCatalog().ResolveTab(id, "main")
	if !ok {
		t.Fatal("missing tab")
	}
	tab, err := h.OpenTab(ref.TabRef, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.LoadTab(tab)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.ActOnTab(page.PageRef, "start", ""); err != nil {
		t.Fatal(err)
	}
	wait(started, "page request was not dispatched")
	h.CloseTab(tab)
	wait(canceled, "closing page did not cancel request")
	call("wait", server.URL+"/wait")
	wait(started, "instance request was not dispatched")
	if err = h.RevokePluginPermission(id, "network.local"); err != nil {
		t.Fatal(err)
	}
	wait(canceled, "revocation did not cancel instance request")
	if h.usable(id, ref.Generation) {
		t.Fatal("revoked generation is still running")
	}
	p, err := db.Preference(context.Background(), id)
	if err != nil || p.Enabled || fmt.Sprint(p.Grants) != "[network.http audio.play]" {
		t.Fatalf("unexpected grants: %+v %v", p, err)
	}
	snap, err := db.Snapshot(h.ctx, id)
	var state struct{ Ready bool }
	if err != nil || json.Unmarshal(snap.State, &state) != nil || !state.Ready {
		t.Fatalf("stale callback changed state: %s %v", snap.State, err)
	}
}
