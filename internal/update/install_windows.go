package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	AppName      = "QuailCrate.exe"
	UpdaterName  = "QuailCrateUpdate.exe"
	workerName   = "QuailCrateNative.exe"
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\QuailCrate`
	updateResult = "install-result.json"
	applyWait    = 30 * time.Second
	maxRequest   = 1 << 20
)

var ErrUpdateInProgress = errors.New("an update is already being applied to this installation")

// InstallRequest is passed between the host and its independently running updater.
type InstallRequest struct {
	Root, CacheDir, Version, WorkingDir string
	Args                                []string
	Prepared                            Prepared
	Restart, Recover                    bool
}

func ReleasePublicKey(encoded string) ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil
	}
	return key
}

// CanonicalRoot resolves path aliases so instances using different working or data
// directories coordinate on the Windows installation they actually share.
func CanonicalRoot(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n >= uint32(len(buffer)) {
		return "", errors.New("installation directory path is too long")
	}
	resolved := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(resolved, `\\?\`), nil
}

type registration struct {
	location string
	version  string
}

// RegisteredInstallation identifies an installer-managed copy, including custom locations.
func RegisteredInstallation(root string) (bool, error) {
	registered, err := registeredInstallation(root)
	return registered != nil, err
}

func registeredInstallation(root string) (*registration, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer key.Close()
	location, _, err := key.GetStringValue("InstallLocation")
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Clean(location), root) {
		resolved, err := CanonicalRoot(location)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(resolved, root) {
			return nil, nil
		}
	}
	version, _, err := key.GetStringValue("DisplayVersion")
	if err != nil {
		return nil, err
	}
	return &registration{location: filepath.Clean(location), version: version}, nil
}

func CheckLaunch(root string) error {
	release, err := lockInstallation(root)
	if err != nil {
		return err
	}
	release()
	return nil
}

func lockInstallation(root string) (func(), error) {
	return waitForInstallLock(root, 0)
}

func waitForInstallLock(root string, milliseconds uint32) (func(), error) {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\QuailCrate.Update.%x`, sha256.Sum256([]byte(strings.ToLower(root)))))
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		runtime.UnlockOSThread()
		return nil, err
	}
	wait, err := windows.WaitForSingleObject(mutex, milliseconds)
	if err != nil || (wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED) {
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		if err != nil {
			return nil, err
		}
		return nil, ErrUpdateInProgress
	}
	return func() {
		windows.ReleaseMutex(mutex)
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
	}, nil
}

// LaunchInstaller copies the verified helper out of the mutable installation and
// hands it an inherited handle to the current host, rather than a reusable PID.
func LaunchInstaller(req InstallRequest, source, expectedHash string) (launchErr error) {
	defer func() {
		if launchErr != nil {
			recordResult(req.CacheDir, "install_failed", launchErr)
		}
	}()
	if decoded, err := hex.DecodeString(expectedHash); err != nil || len(decoded) != sha256.Size {
		return errors.New("invalid bundled updater hash")
	}
	if err := os.MkdirAll(req.CacheDir, 0700); err != nil {
		return err
	}
	staged := filepath.Join(req.CacheDir, "QuailCrateUpdate-"+expectedHash+".exe")
	if hash, err := fileHash(staged); err != nil || hash != expectedHash {
		if err := copyHelper(source, staged, expectedHash); err != nil {
			return err
		}
	}
	request, err := os.CreateTemp(req.CacheDir, "install-request-*.json")
	if err != nil {
		return err
	}
	name := request.Name()
	started := false
	defer func() {
		if !started {
			os.Remove(name)
		}
	}()
	encodeErr := json.NewEncoder(request).Encode(req)
	if err := request.Close(); err != nil || encodeErr != nil {
		return errors.Join(encodeErr, err)
	}
	var parent windows.Handle
	current := windows.CurrentProcess()
	if err := windows.DuplicateHandle(current, current, current, &parent, windows.SYNCHRONIZE, true, 0); err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	cmd := exec.Command(staged, name, strconv.FormatUint(uint64(parent), 10))
	cmd.Dir = req.CacheDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(parent)}}
	if err := cmd.Start(); err != nil {
		return err
	}
	started = true
	_ = cmd.Process.Release()
	return nil
}

