package musicmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func wav(seconds int) []byte {
	size := 8000 * 2 * seconds
	raw := make([]byte, size+44)
	copy(raw, "RIFF")
	put32 := binary.LittleEndian.PutUint32
	put16 := binary.LittleEndian.PutUint16
	put32(raw[4:], uint32(size+36))
	copy(raw[8:], "WAVEfmt ")
	put32(raw[16:], 16)
	put16(raw[20:], 1)
	put16(raw[22:], 1)
	put32(raw[24:], 8000)
	put32(raw[28:], 16000)
	put16(raw[32:], 2)
	put16(raw[34:], 16)
	copy(raw[36:], "data")
	put32(raw[40:], uint32(size))
	return raw
}

type sized struct {
	*bytes.Reader
}

func (*sized) Close() error { return nil }

func source(data []byte, calls *atomic.Int32) Source {
	return func(context.Context) (io.ReadCloser, error) {
		calls.Add(1)
		return &sized{bytes.NewReader(data)}, nil
	}
}

func await(t *testing.T, ok func() bool) {
	t.Helper()
	limit := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(limit) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func read(t *testing.T, s *Store, key Key, label string, src Source) []byte {
	t.Helper()
	f, err := s.Open(t.Context(), key, label, src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPrefetchStopAtFiveSecondsAndPromoteInFlight(t *testing.T) {
	cache, downloads := t.TempDir(), t.TempDir()
	s := New(t.Context(), cache, downloads)
	key, data := Key{"BV1-p2", "192k"}, wav(25)
	calls := new(atomic.Int32)
	if err := s.Prefetch(key, "192K AAC", source(data, calls)); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { e, _ := s.Lookup(key); return e.Bytes >= 80044 })
	e, _ := s.Lookup(key)
	if e.Bytes >= 130000 || e.Total != int64(len(data)) {
		t.Fatalf("speculative read %+v", e)
	}
	r, err := s.Open(t.Context(), key, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Download(t.Context(), key, "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, data) || calls.Load() != 1 {
		t.Fatalf("promotion failed: %v, bytes %d, calls %d", err, len(got), calls.Load())
	}
	s.Close()
	s = New(t.Context(), cache, downloads)
	defer s.Close()
	changed := Key{key.Track, "flac"}
	e, found := s.Lookup(changed)
	if !found || e.Label != "192K AAC" || !e.Downloaded || len(s.cache) != 0 || len(s.Downloads()) != 1 {
		t.Fatalf("offline download state: %+v, cache %v", e, s.cache)
	}
	if got := read(t, s, changed, "", nil); !bytes.Equal(got, data) {
		t.Fatal("offline bytes mismatch")
	}
	s.RejectPlayback(changed)
	if _, found := s.Lookup(changed); !found {
		t.Fatal("device failure removed the download")
	}
	if err := s.RemoveDownload(key.Track); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(t.Context(), key, "", nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed download still playable: %v", err)
	}
}

func TestLRUAndCacheHitPromotion(t *testing.T) {
	s := New(t.Context(), t.TempDir(), t.TempDir())
	defer s.Close()
	data, calls := wav(1), new(atomic.Int32)
	k := func(i int) Key { return Key{fmt.Sprintf("track-%02d", i), "192k"} }
	for i := 0; i < 30; i++ {
		read(t, s, k(i), "192K AAC", source(data, calls))
	}
	read(t, s, k(0), "", nil)
	read(t, s, k(30), "192K AAC", source(data, calls))
	if _, ok := s.Lookup(k(1)); ok || len(s.cache) != 30 {
		t.Fatal("incorrect LRU eviction")
	}
	changedPreference := Key{k(0).Track, "flac"}
	if err := s.Download(t.Context(), changedPreference, "", nil); err != nil {
		t.Fatal(err)
	}
	items := s.Downloads()
	if calls.Load() != 31 || len(s.cache) != 29 || len(items) != 1 || items[0].Quality != "192k" || items[0].Label != "192K AAC" {
		t.Fatalf("after local promotion: calls=%d cache=%d downloads=%d", calls.Load(), len(s.cache), len(s.Downloads()))
	}
}

func TestRejectedOrCorruptData(t *testing.T) {
	cache, downloads := t.TempDir(), t.TempDir()
	s := New(t.Context(), cache, downloads)
	k, calls := Key{"bad", "q"}, new(atomic.Int32)
	if err := s.Download(t.Context(), k, "", source([]byte("invalid media bytes"), calls)); err == nil {
		t.Fatal("malformed audio was committed")
	}
	s.Close()
	s = New(t.Context(), cache, downloads)
	defer s.Close()
	if items := s.Downloads(); len(items) != 1 || items[0].Status != "error" {
		t.Fatalf("lost failure: %+v", items)
	}
	read(t, s, k, "", source(wav(1), calls))
	r := s.cache[k.Track]
	if err := os.WriteFile(filepath.Join(cache, r.File), make([]byte, r.Bytes), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(t.Context(), k, "", nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("same-size corruption not detected: %v", err)
	}
	if _, ok := s.Lookup(k); ok {
		t.Fatal("corrupt file still in catalog")
	}
}

func TestClosingForegroundFinishesInBackgroundAndRemoveIsOrdered(t *testing.T) {
	s := New(t.Context(), t.TempDir(), t.TempDir())
	defer s.Close()
	k, data := Key{"a", "q"}, wav(1)
	pr, pw := io.Pipe()
	r, err := s.Open(t.Context(), k, "q", func(context.Context) (io.ReadCloser, error) { return pr, nil })
	if err != nil {
		t.Fatal(err)
	}
	wait, err := s.BeginDownload(t.Context(), k, "q", nil)
	if err != nil || wait == nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(data[:64]); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := s.RemoveDownload(k.Track); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(data[64:]); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	if err := wait(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed task reported success: %v", err)
	}
	e, found := s.Lookup(k)
	if !found || e.Status != "ready" || e.Downloaded || len(s.Downloads()) != 0 {
		t.Fatalf("previously played track not cached: %+v", e)
	}
}

func TestCacheWriteFailureDoesNotStopPlaying(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocked, []byte{1}, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(t.Context(), blocked, blocked)
	defer s.Close()
	key, data := Key{"track", "q"}, wav(1)
	if got := read(t, s, key, "", source(data, new(atomic.Int32))); !bytes.Equal(got, data) {
		t.Fatal("playback lost bytes")
	}
	if _, ok := s.Lookup(key); ok {
		t.Fatal("disk failure produced a cache hit")
	}
	if err := s.Download(t.Context(), key, "", source(data, new(atomic.Int32))); !errors.Is(err, ErrStorage) {
		t.Fatalf("download did not report storage error: %v", err)
	}
}

func TestRapidSkipAdmitsForegroundAndBoundsOutstandingTracks(t *testing.T) {
	s := New(t.Context(), t.TempDir(), t.TempDir())
	defer s.Close()
	started := make(chan string, 100)
	gated := func(id string) Source {
		return func(ctx context.Context) (io.ReadCloser, error) {
			started <- id
			r, w := io.Pipe()
			context.AfterFunc(ctx, func() { w.CloseWithError(ctx.Err()) })
			return r, nil
		}
	}
	for i := 0; i < 35; i++ {
		id := fmt.Sprint(i)
		f, err := s.Open(t.Context(), Key{id, "q"}, "q", gated(id))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-started:
			if got != id {
				t.Fatalf("background %s took slot from foreground %s", got, id)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("new foreground blocked on old tracks")
		}
		f.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) > 30 || s.active > 4 {
		t.Fatalf("resources unbounded: jobs %d, active %d", len(s.jobs), s.active)
	}
}

type interruptedSource struct {
	data    []byte
	offset  int
	stopAt  int
	resumed atomic.Int32
}

func (r *interruptedSource) Size() int64 { return int64(len(r.data)) }
func (*interruptedSource) Close() error  { return nil }

func (r *interruptedSource) Read(b []byte) (int, error) {
	if r.offset == r.stopAt {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(b, r.data[r.offset:r.stopAt])
	r.offset += n
	return n, nil
}

func (r *interruptedSource) Resume(_ context.Context, offset int64) (io.ReadCloser, error) {
	if int(offset) != r.stopAt {
		return nil, errors.New("incorrect range offset")
	}
	r.resumed.Add(1)
	return &sized{bytes.NewReader(r.data[offset:])}, nil
}

func TestExpiredPrefetchConnectionResumesAtVerifiedOffset(t *testing.T) {
	s := New(t.Context(), t.TempDir(), t.TempDir())
	defer s.Close()
	key := Key{"next", "q"}
	body := &interruptedSource{data: wav(20), stopAt: 80044}
	opens := new(atomic.Int32)
	if err := s.Prefetch(key, "q", func(context.Context) (io.ReadCloser, error) {
		opens.Add(1)
		return body, nil
	}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { e, ok := s.Lookup(key); return ok && e.Bytes == int64(body.stopAt) })
	got := read(t, s, key, "", nil)
	if !bytes.Equal(got, body.data) || body.resumed.Load() != 1 || opens.Load() != 1 {
		t.Fatalf("expired prefix lost: resumed=%d opened=%d", body.resumed.Load(), opens.Load())
	}
}

func TestPrefetchWaiterReportsFailureAndBackupReadiness(t *testing.T) {
	s := New(t.Context(), t.TempDir(), t.TempDir())
	defer s.Close()
	key := Key{"next", "q"}
	pr, pw := io.Pipe()
	wait, err := s.BeginPrefetch(key, "q", func(context.Context) (io.ReadCloser, error) { return pr, nil })
	if err != nil || wait == nil {
		t.Fatalf("register prefetch: %v", err)
	}
	cdnErr := errors.New("disconnected CDN")
	pw.CloseWithError(cdnErr)
	if err := wait(); !errors.Is(err, cdnErr) {
		t.Fatalf("failed CDN reported %v", err)
	}
	if _, ok := s.Lookup(key); ok {
		t.Fatal("failed prefix was reported as a hit")
	}
	wait, err = s.BeginPrefetch(key, "q", source(wav(30), new(atomic.Int32)))
	if err != nil || wait == nil {
		t.Fatalf("register backup: %v", err)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	entry, ok := s.Lookup(key)
	if !ok || entry.Bytes < 80044 || entry.Bytes >= entry.Total {
		t.Fatalf("backup waiter completed at wrong point: %+v", entry)
	}
	s.CancelPrefetch()
}

func TestUnreadableDownloadCatalogPreservesSavedAudio(t *testing.T) {
	cache, downloads := t.TempDir(), t.TempDir()
	key := Key{"keep-me", "192k"}
	data := wav(1)
	calls := new(atomic.Int32)
	s := New(t.Context(), cache, downloads)
	if err := s.Download(t.Context(), key, "192K AAC", source(data, calls)); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(downloads, s.downloads[key.Track].File)
	s.Close()
	index := filepath.Join(downloads, "index.json")
	broken := []byte("[{")
	if err := os.WriteFile(index, broken, 0600); err != nil {
		t.Fatal(err)
	}
	s = New(t.Context(), cache, downloads)
	defer s.Close()
	if err := s.Download(t.Context(), Key{"new", "192k"}, "", source(data, calls)); !errors.Is(err, ErrStorage) {
		t.Fatalf("invalid catalog accepted a new download: %v", err)
	}
	if err := s.RemoveDownload(key.Track); !errors.Is(err, ErrStorage) {
		t.Fatalf("invalid catalog permitted removal: %v", err)
	}
	for path, want := range map[string][]byte{saved: data, index: broken} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("original file changed %s: %v", path, err)
		}
	}
}
