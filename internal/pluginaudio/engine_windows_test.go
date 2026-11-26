package pluginaudio

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"time"

	"quailcrate/internal/testaudio"
)

func silentWAV(ms int) []byte {
	const rate = 16000
	samples := make([]byte, rate*2*ms/1000)
	var data bytes.Buffer
	data.WriteString("RIFF")
	binary.Write(&data, binary.LittleEndian, uint32(36+len(samples)))
	data.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		binary.Write(&data, binary.LittleEndian, v)
	}
	data.WriteString("data")
	binary.Write(&data, binary.LittleEndian, uint32(len(samples)))
	data.Write(samples)
	return data.Bytes()
}

func openBytes(data []byte) func(context.Context) (io.ReadCloser, error) {
	return func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

func TestRejectIndirectMedia(t *testing.T) {
	for _, s := range []string{"#EXTM3U\nhttps://example.invalid/track\n", "<ASX><REF HREF='http://example.invalid/a'/></ASX>", "ID3\x04\x00\x00\x00\x00\x00\x00#EXTM3U"} {
		if _, err := cacheStream(context.Background(), bufio.NewReader(strings.NewReader(s)), nil); err == nil {
			t.Fatal("accepted an indirect media source")
		}
	}
}

func TestReplaceCancelsLoadingAndIsolatesControls(t *testing.T) {
	events := make(chan Event, 10)
	e := New(func(ev Event) { events <- ev })
	defer e.Close()
	entered, canceled := make(chan struct{}), make(chan struct{})
	err := e.Play(context.Background(), "first", func(ctx context.Context) (io.ReadCloser, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := e.Play(context.Background(), "second", openBytes([]byte("invalid audio input"))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("superseded stream still running")
	}
	if _, err := e.Control("first", "audio.stop", 0, 0); err == nil {
		t.Fatal("old session can stop replacement")
	}
	select {
	case ev := <-events:
		if ev.Type != "audio.error" || ev.State.SessionID != "second" {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing decoder error")
	}
}

func TestWindowsPlayback(t *testing.T) {
	// Real device integration. A headless worker may have no audio endpoints.
	if !testaudio.HasOutput() {
		t.Skip("Windows has no audio output device")
	}
	events := make(chan Event, 20)
	e := New(func(ev Event) { events <- ev })
	defer e.Close()
	if err := e.Play(context.Background(), "silence", openBytes(silentWAV(1500))); err != nil {
		t.Fatal(err)
	}
	wait := func(status string) Event {
		t.Helper()
		timeout := time.After(8 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Type == "audio.error" {
					if strings.Contains(ev.Error, "c00d4e84") {
						t.Skip("no audio playback device available")
					}
					t.Fatalf("playback failed: %s", ev.Error)
				}
				if ev.State.Status == status {
					return ev
				}
			case <-timeout:
				t.Fatalf("timed out waiting for %s", status)
			}
		}
	}
	playing := wait("playing")
	if !playing.State.Seekable || playing.State.DurationMS != 1500 {
		t.Fatalf("unexpected state %+v", playing.State)
	}
	if _, err := e.Control("silence", "audio.pause", 0, 0); err != nil {
		t.Fatal(err)
	}
	wait("paused")
	if _, err := e.Control("silence", "audio.seek", 1000, 0); err != nil {
		t.Fatal(err)
	}
	st, err := e.Control("silence", "audio.setVolume", 0, 0.25)
	if err != nil || st.Volume != .25 {
		t.Fatalf("%+v, %v", st, err)
	}
	if _, err := e.Control("silence", "audio.resume", 0, 0); err != nil {
		t.Fatal(err)
	}
	wait("playing")
	ended := wait("ended")
	if ended.Type != "audio.ended" {
		t.Fatalf("bad end event: %+v", ended)
	}
}

func TestWindowsPCMPlaysBeforeDownloadCompletes(t *testing.T) {
	if !testaudio.HasOutput() {
		t.Skip("Windows has no audio output device")
	}
	evs := make(chan Event, 8)
	e := New(func(ev Event) { evs <- ev })
	defer e.Close()
	r, w := io.Pipe()
	defer w.Close()
	if err := e.Play(context.Background(), "progressive", func(context.Context) (io.ReadCloser, error) { return r, nil }); err != nil {
		t.Fatal(err)
	}
	b := silentWAV(3000)
	written := make(chan error, 1)
	go func() { _, err := w.Write(b[:len(b)/2]); written <- err }()
	select {
	case ev := <-evs:
		if ev.State.Status != "playing" {
			t.Fatalf("partial PCM did not start: %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waited for the complete download")
	}
	st, err := e.Control("progressive", "audio.stop", 0, 0)
	if err != nil || st.Status != "stopped" {
		t.Fatalf("%+v %v", st, err)
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release source")
	}
}

// Generated from silence with ffmpeg: anullsrc=r=48000:cl=stereo, 0.32 s,
// AAC-LC, MP4 empty_moov+default_base_moof+frag_keyframe.
//
//go:embed testdata/silence-fragmented.m4a
var fragmentedAAC []byte

func TestFiveSecondPrefixNeedsCompleteAudio(t *testing.T) {
	wav := silentWAV(9000)
	for _, size := range []int64{44, 160043, 160044} {
		if got := PrefixReady(bytes.NewReader(wav), size); got != (size == 160044) {
			t.Errorf("PCM prefix of %d bytes ready=%v", size, got)
		}
	}
	// Stretch the fixture's media timeline so its sole fragment spans five
	// seconds. Receiving the timing header alone must never mark it ready.
	m4a := bytes.Clone(fragmentedAAC)
	mdhd := bytes.Index(m4a, []byte("mdhd"))
	mdat := bytes.Index(m4a, []byte("mdat")) - 4
	if mdhd < 0 || mdat < 0 || m4a[mdhd+4] != 0 {
		t.Fatal("unexpected fragmented fixture")
	}
	binary.BigEndian.PutUint32(m4a[mdhd+16:], 1000)
	end := int64(mdat) + int64(binary.BigEndian.Uint32(m4a[mdat:]))
	if PrefixReady(bytes.NewReader(m4a), end-1) || !PrefixReady(bytes.NewReader(m4a), end) {
		t.Fatal("AAC startup buffer did not follow complete fragment boundaries")
	}
}

func TestFragmentedAACAndExternalReferences(t *testing.T) {
	var duration int64
	if err := checkMP4(bytes.NewReader(fragmentedAAC), int64(len(fragmentedAAC)), &duration); err != nil {
		t.Fatal(err)
	}
	if duration < 320 || duration > 350 {
		t.Fatalf("sample duration %dms", duration)
	}
	modified := bytes.Clone(fragmentedAAC)
	urlBox := bytes.Index(modified, []byte("url "))
	if urlBox < 0 {
		t.Fatal("fixture lacks dref")
	}
	modified[urlBox+7] = 0
	if err := checkMP4(bytes.NewReader(modified), int64(len(modified)), nil); err == nil {
		t.Fatal("external data reference accepted")
	}
	modified = bytes.Clone(fragmentedAAC)
	esds := bytes.Index(modified, []byte("esds"))
	if esds < 0 {
		t.Fatal("fixture lacks AAC configuration")
	}
	length := esds + 9
	for modified[length]&0x80 != 0 {
		length++
	}
	modified[length+3] |= 0x40
	if err := checkMP4(bytes.NewReader(modified), int64(len(modified)), nil); err == nil {
		t.Fatal("external AAC elementary stream reference accepted")
	}
}

func TestWindowsFragmentedAAC(t *testing.T) {
	if !testaudio.HasOutput() {
		t.Skip("Windows has no audio output device")
	}
	events := make(chan Event, 16)
	e := New(func(ev Event) { events <- ev })
	defer e.Close()
	if err := e.Play(context.Background(), "dash", openBytes(fragmentedAAC)); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(8 * time.Second)
	paused, resumed := false, false
	for {
		select {
		case ev := <-events:
			if ev.Type == "audio.error" {
				if strings.Contains(ev.Error, "c00d4e84") {
					t.Skip("no audio playback device available")
				}
				t.Fatalf("AAC decoding failed: %s", ev.Error)
			}
			if ev.Type == "audio.ended" {
				if ev.State.DurationMS < 300 || !resumed {
					t.Fatalf("unexpected AAC duration: %+v", ev.State)
				}
				return
			}
			if ev.State.Status == "playing" && !paused {
				if _, err := e.Control("dash", "audio.pause", 0, 0); err != nil {
					t.Fatal(err)
				}
				paused = true
			} else if ev.State.Status == "paused" && !resumed {
				if _, err := e.Control("dash", "audio.seek", 128, 0); err != nil {
					t.Fatal(err)
				}
				if _, err := e.Control("dash", "audio.resume", 0, 0); err != nil {
					t.Fatal(err)
				}
				resumed = true
			}
		case <-timeout:
			t.Fatal("timed out decoding fragmented AAC")
		}
	}
}

func TestWindowsFragmentedAACPlaysBeforeDownloadCompletes(t *testing.T) {
	if !testaudio.HasOutput() {
		t.Skip("Windows has no audio output device")
	}
	evs := make(chan Event, 16)
	e := New(func(ev Event) { evs <- ev })
	defer e.Close()

	// Pipe the full fragmented AAC sample WITHOUT closing the writer, simulating a
	// slow/incomplete HTTP stream where later fragments have not downloaded yet.
	r, w := io.Pipe()
	defer w.Close()
	if err := e.Play(context.Background(), "dash-stream", func(context.Context) (io.ReadCloser, error) { return r, nil }); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		_, err := w.Write(fragmentedAAC)
		written <- err
		// Intentionally keep the pipe open without EOF until stopped.
	}()

	select {
	case ev := <-evs:
		if ev.Type == "audio.error" {
			if strings.Contains(ev.Error, "c00d4e84") {
				t.Skip("no audio playback device available")
			}
			t.Fatalf("progressive AAC failed: %s", ev.Error)
		}
		if ev.State.Status != "playing" {
			t.Fatalf("fragmented AAC did not start before EOF: %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fragmented AAC waited for complete download instead of streaming progressively")
	}

	st, err := e.Control("dash-stream", "audio.stop", 0, 0)
	if err != nil || st.Status != "stopped" {
		t.Fatalf("stop streaming AAC: %+v %v", st, err)
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("writer did not complete")
	}
}
