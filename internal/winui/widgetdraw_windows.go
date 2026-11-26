package winui

import (
	"image"
	"image/color"
	"log"
	"strings"
	"time"
	"unsafe"

	giofont "gioui.org/font"
	"gioui.org/gpu/headless"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	giotext "gioui.org/text"
	"gioui.org/unit"
	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

type widgetRenderer struct {
	window     *headless.Window
	shaper     *giotext.Shaper
	ops        op.Ops
	retryAfter time.Time
}

type widgetFrame struct {
	dibSurface
	key     widgetVisualKey
	current bool
	image   *image.RGBA
}

func (f *widgetFrame) release() {
	f.dibSurface.release()
	f.current = false
}

type dibSurface struct {
	dibDC     uintptr
	dibBitmap uintptr
	dibPrev   uintptr
	dibBits   uintptr
	dibSize   image.Point
}

func (f *dibSurface) release() {
	if f.dibDC != 0 {
		if f.dibPrev != 0 {
			selectObject.Call(f.dibDC, f.dibPrev)
		}
		if f.dibBitmap != 0 {
			deleteObject.Call(f.dibBitmap)
		}
		deleteDC.Call(f.dibDC)
	}
	f.dibDC, f.dibBitmap, f.dibPrev, f.dibBits = 0, 0, 0, 0
	f.dibSize = image.Point{}
}

func (f *dibSurface) ensureDIB(size image.Point) bool {
	if size.X < 1 || size.Y < 1 {
		return false
	}
	if f.dibDC != 0 && f.dibBitmap != 0 && f.dibSize == size {
		return true
	}
	f.release()
	dc, _, _ := createCompatibleDC.Call(0)
	if dc == 0 {
		return false
	}
	info := widgetBitmapInfo{
		Size:   40,
		Width:  int32(size.X),
		Height: -int32(size.Y),
		Planes: 1,
		Bits:   32,
	}
	var bits uintptr
	bmp, _, _ := createDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		if bmp != 0 {
			deleteObject.Call(bmp)
		}
		deleteDC.Call(dc)
		return false
	}
	prev, _, _ := selectObject.Call(dc, bmp)
	if prev == 0 || prev == ^uintptr(0) {
		deleteObject.Call(bmp)
		deleteDC.Call(dc)
		return false
	}
	f.dibDC, f.dibBitmap, f.dibPrev, f.dibBits, f.dibSize = dc, bmp, prev, bits, size
	return true
}

// Draw the invalid region offscreen and submit it in one copy.
func (f *dibSurface) paint(hwnd uintptr, draw func(dc uintptr, client, dirty rect)) {
	var ps paintStruct
	dc, _, _ := beginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer endPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	dirty := ps.Rect
	if dc == 0 || dirty.width() <= 0 || dirty.height() <= 0 {
		return
	}
	client := clientRect(hwnd)
	if f.ensureDIB(image.Pt(int(client.width()), int(client.height()))) {
		draw(f.dibDC, client, dirty)
		const srcCopy = 0x00CC0020
		if ok, _, _ := bitBlt.Call(dc, coord(dirty.Left), coord(dirty.Top), coord(dirty.width()), coord(dirty.height()),
			f.dibDC, coord(dirty.Left), coord(dirty.Top), srcCopy); ok != 0 {
			return
		}
	}
	draw(dc, client, dirty)
}

type widgetVisualKey struct {
	size                          image.Point
	screenPos                     point
	dpi                           int
	locale, moveHint, dropControl string
	draggingItemID                string
	sliderControl                 string
	sliderValueMilli              int
	focused, hovered, dropIndex   int
	hasFocus, keyboardFocus       bool
	hasHover, mouseInside         bool
	collapsed, locked             bool
	elevated                      bool
	iconVersion                   uint64
	backdropVersion               uint64
	colors                        gioui.WidgetColors
}

func gioColor(ref uintptr) color.NRGBA {
	return color.NRGBA{R: byte(ref), G: byte(ref >> 8), B: byte(ref >> 16), A: 255}
}

func widgetColors() gioui.WidgetColors {
	if activeUI != nil {
		return activeUI.widgetColorsFor(apptheme.ComponentGlobal)
	}
	return widgetColorsFromResolved(apptheme.DefaultConfig().Resolve(apptheme.ComponentGlobal), isSystemHighContrast())
}

