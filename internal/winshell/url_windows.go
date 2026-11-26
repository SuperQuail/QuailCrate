package winshell

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	v1 "quailcrate/pluginapi/v1"
)

var allowedURLSchemes = map[string]bool{
	"http":                    true,
	"https":                   true,
	"steam":                   true,
	"com.epicgames.launcher":  true,
	"epicgames":               true,
	"uplay":                   true,
	"battlenet":               true,
	"ea":                      true,
	"origin":                  true,
	"gog":                     true,
	"xbox":                    true,
	"minecraft":               true,
	"roblox-player":           true,
	"riotclient":              true,
	"wegame":                  true,
}

// ValidateURL checks that raw uses an allowed web or game-launcher URI scheme.
func ValidateURL(raw string) (string, error) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return "", fmt.Errorf("A URL is required.")
	}
	if strings.ContainsAny(cleaned, "\r\n\x00") {
		return "", fmt.Errorf("The URL contains invalid characters.")
	}
	parsed, err := url.Parse(cleaned)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("A valid URL with scheme is required.")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if !allowedURLSchemes[scheme] {
		return "", fmt.Errorf("URL scheme %q is not allowed.", scheme)
	}
	if (scheme == "http" || scheme == "https") && strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("Web URLs must include a host name.")
	}
	return cleaned, nil
}

// OpenURL opens a validated http/https or allowlisted game-launcher URL via ShellExecuteW.
func OpenURL(raw string) error {
	target, err := ValidateURL(raw)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	result, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(u)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("Windows could not open the URL (error %d).", result)
	}
	return nil
}

// OpenPathEx opens a local file or folder, optionally requesting elevation (runas).
func OpenPathEx(path string, runAsAdmin bool) error {
	if !runAsAdmin {
		return OpenPath(path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("An absolute file path is required.")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("The file was not found or could not be accessed: %w", err)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	finish, err := sta()
	if err != nil {
		return err
	}
	defer finish()
	result, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(name)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("Windows could not launch the target as administrator (error %d).", result)
	}
	return nil
}

// ParseURLShortcutFile extracts the target URL, IconFile, and IconIndex from a Windows .url file.
func ParseURLShortcutFile(path string) (targetURL, iconFile string, iconIndex int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	inSection := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			inSection = strings.EqualFold(section, "InternetShortcut")
			continue
		}
		if !inSection {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		switch {
		case strings.EqualFold(key, "URL"):
			targetURL = val
		case strings.EqualFold(key, "IconFile"):
			iconFile = strings.Trim(val, `"`)
		case strings.EqualFold(key, "IconIndex"):
			if parsed, convErr := strconv.Atoi(val); convErr == nil {
				iconIndex = parsed
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", 0, err
	}
	validated, err := ValidateURL(targetURL)
	if err != nil {
		return "", "", 0, err
	}
	return validated, iconFile, iconIndex, nil
}

// ImportURLShortcut parses a Windows .url file (e.g., Steam/Epic/web shortcut),
// extracts and caches its icon as a local PNG so the entry remains independent
// even if the user later deletes the .url file from their Windows desktop.
func ImportURLShortcut(path, cacheDir string) (v1.ImportedShortcut, error) {
	targetURL, iconFile, iconIndex, err := ParseURLShortcutFile(path)
	if err != nil {
		return v1.ImportedShortcut{}, err
	}
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "" {
		name = base
	}
	out := v1.ImportedShortcut{
		Name:       name,
		URL:        targetURL,
		SourcePath: path,
	}
	if cacheDir != "" {
		var img *image.RGBA
		if iconFile != "" {
			expanded := expandWindowsEnv(iconFile)
			if _, statErr := os.Stat(expanded); statErr == nil {
				if iconIndex > 0 {
					img = ExtractIconAtIndex(expanded, iconIndex, 64)
				} else {
					img = ExtractIcon(expanded, 64)
				}
			}
		}
		if img == nil {
			img = ExtractIcon(path, 64)
		}
		if img != nil {
			if saved, saveErr := saveCachedIconPNG(filepath.Join(cacheDir, "icons"), targetURL+"|"+iconFile+"|"+strconv.Itoa(iconIndex), img); saveErr == nil {
				out.CachedIcon = "imgfile:" + saved
			}
		}
	}
	return out, nil
}

func expandWindowsEnv(value string) string {
	if !strings.Contains(value, "%") {
		return value
	}
	out := value
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		k, v := kv[:eq], kv[eq+1:]
		if k == "" {
			continue
		}
		needle := "%" + k + "%"
		if idx := strings.Index(strings.ToLower(out), strings.ToLower(needle)); idx >= 0 {
			out = out[:idx] + v + out[idx+len(needle):]
		}
	}
	return out
}

func saveCachedIconPNG(dir, key string, img image.Image) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(key)))
	dest := filepath.Join(dir, hex.EncodeToString(sum[:16])+".png")
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	encodeErr := png.Encode(f, img)
	closeErr := f.Close()
	if encodeErr != nil {
		_ = os.Remove(tmp)
		return "", encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dest, nil
}
