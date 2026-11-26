package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/pluginapps/pagetest"
	v1 "quailcrate/pluginapi/v1"
)

func newPageTestHost(t *testing.T, grants []string) *Host {
	t.Helper()
	h := newTabHost(t)
	dir := filepath.Join(h.userDir, "pages")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	permissions, err := json.Marshal(grants)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"manifest.json":  fmt.Sprintf(`{"schemaVersion":2,"hostApiVersion":2,"uiVersion":2,"id":"test.pages","version":"1.0.0","name":"Pages","runtime":"lua","entry":"plugin.lua","contributes":["appTabs"],"permissions":%s}`, permissions),
		"web/index.html": "<title>Test page</title>",
		"web/main.js":    "const example = true;",
		"plugin.lua": `return function(req)
  local p, data = req.payload, {}
  if req.op == "init" then
    data.appTabs = {{id="web",title="Web",page="web/index.html",pageActions={"set","pick","queueTimers"}},{id="controls",title="Controls"}}
  elseif req.op == "tab_state" and p.tabId == "web" then
    data.pageState = {title=(p.state or {}).title or "Initial"}
  elseif req.op == "tab_state" then
    data.view = {uiVersion=2,title="Controls",root={type="slider",id="seek",events={submit="jump"}}}
  elseif req.op == "event" and p.event.type == "page.message" and p.event.controlId == "queueTimers" then
    data.actions = {{id="page-timer",type="timer.after",args={scope="page",delayMs=60000}},
                    {id="instance-timer",type="timer.after",args={delayMs=10}}}
  elseif req.op == "event" and p.event.type == "timer.fired" then
    assert(p.tabId == nil and p.event.actionId == "instance-timer")
    data.state = {title="Background"}
  elseif req.op == "event" and p.event.type == "page.message" and p.event.controlId == "pick" then
    data.actions = {{id="chosen",type="files.pickDirectory",args={access="read"}}}
  elseif req.op == "event" and p.event.type == "action.result" then
    assert(p.tabId == "web" and p.event.tabId == "web" and p.event.actionId == "chosen")
    data.state = {title=p.event.status}
  elseif req.op == "event" and p.event.type == "page.message" then
    assert(p.event.tabId == "web" and p.event.controlId == "set" and p.event.actionId == "request-1")
    data.state = {title=p.event.detail.title}
    data.pageReply = {saved=true}
  elseif req.op == "event" then
    assert(p.event.type == "tab.action" and p.event.controlId == "jump" and p.event.inputValue == 0.25)
    data.state = {title="Native"}
  end
  return {requestId=req.requestId,ok=true,data=data}
end`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.SetPreference(h.ctx, "test.pages", true, grants); err != nil {
		t.Fatal(err)
	}
	h.scan()
	return h
}

func TestPackagedPageSessionAndNativeValue(t *testing.T) {
	h := newPageTestHost(t, nil)
	open := func(id string) pluginapps.Page {
		t.Helper()
		tab, ok := h.AppCatalog().ResolveTab("test.pages", id)
		if !ok {
			if rec := h.records["test.pages"]; rec != nil {
				t.Fatalf("missing tab %s: %+v", id, rec.info)
			}
			t.Fatalf("missing tab %s: %+v", id, h.issues)
		}
		session, err := h.OpenTab(tab.TabRef, nil)
		if err != nil {
			t.Fatal(err)
		}
		page, err := h.LoadTab(session)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := open("web")
	if page.Document == nil || string(page.Document.State) != `{"title":"Initial"}` {
		t.Fatalf("initial document %+v", page.Document)
	}
	base := strings.TrimSuffix(page.Document.URL, "index.html")
	for _, resource := range []string{"index.html", "main.js"} {
		asset, err := h.ReadPageAsset(page.Session, base+resource)
		if err != nil || len(asset.Body) == 0 || asset.Headers()["Content-Security-Policy"] == "" {
			t.Fatalf("resource %q: %v", resource, err)
		}
	}
	for _, url := range []string{base + "../plugin.lua", base + "%2e%2e/main.js", base + "other.html", "https://example.org/main.js"} {
		if _, err := h.ReadPageAsset(page.Session, url); err == nil {
			t.Fatalf("accepted foreign resource %q", url)
		}
	}
	msg := func(action string, rev uint64) []byte {
		return []byte(fmt.Sprintf(`{"requestId":"request-1","revision":%d,"operation":%q,"args":{"title":"Updated"}}`, rev, action))
	}
	for _, attempt := range []struct {
		source, action string
		rev            uint64
	}{
		{base + "main.js", "set", page.Revision},
		{page.Document.URL, "unknown", page.Revision},
		{page.Document.URL, "set", page.Revision + 1},
	} {
		if _, err := h.SendPageMessage(page.PageRef, attempt.source, msg(attempt.action, attempt.rev)); err == nil {
			t.Fatal("accepted invalid message", attempt)
		}
	}
	reply, err := h.SendPageMessage(page.PageRef, page.Document.URL, msg("set", page.Revision))
	if err != nil || reply.RequestID != "request-1" || string(reply.Value) != `{"saved":true}` {
		t.Fatalf("page reply %+v, %v", reply, err)
	}
	if _, err := h.SendPageMessage(page.PageRef, page.Document.URL, msg("set", page.Revision)); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatalf("duplicate delivery: %v", err)
	}
	fresh, err := h.LoadTab(page.Session)
	if err != nil || fresh.Document.URL != page.Document.URL || string(fresh.Document.State) != `{"title":"Updated"}` {
		t.Fatalf("updated document %+v, %v", fresh, err)
	}
	native := open("controls")
	if _, err := h.ReadPageAsset(page.Session, page.Document.URL); !errors.Is(err, ErrInvocationUnavailable) {
		t.Fatal("closed session still served resources", err)
	}
	for _, value := range []string{`"bad"`, "2"} {
		if err := h.SubmitTabValue(native.PageRef, "jump", json.RawMessage(value)); err == nil {
			t.Fatal("accepted invalid slider value", value)
		}
	}
	if err := h.SubmitTabValue(native.PageRef, "jump", json.RawMessage("0.25")); err != nil {
		t.Fatal(err)
	}
	if again := open("web"); again.Document.URL == page.Document.URL || string(again.Document.State) != `{"title":"Native"}` {
		t.Fatalf("reopened page %+v", again.Document)
	}
}

