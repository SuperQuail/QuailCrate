package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseVersions(t *testing.T) {
	for _, test := range []struct {
		candidate, current string
		newer, eligible    bool
	}{
		{"v1.2.3-alpha.10", "1.2.3-alpha.2", true, true},
		{"1.2.3", "1.2.3-alpha.10", true, true},
		{"1.2.4-alpha.1", "1.2.3", true, false},
		{"1.2.3-beta.1", "1.2.3-alpha.2", true, false},
		{"1.2.3", "0.0.0-dev+revision", true, false},
		{"2.0.0-alpha.01", "1.2.3-alpha.1", false, false},
		{"1.2.3+second", "1.2.3+first", false, false},
		{"1.02.3", "1.0.0", false, false},
		{"1.2.3-alpha.1.0", "1.2.3-alpha.1", true, true},
	} {
		if got := Newer(test.candidate, test.current); got != test.newer {
			t.Errorf("Newer(%q, %q) = %v", test.candidate, test.current, got)
		}
		if got := CanFollow(test.candidate, test.current); got != test.eligible {
			t.Errorf("CanFollow(%q, %q) = %v", test.candidate, test.current, got)
		}
	}
}

func TestStatementAndPackageVerification(t *testing.T) {
	fixture := newUpdateFixture(t, "1.2.3", Installer)
	manifest, err := VerifyManifest(fixture.manifest, fixture.signature, fixture.key)
	if err != nil || manifest.Version != fixture.version {
		t.Fatalf("verify manifest: %#v, %v", manifest, err)
	}
	if _, err := VerifyManifest(append(bytes.Clone(fixture.manifest), ' '), fixture.signature, fixture.key); err == nil {
		t.Fatal("modified signed document was accepted")
	}
	dir := t.TempDir()
	prepared := fixture.stage(t, dir)
	if _, _, err := VerifyPrepared(prepared, fixture.key); err != nil {
		t.Fatal(err)
	}
	modified := bytes.Clone(fixture.packageData)
	modified[0] ^= 1
	if err := os.WriteFile(prepared.PackagePath, modified, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyPrepared(prepared, fixture.key); err == nil {
		t.Fatal("modified package with same size was accepted")
	}
}

func TestManualCheckThenPrepareAndRestore(t *testing.T) {
	fixture := newUpdateFixture(t, "1.2.3", Installer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings := &memorySettings{values: map[string]string{autoSetting: "false"}}
	config := Config{Version: "1.2.2", CacheDir: t.TempDir(), AssetType: Installer,
		PublicKey: fixture.key, Settings: settings, Client: fixture.client()}
	service := New(ctx, config)
	service.Start()
	deadline := time.Now().Add(3 * time.Second)
	for !service.CheckNow() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := awaitSnapshot(t, service, func(s Snapshot) bool { return s.Phase == PhaseAvailable })
	if !status.CanInstall || status.TargetVersion != fixture.version || fixture.downloads.Load() != 0 {
		t.Fatalf("manual check downloaded a file or reported the wrong update: %#v", status)
	}
	prepared, err := service.PrepareForInstall(ctx)
	if err != nil || fixture.downloads.Load() != 1 {
		t.Fatalf("prepare = %#v, %v", prepared, err)
	}
	if _, _, err := VerifyPrepared(prepared, fixture.key); err != nil {
		t.Fatal(err)
	}
	if _, automatic := service.ReadyForAutomaticInstall(); automatic {
		t.Fatal("user disabled automatic installation")
	}
	if err := service.SetAutoUpdate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if ready, automatic := service.ReadyForAutomaticInstall(); !automatic || ready != prepared {
		t.Fatalf("ready after enable = %#v, %v", ready, automatic)
	}
	cancel()

	restoreCtx, stopRestore := context.WithCancel(context.Background())
	defer stopRestore()
	config.Settings = &memorySettings{values: map[string]string{checkSetting: time.Now().UTC().Format(time.RFC3339Nano)}}
	config.Client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	restored := New(restoreCtx, config)
	restored.ReportFailure("install_busy")
	restored.Start()
	status = awaitSnapshot(t, restored, func(s Snapshot) bool { return s.TargetVersion == fixture.version })
	if !status.CanInstall || status.LastInstallErrorCode != "install_busy" {
		t.Fatalf("stored result was lost when restoring the package: %#v", status)
	}
	if ready, automatic := restored.ReadyForAutomaticInstall(); !automatic || ready != prepared {
		t.Fatalf("restored ready = %#v, %v", ready, automatic)
	}
	if New(restoreCtx, Config{Version: "0.0.0-dev+revision", PublicKey: config.PublicKey, AssetType: Installer}).Snapshot().AutoUpdate {
		t.Fatal("automatic updates enabled for a development build")
	}
}

func TestAutomaticDiscoveryAndInstallAvailability(t *testing.T) {
	for _, test := range []struct {
		name, kind, mode, want  string
		expectLink, wantPackage bool
	}{
		{"installer", Installer, Installer, PhaseReady, true, true},
		{"legacy portable", Portable, PortableManual, PhaseAvailable, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUpdateFixture(t, "1.2.3-alpha.10", test.kind)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			settings := &memorySettings{values: make(map[string]string)}
			service := New(ctx, Config{Version: "1.2.3-alpha.2", CacheDir: t.TempDir(), AssetType: test.mode,
				PublicKey: fixture.key, Settings: settings, Client: fixture.client()})
			service.Start()
			state := awaitSnapshot(t, service, func(s Snapshot) bool { return s.Phase == test.want })
			if state.CanInstall != test.wantPackage || (state.ReleaseURL != "") != test.expectLink {
				t.Fatalf("unexpected availability: %#v", state)
			}
			stored, _ := settings.Setting(ctx, checkSetting, "")
			if stored == "" {
				t.Fatal("automatic check time was not persisted")
			}
			if _, ok := service.ReadyForAutomaticInstall(); ok != test.wantPackage {
				t.Fatalf("automatic install ready = %v", ok)
			}
			if !test.wantPackage {
				if _, err := service.PrepareForInstall(ctx); !errors.Is(err, ErrUnavailable) || fixture.downloads.Load() != 0 {
					t.Fatalf("unsupported portable update attempted installation: %v", err)
				}
			}
		})
	}
}

func TestFailedDownloadsNeverBecomeReady(t *testing.T) {
	fixture := newUpdateFixture(t, "1.2.3", Installer)
	fixture.packageData[0] ^= 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := t.TempDir()
	service := New(ctx, Config{Version: "1.2.2", CacheDir: cache, AssetType: Installer,
		PublicKey: fixture.key, Client: fixture.client()})
	service.Start()
	status := awaitSnapshot(t, service, func(s Snapshot) bool { return s.Phase == PhaseFailed })
	if status.ErrorCode != ErrorIntegrity {
		t.Fatalf("malformed package status: %#v", status)
	}
	if _, ok := service.ReadyForAutomaticInstall(); ok {
		t.Fatal("failed package was marked ready")
	}
	if _, err := os.Stat(cachePaths(cache, fixture.version, Installer).PackagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed package was published to the update cache: %v", err)
	}
}

func TestOfflineCheck(t *testing.T) {
	fixture := newUpdateFixture(t, "1.2.3", Installer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := New(ctx, Config{Version: "1.2.2", CacheDir: t.TempDir(), AssetType: Installer,
		PublicKey: fixture.key, Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})}})
	service.Start()
	status := awaitSnapshot(t, service, func(s Snapshot) bool { return s.Phase == PhaseFailed })
	if status.ErrorCode != ErrorNetwork || status.CanInstall {
		t.Fatalf("offline status: %#v", status)
	}
}

