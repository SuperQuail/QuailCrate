package v2

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MaxPageStateBytes = 512 << 10
	MaxPageArgsBytes  = 64 << 10
)

// PageAssetPath accepts canonical, portable slash paths. URLs are decoded by
// the resource broker before reaching this boundary.
func PageAssetPath(name string) bool {
	if len(name) > 512 || !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:%?#") || !filepath.IsLocal(filepath.FromSlash(name)) {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") || !utf8.ValidString(segment) || strings.ContainsAny(segment, "<>\"|*") || strings.IndexFunc(segment, func(r rune) bool { return r < 32 || r == 127 }) != -1 {
			return false
		}
	}
	return true
}

func PageEntryPath(name string) bool {
	return PageAssetPath(name) && path.Ext(name) == ".html"
}

func PageObject(value json.RawMessage, maxBytes int) bool {
	if len(value) > maxBytes {
		return false
	}
	value = bytes.TrimSpace(value)
	return len(value) > 0 && value[0] == '{' && utf8.Valid(value) && json.Valid(value)
}

// InputValueType identifies a committed SDK value. Sliders use a fraction of
// their range; a UI maps that fraction to a duration or other domain value.
func InputValueType(value json.RawMessage) string {
	if len(value) == 0 || len(value) > 32<<10 || !utf8.Valid(value) {
		return ""
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return ""
	}
	switch v := decoded.(type) {
	case float64:
		if v >= 0 && v <= 1 {
			return "slider"
		}
	case string:
		return "input"
	}
	return ""
}
