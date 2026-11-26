package winui

import (
	"testing"

	v2 "quailcrate/pluginapi/v2"
)

func TestTrayContributionsAndRecovery(t *testing.T) {
	u := &UI{locale: "en-US", hotkey: "Alt+Space", editing: true}
	entries := []TrayEntry{
		{PluginID: "sample.groups", Generation: 9, Item: v2.TrayItem{ID: "new", Intent: "command", Title: "Create group", Command: "new", Order: 20}},
		{PluginID: "sample.ui", Generation: 3, Item: v2.TrayItem{ID: "edit", Intent: "desktop.toggleEdit", Title: "Edit", ActiveTitle: "Done", Order: 40}},
		{PluginID: "sample.ui", Generation: 3, Item: v2.TrayItem{Separator: true, Order: 30}},
		{PluginID: "sample.ui", Generation: 3, Item: v2.TrayItem{ID: "open", Intent: "launcher.open", Title: "Find", Order: 10}},
		{PluginID: "sample.ui", Generation: 3, Item: v2.TrayItem{ID: "choose", Intent: "launcher.choose", Title: "Choose", Order: 15}},
	}
	u.SetTray(entries)
	u.hostProcedure(wmDispatch, 0, 0)
	items, routes := u.trayItems()
	if len(items) != 7 || items[0].Label != "Find" || items[0].Shortcut != "Alt+Space" ||
		items[1].Label != "Create group" || routes[items[1].ID].Generation != 9 ||
		!items[2].Separator || items[3].Label != "Done" || !items[3].Checked ||
		!items[4].Separator || items[5].ID != "settings" || items[6].ID != "exit" {
		t.Fatalf("tray = %#v, routes = %#v", items, routes)
	}
	u.launcherShells = make([]LauncherShell, 2)
	items, _ = u.trayItems()
	if items[1].Label != "Choose" {
		t.Fatal("missing provider chooser for multiple launcher shells")
	}
	u.SetTray(nil)
	u.hostProcedure(wmDispatch, 0, 0)
	items, routes = u.trayItems()
	if len(items) != 2 || len(routes) != 0 || items[0].ID != "settings" || items[1].ID != "exit" {
		t.Fatalf("recovery menu = %#v", items)
	}
}
