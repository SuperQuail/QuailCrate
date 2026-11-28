// Package pluginfiles brokers access to private storage and user-picked directories.
package pluginfiles

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/windows"
	"quailcrate/internal/store"
	v2 "quailcrate/pluginapi/v2"
)

const (
	maxFile              = 512 << 10
	maxEntries           = 2048
	privateQuota         = 16 << 20
	privateDirectoryName = "plugin-data"
)

var ErrDenied = errors.New("directory access denied")

type Service struct {
	db      *store.Store
	profile string
	bundle  string
	mu      sync.Mutex // revocation and quota checks serialize with filesystem operations
}

func New(db *store.Store, profile, bundle string) *Service {
	return &Service{db: db, profile: profile, bundle: bundle}
}

// PrivateDataDir waits for current file operations and returns the plugin's private directory.
// The host calls it after stopping the plugin when removing its files.
func (s *Service) PrivateDataDir(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filepath.Join(s.profile, privateDirectoryName, ownerKey(id))
}

type Directory struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Access string `json:"access"`
}

type grant struct {
	Directory
	Identity string `json:"identity"`
}

func ownerKey(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:])
}

func (s *Service) grants(ctx context.Context, id string) ([]grant, error) {
	value, err := s.db.Setting(ctx, store.DirectoryGrantsSettingKey(id), "[]")
	if err != nil {
		return nil, err
	}
	var list []grant
	err = json.Unmarshal([]byte(value), &list)
	return list, err
}

func (s *Service) save(ctx context.Context, id string, list []grant) error {
	value, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.db.SetSetting(ctx, store.DirectoryGrantsSettingKey(id), string(value))
}

func inside(parent, name string) bool {
	rel, err := filepath.Rel(parent, name)
	return err == nil && filepath.IsLocal(rel)
}

// Match a newly opened root to the exact directory the user originally picked.
func identity(root *os.Root) (string, error) {
	dir, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer dir.Close()
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(dir.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

// Grant is for a trusted directory picker completion, never a path in plugin JSON.
func (s *Service) Grant(ctx context.Context, owner, selected, access string) (Directory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !filepath.IsAbs(selected) || (access != "read" && access != "write") {
		return Directory{}, ErrDenied
	}
	resolved, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return Directory{}, err
	}
	for _, protected := range []string{s.profile, s.bundle} {
		if protected == "" {
			continue
		}
		canonical, err := filepath.EvalSymlinks(protected)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Directory{}, err
		}
		if canonical == "" {
			canonical = protected
		}
		if inside(canonical, resolved) || inside(resolved, canonical) {
			return Directory{}, ErrDenied
		}
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return Directory{}, err
	}
	defer root.Close()
	key, err := identity(root)
	if err != nil {
		return Directory{}, err
	}
	list, err := s.grants(ctx, owner)
	if err != nil {
		return Directory{}, err
	}
	for i, g := range list {
		if g.Identity == key && strings.EqualFold(g.Path, resolved) {
			list[i].Access = access
			return list[i].Directory, s.save(ctx, owner, list)
		}
	}
	if len(list) >= 32 {
		return Directory{}, errors.New("directory grant limit reached")
	}
	d := Directory{ID: rand.Text(), Path: resolved, Access: access}
	return d, s.save(ctx, owner, append(list, grant{Directory: d, Identity: key}))
}

