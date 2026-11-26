package pluginaudio

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mfplat       = windows.NewLazySystemDLL("mfplat.dll")
	startMF      = mfplat.NewProc("MFStartup")
	stopMF       = mfplat.NewProc("MFShutdown")
	createFileMF = mfplat.NewProc("MFCreateFile")
	createPlayer = windows.NewLazySystemDLL("mfplay.dll").NewProc("MFPCreateMediaPlayer")
	timeFormat   windows.GUID // MFP_POSITIONTYPE_100NS is GUID_NULL.
)

// Method indices from the Windows SDK's IMFPMediaPlayer vtable (mfplay.h).
const (
	mfPlay           = 3
	mfPause          = 4
	mfSetPosition    = 7
	mfGetPosition    = 8
	mfGetDuration    = 9
	mfItemFromObject = 15
	mfSetItem        = 16
	mfSetVolume      = 20
	mfShutdown       = 38
)

const (
	eventPlay        = 0
	eventPause       = 1
	eventPositionSet = 3
	eventItemSet     = 6
	eventError       = 10
	eventEnded       = 11
)

type playbackEvent struct {
	kind uint32
	hr   int32
}

func (event playbackEvent) err() error {
	if event.hr < 0 || event.kind == eventError {
		return fmt.Errorf("audio device or decoder error (HRESULT 0x%08x)", uint32(event.hr))
	}
	return nil
}

type comObject struct{ vtable *[39]uintptr }

//go:uintptrescapes
func (obj *comObject) call(method int, args ...uintptr) uintptr {
	result, _, _ := syscall.SyscallN(obj.vtable[method], append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)...)
	return result
}

func checked(op string, hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s failed (HRESULT 0x%08x)", op, uint32(hr))
	}
	return nil
}

type player struct {
	obj      *comObject
	seekable bool
	callback *callback
}

func openPlayer(path string) (_ *player, err error) {
	for _, proc := range []*windows.LazyProc{startMF, stopMF, createFileMF, createPlayer} {
		if err := proc.Find(); err != nil {
			return nil, errors.New("Windows Media Foundation is unavailable")
		}
	}
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, syscall.Errno(1)) {
		return nil, err
	}
	hr, _, _ := startMF.Call(0x20070, 1) // MFSTARTUP_NOSOCKET.
	if err := checked("MFStartup", hr); err != nil {
		windows.CoUninitialize()
		return nil, err
	}
	p := &player{}
	defer func() {
		if err != nil {
			p.close()
		}
	}()
	p.callback = newCallback()
	hr, _, _ = createPlayer.Call(0, 0, 1, uintptr(unsafe.Pointer(p.callback)), 0, uintptr(unsafe.Pointer(&p.obj)))
	if err := checked("MFPCreateMediaPlayer", hr); err != nil {
		return nil, err
	}
	filename, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var stream *comObject
	hr, _, _ = createFileMF.Call(1, 0, 0, uintptr(unsafe.Pointer(filename)), uintptr(unsafe.Pointer(&stream)))
	if err := checked("MFCreateFile", hr); err != nil {
		return nil, err
	}
	defer stream.call(2)
	var item *comObject
	if err := checked("CreateMediaItemFromObject", p.obj.call(mfItemFromObject, uintptr(unsafe.Pointer(stream)), 1, 0, uintptr(unsafe.Pointer(&item)))); err != nil {
		return nil, err
	}
	defer item.call(2)
	var protected, flags uint32
	if err := checked("IsProtected", item.call(12, uintptr(unsafe.Pointer(&protected)))); err != nil {
		return nil, err
	}
	if protected != 0 {
		return nil, errors.New("protected audio is unsupported")
	}
	if err := checked("GetCharacteristics", item.call(19, uintptr(unsafe.Pointer(&flags)))); err != nil {
		return nil, err
	}
	p.seekable = flags&2 != 0
	if err := checked("SetMediaItem", p.obj.call(mfSetItem, uintptr(unsafe.Pointer(item)))); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *player) close() {
	if p.obj != nil {
		p.obj.call(mfShutdown)
		p.obj.call(2)
	}
	if p.callback != nil {
		p.callback.release()
	}
	stopMF.Call()
	windows.CoUninitialize()
}

