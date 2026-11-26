package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	v1 "quailcrate/pluginapi/v1"
	"testing"
	"time"
)

func TestNativeIconAuthorization(t *testing.T) {
	dll, runner := os.Getenv("QUAILCRATE_NATIVE_PROBE"), os.Getenv("QUAILCRATE_NATIVE_RUNNER")
	if dll == "" || runner == "" {
		t.Skip("build native integration fixtures with scripts/build.ps1 -TestNative")
	}
	pkg := Package{Dir: filepath.Dir(dll), Manifest: v1.Manifest{ID: "test.external-icons", Runtime: "dll", Entry: filepath.Base(dll), SchemaVersion: 2, HostAPIVersion: 2}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	process, err := openNativeProcess(ctx, pkg, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	request := v1.Request{RequestID: "icon-policy", Op: "search", Payload: v1.Payload{Limit: 1}}
	init := v1.Data{Search: true, Commands: []v1.Command{{ID: "select"}}}
	worker := &plugin{pkg: pkg}
	result := invokeRuntime(ctx, worker, process, request, init)
	var denied *v1.APIError
	if !errors.As(result.err, &denied) || denied.Code != "permission_denied" || len(result.response.Data.Results) != 0 {
		t.Fatalf("external ungranted DLL icon returned to host: %+v", result)
	}
}
