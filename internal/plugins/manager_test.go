package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "quailcrate/pluginapi/v1"
)

const testEntry = `
return function(request)
  local data = {}
  if request.op == "init" then
    assert(os == nil and io == nil and debug == nil and package == nil and loadfile == nil)
    data.commands = {{id="run", title="Run"}}
  elseif request.op == "command" then
    if request.payload.args.mode == "loop" then
      while true do end
    elseif request.payload.args.mode == "open" then
      data.actions = {{type="paths.open", path="C:\\fixture"}}
    else
      data.state = {items=json.array(), event=request.payload.args.event}
    end
  end
  return {requestId=request.requestId, ok=true, data=data}
end
`

func TestManagerAuthorizationAndGeneration(t *testing.T) {
	pkg := testPackage(t, testEntry)
	manager := NewManager()
	ctx := context.Background()
	first, err := manager.Enable(ctx, pkg, nil, v1.Payload{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation == 0 || manager.Generation(pkg.Manifest.ID) != first.Generation {
		t.Fatalf("unexpected generation: %d", first.Generation)
	}
	open := v1.Payload{CommandID: "run", Args: json.RawMessage(`{"mode":"open"}`)}
	response, err := manager.Call(ctx, pkg.Manifest.ID, "command", open)
	var api *v1.APIError
	if !errors.As(err, &api) || api.Code != "permission_denied" || len(response.Data.Actions) != 0 {
		t.Fatalf("unauthorized response %#v, error %v", response, err)
	}
	if err := manager.Disable(ctx, pkg.Manifest.ID); err != nil {
		t.Fatal(err)
	}
	if manager.Active(pkg.Manifest.ID) {
		t.Fatal("stopped plugin retained an active contribution")
	}
	if _, err := manager.Call(ctx, pkg.Manifest.ID, "command", open); err == nil {
		t.Fatal("disabled plugin accepted a command")
	}
	second, err := manager.Enable(ctx, pkg, []string{"paths.open"}, v1.Payload{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Disable(ctx, pkg.Manifest.ID) })
	if second.Generation == first.Generation {
		t.Fatal("re-enabled plugin reused its old generation")
	}
	for _, stale := range []uint64{0, first.Generation} {
		if _, err := manager.CallGeneration(ctx, pkg.Manifest.ID, stale, "command", open); err == nil {
			t.Fatalf("generation %d reached replacement worker %d", stale, second.Generation)
		}
	}
	response, err = manager.CallGeneration(ctx, pkg.Manifest.ID, second.Generation, "command", open)
	if err != nil || len(response.Data.Actions) != 1 {
		t.Fatalf("authorized response %#v, error %v", response, err)
	}
}

func TestLuaTimeoutAndRecovery(t *testing.T) {
	pkg := testPackage(t, testEntry)
	manager := NewManager()
	ctx := context.Background()
	if _, err := manager.Enable(ctx, pkg, nil, v1.Payload{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Disable(ctx, pkg.Manifest.ID) })
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	response, err := manager.Call(deadline, pkg.Manifest.ID, "command", v1.Payload{CommandID: "run", Args: json.RawMessage(`{"mode":"loop"}`)})
	if !errors.Is(err, context.DeadlineExceeded) || response.Generation != 0 {
		t.Fatalf("timed out response %#v, error %v", response, err)
	}
	response, err = manager.Call(ctx, pkg.Manifest.ID, "command", v1.Payload{CommandID: "run", Args: json.RawMessage(`{"mode":"save","event":[0,null]}`)})
	if err != nil || string(response.Data.State) != `{"event":[0,null],"items":[]}` {
		t.Fatalf("response after cancellation %#v, error %v", response, err)
	}
}

func TestDiscoveryRejectsIncompatibleVersionAndDuplicate(t *testing.T) {
	manifest := fixtureManifest()
	bundled, user := t.TempDir(), t.TempDir()
	writeFixture(t, bundled, "bundle", manifest, testEntry)
	writeFixture(t, user, "duplicate", manifest, testEntry)
	manifest.ID, manifest.HostAPIVersion = "fixture.old", 2
	writeFixture(t, user, "old", manifest, testEntry)
	packages := Discover(bundled, user, nil)
	if len(packages) != 3 || packages[0].Err != nil || packages[1].Err == nil || packages[2].Err == nil {
		t.Fatalf("unexpected discovery result %#v", packages)
	}
	if !strings.Contains(packages[1].Err.Error(), "duplicate") || !strings.Contains(packages[2].Err.Error(), "version") {
		t.Fatalf("unexpected diagnostics %#v", packages)
	}
}

func TestAwaitDiscardsLateResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reply := make(chan outcome, 1)
	reply <- outcome{response: v1.Response{OK: true, Generation: 99}}
	cancel()
	response, err := await(&plugin{ctx: ctx}, job{ctx: context.Background(), reply: reply})
	if !errors.Is(err, context.Canceled) || response.Generation != 0 {
		t.Fatalf("late response %#v, error %v", response, err)
	}
}

func TestTranslationResourcesFollowRequestLocale(t *testing.T) {
	root := t.TempDir()
	manifest := fixtureManifest()
	manifest.Description = "Stateful sample"
	manifest.Localizations = map[string]v1.LocalizedManifest{
		"zh-CN": {Description: "带状态的示例"},
	}
	writeFixture(t, root, "translated", manifest, `
return function(request)
  local data = {}
  local strings = request.payload.strings
  if request.op == "init" then
    data.commands = {{id="run", title=strings.greeting}}
  elseif request.op == "command" then
    data.state = { locale=request.payload.locale, greeting=strings.greeting, fallback=strings.fallback }
  end
  return {requestId=request.requestId, ok=true, data=data}
end
`)
	directory := filepath.Join(root, "translated", "locales")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"en-US.json": `{"greeting":"Hello","fallback":"English fallback"}`,
		"zh-CN.json": `{"greeting":"你好"}`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	packages := Discover("", root, nil)
	if len(packages) != 1 || packages[0].Err != nil {
		t.Fatalf("unexpected discovery result %#v", packages)
	}
	summary := packages[0].Manifest
	if summary.DisplayDescription("zh-cn") != "带状态的示例" ||
		summary.DisplayDescription("en-US") != manifest.Description ||
		summary.DisplayName("zh-CN") != manifest.Name {
		t.Fatalf("unexpected localized plugin metadata: %#v", summary)
	}
	manager := NewManager()
	ctx := context.Background()
	init, err := manager.Enable(ctx, packages[0], nil, v1.Payload{Locale: "zh-cn"})
	if err != nil || len(init.Data.Commands) != 1 || init.Data.Commands[0].Title != "你好" {
		t.Fatalf("unexpected translated init %#v, %v", init, err)
	}
	t.Cleanup(func() { manager.Disable(ctx, manifest.ID) })
	for _, test := range []struct{ locale, expect string }{
		{"zh-CN", `{"fallback":"English fallback","greeting":"你好","locale":"zh-CN"}`},
		{"fr-FR", `{"fallback":"English fallback","greeting":"Hello","locale":"en-US"}`},
	} {
		result, err := manager.Call(ctx, manifest.ID, "command", v1.Payload{CommandID: "run", Locale: test.locale})
		if err != nil || string(result.Data.State) != test.expect {
			t.Fatalf("locale %s response %#v, error %v", test.locale, result, err)
		}
	}
}

func testPackage(t *testing.T, entry string) Package {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "fixture", fixtureManifest(), entry)
	packages := Discover("", root, nil)
	if len(packages) != 1 || packages[0].Err != nil {
		t.Fatalf("unexpected package %#v", packages)
	}
	return packages[0]
}

func fixtureManifest() v1.Manifest {
	return v1.Manifest{
		SchemaVersion: 1, HostAPIVersion: 1, ID: "fixture.plugin", Name: "Fixture", Version: "1.0.0", Runtime: "lua", Entry: "main.lua",
		Contributes: []string{"commands"}, Permissions: []string{"paths.open"},
	}
}

func writeFixture(t *testing.T, root, directory string, manifest v1.Manifest, entry string) {
	t.Helper()
	dir := filepath.Join(root, directory)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"manifest.json": data, "main.lua": []byte(entry)} {
		if err := os.WriteFile(filepath.Join(dir, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
