package pluginaudio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32DLL           = windows.NewLazySystemDLL("ole32.dll")
	coCreateInstanceMF = ole32DLL.NewProc("CoCreateInstance")
	mfCreateMediaType  = mfplat.NewProc("MFCreateMediaType")
	mfCreateSample     = mfplat.NewProc("MFCreateSample")
	mfCreateMemBuffer  = mfplat.NewProc("MFCreateMemoryBuffer")
)

var (
	clsidCMSAACDecMFT = windows.GUID{Data1: 0x32d186a7, Data2: 0x218f, Data3: 0x4c75, Data4: [8]byte{0x88, 0x76, 0xdd, 0x77, 0x27, 0x3a, 0x89, 0x99}}
	iidIMFTransform   = windows.GUID{Data1: 0xbf94c121, Data2: 0x5b05, Data3: 0x4e6f, Data4: [8]byte{0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d}}

	mfMTMajorType          = windows.GUID{Data1: 0x48eba18e, Data2: 0xf8c9, Data3: 0x4687, Data4: [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	mfMTSubtype            = windows.GUID{Data1: 0xf7e34c9a, Data2: 0x42e8, Data3: 0x4714, Data4: [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	mfMTUserData           = windows.GUID{Data1: 0xb6bc765f, Data2: 0x4c3b, Data3: 0x40a4, Data4: [8]byte{0xbd, 0x51, 0x25, 0x35, 0xb6, 0x6f, 0xe0, 0x9d}}
	mfMTAACPayloadType     = windows.GUID{Data1: 0xbfbabe79, Data2: 0x7434, Data3: 0x4d1c, Data4: [8]byte{0x94, 0xf0, 0x72, 0xa3, 0xb9, 0xe1, 0x71, 0x88}}
	mfMTAudioNumChannels   = windows.GUID{Data1: 0x37e48bf5, Data2: 0x645e, Data3: 0x4c5b, Data4: [8]byte{0x89, 0xde, 0xad, 0xa9, 0xe2, 0x9b, 0x69, 0x6a}}
	mfMTAudioSamplesPerSec = windows.GUID{Data1: 0x5faeeae7, Data2: 0x0290, Data3: 0x4c31, Data4: [8]byte{0x9e, 0x8a, 0xc5, 0x34, 0xf6, 0x8d, 0x9d, 0xba}}
	mfMTAudioBitsPerSample = windows.GUID{Data1: 0xf2deb57f, Data2: 0x40fa, Data3: 0x4764, Data4: [8]byte{0xaa, 0x33, 0xed, 0x4f, 0x2d, 0x1f, 0xf6, 0x69}}

	mfMediaTypeAudio   = windows.GUID{Data1: 0x73647561, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfAudioFormatPCM   = windows.GUID{Data1: 0x00000001, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfAudioFormatFloat = windows.GUID{Data1: 0x00000003, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfAudioFormatAAC   = windows.GUID{Data1: 0x00001610, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
)

const (
	mfErrNeedMoreInput = 0xC00D6D72
	mfErrStreamChange  = 0xC00D6D61

	mftMsgFlush          = 0x00000000
	mftMsgDrain          = 0x00000001
	mftMsgBeginStreaming = 0x10000000
	mftMsgStartOfStream  = 0x10000003

	mftOutProvidesSamples   = 0x100
	mftOutCanProvideSamples = 0x200
)

// Wide COM object vtable (covers IMFAttributes 0..32 and IMFSample 33..46).
type mfObject struct{ vtable *[48]uintptr }

//go:uintptrescapes
func (o *mfObject) call(slot int, args ...uintptr) uintptr {
	params := make([]uintptr, 0, len(args)+1)
	params = append(params, uintptr(unsafe.Pointer(o)))
	params = append(params, args...)
	r, _, _ := syscall.SyscallN(o.vtable[slot], params...)
	return r
}

func (o *mfObject) release() {
	if o != nil {
		o.call(2)
	}
}

type mftOutputStreamInfo struct {
	flags     uint32
	size      uint32
	alignment uint32
}

type mftOutputDataBuffer struct {
	streamID uint32
	_        uint32 // 64-bit alignment padding before pointer
	sample   *mfObject
	status   uint32
	_        uint32 // 64-bit alignment padding before pointer
	events   *mfObject
}

type aacDecoder struct {
	mft             *mfObject
	providesSamples bool
	outBufSize      uint32
	channels        uint16
	sampleRate      uint32
	bitsPerSample   uint16
	isFloat         bool
}

func openAACDecoder(sampleRate uint32, channels uint16, asc []byte) (_ *aacDecoder, err error) {
	for _, proc := range []*windows.LazyProc{startMF, stopMF, coCreateInstanceMF, mfCreateMediaType, mfCreateSample, mfCreateMemBuffer} {
		if err := proc.Find(); err != nil {
			return nil, errors.New("Windows Media Foundation AAC decoder is unavailable")
		}
	}
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, syscall.Errno(1)) {
		return nil, err
	}
	hr, _, _ := startMF.Call(0x20070, 1) // MFSTARTUP_NOSOCKET
	if err := checked("MFStartup", hr); err != nil {
		windows.CoUninitialize()
		return nil, err
	}
	dec := &aacDecoder{}
	defer func() {
		if err != nil {
			dec.close()
		}
	}()

	hr, _, _ = coCreateInstanceMF.Call(
		uintptr(unsafe.Pointer(&clsidCMSAACDecMFT)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidIMFTransform)),
		uintptr(unsafe.Pointer(&dec.mft)),
	)
	if err := checked("CoCreateInstance(CMSAACDecMFT)", hr); err != nil {
		return nil, err
	}

	var inType *mfObject
	hr, _, _ = mfCreateMediaType.Call(uintptr(unsafe.Pointer(&inType)))
	if err := checked("MFCreateMediaType", hr); err != nil {
		return nil, err
	}
	defer inType.release()

	// Configure raw AAC input type with 12-byte HEAACWAVEINFO prefix + MP4 AudioSpecificConfig.
	userData := make([]byte, 12+len(asc))
	binary.LittleEndian.PutUint16(userData[0:2], 0)    // wPayloadType = 0 (raw AAC)
	binary.LittleEndian.PutUint16(userData[2:4], 0xFE) // wAudioProfileLevelIndication
	copy(userData[12:], asc)

	if err := checked("SetGUID(MajorType)", inType.call(24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeAudio)))); err != nil {
		return nil, err
	}
	if err := checked("SetGUID(Subtype)", inType.call(24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfAudioFormatAAC)))); err != nil {
		return nil, err
	}
	if err := checked("SetUINT32(AACPayloadType)", inType.call(21, uintptr(unsafe.Pointer(&mfMTAACPayloadType)), 0)); err != nil {
		return nil, err
	}
	if err := checked("SetUINT32(Channels)", inType.call(21, uintptr(unsafe.Pointer(&mfMTAudioNumChannels)), uintptr(channels))); err != nil {
		return nil, err
	}
	if err := checked("SetUINT32(SampleRate)", inType.call(21, uintptr(unsafe.Pointer(&mfMTAudioSamplesPerSec)), uintptr(sampleRate))); err != nil {
		return nil, err
	}
	if err := checked("SetBlob(UserData)", inType.call(26, uintptr(unsafe.Pointer(&mfMTUserData)), uintptr(unsafe.Pointer(&userData[0])), uintptr(len(userData)))); err != nil {
		return nil, err
	}
	if err := checked("SetInputType", dec.mft.call(15, 0, uintptr(unsafe.Pointer(inType)), 0)); err != nil {
		return nil, err
	}

	if err := dec.selectOutputType(); err != nil {
		return nil, err
	}
	_ = dec.mft.call(23, mftMsgBeginStreaming, 0)
	_ = dec.mft.call(23, mftMsgStartOfStream, 0)
	return dec, nil
}

func (d *aacDecoder) selectOutputType() error {
	var bestPCM *mfObject
	var bestFloat *mfObject
	defer func() {
		bestPCM.release()
		bestFloat.release()
	}()

	for idx := uintptr(0); idx < 32; idx++ {
		var mt *mfObject
		hr := d.mft.call(14, 0, idx, uintptr(unsafe.Pointer(&mt))) // GetOutputAvailableType
		if int32(hr) < 0 || mt == nil {
			break
		}
		var sub windows.GUID
		if int32(mt.call(10, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&sub)))) < 0 {
			mt.release()
			continue
		}
		var bits uint32
		mt.call(7, uintptr(unsafe.Pointer(&mfMTAudioBitsPerSample)), uintptr(unsafe.Pointer(&bits)))
		if sub == mfAudioFormatPCM && bits == 16 && bestPCM == nil {
			bestPCM = mt
			continue
		}
		if sub == mfAudioFormatFloat && bits == 32 && bestFloat == nil {
			bestFloat = mt
			continue
		}
		mt.release()
	}

	chosen := bestPCM
	isFloat := false
	if chosen == nil {
		chosen = bestFloat
		isFloat = true
	}
	if chosen == nil {
		return errors.New("AAC decoder has no PCM/Float output type")
	}
	if err := checked("SetOutputType", d.mft.call(16, 0, uintptr(unsafe.Pointer(chosen)), 0)); err != nil {
		return err
	}

	var ch, rate, bits uint32
	chosen.call(7, uintptr(unsafe.Pointer(&mfMTAudioNumChannels)), uintptr(unsafe.Pointer(&ch)))
	chosen.call(7, uintptr(unsafe.Pointer(&mfMTAudioSamplesPerSec)), uintptr(unsafe.Pointer(&rate)))
	chosen.call(7, uintptr(unsafe.Pointer(&mfMTAudioBitsPerSample)), uintptr(unsafe.Pointer(&bits)))
	if ch == 0 || ch > 2 || rate == 0 {
		return errors.New("unsupported decoded AAC channel or sample rate")
	}
	d.channels = uint16(ch)
	d.sampleRate = rate
	d.bitsPerSample = uint16(bits)
	d.isFloat = isFloat

	var info mftOutputStreamInfo
	if int32(d.mft.call(7, 0, uintptr(unsafe.Pointer(&info)))) >= 0 {
		d.providesSamples = (info.flags & (mftOutProvidesSamples | mftOutCanProvideSamples)) != 0
		d.outBufSize = info.size
	}
	if d.outBufSize < 32768 {
		d.outBufSize = 32768
	}
	return nil
}