func (u *UI) widgetColorsFor(pluginID string) gioui.WidgetColors {
	res := u.appearance.Resolve(pluginID)
	if isSystemHighContrast() {
		return widgetColorsFromResolved(res, true)
	}
	return widgetColorsFromResolved(res, false)
}

func (u *UI) widgetColorsForWidget(w *widgetWindow) gioui.WidgetColors {
	colors := u.widgetColorsFor(w.model.PluginID)
	if w != nil && w.iconSize > 0 {
		colors.IconSize = max(apptheme.MinIconSize, min(apptheme.MaxIconSize, w.iconSize))
	}
	return colors
}

func widgetColorsFromResolved(res apptheme.Resolved, highContrast bool) gioui.WidgetColors {
	if highContrast {
		c := highContrastPalette()
		return gioui.WidgetColors{
			Background: gioColor(c.background), Surface: gioColor(c.surface), Ink: gioColor(c.text),
			Secondary: gioColor(c.secondary), Border: gioColor(c.border), Accent: gioColor(c.accent),
			Selection: gioColor(c.selected), Hover: gioColor(c.selected), Weekend: gioColor(c.accent),
			Warning: gioColor(c.warning), OnAccent: gioColor(c.white), OnSelection: gioColor(c.white),
			Opacity: 100, Translucent: true, CornerRadius: res.CornerRadius, HasCornerRadius: true,
			TitleAccentBar: res.TitleAccentBar, IconSize: res.IconSize,
		}
	}
	p := res.Palette
	return gioui.WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: res.Opacity, Translucent: true, GlassStyle: res.GlassStyle,
		CornerRadius: res.CornerRadius, HasCornerRadius: true, TitleAccentBar: res.TitleAccentBar,
		IconSize: res.IconSize,
	}
}

func widgetSize(hwnd uintptr) image.Point {
	r := clientRect(hwnd)
	return image.Pt(int(r.width()), int(r.height()))
}

func nativeWidgetHits(dst []widgetHit, hits []gioui.WidgetHit) []widgetHit {
	dst = dst[:0]
	for _, h := range hits {
		dst = append(dst, widgetHit{
			area: widgetRect(h.Area), controlID: h.ControlID, menu: h.Menu, itemID: h.ItemID, index: h.Index, total: h.Total, reorder: h.Reorder, drop: h.Drop, slider: h.Slider, value: h.Value,
		})
	}
	return dst
}

