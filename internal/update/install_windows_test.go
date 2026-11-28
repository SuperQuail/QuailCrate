package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const handoffFixture = "QUAILCRATE_TEST_UPDATE_HANDOFF"

func TestMain(m *testing.M) {
	if os.Getenv(handoffFixture) != "" && strings.HasPrefix(filepath.Base(os.Args[0]), "QuailCrateUpdate-") && len(os.Args) == 3 {
		parent, err := strconv.ParseUint(os.Args[2], 10, 64)
		if err != nil {
			os.Exit(2)
		}
		key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
		_ = RunInstaller(os.Args[1], windows.Handle(parent), "1.0.0", key)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInstallerHandoffWaitsAndRestartsOnlyOnRequest(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(strconv.FormatBool(restart), func(t *testing.T) {
			root, err := CanonicalRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cache := t.TempDir()
			work := t.TempDir()
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			digest, err := fileHash(self)
			if err != nil {
				t.Fatal(err)
			}
			if err := copyHelper(self, filepath.Join(root, AppName), digest); err != nil {
				t.Fatal(err)
			}
			req := InstallRequest{
				Root: root, CacheDir: cache, WorkingDir: work, Version: "1.0.0", Restart: restart,
				Args:     []string{"-test.run=^TestUpdateRestartFixture$", "--", "-data-dir", "自定义 数据"},
				Prepared: Prepared{Type: Installer, PackagePath: filepath.Join(cache, PackageName(Installer))},
			}
			requestPath := filepath.Join(cache, "fixture.json")
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(requestPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(self, "-test.run=^TestUpdateHandoffParentFixture$")
			cmd.Env = append(os.Environ(), handoffFixture+"="+requestPath)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				input.Close()
				if cmd.ProcessState == nil {
					cmd.Wait()
				}
			})
			waitUpdateFile(t, filepath.Join(cache, "handoff"))
			if _, err := os.Stat(filepath.Join(cache, updateResult)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("updater ran before its parent exited: %v", err)
			}
			input.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			waitUpdateFile(t, filepath.Join(cache, updateResult))
			if code, _ := ConsumeInstallFailure(cache); code != "install_failed" {
				t.Fatalf("expected a recorded failure from the missing setup, got %q", code)
			}
			marker := filepath.Join(work, "restart")
			if restart {
				if got := waitUpdateFile(t, marker); got != "-data-dir\n自定义 数据" {
					t.Fatalf("restart changed arguments: %q", got)
				}
				waitForUpdateFixtureExit(t, root)
			} else if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("normal exit unexpectedly relaunched the application: %v", err)
			}
		})
	}
}

func waitForUpdateFixtureExit(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		busy, err := installationRunning(root)
		if err != nil {
			t.Fatal(err)
		}
		if !busy {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("restarted update fixture did not exit")
}

func waitUpdateFile(t *testing.T, name string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(name); err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for update fixture file %s", name)
	return ""
}

func TestUpdateHandoffParentFixture(t *testing.T) {
	requestPath := os.Getenv(handoffFixture)
	if requestPath == "" {
		return
	}
	data, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var req InstallRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := fileHash(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := LaunchInstaller(req, self, digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(req.CacheDir, "handoff"), []byte("waiting"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRestartFixture(t *testing.T) {
	if os.Getenv(handoffFixture) == "" {
		return
	}
	if err := os.WriteFile("restart", []byte(strings.Join(os.Args[3:], "\n")), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationGateAndActiveProcesses(t *testing.T) {
	root, err := CanonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := CanonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := lockInstallation(root)
	if err != nil {
		t.Fatal(err)
	}
	check := make(chan error, 2)
	go func() {
		check <- CheckLaunch(root)
		check <- CheckLaunch(other)
	}()
	locked, independent := <-check, <-check
	release()
	if !errors.Is(locked, ErrUpdateInProgress) || independent != nil {
		t.Fatalf("locked root = %v, independent root = %v", locked, independent)
	}
	if err := CheckLaunch(root); err != nil {
		t.Fatal(err)
	}

	stopHost := startUpdateFixture(t, filepath.Join(root, AppName))
	stopWorker := startUpdateFixture(t, filepath.Join(root, workerName))
	stopOther := startUpdateFixture(t, filepath.Join(other, AppName))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	parent, err := windows.CreateEvent(nil, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(parent)
	for _, stop := range []func(){stopHost, stopWorker} {
		short, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
		err := waitForInstallation(short, parent, root)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait for active process: %v", err)
		}
		stop()
	}
	if err := waitForInstallation(ctx, parent, root); err != nil {
		t.Fatalf("processes in other installations must not prevent the update: %v", err)
	}
	stopOther()
}

func startUpdateFixture(t *testing.T, path string) func() {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	copy, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copy, f)
	if err := errors.Join(copyErr, copy.Close()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestUpdateProcessFixture$")
	cmd.Env = append(os.Environ(), "QUAILCRATE_TEST_UPDATE_PROCESS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		t.Helper()
		if !stopped {
			stopped = true
			input.Close()
			if err := cmd.Wait(); err != nil {
				t.Errorf("stop process fixture: %v", err)
			}
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestUpdateProcessFixture(t *testing.T) {
	if os.Getenv("QUAILCRATE_TEST_UPDATE_PROCESS") != "1" {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}