func (d *aacDecoder) flush() {
	if d.mft != nil {
		d.mft.call(23, mftMsgFlush, 0)
		d.mft.call(23, mftMsgStartOfStream, 0)
	}
}

func (d *aacDecoder) drain() ([]byte, error) {
	if d.mft == nil {
		return nil, nil
	}
	d.mft.call(23, mftMsgDrain, 0)
	return d.collectOutput()
}

func (d *aacDecoder) decodeFrame(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var sample *mfObject
	hr, _, _ := mfCreateSample.Call(uintptr(unsafe.Pointer(&sample)))
	if err := checked("MFCreateSample", hr); err != nil {
		return nil, err
	}
	defer sample.release()

	var buf *mfObject
	hr, _, _ = mfCreateMemBuffer.Call(uintptr(len(raw)), uintptr(unsafe.Pointer(&buf)))
	if err := checked("MFCreateMemoryBuffer", hr); err != nil {
		return nil, err
	}
	defer buf.release()

	var ptr *byte
	if err := checked("IMFMediaBuffer::Lock", buf.call(3, uintptr(unsafe.Pointer(&ptr)), 0, 0)); err != nil {
		return nil, err
	}
	dst := unsafe.Slice(ptr, len(raw))
	copy(dst, raw)
	buf.call(4) // Unlock
	if err := checked("IMFMediaBuffer::SetCurrentLength", buf.call(6, uintptr(len(raw)))); err != nil {
		return nil, err
	}
	if err := checked("IMFSample::AddBuffer", sample.call(42, uintptr(unsafe.Pointer(buf)))); err != nil {
		return nil, err
	}

	// ProcessInput(dwInputStreamID=0, pSample, dwFlags=0)
	if err := checked("ProcessInput", d.mft.call(24, 0, uintptr(unsafe.Pointer(sample)), 0)); err != nil {
		return nil, err
	}
	return d.collectOutput()
}

