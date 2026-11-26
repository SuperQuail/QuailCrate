package host

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/winshell"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func TestSearchToGroupWithIndependentNotes(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	examples := filepath.Join(root, "examples")
	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(root, "plugins"), examples, nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	defer func() { h.cancel(); h.stopAll() }()
	const notes = "example.notes"
	const groups = "quailcrate.desktop-groups"
	apps := v1.App{ID: "catalog:onenote", Name: "OneNote for Windows 10", Source: "packaged"}
	h.apps = []v1.App{apps}
	h.catalog = map[string]winshell.Entry{apps.ID: {App: apps}}
	h.indexReady = true
	for _, pkg := range plugins.Discover("", examples, nil) {
		if pkg.Manifest.ID == notes {
			if err := db.SetPreference(h.ctx, notes, true, plugins.RequestedGrants(pkg.Manifest)); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.scan()
	var prompts atomic.Int64
	choice := "create"
	h.optionDialog = func(_ context.Context, a v1.Action, done func(pluginapps.DialogResult)) {
		prompts.Add(1)
		done(pluginapps.DialogResult{Items: []string{choice}})
	}
	search := func(q, id string) winui.Result {
		t.Helper()
		h.mu.Lock()
		h.searchID++
		revision := h.searchID
		h.mu.Unlock()
		h.runSearch(h.ctx, revision, q)
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, r := range h.visibleResults {
			if r.PluginID == id {
				return r
			}
		}
		t.Fatalf("missing %s in search for %q: %#v", id, q, h.visibleResults)
		return winui.Result{}
	}
	pin := func(r winui.Result) {
		t.Helper()
		if len(r.Actions) != 1 || r.Actions[0].Provider != groups {
			t.Fatalf("menu: %#v", r.Actions)
		}
		h.activateResult(r, &r.Actions[0])
	}
	type entry struct {
		ID, Kind, AppID, Name string
		Reference             *v2.ItemRef
	}
	items := func() []entry {
		t.Helper()
		s, err := db.Snapshot(h.ctx, groups)
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			Groups map[string]struct{ Tabs []struct{ Items []entry } }
		}
		if json.Unmarshal(s.State, &state) != nil || len(state.Groups["group-1"].Tabs) != 1 {
			t.Fatalf("group state: %s", s.State)
		}
		return state.Groups["group-1"].Tabs[0].Items
	}
	app := search("OneNote", "quailcrate.launcher")
	pin(app)
	got := items()
	if len(got) != 1 || got[0].AppID != apps.ID || got[0].Name != apps.Name {
		t.Fatalf("app entry: %#v", got)
	}
	pin(app) // Repeat of the consumed result must not reopen the picker.
	if prompts.Load() != 1 {
		t.Fatal("consumed result was replayed")
	}
	choice = "group-1"
	pin(search("OneNote", "quailcrate.launcher"))
	if len(items()) != 1 {
		t.Fatal("duplicate pinned app")
	}
	note := search("first note", notes)
	pin(note)
	got = items()
	if len(got) != 2 || got[1].Reference == nil || *got[1].Reference != *note.Item {
		t.Fatalf("saved notes reference: %#v", got)
	}
	h.toggle(notes, false)
	got = items()
	if len(got) != 2 || got[1].Reference.Source != notes {
		t.Fatal("disabling source lost note")
	}
	h.toggle(notes, true)
	ep, reply := h.resolveService(h.ctx, groups, h.records[groups].generation, v1.Action{Service: v2.Items, Method: "open", Provider: notes}, true, nil)
	if reply.Status != "" || ep.id != notes {
		t.Fatal("re-enabled notes provider did not rebind", reply)
	}
	refArgs, _ := json.Marshal(map[string]any{"item": got[1].Reference})
	opened := h.executeService(h.ctx, groups, ep, v2.Items, "open", refArgs, true, []string{groups}, nil)
	if opened.Status != "ok" {
		t.Fatalf("re-enabled notes source failed to open stored ID: %#v", opened)
	}
	catalog := h.AppCatalog()
	tab, ok := catalog.ResolveTab(notes, "notes")
	if !ok {
		t.Fatal("notes tab missing")
	}
	saved := make(chan struct{}, 1)
	session, err := h.OpenTab(tab.TabRef, func(_ context.Context, a v1.Action, done func(pluginapps.DialogResult)) {
		if a.Type == "ui.pickOptions" {
			done(pluginapps.DialogResult{Items: []string{"group-1"}})
			saved <- struct{}{}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.LoadTab(session)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.ActOnTab(page.PageRef, "save", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-saved:
	case <-time.After(3 * time.Second):
		t.Fatal("public notes example could not call groups service from its own page")
	}
	stale := search("OneNote", "quailcrate.launcher")
	h.toggle(groups, false)
	pin(stale)
	app = search("OneNote", "quailcrate.launcher")
	if len(app.Actions) != 0 {
		t.Fatal("inactive groups menu visible")
	}
	page, err = h.LoadTab(session)
	if err != nil || page.View.AllowsTabAction("save", "") {
		t.Fatal("optional service did not disappear", err)
	}
}

func TestVersionedServiceResolution(t *testing.T) {
	h := &Host{records: map[string]*record{}}
	d := v2.Dependency{ID: v2.GroupItems, VersionRange: ">=1.0.0 <2.0.0", Methods: []string{"add"}}
	for i, id := range []string{"one", "two", "incompatible"} {
		versions := []string{"1.0.0", "1.2.0"}
		if i == 2 {
			versions = []string{"2.0.0"}
		}
		rec := &record{generation: uint64(i + 1), lifetime: context.Background()}
		for _, v := range versions {
			rec.pkg.Manifest.Provides = append(rec.pkg.Manifest.Provides, v2.Export{ID: d.ID, Version: v, Methods: d.Methods})
		}
		h.records[id] = rec
	}
	eps, found := h.candidatesLocked(d, "")
	if !found || len(eps) != 2 || eps[0].id != "one" || eps[0].version != "1.2.0" {
		t.Fatal(eps)
	}
	eps, found = h.candidatesLocked(d, "incompatible")
	if len(eps) != 0 || !found {
		t.Fatal("incompatible provider accepted")
	}
	r := h.executeService(context.Background(), "two", endpoint{id: "one"}, "s", "m", nil, true, []string{"one"}, nil)
	if r.Code != "call_cycle" {
		t.Fatal(r)
	}
}

func TestSubscribedEventUsesPublisherIdentity(t *testing.T) {
	publisher := serviceManifest("example.publisher", "", "")
	listener := serviceManifest("example.listener", "", "")
	observer := serviceManifest("example.other", "", "")
	topic := publisher.ID + ".changed"
	publisher.Publishes = []string{topic}
	listener.Listens = []string{topic}
	h := newServiceHost(t, publisher, listener, observer)
	original, err := h.db.Snapshot(h.ctx, observer.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := v1.Action{Type: "event.publish", Topic: topic, Args: json.RawMessage(`{"value":"saved"}`)}
	h.publishEvent(observer.ID, h.records[observer.ID].generation, a, nil)
	h.publishEvent(publisher.ID, h.records[publisher.ID].generation, a, nil)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s, err := h.db.Snapshot(h.ctx, listener.ID)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Source, Topic, Value string
			Revision             uint64
		}
		if json.Unmarshal(s.State, &event) == nil && event.Revision > 0 {
			if event.Revision != 1 || event.Source != publisher.ID || event.Topic != topic || event.Value != "saved" {
				t.Fatal(event)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing topic delivery")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, _ := h.db.Snapshot(h.ctx, observer.ID)
	if string(s.State) != string(original.State) {
		t.Fatalf("unexpected delivery to non-subscriber: %s", s.State)
	}
}
