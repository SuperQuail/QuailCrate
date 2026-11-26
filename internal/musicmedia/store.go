//go:build windows

// Package musicmedia owns the original, compressed audio used by the music
// player. Only the caller's Source ever opens a network connection.
package musicmedia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	cacheTracks = 30
	maxBytes    = 128 << 20
)

var ErrStorage = errors.New("audio storage unavailable")

type Key struct {
	Track   string `json:"track"`
	Quality string `json:"quality"`
}

type Source func(context.Context) (io.ReadCloser, error)

type Entry struct {
	Key
	Label      string `json:"label"`
	Status     string `json:"status"`
	Bytes      int64  `json:"bytes"`
	Total      int64  `json:"total"`
	Downloaded bool   `json:"downloaded"`
}

type record struct {
	Entry
	File   string `json:"file,omitempty"`
	Digest string `json:"digest,omitempty"`
	Used   int64  `json:"used"`
}

type Store struct {
	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	cacheDir         string
	downloadDir      string
	downloadIndexErr error
	cache            map[string]record
	downloads        map[string]record
	jobs             map[Key]*transfer
	foreground       *transfer
	prefetch         *transfer
	clock            int64
	workers          sync.WaitGroup
	active           int
}

func New(ctx context.Context, cacheDir, downloadDir string) *Store {
	ctx, cancel := context.WithCancel(ctx)
	s := &Store{ctx: ctx, cancel: cancel, cacheDir: cacheDir, downloadDir: downloadDir,
		cache: make(map[string]record), downloads: make(map[string]record), jobs: make(map[Key]*transfer)}
	s.load(cacheDir, s.cache, false)
	s.load(downloadDir, s.downloads, true)
	s.trim()
	return s
}

func (s *Store) Close() {
	s.mu.Lock()
	s.cancel()
	for _, j := range s.jobs {
		if !j.running {
			s.finish(j, context.Canceled, nil)
		} else {
			j.signal()
		}
	}
	s.mu.Unlock()
	s.workers.Wait()
}

func validKey(k Key) bool {
	return k.Track != "" && len(k.Track) <= 512 && len(k.Quality) <= 128
}

func audioName(r record) bool {
	return filepath.Base(r.File) == r.File && len(r.File) > 6 && r.File[:6] == "audio-" && len(r.Digest) == 64
}