func (d *aacDecoder) collectOutput() ([]byte, error) {
	var out []byte
	for {
		var outBuf mftOutputDataBuffer
		var ownedSample *mfObject
		if !d.providesSamples {
			hr, _, _ := mfCreateSample.Call(uintptr(unsafe.Pointer(&ownedSample)))
			if err := checked("MFCreateSample(out)", hr); err != nil {
				return out, err
			}
			var mem *mfObject
			hr, _, _ = mfCreateMemBuffer.Call(uintptr(d.outBufSize), uintptr(unsafe.Pointer(&mem)))
			if int32(hr) < 0 {
				ownedSample.release()
				return out, checked("MFCreateMemoryBuffer(out)", hr)
			}
			ownedSample.call(42, uintptr(unsafe.Pointer(mem)))
			mem.release()
			outBuf.sample = ownedSample
		}

		var status uint32
		hr := d.mft.call(25, 0, 1, uintptr(unsafe.Pointer(&outBuf)), uintptr(unsafe.Pointer(&status))) // ProcessOutput
		if outBuf.events != nil {
			outBuf.events.release()
		}
		if uint32(hr) == mfErrNeedMoreInput {
			ownedSample.release()
			return out, nil
		}
		if uint32(hr) == mfErrStreamChange {
			ownedSample.release()
			if err := d.selectOutputType(); err != nil {
				return out, err
			}
			continue
		}
		if int32(hr) < 0 {
			ownedSample.release()
			return out, checked("ProcessOutput", hr)
		}

		resSample := outBuf.sample
		if resSample != nil {
			var contig *mfObject
			if int32(resSample.call(41, uintptr(unsafe.Pointer(&contig)))) >= 0 && contig != nil {
				var ptr *byte
				var curLen uint32
				if int32(contig.call(3, uintptr(unsafe.Pointer(&ptr)), 0, uintptr(unsafe.Pointer(&curLen)))) >= 0 && curLen > 0 {
					raw := unsafe.Slice(ptr, curLen)
					if d.isFloat {
						out = appendFloat32AsPCM16(out, raw)
					} else {
						out = append(out, raw...)
					}
				}
				contig.call(4) // Unlock
				contig.release()
			}
			if d.providesSamples {
				resSample.release()
			}
		}
		ownedSample.release()
	}
}

func appendFloat32AsPCM16(dst, src []byte) []byte {
	n := len(src) / 4
	start := len(dst)
	dst = append(dst, make([]byte, n*2)...)
	for i := range n {
		f := math.Float32frombits(binary.LittleEndian.Uint32(src[i*4:]))
		if f > 1 {
			f = 1
		} else if f < -1 {
			f = -1
		}
		v := int16(f * 32767)
		binary.LittleEndian.PutUint16(dst[start+i*2:], uint16(v))
	}
	return dst
}

func (d *aacDecoder) pcmFormat() pcmFormat {
	align := d.channels * 2
	return pcmFormat{
		tag:            1, // WAVE_FORMAT_PCM
		channels:       d.channels,
		rate:           d.sampleRate,
		bytesPerSecond: d.sampleRate * uint32(align),
		align:          align,
		bits:           16,
	}
}

func (d *aacDecoder) close() {
	if d.mft != nil {
		d.mft.release()
		d.mft = nil
	}
	stopMF.Call()
	windows.CoUninitialize()
}

// --- Progressive fragmented MP4 (ISO-BMFF / DASH AAC) parser & cache ---

type mp4InitInfo struct {
	timescale    uint32
	defaultTicks uint32
	defaultSize  uint32
	channels     uint16
	sampleRate   uint32
	asc          []byte
	sidxDurMS    int64
}

type mp4Frame struct {
	cacheOffset int64
	size        uint32
	startMS     int64
	endMS       int64
}

type mp4Fragment struct {
	startMS int64
	endMS   int64
	frames  []mp4Frame
}

type boxHeader struct {
	name    string
	header  int64
	length  int64
	rawHead []byte
}

func readBoxHeader(r io.Reader) (boxHeader, error) {
	var h [16]byte
	if _, err := io.ReadFull(r, h[:8]); err != nil {
		return boxHeader{}, err
	}
	length := int64(binary.BigEndian.Uint32(h[:4]))
	header := int64(8)
	if length == 1 {
		if _, err := io.ReadFull(r, h[8:16]); err != nil {
			return boxHeader{}, err
		}
		length = int64(binary.BigEndian.Uint64(h[8:16]))
		header = 16
	}
	if length < header && length != 0 {
		return boxHeader{}, errors.New("invalid MP4 box length")
	}
	raw := make([]byte, header)
	copy(raw, h[:header])
	return boxHeader{name: string(h[4:8]), header: header, length: length, rawHead: raw}, nil
}

