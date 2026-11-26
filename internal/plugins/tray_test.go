package plugins

import (
	"testing"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func TestTrayRegistration(t *testing.T) {
	m := fixtureManifest()
	m.SchemaVersion, m.HostAPIVersion, m.UIVersion = 2, 2, 2
	m.Contributes = []string{"commands", "tray"}
	init := v1.Request{RequestID: "1", Op: "init"}
	tray := v2.TrayItem{ID: "run", Title: "Run", Intent: "command", Command: "run"}
	r := v1.Response{RequestID: "1", OK: true, Data: v1.Data{
		Commands: []v1.Command{{ID: "run", Title: "Run"}}, Tray: []v2.TrayItem{tray},
	}}
	if err := validateResponse(m, nil, init, r, v1.Data{}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []v2.TrayItem{
		{ID: "run", Title: "Other plugin", Intent: "command", Command: "external.new"},
		{ID: "stop", Title: "Stop host", Intent: "exit"},
		{Separator: true, Command: "run"},
		{ID: "bad", Title: "Hidden\nlabel", Intent: "launcher.open"},
	} {
		r.Data.Tray = []v2.TrayItem{bad}
		if err := validateResponse(m, nil, init, r, v1.Data{}); err == nil {
			t.Fatalf("accepted invalid tray item %#v", bad)
		}
	}
	r.Data.Tray = []v2.TrayItem{tray, tray}
	if err := validateResponse(m, nil, init, r, v1.Data{}); err == nil {
		t.Fatal("accepted duplicate tray id")
	}
	r.Data.Tray = []v2.TrayItem{tray}
	m.Contributes = []string{"commands"}
	if err := validateResponse(m, nil, init, r, v1.Data{}); err == nil {
		t.Fatal("accepted undeclared tray contribution")
	}
}