func TestPageDirectoryPickerRequiresTrustedGestureAndPermission(t *testing.T) {
	for _, grant := range []bool{false, true} {
		t.Run(fmt.Sprintf("grant=%t", grant), func(t *testing.T) {
			var grants []string
			if grant {
				grants = []string{"files.directory"}
			}
			h := newPageTestHost(t, grants)
			results := make(chan func(pluginapps.DialogResult), 1)
			dialogs := func(ctx context.Context, a v1.Action, done func(pluginapps.DialogResult)) {
				if a.Type != "ui.pickPaths" || !a.DirectoriesOnly {
					t.Errorf("unexpected picker: %+v", a)
				}
				select {
				case results <- done:
				case <-ctx.Done():
				}
			}
			tab, ok := h.AppCatalog().ResolveTab("test.pages", "web")
			if !ok {
				t.Fatal("missing web page")
			}
			session, err := h.OpenTab(tab.TabRef, dialogs)
			if err != nil {
				t.Fatal(err)
			}
			page, err := h.LoadTab(session)
			if err != nil {
				t.Fatal(err)
			}
			message := []byte(fmt.Sprintf(`{"requestId":"pick-1","operation":"pick","args":{"isTrusted":true},"revision":%d}`, page.Revision))
			if _, err := h.SendPageMessage(page.PageRef, page.Document.URL, message); err == nil {
				t.Fatal("page script opened a directory picker")
			}
			_, err = h.SendPageUserMessage(page.PageRef, page.Document.URL, message)
			if !grant {
				if err == nil {
					t.Fatal("user gesture bypassed the directory grant")
				}
				select {
				case <-results:
					t.Fatal("unexpected picker")
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var done func(pluginapps.DialogResult)
			select {
			case done = <-results:
			case <-time.After(2 * time.Second):
				t.Fatal("picker did not open")
			}
			_, changed := h.ObserveApps()
			dir := t.TempDir()
			done(pluginapps.DialogResult{Paths: []string{dir}})
			select {
			case <-changed:
			case <-time.After(2 * time.Second):
				t.Fatal("picker result not delivered")
			}
			page, err = h.LoadTab(session)
			if err != nil || string(page.Document.State) != `{"title":"ok"}` {
				t.Fatalf("picker result: %+v, %v", page.Document, err)
			}
			folders, err := h.DirectoryGrants("test.pages")
			if err != nil || len(folders) != 1 || folders[0].Path != dir {
				t.Fatalf("unexpected grants: %+v, %v", folders, err)
			}
			h.CloseTab(session)
			if _, err := h.SendPageUserMessage(page.PageRef, page.Document.URL, message); !errors.Is(err, ErrInvocationUnavailable) {
				t.Fatal("closed page retained user activation", err)
			}
		})
	}
}

func TestHeadlessPageRuntimeRetry(t *testing.T) {
	h := newPageTestHost(t, nil)
	tab, ok := h.AppCatalog().ResolveTab("test.pages", "web")
	if !ok {
		t.Fatal("missing page")
	}
	b := pagetest.Browser{Service: h, Unavailable: true}
	if err := b.Open(tab.TabRef, nil); !errors.Is(err, pagetest.ErrRuntimeUnavailable) {
		t.Fatal(err)
	}
	b.Unavailable = false
	if err := b.Open(tab.TabRef, nil); err != nil {
		t.Fatal(err)
	}
	reply, err := b.Send("request-1", "set", json.RawMessage(`{"title":"Retry"}`))
	if err != nil || string(reply.Value) != `{"saved":true}` || string(b.Page.Document.State) != `{"title":"Retry"}` {
		t.Fatalf("%+v, %v", reply, err)
	}
	b.Close()
	if _, err := b.Send("request-1", "set", json.RawMessage("{}")); !errors.Is(err, pluginapps.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestCommittedInstanceActionDispatchAfterPageClose(t *testing.T) {
	h := newPageTestHost(t, nil)
	tab, ok := h.AppCatalog().ResolveTab("test.pages", "web")
	if !ok {
		t.Fatal("missing packaged page")
	}
	session, err := h.OpenTab(tab.TabRef, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.LoadTab(session)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	rec := h.records[tab.PluginID]
	h.mu.Unlock()
	barrier := make(chan struct{})
	rec.serial.Lock()
	rec.toolbox.lastDispatch = barrier
	rec.serial.Unlock()
	msg := []byte(fmt.Sprintf(`{"requestId":"queued","operation":"queueTimers","revision":%d,"args":{}}`, page.Revision))
	if _, err := h.SendPageMessage(page.PageRef, page.Document.URL, msg); err != nil {
		close(barrier)
		t.Fatal(err)
	}
	h.CloseTab(session)
	_, changed := h.ObserveApps()
	close(barrier)
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("queued instance action was lost on close")
	}
	snapshot, err := h.db.Snapshot(h.ctx, tab.PluginID)
	if err != nil || string(snapshot.State) != `{"title":"Background"}` {
		t.Fatalf("background completion = %s, %v", snapshot.State, err)
	}
	if rec.toolbox.net.CancelTimer("page-timer") {
		t.Fatal("closed page retained its timer")
	}
}