// parseMP4Init validates the moov box with the exact same security boundaries as
// checkMP4 (single soun track, local dref, embedded mp4a/esds with no external URL,
// no forbidden boxes) and returns the AAC init metadata if this is a fragmented MP4.
func parseMP4Init(moovBody []byte) (mp4InitInfo, bool, error) {
	var info mp4InitInfo
	var hasMvex, hasAudio, localRef bool
	tracks := 0
	boxes := 0
	parents := map[string]string{
		"trak": "moov", "mdia": "trak", "minf": "mdia", "dinf": "minf", "stbl": "minf", "mvex": "moov",
	}

	var walk func(data []byte, parent string) error
	walk = func(data []byte, parent string) error {
		for off := 0; off < len(data); {
			if len(data)-off < 8 {
				return errors.New("invalid MP4 box")
			}
			length := int64(binary.BigEndian.Uint32(data[off : off+4]))
			hdr := int64(8)
			if length == 1 {
				if len(data)-off < 16 {
					return errors.New("invalid extended MP4 box")
				}
				length = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
				hdr = 16
			} else if length == 0 {
				length = int64(len(data) - off)
			}
			if length < hdr || length > int64(len(data)-off) {
				return errors.New("invalid MP4 box length")
			}
			boxes++
			if boxes > 4096 {
				return errors.New("excessive MP4 init boxes")
			}
			name := string(data[off+4 : off+8])
			body := data[off+int(hdr) : off+int(length)]

			switch name {
			case "trak", "mdia", "minf", "dinf", "stbl", "mvex":
				if parents[name] != parent {
					return errors.New("invalid MP4 hierarchy")
				}
				if name == "trak" {
					tracks++
					if tracks > 1 {
						return errors.New("expected one audio track")
					}
				}
				if name == "mvex" {
					hasMvex = true
				}
				if err := walk(body, name); err != nil {
					return err
				}
			case "mdhd":
				if parent == "mdia" {
					if len(body) < 16 {
						return errors.New("truncated MP4 media header")
					}
					at := 12
					if body[0] == 1 {
						at = 20
					} else if body[0] != 0 {
						return errors.New("unsupported mdhd version")
					}
					if len(body) < at+4 {
						return errors.New("truncated MP4 media header")
					}
					info.timescale = binary.BigEndian.Uint32(body[at : at+4])
				}
			case "trex":
				if parent == "mvex" && len(body) >= 20 {
					info.defaultTicks = binary.BigEndian.Uint32(body[12:16])
					info.defaultSize = binary.BigEndian.Uint32(body[16:20])
				}
			case "dref":
				if parent != "dinf" || len(body) != 20 ||
					binary.BigEndian.Uint32(body[4:8]) != 1 ||
					binary.BigEndian.Uint32(body[8:12]) != 12 ||
					string(body[12:16]) != "url " ||
					binary.BigEndian.Uint32(body[16:20]) != 1 {
					return errors.New("external MP4 media references are unsupported")
				}
				localRef = true
			case "hdlr":
				if parent == "mdia" {
					if len(body) < 12 || string(body[8:12]) != "soun" {
						return errors.New("expected an audio-only MP4")
					}
				}
			case "stsd":
				if parent != "stbl" || len(body) < 36 {
					return errors.New("invalid MP4 sample description")
				}
				if binary.BigEndian.Uint32(body[4:8]) != 1 || int(binary.BigEndian.Uint32(body[8:12])) != len(body)-8 || string(body[12:16]) != "mp4a" {
					return errors.New("only unencrypted AAC MP4 tracks are supported")
				}
				ch, rate, asc, err := parseMP4AEntry(body[8:])
				if err != nil {
					return err
				}
				info.channels = ch
				info.sampleRate = rate
				info.asc = asc
				hasAudio = true
			case "rmra", "rmda", "rdrf", "pssh":
				return fmt.Errorf("unsupported MP4 box %q", name)
			}
			off += int(length)
		}
		return nil
	}

	if err := walk(moovBody, "moov"); err != nil {
		return info, false, err
	}
	if !hasAudio || !localRef || info.timescale == 0 || len(info.asc) == 0 {
		return info, false, nil
	}
	return info, hasMvex, nil
}

func parseMP4AEntry(entry []byte) (uint16, uint32, []byte, error) {
	if len(entry) < 36 {
		return 0, 0, nil, errors.New("invalid AAC sample entry")
	}
	if binary.BigEndian.Uint16(entry[14:16]) != 1 || binary.BigEndian.Uint16(entry[16:18]) != 0 {
		return 0, 0, nil, errors.New("unsupported AAC sample reference")
	}
	channels := binary.BigEndian.Uint16(entry[24:26])
	sampleRate := binary.BigEndian.Uint32(entry[32:36]) >> 16
	if channels == 0 || channels > 2 || sampleRate < 8000 || sampleRate > 192000 {
		return 0, 0, nil, errors.New("unsupported AAC audio format")
	}

	var asc []byte
	for at := 36; at < len(entry); {
		if len(entry)-at < 8 {
			return 0, 0, nil, errors.New("invalid AAC sample configuration")
		}
		sz := int(binary.BigEndian.Uint32(entry[at : at+4]))
		if sz < 8 || sz > len(entry)-at {
			return 0, 0, nil, errors.New("invalid AAC configuration size")
		}
		switch string(entry[at+4 : at+8]) {
		case "btrt", "chan", "srat":
		case "esds":
			var err error
			asc, err = parseESDSConfig(entry[at+8 : at+sz])
			if err != nil {
				return 0, 0, nil, err
			}
		default:
			return 0, 0, nil, errors.New("unsupported AAC configuration")
		}
		at += sz
	}
	if len(asc) == 0 {
		return 0, 0, nil, errors.New("missing AAC configuration")
	}
	return channels, sampleRate, asc, nil
}

