package winshell

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateURLAllowlist(t *testing.T) {
	valid := []string{
		"https://github.com/user/repo",
		"http://localhost:3000/dashboard",
		"http://192.168.1.10:8080",
		"steam://rungameid/2358720",
		"com.epicgames.launcher://apps/test",
	}
	for _, u := range valid {
		if _, err := ValidateURL(u); err != nil {
			t.Errorf("ValidateURL(%q) unexpected error: %v", u, err)
		}
	}
	invalid := []string{
		"",
		"javascript:alert(1)",
		"file:///C:/Windows/System32/cmd.exe",
		"ms-msdt:/id",
		"https://",
		"http://example.com\r\nInjected",
	}
	for _, u := range invalid {
		if _, err := ValidateURL(u); err == nil {
			t.Errorf("ValidateURL(%q) expected error, got nil", u)
		}
	}
}

func TestImportURLShortcutExtractsProtocolAndName(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	shortcutPath := filepath.Join(dir, "Black Myth Wukong.url")
	content := "[InternetShortcut]\r\nURL=steam://rungameid/2358720\r\nIconIndex=0\r\n"
	if err := os.WriteFile(shortcutPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := ImportURLShortcut(shortcutPath, cacheDir)
	if err != nil {
		t.Fatalf("ImportURLShortcut failed: %v", err)
	}
	if imported.Name != "Black Myth Wukong" || imported.URL != "steam://rungameid/2358720" {
		t.Fatalf("unexpected imported shortcut: %+v", imported)
	}
}

func TestFetchFaviconDirectIntranetAndCache(t *testing.T) {
	// Generate a small 16x16 blue PNG icon served by a local intranet-like server.
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 30, G: 120, B: 240, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><link rel="icon" href="/assets/logo.png"></head></html>`))
		case "/assets/logo.png":
			hits++
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBuf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	img := FetchFavicon(srv.URL+"/app", cacheDir, false)
	if img == nil {
		t.Fatal("expected favicon image from local server")
	}
	if hits != 1 {
		t.Fatalf("expected 1 fetch hit, got %d", hits)
	}
	// Second call must hit disk cache without another HTTP request.
	img2 := FetchFavicon(srv.URL+"/app", cacheDir, false)
	if img2 == nil || hits != 1 {
		t.Fatalf("expected cached icon with hits=1, got img=%v hits=%d", img2 != nil, hits)
	}
	// Missing icon on dead route returns nil cleanly (triggers colored monogram fallback).
	if miss := FetchFavicon(srv.URL+"/404-no-icon", cacheDir, true); miss != nil {
		t.Fatalf("expected nil on missing favicon, got %v", miss)
	}
	ClearFaviconCache(srv.URL+"/app", cacheDir)
}

func TestDetectToolsIncludesBuiltins(t *testing.T) {
	tools := DetectTools()
	hasExplorer, hasTerminal := false, false
	for _, tool := range tools {
		if tool.Tool.ID == "explorer" {
			hasExplorer = true
		}
		if tool.Tool.ID == "terminal" {
			hasTerminal = true
		}
		if strings.TrimSpace(tool.Tool.Name) == "" {
			t.Fatalf("empty tool name: %+v", tool)
		}
	}
	if !hasExplorer || !hasTerminal {
		t.Fatalf("expected explorer and terminal builtins, got %+v", tools)
	}
}
