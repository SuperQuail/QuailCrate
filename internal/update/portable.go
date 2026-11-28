package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	portableManifestName  = "portable-manifest.json"
	portableSignatureName = "portable-manifest.sig"
	portableSchema        = 1
	portableMetadataLimit = 8 << 20
	portableHost          = "QuailCrate.exe"
	portableWorker        = "QuailCrateNative.exe"
	portableHelper        = "QuailCrateUpdate.exe"
)

var (
	ErrPortableMigration = errors.New("portable installation has no ownership manifest")
	ErrPortableConflict  = errors.New("portable update conflicts with an existing path")
	errPortableDriveRoot = errors.New("portable installation cannot be updated at a drive root")
)

// PortableManifest identifies the files owned by a signed portable release.
type PortableManifest struct {
	SchemaVersion int            `json:"schema_version"`
	Version       string         `json:"version"`
	Files         []PortableFile `json:"files"`
}

type PortableFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// WritePortableManifest signs the files staged for a portable release.
func WritePortableManifest(root, version string, key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid portable manifest signing key")
	}
	root, err := portableRoot(root)
	if err != nil {
		return err
	}
	manifest := PortableManifest{SchemaVersion: portableSchema, Version: version}
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || name == root {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := validatePortablePath(rel); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if portableReparse(info) {
			return fmt.Errorf("invalid portable package path: %s", rel)
		}
		if info.IsDir() || portableMetadata(rel) {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid portable package file: %s", rel)
		}
		file, err := describePortableFile(name, rel)
		if err == nil {
			manifest.Files = append(manifest.Files, file)
		}
		return err
	})
	if err != nil {
		return err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if _, err := portableInventory(manifest); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if len(contents) > portableMetadataLimit {
		return errors.New("portable ownership manifest is too large")
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, contents)) + "\n"
	if err := os.WriteFile(filepath.Join(root, portableManifestName), contents, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, portableSignatureName), []byte(signature), 0600)
}

// ReadPortableManifest verifies the release signature and its ownership paths.
func ReadPortableManifest(root string, key ed25519.PublicKey) (PortableManifest, error) {
	root, err := portableRoot(root)
	if err != nil {
		return PortableManifest{}, err
	}
	manifest, _, err := loadPortableInventory(root, key)
	return manifest, err
}

func PortableVersion(root string, key ed25519.PublicKey) (string, error) {
	manifest, err := ReadPortableManifest(root, key)
	return manifest.Version, err
}

func loadPortableInventory(root string, key ed25519.PublicKey) (PortableManifest, map[string]PortableFile, error) {
	contents, err := readPortableMetadata(root, portableManifestName, portableMetadataLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return PortableManifest{}, nil, ErrPortableMigration
	}
	if err != nil {
		return PortableManifest{}, nil, err
	}
	signature, err := readPortableMetadata(root, portableSignatureName, 256)
	if err != nil {
		return PortableManifest{}, nil, err
	}
	return verifyPortableInventory(contents, signature, key)
}

func verifyPortableInventory(contents, signature []byte, key ed25519.PublicKey) (PortableManifest, map[string]PortableFile, error) {
	rawSignature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if len(key) != ed25519.PublicKeySize || err != nil || !ed25519.Verify(key, contents, rawSignature) {
		return PortableManifest{}, nil, errors.New("invalid portable ownership signature")
	}
	var manifest PortableManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return PortableManifest{}, nil, fmt.Errorf("decode portable ownership manifest: %w", err)
	}
	inventory, err := portableInventory(manifest)
	if err != nil {
		return PortableManifest{}, nil, err
	}
	for name, data := range map[string][]byte{portableManifestName: contents, portableSignatureName: signature} {
		digest := sha256.Sum256(data)
		inventory[strings.ToLower(name)] = PortableFile{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
	}
	return manifest, inventory, nil
}

func portableInventory(manifest PortableManifest) (map[string]PortableFile, error) {
	if _, valid := parseVersion(manifest.Version); manifest.SchemaVersion != portableSchema || !valid {
		return nil, errors.New("unsupported portable ownership manifest")
	}
	files := make(map[string]PortableFile, len(manifest.Files)+2)
	for _, file := range manifest.Files {
		if err := validatePortablePath(file.Path); err != nil {
			return nil, err
		}
		digest, err := hex.DecodeString(file.SHA256)
		if portableMetadata(file.Path) || file.Size < 0 || file.Size == math.MaxInt64 || err != nil || len(digest) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) {
			return nil, fmt.Errorf("invalid portable file metadata: %s", file.Path)
		}
		key := strings.ToLower(file.Path)
		if _, exists := files[key]; exists {
			return nil, fmt.Errorf("duplicate portable package path: %s", file.Path)
		}
		files[key] = file
	}
	for name := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, collision := files[parent]; collision || portableMetadata(parent) {
				return nil, fmt.Errorf("portable package path has a file as parent: %s", name)
			}
		}
	}
	for _, name := range []string{portableHost, portableWorker, portableHelper} {
		if file, ok := files[strings.ToLower(name)]; !ok || file.Path != name || file.Size == 0 {
			return nil, fmt.Errorf("portable package lacks %s", name)
		}
	}
	return files, nil
}

func portableMetadata(name string) bool {
	return strings.EqualFold(name, portableManifestName) || strings.EqualFold(name, portableSignatureName)
}

func validatePortablePath(name string) error {
	if name == "" || !fs.ValidPath(name) || name == "." || strings.EqualFold(strings.SplitN(name, "/", 2)[0], "data") {
		return fmt.Errorf("invalid portable package path: %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		invalidChar := strings.ContainsAny(part, `\:<>"|?*`) || strings.ContainsFunc(part, func(r rune) bool { return r < 32 })
		base := strings.ToUpper(strings.TrimRight(strings.SplitN(part, ".", 2)[0], " "))
		device := base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$"
		if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
			suffix := base[3:]
			device = utf8.RuneCountInString(suffix) == 1 && strings.Contains("123456789¹²³", suffix)
		}
		if invalidChar || device || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("invalid portable package path: %q", name)
		}
	}
	return nil
}

func portableRoot(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	root, err = resolvePortableRoot(root)
	if err != nil {
		return "", err
	}
	if filepath.Dir(root) == root {
		return "", errPortableDriveRoot
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || portableReparse(info) {
		return "", fmt.Errorf("invalid portable installation directory: %s", root)
	}
	return root, nil
}

// inspectPortablePath walks each component so a reparse point cannot direct a
// replacement outside its intended root. The relative path must be validated.
func inspectPortablePath(root, relative string) (string, fs.FileInfo, error) {
	name := filepath.Join(root, filepath.FromSlash(relative))
	current := root
	parts := strings.Split(relative, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if portableReparse(info) || (i < len(parts)-1 && !info.IsDir()) {
			return "", nil, fmt.Errorf("%w: %s", ErrPortableConflict, current)
		}
		if i == len(parts)-1 {
			return name, info, nil
		}
	}
	return "", nil, fs.ErrInvalid
}

func readPortableMetadata(root, relative string, limit int64) ([]byte, error) {
	name, info, err := inspectPortablePath(root, relative)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fs.ErrNotExist
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid portable metadata: %s", relative)
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readPortableLimit(file, limit)
}

func readPortableLimit(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if int64(len(contents)) > limit {
		return nil, errors.New("portable metadata is too large")
	}
	return contents, err
}

func describePortableFile(name, relative string) (PortableFile, error) {
	file, err := os.Open(name)
	if err != nil {
		return PortableFile{}, err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	return PortableFile{Path: relative, Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, err
}
