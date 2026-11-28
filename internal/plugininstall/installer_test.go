package plugininstall

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const luaManifest = `{"id":"example.counter","name":"Counter","version":"1.0.0","schemaVersion":1,"hostApiVersion":1,"runtime":"lua","entry":"plugin.lua"}`

type zipEntry struct {
	name, contents string
	mode           os.FileMode
}

func createZip(t *testing.T, files ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetMode(file.mode)
		entry, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(file.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testInstaller(t *testing.T, handler http.HandlerFunc) Installer {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	transport.Proxy = nil
	transport.TLSClientConfig.ServerName = server.Listener.Addr().(*net.TCPAddr).IP.String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return Installer{CacheDir: t.TempDir(), HostVersion: "1.0.0", Client: &http.Client{Transport: transport}}
}

func assertCode(t *testing.T, err error, expected ErrorCode) *Error {
	t.Helper()
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Code != expected {
		t.Fatalf("expected %s error, got %v", expected, err)
	}
	return installErr
}

func TestParseSource(t *testing.T) {
	tests := []struct {
		input              string
		kind               SourceKind
		ref, subdir, asset string
		latestOnly         bool
	}{
		{"OWNER/Repo", KindRepoLatest, "", "", "", false},
		{"https://GitHub.com/OWNER/Repo/?tab=readme", KindRepoLatest, "", "", "", false},
		{"https://github.com/owner/repo/releases/latest", KindRepoLatest, "", "", "", true},
		{"https://github.com/owner/repo/releases/tag/nightly/v1", KindReleaseTag, "nightly/v1", "", "", false},
		{"https://github.com/owner/repo/releases/download/nightly/v1/counter%20bundle.zip", KindDirectAsset, "nightly/v1", "", "counter bundle.zip", false},
		{"https://github.com/owner/repo/tree/main", KindTreeArchive, "main", "", "", false},
		{"https://github.com/owner/repo/tree/main/examples/lua-widget", KindTreeArchive, "main", "examples/lua-widget", "", false},
		{"https://github.com/owner/repo/tree/feature%2Fone/examples/my%20plugin", KindTreeArchive, "feature/one", "examples/my plugin", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			spec, err := ParseSource(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if spec.Owner != "owner" || spec.Repo != "repo" || spec.Kind != tc.kind || spec.Ref != tc.ref ||
				spec.Subdir != tc.subdir || spec.Asset != tc.asset || spec.latestOnly != tc.latestOnly {
				t.Fatalf("unexpected source %+v", spec)
			}
			if roundTrip, err := ParseSource(spec.URL); err != nil || roundTrip != spec {
				t.Fatalf("normalized link did not round trip: %+v %v", roundTrip, err)
			}
		})
	}
	for _, input := range []string{
		"http://github.com/owner/repo", "https://github.com:443/owner/repo", "https://github.com:/owner/repo",
		"https://user@github.com/owner/repo", "https://github.com.invalid/owner/repo",
		"https://github.com/owner/repo/releases/download/v1/counter.exe", "https://github.com/owner/repo/tree/main/%2E%2E",
		"https://github.com/owner/repo/tree/main/foo%2Fbar", "owner/repo/extra", "https://github.com/owner/repo/tree/main/CON.txt",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := ParseSource(input)
			assertCode(t, err, ErrorSource)
		})
	}
}

