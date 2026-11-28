package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"quailcrate/internal/host"
)

func TestLoginStartupUsesSavedWorkingDirectoryAndDataProfile(t *testing.T) {
	initialDir := t.TempDir()
	workingDir := filepath.Join(t.TempDir(), "工作 目录")
	if err := os.Mkdir(workingDir, 0700); err != nil {
		t.Fatal(err)
	}
	dataDir, err := host.PrepareDataDir(filepath.Join(t.TempDir(), "自定义 数据"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(initialDir)
	name := fmt.Sprintf(`Local\QuailCrate-%x`, sha256.Sum256([]byte(strings.ToLower(dataDir))))
	namePtr, _ := windows.UTF16PtrFromString(name)
	mutex, err := windows.CreateMutex(nil, false, namePtr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(mutex) })

	// The existing data-profile lock guarantees this exercises startup without
	// launching Win32, loading plugins, or registering a real startup entry.
	if err := run(dataDir, "", false, loginStartup{enabled: true, workingDir: workingDir}); err != nil {
		t.Fatalf("duplicate sign-in launch: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(cwd, workingDir) {
		t.Fatalf("working directory = %q, want %q", cwd, workingDir)
	}
	if err := run(dataDir, "", false, loginStartup{enabled: true}); err != nil {
		t.Fatalf("default sign-in launch: %v", err)
	}
	if exe, exeErr := os.Executable(); exeErr != nil {
		t.Fatal(exeErr)
	} else if cwd, err = os.Getwd(); err != nil || !strings.EqualFold(cwd, filepath.Dir(exe)) {
		t.Fatalf("default working directory = %q (%v), want %q", cwd, err, filepath.Dir(exe))
	}
	if err := run(dataDir, "", false, loginStartup{}); err == nil {
		t.Fatal("a duplicate manual launch should still report the existing instance")
	}
}
