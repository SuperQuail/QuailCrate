package pluginio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenMediaStitchesValidatedRanges(t *testing.T) {
	data := bytes.Repeat([]byte("track"), int(mediaRangeSize)/5+200)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		lo, hi, err := requestedRange(r.Header.Get("Range"))
		if err != nil || lo < 0 || hi >= lo+mediaRangeSize || r.Header.Get("Referer") != "https://example.org/player" {
			t.Error("bad media request")
			w.WriteHeader(400)
			return
		}
		if n > 1 && r.Header.Get("If-Range") != `"version-1"` {
			t.Error("missing range validator")
		}
		hi = min(hi, int64(len(data)-1))
		w.Header().Set("ETag", `"version-1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, hi, len(data)))
		w.WriteHeader(206)
		w.Write(data[lo : hi+1])
	}))
	defer s.Close()
	i := New(t.TempDir()).Instance(t.Context(), "audio", true)
	body, err := i.OpenMedia(t.Context(), Request{URL: s.URL, Headers: map[string]string{"Referer": "https://example.org/player"}})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, data) || calls.Load() != 2 {
		t.Fatalf("bytes=%d calls=%d err=%v", len(got), calls.Load(), err)
	}
}

func TestOpenMediaIgnoresUnsupportedRangeOrHonorsExplicit(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/full":
			io.WriteString(w, "audio")
		case "/part":
			if r.Header.Get("Range") != "bytes=-3" {
				t.Error("caller range changed")
			}
			w.Header().Set("Content-Range", "bytes 2-4/5")
			w.WriteHeader(206)
			io.WriteString(w, "dio")
		case "/empty":
			w.Header().Set("Content-Range", "bytes */0")
			w.WriteHeader(416)
		}
	}))
	defer s.Close()
	i := New(t.TempDir()).Instance(t.Context(), "audio", true)
	for _, tt := range []struct{ path, rng, want string }{{"/full", "", "audio"}, {"/full", "bytes=0-1", "audio"}, {"/part", "bytes=-3", "dio"}, {"/empty", "", ""}} {
		r := Request{URL: s.URL + tt.path}
		if tt.rng != "" {
			r.Headers = map[string]string{"range": tt.rng}
		}
		body, err := i.OpenMedia(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(b) != tt.want {
			t.Errorf("%s: %q %v", tt.path, b, err)
		}
	}
}

func TestMediaResumeUsesOriginalValidatorAndOffset(t *testing.T) {
	const data = "abcdefghijkl"
	var changed atomic.Bool
	var ignoresRange atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag := `"first"`
		if changed.Load() {
			tag = `"second"`
		}
		w.Header().Set("ETag", tag)
		lo, hi, err := requestedRange(r.Header.Get("Range"))
		if err != nil || lo < 0 || lo >= int64(len(data)) {
			t.Errorf("unexpected Range %q", r.Header.Get("Range"))
			w.WriteHeader(416)
			return
		}
		if lo > 0 && r.Header.Get("If-Range") != `"first"` {
			t.Error("missing original validator")
		}
		if ignoresRange.Load() {
			io.WriteString(w, data)
			return
		}
		hi = min(hi, int64(len(data))-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, hi, len(data)))
		w.WriteHeader(206)
		io.WriteString(w, data[lo:hi+1])
	}))
	defer s.Close()
	i := New(t.TempDir()).Instance(t.Context(), "audio", true)
	body, err := i.OpenMedia(t.Context(), Request{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	resumer := body.(interface {
		Resume(context.Context, int64) (io.ReadCloser, error)
	})
	tail, err := resumer.Resume(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(tail)
	tail.Close()
	if err != nil || string(b) != data[3:] {
		t.Fatalf("wrong resumed body %q, %v", b, err)
	}
	changed.Store(true)
	if replacement, err := resumer.Resume(t.Context(), 3); err == nil {
		replacement.Close()
		t.Fatal("accepted a different version of the media")
	}
	changed.Store(false)
	ignoresRange.Store(true)
	if _, err := resumer.Resume(t.Context(), 3); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("expected full-source fallback when ranges are ignored, got %v", err)
	}
}

func TestOpenMediaRejectsInconsistentResponses(t *testing.T) {
	for _, scenario := range []string{"bad-first", "extra", "short", "different-total", "wrong-start", "changed-version", "lost-partial"} {
		t.Run(scenario, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				first := r.Header.Get("Range") == "bytes=0-1048575"
				rng, piece, tag, status := "bytes 0-2/6", "abc", `"v1"`, 206
				if !first {
					rng, piece = "bytes 3-5/6", "def"
				}
				switch scenario {
				case "bad-first":
					rng = "bytes 0-6/6"
				case "extra":
					piece = "abcd"
				case "short":
					piece = "ab"
				case "different-total":
					if !first {
						rng = "bytes 3-5/7"
					}
				case "wrong-start":
					if !first {
						rng = "bytes 2-4/6"
					}
				case "changed-version":
					if !first {
						tag = `"v2"`
					}
				case "lost-partial":
					if !first {
						status = 200
					}
				}
				w.Header().Set("Content-Range", rng)
				w.Header().Set("ETag", tag)
				w.WriteHeader(status)
				w.(http.Flusher).Flush() // exercise exact body counts in the absence of Content-Length
				io.WriteString(w, piece)
			}))
			defer s.Close()
			i := New(t.TempDir()).Instance(t.Context(), "audio", true)
			body, err := i.OpenMedia(t.Context(), Request{URL: s.URL})
			if err == nil {
				_, err = io.ReadAll(body)
				body.Close()
			}
			if err == nil {
				t.Fatal("accepted inconsistent response")
			}
		})
	}
}

func TestOpenMediaCancelsActiveRange(t *testing.T) {
	done := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-4/5")
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(done)
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	i := New(t.TempDir()).Instance(ctx, "audio", true)
	body, err := i.OpenMedia(ctx, Request{URL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("range not canceled after close")
	}
}
