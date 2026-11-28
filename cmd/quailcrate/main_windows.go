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
	"quailcrate/internal/update"
)

var releaseVersion = "0.0.0-dev"
var releaseUpdatePublicKey string
var releaseUpdateHelperSHA256 string

type loginStartup struct {
	enabled    bool
	workingDir string
}

func main() {
	dataDir := flag.String("data-dir", "", "override the application data directory")
	bundle := flag.String("bundle", "", "override the bundled plugin directory for development")
	trace := flag.Bool("trace", false, "log diagnostic timing")
	startup := flag.Bool("startup", false, "launch from the Windows sign-in entry")
	startupWorkDir := flag.String("startup-workdir", "", "working directory recorded for a sign-in launch")
	flag.Parse()
	if err := run(*dataDir, *bundle, *trace, loginStartup{enabled: *startup, workingDir: *startupWorkDir}); err != nil {
		message := diagnostic.English(err)
		fmt.Fprintln(os.Stderr, message)
		if !*startup {
			startupMessage(message)
		}
		os.Exit(1)
	}
}

func run(dataDir, bundle string, trace bool, startup loginStartup) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve application executable: %w", err)
	}
	if startup.enabled {
		workDir := startup.workingDir
		if workDir == "" {
			workDir = filepath.Dir(executable)
		}
		if !filepath.IsAbs(workDir) {
			return fmt.Errorf("login startup working directory must be absolute")
		}
		if err := os.Chdir(workDir); err != nil {
			return fmt.Errorf("set login startup working directory: %w", err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	key := update.ReleasePublicKey(releaseUpdatePublicKey)
	var root string
	if key != nil {
		root, err = update.CanonicalRoot(filepath.Dir(executable))
		if err != nil {
			return fmt.Errorf("resolve application installation: %w", err)
		}
		if err := update.CheckLaunch(root); err != nil {
			if err == update.ErrUpdateInProgress {
				return nil
			}
			return fmt.Errorf("check application update lock: %w", err)
		}
	}
	if dataDir == "" {
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
		if startup.enabled {
			return nil
		}
		return fmt.Errorf("QuailCrate is already running for this data directory.")
	}
	if err != nil {
		return fmt.Errorf("create application instance lock: %w", err)
	}
	cache := filepath.Join(base, "cache", "updates")
	req := update.InstallRequest{Root: root, CacheDir: cache, Version: releaseVersion, WorkingDir: cwd, Args: os.Args[1:]}
	if root != "" {
		pending, err := update.PortableNeedsRecovery(root)
		if err != nil {
			return fmt.Errorf("check interrupted update: %w", err)
		}
		if pending {
			source, err := update.PortableRecoveryHelper(root, releaseUpdateHelperSHA256)
			if err != nil {
				return fmt.Errorf("find updater for recovery: %w", err)
			}
			req.Recover, req.Restart = true, true
			return update.LaunchInstaller(req, source, releaseUpdateHelperSHA256)
		}
	}
	customBundle := bundle != ""
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
	catalog := plugins.Catalog{SourceNative: source}
	if source != nil {
		defer func() {
			if err := source.Close(); err != nil {
				logger.Warn("remove native development artifacts", "error", err)
			}
		}()
	}
	logger.Info("starting QuailCrate", "bundle", bundle, "dataDir", base)
	app := host.New(db, logger, bundle, filepath.Join(base, "user-plugins"), &catalog)
	if !customBundle && source == nil && strings.EqualFold(filepath.Base(executable), update.AppName) {
		if info, err := os.Stat(bundle); err == nil && info.IsDir() {
			app.ConfigureAutostart(executable, cwd, base)
		}
	}
	config := update.Config{Version: releaseVersion, CacheDir: cache, Settings: db}
	var launch func(update.Prepared, bool) error
	if key != nil && source == nil {
		installed, err := update.RegisteredInstallation(root)
		if err != nil {
			logger.Warn("read registered installation for updates", "error", err)
		} else {
			config.AssetType = update.PortableManual
			if installed {
				config.AssetType = update.Installer
			} else if manifest, err := update.ReadPortableManifest(root, key); err == nil && manifest.Version == releaseVersion && update.PortableCanApply(root) {
				config.AssetType = update.Portable
			}
			config.PublicKey = key
			launch = func(prepared update.Prepared, restart bool) error {
				request := req
				request.Prepared, request.Restart = prepared, restart
				return update.LaunchInstaller(request, filepath.Join(root, update.UpdaterName), releaseUpdateHelperSHA256)
			}
		}
	}
	app.ConfigureUpdates(config, launch)
	return app.Run()
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
