package update

import (
	"bytes"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPortableRollsBackWhenHostFileIsLocked(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize))
	public := key.Public().(ed25519.PublicKey)
	root := makePortableFixture(t, "1.0.0", key, map[string]string{"plugins/old.lua": "old"})
	stage := makePortableFixture(t, "1.1.0", key, map[string]string{"plugins/new.lua": "new"})
	archive := zipPortableFixture(t, stage)
	path, err := windows.UTF16PtrFromString(filepath.Join(root, portableHost))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	applyErr := ApplyPortable(root, archive, "1.1.0", public)
	if err := windows.CloseHandle(lock); err != nil {
		t.Fatal(err)
	}
	if applyErr == nil {
		t.Fatal("upgrade succeeded with a locked host executable")
	}
	if pending, err := PortableNeedsRecovery(root); pending || err != nil {
		t.Fatalf("rollback left a pending transaction: %v, %v", pending, err)
	}
	assertPortableTestFile(t, root, portableHost, "1.0.0:"+portableHost)
	assertPortableTestFile(t, root, portableHelper, "1.0.0:"+portableHelper)
	assertPortableTestFile(t, root, "plugins/old.lua", "old")
	if err := ApplyPortable(root, archive, "1.1.0", public); err != nil {
		t.Fatalf("retry failed after closing the locked file: %v", err)
	}
}
