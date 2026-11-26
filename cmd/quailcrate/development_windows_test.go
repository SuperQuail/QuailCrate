package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchBundle(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	sourceBundle := filepath.Join(checkout, "plugins")
	installed := filepath.Join(root, "installed", "plugins")
	foreign := filepath.Join(root, "another-project", "plugins")
	for _, dir := range []string{sourceBundle, installed, foreign} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module quailcrate\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(checkout)
	source := filepath.Join(checkout, "cmd", "quailcrate", "development_windows.go")
	trimmed := "quailcrate/cmd/quailcrate/development_windows.go"
	goRunExe := filepath.Join(root, "go-build", "b001", "exe", "quailcrate.exe")
	goRunBundle := filepath.Join(filepath.Dir(goRunExe), "plugins")
	installedExe := filepath.Join(filepath.Dir(installed), "QuailCrate.exe")
	pin := strings.Repeat("a", 64)
	tests := []struct {
		name, requested, exe, sourceFile, releasePin, wantBundle string
		wantBuilder                                              bool
	}{
		{"go run defaults to checkout", "", goRunExe, source, "", sourceBundle, true},
		{"explicit relative checkout", "plugins", goRunExe, source, "", sourceBundle, true},
		{"explicit absolute checkout", sourceBundle, goRunExe, source, "", sourceBundle, true},
		{"foreign bundle", foreign, goRunExe, source, "", foreign, false},
		{"bundle beside executable takes precedence", "", installedExe, source, "", installed, false},
		{"release pin prevents checkout fallback", "", goRunExe, source, pin, goRunBundle, false},
		{"release pin also protects explicit checkout", sourceBundle, installedExe, source, pin, sourceBundle, false},
		{"trimmed executable without a bundle", "", goRunExe, trimmed, "", goRunBundle, false},
		{"trimmed executable pointed at checkout", sourceBundle, goRunExe, trimmed, "", sourceBundle, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, dev, err := launchBundle(test.requested, test.exe, test.sourceFile, test.releasePin)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.wantBundle {
				t.Errorf("selected bundle %q, want %q", got, test.wantBundle)
			}
			if (dev != nil) != test.wantBuilder || (dev != nil && dev.Build == nil) {
				t.Errorf("development builder present = %t, want %t", dev != nil && dev.Build != nil, test.wantBuilder)
			}
		})
	}
}
