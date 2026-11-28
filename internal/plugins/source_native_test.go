package plugins

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const fixtureNativeDLL = "fixture.dll"

func writeBundledSourceFixture(t *testing.T, bundle string) string {
	t.Helper()
	spec := bundledSourceDLLSpec{
		dir: "fixture-native", id: "quailcrate.fixture-native", dll: fixtureNativeDLL,
		source: filepath.Join("native", "fixture.cpp"),
	}
	previous := bundledSourceDLLs
	bundledSourceDLLs = []bundledSourceDLLSpec{spec}
	t.Cleanup(func() { bundledSourceDLLs = previous })

	manifest := fixtureManifest()
	manifest.ID = spec.id
	manifest.Runtime, manifest.Entry = "dll", spec.dll
	manifest.Platform, manifest.ABIVersion = "windows/amd64", 2
	writeFixture(t, bundle, spec.dir, manifest, "")
	packageDir := filepath.Join(bundle, spec.dir)
	source := filepath.Join(packageDir, spec.source)
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("// source"), 0600); err != nil {
		t.Fatal(err)
	}
	return packageDir
}

func writeExternalNativeFixture(t *testing.T, user, directory string) string {
	t.Helper()
	manifest := fixtureManifest()
	manifest.Runtime, manifest.Entry = "dll", fixtureNativeDLL
	manifest.Platform, manifest.ABIVersion = "windows/amd64", 2
	writeFixture(t, user, directory, manifest, "")
	entry := filepath.Join(user, directory, fixtureNativeDLL)
	if err := os.WriteFile(entry, []byte("user DLL"), 0600); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestSourceNativeBuildIsLazyAndReused(t *testing.T) {
	bundle, user := t.TempDir(), t.TempDir()
	userEntry := writeExternalNativeFixture(t, user, "external")
	writeBundledSourceFixture(t, bundle)
	if pkg := Discover(bundle, "", nil)[0]; pkg.Err == nil {
		t.Fatal("release discovery accepted a package without the DLL")
	}
	builds := 0
	source := &SourceNative{Build: func(_ context.Context, dir string) error {
		builds++
		if err := os.WriteFile(filepath.Join(dir, fixtureNativeDLL), []byte("built DLL"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, nativeRunner), []byte("runner"), 0600)
	}}
	t.Cleanup(func() { source.Close() })
	pkgs := (Catalog{SourceNative: source}).Discover(bundle, user)
	if len(pkgs) != 2 || pkgs[0].Err != nil || pkgs[1].Err != nil || builds != 0 {
		t.Fatalf("discovery returned %+v after %d builds", pkgs, builds)
	}
	for i := range pkgs {
		if err := pkgs[i].sourceBuild.prepare(t.Context(), &pkgs[i]); err != nil {
			t.Fatal(err)
		}
	}
	bundled, external := pkgs[0], pkgs[1]
	if builds != 1 || bundled.runner == nil || external.runner != bundled.runner {
		t.Fatalf("built %d times for two native plugins", builds)
	}
	if bundled.Entry != filepath.Join(bundled.runner.dir, fixtureNativeDLL) || bundled.dllHash != fmt.Sprintf("%x", sha256.Sum256([]byte("built DLL"))) ||
		bundled.runner.runnerHash != fmt.Sprintf("%x", sha256.Sum256([]byte("runner"))) {
		t.Fatal("bundled native plugin did not select the compiled DLL and runner with their independent digests")
	}
	if external.Entry != userEntry || external.dllHash != "" {
		t.Fatalf("development build replaced third-party plugin entry %q or its digest", external.Entry)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(bundled.Entry)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking the build directory after closing: %v", err)
	}
}

func TestSourceNativePreservesPackageDirForWebAssets(t *testing.T) {
	bundle := t.TempDir()
	packageDir := writeBundledSourceFixture(t, bundle)
	if err := os.MkdirAll(filepath.Join(packageDir, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	const document = "<html></html>"
	if err := os.WriteFile(filepath.Join(packageDir, "web", "index.html"), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	source := &SourceNative{Build: func(_ context.Context, dir string) error {
		if err := os.WriteFile(filepath.Join(dir, fixtureNativeDLL), []byte("built DLL"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, nativeRunner), []byte("runner"), 0600)
	}}
	t.Cleanup(func() { source.Close() })
	pkgs := (Catalog{SourceNative: source}).Discover(bundle, "")
	if len(pkgs) != 1 || pkgs[0].Err != nil {
		t.Fatalf("native fixture discovery failed: %+v", pkgs)
	}
	origDir := pkgs[0].Dir
	if err := pkgs[0].sourceBuild.prepare(t.Context(), &pkgs[0]); err != nil {
		t.Fatal(err)
	}
	if pkgs[0].Dir != origDir {
		t.Fatalf("prepare overwrote native package dir: got %q want %q", pkgs[0].Dir, origDir)
	}
	if pkgs[0].sourceBuiltEntry != filepath.Join(pkgs[0].runner.dir, fixtureNativeDLL) ||
		pkgs[0].dllHash != fmt.Sprintf("%x", sha256.Sum256([]byte("built DLL"))) {
		t.Fatalf("unexpected native source build wiring: %+v", pkgs[0])
	}
	if body, _, err := pkgs[0].ReadPageAsset("web/index.html", "index.html"); err != nil || string(body) != document {
		t.Fatalf("read packaged page after native build: %q, %v", body, err)
	}
}

func TestSourceBuildRetriesAfterMissingRunner(t *testing.T) {
	user := t.TempDir()
	entry := writeExternalNativeFixture(t, user, "external")
	var firstDir string
	source := &SourceNative{Build: func(_ context.Context, dir string) error {
		if firstDir == "" {
			firstDir = dir
			return nil
		}
		return os.WriteFile(filepath.Join(dir, nativeRunner), []byte("runner"), 0600)
	}}
	t.Cleanup(func() { source.Close() })
	pkgs := (Catalog{SourceNative: source}).Discover("", user)
	if len(pkgs) != 1 || pkgs[0].Err != nil || firstDir != "" {
		t.Fatalf("external discovery returned %+v before runner build (%q)", pkgs, firstDir)
	}
	pkg := pkgs[0]
	if err := source.prepare(t.Context(), &pkg); !errors.Is(err, os.ErrNotExist) || pkg.runner != nil {
		t.Fatalf("accepted a partial build or lost its diagnostic: %v", err)
	}
	if _, err := os.Stat(firstDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking incomplete output directory: %v", err)
	}
	if err := source.prepare(t.Context(), &pkg); err != nil || pkg.runner == nil {
		t.Fatalf("retry failed: %v", err)
	}
	if pkg.Entry != entry || len(pkg.runner.dllHashes) != 0 {
		t.Fatalf("third-party plugin should build only the runner, got %+v", pkg)
	}
}
