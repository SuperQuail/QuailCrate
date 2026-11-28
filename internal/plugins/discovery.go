package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"quailcrate/internal/update"
	v1 "quailcrate/pluginapi/v1"
)

type Package struct {
	Manifest     v1.Manifest
	Dir          string
	Entry        string
	DataDir      string
	Bundled      bool
	EntrySHA256  string
	NativeDigest string
	Err          error

	dllHash          string
	sourceBuild      *SourceNative
	sourceDLL        string
	sourceBuiltEntry string
	runner           *nativeArtifacts
	localizedStrings map[string]map[string]string
}

type Catalog struct {
	TrustedDLLs  map[string]string
	SourceNative *SourceNative
}

var (
	pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)*$`)
	versionPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	localePattern   = regexp.MustCompile(`^[a-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)
)

// Discover reads each package directory. Invalid packages are retained for diagnostics.
func Discover(bundledRoot, userRoot string, trustedDLL map[string]string) []Package {
	return (Catalog{TrustedDLLs: trustedDLL}).Discover(bundledRoot, userRoot)
}

// InspectUserPackage applies discovery checks to a single third-party package.
// A failed inspection can still return parsed manifest fields for diagnostics.
func InspectUserPackage(dir string) (Package, error) {
	path, err := filepath.Abs(dir)
	if err != nil {
		return Package{Dir: dir, Err: err}, err
	}
	pkg := (Catalog{}).readPackage(filepath.Dir(path), filepath.Base(path), false)
	return pkg, pkg.Err
}

func (catalog Catalog) Discover(bundledRoot, userRoot string) []Package {
	var packages []Package
	ids := make(map[string]bool)
	for _, source := range []struct {
		root    string
		bundled bool
	}{{bundledRoot, true}, {userRoot, false}} {
		if source.root == "" {
			continue
		}
		entries, err := os.ReadDir(source.root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			packages = append(packages, Package{Dir: source.root, Bundled: source.bundled, Err: err})
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pkg := catalog.readPackage(source.root, entry.Name(), source.bundled)
			if pkg.Err == nil {
				if ids[pkg.Manifest.ID] {
					pkg.Err = fmt.Errorf("duplicate plugin id %q", pkg.Manifest.ID)
				} else {
					ids[pkg.Manifest.ID] = true
				}
			}
			packages = append(packages, pkg)
		}
	}
	return packages
}

func (catalog Catalog) readPackage(root, directory string, bundled bool) (pkg Package) {
	pkg = Package{Dir: filepath.Join(root, directory), Bundled: bundled}
	root, pkg.Err = filepath.EvalSymlinks(root)
	if pkg.Err != nil {
		return pkg
	}
	pkg.Dir, pkg.Err = resolveFile(root, directory)
	if pkg.Err != nil {
		return pkg
	}
	manifestPath, err := resolveFile(pkg.Dir, "manifest.json")
	if err != nil {
		pkg.Err = fmt.Errorf("manifest: %w", err)
		return pkg
	}
	data, err := readBounded(manifestPath, 64<<10)
	if err == nil {
		if utf8.Valid(data) {
			err = json.Unmarshal(data, &pkg.Manifest)
		} else {
			err = errors.New("expected a UTF-8 JSON manifest")
		}
	}
	if err == nil {
		err = ValidateManifest(pkg.Manifest)
	}
	if err != nil {
		pkg.Err = fmt.Errorf("manifest: %w", err)
		return pkg
	}
	if !bundled && (pkg.Manifest.ID == "quailcrate" || strings.HasPrefix(pkg.Manifest.ID, "quailcrate.")) {
		pkg.Err = errors.New("the quailcrate namespace is reserved for bundled plugins")
		return pkg
	}
	entry := pkg.Manifest.Entry
	if pkg.Manifest.Runtime == "dll" {
		pkg.sourceBuild = catalog.SourceNative
	}
	if pkg.sourceBuild != nil && bundled {
		if spec, ok := matchBundledSourceDLL(directory, pkg.Manifest.ID, entry); ok {
			// Discovery checks the source; enable uses a private copy of the compiled DLL.
			pkg.sourceDLL = spec.dll
			entry = spec.source
		}
	}
	pkg.Entry, pkg.Err = resolveFile(pkg.Dir, entry)
	if pkg.Err != nil {
		return pkg
	}
	info, err := os.Stat(pkg.Entry)
	if err != nil || !info.Mode().IsRegular() {
		pkg.Err = fmt.Errorf("entry must be a regular file: %s", pkg.Entry)
		return pkg
	}
	pkg.localizedStrings, pkg.Err = loadTranslations(pkg.Dir)
	if pkg.Err != nil {
		return pkg
	}
	if pkg.Manifest.Runtime == "dll" && pkg.sourceDLL == "" {
		pkg.EntrySHA256, pkg.NativeDigest, pkg.Err = ComputeNativeDigest(pkg.Dir, pkg.Entry)
		if pkg.Err != nil {
			return pkg
		}
		if bundled {
			pkg.dllHash = strings.ToLower(catalog.TrustedDLLs[pkg.Manifest.ID])
			digest, err := hex.DecodeString(pkg.dllHash)
			if err != nil || len(digest) != sha256.Size {
				pkg.Err = errors.New("DLL not in the trusted release manifest")
			}
		}
	}
	return pkg
}