type updateFixture struct {
	version, kind                    string
	key                              ed25519.PublicKey
	manifest, signature, packageData []byte
	downloads                        atomic.Int32
}

func newUpdateFixture(t *testing.T, version, kind string) *updateFixture {
	t.Helper()
	key, secret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("test update package")
	hash := sha256.Sum256(content)
	asset := Asset{Type: kind, Name: PackageName(kind), Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}
	manifest, err := json.Marshal(Manifest{Schema: 1, Version: version, Channel: Channel(version), Platform: Platform, Assets: []Asset{asset}})
	if err != nil {
		t.Fatal(err)
	}
	return &updateFixture{version: version, kind: kind, key: key, manifest: manifest, packageData: content,
		signature: []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(secret, manifest)))}
}

func (f *updateFixture) stage(t *testing.T, directory string) Prepared {
	t.Helper()
	prepared := cachePaths(directory, f.version, f.kind)
	if err := os.MkdirAll(filepath.Dir(prepared.PackagePath), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		prepared.PackagePath: f.packageData, prepared.ManifestPath: f.manifest, prepared.SignaturePath: f.signature,
	} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return prepared
}

func (f *updateFixture) client() *http.Client {
	return &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		var data []byte
		switch req.URL.String() {
		case releasesAPI:
			assets := fmt.Sprintf(`"assets":[{"name":%q},{"name":%q},{"name":%q}]`, ManifestName, SignatureName, PackageName(f.kind))
			data = []byte(fmt.Sprintf(`[{"tag_name":"invalid"},{"tag_name":"v9.9.9","draft":true,%s},{"tag_name":"v9.9.8"},{"tag_name":"v%s","prerelease":%t,%s}]`,
				assets, f.version, Channel(f.version) == "alpha", assets))
		case assetURL(f.version, ManifestName):
			data = f.manifest
		case assetURL(f.version, SignatureName):
			data = f.signature
		case assetURL(f.version, PackageName(f.kind)):
			f.downloads.Add(1)
			data = f.packageData
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: req}, nil
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (transport roundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *memorySettings) Setting(_ context.Context, key, fallback string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return fallback, nil
}

func (s *memorySettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func awaitSnapshot(t *testing.T, service *Service, condition func(Snapshot) bool) Snapshot {
	t.Helper()
	updates := make(chan Snapshot, 1)
	unsubscribe := service.Subscribe(func(snapshot Snapshot) {
		if condition(snapshot) {
			select {
			case updates <- snapshot:
			default:
			}
		}
	})
	defer unsubscribe()
	select {
	case status := <-updates:
		return status
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for status, got %#v", service.Snapshot())
		return Snapshot{}
	}
}
