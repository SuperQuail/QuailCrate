package musicmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"quailcrate/internal/pluginaudio"
)

type transfer struct {
	store *Store
	key   Key
	label string
	used  int64

	ctx    context.Context
	cancel context.CancelFunc
	source Source
	resume func(context.Context, int64) (io.ReadCloser, error)
	f      *os.File

	// A cache write may fail while the decoder is consuming the prefix.
	// Preserve the remainder in RAM only in this exceptional case so audio
	// continues. The same per-source limit applies; never publish this data.
	fileBytes int64
	tail      []byte
	size      int64
	total     int64
	diskErr   error
	err       error
	storeErr  error
	digest    string

	played    bool
	pinned    bool
	warmed    bool
	running   bool
	yield     bool
	interrupt context.CancelFunc
	ended     bool
	readers   int
	changed   chan struct{}
	done      chan struct{}
}

func (j *transfer) signal() {
	close(j.changed)
	j.changed = make(chan struct{})
}

func (j *transfer) entry() Entry {
	return Entry{Key: j.key, Label: j.label, Bytes: j.size, Total: j.total, Status: "downloading"}
}

func (s *Store) job(key Key, label string, open Source) (*transfer, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if !validKey(key) {
		return nil, errors.New("invalid media key")
	}
	if j := s.jobs[key]; j != nil && j.ctx.Err() == nil {
		return j, nil
	}
	if open == nil {
		return nil, os.ErrNotExist
	}
	ctx, cancel := context.WithCancel(s.ctx)
	j := &transfer{store: s, key: key, label: label, used: s.tick(), ctx: ctx, cancel: cancel, source: open,
		changed: make(chan struct{}), done: make(chan struct{})}
	s.jobs[key] = j
	return j, nil
}

func (s *Store) promote(j *transfer) {
	if s.prefetch == j {
		s.prefetch = nil
	}
	j.signal()
	s.schedule()
}

// Open returns growing bytes immediately. Closing the reader stops this
// decoder's wait; a track that has actually been selected keeps downloading.
func (s *Store) Open(ctx context.Context, key Key, label string, open Source) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if r, dir, ok := s.complete(key); ok {
		f, err := checkedFile(dir, r)
		if err == nil {
			if !r.Downloaded {
				r.Used = s.tick()
				s.cache[key.Track] = r
				_ = save(s.cacheDir, s.cache)
			}
			return &storedReader{File: f, store: s, record: r, dir: dir}, nil
		}
		s.invalidate(key)
	}
	j, err := s.job(key, label, open)
	if err != nil {
		return nil, err
	}
	j.played = true
	s.foreground = j
	j.used = s.tick()
	j.readers++
	s.trimRecent()
	s.promote(j)
	return &reader{j: j, ctx: ctx}, nil
}

// Windows may keep a linked file's old name alive while a decoder is reading
// it. Retry deletion when that reader releases its handle after an eviction,
// promotion, or explicit removal.
type storedReader struct {
	*os.File
	store  *Store
	record record
	dir    string
}

func (r *storedReader) Close() error {
	err := r.File.Close()
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	catalog := r.store.cache
	if r.record.Downloaded {
		catalog = r.store.downloads
	}
	if catalog[r.record.Track].File != r.record.File {
		removeFile(r.dir, r.record)
	}
	return err
}

// Count recently requested distinct tracks, including their unfinished jobs.
// Rapid skips then retain at most 30 pending sources, and a late completion
// cannot bring an evicted song back.
func (s *Store) trimRecent() {
	used := make(map[string]int64, len(s.cache))
	for id, r := range s.cache {
		used[id] = r.Used
	}
	for _, j := range s.jobs {
		if j.played && !j.pinned {
			used[j.key.Track] = max(used[j.key.Track], j.used)
		}
	}
	if len(used) <= cacheTracks {
		return
	}
	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, k int) bool { return used[ids[i]] > used[ids[k]] })
	for _, id := range ids[cacheTracks:] {
		if r, ok := s.cache[id]; ok {
			delete(s.cache, id)
			removeFile(s.cacheDir, r)
		}
		for _, j := range s.jobs {
			if j.key.Track == id && !j.pinned {
				j.played = false
				if j.readers == 0 {
					j.cancel()
					j.signal()
					if !j.running {
						s.finish(j, context.Canceled, nil)
					}
				}
			}
		}
	}
	_ = save(s.cacheDir, s.cache)
}