// ComputeNativeDigest returns the SHA-256 of the entry DLL and a combined
// trust digest covering the entry DLL plus every companion .dll/.exe inside dir.
func ComputeNativeDigest(dir, entry string) (entrySHA256, nativeDigest string, err error) {
	entrySHA256, err = hashFileSHA256(entry)
	if err != nil {
		return "", "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	absEntry, err := filepath.Abs(entry)
	if err != nil {
		return "", "", err
	}
	type binaryItem struct {
		rel  string
		path string
	}
	var binaries []binaryItem
	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".dll" && ext != ".exe" && !strings.EqualFold(path, absEntry) {
			return nil
		}
		rel, relErr := filepath.Rel(absDir, path)
		if relErr != nil {
			return relErr
		}
		binaries = append(binaries, binaryItem{rel: filepath.ToSlash(strings.ToLower(rel)), path: path})
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if len(binaries) <= 1 {
		return entrySHA256, entrySHA256, nil
	}
	sort.Slice(binaries, func(i, j int) bool { return binaries[i].rel < binaries[j].rel })
	combined := sha256.New()
	for _, b := range binaries {
		var digest string
		if strings.EqualFold(b.path, absEntry) {
			digest = entrySHA256
		} else {
			digest, err = hashFileSHA256(b.path)
			if err != nil {
				return "", "", err
			}
		}
		_, _ = combined.Write([]byte(b.rel))
		_, _ = combined.Write([]byte{0})
		_, _ = combined.Write([]byte(digest))
		_, _ = combined.Write([]byte{'\n'})
	}
	return entrySHA256, hex.EncodeToString(combined.Sum(nil)), nil
}

func hashFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ValidateManifest(manifest v1.Manifest) error {
	if manifest.SchemaVersion != manifest.HostAPIVersion || (manifest.SchemaVersion != 1 && manifest.SchemaVersion != 2) {
		return fmt.Errorf("unsupported manifest or host API version (%d/%d)", manifest.SchemaVersion, manifest.HostAPIVersion)
	}
	if len(manifest.ID) > 128 || !pluginIDPattern.MatchString(manifest.ID) || strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 256 || !versionPattern.MatchString(manifest.Version) {
		return errors.New("invalid plugin id, name, or version")
	}
	if !validDescription(manifest.Description) {
		return errors.New("invalid plugin description")
	}
	if manifest.Homepage != "" {
		homepage, err := url.Parse(manifest.Homepage)
		if err != nil || len(manifest.Homepage) > 512 || homepage.Scheme != "https" || homepage.Hostname() == "" || homepage.User != nil {
			return errors.New("plugin homepage must be an HTTPS URL without credentials and at most 512 bytes")
		}
	}
	if manifest.MinHostVersion != "" && (!versionPattern.MatchString(manifest.MinHostVersion) || !update.ValidVersion(manifest.MinHostVersion)) {
		return errors.New("minHostVersion must be a semantic version")
	}
	for locale, localized := range manifest.Localizations {
		if !localePattern.MatchString(locale) ||
			(localized.Name == "" && localized.Description == "") ||
			(localized.Name != "" && strings.TrimSpace(localized.Name) == "") ||
			len(localized.Name) > 256 || !validDescription(localized.Description) {
			return fmt.Errorf("invalid localized plugin metadata for %q", locale)
		}
	}
	if !filepath.IsLocal(manifest.Entry) {
		return errors.New("entry must be relative to the package directory")
	}
	switch manifest.Runtime {
	case "lua":
		if !strings.EqualFold(filepath.Ext(manifest.Entry), ".lua") {
			return errors.New("Lua entry must end in .lua")
		}
	case "dll":
		if !strings.EqualFold(filepath.Ext(manifest.Entry), ".dll") || manifest.Platform != "windows/amd64" ||
			(manifest.ABIVersion != v1.ABIVersion && manifest.ABIVersion != v1.ABIVersionV2) {
			return errors.New("unsupported DLL entry, platform, or ABI version")
		}
	default:
		return fmt.Errorf("unsupported runtime %q", manifest.Runtime)
	}
	if err := validateContracts(manifest); err != nil {
		return err
	}
	contributions := []string{"search", "commands", "widgets", "appTabs"}
	if manifest.SchemaVersion == 2 {
		contributions = append(contributions, "quailcrate.search.provider", "quailcrate.result.actions")
		if manifest.UIVersion == 2 {
			contributions = append(contributions, "launcher", "settings", "tray")
		}
	}
	if err := validateList(manifest.Contributes, "contribution", contributions); err != nil {
		return err
	}
	permissions := []string{"apps.read", "apps.launch", "paths.open", "urls.open", "wallpaper.set"}
	if manifest.HostAPIVersion == 2 {
		permissions = append(permissions, "network.http", "network.local", "audio.play", "files.directory")
		if slices.Contains(manifest.Permissions, "network.local") && !slices.Contains(manifest.Permissions, "network.http") {
			return errors.New("network.local also requires network.http")
		}
	}
	return validateList(manifest.Permissions, "permission", permissions)
}

func validDescription(value string) bool {
	return value == "" || (len(value) <= 512 && strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n\x00"))
}

func loadTranslations(directory string) (map[string]map[string]string, error) {
	localized := make(map[string]map[string]string)
	for _, locale := range []string{"en-US", "zh-CN"} {
		name := filepath.Join("locales", locale+".json")
		if _, err := os.Lstat(filepath.Join(directory, name)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("translation %s: %w", locale, err)
		}
		path, err := resolveFile(directory, name)
		if err != nil {
			return nil, fmt.Errorf("translation %s: %w", locale, err)
		}
		data, err := readBounded(path, 64<<10)
		if err != nil {
			return nil, fmt.Errorf("translation %s: %w", locale, err)
		}
		var entries map[string]*string
		if !utf8.Valid(data) || json.Unmarshal(data, &entries) != nil || entries == nil {
			return nil, fmt.Errorf("translation %s must be a UTF-8 JSON object of strings", locale)
		}
		values := make(map[string]string, len(entries))
		for key, value := range entries {
			if value == nil {
				return nil, fmt.Errorf("translation %s must assign a string to %q", locale, key)
			}
			values[key] = *value
		}
		localized[locale] = values
	}
	if len(localized["en-US"]) != 0 {
		if localized["zh-CN"] == nil {
			localized["zh-CN"] = make(map[string]string)
		}
		for key, value := range localized["en-US"] {
			if _, present := localized["zh-CN"][key]; !present {
				localized["zh-CN"][key] = value
			}
		}
	}
	return localized, nil
}

func validateList(values []string, kind string, allowed []string) error {
	seen := make(map[string]bool)
	for _, value := range values {
		if !contains(allowed, value) || seen[value] {
			return fmt.Errorf("invalid or duplicate %s %q", kind, value)
		}
		seen[value] = true
	}
	return nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func resolveFile(root, name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("invalid package path %q", name)
	}
	path, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		return "", fmt.Errorf("path escapes package directory: %q", name)
	}
	return path, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), limit)
	}
	return data, nil
}