func (s *Store) load(dir string, dst map[string]record, pinned bool) {
	protect := func(err error) {
		if pinned {
			s.downloadIndexErr = errors.Join(ErrStorage, err)
		}
	}
	if dir == "" {
		protect(ErrStorage)
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		protect(err)
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	var list []record
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &list); err != nil || list == nil {
			protect(errors.New("invalid media catalog"))
			return
		}
	case errors.Is(err, os.ErrNotExist):
		// A new directory has no index. Previously saved audio with no
		// catalog is different: preserve it for repair on a future start.
		if pinned {
			names, err := os.ReadDir(dir)
			if err != nil {
				protect(err)
				return
			}
			for _, e := range names {
				if strings.HasPrefix(e.Name(), "audio-") {
					protect(errors.New("missing download catalog"))
					return
				}
			}
		}
	default:
		protect(err)
		return
	}
	live := make(map[string]bool)
	for _, r := range list {
		if pinned {
			_, duplicate := dst[r.Track]
			if !validKey(r.Key) || duplicate || (r.Status != "ready" && r.Status != "downloading" && r.Status != "error") || (r.Status == "ready" && !audioName(r)) {
				protect(errors.New("invalid download catalog entry"))
				return
			}
		}
		if !validKey(r.Key) {
			continue
		}
		if r.Status == "ready" && audioName(r) {
			st, err := os.Lstat(filepath.Join(dir, r.File))
			if pinned && err != nil && !errors.Is(err, os.ErrNotExist) {
				protect(err)
				return
			}
			if err == nil && st.Mode().IsRegular() && st.Size() == r.Bytes && r.Bytes > 0 && r.Bytes <= maxBytes {
				live[r.File] = true
				dst[r.Track] = r
				s.clock = max(s.clock, r.Used)
				continue
			}
		}
		if pinned {
			r.Status, r.Downloaded, r.Digest, r.File = "error", false, "", ""
			dst[r.Track] = r
		}
	}
	// All files in these two directories are private to this owner. Names
	// created before a failed commit are never reusable.
	names, _ := os.ReadDir(dir)
	for _, e := range names {
		name := e.Name()
		if !e.IsDir() && !live[name] && (strings.HasPrefix(name, "audio-") || strings.HasPrefix(name, "work-") || strings.HasPrefix(name, "index-")) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func save(dir string, values map[string]record) error {
	if dir == "" {
		return ErrStorage
	}
	list := make([]record, 0, len(values))
	for _, r := range values {
		list = append(list, r)
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "index-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, "index.json"))
	}
	return err
}

func (s *Store) tick() int64 {
	s.clock = max(time.Now().UnixNano(), s.clock+1)
	return s.clock
}

func (s *Store) trim() {
	for len(s.cache) > cacheTracks {
		var oldest record
		for _, r := range s.cache {
			if oldest.Track == "" || r.Used < oldest.Used {
				oldest = r
			}
		}
		delete(s.cache, oldest.Track)
		removeFile(s.cacheDir, oldest)
	}
}

func removeFile(dir string, r record) {
	if audioName(r) {
		_ = os.Remove(filepath.Join(dir, r.File))
	}
}

func (s *Store) complete(key Key) (record, string, bool) {
	if r, ok := s.downloads[key.Track]; ok && r.Status == "ready" {
		return r, s.downloadDir, true
	}
	if r, ok := s.cache[key.Track]; ok && r.Quality == key.Quality && r.Status == "ready" {
		return r, s.cacheDir, true
	}
	return record{}, "", false
}

// Lookup also reports an in-flight transfer, including the currently reserved
// prefix. Its body can be attached without waiting for a second resolution.
func (s *Store) Lookup(key Key) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, _, ok := s.complete(key); ok {
		return r.Entry, true
	}
	if j := s.jobs[key]; j != nil && !j.ended && j.ctx.Err() == nil {
		return j.entry(), true
	}
	return Entry{}, false
}

func (s *Store) Downloads() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]record, 0, len(s.downloads))
	for _, r := range s.downloads {
		if j := s.jobs[r.Key]; j != nil && j.pinned && r.Status == "downloading" {
			r.Entry = j.entry()
		}
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Used > list[j].Used })
	result := make([]Entry, 0, len(list))
	for _, r := range list {
		result = append(result, r.Entry)
	}
	return result
}

func (s *Store) RemoveDownload(track string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.downloadIndexErr != nil {
		return s.downloadIndexErr
	}
	r, ok := s.downloads[track]
	if !ok {
		return nil
	}
	delete(s.downloads, track)
	if err := save(s.downloadDir, s.downloads); err != nil {
		s.downloads[track] = r
		return err
	}
	if j := s.jobs[r.Key]; j != nil {
		j.pinned = false
		if !j.played {
			j.cancel()
			if !j.running {
				s.finish(j, context.Canceled, nil)
			}
		}
		j.signal()
	}
	removeFile(s.downloadDir, r)
	return nil
}

// Invalidate discards a corrupt asset so the caller can retry online once.
func (s *Store) Invalidate(key Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidate(key)
	if j := s.jobs[key]; j != nil {
		j.cancel()
		j.signal()
		if !j.running {
			s.finish(j, context.Canceled, nil)
		}
	}
}

