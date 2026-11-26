package winui

import (
	"image"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"quailcrate/internal/winshell"
	v1 "quailcrate/pluginapi/v1"
)

var (
	msimg32    = windows.NewLazySystemDLL("msimg32.dll")
	alphaBlend = msimg32.NewProc("AlphaBlend")
	gdiFlush   = gdi32.NewProc("GdiFlush")
)

type iconCache struct {
	mu       sync.Mutex
	cacheDir string
	images   map[string]*image.RGBA
	missing  map[string]bool
	pending  map[string]bool
	version  uint64
}

func (u *UI) SetIconCacheDir(dir string) {
	u.icons.mu.Lock()
	u.icons.cacheDir = dir
	u.icons.mu.Unlock()
}

func (u *UI) cacheDirectory() string {
	u.icons.mu.Lock()
	dir := u.icons.cacheDir
	u.icons.mu.Unlock()
	return dir
}

func (u *UI) RefreshIcon(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	u.ensureIconCache()
	u.icons.mu.Lock()
	cacheDir := u.icons.cacheDir
	delete(u.icons.images, key)
	delete(u.icons.missing, key)
	delete(u.icons.pending, key)
	u.icons.version++
	u.icons.mu.Unlock()
	if strings.HasPrefix(key, "favicon:") {
		rawURL := strings.TrimPrefix(key, "favicon:")
		go func() {
			winshell.ClearFaviconCache(rawURL, cacheDir)
			img := winshell.FetchFavicon(rawURL, cacheDir, true)
			u.icons.mu.Lock()
			if img != nil {
				u.icons.images[key] = img
				delete(u.icons.missing, key)
			} else {
				u.icons.missing[key] = true
			}
			u.icons.version++
			u.icons.mu.Unlock()
			u.post(func() {
				for _, w := range u.widgets {
					u.presentWidget(w)
				}
				u.repaintLauncherIcons([]string{key})
			})
		}()
	} else {
		u.post(func() {
			for _, w := range u.widgets {
				u.presentWidget(w)
			}
		})
	}
}

func (u *UI) ensureIconCache() {
	if u.icons.images == nil {
		u.icons.images = make(map[string]*image.RGBA)
		u.icons.missing = make(map[string]bool)
		u.icons.pending = make(map[string]bool)
	}
}

func (u *UI) widgetIcons(view v1.View) (map[string]image.Image, uint64) {
	u.ensureIconCache()
	var keys []string
	for _, c := range view.DisplayControls() {
		for _, item := range c.Items {
			if item.Icon != "" && !v1.IsIconName(item.Icon) {
				keys = append(keys, item.Icon)
			}
		}
	}
	if len(keys) == 0 {
		return nil, 0
	}
	u.icons.mu.Lock()
	out := make(map[string]image.Image, len(keys))
	var toLoad []string
	for _, k := range keys {
		if img := u.icons.images[k]; img != nil {
			out[k] = img
		} else if !u.icons.missing[k] && !u.icons.pending[k] {
			u.icons.pending[k] = true
			toLoad = append(toLoad, k)
		}
	}
	ver := u.icons.version
	u.icons.mu.Unlock()
	if len(toLoad) > 0 {
		go u.loadIconsAsync(toLoad)
	}
	return out, ver
}

func (u *UI) launcherIcon(key string) *image.RGBA {
	if key == "" || v1.IsIconName(key) {
		return nil
	}
	u.ensureIconCache()
	u.icons.mu.Lock()
	img := u.icons.images[key]
	needLoad := img == nil && !u.icons.missing[key] && !u.icons.pending[key]
	if needLoad {
		u.icons.pending[key] = true
	}
	u.icons.mu.Unlock()
	if needLoad {
		go u.loadIconsAsync([]string{key})
	}
	return img
}

func (u *UI) loadIconsAsync(keys []string) {
	u.icons.mu.Lock()
	cacheDir := u.icons.cacheDir
	u.icons.mu.Unlock()
	var loaded []string
	for _, key := range keys {
		var img *image.RGBA
		switch {
		case strings.HasPrefix(key, "favicon:"):
			img = winshell.FetchFavicon(strings.TrimPrefix(key, "favicon:"), cacheDir, false)
		case strings.HasPrefix(key, "imgfile:"):
			img = winshell.LoadCachedIcon(strings.TrimPrefix(key, "imgfile:"))
		default:
			img = winshell.ExtractIcon(key, 96)
		}
		u.icons.mu.Lock()
		delete(u.icons.pending, key)
		if img != nil {
			u.icons.images[key] = img
			u.icons.version++
			loaded = append(loaded, key)
		} else {
			u.icons.missing[key] = true
		}
		u.icons.mu.Unlock()
	}
	if len(loaded) == 0 {
		return
	}
	u.post(func() {
		u.repaintLauncherIcons(loaded)
		if u.picker != nil {
			u.picker.repaintIcons(loaded)
		}
		changed := make(map[string]bool, len(loaded))
		for _, key := range loaded {
			changed[key] = true
		}
		for _, w := range u.widgets {
			if widgetUsesChangedIcon(w.model.View, changed) {
				u.presentWidget(w)
			}
		}
	})
}

func widgetUsesChangedIcon(view v1.View, changed map[string]bool) bool {
	for _, control := range view.DisplayControls() {
		for _, item := range control.Items {
			if changed[item.Icon] {
				return true
			}
		}
	}
	return false
}

func (p *optionPicker) repaintIcons(loaded []string) {
	end := min(len(p.filtered), p.scroll+p.visibleRows())
	for i := p.scroll; i < end; i++ {
		if slices.Contains(loaded, p.options[p.filtered[i]].Icon) {
			_, icon := p.itemRects(i)
			invalidateRect.Call(p.hwnd, uintptr(unsafe.Pointer(&icon)), 0)
		}
	}
}

type cachedIconBitmap struct {
	dibSurface
	source *image.RGBA
}

func (b *cachedIconBitmap) draw(hdc uintptr, dst rect, img *image.RGBA) {
	if hdc == 0 || img == nil || dst.width() <= 0 || dst.height() <= 0 {
		return
	}
	size := img.Rect.Size()
	if b.source != img {
		reused := b.source != nil && b.dibSize == size
		b.source = nil
		if !b.ensureDIB(size) {
			return
		}
		// A reused bitmap may still be referenced by the calling thread's GDI batch.
		if reused {
			gdiFlush.Call()
		}
		raw := unsafe.Slice(nativePointer[byte](b.dibBits), size.X*size.Y*4)
		rgbaToPremultipliedBGRA(raw, img)
		b.source = img
	}

	// Pack BLENDFUNCTION {AC_SRC_OVER, 0, 255, AC_SRC_ALPHA} as a 32-bit value.
	blend := uintptr(acSrcOver) | (uintptr(255) << 16) | (uintptr(acSrcAlpha) << 24)
	alphaBlend.Call(
		hdc,
		coord(dst.Left), coord(dst.Top), coord(dst.width()), coord(dst.height()),
		b.dibDC,
		0, 0, coord(int32(size.X)), coord(int32(size.Y)),
		blend,
	)
}

func drawRGBAIcon(hdc uintptr, dst rect, img *image.RGBA) {
	var b cachedIconBitmap
	defer b.release()
	b.draw(hdc, dst, img)
}
