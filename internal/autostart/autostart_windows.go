package autostart

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKey         = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue       = "QuailCrate"
	mutexPrefix    = `Global\QuailCrate.Autostart.`
	maxRunCommand  = 260
	lockWaitMillis = 5000
)

var (
	ErrUnavailable = errors.New("login startup is unavailable")
	ErrConflict    = errors.New("another QuailCrate launch command owns the login startup entry")
)

// Status describes the registration for this installation and data directory.
// Windows can separately pause a registered app in Startup Apps.
type Status struct {
	Available bool
	Enabled   bool
	Conflict  bool
}

type runStore interface {
	lock() (func(), error)
	read() (command string, exists bool, err error)
	write(command string) error
	remove() error
}

type Controller struct {
	command string
	store   runStore
}

// New prepares a current-user login registration without opening the registry.
func New(executable, workDir, dataDir string) (*Controller, error) {
	for _, path := range []string{executable, workDir, dataDir} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n\"") {
			return nil, fmt.Errorf("%w: startup paths must be valid absolute paths", ErrUnavailable)
		}
	}
	executable, workDir, dataDir = filepath.Clean(executable), filepath.Clean(workDir), filepath.Clean(dataDir)
	parts := []string{`"` + executable + `"`, "-startup"}
	if !strings.EqualFold(workDir, filepath.Dir(executable)) {
		parts = append(parts, syscall.EscapeArg("-startup-workdir="+workDir))
	}
	if !strings.EqualFold(dataDir, filepath.Join(workDir, "data")) {
		parts = append(parts, syscall.EscapeArg("-data-dir="+dataDir))
	}
	command := strings.Join(parts, " ")
	if len(utf16.Encode([]rune(command))) > maxRunCommand {
		return nil, fmt.Errorf("%w: startup command exceeds the %d-character Windows Run limit", ErrUnavailable, maxRunCommand)
	}
	return &Controller{command: command, store: windowsRunStore{}}, nil
}

func (c *Controller) Status() (Status, error) {
	if c == nil || c.store == nil {
		return Status{}, nil
	}
	command, exists, err := c.store.read()
	if err != nil {
		return Status{}, err
	}
	owns := exists && strings.EqualFold(command, c.command)
	return Status{Available: true, Enabled: owns, Conflict: exists && !owns}, nil
}

func (c *Controller) SetEnabled(enabled bool) error {
	if c == nil || c.store == nil {
		return ErrUnavailable
	}
	unlock, err := c.store.lock()
	if err != nil {
		return err
	}
	defer unlock()
	status, err := c.Status()
	if err != nil {
		return err
	}
	if status.Conflict {
		return ErrConflict
	}
	if status.Enabled == enabled {
		return nil
	}
	if enabled {
		return c.store.write(c.command)
	}
	return c.store.remove()
}

type windowsRunStore struct{}

func (windowsRunStore) lock() (func(), error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read startup user: %w", err)
	}
	name, err := windows.UTF16PtrFromString(mutexPrefix + user.User.Sid.String())
	if err != nil {
		return nil, err
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, fmt.Errorf("open startup registration lock: %w", err)
	}
	runtime.LockOSThread()
	result, waitErr := windows.WaitForSingleObject(mutex, lockWaitMillis)
	if waitErr != nil || (result != windows.WAIT_OBJECT_0 && result != windows.WAIT_ABANDONED) {
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
		if waitErr != nil {
			return nil, fmt.Errorf("wait for startup registration lock: %w", waitErr)
		}
		return nil, errors.New("startup registration lock timed out")
	}
	return func() {
		windows.ReleaseMutex(mutex)
		windows.CloseHandle(mutex)
		runtime.UnlockOSThread()
	}, nil
}

func (windowsRunStore) read() (string, bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.WOW64_64KEY|registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open startup registrations: %w", err)
	}
	defer key.Close()
	value, kind, err := key.GetStringValue(runValue)
	switch {
	case errors.Is(err, registry.ErrNotExist):
		return "", false, nil
	case errors.Is(err, registry.ErrUnexpectedType):
		return "", true, nil // Preserve a value of any type that this app did not write.
	case err != nil:
		return "", false, fmt.Errorf("read startup registration: %w", err)
	case kind != registry.SZ:
		return "", true, nil
	default:
		return value, true, nil
	}
}

func (windowsRunStore) write(command string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.WOW64_64KEY|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open startup registrations for writing: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(runValue, command); err != nil {
		return fmt.Errorf("write startup registration: %w", err)
	}
	return nil
}

func (windowsRunStore) remove() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.WOW64_64KEY|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open startup registrations for removal: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("remove startup registration: %w", err)
	}
	return nil
}
