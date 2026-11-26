package winui

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gioui.org/f32"
	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
	"quailcrate/internal/winshell"
)

const backdropScaleDiv = 4

type monitorBackdrop struct {
	bounds  image.Rectangle
	blurred *image.RGBA
}

type wallpaperBackdrop struct {
	monitors    []monitorBackdrop
	version     uint64
	fingerprint string

	mu         sync.Mutex
	refreshing bool
	pending    bool
}

type monitorDPIEntry struct {
	bounds image.Rectangle
	dpi    int32
}

func (u *UI) needsSpatialBackdrop() bool {
	return len(u.widgets) > 0 && apptheme.NormalizeGlassStyle(u.appearance.GlassStyle) == apptheme.GlassSmoked && !isSystemHighContrast()
}

// refreshWallpaperBackdrop asynchronously captures, downsamples, blurs (36dp),
// and vibrancy-saturates (185%) the active Windows desktop wallpaper per monitor
// without blocking the Win32 message loop.
func (u *UI) refreshWallpaperBackdrop(force bool) {
	u.backdrop.mu.Lock()
	if u.backdrop.refreshing {
		u.backdrop.pending = true
		u.backdrop.mu.Unlock()
		return
	}
	u.backdrop.refreshing = true
	u.backdrop.pending = false
	prevFingerprint := u.backdrop.fingerprint
	u.backdrop.mu.Unlock()

	if len(u.monitors) == 0 {
		u.refreshMonitors()
	}
	dpiList := make([]monitorDPIEntry, 0, len(u.monitors))
	for _, m := range u.monitors {
		r := m.info.Monitor
		if r.width() > 0 && r.height() > 0 {
			dpiList = append(dpiList, monitorDPIEntry{
				bounds: image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)),
				dpi:    max(int32(96), m.dpi),
			})
		}
	}

	go func() {
		state, _ := winshell.CurrentWallpaper()
		monitors, fp, changed := buildWallpaperBackdrops(state, dpiList, prevFingerprint, force)

		u.backdrop.mu.Lock()
		u.backdrop.refreshing = false
		again := u.backdrop.pending
		u.backdrop.pending = false
		u.backdrop.mu.Unlock()

		u.post(func() {
			if changed {
				u.backdrop.monitors = monitors
				u.backdrop.fingerprint = fp
				u.backdrop.version++
				if u.needsSpatialBackdrop() {
					for _, w := range u.widgets {
						u.presentWidget(w)
					}
				}
			}
			if again && u.needsSpatialBackdrop() {
				u.refreshWallpaperBackdrop(false)
			}
		})
	}()
}