func copyHelper(source, destination, expected string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(destination), "updater-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	if err := output.Close(); err != nil || copyErr != nil {
		return errors.Join(copyErr, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("bundled updater checksum mismatch")
	}
	return os.Rename(output.Name(), destination)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RunInstaller is the helper entry point. It validates the signed payload again
// after the original host and any other users of the installation have exited.
func RunInstaller(requestPath string, parent windows.Handle, version string, key ed25519.PublicKey) error {
	defer windows.CloseHandle(parent)
	f, err := os.Open(requestPath)
	if err != nil {
		return err
	}
	var req InstallRequest
	decodeErr := json.NewDecoder(io.LimitReader(f, maxRequest)).Decode(&req)
	f.Close()
	os.Remove(requestPath)
	if decodeErr != nil {
		return decodeErr
	}
	root, err := CanonicalRoot(req.Root)
	if err != nil || !strings.EqualFold(root, req.Root) || req.Version != version || len(key) != ed25519.PublicKeySize ||
		!filepath.IsAbs(req.CacheDir) || !strings.EqualFold(filepath.Clean(req.CacheDir), filepath.Dir(requestPath)) {
		return errors.New("invalid installation update request")
	}
	release, err := waitForInstallLock(root, uint32(applyWait/time.Millisecond))
	if err != nil {
		recordResult(req.CacheDir, "install_busy", err)
		return err
	}
	defer func() { release() }()
	ctx, cancel := context.WithTimeout(context.Background(), applyWait)
	defer cancel()
	if err := waitForInstallation(ctx, parent, root); err != nil {
		recordResult(req.CacheDir, "install_busy", err)
		release()
		release = func() {}
		wait, waitErr := windows.WaitForSingleObject(parent, 0)
		if req.Restart && !req.Recover && waitErr == nil && wait == windows.WAIT_OBJECT_0 {
			return errors.Join(err, restartApp(req))
		}
		return err
	}
	app := filepath.Join(root, AppName)
	oldApp, _ := fileHash(app)
	err = applyRequest(req, version, key)
	if err != nil {
		recordResult(req.CacheDir, "install_failed", err)
	} else {
		recordResult(req.CacheDir, "", nil)
	}
	release()
	release = func() {}
	if req.Restart {
		if err == nil {
			return restartApp(req)
		}
		if !req.Recover && oldApp != "" {
			hash, _ := fileHash(app)
			var pending bool
			var recoveryErr error
			if req.Prepared.Type == Portable {
				pending, recoveryErr = PortableNeedsRecovery(root)
			}
			if hash == oldApp && recoveryErr == nil && !pending {
				return errors.Join(err, restartApp(req))
			}
		}
	}
	return err
}

func applyRequest(req InstallRequest, version string, key ed25519.PublicKey) error {
	if req.Recover {
		return RecoverPortable(req.Root, key)
	}
	packagePath, err := windows.UTF16PtrFromString(req.Prepared.PackagePath)
	if err != nil {
		return err
	}
	// Keep the verified bytes protected against replacement until they have been used.
	packageHandle, err := windows.CreateFile(packagePath, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(packageHandle)
	manifest, _, err := VerifyPrepared(req.Prepared, key)
	if err != nil {
		return err
	}
	if !CanFollow(manifest.Version, version) {
		return errors.New("update version does not follow the running version and channel")
	}
	registered, err := registeredInstallation(req.Root)
	if err != nil {
		return err
	}
	if req.Prepared.Type == Portable && registered == nil {
		if err := RecoverPortable(req.Root, key); err != nil {
			return fmt.Errorf("recover pending portable update: %w", err)
		}
		if installed, err := PortableVersion(req.Root, key); err == nil && installed == manifest.Version {
			return nil // Another instance's helper already applied this release.
		}
		return ApplyPortable(req.Root, req.Prepared.PackagePath, manifest.Version, key)
	}
	if req.Prepared.Type != Installer || registered == nil {
		return errors.New("the registered installation or installed version changed")
	}
	if manifest.Version == registered.version {
		return nil
	}
	if !CanFollow(manifest.Version, registered.version) {
		return errors.New("update version does not follow the installed version and channel")
	}
	setup := req.Prepared.PackagePath
	command := exec.Command(setup)
	command.Dir = req.CacheDir
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CmdLine:    `"` + setup + `" /S /UPDATEONLY /D=` + registered.location,
	}
	command.Env = append(os.Environ(), "QUAILCRATE_SETUP_LOG="+filepath.Join(req.CacheDir, "setup-error.log"))
	if err := command.Run(); err != nil {
		return fmt.Errorf("run silent installer: %w", err)
	}
	registered, err = registeredInstallation(req.Root)
	if err != nil || registered == nil || registered.version != manifest.Version {
		return errors.Join(err, errors.New("installed version did not match the update"))
	}
	return nil
}

func restartApp(req InstallRequest) error {
	cmd := exec.Command(filepath.Join(req.Root, AppName), req.Args...)
	cmd.Dir = req.WorkingDir
	if err := cmd.Start(); err != nil {
		recordResult(req.CacheDir, "restart_failed", err)
		return err
	}
	return cmd.Process.Release()
}

func waitForInstallation(ctx context.Context, parent windows.Handle, root string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := windows.WaitForSingleObject(parent, 0)
		if err != nil {
			return err
		}
		if result == windows.WAIT_OBJECT_0 {
			busy, err := installationRunning(root)
			if err != nil {
				return err
			}
			if !busy {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func installationRunning(root string) (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if !strings.EqualFold(name, AppName) && !strings.EqualFold(name, workerName) {
			continue
		}
		path, err := processPath(entry.ProcessID)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_PARTIAL_COPY) {
			continue // The process exited after the snapshot.
		}
		if err != nil {
			return false, err
		}
		processRoot, err := CanonicalRoot(filepath.Dir(path))
		if err != nil {
			return false, err
		}
		if strings.EqualFold(processRoot, root) {
			return true, nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, nil
	}
	return false, err
}

func processPath(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, 32768)
	length := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &length); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:length]), nil
}

type installResult struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

func recordResult(cache, code string, err error) {
	path := filepath.Join(cache, updateResult)
	if code == "" {
		os.Remove(path)
		return
	}
	result := installResult{Code: code, Detail: err.Error()}
	data, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		_ = writeCacheFile(path, data)
	}
}

func ConsumeInstallFailure(cache string) (string, error) {
	path := filepath.Join(cache, updateResult)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var result installResult
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	os.Remove(path)
	return result.Code, errors.New(result.Detail)
}
