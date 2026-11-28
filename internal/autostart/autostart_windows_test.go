package autostart

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStartupCommand(t *testing.T) {
	executable := `D:\鹌鹑 工具箱\QuailCrate.exe`
	for _, tc := range []struct {
		name, workDir, dataDir string
		args                   []string
	}{
		{
			name: "defaults", workDir: `D:\鹌鹑 工具箱`, dataDir: `D:\鹌鹑 工具箱\data`,
		},
		{
			name: "custom working directory", workDir: `D:\目录 with spaces`, dataDir: `D:\目录 with spaces\data`,
			args: []string{`-startup-workdir=D:\目录 with spaces`},
		},
		{
			name: "custom profile", workDir: `D:\工作`, dataDir: `\\server\share\个人 数据`,
			args: []string{`-startup-workdir=D:\工作`, `-data-dir=\\server\share\个人 数据`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller, err := New(executable, tc.workDir, tc.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(controller.command, `"`+executable+`" -startup`) {
				t.Fatalf("unexpected startup command: %s", controller.command)
			}
			args, err := windows.DecomposeCommandLine(controller.command)
			want := append([]string{executable, "-startup"}, tc.args...)
			if err != nil || !slices.Equal(args, want) {
				t.Fatalf("decoded startup command = %q, %v; want %q", args, err, want)
			}
		})
	}

	first, err := New(`D:\Quail\QuailCrate.exe`, `D:\Quail`, `D:\Quail\data`)
	if err != nil || first.command != `"D:\Quail\QuailCrate.exe" -startup` {
		t.Fatalf("startup command for an executable without spaces = %v, %v", first, err)
	}
}

func TestUnavailableStartupCommand(t *testing.T) {
	if _, err := New(`QuailCrate.exe`, `D:\Quail`, `D:\Quail\data`); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("relative executable = %v", err)
	}
	const fixed = `"D:\\QuailCrate.exe" -startup`
	dir := `D:\` + strings.Repeat("a", maxRunCommand-len(fixed))
	if _, err := New(filepath.Join(dir, "QuailCrate.exe"), dir, filepath.Join(dir, "data")); err != nil {
		t.Fatalf("command at the Windows limit is unavailable: %v", err)
	}
	// A supplementary Unicode character occupies two UTF-16 code units.
	dir = strings.TrimSuffix(dir, "a") + "🐣"
	if _, err := New(filepath.Join(dir, "QuailCrate.exe"), dir, filepath.Join(dir, "data")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("command exceeding the Windows limit = %v", err)
	}
	var unsupported *Controller
	if state, err := unsupported.Status(); state != (Status{}) || err != nil {
		t.Fatalf("unsupported state = %v, %v", state, err)
	}
	if err := unsupported.SetEnabled(true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("enable unsupported registration = %v", err)
	}
}

func TestControllerPreservesOtherProfiles(t *testing.T) {
	executable := `D:\Quail\QuailCrate.exe`
	controller, err := New(executable, `D:\Quail`, `D:\Quail\data`)
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(executable, `D:\Quail`, `D:\Other Profile`)
	if err != nil {
		t.Fatal(err)
	}
	entry := &testRunStore{t: t}
	controller.store, other.store = entry, entry

	checkStatus := func(c *Controller, want Status) {
		t.Helper()
		if state, err := c.Status(); state != want || err != nil {
			t.Fatalf("startup state = %v, %v; want %v", state, err, want)
		}
	}
	checkStatus(controller, Status{Available: true})
	if err := controller.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	checkStatus(controller, Status{Available: true, Enabled: true})
	checkStatus(other, Status{Available: true, Conflict: true})
	for _, enabled := range []bool{true, false} {
		if err := other.SetEnabled(enabled); !errors.Is(err, ErrConflict) {
			t.Fatalf("modify the other profile's registration = %v", err)
		}
	}
	if entry.writes != 1 || entry.removals != 0 || entry.command != controller.command {
		t.Fatal("a redundant or conflicting request modified the startup registration")
	}
	if err := controller.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	checkStatus(controller, Status{Available: true})
	if entry.removals != 1 || entry.present {
		t.Fatal("the owner's registration was not removed")
	}
}

type testRunStore struct {
	t                *testing.T
	command          string
	present, locked  bool
	writes, removals int
}

func (s *testRunStore) lock() (func(), error) {
	s.locked = true
	return func() { s.locked = false }, nil
}

func (s *testRunStore) read() (string, bool, error) { return s.command, s.present, nil }

func (s *testRunStore) write(command string) error {
	if !s.locked {
		s.t.Fatal("startup registration write outside the lock")
	}
	s.command, s.present = command, true
	s.writes++
	return nil
}

func (s *testRunStore) remove() error {
	if !s.locked {
		s.t.Fatal("startup registration removal outside the lock")
	}
	s.command, s.present = "", false
	s.removals++
	return nil
}
