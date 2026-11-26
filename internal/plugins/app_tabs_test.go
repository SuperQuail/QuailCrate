package plugins

import (
	"context"
	"encoding/json"
	"testing"

	v1 "quailcrate/pluginapi/v1"
)

func TestLuaAppTabLifecycle(t *testing.T) {
	manifest := fixtureManifest()
	manifest.Contributes = []string{"appTabs"}
	root := t.TempDir()
	writeFixture(t, root, "tabs", manifest, `
return function(r)
  local data = {}
  if r.op == "init" then
    data.appTabs = {{id="main", title="Main", icon="calendar"}}
  elseif r.op == "tab_state" then
    assert(r.payload.tabId == "main")
    data.view = {title="Main", controls={{id="click", type="button", text="Edit"}}}
  elseif r.op == "event" then
    local e = r.payload.event
    assert(e.type == "tab.action" and e.tabId == "main" and e.controlId == "click")
    data.actions = {{type="ui.promptText", id="edit", title="Edit"}}
  end
  return {requestId=r.requestId, ok=true, data=data}
end`)
	packages := Discover("", root, nil)
	if len(packages) != 1 || packages[0].Err != nil {
		t.Fatalf("app tab discovery: %#v", packages)
	}
	ctx := context.Background()
	manager := NewManager()
	init, err := manager.Enable(ctx, packages[0], nil, v1.Payload{})
	if err != nil || len(init.Data.AppTabs) != 1 || init.Data.AppTabs[0].Icon != "calendar" {
		t.Fatalf("registration %#v, error %v", init, err)
	}
	t.Cleanup(func() { manager.Disable(ctx, manifest.ID) })
	page, err := manager.Call(ctx, manifest.ID, "tab_state", v1.Payload{TabID: "main"})
	if err != nil || !page.Data.View.AllowsTabAction("click", "") || page.Generation != init.Generation {
		t.Fatalf("tab view %#v, error %v", page, err)
	}
	click := v1.Event{Type: "tab.action", TabID: "main", ControlID: "click"}
	response, err := manager.Call(ctx, manifest.ID, "event", v1.Payload{TabID: "main", Event: &click})
	if err != nil || len(response.Data.Actions) != 1 || response.Data.Actions[0].Type != "ui.promptText" {
		t.Fatalf("tab click %#v, error %v", response, err)
	}
	if _, err := manager.Call(ctx, manifest.ID, "tab_state", v1.Payload{TabID: "other"}); err == nil {
		t.Fatal("unregistered tab query accepted")
	}
	if _, err := manager.Call(ctx, manifest.ID, "event", v1.Payload{TabID: "other", Event: &click}); err == nil {
		t.Fatal("mismatched tab event accepted")
	}
}

func TestAppTabResponseValidation(t *testing.T) {
	manifest := fixtureManifest()
	tab := v1.AppTabDefinition{ID: "main", Title: "Main"}
	init := v1.Data{AppTabs: []v1.AppTabDefinition{tab}}
	if err := validateContributions(manifest, nil, init); err == nil {
		t.Fatal("undeclared tab accepted")
	}
	manifest.Contributes = []string{"appTabs"}
	if err := validateContributions(manifest, nil, v1.Data{AppTabs: []v1.AppTabDefinition{tab, tab}}); err == nil {
		t.Fatal("duplicate tab accepted")
	}
	page := v1.Request{Op: "tab_state", Payload: v1.Payload{TabID: tab.ID}}
	for name, data := range map[string]v1.Data{
		"missing view": {},
		"state":        {View: &v1.View{}, State: json.RawMessage(`{}`)},
		"action":       {View: &v1.View{}, Actions: []v1.Action{{Type: "ui.promptText", ID: "prompt"}}},
		"registration": {View: &v1.View{}, AppTabs: init.AppTabs},
		"menu":         {View: &v1.View{Menu: []v1.Control{{Type: "separator"}}}},
		"reorder":      {View: &v1.View{Controls: []v1.Control{{ID: "list", Type: "list", Reorder: true}}}},
		"drop":         {View: &v1.View{Controls: []v1.Control{{ID: "list", Type: "list", Drop: true}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateResponse(manifest, nil, page, v1.Response{OK: true, Data: data}, init); err == nil {
				t.Fatal("invalid tab response accepted")
			}
		})
	}
	click := v1.Request{Op: "event", Payload: v1.Payload{Event: &v1.Event{Type: "tab.action", TabID: tab.ID, ControlID: "pick"}}}
	response := v1.Response{OK: true, Data: v1.Data{Actions: []v1.Action{{Type: "paths.open", Path: "example"}}}}
	if err := validateResponse(manifest, nil, click, response, init); err == nil {
		t.Fatal("tab action bypassed paths.open grant")
	}
}

func TestAppTabClickTargets(t *testing.T) {
	view := v1.View{Controls: []v1.Control{
		{ID: "help", Type: "text"},
		{ID: "run", Type: "button"},
		{ID: "rows", Type: "list", Items: []v1.Item{{ID: "file"}}},
		{ID: "days", Type: "grid", Columns: 7, Items: []v1.Item{{ID: "today"}}},
	}}
	for _, test := range []struct {
		control, item string
		want          bool
	}{
		{"help", "", false}, {"run", "", true}, {"run", "file", false},
		{"rows", "", false}, {"rows", "file", true}, {"days", "file", false},
		{"days", "today", true}, {"missing", "", false},
	} {
		if got := view.AllowsTabAction(test.control, test.item); got != test.want {
			t.Errorf("AllowsTabAction(%q, %q) = %v, want %v", test.control, test.item, got, test.want)
		}
	}
}
