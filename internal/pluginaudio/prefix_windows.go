package pluginaudio

import (
	"bufio"
	"io"
)

// PrefixReady reports whether a growing source contains five seconds of
// usable audio. Only fully received MP4 fragments count toward that target.
func PrefixReady(r io.ReaderAt, size int64) bool {
	if size < 44 {
		return false
	}
	input := io.NewSectionReader(r, 0, size)
	if pcm, ok := peekPCM(bufio.NewReader(input)); ok {
		return size-44 >= min(5*int64(pcm.format.bytesPerSecond), pcm.length)
	}
	var magic [12]byte
	if _, err := r.ReadAt(magic[:], 0); err != nil || mediaExtension(magic) != ".m4a" {
		return false
	}
	var lastMediaEnd int64
	for pos := int64(0); pos < size; {
		header, err := readBoxHeader(io.NewSectionReader(r, pos, size-pos))
		if err != nil || header.length < header.header || header.length > size-pos {
			break
		}
		pos += header.length
		if header.name == "mdat" {
			lastMediaEnd = pos
		}
	}
	if lastMediaEnd == 0 {
		return false
	}
	var duration int64
	return checkMP4(r, lastMediaEnd, &duration) == nil && duration >= 5000
}