func readDescLen(b []byte, pos int) (int, int, bool) {
	length := 0
	for range 4 {
		if pos >= len(b) {
			return 0, pos, false
		}
		v := b[pos]
		pos++
		length = (length << 7) | int(v&0x7f)
		if v&0x80 == 0 {
			return length, pos, true
		}
	}
	return 0, pos, false
}

func parseESDSConfig(body []byte) ([]byte, error) {
	if len(body) < 12 || body[4] != 3 {
		return nil, errors.New("missing AAC elementary stream descriptor")
	}
	pos := 5
	esLen, next, ok := readDescLen(body, pos)
	if !ok || next+3 > len(body) || next+esLen > len(body) {
		return nil, errors.New("invalid AAC elementary stream descriptor")
	}
	esEnd := next + esLen
	pos = next
	flags := body[pos+2]
	if flags&0x40 != 0 {
		return nil, errors.New("external AAC elementary stream references are unsupported")
	}
	pos += 3
	if flags&0x80 != 0 {
		pos += 2
	}
	if flags&0x20 != 0 {
		pos += 2
	}
	for pos < esEnd {
		tag := body[pos]
		pos++
		dLen, dStart, ok := readDescLen(body, pos)
		if !ok || dStart+dLen > esEnd {
			return nil, errors.New("truncated AAC descriptor")
		}
		if tag == 4 { // DecoderConfigDescriptor
			if dLen < 13 {
				return nil, errors.New("truncated AAC decoder config")
			}
			subPos := dStart + 13
			subEnd := dStart + dLen
			for subPos < subEnd {
				subTag := body[subPos]
				subPos++
				sLen, sStart, ok := readDescLen(body, subPos)
				if !ok || sStart+sLen > subEnd {
					return nil, errors.New("truncated AAC specific info")
				}
				if subTag == 5 && sLen > 0 {
					out := make([]byte, sLen)
					copy(out, body[sStart:sStart+sLen])
					return out, nil
				}
				subPos = sStart + sLen
			}
		}
		pos = dStart + dLen
	}
	return nil, errors.New("missing AAC AudioSpecificConfig")
}

func parseSIDXDurationMS(body []byte) int64 {
	if len(body) < 12 {
		return 0
	}
	ver := body[0]
	scale := binary.BigEndian.Uint32(body[8:12])
	if scale == 0 {
		return 0
	}
	off := 12
	if ver == 0 {
		off += 8 // earliest_presentation_time(4) + first_offset(4)
	} else if ver == 1 {
		off += 16 // earliest_presentation_time(8) + first_offset(8)
	} else {
		return 0
	}
	if off+4 > len(body) {
		return 0
	}
	refCount := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
	off += 4
	var totalTicks uint64
	for range refCount {
		if off+12 > len(body) {
			break
		}
		dur := binary.BigEndian.Uint32(body[off+4 : off+8])
		totalTicks += uint64(dur)
		off += 12
	}
	if totalTicks == 0 {
		return 0
	}
	return int64(totalTicks * 1000 / uint64(scale))
}

type parsedSample struct {
	relOffset int64
	size      uint32
	startMS   int64
	endMS     int64
}

func parseMoofSamples(moofBody []byte, moofLen int64, mdatHeaderLen int64, mdatPayloadLen int64, info mp4InitInfo, nextTick *uint64) ([]parsedSample, error) {
	trafDur := info.defaultTicks
	trafSize := info.defaultSize
	var baseDataOffset int64
	hasBaseDataOffset := false
	curTick := *nextTick
	var samples []parsedSample
	var trafCount int

	for off := 0; off < len(moofBody); {
		if len(moofBody)-off < 8 {
			return nil, errors.New("invalid moof box")
		}
		sz := int(binary.BigEndian.Uint32(moofBody[off : off+4]))
		if sz < 8 || sz > len(moofBody)-off {
			return nil, errors.New("invalid moof child length")
		}
		name := string(moofBody[off+4 : off+8])
		body := moofBody[off+8 : off+sz]
		if name == "traf" {
			trafCount++
			if trafCount > 1 {
				return nil, errors.New("multiple traf boxes unsupported")
			}
			for tOff := 0; tOff < len(body); {
				if len(body)-tOff < 8 {
					return nil, errors.New("invalid traf box")
				}
				tSz := int(binary.BigEndian.Uint32(body[tOff : tOff+4]))
				if tSz < 8 || tSz > len(body)-tOff {
					return nil, errors.New("invalid traf child length")
				}
				tName := string(body[tOff+4 : tOff+8])
				tBody := body[tOff+8 : tOff+tSz]
				switch tName {
				case "tfhd":
					if len(tBody) < 8 {
						return nil, errors.New("truncated tfhd")
					}
					flags := binary.BigEndian.Uint32(tBody[:4]) & 0xffffff
					p := 8
					if flags&0x01 != 0 {
						if p+8 > len(tBody) {
							return nil, errors.New("truncated tfhd base offset")
						}
						baseDataOffset = int64(binary.BigEndian.Uint64(tBody[p : p+8]))
						hasBaseDataOffset = true
						p += 8
					}
					if flags&0x02 != 0 {
						p += 4
					}
					if flags&0x08 != 0 {
						if p+4 > len(tBody) {
							return nil, errors.New("truncated tfhd duration")
						}
						trafDur = binary.BigEndian.Uint32(tBody[p : p+4])
						p += 4
					}
					if flags&0x10 != 0 {
						if p+4 > len(tBody) {
							return nil, errors.New("truncated tfhd size")
						}
						trafSize = binary.BigEndian.Uint32(tBody[p : p+4])
						p += 4
					}
				case "tfdt":
					if len(tBody) < 8 {
						return nil, errors.New("truncated tfdt")
					}
					if tBody[0] == 0 {
						curTick = uint64(binary.BigEndian.Uint32(tBody[4:8]))
					} else if tBody[0] == 1 && len(tBody) >= 12 {
						curTick = binary.BigEndian.Uint64(tBody[4:12])
					} else {
						return nil, errors.New("invalid tfdt")
					}
				case "trun":
					if len(tBody) < 8 {
						return nil, errors.New("truncated trun")
					}
					flags := binary.BigEndian.Uint32(tBody[:4]) & 0xffffff
					count := int(binary.BigEndian.Uint32(tBody[4:8]))
					p := 8
					var relOff int64
					if flags&0x01 != 0 {
						if p+4 > len(tBody) {
							return nil, errors.New("truncated trun data_offset")
						}
						dataOff := int64(int32(binary.BigEndian.Uint32(tBody[p : p+4])))
						p += 4
						if hasBaseDataOffset {
							// Absolute base offset is rare in DASH; fall back to mdat start if it doesn't map cleanly.
							_ = baseDataOffset
						}
						relOff = dataOff - moofLen - mdatHeaderLen
						if relOff < 0 {
							relOff = 0
						}
					}
					if flags&0x04 != 0 {
						p += 4
					}
					for range count {
						sDur := trafDur
						sSize := trafSize
						if flags&0x100 != 0 {
							if p+4 > len(tBody) {
								return nil, errors.New("truncated trun duration")
							}
							sDur = binary.BigEndian.Uint32(tBody[p : p+4])
							p += 4
						}
						if flags&0x200 != 0 {
							if p+4 > len(tBody) {
								return nil, errors.New("truncated trun size")
							}
							sSize = binary.BigEndian.Uint32(tBody[p : p+4])
							p += 4
						}
						if flags&0x400 != 0 {
							p += 4
						}
						if flags&0x800 != 0 {
							p += 4
						}
						if p > len(tBody) {
							return nil, errors.New("truncated trun entries")
						}
						if sSize == 0 || relOff < 0 || relOff+int64(sSize) > mdatPayloadLen {
							return nil, errors.New("MP4 sample out of mdat bounds")
						}
						startMS := int64(curTick * 1000 / uint64(info.timescale))
						curTick += uint64(sDur)
						endMS := int64(curTick * 1000 / uint64(info.timescale))
						samples = append(samples, parsedSample{
							relOffset: relOff,
							size:      sSize,
							startMS:   startMS,
							endMS:     endMS,
						})
						relOff += int64(sSize)
					}
				}
				tOff += tSz
			}
		}
		off += sz
	}
	*nextTick = curTick
	return samples, nil
}

