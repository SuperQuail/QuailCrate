package pluginapps

import (
	"slices"
	"testing"

	v1 "quailcrate/pluginapi/v1"
)

func TestBuildPublicContributions(t *testing.T) {
	records := []Registration{
		{PluginID: "z", PluginName: "Calendar", Generation: 8,
			Tabs:     []v1.AppTabDefinition{{ID: "home", Title: "Home"}},
			Commands: []v1.Command{{ID: "open", Title: "Open", Location: "settings"}}},
		{PluginID: "a", PluginName: "Groups", Generation: 41,
			Tabs: []v1.AppTabDefinition{{ID: "home", Title: "Home", Icon: "folder"}, {ID: "edit", Title: "Edit"}},
			Commands: []v1.Command{
				{ID: "open", Title: "Open", Location: "desktop-edit"},
				{ID: "private", Title: "Invoke internally"},
				{ID: "search", Title: "Launcher only", Location: "search"},
				{ID: "change", Title: "Change", Location: "settings"},
			}},
		{PluginID: "disabled",
			Tabs:     []v1.AppTabDefinition{{ID: "home", Title: "Old tab"}},
			Commands: []v1.Command{{ID: "open", Title: "Old command", Location: "settings"}}},
	}
	got := Build(records)
	wantTabs := []Tab{
		{TabRef: TabRef{PluginID: "a", TabID: "home", Generation: 41}, PluginName: "Groups", Title: "Home", Icon: "folder"},
		{TabRef: TabRef{PluginID: "a", TabID: "edit", Generation: 41}, PluginName: "Groups", Title: "Edit"},
		{TabRef: TabRef{PluginID: "z", TabID: "home", Generation: 8}, PluginName: "Calendar", Title: "Home"},
	}
	if !slices.Equal(got.Tabs, wantTabs) {
		t.Fatalf("tabs = %+v, want %+v", got.Tabs, wantTabs)
	}
	wantCommands := []Command{
		{PluginID: "a", ID: "open", Generation: 41, PluginName: "Groups", Title: "Open", Location: "desktop-edit"},
		{PluginID: "a", ID: "change", Generation: 41, PluginName: "Groups", Title: "Change", Location: "settings"},
		{PluginID: "z", ID: "open", Generation: 8, PluginName: "Calendar", Title: "Open", Location: "settings"},
	}
	if !slices.Equal(got.Commands, wantCommands) {
		t.Fatalf("commands = %+v, want %+v", got.Commands, wantCommands)
	}
	if records[0].PluginID != "z" {
		t.Fatal("Build reordered the host's records")
	}
	if tab, ok := got.ResolveTab("z", "home"); !ok || tab != wantTabs[2] {
		t.Fatalf("lookup across plugins with identical local IDs returned %+v, %t", tab, ok)
	}
	if _, ok := got.ResolveTab("disabled", "home"); ok {
		t.Fatal("disabled plugin contributed a tab")
	}
	if cmd, ok := got.ResolveCommand("z", "open"); !ok || cmd != wantCommands[2] {
		t.Fatalf("lookup across plugins with identical command IDs returned %+v, %t", cmd, ok)
	}
	if _, ok := got.ResolveCommand("a", "private"); ok {
		t.Fatal("private command exposed through public command lookup")
	}
}

func TestSnapshotDetachedAcrossReload(t *testing.T) {
	records := []Registration{{
		PluginID: "a", PluginName: "Old name", Generation: 1,
		Tabs:     []v1.AppTabDefinition{{ID: "home", Title: "Home"}},
		Commands: []v1.Command{{ID: "open", Title: "Open", Location: "settings"}},
	}}
	first := Build(records)
	records[0].Generation++
	records[0].Tabs[0].Title = "首页"
	records[0].Commands[0].Title = "打开"
	next := Build(records)
	copy := next.Clone()
	copy.Tabs[0].Title = "caller mutation"
	copy.Commands[0].Title = "caller mutation"
	if first.Tabs[0].Title != "Home" || first.Commands[0].Title != "Open" || first.Tabs[0].Generation != 1 {
		t.Fatalf("old snapshot changed across reload: %+v", first)
	}
	if next.Tabs[0].Title != "首页" || next.Commands[0].Title != "打开" || next.Tabs[0].Generation != 2 {
		t.Fatalf("new snapshot lost localized data or shares writable slices: %+v", next)
	}
}