func TestStageLatestReleaseSelectsPluginAssetAndChecksHash(t *testing.T) {
	archive := createZip(t, zipEntry{"manifest.json", luaManifest, 0}, zipEntry{"plugin.lua", "return {}", 0})
	installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.Host + req.URL.Path {
		case "api.github.com/repos/owner/repo/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v1.0.0","assets":[{"name":"other.zip"},{"name":"probe-windows-amd64.zip"},{"name":"probe.qcplugin.zip"},{"name":"probe.qcplugin.zip.sha256"}]}`)
		case "github.com/owner/repo/releases/download/v1.0.0/probe.qcplugin.zip.sha256":
			fmt.Fprintf(w, "%x  probe.qcplugin.zip\n", sha256.Sum256(archive))
		case "github.com/owner/repo/releases/download/v1.0.0/probe.qcplugin.zip":
			http.Redirect(w, req, "https://release-assets.githubusercontent.com/releases/asset.zip", http.StatusFound)
		case "release-assets.githubusercontent.com/releases/asset.zip":
			w.Write(archive)
		default:
			t.Errorf("unexpected request: %s %s", req.Host, req.URL)
			http.NotFound(w, req)
		}
	})
	staged, err := installer.Stage(t.Context(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if staged.Package.Manifest.ID != "example.counter" || staged.RefOrTag != "v1.0.0" ||
		staged.AssetName != "probe.qcplugin.zip" || staged.Source.URL != "https://github.com/owner/repo" {
		t.Fatalf("unexpected stage %+v", staged)
	}
	if content, err := os.ReadFile(staged.Package.Entry); err != nil || string(content) != "return {}" {
		t.Fatalf("staged script %q: %v", content, err)
	}
	staged.Cleanup()
	if entries, err := os.ReadDir(installer.CacheDir); err != nil || len(entries) != 0 {
		t.Fatalf("cache after cleanup: %v %v", entries, err)
	}
}

func TestStageGitHubSourceFallback(t *testing.T) {
	wrapped := createZip(t, zipEntry{"repo-main/manifest.json", luaManifest, 0}, zipEntry{"repo-main/plugin.lua", "return {}", 0})
	subdir := createZip(t,
		zipEntry{"repo-main/README.md", "other file", 0},
		zipEntry{"repo-main/examples/other/manifest.json", "other manifest", 0},
		zipEntry{"repo-main/examples/other/not-plugin.txt", "outside selected plugin", 0},
		zipEntry{"repo-main/examples/lua-widget/manifest.json", luaManifest, 0},
		zipEntry{"repo-main/examples/lua-widget/plugin.lua", "return {}", 0},
	)
	t.Run("repository default branch", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			switch req.Host + req.URL.Path {
			case "api.github.com/repos/owner/repo/releases/latest":
				http.NotFound(w, req)
			case "github.com/owner/repo/archive/HEAD.zip":
				w.Write(wrapped)
			default:
				t.Errorf("unexpected request: %s %s", req.Host, req.URL)
				http.NotFound(w, req)
			}
		})
		staged, err := installer.Stage(t.Context(), "owner/repo")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(staged.Cleanup)
		if staged.RefOrTag != "HEAD" || staged.AssetName != "" {
			t.Fatalf("unexpected archive metadata: %+v", staged)
		}
		_, err = installer.Stage(t.Context(), "https://github.com/owner/repo/releases/latest")
		assertCode(t, err, ErrorRelease)
	})
	t.Run("monorepo", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			if req.Host != "github.com" || req.URL.Path != "/owner/repo/archive/refs/heads/main.zip" {
				t.Errorf("unexpected request: %s %s", req.Host, req.URL)
				http.NotFound(w, req)
				return
			}
			w.Write(subdir)
		})
		staged, err := installer.Stage(t.Context(), "https://github.com/owner/repo/tree/main/examples/lua-widget")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(staged.Cleanup)
		files, err := os.ReadDir(staged.Package.Dir)
		if err != nil || len(files) != 2 || staged.RefOrTag != "main" {
			t.Fatalf("selected package files %v, stage %+v: %v", files, staged, err)
		}
	})
	t.Run("tree with unescaped slash in branch", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			switch req.Host + req.URL.Path {
			case "github.com/owner/repo/archive/refs/heads/feature.zip", "github.com/owner/repo/archive/refs/tags/feature.zip":
				http.NotFound(w, req)
			case "github.com/owner/repo/archive/refs/heads/feature/two.zip":
				w.Write(subdir)
			default:
				t.Errorf("unexpected request: %s %s", req.Host, req.URL)
				http.NotFound(w, req)
			}
		})
		staged, err := installer.Stage(t.Context(), "https://github.com/owner/repo/tree/feature/two/examples/lua-widget")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(staged.Cleanup)
		if staged.RefOrTag != "feature/two" || staged.Source.Subdir != "examples/lua-widget" {
			t.Fatalf("unexpected resolved tree: %+v", staged)
		}
	})
	t.Run("tagged release with no attachments", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			switch req.Host + req.URL.Path {
			case "api.github.com/repos/owner/repo/releases/tags/v1":
				fmt.Fprint(w, `{"tag_name":"v1","assets":[]}`)
			case "github.com/owner/repo/archive/refs/tags/v1.zip":
				w.Write(wrapped)
			default:
				t.Errorf("unexpected request: %s %s", req.Host, req.URL)
				http.NotFound(w, req)
			}
		})
		staged, err := installer.Stage(t.Context(), "https://github.com/owner/repo/releases/tag/v1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(staged.Cleanup)
	})
}

func TestStageRejectsUnsupportedSourceAndHostVersion(t *testing.T) {
	native := `{"id":"example.counter","name":"Counter","version":"1.0.0","schemaVersion":1,"hostApiVersion":1,"runtime":"dll","entry":"counter.dll","platform":"windows/amd64","abiVersion":1}`
	newer := strings.TrimSuffix(luaManifest, "}") + `,"minHostVersion":"1.1.0"}`
	for _, tc := range []struct {
		name, url, version string
		code               ErrorCode
		entries            []zipEntry
	}{
		{"native source missing entry", "tree/main", "1.0.0", ErrorNativeSource, []zipEntry{{"manifest.json", native, 0}}},
		{"native source with entry", "tree/main", "1.0.0", ErrorNativeSource, []zipEntry{{"manifest.json", native, 0}, {"counter.dll", "DLL", 0}}},
		{"web assets in source", "tree/main", "1.0.0", ErrorWebSource, []zipEntry{{"manifest.json", luaManifest, 0}, {"plugin.lua", "return {}", 0}, {"web/index.html", "<html>", 0}}},
		{"reserved ID", "tree/main", "1.0.0", ErrorReservedID, []zipEntry{{"manifest.json", strings.Replace(luaManifest, "example.counter", "quailcrate.counter", 1), 0}}},
		{"DOS device ID", "tree/main", "1.0.0", ErrorPackage, []zipEntry{{"manifest.json", strings.Replace(luaManifest, "example.counter", "con.counter", 1), 0}, {"plugin.lua", "return {}", 0}}},
		{"newer host required", "releases/download/v1/plugin.zip", "1.0.0", ErrorHostVersion, []zipEntry{{"manifest.json", newer, 0}, {"plugin.lua", "return {}", 0}}},
		{"dev host bypasses minimum", "releases/download/v1/plugin.zip", "0.0.0-dev", "", []zipEntry{{"manifest.json", newer, 0}, {"plugin.lua", "return {}", 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := createZip(t, tc.entries...)
			installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.Host + req.URL.Path {
				case "github.com/owner/repo/archive/refs/heads/main.zip", "github.com/owner/repo/releases/download/v1/plugin.zip":
					w.Write(archive)
				default:
					t.Errorf("unexpected request: %s %s", req.Host, req.URL)
					http.NotFound(w, req)
				}
			})
			installer.HostVersion = tc.version
			staged, err := installer.Stage(t.Context(), "https://github.com/owner/repo/"+tc.url)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			} else {
				t.Cleanup(staged.Cleanup)
			}
		})
	}
}

func TestStageReportsGitHubRateLimitAndAmbiguousAssets(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := installer.Stage(t.Context(), "owner/repo")
		assertCode(t, err, ErrorRateLimit)
	})
	t.Run("ambiguous", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			fmt.Fprint(w, `{"tag_name":"v1","assets":[{"name":"x.zip"},{"name":"b.qcplugin.zip"},{"name":"a.qcplugin.zip"}]}`)
		})
		_, err := installer.Stage(t.Context(), "owner/repo")
		if detail := assertCode(t, err, ErrorAmbiguousAssets).Detail; detail != "a.qcplugin.zip, b.qcplugin.zip" {
			t.Fatalf("unexpected ambiguity options %q", detail)
		}
	})
}

func TestStageRejectsBadChecksumAndOversizedDownload(t *testing.T) {
	t.Run("checksum mismatch", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			switch req.Host + req.URL.Path {
			case "api.github.com/repos/owner/repo/releases/latest":
				fmt.Fprint(w, `{"tag_name":"v1","assets":[{"name":"plugin.zip"},{"name":"plugin.zip.sha256"}]}`)
			case "github.com/owner/repo/releases/download/v1/plugin.zip.sha256":
				fmt.Fprint(w, strings.Repeat("0", 64))
			case "github.com/owner/repo/releases/download/v1/plugin.zip":
				fmt.Fprint(w, "downloaded content")
			default:
				t.Errorf("unexpected request: %s %s", req.Host, req.URL)
				http.NotFound(w, req)
			}
		})
		_, err := installer.Stage(t.Context(), "owner/repo")
		assertCode(t, err, ErrorChecksum)
		if files, err := os.ReadDir(installer.CacheDir); err != nil || len(files) != 0 {
			t.Fatalf("leftover staging files %v: %v", files, err)
		}
	})
	t.Run("oversized download", func(t *testing.T) {
		installer := testInstaller(t, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(downloadLimit+1))
			w.WriteHeader(http.StatusOK)
		})
		_, err := installer.Stage(t.Context(), "https://github.com/owner/repo/releases/download/v1/plugin.zip")
		assertCode(t, err, ErrorDownload)
	})
}

func TestGitHubRedirectPolicy(t *testing.T) {
	client := githubClient(nil)
	for _, raw := range []string{
		"http://github.com/", "https://name@github.com/", "https://github.com:443/",
		"https://github.com:/", "https://github.com./", "https://github.com.invalid/",
	} {
		address, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.CheckRedirect(&http.Request{URL: address}, nil); err == nil {
			t.Errorf("accepted invalid redirect to %s", raw)
		}
	}
	request, err := http.NewRequest(http.MethodGet, "https://codeload.github.com/owner/repo/zip/HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, make([]*http.Request, 5)); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, make([]*http.Request, 6)); err == nil {
		t.Fatal("sixth redirect was accepted")
	}
}

func TestArchiveValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []zipEntry
		limit int64
		code  ErrorCode
	}{
		{"flat", []zipEntry{{"manifest.json", "{}", 0}}, 2, ""},
		{"wrapped with metadata", []zipEntry{{".DS_Store", "ignored", 0}, {"__MACOSX/foo", "ignored", 0}, {"repo/", "", os.ModeDir}, {"repo/manifest.json", "{}", 0}}, 2, ""},
		{"zip slip", []zipEntry{{"../escape.txt", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"absolute path", []zipEntry{{"/absolute.txt", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"drive path", []zipEntry{{"C:/entry.txt", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"backslash", []zipEntry{{"subdir\\entry.txt", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"DOS path", []zipEntry{{"COM¹.txt", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"trailing space alias", []zipEntry{{"plugin.lua ", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"link", []zipEntry{{"symlink", "elsewhere", os.ModeSymlink}}, maxExtractedBytes, ErrorArchive},
		{"special file", []zipEntry{{"fifo", "", os.ModeNamedPipe}}, maxExtractedBytes, ErrorArchive},
		{"case mismatch aliases directory", []zipEntry{{"Subdir/one", "", 0}, {"subdir/two", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"file as parent", []zipEntry{{"subdir", "", 0}, {"subdir/file", "", 0}}, maxExtractedBytes, ErrorArchive},
		{"missing manifest", []zipEntry{{"repo/deep/manifest.json", "{}", 0}}, maxExtractedBytes, ErrorManifestMissing},
		{"multiple manifests", []zipEntry{{"repo/one/manifest.json", "{}", 0}, {"repo/two/manifest.json", "{}", 0}}, maxExtractedBytes, ErrorManifestAmbiguous},
		{"oversized decompressed file", []zipEntry{{"manifest.json", strings.Repeat("a", 8192), 0}}, 64, ErrorArchive},
		{"cumulative decompression limit", []zipEntry{{"manifest.json", "{}", 0}, {"one", "one", 0}, {"two", "two", 0}}, 7, ErrorArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "package.zip")
			if err := os.WriteFile(archive, createZip(t, tc.files...), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "extract")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			err := extractArchive(t.Context(), archive, destination, "", tc.limit)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			} else if manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json")); err != nil || string(manifest) != "{}" {
				t.Fatalf("extracted manifest %q: %v", manifest, err)
			}
		})
	}
}

func TestArchiveFileCountLimit(t *testing.T) {
	var files []zipEntry
	for i := range maxArchiveFiles + 1 {
		files = append(files, zipEntry{name: fmt.Sprintf("file-%d", i)})
	}
	archive := filepath.Join(t.TempDir(), "over-limit.zip")
	if err := os.WriteFile(archive, createZip(t, files...), 0600); err != nil {
		t.Fatal(err)
	}
	err := extractArchive(t.Context(), archive, t.TempDir(), "", maxExtractedBytes)
	assertCode(t, err, ErrorArchive)
}

func TestCommitSwapRestoresOldPackageOnFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "installed", "example.counter")
	stage := filepath.Join(root, "staging", "new")
	writeEntry := func(dir, content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "entry.lua"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	checkEntry := func(dir, expected string) {
		t.Helper()
		if got, err := os.ReadFile(filepath.Join(dir, "entry.lua")); err != nil || string(got) != expected {
			t.Fatalf("%s entry %q, want %q: %v", dir, got, expected, err)
		}
	}
	writeEntry(target, "old")
	writeEntry(target+".bak", "separate plugin")
	if err := os.MkdirAll(filepath.Dir(stage), 0700); err != nil {
		t.Fatal(err)
	}
	assertCode(t, CommitSwap(target, stage), ErrorFilesystem)
	checkEntry(target, "old")

	writeEntry(stage, "new")
	commitErr := errors.New("database write failed")
	err := CommitSwap(target, stage, func() error {
		checkEntry(target, "new")
		return commitErr
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("metadata callback error was lost: %v", err)
	}
	checkEntry(target, "old")
	checkEntry(stage, "new")

	if err := CommitSwap(target, stage); err != nil {
		t.Fatal(err)
	}
	checkEntry(target, "new")
	checkEntry(target+".bak", "separate plugin")
	if leftovers, err := os.ReadDir(filepath.Dir(stage)); err != nil || len(leftovers) != 0 {
		t.Fatalf("unexpected staging leftovers %v: %v", leftovers, err)
	}
}