type mp4StreamBuffer struct {
	mu      sync.Mutex
	file    *os.File
	frags   []mp4Fragment
	totalMS int64
	done    bool
	err     error
	notify  chan struct{}
}

func (b *mp4StreamBuffer) signal() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

func (b *mp4StreamBuffer) snapshot() (fragCount int, totalMS int64, done bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.frags), b.totalMS, b.done, b.err
}

func (b *mp4StreamBuffer) fragment(idx int) (mp4Fragment, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if idx < 0 || idx >= len(b.frags) {
		return mp4Fragment{}, false
	}
	return b.frags[idx], true
}

func (b *mp4StreamBuffer) findSeekTarget(posMS int64) (fragIdx, frameIdx int, found bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, fr := range b.frags {
		if posMS < fr.endMS || (i == len(b.frags)-1 && b.done) {
			for j, frm := range fr.frames {
				if posMS < frm.endMS || j == len(fr.frames)-1 {
					return i, j, true
				}
			}
			return i, 0, true
		}
	}
	return 0, 0, false
}

// tryStreamFragmentedMP4 inspects the leading MP4 boxes from the live HTTP stream.
// If this is a self-contained fragmented AAC MP4 (e.g. Bilibili DASH .m4s / .m4a),
// it begins decoding and playing immediately on the first moof+mdat fragment while
// streaming the remaining fragments in the background.
// Otherwise it returns (false, combinedReader, nil) so the caller falls back cleanly
// without re-reading from the network.
func (s *session) tryStreamFragmentedMP4(download context.Context, reader *bufio.Reader, closeInput func(), notify func(Event)) (bool, *bufio.Reader, error) {
	prefix, err := reader.Peek(12)
	if err != nil || string(prefix[4:8]) != "ftyp" {
		return false, reader, nil
	}

	var lead bytes.Buffer
	var initInfo mp4InitInfo
	var hasMoov, isFrag bool
	var firstMoofHead boxHeader
	var haveFirstMoof bool
	var totalBytes int64

	const maxInitBytes = 2 << 20
	for {
		bh, err := readBoxHeader(reader)
		if err != nil {
			if lead.Len() == 0 {
				return false, reader, nil
			}
			return false, bufio.NewReader(io.MultiReader(bytes.NewReader(lead.Bytes()), reader)), nil
		}
		if bh.name == "moof" && hasMoov && isFrag {
			firstMoofHead = bh
			haveFirstMoof = true
			break
		}
		lead.Write(bh.rawHead)
		totalBytes += bh.header

		if bh.name == "mdat" || bh.length == 0 || bh.length-bh.header > maxInitBytes || totalBytes+(bh.length-bh.header) > maxInitBytes {
			// Non-fragmented MP4 (mdat before moov) or oversized init box: fall back to cacheStream.
			return false, bufio.NewReader(io.MultiReader(bytes.NewReader(lead.Bytes()), reader)), nil
		}
		bodyLen := bh.length - bh.header
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(reader, body); err != nil {
			return false, bufio.NewReader(io.MultiReader(bytes.NewReader(lead.Bytes()), reader)), nil
		}
		lead.Write(body)
		totalBytes += bodyLen

		switch bh.name {
		case "rmra", "rmda", "rdrf", "pssh":
			return true, nil, fmt.Errorf("unsupported MP4 box %q", bh.name)
		case "moov":
			info, frag, parseErr := parseMP4Init(body)
			if parseErr != nil {
				return true, nil, parseErr
			}
			if !frag {
				return false, bufio.NewReader(io.MultiReader(bytes.NewReader(lead.Bytes()), reader)), nil
			}
			initInfo = info
			hasMoov = true
			isFrag = true
		case "sidx":
			if dur := parseSIDXDurationMS(body); dur > initInfo.sidxDurMS {
				initInfo.sidxDurMS = dur
			}
		}
	}

	if !haveFirstMoof {
		return false, bufio.NewReader(io.MultiReader(bytes.NewReader(lead.Bytes()), reader)), nil
	}

	cacheFile, err := os.CreateTemp("", "quailcrate-aac-*")
	if err != nil {
		return true, nil, errors.New("could not create streaming audio cache")
	}
	defer os.Remove(cacheFile.Name())
	defer cacheFile.Close()

	streamBuf := &mp4StreamBuffer{
		file:    cacheFile,
		totalMS: initInfo.sidxDurMS,
		notify:  make(chan struct{}, 1),
	}

	// Background goroutine streams remaining moof+mdat fragments into cacheFile while playback runs.
	dlDone := make(chan struct{})
	go func() {
		defer close(dlDone)
		defer closeInput()
		var cacheOff int64
		var nextTick uint64
		var pendingMoof []byte
		var pendingMoofLen int64
		bytesRead := totalBytes

		processBox := func(bh boxHeader) error {
			if bh.length == 0 || bh.length < bh.header || bytesRead+bh.length > maxMediaBytes {
				return errors.New("MP4 stream exceeds limits or invalid box")
			}
			bodyLen := bh.length - bh.header
			bytesRead += bh.length
			switch bh.name {
			case "rmra", "rmda", "rdrf", "pssh":
				return fmt.Errorf("unsupported MP4 box %q", bh.name)
			case "moof":
				if bodyLen > 4<<20 {
					return errors.New("MP4 moof box too large")
				}
				pendingMoof = make([]byte, bodyLen)
				if _, err := io.ReadFull(reader, pendingMoof); err != nil {
					return errors.New("audio stream interrupted")
				}
				pendingMoofLen = bh.length
			case "mdat":
				if len(pendingMoof) == 0 {
					if _, err := io.CopyN(io.Discard, reader, bodyLen); err != nil {
						return errors.New("audio stream interrupted")
					}
					return nil
				}
				samples, err := parseMoofSamples(pendingMoof, pendingMoofLen, bh.header, bodyLen, initInfo, &nextTick)
				pendingMoof = nil
				pendingMoofLen = 0
				if err != nil {
					return err
				}
				if _, err := io.CopyN(cacheFile, reader, bodyLen); err != nil {
					return errors.New("audio stream interrupted")
				}
				baseOff := cacheOff
				cacheOff += bodyLen
				if len(samples) > 0 {
					frames := make([]mp4Frame, len(samples))
					for i, sm := range samples {
						frames[i] = mp4Frame{
							cacheOffset: baseOff + sm.relOffset,
							size:        sm.size,
							startMS:     sm.startMS,
							endMS:       sm.endMS,
						}
					}
					frag := mp4Fragment{
						startMS: frames[0].startMS,
						endMS:   frames[len(frames)-1].endMS,
						frames:  frames,
					}
					streamBuf.mu.Lock()
					streamBuf.frags = append(streamBuf.frags, frag)
					if frag.endMS > streamBuf.totalMS {
						streamBuf.totalMS = frag.endMS
					}
					streamBuf.mu.Unlock()
					streamBuf.signal()
				}
			default:
				if _, err := io.CopyN(io.Discard, reader, bodyLen); err != nil {
					return errors.New("audio stream interrupted")
				}
			}
			return nil
		}

		err := processBox(firstMoofHead)
		for err == nil {
			if download.Err() != nil {
				err = download.Err()
				break
			}
			bh, hdrErr := readBoxHeader(reader)
			if errors.Is(hdrErr, io.EOF) {
				break
			}
			if hdrErr != nil {
				err = errors.New("audio stream interrupted")
				break
			}
			err = processBox(bh)
		}
		streamBuf.mu.Lock()
		streamBuf.done = true
		if err == nil && len(streamBuf.frags) == 0 {
			err = errors.New("audio samples are missing from MP4")
		}
		streamBuf.err = err
		streamBuf.mu.Unlock()
		streamBuf.signal()
	}()
	defer func() { closeInput(); <-dlDone }()

	return true, nil, s.playFragmentedAAC(initInfo, streamBuf, notify)
}

