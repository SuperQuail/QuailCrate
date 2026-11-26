package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "quailcrate/pluginapi/v1"
)

func TestPagePackageBoundaryAndProtocol(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets", "images"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"assets/index.html", "assets/images/cover.png", "assets/second.html", "outside.js", "assets/plugin.lua"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := Package{Dir: root}
	data, kind, err := p.ReadPageAsset("assets/index.html", "images/cover.png")
	if err != nil || kind != "image/png" || string(data) != "assets/images/cover.png" {
		t.Fatalf("%s %s %v", data, kind, err)
	}
	for _, path := range []string{"../outside.js", `..\outside.js`, "plugin.lua", "second.html", "foo.js:stream"} {
		if _, _, err := p.ReadPageAsset("assets/index.html", path); err == nil {
			t.Fatal("unexpected access", path)
		}
	}
	const page = "assets/index.html"
	tab := v1.AppTabDefinition{ID: "p", Title: "P", Page: page, PageActions: []string{"save"}}
	manifest := v1.Manifest{HostAPIVersion: 2, UIVersion: 2, Contributes: []string{"appTabs"}}
	init := v1.Data{AppTabs: []v1.AppTabDefinition{tab}}
	if err := validateContributions(manifest, nil, init); err != nil {
		t.Fatal(err)
	}
	if err := validateContributions(v1.Manifest{Contributes: []string{"appTabs"}}, nil, init); err == nil {
		t.Fatal("v1 accepted HTML page")
	}
	for _, entry := range []string{"../page.html", "https://example.org/page.html", "page.js", `assets\page.html`, strings.Repeat("a", 600) + ".html"} {
		bad := tab
		bad.Page = entry
		if err := validatePageDefinition(manifest, bad); err == nil {
			t.Fatal("accepted entry", entry)
		}
	}
	query := v1.Request{Op: "tab_state", Payload: v1.Payload{TabID: "p"}}
	for _, data := range []v1.Data{{}, {PageState: []byte("[]")}, {PageState: []byte("{}"), View: &v1.View{}}, {PageReply: []byte("{}")}} {
		if err := validateResponse(manifest, nil, query, v1.Response{OK: true, Data: data}, init); err == nil {
			t.Fatal("accepted invalid page query response", data)
		}
	}
}
