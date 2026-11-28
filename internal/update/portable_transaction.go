package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	portableUpdateCache = "data/cache/updates"
	portableTransaction = portableUpdateCache + "/portable-transaction-v1"
	portableJournalName = "journal.json"
	portableBackupDir   = "backup"
	portableStagedDir   = "staged"
	portableOwnerName   = "owner"
	portableOwner       = "QuailCrate portable update, schema 1\n"
)

type portableJournal struct {
	Committed   bool             `json:"committed"`
	Changes     []portableChange `json:"changes"`
	CreatedDirs []string         `json:"created_directories,omitempty"`
}

type portableChange struct {
	Path     string        `json:"path"`
	Original *PortableFile `json:"original,omitempty"`
}

// ApplyPortable applies a staged, signed portable bundle. The caller must hold
// the installation's exclusive update lock until application or rollback ends.
func ApplyPortable(root, archive, version string, key ed25519.PublicKey) error {
	root, err := portableRoot(root)
	if err != nil {
		return err
	}
	if err := recoverPortable(root, key); err != nil {
		return fmt.Errorf("recover previous portable update: %w", err)
	}
	installed, previous, err := loadPortableInventory(root, key)
	if err != nil {
		return err
	}
	if !CanFollow(version, installed.Version) {
		return errors.New("portable update version does not follow the installed version and channel")
	}
	transaction, info, err := inspectPortablePath(root, portableTransaction)
	if err != nil {
		return err
	}
	if info != nil {
		return fmt.Errorf("%w: %s", ErrPortableConflict, transaction)
	}
	if err := os.MkdirAll(filepath.Dir(transaction), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(transaction, 0700); err != nil {
		return err
	}
	defer func() {
		if _, err := os.Lstat(filepath.Join(transaction, portableJournalName)); errors.Is(err, fs.ErrNotExist) {
			_ = os.RemoveAll(transaction)
		}
	}()
	if err := os.WriteFile(filepath.Join(transaction, portableOwnerName), []byte(portableOwner), 0600); err != nil {
		return err
	}
	stage := filepath.Join(transaction, portableStagedDir)
	backup := filepath.Join(transaction, portableBackupDir)
	incoming, err := stagePortableArchive(archive, stage, version, key)
	if err != nil {
		return err
	}
	journal, err := preparePortableChanges(root, backup, previous, incoming)
	if err != nil {
		return err
	}
	if err := savePortableJournal(transaction, journal, false); err != nil {
		return err
	}
	if err := applyPortableChanges(root, transaction, incoming, journal); err != nil {
		return errors.Join(err, recoverPortable(root, key))
	}
	journal.Committed = true
	if err := savePortableJournal(transaction, journal, true); err != nil {
		return errors.Join(err, recoverPortable(root, key))
	}
	return finishPortableTransaction(transaction)
}

func preparePortableChanges(root, backup string, previous, incoming map[string]PortableFile) (portableJournal, error) {
	journal := portableJournal{}
	directories := make(map[string]bool)
	for _, relative := range portableChangeOrder(previous, incoming) {
		id := strings.ToLower(relative)
		old, owned := previous[id]
		file, installing := incoming[id]
		if owned && installing && old.Path != file.Path {
			return portableJournal{}, fmt.Errorf("%w: case-only path change %s", ErrPortableConflict, relative)
		}
		destination, info, err := inspectPortablePath(root, relative)
		if err != nil {
			return portableJournal{}, err
		}
		change := portableChange{Path: relative}
		if info != nil {
			if !owned || !info.Mode().IsRegular() {
				return portableJournal{}, fmt.Errorf("%w: %s", ErrPortableConflict, relative)
			}
			copyPath := filepath.Join(backup, filepath.FromSlash(relative))
			original, err := copyPortableFile(destination, copyPath, relative)
			if err != nil {
				return portableJournal{}, fmt.Errorf("back up %s: %w", relative, err)
			}
			change.Original = &original
		} else if relative == portableHost {
			return portableJournal{}, fmt.Errorf("%w: missing %s", ErrPortableConflict, relative)
		}
		if installing {
			for parent := path.Dir(relative); parent != "."; parent = path.Dir(parent) {
				_, parentInfo, err := inspectPortablePath(root, parent)
				if err != nil {
					return portableJournal{}, err
				}
				if parentInfo != nil {
					if !parentInfo.IsDir() {
						return portableJournal{}, fmt.Errorf("%w: %s", ErrPortableConflict, parent)
					}
					break
				}
				directories[parent] = true
			}
		}
		journal.Changes = append(journal.Changes, change)
	}
	for directory := range directories {
		journal.CreatedDirs = append(journal.CreatedDirs, directory)
	}
	sort.Strings(journal.CreatedDirs)
	return journal, nil
}

func portableChangeOrder(previous, incoming map[string]PortableFile) []string {
	paths := make([]string, 0, len(previous)+len(incoming))
	for id, file := range previous {
		if _, found := incoming[id]; !found {
			paths = append(paths, file.Path)
		}
	}
	for _, file := range incoming {
		if file.Path != portableHost {
			paths = append(paths, file.Path)
		}
	}
	sort.Strings(paths)
	return append(paths, portableHost)
}

func applyPortableChanges(root, transaction string, incoming map[string]PortableFile, journal portableJournal) error {
	for _, change := range journal.Changes {
		target, info, err := inspectPortablePath(root, change.Path)
		if err != nil {
			return err
		}
		hadOriginal := change.Original != nil
		if (info != nil) != hadOriginal || info != nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", ErrPortableConflict, change.Path)
		}
		if hadOriginal {
			actual, err := describePortableFile(target, change.Path)
			if err != nil || actual != *change.Original {
				return errors.Join(err, fmt.Errorf("%w: %s", ErrPortableConflict, change.Path))
			}
		}
		if file, installing := incoming[strings.ToLower(change.Path)]; installing {
			if err := replacePortableFile(transaction, portableStagedDir, file, target, hadOriginal); err != nil {
				return fmt.Errorf("install %s: %w", change.Path, err)
			}
		} else if hadOriginal {
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("remove retired %s: %w", change.Path, err)
			}
		}
	}
	return nil
}