func (s *Service) Execute(ctx context.Context, owner, action string, a v2.ToolArgs) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if action == "files.directories" || action == "files.revokeDirectory" {
		list, err := s.grants(ctx, owner)
		if err != nil {
			return nil, err
		}
		if action == "files.revokeDirectory" {
			return nil, s.save(ctx, owner, slices.DeleteFunc(list, func(g grant) bool { return g.ID == a.Root }))
		}
		out := make([]Directory, 0, len(list))
		for _, g := range list {
			out = append(out, g.Directory)
		}
		return out, nil
	}
	private := a.Root == "" || a.Root == "private"
	write := action == "files.write" || action == "files.remove" || action == "files.mkdir"
	root, err := s.open(ctx, owner, a.Root, write)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := a.Path
	if name == "" && action == "files.list" {
		name = "."
	}
	if !fs.ValidPath(name) || name == "." && action != "files.list" || strings.ContainsAny(name, "\\:\x00") || len(name) > 1024 {
		return nil, ErrDenied
	}
	if _, err := filepath.Localize(name); err != nil {
		return nil, ErrDenied
	}
	switch action {
	case "files.list":
		dir, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		entries, err := dir.ReadDir(maxEntries + 1)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(entries) > maxEntries {
			return nil, errors.New("directory listing exceeds limit")
		}
		type entry struct {
			Name      string `json:"name"`
			Directory bool   `json:"directory"`
		}
		out := make([]entry, 0, len(entries))
		for _, e := range entries {
			out = append(out, entry{e.Name(), e.IsDir()})
		}
		return out, nil
	case "files.read":
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return nil, ErrDenied
		}
		b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxFile {
			return nil, errors.New("file exceeds size limit")
		}
		if utf8.Valid(b) {
			return map[string]string{"body": string(b), "encoding": "utf8"}, nil
		}
		return map[string]string{"body": base64.StdEncoding.EncodeToString(b), "encoding": "base64"}, nil
	case "files.remove":
		return nil, root.Remove(name)
	case "files.mkdir", "files.write":
		data := []byte(a.Body)
		if a.Encoding == "base64" {
			data, err = base64.StdEncoding.DecodeString(a.Body)
			if err != nil {
				return nil, errors.New("invalid base64 file content")
			}
		}
		if len(data) > maxFile {
			return nil, errors.New("file exceeds size limit")
		}
		if private {
			if err := checkQuota(ctx, root, name, len(data)); err != nil {
				return nil, err
			}
		}
		if action == "files.mkdir" {
			return nil, root.Mkdir(name, 0700)
		}
		return nil, replace(root, name, data)
	default:
		return nil, errors.New("unknown file operation")
	}
}

func (s *Service) open(ctx context.Context, owner, ref string, write bool) (*os.Root, error) {
	if ref == "" || ref == "private" {
		profile, err := os.OpenRoot(s.profile)
		if err != nil {
			return nil, err
		}
		defer profile.Close()
		data, err := privateChild(profile, privateDirectoryName)
		if err != nil {
			return nil, err
		}
		defer data.Close()
		return privateChild(data, ownerKey(owner))
	}
	list, err := s.grants(ctx, owner)
	if err != nil {
		return nil, err
	}
	for _, g := range list {
		if g.ID != ref {
			continue
		}
		if write && g.Access != "write" {
			return nil, ErrDenied
		}
		root, err := os.OpenRoot(g.Path)
		if err != nil {
			return nil, err
		}
		key, err := identity(root)
		if err != nil || key != g.Identity {
			root.Close()
			return nil, ErrDenied
		}
		return root, nil
	}
	return nil, ErrDenied
}

// Open each managed directory relative to its parent. A private root is owned
// by one plugin; a link to a sibling would give it the wrong namespace.
func privateChild(parent *os.Root, name string) (*os.Root, error) {
	if err := parent.Mkdir(name, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, ErrDenied
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, ErrDenied
	}
	return root, nil
}

func checkQuota(ctx context.Context, root *os.Root, name string, size int) error {
	total, count := int64(size), 1
	// Windows can resolve a different path spelling to the same file. Subtract
	// the actual replaced entry once before counting the entire directory.
	replaced, err := root.Lstat(name)
	if err == nil {
		count--
		if replaced.Mode().IsRegular() {
			total -= replaced.Size()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		count++
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		if count > maxEntries || total > privateQuota {
			return errors.New("private storage quota exceeded")
		}
		return nil
	})
}

func replace(root *os.Root, name string, data []byte) error {
	tmp := path.Join(path.Dir(name), ".quailcrate-"+rand.Text())
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(tmp, name)
}