func buildWallpaperBackdrops(state winshell.WallpaperState, dpiList []monitorDPIEntry, prevFingerprint string, force bool) ([]monitorBackdrop, string, bool) {
	// If fallback returned a single entry with empty bounds, fill bounds from active monitors.
	if len(state.Monitors) == 1 && state.Monitors[0].Bounds.Empty() && len(dpiList) > 0 {
		path := state.Monitors[0].Path
		expanded := make([]winshell.MonitorWallpaper, 0, len(dpiList))
		for _, m := range dpiList {
			expanded = append(expanded, winshell.MonitorWallpaper{Bounds: m.bounds, Path: path})
		}
		state.Monitors = expanded
	}
	if len(state.Monitors) == 0 {
		if prevFingerprint == "" && !force {
			return nil, "", false
		}
		return nil, "", prevFingerprint != ""
	}

	// Build fingerprint from wallpaper metadata so redundant WM_SETTINGCHANGE events skip decoding.
	var fp strings.Builder
	fmt.Fprintf(&fp, "pos=%d;bg=%02x%02x%02x;", state.Position, state.Background.R, state.Background.G, state.Background.B)
	hasAnyPath := false
	for _, mon := range state.Monitors {
		dpi := monitorDPIForBounds(mon.Bounds, dpiList)
		resolvedPath := resolveWallpaperPath(mon.Path)
		if resolvedPath != "" {
			hasAnyPath = true
		}
		var mtime int64
		var fsize int64
		if resolvedPath != "" {
			if st, err := os.Stat(resolvedPath); err == nil {
				mtime = st.ModTime().UnixNano()
				fsize = st.Size()
			}
		}
		fmt.Fprintf(&fp, "%v@%d=%s#%d:%d|", mon.Bounds, dpi, resolvedPath, mtime, fsize)
	}
	fingerprint := fp.String()
	if !force && fingerprint == prevFingerprint {
		return nil, fingerprint, false
	}
	if !hasAnyPath {
		return nil, fingerprint, true
	}

	var virtualBounds image.Rectangle
	for i, mon := range state.Monitors {
		if i == 0 {
			virtualBounds = mon.Bounds
		} else {
			virtualBounds = virtualBounds.Union(mon.Bounds)
		}
	}

	decoded := make(map[string]image.Image, len(state.Monitors))
	loadImage := func(path string) image.Image {
		if path == "" {
			return nil
		}
		if img, ok := decoded[path]; ok {
			return img
		}
		img := decodeWallpaperFile(path)
		if img == nil {
			if fallback := transcodedWallpaperPath(); fallback != "" && !strings.EqualFold(fallback, path) {
				if cached, ok := decoded[fallback]; ok {
					img = cached
				} else {
					img = decodeWallpaperFile(fallback)
					decoded[fallback] = img
				}
			}
		}
		decoded[path] = img
		return img
	}

	out := make([]monitorBackdrop, 0, len(state.Monitors))
	for _, mon := range state.Monitors {
		if mon.Bounds.Empty() {
			continue
		}
		img := loadImage(resolveWallpaperPath(mon.Path))
		if img == nil {
			continue
		}
		dpi := monitorDPIForBounds(mon.Bounds, dpiList)
		composed := composeMonitorWallpaper(img, mon.Bounds, virtualBounds, state.Position, state.Background, backdropScaleDiv)
		blurRadius := max(4, int(math.Round(36.0*float64(dpi)/96.0/float64(backdropScaleDiv))))
		blurred := gioui.BuildSpatialBackdrop(composed, blurRadius)
		if blurred != nil {
			out = append(out, monitorBackdrop{
				bounds:  mon.Bounds,
				blurred: blurred,
			})
		}
	}
	return out, fingerprint, true
}

func monitorDPIForBounds(b image.Rectangle, dpiList []monitorDPIEntry) int32 {
	for _, m := range dpiList {
		if m.bounds == b {
			return m.dpi
		}
	}
	for _, m := range dpiList {
		if !m.bounds.Intersect(b).Empty() {
			return m.dpi
		}
	}
	return 96
}