func widgetRect(r image.Rectangle) rect {
	return rect{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
}

func (u *UI) buildWidgetHits(w *widgetWindow) {
	if w == nil || w.hwnd == 0 {
		return
	}
	opt := gioui.WidgetOptions{
		DPI: int(dpiForWindow(w.hwnd)), Size: widgetSize(w.hwnd),
		Colors: u.widgetColorsForWidget(w), Scroll: w.listScroll,
		MouseInside: w.mouseInside, Collapsed: w.collapsed, Locked: w.locked,
	}
	g := gioui.LayoutWidget(w.model.View, opt, nil)
	var previous widgetHit
	if w.keyboardFocus && w.focused >= 0 && w.focused < len(w.hits) {
		previous = w.hits[w.focused]
	}
	w.scrollAreas = g.ScrollAreas
	w.hits = nativeWidgetHits(w.hits, g.Hits)
	if previous.controlID != "" {
		for i, h := range w.hits {
			if h.controlID == previous.controlID && h.itemID == previous.itemID {
				w.focused = i
				break
			}
		}
	}
	w.dropZones = nativeWidgetHits(w.dropZones, g.DropZones)
	w.focused = max(0, min(w.focused, len(w.hits)-1))
	if w.hovered >= len(w.hits) {
		w.hasHover, w.hovered = false, -1
	}
	w.frame.current = false
}

func (u *UI) widgetKeyAndOptions(w *widgetWindow, size image.Point) (widgetVisualKey, gioui.WidgetOptions) {
	focus, _, _ := getFocus.Call()
	icons, iconVersion := u.widgetIcons(w.model.View)
	key := widgetVisualKey{
		size: size, dpi: int(dpiForWindow(w.hwnd)), locale: u.locale,
		colors:  u.widgetColorsForWidget(w),
		focused: w.focused, hovered: w.hovered,
		hasFocus: focus == w.hwnd, keyboardFocus: w.keyboardFocus,
		hasHover: w.hasHover, mouseInside: w.mouseInside,
		collapsed: w.collapsed, locked: w.locked,
		dropControl: w.dropControl, dropIndex: w.dropIndex,
		draggingItemID:   w.draggingItemID,
		sliderControl:    w.sliderControl,
		sliderValueMilli: int(w.sliderValue*1000 + 0.5),
		iconVersion:      iconVersion,
	}
	if u.editing {
		key.moveHint = u.label("[拖动]", "[DRAG]")
	}
	var backdrop *gioui.SpatialBackdrop
	isSpatialGlass := key.colors.Translucent && apptheme.NormalizeGlassStyle(key.colors.GlassStyle) == apptheme.GlassSmoked && key.colors.Opacity > 0
	if isSpatialGlass {
		key.elevated = w.dragging
		if w.hwnd != 0 && key.colors.Opacity < 100 {
			wr := windowRect(w.hwnd)
			key.screenPos = point{wr.Left, wr.Top}
			key.backdropVersion = u.backdrop.version
			alpha := uint8(max(0, min(100, key.colors.Opacity)) * 255 / 100)
			padL, padT, padR, padB := gioui.SpatialGlassPadding(size, key.dpi, true, alpha)
			frameScreen := image.Rect(
				int(wr.Left)+padL,
				int(wr.Top)+padT,
				int(wr.Left)+size.X-padR,
				int(wr.Top)+size.Y-padB,
			)
			backdrop = u.backdrop.sliceForRect(frameScreen)
		}
	}
	opt := gioui.WidgetOptions{
		DPI: key.dpi, Size: key.size, Colors: key.colors, MoveHint: key.moveHint,
		HasFocus: key.hasFocus, KeyboardFocus: key.keyboardFocus, Focused: key.focused,
		HasHover: key.hasHover, Hovered: key.hovered,
		MouseInside: key.mouseInside, Collapsed: key.collapsed, Locked: key.locked,
		DropControl: key.dropControl, DropIndex: key.dropIndex, DraggingItemID: key.draggingItemID,
		SliderDragControl: w.sliderControl, SliderDragValue: w.sliderValue,
		Scroll:   w.listScroll,
		Icons:    icons,
		Backdrop: backdrop,
		Elevated: key.elevated,
	}
	return key, opt
}

// presentWidget renders and submits a per-pixel translucent layered frame via
// UpdateLayeredWindow. If Gio offscreen rendering is unavailable, it falls back
// to GDI painting inside WM_PAINT with whole-window opacity.
func (u *UI) presentWidget(w *widgetWindow) {
	if w == nil || w.hwnd == 0 {
		return
	}
	size := widgetSize(w.hwnd)
	if size.X < 1 || size.Y < 1 {
		return
	}
	key, opt := u.widgetKeyAndOptions(w, size)
	frame := &w.frame
	if !frame.current || key != frame.key {
		frame.current = false
		if !time.Now().Before(u.widgetRenderer.retryAfter) {
			if err := u.widgetRenderer.render(w.model.View, key.locale, opt, frame); err != nil {
				log.Printf("Gio desktop renderer unavailable: %v", err)
				u.widgetRenderer.release()
				u.widgetRenderer.retryAfter = time.Now().Add(30 * time.Second)
			} else {
				frame.key, frame.current = key, true
			}
		}
	}
	if frame.current && frame.image != nil && presentLayeredBitmap(w.hwnd, frame) {
		w.layeredOK = true
		return
	}
	// Fallback to GDI WM_PAINT with whole-window opacity.
	w.layeredOK = false
	alpha := byte(max(45, key.colors.Opacity*255/100))
	setLayeredWindowAttributes.Call(w.hwnd, 0, uintptr(alpha), lwaAlpha)
	repaint(w.hwnd)
}

func (u *UI) paintWidget(w *widgetWindow) {
	var ps paintStruct
	hdc, _, _ := beginPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer endPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	if w.layeredOK {
		return
	}
	size := widgetSize(w.hwnd)
	if hdc == 0 || size.X < 1 || size.Y < 1 {
		return
	}
	key, opt := u.widgetKeyAndOptions(w, size)
	if !w.frame.current && !time.Now().Before(u.widgetRenderer.retryAfter) {
		if err := u.widgetRenderer.render(w.model.View, key.locale, opt, &w.frame); err == nil {
			w.frame.key, w.frame.current = key, true
			if presentLayeredBitmap(w.hwnd, &w.frame) {
				w.layeredOK = true
				return
			}
		}
	}
	opt.Colors.Translucent = false
	fallback := widgetGDICanvas{dc: hdc, dpi: int32(key.dpi), fonts: make(map[widgetFontKey]uintptr)}
	defer fallback.release()
	gioui.LayoutWidget(w.model.View, opt, &fallback)
}

func (r *widgetRenderer) release() {
	if r.window != nil {
		r.window.Release()
	}
	*r = widgetRenderer{}
}

func (r *widgetRenderer) render(view v1.View, locale string, options gioui.WidgetOptions, frame *widgetFrame) error {
	size := options.Size
	var target image.Point
	if r.window != nil {
		target = r.window.Size()
	}
	if r.window == nil || size.X > target.X || size.Y > target.Y {
		// A single device serves all widgets. Once large enough for each
		// widget, it only needs to read back the widget's own rectangle.
		if r.window != nil {
			r.window.Release()
			r.window = nil
		}
		var err error
		r.window, err = headless.NewWindow(max(size.X, target.X), max(size.Y, target.Y))
		if err != nil {
			return err
		}
	}
	if r.shaper == nil {
		r.shaper = gioui.NewTextShaper()
	}
	r.ops.Reset()
	scale := float32(options.DPI) / 96
	gtx := layout.Context{
		Ops:         &r.ops,
		Constraints: layout.Exact(size),
		Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
		Locale:      system.Locale{Language: locale},
	}
	uiFamily := giofont.Typeface("Segoe UI Variable Text, Segoe UI, Microsoft YaHei UI, sans-serif")
	monoFamily := giofont.Typeface("Cascadia Mono, JetBrains Mono, Consolas, Microsoft YaHei UI, monospace")
	if strings.HasPrefix(locale, "zh") {
		uiFamily = "Microsoft YaHei UI, Segoe UI Variable Text, Segoe UI, sans-serif"
	}
	gioui.LayoutWidget(view, options, gioui.GioWidgetCanvas{
		Context:      gtx,
		Shaper:       r.shaper,
		Typeface:     uiFamily,
		MonoTypeface: monoFamily,
	})
	if err := r.window.Frame(&r.ops); err != nil {
		return err
	}
	if frame.image == nil || frame.image.Rect.Size() != size {
		frame.image = image.NewRGBA(image.Rectangle{Max: size})
	}
	return r.window.Screenshot(frame.image)
}

// rgbaToPremultipliedBGRA converts Gio's RGBA framebuffer pixels into the
// 32-bit premultiplied BGRA layout required by Win32 UpdateLayeredWindow.
func rgbaToPremultipliedBGRA(dst []byte, src *image.RGBA) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	for y := 0; y < h; y++ {
		sRow := src.Pix[y*src.Stride : y*src.Stride+w*4]
		dRow := dst[y*w*4 : (y+1)*w*4]
		for i := 0; i < w*4; i += 4 {
			r, g, b, a := sRow[i], sRow[i+1], sRow[i+2], sRow[i+3]
			if r > a {
				r = a
			}
			if g > a {
				g = a
			}
			if b > a {
				b = a
			}
			dRow[i] = b
			dRow[i+1] = g
			dRow[i+2] = r
			dRow[i+3] = a
		}
	}
}

