package update

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPortableUpgradePreservesUnownedFiles(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	public := key.Public().(ed25519.PublicKey)
	root := makePortableFixture(t, "1.0.0-alpha.1", key, map[string]string{"plugins/old.lua": "old"})
	stage := makePortableFixture(t, "1.0.0-alpha.2", key, map[string]string{"plugins/new.lua": "new"})
	archive := zipPortableFixture(t, stage)
	writePortableTestFile(t, root, "data/user-plugins/user.lua", "user plugin")
	writePortableTestFile(t, root, "plugins/local.txt", "local file")
	if err := ApplyPortable(root, archive, "1.0.0-alpha.2", public); err != nil {
		t.Fatal(err)
	}
	if got, err := PortableVersion(root, public); got != "1.0.0-alpha.2" || err != nil {
		t.Fatalf("updated version = %q, %v", got, err)
	}
	for path, content := range map[string]string{
		portableHost: "1.0.0-alpha.2:" + portableHost, portableWorker: "1.0.0-alpha.2:" + portableWorker,
		portableHelper:    "1.0.0-alpha.2:" + portableHelper,
		"plugins/new.lua": "new", "plugins/local.txt": "local file", "data/user-plugins/user.lua": "user plugin",
	} {
		assertPortableTestFile(t, root, path, content)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "old.lua")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("retired file was not removed: %v", err)
	}
	if pending, err := PortableNeedsRecovery(root); pending || err != nil {
		t.Fatalf("recovery needed after commit: %v, %v", pending, err)
	}
	if err := ApplyPortable(root, archive, "1.0.0-alpha.2", public); err == nil {
		t.Fatal("same version accepted")
	}
}

func TestPortableRecoversInterruptedReplacement(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	public := key.Public().(ed25519.PublicKey)
	root := makePortableFixture(t, "1.0.0", key, map[string]string{"plugins/old.lua": "old"})
	stage := makePortableFixture(t, "1.1.0", key, map[string]string{"new/plugin.lua": "new"})
	archive := zipPortableFixture(t, stage)
	transaction := filepath.Join(root, filepath.FromSlash(portableTransaction))
	writePortableTestFile(t, transaction, portableOwnerName, portableOwner)
	previous, err := ReadPortableManifest(root, public)
	if err != nil {
		t.Fatal(err)
	}
	_, old, err := loadPortableInventory(root, public)
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := stagePortableArchive(archive, filepath.Join(transaction, portableStagedDir), "1.1.0", public)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := preparePortableChanges(root, filepath.Join(transaction, portableBackupDir), old, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePortableJournal(transaction, journal, false); err != nil {
		t.Fatal(err)
	}
	// Interrupt after replacing the package, before persisting the commit.
	if err := applyPortableChanges(root, transaction, incoming, journal); err != nil {
		t.Fatal(err)
	}
	if pending, err := PortableNeedsRecovery(root); !pending || err != nil {
		t.Fatalf("interrupted update needed recovery: %v, %v", pending, err)
	}
	assertPortableTestFile(t, root, portableHost, "1.1.0:"+portableHost)
	oldHelper := old["quailcrateupdate.exe"]
	if helper, err := PortableRecoveryHelper(root, oldHelper.SHA256); err != nil || helper != filepath.Join(transaction, portableBackupDir, portableHelper) {
		t.Fatalf("old recovery helper = %q, %v", helper, err)
	}
	if err := RecoverPortable(root, public); err != nil {
		t.Fatal(err)
	}
	if verified, err := ReadPortableManifest(root, public); err != nil || verified.Version != previous.Version {
		t.Fatalf("recovered manifest = %+v, %v", verified, err)
	}
	assertPortableTestFile(t, root, portableHost, "1.0.0:"+portableHost)
	assertPortableTestFile(t, root, portableHelper, "1.0.0:"+portableHelper)
	assertPortableTestFile(t, root, "plugins/old.lua", "old")
	if _, err := os.Stat(filepath.Join(root, "new")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new files or directories remained: %v", err)
	}
	if err := RecoverPortable(root, public); err != nil {
		t.Fatalf("repeated recovery failed: %v", err)
	}
}

func TestPortableRejectsCorruptionAndUnownedCollisions(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	public := key.Public().(ed25519.PublicKey)
	root := makePortableFixture(t, "1.0.0", key, nil)
	stage := makePortableFixture(t, "1.1.0", key, map[string]string{"plugins/new.lua": "new"})
	writePortableTestFile(t, root, "plugins/new.lua", "user content")
	if err := ApplyPortable(root, zipPortableFixture(t, stage), "1.1.0", public); !errors.Is(err, ErrPortableConflict) {
		t.Fatalf("unowned collision accepted: %v", err)
	}
	assertPortableTestFile(t, root, "plugins/new.lua", "user content")
	writePortableTestFile(t, stage, "plugins/new.lua", "bad")
	if err := ApplyPortable(root, zipPortableFixture(t, stage), "1.1.0", public); err == nil {
		t.Fatal("tampered archive accepted")
	}
	assertPortableTestFile(t, root, portableHost, "1.0.0:"+portableHost)
	if _, err := PortableVersion(t.TempDir(), public); !errors.Is(err, ErrPortableMigration) {
		t.Fatalf("legacy ownership error = %v", err)
	}
}

func TestPortableRejectsUnsafeArchivePaths(t *testing.T) {
	for _, name := range []string{"data/user.db", "DATA/user.db", "../outside", `plugins\outside`, "plugins/COM¹.ini", "plugins/com1 .txt", "plugins/dir.", "plugins/example.txt:stream"} {
		if err := validatePortablePath(name); err == nil {
			t.Errorf("accepted unsafe path %q", name)
		}
	}
	for _, name := range []string{"plugins/valid.lua", "plugins/音乐.lua", "plugins/COMLPT1.txt"} {
		if err := validatePortablePath(name); err != nil {
			t.Errorf("rejected valid path %q: %v", name, err)
		}
	}
}

func TestPortableRecoveryIgnoresUnusableDataParent(t *testing.T) {
	root := t.TempDir()
	if !PortableCanApply(root) {
		t.Fatal("ordinary installation without a data directory was rejected")
	}
	writePortableTestFile(t, root, "data", "user file")
	if PortableCanApply(root) {
		t.Fatal("non-directory data path was accepted for staging")
	}
	if pending, err := PortableNeedsRecovery(root); pending || err != nil {
		t.Fatalf("unusable data directory blocked startup: %v, %v", pending, err)
	}
}

func makePortableFixture(t *testing.T, version string, key ed25519.PrivateKey, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{portableHost, portableWorker, portableHelper} {
		writePortableTestFile(t, root, name, version+":"+name)
	}
	for name, contents := range files {
		writePortableTestFile(t, root, name, contents)
	}
	if err := WritePortableManifest(root, version, key); err != nil {
		t.Fatal(err)
	}
	return root
}

func writePortableTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertPortableTestFile(t *testing.T, root, name, want string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil || string(contents) != want {
		t.Fatalf("file %q = %q, %v (want %q)", name, contents, err, want)
	}
}

func zipPortableFixture(t *testing.T, root string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture.zip")
	archive, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	err = filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		output, err := zipWriter.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = output.Write(contents)
		return err
	})
	if err := errors.Join(err, zipWriter.Close(), archive.Close()); err != nil {
		t.Fatal(err)
	}
	return name
}