func replacePortableFile(transaction, sourceDir string, expected PortableFile, target string, replace bool) error {
	sourceRoot := filepath.Join(transaction, sourceDir)
	source, info, err := inspectPortablePath(sourceRoot, expected.Path)
	if err != nil {
		return err
	}
	if info == nil || !info.Mode().IsRegular() || info.Size() != expected.Size {
		return fmt.Errorf("invalid portable transaction file: %s", expected.Path)
	}
	temporary, err := os.CreateTemp(transaction, "replace-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Close(); err != nil {
		return err
	}
	actual, err := copyPortableTo(source, name, expected.Path, os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("portable transaction file changed: %s", expected.Path)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return publishPortableFile(name, target, replace)
}

// PortableCanApply reports whether the installation can stage an update on the
// same volume without traversing a redirected data directory.
func PortableCanApply(root string) bool {
	root, err := portableRoot(root)
	if err != nil {
		return false
	}
	_, info, err := inspectPortablePath(root, portableTransaction)
	return err == nil && (info == nil || info.IsDir())
}

// PortableNeedsRecovery reports whether a portable transaction reached disk.
func PortableNeedsRecovery(root string) (bool, error) {
	root, err := portableRoot(root)
	if errors.Is(err, errPortableDriveRoot) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, parent, err := inspectPortablePath(root, portableUpdateCache)
	if errors.Is(err, ErrPortableConflict) || errors.Is(err, fs.ErrPermission) || (err == nil && (parent == nil || !parent.IsDir())) {
		// A redirected data directory can be used by the application. The
		// updater never stages transactions through a redirected path.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, info, err := inspectPortablePath(root, portableTransaction+"/"+portableJournalName)
	return info != nil, err
}

// RecoverPortable restores an interrupted update or cleans a committed one.
// The portable process and all other instances must be stopped by the caller.
func RecoverPortable(root string, key ed25519.PublicKey) error {
	root, err := portableRoot(root)
	if err != nil {
		return err
	}
	return recoverPortable(root, key)
}

func recoverPortable(root string, key ed25519.PublicKey) error {
	transaction, info, err := inspectPortablePath(root, portableTransaction)
	if err != nil || info == nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrPortableConflict, transaction)
	}
	owner, err := readPortableMetadata(transaction, portableOwnerName, int64(len(portableOwner)))
	if errors.Is(err, fs.ErrNotExist) {
		// A process may have stopped immediately after creating the directory.
		// A nonempty directory without our marker belongs to someone else.
		if err := os.Remove(transaction); err != nil {
			return errors.Join(err, fmt.Errorf("%w: %s", ErrPortableConflict, transaction))
		}
		return nil
	}
	if err != nil || string(owner) != portableOwner {
		return errors.Join(err, fmt.Errorf("%w: %s", ErrPortableConflict, transaction))
	}
	contents, err := readPortableMetadata(transaction, portableJournalName, portableMetadataLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return os.RemoveAll(transaction)
	}
	if err != nil {
		return err
	}
	var journal portableJournal
	if err := json.Unmarshal(contents, &journal); err != nil {
		return fmt.Errorf("decode portable update journal: %w", err)
	}
	if journal.Committed {
		return finishPortableTransaction(transaction)
	}
	backup := filepath.Join(transaction, portableBackupDir)
	stage := filepath.Join(transaction, portableStagedDir)
	_, previous, err := loadPortableInventory(backup, key)
	if err != nil {
		return fmt.Errorf("verify previous portable ownership: %w", err)
	}
	_, incoming, err := loadPortableInventory(stage, key)
	if err != nil {
		return fmt.Errorf("verify staged portable ownership: %w", err)
	}
	if err := validatePortableJournal(journal, previous, incoming); err != nil {
		return err
	}
	var recoveryErr error
	for _, change := range journal.Changes {
		err := restorePortableChange(root, transaction, change, incoming)
		if err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("restore %s: %w", change.Path, err))
		}
	}
	if recoveryErr != nil {
		return recoveryErr
	}
	for i := len(journal.CreatedDirs) - 1; i >= 0; i-- {
		name, info, err := inspectPortablePath(root, journal.CreatedDirs[i])
		if err == nil && info != nil && info.IsDir() {
			// Remove only an empty directory. A user may have added other files.
			_ = os.Remove(name)
		}
	}
	return finishPortableTransaction(transaction)
}