func presentLayeredBitmap(hwnd uintptr, frame *widgetFrame) bool {
	size := frame.image.Rect.Size()
	if !frame.ensureDIB(size) {
		return false
	}
	byteLen := size.X * size.Y * 4
	dst := unsafe.Slice((*byte)(unsafe.Pointer(frame.dibBits)), byteLen)
	rgbaToPremultipliedBGRA(dst, frame.image)
	sz := point{int32(size.X), int32(size.Y)}
	src := point{}
	blend := blendFunction{
		BlendOp:             acSrcOver,
		SourceConstantAlpha: 255,
		AlphaFormat:         acSrcAlpha,
	}
	ok, _, _ := updateLayeredWindow.Call(
		hwnd, 0, 0,
		uintptr(unsafe.Pointer(&sz)),
		frame.dibDC,
		uintptr(unsafe.Pointer(&src)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
	return ok != 0
}

// The mask order describes Gio's RGBA pixels without an extra BGRA copy.
// A negative height tells GDI that the first scan line is at the top.
type widgetBitmapInfo struct {
	Size                   uint32
	Width, Height          int32
	Planes, Bits           uint16
	Compression, ImageSize uint32
	Xppm, Yppm             int32
	Colors, Important      uint32
	Masks                  [3]uint32
}

var widgetStretchDIBits = gdi32.NewProc("StretchDIBits")

func drawWidgetBitmap(hdc uintptr, img *image.RGBA) bool {
	width, height := img.Rect.Dx(), img.Rect.Dy()
	info := widgetBitmapInfo{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, Bits: 32, Compression: 3,
		Masks: [3]uint32{0xff, 0xff00, 0xff0000}}
	result, _, _ := widgetStretchDIBits.Call(hdc, 0, 0, uintptr(width), uintptr(height), 0, 0, uintptr(width), uintptr(height),
		uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&info)), 0, 0x00cc0020)
	return int32(result) > 0
}

