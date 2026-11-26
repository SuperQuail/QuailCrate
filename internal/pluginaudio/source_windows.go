package pluginaudio

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const maxMediaBytes = 128 << 20

func authorizedInput(ctx context.Context, open func(context.Context) (io.ReadCloser, error)) (*bufio.Reader, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	body, err := open(ctx)
	if err != nil {
		return nil, nil, errors.New("could not open authorized audio stream")
	}
	if body == nil {
		return nil, nil, errors.New("empty audio stream")
	}
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		body.Close()
		close(closed)
	})
	finish := sync.OnceFunc(func() {
		if stopClose() {
			body.Close()
		} else {
			<-closed
		}
	})
	return bufio.NewReader(body), finish, nil
}

// Hand Media Foundation a seekable byte stream containing a single local
// asset. Playlist documents and containers with external media references
// cannot use the decoder as an independent network client.
func cacheStream(ctx context.Context, reader *bufio.Reader, duration *int64) (string, error) {
	prefix, err := reader.Peek(12)
	if err != nil {
		return "", errors.New("incomplete audio stream")
	}
	ext := mediaExtension([12]byte(prefix))
	if ext == "" {
		return "", errors.New("unsupported audio format (expected WAV, MP3 or AAC/MP4)")
	}
	f, err := os.CreateTemp("", "quailcrate-audio-*"+ext)
	if err != nil {
		return "", errors.New("could not create audio cache")
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(f.Name())
		}
	}()
	size, err := io.Copy(f, io.LimitReader(reader, maxMediaBytes+1))
	if err != nil || ctx.Err() != nil {
		return "", errors.New("audio stream interrupted")
	}
	if size > maxMediaBytes {
		return "", errors.New("audio stream exceeds size limit")
	}
	if err := validateMedia(f, size, duration); err != nil {
		return "", err
	}
	keep = true
	return f.Name(), nil
}

func mediaExtension(header [12]byte) string {
	switch {
	case string(header[:4]) == "RIFF" && string(header[8:]) == "WAVE":
		return ".wav"
	case string(header[4:8]) == "ftyp":
		return ".m4a"
	case string(header[:3]) == "ID3" || (header[0] == 0xff && header[1]&0xe6 == 0xe2):
		return ".mp3"
	}
	return ""
}

// ValidateMedia applies the same local-only container checks used by playback
// before an acquired file can become available for offline use.
func ValidateMedia(r io.ReaderAt, size int64) error {
	return validateMedia(r, size, nil)
}

func validateMedia(r io.ReaderAt, size int64, duration *int64) error {
	var header [12]byte
	if size < 12 || size > maxMediaBytes {
		return errors.New("invalid audio length")
	}
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return err
	}
	switch mediaExtension(header) {
	case ".wav":
		return checkWAV(r, size)
	case ".m4a":
		return checkMP4(r, size, duration)
	case ".mp3":
		// ID3 tags also occur on non-audio formats. Confirm there is an
		// actual MPEG audio frame immediately after the optional leading tag.
		offset := int64(0)
		if string(header[:3]) == "ID3" {
			for _, b := range header[6:10] {
				if b&0x80 != 0 {
					return errors.New("invalid MP3 tag")
				}
				offset = offset<<7 | int64(b)
			}
			offset += 10
			if header[3] == 4 && header[5]&0x10 != 0 {
				offset += 10
			}
		}
		var frame [4]byte
		if _, err := r.ReadAt(frame[:], offset); err != nil || frame[0] != 0xff || frame[1]&0xe6 != 0xe2 || frame[1]&0x18 == 0x08 || frame[2]&0x0c == 0x0c || frame[2]>>4 == 15 {
			return errors.New("invalid MPEG audio frame")
		}
		return nil
	}
	return errors.New("unsupported audio format")
}

// Only hand MF uncompressed, self-contained WAVE files. Ancillary chunks
// such as LIST/INFO are common in WAVs with a noncanonical header.
func checkWAV(r io.ReaderAt, size int64) error {
	var header [24]byte
	if _, err := r.ReadAt(header[:12], 0); err != nil {
		return err
	}
	if int64(binary.LittleEndian.Uint32(header[4:8]))+8 != size {
		return errors.New("invalid WAV length")
	}
	format, data := false, false
	for at := int64(12); at < size; {
		if size-at < 8 {
			return errors.New("incomplete WAV chunk")
		}
		if _, err := r.ReadAt(header[:8], at); err != nil {
			return err
		}
		chunk := string(header[:4])
		n := int64(binary.LittleEndian.Uint32(header[4:8]))
		if n > size-at-8 {
			return errors.New("invalid WAV chunk length")
		}
		switch chunk {
		case "fmt ":
			if n < 16 || format {
				return errors.New("invalid WAV format")
			}
			if _, err := r.ReadAt(header[:16], at+8); err != nil {
				return err
			}
			tag := binary.LittleEndian.Uint16(header[:2])
			if tag != 1 && tag != 3 {
				return errors.New("expected PCM or floating-point WAV audio")
			}
			format = true
		case "data":
			data = n > 0
		case "LIST", "JUNK", "PAD ", "fact", "bext", "cue ", "smpl", "iXML", "id3 ":
		default:
			return errors.New("unsupported WAV metadata")
		}
		at += 8 + n + (n & 1)
	}
	if !format || !data {
		return errors.New("WAV has no PCM data")
	}
	return nil
}

