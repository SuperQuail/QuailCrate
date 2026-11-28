//go:build windows

package pluginio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func TestProtectedSessions(t *testing.T) {
	if _, err := seal([]byte("probe"), []byte("test context")); err != nil {
		t.Fatalf("Windows user credential protection unavailable: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "token", Value: "not-stored-as-plain-text", Path: "/account", HttpOnly: true})
			http.Redirect(w, r, "/account", http.StatusFound)
		default:
			io.WriteString(w, r.Header.Get("Cookie"))
		}
	}))
	defer srv.Close()
	profile := t.TempDir()
	s := New(profile)
	ctx := t.Context()
	i := s.Instance(ctx, "one", true)
	reader := func(instance *Instance, path string) string {
		t.Helper()
		res, err := instance.fetch(ctx, Request{URL: srv.URL + path, Session: "profile"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Headers.Get("Set-Cookie") != "" {
			t.Fatal("returned raw cookies")
		}
		return res.Body
	}
	key := sessionKey{"one", "profile"}
	if got := reader(i, "/login"); got != "token=not-stored-as-plain-text" {
		t.Fatalf("Cookie missing after login redirect: %s", got)
	}
	data, err := os.ReadFile(s.path(key))
	if err != nil || bytes.Contains(data, []byte("not-stored-as-plain-text")) {
		t.Fatal("persistence failed")
	}
	if got := reader(i, "/public"); got != "" {
		t.Fatal("cookie escaped path scope")
	}
	if got := reader(s.Instance(ctx, "two", true), "/account"); got != "" {
		t.Fatal("cookie escaped plugin scope")
	}
	if got := reader(New(profile).Instance(ctx, "one", true), "/account"); got == "" {
		t.Fatal("session lost on restart")
	}
	old, err := i.getSession("profile")
	if err != nil {
		t.Fatal(err)
	}
	if err := i.ClearSession("profile"); err != nil {
		t.Fatal(err)
	}
	loginURL, _ := url.Parse(srv.URL)
	if err := old.save(context.Background(), loginURL, []*http.Cookie{{Name: "stale", Value: "x"}}); !errors.Is(err, errSession) {
		t.Fatal("revived revoked session")
	}
	if got := reader(i, "/account"); got != "" {
		t.Fatal("logout did not clear cookie")
	}
}

func TestForgetPluginSessions(t *testing.T) {
	s := New(t.TempDir())
	first := s.Instance(t.Context(), "first", false)
	other := s.Instance(t.Context(), "other", false)
	old, err := first.getSession("profile")
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := other.getSession("profile")
	if err != nil {
		t.Fatal(err)
	}
	s.ForgetPluginSessions("first")
	if err := old.save(t.Context(), nil, nil); !errors.Is(err, errSession) {
		t.Fatalf("forgotten session can still save: %v", err)
	}
	if current, err := first.getSession("profile"); err != nil || current == old {
		t.Fatalf("plugin reused a forgotten session: %v", err)
	}
	if current, err := other.getSession("profile"); err != nil || current != untouched {
		t.Fatalf("forgetting one plugin affected another plugin's session: %v", err)
	}
}