type widgetFontKey struct {
	size int
	bold bool
	mono bool
}
type widgetGDICanvas struct {
	dc    uintptr
	dpi   int32
	fonts map[widgetFontKey]uintptr
}

func (c *widgetGDICanvas) Fill(r image.Rectangle, col color.NRGBA, _ int) {
	if col.A == 0 || r.Empty() {
		return
	}
	fill(c.dc, widgetRect(r), rgb(col.R, col.G, col.B))
}

func (c *widgetGDICanvas) Stroke(r image.Rectangle, col color.NRGBA, _, width int) {
	if col.A == 0 || r.Empty() || width <= 0 {
		return
	}
	w := int32(max(1, width))
	cr := rgb(col.R, col.G, col.B)
	wr := widgetRect(r)
	fill(c.dc, rect{wr.Left, wr.Top, wr.Right, wr.Top + w}, cr)
	fill(c.dc, rect{wr.Left, wr.Bottom - w, wr.Right, wr.Bottom}, cr)
	fill(c.dc, rect{wr.Left, wr.Top, wr.Left + w, wr.Bottom}, cr)
	fill(c.dc, rect{wr.Right - w, wr.Top, wr.Right, wr.Bottom}, cr)
}

func (c *widgetGDICanvas) Image(r image.Rectangle, img image.Image) {
	if rgba, ok := img.(*image.RGBA); ok {
		drawRGBAIcon(c.dc, widgetRect(r), rgba)
	}
}

// Use text glyphs when the Gio renderer is unavailable so icon-only buttons
// retain a visible target on the GDI surface.
func (c *widgetGDICanvas) Icon(r image.Rectangle, name string, ink color.NRGBA) {
	if !r.Empty() {
		c.Label(r, symbolGlyph(name), gioui.WidgetText{Size: max(8, min(22, r.Dy()*96/int(max(1, c.dpi)))), Color: ink, Align: gioui.WidgetCenter})
	}
}

func symbolGlyph(name string) string {
	glyph := map[string]string{
		"chevron-left": "‹", "chevron-right": "›", "today": "●", "plus": "+",
		"more": "⋯", "lock": "▣", "pin": "◆", "search": "⌕", "calendar": "▦",
		"folder": "▱", "note": "▤", "image": "▧", "list": "☰", "grid": "⊞",
	}[name]
	if glyph == "" {
		glyph = "◇"
	}
	return glyph
}

func (c *widgetGDICanvas) Label(r image.Rectangle, value string, style gioui.WidgetText) {
	key := widgetFontKey{style.Size, style.Bold, style.Mono}
	f := c.fonts[key]
	if f == 0 {
		face := "Segoe UI"
		if style.Mono {
			face = "Consolas"
		}
		f = fontFace(c.dpi, int32(style.Size), style.Bold, face)
		c.fonts[key] = f
	}
	col := style.Color
	text(c.dc, widgetRect(r), value, f, rgb(col.R, col.G, col.B), uintptr(style.Align))
}

func (c *widgetGDICanvas) release() {
	for _, f := range c.fonts {
		deleteObject.Call(f)
	}
}
