package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"quailcrate/internal/diagnostic"
	"quailcrate/internal/gioui"
	"quailcrate/internal/host"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
)

// Populated by the release build after compiling bundled native DLLs.
var musicDLLSHA256 string

func main() {
	dataDir := flag.String("data-dir", "", "override the application data directory")
	bundle := flag.String("bundle", "", "override the bundled plugin directory for development")
	trace := flag.Bool("trace", false, "log diagnostic timing")
	flag.Parse()
	if err := run(*dataDir, *bundle, *trace); err != nil {
		message := diagnostic.English(err)
		fmt.Fprintln(os.Stderr, message)
		startupMessage(message)
		os.Exit(1)
	}
}

func run(dataDir, bundle string, trace bool) error {
	if dataDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		dataDir = filepath.Join(cwd, "data")
	}
	base, err := host.PrepareDataDir(dataDir)
	if err != nil {
		return fmt.Errorf("create application data folders: %w", err)
	}
	gioui.SetFontCacheDir(filepath.Join(base, "cache", "font-cache"))
	mutexName, _ := windows.UTF16PtrFromString(fmt.Sprintf(`Local\QuailCrate-%x`, sha256.Sum256([]byte(strings.ToLower(base)))))
	handle, err := windows.CreateMutex(nil, false, mutexName)
	if handle != 0 {
		defer windows.CloseHandle(handle)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("QuailCrate is already running for this data directory.")
	}
	if err != nil {
		return fmt.Errorf("create application instance lock: %w", err)
	}
	bundle, source, err := currentBundle(bundle)
	if err != nil {
		return fmt.Errorf("resolve bundled plugin folder: %w", err)
	}
	if source != nil {
		source.TempRoot = filepath.Join(base, "cache", "dev-native")
	}
	file, err := os.OpenFile(filepath.Join(base, "logs", "quailcrate.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open application log: %w", err)
	}
	defer file.Close()
	log.SetOutput(file)
	level := slog.LevelInfo
	if trace {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(file, &slog.HandlerOptions{Level: level}))
	db, err := store.Open(base)
	if err != nil {
		return fmt.Errorf("open application database: %w", err)
	}
	defer db.Close()
	catalog := plugins.Catalog{
		TrustedDLLs: map[string]string{
			plugins.MusicDLLID: musicDLLSHA256,
		},
		SourceNative: source,
	}
	if source != nil {
		defer func() {
			if err := source.Close(); err != nil {
				logger.Warn("remove native development artifacts", "error", err)
			}
		}()
	}
	logger.Info("starting QuailCrate", "bundle", bundle, "dataDir", base)
	return host.New(db, logger, bundle, filepath.Join(base, "user-plugins"), &catalog).Run()
}

func startupMessage(message string) {
	text, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	title, _ := windows.UTF16PtrFromString("QuailCrate could not start")
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(
		0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10,
	)
}
