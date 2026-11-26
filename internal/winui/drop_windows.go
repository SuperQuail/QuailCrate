package winui

import (
	"log"
	"net/url"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	v1 "quailcrate/pluginapi/v1"
)

var (
	registerDragDrop = ole32.NewProc("RegisterDragDrop")
	revokeDragDrop   = ole32.NewProc("RevokeDragDrop")
	releaseStgMedium = ole32.NewProc("ReleaseStgMedium")
	dragQueryFile    = shell32.NewProc("DragQueryFileW")
)

type guid struct {
	Data1        uint32
	Data2, Data3 uint16
	Data4        [8]byte
}

type formatEtc struct {
	Format uint16
	Device uintptr
	Aspect uint32
	Index  int32
	Medium uint32
}

type stgMedium struct {
	Kind    uint32
	Value   uintptr
	Release uintptr
}

type dropKind int

const (
	dropNone dropKind = iota
	dropHDrop
	dropURLW
	dropUnicodeText
)

type dropTarget struct {
	identity *uintptr
	pin      runtime.Pinner
	ui       *UI
	widget   *widgetWindow
	refs     uintptr
	kind     dropKind
}

var (
	iidUnknown    = guid{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidDropTarget = guid{Data1: 0x00000122, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	targets       = make(map[uintptr]*dropTarget)
	dropVTable    [7]uintptr
	cfUniformURLW uint16
)

func uniformURLFormat() uint16 {
	if cfUniformURLW == 0 {
		id, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(wstr("UniformResourceLocatorW"))))
		cfUniformURLW = uint16(id)
	}
	return cfUniformURLW
}

func registerDropTarget(ui *UI, widget *widgetWindow) *dropTarget {
	if dropVTable[0] == 0 {
		dropVTable = [7]uintptr{
			syscall.NewCallback(dropQueryInterface), syscall.NewCallback(dropAddRef), syscall.NewCallback(dropRelease),
			syscall.NewCallback(dropEnter), syscall.NewCallback(dropOver), syscall.NewCallback(dropLeave), syscall.NewCallback(dropFiles),
		}
	}
	// COM retains the external identity between calls. Keep the pinned object
	// limited to its vtable address and keep Go state in the STA-owned map.
	identity := new(uintptr)
	*identity = uintptr(unsafe.Pointer(&dropVTable))
	target := &dropTarget{identity: identity, ui: ui, widget: widget, refs: 1}
	target.pin.Pin(identity)
	self := uintptr(unsafe.Pointer(identity))
	targets[self] = target
	hr, _, _ := registerDragDrop.Call(widget.hwnd, self)
	if int32(hr) < 0 {
		delete(targets, self)
		target.pin.Unpin()
		log.Printf("widget %s register file drops: %#08x", widget.model.ID, uint32(hr))
		return nil
	}
	return target
}

func revokeDropTarget(target *dropTarget) {
	hwnd := target.widget.hwnd
	target.widget = nil
	revokeDragDrop.Call(hwnd)
	dropRelease(uintptr(unsafe.Pointer(target.identity)))
}

func dropQueryInterface(self, riid, result uintptr) uintptr {
	if result == 0 {
		return 0x80004003
	}
	out := nativePointer[uintptr](result)
	*out = 0
	if riid == 0 {
		return 0x80004003
	}
	if iid := *nativePointer[guid](riid); iid != iidUnknown && iid != iidDropTarget {
		return 0x80004002
	}
	*out = self
	dropAddRef(self)
	return 0
}

func dropAddRef(self uintptr) uintptr {
	t := targets[self]
	if t == nil {
		return 0
	}
	t.refs++
	return t.refs
}

func dropRelease(self uintptr) uintptr {
	t := targets[self]
	if t == nil {
		return 0
	}
	t.refs--
	if t.refs == 0 {
		delete(targets, self)
		t.pin.Unpin()
	}
	return t.refs
}