func resolveWallpaperPath(path string) string {
	path = strings.TrimSpace(path)
	if path != "" {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	// Windows sometimes clears/moves the original source file while keeping TranscodedWallpaper active.
	if path != "" {
		return transcodedWallpaperPath()
	}
	return ""
}

func transcodedWallpaperPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	p := filepath.Join(appData, "Microsoft", "Windows", "Themes", "TranscodedWallpaper")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func decodeWallpaperFile(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// composeMonitorWallpaper renders src into a 1/scaleDiv RGBA buffer representing
// monBounds using Windows' exact wallpaper positioning mode and background color.
func composeMonitorWallpaper(src image.Image, monBounds, virtualBounds image.Rectangle, pos winshell.WallpaperPosition, bg color.NRGBA, scaleDiv int) *image.RGBA {
	if scaleDiv < 1 {
		scaleDiv = 1
	}
	dw := max(1, (monBounds.Dx()+scaleDiv-1)/scaleDiv)
	dh := max(1, (monBounds.Dy()+scaleDiv-1)/scaleDiv)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	fillSolidRGBA(dst, bg)

	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	mw, mh := monBounds.Dx(), monBounds.Dy()
	if sw <= 0 || sh <= 0 || mw <= 0 || mh <= 0 {
		return dst
	}

	switch pos {
	case winshell.WallpaperStretch:
		xdraw.BiLinear.Scale(dst, dst.Bounds(), src, sb, xdraw.Src, nil)

	case winshell.WallpaperFit:
		scale := math.Min(float64(dw)/float64(sw), float64(dh)/float64(sh))
		tw := max(1, int(math.Round(float64(sw)*scale)))
		th := max(1, int(math.Round(float64(sh)*scale)))
		ox := (dw - tw) / 2
		oy := (dh - th) / 2
		xdraw.BiLinear.Scale(dst, image.Rect(ox, oy, ox+tw, oy+th), src, sb, xdraw.Src, nil)

	case winshell.WallpaperCenter:
		tw := max(1, int(math.Round(float64(sw)/float64(scaleDiv))))
		th := max(1, int(math.Round(float64(sh)/float64(scaleDiv))))
		ox := (dw - tw) / 2
		oy := (dh - th) / 2
		xdraw.BiLinear.Scale(dst, image.Rect(ox, oy, ox+tw, oy+th), src, sb, xdraw.Src, nil)

	case winshell.WallpaperTile:
		tw := max(1, int(math.Round(float64(sw)/float64(scaleDiv))))
		th := max(1, int(math.Round(float64(sh)/float64(scaleDiv))))
		tile := image.NewRGBA(image.Rect(0, 0, tw, th))
		xdraw.BiLinear.Scale(tile, tile.Bounds(), src, sb, xdraw.Src, nil)
		for y := 0; y < dh; y += th {
			for x := 0; x < dw; x += tw {
				r := image.Rect(x, y, min(dw, x+tw), min(dh, y+th))
				xdraw.Copy(dst, r.Min, tile, image.Rect(0, 0, r.Dx(), r.Dy()), xdraw.Src, nil)
			}
		}

	case winshell.WallpaperSpan:
		if virtualBounds.Empty() {
			virtualBounds = monBounds
		}
		vw, vh := float64(virtualBounds.Dx()), float64(virtualBounds.Dy())
		scale := math.Max(vw/float64(sw), vh/float64(sh))
		visW := vw / scale
		visH := vh / scale
		cropX0 := (float64(sw) - visW) / 2.0
		cropY0 := (float64(sh) - visH) / 2.0
		relX0 := float64(monBounds.Min.X-virtualBounds.Min.X) / scale
		relY0 := float64(monBounds.Min.Y-virtualBounds.Min.Y) / scale
		relX1 := float64(monBounds.Max.X-virtualBounds.Min.X) / scale
		relY1 := float64(monBounds.Max.Y-virtualBounds.Min.Y) / scale
		sr := image.Rect(
			sb.Min.X+int(math.Floor(cropX0+relX0)),
			sb.Min.Y+int(math.Floor(cropY0+relY0)),
			sb.Min.X+int(math.Ceil(cropX0+relX1)),
			sb.Min.Y+int(math.Ceil(cropY0+relY1)),
		).Intersect(sb)
		if !sr.Empty() {
			xdraw.BiLinear.Scale(dst, dst.Bounds(), src, sr, xdraw.Src, nil)
		}

	default: // WallpaperFill
		scale := math.Max(float64(mw)/float64(sw), float64(mh)/float64(sh))
		visW := float64(mw) / scale
		visH := float64(mh) / scale
		x0 := int(math.Floor((float64(sw) - visW) / 2.0))
		y0 := int(math.Floor((float64(sh) - visH) / 2.0))
		x1 := int(math.Ceil(float64(x0) + visW))
		y1 := int(math.Ceil(float64(y0) + visH))
		sr := image.Rect(sb.Min.X+x0, sb.Min.Y+y0, sb.Min.X+x1, sb.Min.Y+y1).Intersect(sb)
		if sr.Empty() {
			sr = sb
		}
		xdraw.BiLinear.Scale(dst, dst.Bounds(), src, sr, xdraw.Src, nil)
	}
	return dst
}

func fillSolidRGBA(dst *image.RGBA, col color.NRGBA) {
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	for y := 0; y < h; y++ {
		row := dst.Pix[y*dst.Stride : y*dst.Stride+w*4]
		for x := 0; x < len(row); x += 4 {
			row[x] = col.R
			row[x+1] = col.G
			row[x+2] = col.B
			row[x+3] = 255
		}
	}
}

// sliceForRect returns a zero-allocation sub-pixel SpatialBackdrop referencing
// the cached blurred monitor image when screenRect falls inside one monitor, or
// seamlessly stitches a tiny composite buffer when straddling multiple monitors.
func (b *wallpaperBackdrop) sliceForRect(screenRect image.Rectangle) *gioui.SpatialBackdrop {
	if b == nil || len(b.monitors) == 0 || screenRect.Empty() {
		return nil
	}
	// Fast path: widget frame lies completely inside one monitor -> zero allocations.
	for i := range b.monitors {
		m := &b.monitors[i]
		if m.blurred == nil || m.bounds.Empty() {
			continue
		}
		if screenRect.In(m.bounds) {
			bw := float32(m.blurred.Rect.Dx())
			bh := float32(m.blurred.Rect.Dy())
			sx := bw / float32(m.bounds.Dx())
			sy := bh / float32(m.bounds.Dy())
			src := gioui.SpatialRect{
				Min: f32.Pt(
					clampF32(float32(screenRect.Min.X-m.bounds.Min.X)*sx, 0, bw),
					clampF32(float32(screenRect.Min.Y-m.bounds.Min.Y)*sy, 0, bh),
				),
				Max: f32.Pt(
					clampF32(float32(screenRect.Max.X-m.bounds.Min.X)*sx, 0, bw),
					clampF32(float32(screenRect.Max.Y-m.bounds.Min.Y)*sy, 0, bh),
				),
			}
			if src.Dx() > 0 && src.Dy() > 0 {
				return &gioui.SpatialBackdrop{Image: m.blurred, Source: src}
			}
		}
	}

	// Fallback path: widget straddles multiple monitors or screen edge.
	cw := max(2, (screenRect.Dx()+backdropScaleDiv-1)/backdropScaleDiv)
	ch := max(2, (screenRect.Dy()+backdropScaleDiv-1)/backdropScaleDiv)
	comp := image.NewRGBA(image.Rect(0, 0, cw, ch))
	matched := false
	for y := 0; y < ch; y++ {
		sy := screenRect.Min.Y + (y*screenRect.Dy()+ch/2)/ch
		for x := 0; x < cw; x++ {
			sx := screenRect.Min.X + (x*screenRect.Dx()+cw/2)/cw
			col, ok := b.sampleClamped(sx, sy)
			if !ok {
				continue
			}
			matched = true
			off := y*comp.Stride + x*4
			comp.Pix[off] = col.R
			comp.Pix[off+1] = col.G
			comp.Pix[off+2] = col.B
			comp.Pix[off+3] = 255
		}
	}
	if !matched {
		return nil
	}
	return &gioui.SpatialBackdrop{
		Image:  comp,
		Source: gioui.SpatialRect{Max: f32.Pt(float32(cw), float32(ch))},
	}
}

func (b *wallpaperBackdrop) sampleClamped(screenX, screenY int) (color.RGBA, bool) {
	var best *monitorBackdrop
	bestDist := math.MaxInt
	for i := range b.monitors {
		m := &b.monitors[i]
		if m.blurred == nil || m.bounds.Empty() {
			continue
		}
		if screenX >= m.bounds.Min.X && screenX < m.bounds.Max.X && screenY >= m.bounds.Min.Y && screenY < m.bounds.Max.Y {
			best = m
			break
		}
		cx := max(m.bounds.Min.X, min(m.bounds.Max.X-1, screenX))
		cy := max(m.bounds.Min.Y, min(m.bounds.Max.Y-1, screenY))
		dx, dy := screenX-cx, screenY-cy
		if d := dx*dx + dy*dy; d < bestDist {
			bestDist = d
			best = m
		}
	}
	if best == nil {
		return color.RGBA{}, false
	}
	bw, bh := best.blurred.Rect.Dx(), best.blurred.Rect.Dy()
	ux := (screenX - best.bounds.Min.X) * bw / best.bounds.Dx()
	uy := (screenY - best.bounds.Min.Y) * bh / best.bounds.Dy()
	ux = max(0, min(bw-1, ux))
	uy = max(0, min(bh-1, uy))
	return best.blurred.RGBAAt(ux, uy), true
}

func clampF32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