func (p *player) invoke(method int) error {
	return checked("playback command", p.obj.call(method))
}

// The 64-bit PROPVARIANT has a 16-byte union following its 8-byte header.
type timeVariant struct {
	vt  uint16
	_   [6]byte
	hns int64
	_   [8]byte
}

func (p *player) time(method int) int64 {
	var v timeVariant
	hr := p.obj.call(method, uintptr(unsafe.Pointer(&timeFormat)), uintptr(unsafe.Pointer(&v)))
	if int32(hr) < 0 || (v.vt != 20 && v.vt != 21) || v.hns < 0 {
		return 0
	}
	return v.hns / 10000
}

func (p *player) seek(ms int64) error {
	v := timeVariant{vt: 20, hns: ms * 10000} // VT_I8
	return checked("SetPosition", p.obj.call(mfSetPosition, uintptr(unsafe.Pointer(&timeFormat)), uintptr(unsafe.Pointer(&v))))
}

func (p *player) volume(value float64) error {
	// The Windows amd64 syscall trampoline mirrors argument slots into XMM
	// registers, as required for the float argument to SetVolume.
	return checked("SetVolume", p.obj.call(mfSetVolume, uintptr(math.Float32bits(float32(value)))))
}

// MFPlay retains an IMFPMediaPlayerCallback across Go calls. Pin its COM
// identity and keep the callback alive until the last native Release.
type callback struct {
	vtable  *[4]uintptr
	refs    atomic.Int32
	mu      sync.Mutex
	pending []playbackEvent
	ready   chan struct{}
	pin     runtime.Pinner
}

var liveCallbacks sync.Map
var iidCallback = windows.GUID{Data1: 0x766c8ffb, Data2: 0x5fdb, Data3: 0x4fea, Data4: [8]byte{0xa2, 0x8d, 0xb9, 0x12, 0x99, 0x6f, 0x51, 0xbd}}
var iidUnknown = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

var callbackMethods = [4]uintptr{
	syscall.NewCallback(func(c *callback, iid *windows.GUID, out *uintptr) uintptr {
		if out == nil || iid == nil {
			return 0x80004003
		}
		*out = 0
		if *iid != iidUnknown && *iid != iidCallback {
			return 0x80004002
		}
		c.refs.Add(1)
		*out = uintptr(unsafe.Pointer(c))
		return 0
	}),
	syscall.NewCallback(func(c *callback) uintptr { return uintptr(c.refs.Add(1)) }),
	syscall.NewCallback(func(c *callback) uintptr { return c.release() }),
	syscall.NewCallback(func(c *callback, header *playbackEvent) uintptr {
		if header != nil {
			switch {
			case header.hr < 0, header.kind == eventPlay, header.kind == eventPause, header.kind == eventPositionSet, header.kind == eventItemSet, header.kind == eventError, header.kind == eventEnded:
				c.mu.Lock()
				c.pending = append(c.pending, *header)
				c.signal()
				c.mu.Unlock()
			}
		}
		return 0
	}),
}

func newCallback() *callback {
	c := &callback{vtable: &callbackMethods, ready: make(chan struct{}, 1)}
	c.refs.Store(1)
	c.pin.Pin(c)
	liveCallbacks.Store(c, struct{}{})
	return c
}

// The native worker must never wait for the Go media thread, which might be
// in Shutdown. Use a wake-up signal and retain the ordered events until read.
func (c *callback) signal() {
	select {
	case c.ready <- struct{}{}:
	default:
	}
}

func (c *callback) pop() playbackEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	ev := c.pending[0]
	c.pending = c.pending[1:]
	if len(c.pending) > 0 {
		c.signal()
	}
	return ev
}

func (c *callback) release() uintptr {
	refs := c.refs.Add(-1)
	if refs == 0 {
		liveCallbacks.Delete(c)
		c.pin.Unpin()
	}
	return uintptr(refs)
}