// Download joins the foreground or prefetched transfer when available. It
// writes status before starting so interrupted downloads survive host restart.
func (s *Store) Download(ctx context.Context, key Key, label string, open Source) error {
	wait, err := s.BeginDownload(ctx, key, label, open)
	if err != nil || wait == nil {
		return err
	}
	return wait()
}

// BeginDownload commits the request to the catalog before returning. Host
// dispatch can then process a later remove operation in the original order.
func (s *Store) BeginDownload(ctx context.Context, key Key, label string, open Source) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := s.downloadIndexErr; err != nil {
		s.mu.Unlock()
		return nil, err
	}
	// The download action saves this track. A preference change applies to
	// newly resolved streams; already available audio keeps its actual label.
	requested := key
	if _, _, ok := s.complete(key); !ok && (s.jobs[key] == nil || s.jobs[key].ctx.Err() != nil) {
		if r, ok := s.cache[key.Track]; ok {
			key = r.Key
		} else {
			var latest *transfer
			for _, running := range s.jobs {
				if running.key.Track == key.Track && running.ctx.Err() == nil && (latest == nil || running.used > latest.used) {
					latest = running
				}
			}
			if latest != nil {
				key = latest.key
			}
		}
	}
	if r, dir, ok := s.complete(key); ok {
		f, err := checkedFile(dir, r)
		if err == nil {
			src := f.Name()
			f.Close()
			if !r.Downloaded {
				r.Used = s.tick()
				err = s.publish(r, src, true)
				if err != nil {
					s.markFailure(key, label)
					err = errors.Join(ErrStorage, err)
				}
			}
			s.mu.Unlock()
			return nil, err
		}
		s.invalidate(key)
		key = requested
	}
	j, err := s.job(key, label, open)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !j.pinned {
		old := s.downloads[key.Track]
		s.downloads[key.Track] = record{Entry: j.entry(), Used: s.tick()}
		if err := save(s.downloadDir, s.downloads); err != nil {
			s.markFailure(key, label)
			if !j.played && s.prefetch != j {
				j.cancel()
				if !j.running {
					s.finish(j, context.Canceled, nil)
				}
			}
			s.mu.Unlock()
			return nil, errors.Join(ErrStorage, err)
		}
		if old.Key != key {
			if prev := s.jobs[old.Key]; prev != nil {
				prev.pinned = false
				if !prev.played {
					prev.cancel()
					if !prev.running {
						s.finish(prev, context.Canceled, nil)
					}
				}
			}
		}
		removeFile(s.downloadDir, old)
		j.pinned = true
		s.promote(j)
	}
	s.mu.Unlock()
	return func() error { return j.waitDownload(ctx) }, nil
}