// comCall keeps pointer arguments alive while COM consumes them synchronously.
//
//go:uintptrescapes
func comCall(object uintptr, slot int, args ...uintptr) uintptr {
	vtable := *nativePointer[*uintptr](object)
	address := *(*uintptr)(unsafe.Add(unsafe.Pointer(vtable), uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	params := make([]uintptr, 1, len(args)+1)
	params[0] = object
	params = append(params, args...)
	result, _, _ := syscall.SyscallN(address, params...)
	return result
}

func dropFormat(fmtID uint16) formatEtc { return formatEtc{Format: fmtID, Aspect: 1, Index: -1, Medium: 1} }

func detectDropKind(object uintptr) dropKind {
	if object == 0 {
		return dropNone
	}
	hdrop := dropFormat(15) // CF_HDROP
	if int32(comCall(object, 5, uintptr(unsafe.Pointer(&hdrop)))) == 0 {
		return dropHDrop
	}
	if cf := uniformURLFormat(); cf != 0 {
		uf := dropFormat(cf)
		if int32(comCall(object, 5, uintptr(unsafe.Pointer(&uf)))) == 0 {
			return dropURLW
		}
	}
	txt := dropFormat(13) // CF_UNICODETEXT
	if int32(comCall(object, 5, uintptr(unsafe.Pointer(&txt)))) == 0 {
		if readDroppedURL(object, 13) != "" {
			return dropUnicodeText
		}
	}
	return dropNone
}

func readDroppedURL(object uintptr, fmtID uint16) string {
	format := dropFormat(fmtID)
	var medium stgMedium
	if int32(comCall(object, 3, uintptr(unsafe.Pointer(&format)), uintptr(unsafe.Pointer(&medium)))) < 0 {
		return ""
	}
	defer releaseStgMedium.Call(uintptr(unsafe.Pointer(&medium)))
	if medium.Kind != 1 || medium.Value == 0 {
		return ""
	}
	ptr, _, _ := globalLock.Call(medium.Value)
	if ptr == 0 {
		return ""
	}
	defer globalUnlock.Call(medium.Value)
	raw := strings.TrimSpace(windowsUTF16FromPtr((*uint16)(unsafe.Pointer(ptr)), 4096))
	if nl := strings.IndexAny(raw, "\r\n"); nl >= 0 {
		raw = strings.TrimSpace(raw[:nl])
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	s := strings.ToLower(parsed.Scheme)
	if s != "http" && s != "https" {
		return ""
	}
	return raw
}

func windowsUTF16FromPtr(p *uint16, maxLen int) string {
	if p == nil {
		return ""
	}
	n := 0
	for n < maxLen && *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

func dropEnter(self, object, keys, screen, allowed uintptr) uintptr {
	t := targets[self]
	if t == nil || allowed == 0 {
		return 0x80004003
	}
	t.kind = detectDropKind(object)
	return dropOver(self, keys, screen, allowed)
}

func (t *dropTarget) locate(screen uintptr) (controlID string, index int, tabID string) {
	w := t.widget
	if w == nil || t.kind == dropNone {
		return "", 0, ""
	}
	p := point{X: int32(screen), Y: int32(screen >> 32)}
	screenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	h, _ := hitAt(w, p)
	if h.drop {
		if h.controlID == "__tab__" {
			return "__tab__", h.index, h.itemID
		}
		to := h.index
		if h.itemID != "" {
			if h.area.width() <= h.area.height()*3/2 {
				if p.X >= h.area.Left+h.area.width()/2 {
					to++
				}
			} else if p.Y >= h.area.Top+h.area.height()/2 {
				to++
			}
		}
		return h.controlID, to, ""
	}
	for _, zone := range w.dropZones {
		if zone.area.contains(p) {
			return zone.controlID, zone.index, ""
		}
	}
	return "", 0, ""
}

func (t *dropTarget) indicate(id string, index int) {
	w := t.widget
	if w != nil && (w.dropControl != id || w.dropIndex != index) {
		w.dropControl, w.dropIndex = id, index
		if t.ui != nil {
			t.ui.presentWidget(w)
		} else {
			repaint(w.hwnd)
		}
	}
}

func dropEffect(allowed uint32) uint32 {
	if allowed&4 != 0 {
		return 4
	}
	if allowed&1 != 0 {
		return 1
	}
	return 0
}

func dropOver(self, keys, screen, allowed uintptr) uintptr {
	t := targets[self]
	if t == nil || allowed == 0 {
		return 0x80004003
	}
	effect := nativePointer[uint32](allowed)
	id, index, _ := t.locate(screen)
	accepted := dropEffect(*effect)
	if id == "" {
		accepted = 0
	}
	if accepted == 0 {
		id = ""
	}
	*effect = accepted
	t.indicate(id, index)
	return 0
}

func dropLeave(self uintptr) uintptr {
	t := targets[self]
	if t != nil {
		t.kind = dropNone
		t.indicate("", 0)
	}
	return 0
}

func dropFiles(self, object, keys, screen, allowed uintptr) uintptr {
	t := targets[self]
	if t == nil || allowed == 0 {
		return 0x80004003
	}
	kind := t.kind
	defer dropLeave(self)
	effect := nativePointer[uint32](allowed)
	accepted := dropEffect(*effect)
	*effect = 0
	id, index, targetTab := t.locate(screen)
	if id == "" || accepted == 0 || object == 0 || kind == dropNone {
		return 0
	}
	emitControl := id
	emitIndex := index
	if id == "__tab__" {
		emitControl = "items"
		emitIndex = -1
	}
	if kind == dropURLW || kind == dropUnicodeText {
		fmtID := uint16(13)
		if kind == dropURLW {
			fmtID = uniformURLFormat()
		}
		if droppedURL := readDroppedURL(object, fmtID); droppedURL != "" {
			*effect = accepted
			t.ui.emitWidget(t.widget, v1.Event{
				Type:        "widget.drop",
				ControlID:   emitControl,
				TargetTabID: targetTab,
				To:          emitIndex,
				Urls:        []string{droppedURL},
			})
		}
		return 0
	}
	format := dropFormat(15)
	var medium stgMedium
	if int32(comCall(object, 3, uintptr(unsafe.Pointer(&format)), uintptr(unsafe.Pointer(&medium)))) < 0 {
		return 0
	}
	defer releaseStgMedium.Call(uintptr(unsafe.Pointer(&medium)))
	if medium.Kind != 1 || medium.Value == 0 {
		return 0
	}
	count, _, _ := dragQueryFile.Call(medium.Value, 0xffffffff, 0, 0)
	paths := make([]string, 0, min(1024, count))
	for i := uintptr(0); i < count && i < 1024; i++ {
		n, _, _ := dragQueryFile.Call(medium.Value, i, 0, 0)
		if n == 0 || n >= 32768 {
			continue
		}
		buffer := make([]uint16, n+1)
		if read, _, _ := dragQueryFile.Call(medium.Value, i, uintptr(unsafe.Pointer(&buffer[0])), n+1); read > 0 {
			paths = append(paths, syscall.UTF16ToString(buffer))
		}
	}
	if len(paths) > 0 {
		*effect = accepted
		t.ui.emitWidget(t.widget, v1.Event{
			Type:        "widget.drop",
			ControlID:   emitControl,
			TargetTabID: targetTab,
			To:          emitIndex,
			Paths:       paths,
		})
	}
	return 0
}