// Walk the ISO-BMFF track hierarchy before opening the file. Limit accepted
// tracks to self-contained AAC audio. In particular, reject reference movies,
// external dref entries, encrypted tracks, and non-audio presentations.
func checkMP4(r io.ReaderAt, size int64, duration *int64) error {
	count, tracks := 0, 0
	audio, data, localRef := false, false, false
	timing := fragmentTiming{}
	parents := map[string]string{"moov": "", "trak": "moov", "mdia": "trak", "minf": "mdia", "dinf": "minf", "stbl": "minf", "mvex": "moov", "moof": "", "traf": "moof"}
	var walk func(int64, int64, string) error
	walk = func(start, end int64, parent string) error {
		for start < end {
			var h [16]byte
			if end-start < 8 {
				return errors.New("invalid MP4 box")
			}
			if _, err := r.ReadAt(h[:8], start); err != nil {
				return err
			}
			length, header := int64(binary.BigEndian.Uint32(h[:4])), int64(8)
			if length == 1 {
				if _, err := r.ReadAt(h[8:], start+8); err != nil {
					return err
				}
				length, header = int64(binary.BigEndian.Uint64(h[8:])), 16
			} else if length == 0 {
				length = end - start
			}
			if length < header || length > end-start {
				return errors.New("invalid MP4 box length")
			}
			count++
			if count > 65536 {
				return errors.New("excessive MP4 boxes")
			}
			name := string(h[4:8])
			content, next := start+header, start+length
			if err := timing.read(r, name, content, next-content); err != nil {
				return err
			}
			switch name {
			case "moov", "trak", "mdia", "minf", "dinf", "stbl", "mvex", "moof", "traf":
				if parents[name] != parent {
					return errors.New("invalid MP4 hierarchy")
				}
				if name == "trak" {
					tracks++
					if tracks > 1 {
						return errors.New("expected one audio track")
					}
				}
				if err := walk(content, next, name); err != nil {
					return err
				}
			case "dref":
				// One full box header and entry count, one local url entry.
				var entry [20]byte
				if parent != "dinf" || next-content != 20 {
					return errors.New("external MP4 media references are unsupported")
				}
				if _, err := r.ReadAt(entry[:], content); err != nil {
					return err
				}
				if binary.BigEndian.Uint32(entry[4:8]) != 1 || binary.BigEndian.Uint32(entry[8:12]) != 12 || string(entry[12:16]) != "url " || binary.BigEndian.Uint32(entry[16:]) != 1 {
					return errors.New("external MP4 media references are unsupported")
				}
				localRef = true
			case "hdlr":
				var handler [4]byte
				if parent == "mdia" {
					if next-content < 12 {
						return errors.New("invalid MP4 track handler")
					}
					if _, err := r.ReadAt(handler[:], content+8); err != nil {
						return err
					}
					if string(handler[:]) != "soun" {
						return errors.New("expected an audio-only MP4")
					}
				}
			case "stsd":
				var desc [16]byte
				if parent != "stbl" || next-content < 36 {
					return errors.New("invalid MP4 sample description")
				}
				if _, err := r.ReadAt(desc[:], content); err != nil {
					return err
				}
				if binary.BigEndian.Uint32(desc[4:8]) != 1 || int64(binary.BigEndian.Uint32(desc[8:12])) != next-content-8 || string(desc[12:]) != "mp4a" {
					return errors.New("only unencrypted AAC MP4 tracks are supported")
				}
				if err := checkAACDescription(r, content+8, next); err != nil {
					return err
				}
				audio = true
			case "mdat":
				if parent == "" && next > content {
					data = true
				}
			case "rmra", "rmda", "rdrf", "pssh":
				return fmt.Errorf("unsupported MP4 box %q", name)
			}
			start = next
		}
		return nil
	}
	if err := walk(0, size, ""); err != nil {
		return err
	}
	if !audio || !data || !localRef {
		return errors.New("audio samples are missing from MP4")
	}
	if duration != nil {
		*duration = timing.durationMS()
	}
	return nil
}

// The MPEG-4 Systems ES_Descriptor can carry another media URL in addition
// to the track's dref. Accept embedded AAC configuration only.
func checkAACDescription(r io.ReaderAt, start, end int64) error {
	var fixed [36]byte
	if end-start < int64(len(fixed)) {
		return errors.New("invalid AAC sample entry")
	}
	if _, err := r.ReadAt(fixed[:], start); err != nil {
		return err
	}
	if binary.BigEndian.Uint16(fixed[14:16]) != 1 || binary.BigEndian.Uint16(fixed[16:18]) != 0 {
		return errors.New("unsupported AAC sample reference")
	}
	data := false
	for at := start + 36; at < end; {
		var header [16]byte
		if end-at < 8 {
			return errors.New("invalid AAC sample configuration")
		}
		if _, err := r.ReadAt(header[:8], at); err != nil {
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		if size < 8 || size > end-at {
			return errors.New("invalid AAC configuration size")
		}
		switch string(header[4:8]) {
		case "btrt", "chan", "srat":
		case "esds":
			n := min(int64(len(header)), size-8)
			if _, err := r.ReadAt(header[:n], at+8); err != nil {
				return err
			}
			if n < 9 || header[4] != 3 {
				return errors.New("missing AAC elementary stream descriptor")
			}
			idx := 5
			for idx < 8 && header[idx]&0x80 != 0 {
				idx++
			}
			if header[idx]&0x80 != 0 || int64(idx+4) > n || header[idx+3]&0x40 != 0 {
				return errors.New("external AAC elementary stream references are unsupported")
			}
			data = true
		default:
			return errors.New("unsupported AAC configuration")
		}
		at += size
	}
	if !data {
		return errors.New("missing AAC configuration")
	}
	return nil
}