func (j *transfer) waitDownload(ctx context.Context) error {
	s := j.store
	select {
	case <-j.done:
		if j.err != nil {
			return j.err
		}
		if j.storeErr != nil {
			return j.storeErr
		}
		s.mu.Lock()
		r, ok := s.downloads[j.key.Track]
		s.mu.Unlock()
		if !ok || r.Key != j.key || !r.Downloaded {
			return os.ErrNotExist
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *Store) markFailure(key Key, label string) {
	s.downloads[key.Track] = record{Entry: Entry{Key: key, Label: label, Status: "error"}, Used: s.tick()}
	_ = save(s.downloadDir, s.downloads)
}

// Prefetch reserves a single next track. Changing the prediction releases the
// previous prefix unless a real playback or download has claimed it.
func (s *Store) Prefetch(key Key, label string, open Source) error {
	_, err := s.BeginPrefetch(key, label, open)
	return err
}

// BeginPrefetch registers the predicted track in dispatch order. The
// asynchronous waiter reports the actual outcome so a failed CDN can be
// retried before the user reaches this track.
func (s *Store) BeginPrefetch(key Key, label string, open Source) (func() error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if s.prefetch != nil && s.prefetch.key != key {
		s.cancelPrefetch()
	}
	if _, _, ok := s.complete(key); ok {
		return nil, nil
	}
	j, err := s.job(key, label, open)
	if err != nil {
		return nil, err
	}
	if !j.played && !j.pinned {
		s.prefetch = j
		s.schedule()
		if !j.warmed {
			return j.waitPrefix, nil
		}
	}
	return nil, nil
}

func (j *transfer) waitPrefix() error {
	s := j.store
	for {
		s.mu.Lock()
		if j.warmed || j.played || j.pinned {
			s.mu.Unlock()
			return nil
		}
		if j.ended {
			err := j.err
			s.mu.Unlock()
			return err
		}
		changed := j.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
}

func (s *Store) CancelPrefetch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelPrefetch()
}

func (s *Store) cancelPrefetch() {
	if j := s.prefetch; j != nil {
		s.prefetch = nil
		j.cancel()
		j.signal()
		if !j.running {
			s.finish(j, context.Canceled, nil)
		}
	}
}

// At most four transfers (including one speculative body) can occupy sockets
// and staging files. Waiting jobs have no resources. A requested foreground
// track gets the next available slot before a download or a speculative job.
func (s *Store) schedule() {
	if s.ctx.Err() != nil {
		return
	}
	target := s.prefetch
	if fg := s.foreground; fg != nil && !fg.running {
		target = fg
	}
	if s.active == 4 && target != nil && !target.running && !target.ended {
		var victim *transfer
		for _, j := range s.jobs {
			if j.yield {
				return
			}
			if j.running && j != s.foreground && j != target && (victim == nil || j.used < victim.used) {
				victim = j
			}
		}
		if victim != nil {
			victim.yield = true
			victim.interrupt()
		}
	}
	for s.active < 4 {
		var next *transfer
		for _, j := range s.jobs {
			if j.running || j.ended || j.ctx.Err() != nil || (!j.played && !j.pinned && j != s.prefetch) {
				continue
			}
			if s.active >= 3 && j != s.foreground && j != s.prefetch {
				continue
			}
			if next == nil || j == s.foreground || next != s.foreground && (priority(j) > priority(next) || priority(j) == priority(next) && j.used > next.used) {
				next = j
			}
		}
		if next == nil {
			break
		}
		next.running = true
		ctx, cancel := context.WithCancel(next.ctx)
		next.interrupt = cancel
		s.active++
		s.workers.Add(1)
		go next.run(ctx)
	}
}

func priority(j *transfer) int {
	if j == j.store.prefetch {
		return 3
	}
	if j.pinned {
		return 2
	}
	return 1
}

func (j *transfer) run(ctx context.Context) {
	s := j.store
	defer s.workers.Done()
	digest, err := j.fetch(ctx)
	if errors.Is(err, errParkedBody) && ctx.Err() == nil {
		digest, err = j.fetch(ctx)
	}
	s.mu.Lock()
	j.interrupt()
	s.active--
	j.running = false
	if j.yield && j.ctx.Err() == nil {
		j.yield = false
		s.schedule()
		s.mu.Unlock()
		return
	}
	var storageErr error
	if err == nil && j.ctx.Err() == nil {
		if writeErr := s.commit(j, digest); writeErr != nil {
			storageErr = errors.Join(ErrStorage, writeErr)
		}
	} else if err == nil {
		err = j.ctx.Err()
	}
	s.finish(j, err, storageErr)
	s.schedule()
	s.mu.Unlock()
}

func (s *Store) finish(j *transfer, err, storageErr error) {
	if j.ended {
		return
	}
	j.ended, j.err, j.storeErr = true, err, storageErr
	j.cancel()
	current := s.jobs[j.key] == j
	if current {
		delete(s.jobs, j.key)
	}
	if s.prefetch == j {
		s.prefetch = nil
	}
	if s.foreground == j {
		s.foreground = nil
	}
	if current && j.pinned && (err != nil || storageErr != nil) {
		s.failDownload(j)
	}
	j.signal()
	close(j.done)
	j.cleanup()
}

func (j *transfer) cleanup() {
	if j.ended && j.readers == 0 {
		if j.f != nil {
			name := j.f.Name()
			j.f.Close()
			_ = os.Remove(name)
			j.f = nil
		}
		j.tail = nil
	}
}

func (j *transfer) append(b []byte) {
	if j.diskErr == nil {
		n, err := j.f.Write(b)
		j.fileBytes += int64(n)
		if err == nil && n < len(b) {
			err = io.ErrShortWrite
		}
		j.diskErr = err
		b = b[n:]
	}
	j.tail = append(j.tail, b...)
	j.size = j.fileBytes + int64(len(j.tail))
	j.signal()
}

var errParkedBody = errors.New("prefetched connection expired")

// Resume with a validated byte range whenever the original HTTP response
// supports it. Without a strong representation identity the full response is
// compared to the retained prefix before any new bytes can be appended.
func (j *transfer) open(ctx context.Context, previous []byte) (io.ReadCloser, bool, error) {
	if j.size > 0 && j.resume != nil && (j.total == 0 || j.size < j.total) {
		body, err := j.resume(ctx, j.size)
		if !errors.Is(err, errors.ErrUnsupported) {
			return body, true, err
		}
	}
	body, err := j.source(ctx)
	if err != nil || body == nil {
		return body, false, err
	}
	if j.size > 0 {
		// Source implementations can require an explicit Close to interrupt a
		// read even after their opening context has been canceled.
		closeBody := sync.OnceFunc(func() { _ = body.Close() })
		stop := context.AfterFunc(ctx, closeBody)
		sum := sha256.New()
		_, err = io.CopyN(sum, body, j.size)
		stop()
		if err != nil || !bytes.Equal(previous, sum.Sum(nil)) {
			closeBody()
			return nil, false, errors.New("resumed audio source changed")
		}
	}
	return body, false, nil
}

func (j *transfer) fetch(attempt context.Context) (string, error) {
	s := j.store
	if j.digest != "" {
		return j.digest, j.waitPromotion(attempt)
	}
	// Start the transfer deadline when a queued job actually acquires a slot.
	ctx, cancel := context.WithCancel(attempt)
	defer cancel()
	const budget = 2 * time.Minute
	started := time.Now()
	timer := time.AfterFunc(budget, cancel)
	defer timer.Stop()
	hash := sha256.New()
	if j.fileBytes > 0 {
		if _, err := io.Copy(hash, io.NewSectionReader(j.f, 0, j.fileBytes)); err != nil {
			return "", err
		}
	}
	hash.Write(j.tail)
	body, ranged, err := j.open(ctx, hash.Sum(nil))
	if err != nil {
		return "", err
	}
	if body == nil {
		return "", errors.New("empty audio source")
	}
	closeBody := sync.OnceFunc(func() { _ = body.Close() })
	stop := context.AfterFunc(ctx, closeBody)
	defer func() { stop(); closeBody() }()
	s.mu.Lock()
	if resume, ok := body.(interface {
		Resume(context.Context, int64) (io.ReadCloser, error)
	}); !ranged && ok {
		j.resume = resume.Resume
	}
	if known, ok := body.(interface{ Size() int64 }); !ranged && ok {
		j.total = max(0, known.Size())
	}
	dir := s.cacheDir
	if j.pinned {
		dir = s.downloadDir
	}
	if j.f == nil && j.diskErr == nil {
		if dir != "" {
			j.f, j.diskErr = os.CreateTemp(dir, "work-")
		} else {
			j.diskErr = ErrStorage
		}
	}
	speculative := s.prefetch == j
	s.mu.Unlock()
	if j.total > maxBytes {
		return "", errors.New("audio exceeds size limit")
	}
	buf := make([]byte, 32<<10)
	var prefixCheckedAt int64
	var first [12]byte
	if j.fileBytes >= int64(len(first)) {
		_, _ = j.f.ReadAt(first[:], 0)
	}
	parked := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		chunk := buf
		if speculative {
			chunk = buf[:8192]
		}
		n, readErr := body.Read(chunk)
		if n > 0 {
			s.mu.Lock()
			if j.size < int64(len(first)) {
				copy(first[j.size:], buf[:n])
			}
			if j.size+int64(n) > maxBytes {
				s.mu.Unlock()
				return "", errors.New("audio exceeds size limit")
			}
			j.append(buf[:n])
			speculative = s.prefetch == j
			abortStorage := j.diskErr != nil && !j.played
			size := j.size
			s.mu.Unlock()
			if abortStorage {
				return "", errors.Join(ErrStorage, j.diskErr)
			}
			hash.Write(buf[:n])
			if speculative && readErr == nil && (size-prefixCheckedAt >= 8192 || size == j.total || n < len(chunk) || prefixCheckedAt == 0) {
				prefixCheckedAt = size
				// A format for which a prefix cannot currently be decoded
				// speculatively reads at most 512 KiB. Larger PCM streams and
				// fragmented AAC use the measured duration with a hard ceiling.
				prefixLimit := int64(512 << 10)
				if string(first[:4]) == "RIFF" {
					prefixLimit = 4 << 20
				} else if string(first[4:8]) == "ftyp" {
					prefixLimit = 2 << 20
				}
				if size >= prefixLimit || pluginaudio.PrefixReady(j.f, size) {
					left := budget - time.Since(started)
					timer.Stop()
					if err := j.waitPromotion(ctx); err != nil {
						return "", err
					}
					parked = true
					speculative = false
					timer.Reset(left)
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				if parked && ctx.Err() == nil {
					return "", errParkedBody
				}
				return "", readErr
			}
			break
		}
	}
	if j.size == 0 || j.total > 0 && j.size != j.total {
		if parked {
			return "", errParkedBody
		}
		return "", io.ErrUnexpectedEOF
	}
	if j.diskErr == nil {
		if err := pluginaudio.ValidateMedia(j.f, j.size); err != nil {
			return "", err
		}
	}
	closeBody()
	timer.Stop()
	j.digest = hex.EncodeToString(hash.Sum(nil))
	// A song shorter than five seconds can already be complete. It only
	// becomes a recent or downloaded asset when someone explicitly uses it.
	if err := j.waitPromotion(ctx); err != nil {
		return "", err
	}
	return j.digest, nil
}

func (j *transfer) waitPromotion(ctx context.Context) error {
	s := j.store
	for {
		s.mu.Lock()
		if !j.warmed && s.prefetch == j {
			j.warmed = true
			j.signal()
		}
		promoted := s.prefetch != j || j.played || j.pinned
		changed := j.changed
		s.mu.Unlock()
		if promoted {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type reader struct {
	j      *transfer
	ctx    context.Context
	offset int64
	closed bool
}

func (r *reader) Read(b []byte) (int, error) {
	j := r.j
	s := j.store
	for {
		s.mu.Lock()
		if r.closed {
			s.mu.Unlock()
			return 0, os.ErrClosed
		}
		if err := r.ctx.Err(); err != nil {
			s.mu.Unlock()
			return 0, err
		}
		if len(b) == 0 {
			s.mu.Unlock()
			return 0, nil
		}
		if r.offset < j.size {
			var n int
			var err error
			if r.offset < j.fileBytes {
				n, err = j.f.ReadAt(b[:min(int64(len(b)), j.fileBytes-r.offset)], r.offset)
			} else {
				n = copy(b, j.tail[r.offset-j.fileBytes:])
			}
			r.offset += int64(n)
			s.mu.Unlock()
			return n, err
		}
		if j.ended {
			err := j.err
			s.mu.Unlock()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		changed := j.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-s.ctx.Done():
			return 0, s.ctx.Err()
		}
	}
}

func (r *reader) Close() error {
	j := r.j
	j.store.mu.Lock()
	defer j.store.mu.Unlock()
	if !r.closed {
		r.closed = true
		j.readers--
		j.signal()
		if j.readers == 0 && !j.played && !j.pinned && j.store.prefetch != j && !j.ended {
			j.cancel()
			if !j.running {
				j.store.finish(j, context.Canceled, nil)
			}
		}
		j.cleanup()
	}
	return nil
}
