package pluginfiles

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quailcrate/internal/store"
	v2 "quailcrate/pluginapi/v2"
)

func fileService(t *testing.T, profile, bundle string) *Service {
	t.Helper()
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, profile, bundle)
}

func TestPrivateFilesAndLimits(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "profile")
	s := fileService(t, profile, "")
	ctx := t.Context()
	do := func(owner, act, path, body, encoding string) (any, error) {
		return s.Execute(ctx, owner, "files."+act, v2.ToolArgs{Path: path, Body: body, Encoding: encoding})
	}
	if _, err := do("a", "mkdir", "sets", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := do("a", "write", "sets/list", "first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := do("a", "write", "sets/list", "AP8=", "base64"); err != nil {
		t.Fatal(err)
	}
	got, err := do("a", "read", "sets/list", "", "")
	if err != nil || got.(map[string]string)["body"] != "AP8=" || got.(map[string]string)["encoding"] != "base64" {
		t.Fatalf("binary round trip: %v %v", got, err)
	}
	entries, err := do("a", "list", "sets", "", "")
	raw, _ := json.Marshal(entries)
	if err != nil || string(raw) != `[{"name":"list","directory":false}]` {
		t.Fatalf("listing: %s %v", raw, err)
	}
	if _, err := do("b", "read", "sets/list", "", ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("private owner: %v", err)
	}
	for _, path := range []string{"outside/../file", "/absolute", "C:/Windows", "cache:stream", ".", "..", "a\\b"} {
		if _, err := do("a", "write", path, "", ""); !errors.Is(err, ErrDenied) {
			t.Errorf("accepted path %q (%v)", path, err)
		}
	}
	if _, err := do("a", "write", "sets/list", strings.Repeat("x", maxFile+1), ""); err == nil {
		t.Fatal("accepted oversized file")
	}
	if _, err := do("a", "write", "sets/list", "?", "base64"); err == nil {
		t.Fatal("accepted invalid base64")
	}
	got, err = do("a", "read", "sets/list", "", "")
	if err != nil || got.(map[string]string)["body"] != "AP8=" {
		t.Fatalf("failed write changed original file: %v %v", got, err)
	}
	// Seed the fixture directly so the test need not copy sixteen MiB through the API.
	private := s.PrivateDataDir("a")
	f, err := os.Create(filepath.Join(private, "quota-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(privateQuota - 2)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := do("a", "write", "new", "x", ""); err == nil {
		t.Fatal("exceeded private quota")
	}
	if _, err := do("a", "write", "SETS/LIST", "AB", ""); err != nil {
		t.Fatalf("replacing at quota: %v", err)
	}
	if _, err := do("a", "read", "quota-fixture", "", ""); err == nil {
		t.Fatal("oversized read")
	}
	if _, err := do("a", "remove", "sets/list", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := do("a", "read", "sets/list", "", ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed file available: %v", err)
	}
}

func TestDirectoryGrantPersistenceAndRevocation(t *testing.T) {
	root := t.TempDir()
	profile, bundle, external := filepath.Join(root, "profile"), filepath.Join(root, "bundle"), filepath.Join(root, "music")
	for _, d := range []string{bundle, external} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(external, "track"), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	s := fileService(t, profile, bundle)
	for _, path := range []string{root, profile, bundle, "relative"} {
		if _, err := s.Grant(ctx, "music", path, "read"); !errors.Is(err, ErrDenied) {
			t.Errorf("accepted protected %q: %v", path, err)
		}
	}
	g, err := s.Grant(ctx, "music", external, "read")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == "" || g.Access != "read" {
		t.Fatal(g)
	}
	rawArgs := v2.ToolArgs{Path: "track", Root: external}
	if _, err := s.Execute(ctx, "music", "files.read", rawArgs); !errors.Is(err, ErrDenied) {
		t.Fatalf("accepted raw path: %v", err)
	}
	a := v2.ToolArgs{Path: "track", Root: g.ID, Body: "edited"}
	reloaded := fileService(t, profile, bundle)
	got, err := reloaded.Execute(ctx, "music", "files.read", a)
	if err != nil || got.(map[string]string)["body"] != "sample" {
		t.Fatalf("persistent read: %v %v", got, err)
	}
	if _, err := reloaded.Execute(ctx, "music", "files.write", a); !errors.Is(err, ErrDenied) {
		t.Fatal("read grant allowed writing")
	}
	if _, err := reloaded.Execute(ctx, "other", "files.read", a); !errors.Is(err, ErrDenied) {
		t.Fatal("grant accepted for another plugin")
	}
	list, err := reloaded.Execute(ctx, "music", "files.directories", v2.ToolArgs{})
	if err != nil || len(list.([]Directory)) != 1 || list.([]Directory)[0] != g {
		t.Fatalf("grant listing: %v %v", list, err)
	}
	rw, err := reloaded.Grant(ctx, "music", external, "write")
	if err != nil || rw.ID != g.ID {
		t.Fatalf("changed grant identity: %v %v", rw, err)
	}
	if _, err := reloaded.Execute(ctx, "music", "files.write", a); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Execute(ctx, "music", "files.revokeDirectory", v2.ToolArgs{Root: g.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(ctx, "music", "files.read", a); !errors.Is(err, ErrDenied) {
		t.Fatal("revocation not persisted")
	}
	legacyGrant, err := s.Grant(ctx, "music", external, "write")
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := s.db.Setting(ctx, "directories."+ownerKey("music"), ""); err != nil || !strings.Contains(saved, legacyGrant.ID) {
		t.Fatalf("legacy directory grant key: %q, %v", saved, err)
	}
	if err := s.db.PurgePlugin(ctx, "music"); err != nil {
		t.Fatal(err)
	}
	a.Root = legacyGrant.ID
	if _, err := s.Execute(ctx, "music", "files.read", a); !errors.Is(err, ErrDenied) {
		t.Fatal("purge left directory grant available")
	}
}

func TestDirectoryReplacementAndCancellation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "library")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := fileService(t, filepath.Join(root, "profile"), "")
	g, err := s.Grant(t.Context(), "one", dir, "write")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(t.Context(), "one", "files.write", v2.ToolArgs{Root: g.ID, Path: "file"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("replaced root accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Execute(cancelled, "one", "files.write", v2.ToolArgs{Path: "file"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
