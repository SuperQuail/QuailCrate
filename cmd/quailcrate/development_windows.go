package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"quailcrate/internal/plugins"
)

// Only a host built from this checkout may run its native build helper. A
// release pin, an installed bundle, or a foreign -bundle cannot select one.
func launchBundle(requested, executable, sourceFile, releasePin string) (string, *plugins.SourceNative, error) {
	installed := filepath.Join(filepath.Dir(executable), "plugins")
	var checkout string
	if releasePin == "" && filepath.IsAbs(sourceFile) {
		root := filepath.Dir(filepath.Dir(filepath.Dir(sourceFile)))
		module, err := os.ReadFile(filepath.Join(root, "go.mod"))
		if err == nil && strings.TrimSpace(strings.SplitN(string(module), "\n", 2)[0]) == "module quailcrate" {
			checkout = root
		}
	}
	if requested == "" {
		requested = installed
		if _, err := os.Stat(installed); os.IsNotExist(err) && checkout != "" {
			requested = filepath.Join(checkout, "plugins")
		}
	}
	bundle, err := filepath.Abs(requested)
	if err != nil {
		return "", nil, err
	}
	if checkout != "" {
		source, srcErr := os.Stat(filepath.Join(checkout, "plugins"))
		selected, selErr := os.Stat(bundle)
		if srcErr == nil && selErr == nil && source.IsDir() && os.SameFile(source, selected) {
			return bundle, &plugins.SourceNative{Build: func(ctx context.Context, dir string) error {
				return buildSourceNative(ctx, checkout, dir)
			}}, nil
		}
	}
	return bundle, nil, nil
}

func buildSourceNative(ctx context.Context, root, output string) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	shell := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "RemoteSigned", "-File", filepath.Join(root, "scripts", "build-native.ps1"), "-OutputDir", output)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	log, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("native build helper: %w\n%s", err, strings.TrimSpace(string(log)))
	}
	return nil
}

func currentBundle(requested string) (string, *plugins.SourceNative, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	_, file, _, _ := runtime.Caller(0)
	return launchBundle(requested, exe, file, musicDLLSHA256)
}
