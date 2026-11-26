package host

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func serviceManifest(id, provides, uses string) v1.Manifest {
	m := v1.Manifest{ID: id, Name: id, Runtime: "lua", Version: "1.0.0", Entry: "plugin.lua", SchemaVersion: 2, HostAPIVersion: 2}
	if provides != "" {
		m.Provides = []v2.Export{{ID: provides, Version: "1.0.0", Methods: []string{"get"}}}
	}
	if uses != "" {
		m.Uses = []v2.Dependency{{ID: uses, VersionRange: ">=1.0.0 <2.0.0", Methods: []string{"get"}}}
	}
	return m
}

func newServiceHost(t *testing.T, manifests ...v1.Manifest) *Host {
	t.Helper()
	const script = `return function(req)
        local data = {}
        if req.op == "service" then
            data.state = { calls = ((req.payload.state or {}).calls or 0) + 1 }
            data.reply = {status = "ok", value = {success = true}}
        elseif req.op == "event" and req.payload.event.type == "service.result" then
            data.state = {calls = ((req.payload.state or {}).calls or 0) + 1}
        elseif req.op == "event" and req.payload.event.type == "plugin.event" then
            local event=req.payload.event
            data.state={source=event.source,topic=event.topic,revision=event.revision,value=event.detail.value}
        end
        return {requestId = req.requestId, ok = true, data = data}
    end`
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, slog.New(slog.DiscardHandler), filepath.Join(dir, "bundled"), filepath.Join(dir, "plugins"), nil)
	h.ui = winui.New(winui.Callbacks{PluginApps: h})
	t.Cleanup(func() { h.stopAll(); h.cancel(); db.Close() })
	for _, m := range manifests {
		pluginDir := filepath.Join(h.userDir, m.ID)
		if err := os.MkdirAll(pluginDir, 0700); err != nil {
			t.Fatal(err)
		}
		blob, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(pluginDir, "manifest.json"), blob, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(pluginDir, "plugin.lua"), []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		if err = db.SetPreference(h.ctx, m.ID, true, plugins.RequestedGrants(m)); err != nil {
			t.Fatal(err)
		}
	}
	h.scan()
	return h
}

func TestRequiredServiceCycleAndRecovery(t *testing.T) {
	a := serviceManifest("cycle.a", "cycle.a.data", v2.Items)
	b := serviceManifest("cycle.b", v2.Items, "cycle.a.data")
	user := serviceManifest("cycle.consumer", "", "cycle.a.data")
	h := newServiceHost(t, a, b, user)
	for _, m := range []v1.Manifest{a, b, user} {
		r := h.records[m.ID]
		if !r.blocked || r.generation != 0 || !strings.Contains(r.info.Detail, "required dependency cycle") || !strings.Contains(r.info.Detail, "cycle.a.data") || !strings.Contains(r.info.Detail, v2.Items) || strings.Count(r.info.Detail, " -> ") < 4 {
			t.Fatalf("%s: %#v", m.ID, r.info)
		}
	}
}

func TestRequiredServiceAlternativeLifecycle(t *testing.T) {
	a := serviceManifest("cycle.a", "cycle.a.data", v2.Items)
	b := serviceManifest("cycle.b", v2.Items, "cycle.a.data")
	independent := serviceManifest("cycle.source", v2.Items, "")
	user := serviceManifest("cycle.consumer", "", "cycle.a.data")
	optional := serviceManifest("cycle.optional", "", "cycle.a.data")
	optional.Uses[0].Optional = true
	h := newServiceHost(t, a, b, user, optional, independent)
	for _, id := range []string{a.ID, b.ID, user.ID, independent.ID, optional.ID} {
		if h.records[id].generation == 0 {
			t.Fatalf("%s failed to start", id)
		}
	}
	optionalGen := h.records[optional.ID].generation
	old := h.records[user.ID].lifetime
	h.toggle(independent.ID, false)
	for _, id := range []string{a.ID, b.ID, user.ID} {
		if h.records[id].generation != 0 || !h.records[id].blocked {
			t.Errorf("%s survived an ungrounded cycle", id)
		}
	}
	if old.Err() == nil || h.records[optional.ID].generation != optionalGen {
		t.Fatal("wrong generation invalidated")
	}
	h.toggle(independent.ID, true)
	for _, id := range []string{a.ID, b.ID, user.ID} {
		if h.records[id].generation == 0 || h.records[id].blocked {
			t.Errorf("%s failed to recover", id)
		}
	}
}

