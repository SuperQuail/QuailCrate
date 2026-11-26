package pluginaudio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type pcmFormat struct {
	tag, channels        uint16
	rate, bytesPerSecond uint32
	align, bits, extra   uint16
}

type pcmSource struct {
	format pcmFormat
	length int64
}

// Canonical PCM has a known total length and no indirect resources. It can
// begin playback immediately. Other WAV layouts take the Media Foundation path.
func peekPCM(r *bufio.Reader) (pcmSource, bool) {
	b, err := r.Peek(44)
	if err != nil || string(b[:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		return pcmSource{}, false
	}
	u16, u32 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32
	f := pcmFormat{tag: u16(b[20:]), channels: u16(b[22:]), rate: u32(b[24:]), bytesPerSecond: u32(b[28:]), align: u16(b[32:]), bits: u16(b[34:])}
	length := int64(u32(b[40:]))
	if u32(b[16:]) != 16 || f.tag != 1 || f.channels < 1 || f.channels > 2 || f.bits != 16 || f.rate < 8000 || f.rate > 192000 || f.align != f.channels*2 || f.bytesPerSecond != f.rate*uint32(f.align) || length == 0 || length+44 > maxMediaBytes || length%int64(f.align) != 0 || int64(u32(b[4:])) != 36+length {
		return pcmSource{}, false
	}
	r.Discard(44)
	return pcmSource{f, length}, true
}

var winmm = windows.NewLazySystemDLL("winmm.dll")
var (
	waveOpen      = winmm.NewProc("waveOutOpen")
	waveClose     = winmm.NewProc("waveOutClose")
	wavePause     = winmm.NewProc("waveOutPause")
	waveRestart   = winmm.NewProc("waveOutRestart")
	waveReset     = winmm.NewProc("waveOutReset")
	wavePrepare   = winmm.NewProc("waveOutPrepareHeader")
	waveUnprepare = winmm.NewProc("waveOutUnprepareHeader")
	waveWrite     = winmm.NewProc("waveOutWrite")
	wavePosition  = winmm.NewProc("waveOutGetPosition")
)

// Use separate native headers and per-stream software volume. The legacy
// waveOutSetVolume call changes a device's volume on some Windows drivers.
type waveHeader struct {
	data             *byte
	length, recorded uint32
	user             uintptr
	flags, loops     uint32
	next, reserved   uintptr
}
type waveChunk struct {
	header waveHeader
	data   []byte
	pin    runtime.Pinner
}
type waveDevice struct {
	handle uintptr
	f      pcmFormat
	chunks []*waveChunk
	base   int64
}

//go:uintptrescapes
func waveCall(op *windows.LazyProc, args ...uintptr) error {
	code, _, _ := op.Call(args...)
	if code != 0 {
		return fmt.Errorf("%s failed (audio device code %d)", op.Name, code)
	}
	return nil
}

func (d *waveDevice) submit(pcm []byte, volume float64) error {
	if volume != 1 {
		for offset := 0; offset < len(pcm); offset += 2 {
			sample := int16(binary.LittleEndian.Uint16(pcm[offset:]))
			binary.LittleEndian.PutUint16(pcm[offset:], uint16(int16(float64(sample)*volume)))
		}
	}
	c := &waveChunk{data: pcm}
	c.header.data = &pcm[0]
	c.header.length = uint32(len(pcm))
	c.pin.Pin(&c.header)
	c.pin.Pin(&pcm[0])
	h := uintptr(unsafe.Pointer(&c.header))
	if err := waveCall(wavePrepare, d.handle, h, unsafe.Sizeof(c.header)); err != nil {
		c.pin.Unpin()
		return err
	}
	if err := waveCall(waveWrite, d.handle, h, unsafe.Sizeof(c.header)); err != nil {
		waveUnprepare.Call(d.handle, h, unsafe.Sizeof(c.header))
		c.pin.Unpin()
		return err
	}
	d.chunks = append(d.chunks, c)
	return nil
}

func (d *waveDevice) reap() error {
	for len(d.chunks) > 0 {
		c := d.chunks[0]
		if atomic.LoadUint32(&c.header.flags)&1 == 0 {
			break
		} // WHDR_DONE
		if err := waveCall(waveUnprepare, d.handle, uintptr(unsafe.Pointer(&c.header)), unsafe.Sizeof(c.header)); err != nil {
			return err
		}
		c.pin.Unpin()
		d.chunks = d.chunks[1:]
	}
	return nil
}

func (d *waveDevice) positionMS() int64 {
	var tm = [3]uint32{4} // TIME_BYTES; the driver can return TIME_MS or TIME_SAMPLES.
	if err := waveCall(wavePosition, d.handle, uintptr(unsafe.Pointer(&tm)), unsafe.Sizeof(tm)); err != nil {
		return d.base
	}
	switch tm[0] {
	case 1:
		return d.base + int64(tm[1])
	case 2:
		return d.base + int64(tm[1])*1000/int64(d.f.rate)
	case 4:
		return d.base + int64(tm[1])*1000/int64(d.f.bytesPerSecond)
	default:
		return d.base
	}
}

type pcmCache struct {
	file      *os.File
	available atomic.Int64
}

func (f *pcmCache) Write(p []byte) (int, error) {
	n, err := f.file.Write(p)
	f.available.Add(int64(n))
	return n, err
}

func (s *session) playPCM(r io.Reader, pcm pcmSource, closeInput func(), notify func(Event)) error {
	f, err := os.CreateTemp("", "quailcrate-pcm-*")
	if err != nil {
		return errors.New("could not create PCM cache")
	}
	cache := &pcmCache{file: f}
	defer os.Remove(f.Name())
	defer f.Close()
	done := make(chan struct{})
	var downloadErr error
	go func() {
		defer close(done)
		defer closeInput()
		_, downloadErr = io.CopyN(cache, r, pcm.length)
	}()
	defer func() { closeInput(); <-done }()
	d := waveDevice{f: pcm.format}
	if err := waveCall(waveOpen, uintptr(unsafe.Pointer(&d.handle)), 0xffffffff, uintptr(unsafe.Pointer(&d.f)), 0, 0, 0); err != nil {
		return err
	}
	defer func() {
		waveReset.Call(d.handle)
		d.reap()
		waveClose.Call(d.handle)
	}()
	tick := time.NewTicker(12 * time.Millisecond)
	defer tick.Stop()
	offset := int64(0)
	duration := pcm.length * 1000 / int64(d.f.bytesPerSecond)
	block := int64(d.f.rate/30) * int64(d.f.align)
	mode, volume := "loading", 1.0
	for {
		if err := d.reap(); err != nil {
			return err
		}
		select {
		case <-done:
			if downloadErr != nil {
				return errors.New("PCM audio stream interrupted")
			}
		default:
		}
		for mode != "paused" && len(d.chunks) < 4 {
			amount := min(block, cache.available.Load()-offset)
			amount -= amount % int64(d.f.align)
			if amount <= 0 {
				break
			}
			buf := make([]byte, amount)
			if _, err := f.ReadAt(buf, offset); err != nil {
				return errors.New("could not read cached PCM")
			}
			if err := d.submit(buf, volume); err != nil {
				return err
			}
			offset += amount
			if mode == "loading" {
				mode = "playing"
				s.update(mode, 0, duration, true)
				s.emit(notify, "audio.stateChanged", "")
			}
		}
		if mode != "paused" && offset == pcm.length && len(d.chunks) == 0 {
			s.update("ended", duration, duration, false)
			s.emit(notify, "audio.ended", "")
			return nil
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-tick.C:
		case cmd := <-s.commands:
			var err error
			switch cmd.action {
			case "audio.pause":
				err = waveCall(wavePause, d.handle)
				if err == nil {
					mode = "paused"
				}
			case "audio.resume":
				err = waveCall(waveRestart, d.handle)
				if err == nil {
					mode = "playing"
				}
			case "audio.seek":
				err = waveCall(waveReset, d.handle)
				if err == nil {
					err = d.reap()
					offset = min(pcm.length, cmd.position*int64(d.f.rate)/1000*int64(d.f.align))
					d.base = cmd.position
					if mode == "paused" && err == nil {
						err = waveCall(wavePause, d.handle)
					}
				}
			case "audio.setVolume":
				volume = cmd.volume
			}
			s.mu.Lock()
			s.state.PositionMS = min(duration, d.positionMS())
			s.state.Status = mode
			s.state.Volume = volume
			s.mu.Unlock()
			if cmd.action != "audio.getState" && err == nil {
				s.emit(notify, "audio.stateChanged", "")
			}
			cmd.result <- err
		}
	}
}
