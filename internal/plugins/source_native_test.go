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

func TestSourceNativeBuildIsLazyAndReused(t *testing.T) {
	bundle, user := t.TempDir(), t.TempDir()
	manifest := fixtureManifest()
	manifest.Runtime, manifest.Entry = "dll", musicDLL
	manifest.Platform, manifest.ABIVersion = "windows/amd64", 2
	writeFixture(t, user, "external", manifest, "")
	userEntry := filepath.Join(user, "external", musicDLL)
	if err := os.WriteFile(userEntry, []byte("user DLL"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest.ID = MusicDLLID
	writeFixture(t, bundle, "music", manifest, "")
	if err := os.MkdirAll(filepath.Join(bundle, "music", "native"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "music", "native", "music_plugin.cpp"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if pkg := Discover(bundle, "", nil)[0]; pkg.Err == nil {
		t.Fatal("release discovery accepted a package without the DLL")
	}
	builds := 0
	source := &SourceNative{Build: func(_ context.Context, dir string) error {
		builds++
		if err := os.WriteFile(filepath.Join(dir, musicDLL), []byte("built DLL"), 0600); err != nil {
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
	if bundled.Entry != filepath.Join(bundled.runner.dir, musicDLL) || bundled.dllHash != fmt.Sprintf("%x", sha256.Sum256([]byte("built DLL"))) ||
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

func TestSourceMusicPreservesPackageDirForWebAssets(t *testing.T) {
	bundle := t.TempDir()
	manifest := fixtureManifest()
	manifest.ID = MusicDLLID
	manifest.Runtime, manifest.Entry = "dll", musicDLL
	manifest.Platform, manifest.ABIVersion = "windows/amd64", 2
	writeFixture(t, bundle, "music", manifest, "")
	if err := os.MkdirAll(filepath.Join(bundle, "music", "native"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "music", "native", "music_plugin.cpp"), []byte("// source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "music", "web"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "music", "web", "index.html"), []byte("<html></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	source := &SourceNative{Build: func(_ context.Context, dir string) error {
		if err := os.WriteFile(filepath.Join(dir, musicDLL), []byte("music DLL"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, nativeRunner), []byte("runner"), 0600)
	}}
	t.Cleanup(func() { source.Close() })
	pkgs := (Catalog{SourceNative: source}).Discover(bundle, "")
	if len(pkgs) != 1 || pkgs[0].Err != nil {
		t.Fatalf("music discovery failed: %+v", pkgs)
	}
	origDir := pkgs[0].Dir
	if err := pkgs[0].sourceBuild.prepare(t.Context(), &pkgs[0]); err != nil {
		t.Fatal(err)
	}
	if pkgs[0].Dir != origDir {
		t.Fatalf("prepare overwrote music package dir: got %q want %q", pkgs[0].Dir, origDir)
	}
	if pkgs[0].sourceBuiltEntry != filepath.Join(pkgs[0].runner.dir, musicDLL) ||
		pkgs[0].dllHash != fmt.Sprintf("%x", sha256.Sum256([]byte("music DLL"))) {
		t.Fatalf("unexpected music source build wiring: %+v", pkgs[0])
	}
}

func TestSourceBuildRetriesAfterMissingRunner(t *testing.T) {
	var firstDir string
	source := &SourceNative{Build: func(_ context.Context, dir string) error {
		if err := os.WriteFile(filepath.Join(dir, musicDLL), []byte("DLL"), 0600); err != nil {
			return err
		}
		if firstDir == "" {
			firstDir = dir
			return nil
		}
		return os.WriteFile(filepath.Join(dir, nativeRunner), []byte("runner"), 0600)
	}}
	t.Cleanup(func() { source.Close() })
	var pkg Package
	if err := source.prepare(t.Context(), &pkg); !errors.Is(err, os.ErrNotExist) || pkg.runner != nil {
		t.Fatalf("accepted a partial build or lost its diagnostic: %v", err)
	}
	if _, err := os.Stat(firstDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking incomplete output directory: %v", err)
	}
	if err := source.prepare(t.Context(), &pkg); err != nil || pkg.runner == nil {
		t.Fatalf("retry failed: %v", err)
	}
}