func TestProviderChoiceBindingAndLostReply(t *testing.T) {
	caller := serviceManifest("test.caller", "", v2.Items)
	first := serviceManifest("test.first", v2.Items, "")
	second := serviceManifest("test.second", v2.Items, "")
	h := newServiceHost(t, caller, first, second)
	generation := h.records[caller.ID].generation
	action := v1.Action{ID: "work", Type: "service.call", Service: v2.Items, Method: "get"}
	resolve := func(interactive bool, dialog pluginapps.DialogHandler) (endpoint, v2.Reply) {
		return h.resolveService(h.ctx, caller.ID, generation, action, interactive, dialog)
	}
	if _, reply := resolve(false, nil); reply.Code != "ambiguous_provider" {
		t.Fatalf("ambiguous binding: %+v", reply)
	}
	selected, reply := resolve(true, func(_ context.Context, a v1.Action, done func(pluginapps.DialogResult)) {
		if !slices.ContainsFunc(a.Options, func(o v1.Option) bool { return o.ID == second.ID }) {
			t.Fatal(a.Options)
		}
		done(pluginapps.DialogResult{Items: []string{second.ID}})
	})
	if reply.Code != "" || selected.id != second.ID {
		t.Fatal(selected, reply)
	}
	if next, _ := resolve(false, nil); next.id != second.ID {
		t.Fatal("saved preference not honored")
	}
	h.toggle(second.ID, false)
	if next, _ := resolve(false, nil); next.id != first.ID {
		t.Fatal("inactive provider selected")
	}
	if reply := h.executeService(h.ctx, caller.ID, selected, action.Service, action.Method, nil, true, nil, nil); reply.Code != "unavailable" {
		t.Fatal("stale endpoint", reply)
	}
	calls := func(id string) int {
		t.Helper()
		s, err := h.db.Snapshot(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var state struct{ Calls int }
		if len(s.State) > 0 {
			if err = json.Unmarshal(s.State, &state); err != nil {
				t.Fatal(err)
			}
		}
		return state.Calls
	}
	// Hold the caller's serial slot while the provider commits its write.
	// Retiring this caller generation discards the completion without a replay.
	consumer := h.records[caller.ID]
	consumer.serial.Lock()
	finished := make(chan struct{})
	go func() { defer close(finished); h.callService(caller.ID, generation, action, nil, "", true, nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for calls(first.ID) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	committed := calls(first.ID)
	if committed != 1 {
		consumer.serial.Unlock()
		t.Fatalf("provider commits: %d", committed)
	}
	h.toggle(caller.ID, false)
	consumer.serial.Unlock()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("service completion leaked")
	}
	h.toggle(caller.ID, true)
	if calls(first.ID) != 1 || calls(caller.ID) != 0 {
		t.Fatal("lost completion replayed a write or was delivered to new generation")
	}
}

func TestStaleProviderChoiceDoesNotBindNewGeneration(t *testing.T) {
	caller := serviceManifest("test.caller", "", v2.Items)
	first := serviceManifest("test.first", v2.Items, "")
	second := serviceManifest("test.second", v2.Items, "")
	h := newServiceHost(t, caller, first, second)
	action := v1.Action{Type: "service.call", Service: v2.Items, Method: "get"}
	_, reply := h.resolveService(h.ctx, caller.ID, h.records[caller.ID].generation, action, true, func(_ context.Context, _ v1.Action, done func(pluginapps.DialogResult)) {
		h.toggle(second.ID, false)
		h.toggle(second.ID, true)
		done(pluginapps.DialogResult{Items: []string{second.ID}})
	})
	if reply.Code != "unavailable" {
		t.Fatal("restarted provider accepted an old choice", reply)
	}
	_, retry := h.resolveService(h.ctx, caller.ID, h.records[caller.ID].generation, action, false, nil)
	if retry.Code != "ambiguous_provider" {
		t.Fatal("stale choice was saved", retry)
	}
}

func TestBlockedDependencyRechecksAuthorization(t *testing.T) {
	caller := serviceManifest("test.caller", "", v2.Items)
	provider := serviceManifest("test.provider", v2.Items, "")
	h := newServiceHost(t, caller, provider)
	h.toggle(provider.ID, false)
	if !h.records[caller.ID].blocked || !strings.Contains(h.records[caller.ID].info.Detail, "disabled") {
		t.Fatal("missing provider status", h.records[caller.ID].info)
	}
	caller.Permissions = []string{"apps.read"}
	blob, err := json.Marshal(caller)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.userDir, caller.ID, "manifest.json"), blob, 0600); err != nil {
		t.Fatal(err)
	}
	h.scan()
	h.toggle(provider.ID, true)
	r := h.records[caller.ID]
	if r.generation != 0 || r.blocked || r.info.State != "authorization" {
		t.Fatal("old grants restarted changed consumer", r.info)
	}
}