func validatePortableJournal(journal portableJournal, previous, incoming map[string]PortableFile) error {
	paths := portableChangeOrder(previous, incoming)
	if len(journal.Changes) != len(paths) {
		return errors.New("portable update journal inventory changed")
	}
	for i, change := range journal.Changes {
		if change.Path != paths[i] {
			return errors.New("portable update journal path changed")
		}
		if change.Original != nil {
			if previous[strings.ToLower(change.Path)].Path != change.Path || change.Original.Path != change.Path {
				return errors.New("portable update journal contains an unowned backup")
			}
		} else if change.Path == portableHost || portableMetadata(change.Path) {
			return errors.New("portable update journal is missing its original package files")
		}
	}
	parents := make(map[string]bool)
	for _, file := range incoming {
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			parents[parent] = true
		}
	}
	for _, directory := range journal.CreatedDirs {
		if !parents[directory] {
			return errors.New("portable update journal contains an unowned directory")
		}
	}
	return nil
}

func restorePortableChange(root, transaction string, change portableChange, incoming map[string]PortableFile) error {
	target, info, err := inspectPortablePath(root, change.Path)
	if err != nil {
		return err
	}
	if info != nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", ErrPortableConflict, change.Path)
		}
		file, err := describePortableFile(target, change.Path)
		if err != nil {
			return err
		}
		if change.Original != nil && file == *change.Original {
			return nil
		}
		if file != incoming[strings.ToLower(change.Path)] {
			return fmt.Errorf("%w: %s", ErrPortableConflict, change.Path)
		}
	}
	if change.Original != nil {
		return replacePortableFile(transaction, portableBackupDir, *change.Original, target, info != nil)
	}
	if info != nil {
		return os.Remove(target)
	}
	return nil
}

// PortableRecoveryHelper finds the helper matching the executing host even when
// the helper file at the install root has already moved to the other version.
func PortableRecoveryHelper(root, expectedSHA256 string) (string, error) {
	root, err := portableRoot(root)
	if err != nil {
		return "", err
	}
	for _, relative := range []string{portableHelper, portableTransaction + "/" + portableBackupDir + "/" + portableHelper, portableTransaction + "/" + portableStagedDir + "/" + portableHelper} {
		name, info, err := inspectPortablePath(root, relative)
		if err != nil {
			return "", err
		}
		if info == nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := describePortableFile(name, relative)
		if err == nil && file.SHA256 == expectedSHA256 {
			return name, nil
		}
	}
	return "", errors.New("no matching portable recovery helper")
}

func savePortableJournal(transaction string, journal portableJournal, replace bool) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if len(data) > portableMetadataLimit {
		return errors.New("portable update journal is too large")
	}
	temporary, err := os.CreateTemp(transaction, "journal-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	_, writeErr := temporary.Write(data)
	if err := errors.Join(writeErr, temporary.Sync(), temporary.Close()); err != nil {
		return err
	}
	return publishPortableFile(name, filepath.Join(transaction, portableJournalName), replace)
}

func finishPortableTransaction(transaction string) error {
	if err := os.Remove(filepath.Join(transaction, portableJournalName)); err != nil {
		return err
	}
	// The installation is consistent once the journal is removed. Leftover
	// transaction files can be discarded when the next update begins.
	_ = os.RemoveAll(transaction)
	return nil
}

func copyPortableFile(source, destination, relative string) (PortableFile, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return PortableFile{}, err
	}
	return copyPortableTo(source, destination, relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
}

func copyPortableTo(source, destination, relative string, flags int) (PortableFile, error) {
	input, err := os.Open(source)
	if err != nil {
		return PortableFile{}, err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, flags, 0600)
	if err != nil {
		return PortableFile{}, err
	}
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, digest), input)
	if err := errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
		return PortableFile{}, err
	}
	return PortableFile{Path: relative, Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}
