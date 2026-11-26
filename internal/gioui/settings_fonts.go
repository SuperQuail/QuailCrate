package gioui

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"gioui.org/font/gofont"
	"gioui.org/text"
	"github.com/go-text/typesetting/fontscan"
)

var (
	fontCacheDirMu         sync.Mutex
	configuredFontCacheDir string
)

// SetFontCacheDir directs Gio's system font index into the portable data cache
// directory instead of %LOCALAPPDATA%. Must be called before NewTextShaper.
func SetFontCacheDir(dir string) {
	fontCacheDirMu.Lock()
	configuredFontCacheDir = dir
	fontCacheDirMu.Unlock()
}

// go-text initializes its process-wide font index only once and discards a scan
// if the index cannot be written. Choose a writable directory before that scan.
var systemFontsAvailable = sync.OnceValue(func() bool {
	fontCacheDirMu.Lock()
	dir := configuredFontCacheDir
	fontCacheDirMu.Unlock()
	var err error
	if dir == "" {
		dir, err = os.UserCacheDir()
		if err == nil {
			dir = filepath.Join(dir, "QuailCrate", "font-cache")
		}
	}
	if err == nil && dir != "" {
		err = os.MkdirAll(dir, 0700)
		if err == nil {
			// A pre-existing directory can still be read-only.
			var probe *os.File
			probe, err = os.CreateTemp(dir, ".write-test-*")
			if err == nil {
				probe.Close()
				os.Remove(probe.Name())
			}
		}
	}
	if err != nil {
		dir, err = os.MkdirTemp("", "quailcrate-font-cache-*")
		if err != nil {
			return false
		}
		defer os.RemoveAll(dir)
	}
	fonts, err := fontscan.SystemFonts(log.New(io.Discard, "", 0), dir)
	return err == nil && len(fonts) != 0
})

// NewTextShaper returns an independent glyph cache for a window. System font
// discovery is shared by settings and widgets, including in portable profiles.
func NewTextShaper() *text.Shaper {
	if systemFontsAvailable() {
		return text.NewShaper()
	}
	return text.NewShaper(text.NoSystemFonts(), text.WithCollection(gofont.Regular()))
}