func (s *session) playFragmentedAAC(initInfo mp4InitInfo, streamBuf *mp4StreamBuffer, notify func(Event)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dec, err := openAACDecoder(initInfo.sampleRate, initInfo.channels, initInfo.asc)
	if err != nil {
		return err
	}
	defer dec.close()

	var dev waveDevice
	devOpen := false
	defer func() {
		if devOpen {
			waveReset.Call(dev.handle)
			dev.reap()
			waveClose.Call(dev.handle)
		}
	}()

	ensureWaveOpen := func() error {
		if devOpen {
			return nil
		}
		dev.f = dec.pcmFormat()
		if err := waveCall(waveOpen, uintptr(unsafe.Pointer(&dev.handle)), 0xffffffff, uintptr(unsafe.Pointer(&dev.f)), 0, 0, 0); err != nil {
			return err
		}
		devOpen = true
		return nil
	}

	tick := time.NewTicker(12 * time.Millisecond)
	defer tick.Stop()

	fragIdx, frameIdx := 0, 0
	var pcmQueue []byte
	var rawBuf []byte
	drained := false
	pendingSeek := int64(-1)
	mode, volume := "loading", 1.0

	currentDuration := func() int64 {
		_, totalMS, _, _ := streamBuf.snapshot()
		return max(totalMS, initInfo.sidxDurMS)
	}

	applySeek := func(targetMS int64) error {
		fi, fri, ok := streamBuf.findSeekTarget(targetMS)
		if !ok {
			return nil
		}
		pendingSeek = -1
		pcmQueue = pcmQueue[:0]
		drained = false
		dec.flush()
		if devOpen {
			if err := waveCall(waveReset, dev.handle); err != nil {
				return err
			}
			if err := dev.reap(); err != nil {
				return err
			}
			if mode == "paused" {
				if err := waveCall(wavePause, dev.handle); err != nil {
					return err
				}
			}
		}
		fragIdx, frameIdx = fi, fri
		dev.base = targetMS
		if frag, ok := streamBuf.fragment(fi); ok && fri < len(frag.frames) {
			dev.base = frag.frames[fri].startMS
		}
		return nil
	}

	for {
		if devOpen {
			if err := dev.reap(); err != nil {
				return err
			}
		}

		fragCount, totalMS, dlDone, dlErr := streamBuf.snapshot()
		if dlDone && dlErr != nil && fragIdx >= fragCount && len(pcmQueue) == 0 {
			return dlErr
		}
		if pendingSeek >= 0 {
			if _, _, ok := streamBuf.findSeekTarget(pendingSeek); ok {
				if err := applySeek(pendingSeek); err != nil {
					return err
				}
				s.update(mode, dev.base, currentDuration(), true)
				s.emit(notify, "audio.stateChanged", "")
			} else if dlDone {
				pendingSeek = -1
			}
		}

		// Decode frames just-in-time to keep ~4 waveOut buffers (~130ms) filled.
		for mode != "paused" && pendingSeek < 0 && (!devOpen || len(dev.chunks) < 4) {
			blockBytes := 4096
			if devOpen {
				blockBytes = max(int(dev.f.align), int(dev.f.rate/30)*int(dev.f.align))
			}

			for len(pcmQueue) < blockBytes {
				frag, ok := streamBuf.fragment(fragIdx)
				if !ok {
					if dlDone && !drained {
						drained = true
						tail, dErr := dec.drain()
						if dErr == nil && len(tail) > 0 {
							pcmQueue = append(pcmQueue, tail...)
						}
					}
					break
				}
				if frameIdx >= len(frag.frames) {
					fragIdx++
					frameIdx = 0
					continue
				}
				frm := frag.frames[frameIdx]
				frameIdx++
				if cap(rawBuf) < int(frm.size) {
					rawBuf = make([]byte, frm.size)
				}
				frameBytes := rawBuf[:frm.size]
				if _, err := streamBuf.file.ReadAt(frameBytes, frm.cacheOffset); err != nil {
					return errors.New("could not read cached AAC frame")
				}
				decoded, err := dec.decodeFrame(frameBytes)
				if err != nil {
					return err
				}
				if len(decoded) > 0 {
					pcmQueue = append(pcmQueue, decoded...)
				}
			}

			if len(pcmQueue) == 0 {
				break
			}
			if err := ensureWaveOpen(); err != nil {
				return err
			}
			blockBytes = max(int(dev.f.align), int(dev.f.rate/30)*int(dev.f.align))
			take := min(len(pcmQueue), blockBytes)
			take -= take % int(dev.f.align)
			if take <= 0 {
				if !dlDone || fragIdx < fragCount {
					break
				}
				pcmQueue = pcmQueue[:0]
				break
			}
			chunk := make([]byte, take)
			copy(chunk, pcmQueue[:take])
			pcmQueue = pcmQueue[take:]
			if err := dev.submit(chunk, volume); err != nil {
				return err
			}
			if mode == "loading" {
				mode = "playing"
				dur := max(totalMS, initInfo.sidxDurMS)
				s.update(mode, 0, dur, dur > 0)
				s.emit(notify, "audio.stateChanged", "")
			}
		}

		if mode != "paused" && pendingSeek < 0 && dlDone && fragIdx >= fragCount && len(pcmQueue) == 0 && (!devOpen || len(dev.chunks) == 0) {
			if dlErr != nil {
				return dlErr
			}
			dur := currentDuration()
			s.update("ended", dur, dur, false)
			s.emit(notify, "audio.ended", "")
			return nil
		}

		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-streamBuf.notify:
		case <-tick.C:
		case cmd := <-s.commands:
			var cmdErr error
			dur := currentDuration()
			switch cmd.action {
			case "audio.pause":
				if devOpen {
					cmdErr = waveCall(wavePause, dev.handle)
				}
				if cmdErr == nil {
					mode = "paused"
				}
			case "audio.resume":
				if devOpen {
					cmdErr = waveCall(waveRestart, dev.handle)
				}
				if cmdErr == nil {
					mode = "playing"
				}
			case "audio.seek":
				target := min(max(int64(0), cmd.position), dur)
				if _, _, ok := streamBuf.findSeekTarget(target); ok {
					cmdErr = applySeek(target)
				} else {
					pendingSeek = target
					if devOpen {
						_ = waveCall(waveReset, dev.handle)
						_ = dev.reap()
					}
					dev.base = target
				}
			case "audio.setVolume":
				volume = cmd.volume
			}
			pos := dev.base
			if devOpen && pendingSeek < 0 {
				pos = dev.positionMS()
			}
			if dur > 0 && pos > dur {
				pos = dur
			}
			s.mu.Lock()
			s.state.PositionMS = pos
			s.state.DurationMS = dur
			s.state.Seekable = dur > 0
			s.state.Status = mode
			s.state.Volume = volume
			s.mu.Unlock()
			if cmd.action != "audio.getState" && cmdErr == nil {
				s.emit(notify, "audio.stateChanged", "")
			}
			cmd.result <- cmdErr
		}
	}
}
