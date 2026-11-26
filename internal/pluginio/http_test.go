package pluginio

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAddressesAndHeaders(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "::1", "::ffff:10.2.3.4", "100.64.0.1", "169.254.169.254", "fd00::1", "2002:7f00:1::1", "64:ff9b::7f00:1", "0.0.0.0", "192.0.2.12", "224.0.0.1"} {
		if permittedIP(netip.MustParseAddr(addr), false) {
			t.Errorf("accepted %s", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !permittedIP(netip.MustParseAddr(addr), false) {
			t.Errorf("denied %s", addr)
		}
	}
	// Transparent TUN Fake-IP ranges are allowed ONLY when resolved from a real public ICANN domain.
	for _, addr := range []string{"198.18.1.213", "2001:2::206"} {
		ip := netip.MustParseAddr(addr)
		if permittedIP(ip, false) {
			t.Errorf("permittedIP should still reject raw fake-IP %s", addr)
		}
		if !permittedResolvedIP("passport.bilibili.com", ip, false) {
			t.Errorf("permittedResolvedIP denied public domain fake-IP %s", addr)
		}
		if permittedResolvedIP(addr, ip, false) {
			t.Errorf("permittedResolvedIP allowed raw IP literal %s", addr)
		}
		if permittedResolvedIP("nas.local", ip, false) || permittedResolvedIP("localhost", ip, false) {
			t.Errorf("permittedResolvedIP allowed non-public host for fake-IP %s", addr)
		}
	}
	// DNS rebinding of a public domain to loopback / RFC1918 / metadata / ULA must remain denied.
	for _, addr := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "fd00::1", "::1"} {
		if permittedResolvedIP("passport.bilibili.com", netip.MustParseAddr(addr), false) {
			t.Errorf("permittedResolvedIP allowed DNS rebinding to %s", addr)
		}
	}
	for _, k := range []string{"Host", "Cookie", "Connection", "content-length", "Proxy-Authorization", "accept-encoding", "X-Forwarded-Host"} {
		r := Request{URL: "https://example.org", Headers: map[string]string{k: "sample"}}
		if !errors.Is(r.validate(), ErrInvalid) {
			t.Errorf("accepted %s", k)
		}
	}
}

func TestRequestPolicies(t *testing.T) {
	end := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Private") != "" || r.Referer() != "" {
			t.Error("sent private headers across origins")
		}
		w.Header().Set("Set-Cookie", "test=private")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer end.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, end.URL, http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/big":
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			io.WriteString(gz, strings.Repeat("a", maxBody+1))
			gz.Close()
		}
	}))
	defer start.Close()
	s := New(t.TempDir())
	ctx := t.Context()
	i := s.Instance(ctx, "one", false)
	if _, err := i.fetch(ctx, Request{URL: start.URL}); !errors.Is(err, ErrDenied) {
		t.Fatalf("local access: %v", err)
	}
	i = s.Instance(ctx, "one", true)
	res, err := i.fetch(ctx, Request{URL: start.URL + "/redirect", Headers: map[string]string{"Authorization": "private", "X-Private": "private"}, ResponseType: "json"})
	if err != nil || string(res.JSON) != `{"ok":true}` || res.Status != http.StatusOK || res.Headers.Get("Set-Cookie") != "" {
		t.Fatalf("redirected response %#v, %v", res, err)
	}
	for _, p := range []string{"/loop", "/big"} {
		if _, err := i.fetch(ctx, Request{URL: start.URL + p}); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
}

func TestCrossOriginRedirectWithEmptyBody(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("unexpected method or credentials on redirected request")
		}
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer start.Close()
	i := New(t.TempDir()).Instance(t.Context(), "one", true)
	result, err := i.fetch(t.Context(), Request{URL: start.URL, Headers: map[string]string{"Authorization": "test-only"}})
	if err != nil || result.Body != "ok" {
		t.Fatalf("GET redirect: %v", err)
	}
	_, err = i.fetch(t.Context(), Request{Method: http.MethodPost, URL: start.URL, Body: "test-only"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("redirect with a body: %v", err)
	}
}

func TestStreamRangeAndCancellation(t *testing.T) {
	blocked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			blocked <- struct{}{}
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") != "bytes=3-5" {
			t.Error("lost Range")
		}
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "345")
	}))
	defer srv.Close()
	ctx := t.Context()
	i := New(t.TempDir()).Instance(ctx, "one", true)
	resp, err := i.Stream(ctx, Request{URL: srv.URL, Headers: map[string]string{"Range": "bytes=3-5"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 206 || string(b) != "345" {
		t.Fatal("invalid range response")
	}
	scope, closePage := context.WithCancel(ctx)
	ch := make(chan error, 1)
	if err := i.RequestContext(scope, "request", Request{URL: srv.URL + "/block"}, func(_ Result, err error) { ch <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("request never started")
	}
	if err := i.Request("request", Request{URL: srv.URL}, func(Result, error) {}); !errors.Is(err, ErrBusy) {
		t.Fatal("duplicate request admitted")
	}
	closePage()
	select {
	case err := <-ch:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete")
	}
}

func TestTimerScopeAndReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	i := New(t.TempDir()).Instance(ctx, "one", false)
	got := make(chan string, 3)
	if err := i.After("poll", 15*time.Millisecond, func() { got <- "stale" }); err != nil {
		t.Fatal(err)
	}
	if !i.CancelTimer("poll") {
		t.Fatal("cancel failed")
	}
	if err := i.After("poll", 20*time.Millisecond, func() { got <- "fresh" }); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "fresh" {
			t.Fatal(v)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not fire")
	}
	scope, closePage := context.WithCancel(ctx)
	if err := i.AfterContext(scope, "page", 15*time.Millisecond, func() { got <- "page" }); err != nil {
		t.Fatal(err)
	}
	closePage()
	if err := i.After("other", 15*time.Millisecond, func() { got <- "instance" }); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case v := <-got:
		t.Fatal(v)
	case <-time.After(50 * time.Millisecond):
	}
}
