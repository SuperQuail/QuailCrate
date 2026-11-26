package pluginaudio

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/bits"
)

// A DASH file can have an empty initial movie header. Work out the duration
// from each fragment's decode time and sample durations; MFPlay can seek and
// decode these files even when GetDuration reports zero.
type fragmentTiming struct {
	scale        uint32
	defaultTicks uint32
	trafTicks    uint32
	nextTick     uint64
	endTick      uint64
}

func (t *fragmentTiming) durationMS() int64 {
	if t.scale == 0 || t.endTick > math.MaxInt64/1000 {
		return 0
	}
	ms := int64(t.endTick * 1000 / uint64(t.scale))
	if ms > math.MaxInt64/10000 {
		return 0
	}
	return ms
}

func (t *fragmentTiming) read(r io.ReaderAt, kind string, off, size int64) error {
	if kind == "traf" {
		t.trafTicks = t.defaultTicks
		t.nextTick = t.endTick
		return nil
	}
	switch kind {
	case "mdhd", "trex", "tfhd", "tfdt", "trun":
	default:
		return nil
	}
	var head [32]byte
	if size < 8 {
		return errors.New("truncated MP4 timing box")
	}
	headSize := min(int64(len(head)), size)
	if _, err := r.ReadAt(head[:headSize], off); err != nil {
		return err
	}
	version := head[0]
	if version > 1 {
		return errors.New("unsupported MP4 timing box version")
	}
	flags := binary.BigEndian.Uint32(head[:4]) & 0xffffff
	switch kind {
	case "mdhd":
		at := 12
		if version == 1 {
			at = 20
		}
		if size < int64(at+4) {
			return errors.New("truncated MP4 media header")
		}
		t.scale = binary.BigEndian.Uint32(head[at:])
	case "trex":
		if size < 16 {
			return errors.New("truncated MP4 track defaults")
		}
		t.defaultTicks = binary.BigEndian.Uint32(head[12:])
	case "tfhd":
		if flags&8 != 0 {
			at := 8
			if flags&1 != 0 {
				at += 8
			}
			if flags&2 != 0 {
				at += 4
			}
			if size < int64(at+4) {
				return errors.New("truncated MP4 fragment header")
			}
			t.trafTicks = binary.BigEndian.Uint32(head[at:])
		}
	case "tfdt":
		if version == 0 {
			t.nextTick = uint64(binary.BigEndian.Uint32(head[4:]))
			return nil
		}
		if size < 12 {
			return errors.New("truncated MP4 decode time")
		}
		t.nextTick = binary.BigEndian.Uint64(head[4:])
	case "trun":
		count := int64(binary.BigEndian.Uint32(head[4:]))
		at := int64(8 + 4*bits.OnesCount32(flags&5))
		stride := int64(4 * bits.OnesCount32(flags&0xf00))
		if at+count*stride > size {
			return errors.New("truncated MP4 sample table")
		}
		if flags&0x100 == 0 {
			ticks := uint64(count) * uint64(t.trafTicks)
			if math.MaxUint64-t.nextTick < ticks {
				return errors.New("MP4 duration overflow")
			}
			t.nextTick += ticks
		} else {
			// Only duration varies. Read bounded blocks to avoid a syscall per
			// AAC packet or an allocation proportional to the entire file.
			var block [4096]byte
			for count > 0 {
				n := min(count, int64(len(block))/stride)
				if _, err := r.ReadAt(block[:n*stride], off+at); err != nil {
					return err
				}
				for i := int64(0); i < n*stride; i += stride {
					ticks := uint64(binary.BigEndian.Uint32(block[i:]))
					if math.MaxUint64-t.nextTick < ticks {
						return errors.New("MP4 duration overflow")
					}
					t.nextTick += ticks
				}
				count -= n
				at += n * stride
			}
		}
		t.endTick = max(t.endTick, t.nextTick)
	}
	return nil
}