// RejectPlayback forgets a failing playback source without removing a user's
// saved download. Disk integrity is checked independently by Open.
func (s *Store) RejectPlayback(key Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.cache[key.Track]; ok && r.Quality == key.Quality {
		delete(s.cache, key.Track)
		_ = save(s.cacheDir, s.cache)
		removeFile(s.cacheDir, r)
	}
	if j := s.jobs[key]; j != nil && !j.pinned {
		j.cancel()
		j.signal()
		if !j.running {
			s.finish(j, context.Canceled, nil)
		}
	}
}

func (s *Store) invalidate(key Key) {
	if r, ok := s.cache[key.Track]; ok && r.Quality == key.Quality {
		delete(s.cache, key.Track)
		_ = save(s.cacheDir, s.cache)
		removeFile(s.cacheDir, r)
	}
	if r, ok := s.downloads[key.Track]; ok && r.Status == "ready" && s.downloadIndexErr == nil {
		s.downloads[key.Track] = record{Entry: Entry{Key: key, Label: r.Label, Status: "error"}, Used: r.Used}
		_ = save(s.downloadDir, s.downloads)
		removeFile(s.downloadDir, r)
	}
}

// checkedFile catches same-size corruption too. Hash before handing the
// decoder its first byte so a broken local file can fall back to the CDN.
func checkedFile(dir string, r record) (*os.File, error) {
	f, err := os.Open(filepath.Join(dir, r.File))
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(f, maxBytes+1))
	if err == nil && (n != r.Bytes || hex.EncodeToString(sum.Sum(nil)) != r.Digest) {
		err = errors.New("invalid stored audio")
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// install publishes a complete asset. Linking on the same volume avoids
// copying while playing or promoting a cache hit into downloads.
func install(src, dir string) (string, error) {
	if dir == "" {
		return "", ErrStorage
	}
	f, err := os.CreateTemp(dir, "audio-")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err = f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err = os.Remove(name); err != nil {
		return "", err
	}
	if os.Link(src, name) == nil {
		return filepath.Base(name), nil
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return filepath.Base(name), nil
}

func (s *Store) publish(r record, src string, pinned bool) error {
	dir, entries := s.cacheDir, s.cache
	if pinned {
		if s.downloadIndexErr != nil {
			return s.downloadIndexErr
		}
		dir, entries = s.downloadDir, s.downloads
	}
	file, err := install(src, dir)
	if err != nil {
		return err
	}
	old, exists := entries[r.Track]
	r.File, r.Status, r.Downloaded = file, "ready", pinned
	entries[r.Track] = r
	if err = save(dir, entries); err != nil {
		if exists {
			entries[r.Track] = old
		} else {
			delete(entries, r.Track)
		}
		removeFile(dir, r)
		return err
	}
	removeFile(dir, old)
	if pinned {
		if stale, ok := s.cache[r.Track]; ok {
			delete(s.cache, r.Track)
			_ = save(s.cacheDir, s.cache)
			removeFile(s.cacheDir, stale)
		}
	} else if len(entries) > cacheTracks {
		s.trim()
		_ = save(dir, entries)
	}
	return nil
}

// Keep the newer request when different qualities of a track complete out of order.
func (s *Store) commit(j *transfer, digest string) error {
	if j.diskErr != nil {
		return j.diskErr
	}
	r := record{Entry: j.entry(), Digest: digest, Used: j.used}
	if j.pinned {
		if requested, ok := s.downloads[j.key.Track]; ok && requested.Key == j.key {
			return s.publish(r, j.f.Name(), true)
		}
	}
	if j.played && s.downloads[j.key.Track].Status != "ready" && s.cache[j.key.Track].Used <= j.used {
		return s.publish(r, j.f.Name(), false)
	}
	return nil
}

func (s *Store) failDownload(j *transfer) {
	if r, ok := s.downloads[j.key.Track]; ok && r.Key == j.key && r.Status == "downloading" {
		r.Status, r.Bytes, r.Total = "error", j.size, j.total
		s.downloads[r.Track] = r
		_ = save(s.downloadDir, s.downloads)
	}
}
