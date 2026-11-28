package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	PhaseIdle        = "idle"
	PhaseChecking    = "checking"
	PhaseAvailable   = "available"
	PhaseDownloading = "downloading"
	PhaseReady       = "ready"
	PhaseFailed      = "failed"
	PhaseInstalling  = "installing"

	ErrorNetwork   = "network"
	ErrorRelease   = "release"
	ErrorSignature = "signature"
	ErrorIntegrity = "integrity"
	ErrorDownload  = "download"
	ErrorCache     = "cache"
	ErrorSettings  = "settings"

	autoSetting  = "updates.auto"
	checkSetting = "updates.lastAutomaticCheck"
	checkEvery   = 24 * time.Hour
)

var ErrUnavailable = errors.New("no update is available for installation")

type Settings interface {
	Setting(context.Context, string, string) (string, error)
	SetSetting(context.Context, string, string) error
}

type Config struct {
	Version   string
	CacheDir  string
	AssetType string
	PublicKey ed25519.PublicKey
	Settings  Settings
	Client    *http.Client
}

type Snapshot struct {
	CurrentVersion       string
	TargetVersion        string
	Channel              string
	AutoUpdate           bool
	LastCheckedAt        time.Time
	DownloadedBytes      int64
	TotalBytes           int64
	CanInstall           bool
	ReleaseURL           string
	Phase                string
	ErrorCode            string
	LastInstallErrorCode string
}

type Service struct {
	ctx         context.Context
	config      Config
	client      *http.Client
	supported   bool
	installable bool
	startOnce   sync.Once
	ops         chan struct{}
	wake        chan struct{}

	settingMu sync.Mutex

	mu         sync.Mutex
	state      Snapshot
	ready      Prepared
	readySize  int64
	lastAuto   time.Time
	autoCancel context.CancelFunc
	subs       map[chan Snapshot]struct{}

	// The active check or download holds ops while accessing the candidate.
	candidate *candidate
}

func New(ctx context.Context, config Config) *Service {
	channel := Channel(config.Version)
	supported := channel != "dev" && len(config.PublicKey) == ed25519.PublicKeySize && PackageName(config.AssetType) != ""
	s := &Service{
		ctx: ctx, config: config, client: newHTTPClient(config.Client),
		supported: supported, installable: supported && config.AssetType != PortableManual && config.CacheDir != "",
		ops: make(chan struct{}, 1), wake: make(chan struct{}, 1), subs: make(map[chan Snapshot]struct{}),
		state: Snapshot{CurrentVersion: config.Version, Channel: channel, AutoUpdate: supported, Phase: PhaseIdle},
	}
	s.config.PublicKey = append(ed25519.PublicKey(nil), config.PublicKey...)
	if supported && config.Settings != nil {
		preference, err := config.Settings.Setting(ctx, autoSetting, "true")
		if err != nil {
			s.state.AutoUpdate = false
			s.setError(ErrorSettings)
		} else {
			s.state.AutoUpdate = preference == "true"
		}
		stamp, err := config.Settings.Setting(ctx, checkSetting, "")
		if err == nil {
			s.lastAuto, _ = time.Parse(time.RFC3339Nano, stamp)
			s.state.LastCheckedAt = s.lastAuto
		}
	}
	return s
}

func (s *Service) Start() {
	s.startOnce.Do(func() {
		if s.supported {
			go s.automaticLoop()
		}
	})
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Subscribe delivers the current snapshot and coalesces subsequent changes for slow listeners.
func (s *Service) Subscribe(listener func(Snapshot)) (unsubscribe func()) {
	ctx, cancel := context.WithCancel(s.ctx)
	updates := make(chan Snapshot, 1)
	s.mu.Lock()
	s.subs[updates] = struct{}{}
	updates <- s.state
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.subs, updates)
			s.mu.Unlock()
		}()
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return
			case snapshot := <-updates:
				listener(snapshot)
			}
		}
	}()
	return cancel
}

func (s *Service) SetAutoUpdate(ctx context.Context, enabled bool) error {
	if !s.supported {
		return ErrUnavailable
	}
	s.settingMu.Lock()
	defer s.settingMu.Unlock()
	if s.config.Settings != nil {
		if err := s.config.Settings.SetSetting(ctx, autoSetting, strconv.FormatBool(enabled)); err != nil {
			s.setError(ErrorSettings)
			return err
		}
	}
	s.mu.Lock()
	s.state.AutoUpdate = enabled
	if !enabled && s.autoCancel != nil {
		s.autoCancel()
	}
	s.publishLocked()
	s.mu.Unlock()
	if enabled {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// CheckNow starts a manual release check without downloading a package.
func (s *Service) CheckNow() bool {
	if !s.supported || s.ctx.Err() != nil || !s.tryOperation() {
		return false
	}
	go func() {
		defer s.endOperation()
		if err := s.check(s.ctx); err != nil && s.ctx.Err() == nil {
			s.report(err)
		}
	}()
	return true
}

// PrepareForInstall returns a verified package. The host may call it in a worker before requesting exit.
func (s *Service) PrepareForInstall(ctx context.Context) (Prepared, error) {
	if !s.installable {
		return Prepared{}, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case <-ctx.Done():
		return Prepared{}, ctx.Err()
	case s.ops <- struct{}{}:
		defer s.endOperation()
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	if s.candidate == nil {
		if ready, found := s.validateReady(); found {
			return ready, nil
		}
		if err := s.check(ctx); err != nil {
			s.report(err)
			return Prepared{}, err
		}
	}
	if s.candidate == nil {
		return Prepared{}, ErrUnavailable
	}
	return s.prepare(ctx)
}

func (s *Service) ReadyForAutomaticInstall() (Prepared, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready, s.installable && s.state.AutoUpdate && s.ready.Version != ""
}

func (s *Service) ReportFailure(code string) {
	s.change(func(state *Snapshot) {
		state.Phase = PhaseFailed
		state.ErrorCode = code
		state.LastInstallErrorCode = code
	})
}

func (s *Service) MarkInstalling() {
	s.change(func(state *Snapshot) {
		state.Phase = PhaseInstalling
		state.ErrorCode = ""
		state.LastInstallErrorCode = ""
	})
}

func (s *Service) automaticLoop() {
	if s.installable {
		select {
		case <-s.ctx.Done():
			return
		case s.ops <- struct{}{}:
			s.restoreReady()
			s.endOperation()
		}
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for s.ctx.Err() == nil {
		s.tryAutomaticCheck()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *Service) restoreReady() {
	prepared, asset, err := loadPrepared(s.config.CacheDir, s.config.Version, s.config.AssetType, s.config.PublicKey)
	if err != nil && s.Snapshot().ErrorCode == "" {
		s.report(err)
	}
	if prepared.Version != "" && (s.candidate == nil || !Newer(s.candidate.version, prepared.Version)) {
		s.setReady(prepared, asset.Size)
	}
}

func (s *Service) tryAutomaticCheck() {
	if !s.tryOperation() {
		return
	}
	defer s.endOperation()
	now := time.Now()
	s.mu.Lock()
	// A timestamp in the future can occur after a clock adjustment.
	if !s.state.AutoUpdate || (!now.Before(s.lastAuto) && now.Sub(s.lastAuto) < checkEvery) {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.autoCancel = cancel
	s.lastAuto = now
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.autoCancel = nil
		s.mu.Unlock()
	}()
	if s.config.Settings != nil {
		if err := s.config.Settings.SetSetting(ctx, checkSetting, now.UTC().Format(time.RFC3339Nano)); err != nil && ctx.Err() == nil {
			s.setError(ErrorSettings)
			return
		}
	}
	if err := s.check(ctx); err != nil {
		if ctx.Err() == nil {
			s.report(err)
		}
		return
	}
	if s.candidate != nil && s.installable {
		_, _ = s.prepare(ctx)
	}
}

func (s *Service) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.change(func(state *Snapshot) {
		state.Phase = PhaseChecking
		state.LastCheckedAt = time.Now()
		state.ErrorCode = ""
	})
	kind := s.config.AssetType
	if kind == PortableManual {
		kind = Portable
	}
	candidate, err := discover(ctx, s.client, s.config.Version, kind, s.config.PublicKey)
	if err != nil {
		if ctx.Err() != nil {
			s.showCandidate()
		}
		return err
	}
	s.candidate = candidate
	s.showCandidate()
	return nil
}

func (s *Service) showCandidate() {
	s.change(func(state *Snapshot) {
		state.ErrorCode = ""
		state.Phase = PhaseIdle
		state.TargetVersion, state.ReleaseURL = "", ""
		state.DownloadedBytes, state.TotalBytes, state.CanInstall = 0, 0, false
		if s.ready.Version != "" && (s.candidate == nil || !Newer(s.candidate.version, s.ready.Version)) {
			state.Phase = PhaseReady
			state.TargetVersion, state.ReleaseURL = s.ready.Version, releasePage(s.ready.Version)
			state.DownloadedBytes, state.TotalBytes, state.CanInstall = s.readySize, s.readySize, true
			return
		}
		if s.candidate != nil {
			state.Phase = PhaseAvailable
			state.TargetVersion, state.ReleaseURL = s.candidate.version, releasePage(s.candidate.version)
			state.TotalBytes, state.CanInstall = s.candidate.asset.Size, s.installable
		}
	})
}

func (s *Service) prepare(ctx context.Context) (Prepared, error) {
	candidate := s.candidate
	if ready, found := s.validateReady(); found {
		return ready, nil
	}
	s.change(func(state *Snapshot) {
		state.Phase = PhaseDownloading
		state.ErrorCode = ""
		state.DownloadedBytes = 0
	})
	prepared, err := cacheDownload(ctx, s.client, s.config.CacheDir, candidate, s.config.PublicKey, func(downloaded int64) {
		s.change(func(state *Snapshot) { state.DownloadedBytes = downloaded })
	})
	if err != nil {
		if ctx.Err() != nil {
			s.showCandidate()
		} else {
			s.report(err)
		}
		return Prepared{}, err
	}
	s.setReady(prepared, candidate.asset.Size)
	return prepared, nil
}

func (s *Service) validateReady() (Prepared, bool) {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if ready.Version == "" || (s.candidate != nil && Newer(s.candidate.version, ready.Version)) {
		return Prepared{}, false
	}
	if _, _, err := VerifyPrepared(ready, s.config.PublicKey); err == nil {
		return ready, true
	}
	s.mu.Lock()
	s.ready, s.readySize = Prepared{}, 0
	s.mu.Unlock()
	s.showCandidate()
	return Prepared{}, false
}

func (s *Service) setReady(prepared Prepared, size int64) {
	s.change(func(state *Snapshot) {
		if s.ready.Version != "" && Newer(s.ready.Version, prepared.Version) {
			return
		}
		s.ready, s.readySize = prepared, size
		state.TargetVersion, state.ReleaseURL = prepared.Version, releasePage(prepared.Version)
		state.DownloadedBytes, state.TotalBytes, state.CanInstall = size, size, true
		if state.ErrorCode == "" {
			state.Phase = PhaseReady
		}
	})
}

func (s *Service) tryOperation() bool {
	select {
	case s.ops <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) endOperation() { <-s.ops }

func (s *Service) change(apply func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.state
	apply(&s.state)
	if before != s.state {
		s.publishLocked()
	}
}

func (s *Service) publishLocked() {
	for listener := range s.subs {
		select {
		case <-listener:
		default:
		}
		listener <- s.state
	}
}

type serviceError struct {
	code string
	err  error
}

func (err *serviceError) Error() string { return err.err.Error() }
func (err *serviceError) Unwrap() error { return err.err }

func failure(code string, err error) error { return &serviceError{code, err} }

func (s *Service) report(err error) {
	var coded *serviceError
	if errors.As(err, &coded) {
		s.setError(coded.code)
		return
	}
	s.setError(ErrorRelease)
}

func (s *Service) setError(code string) {
	s.change(func(state *Snapshot) {
		state.Phase = PhaseFailed
		state.ErrorCode = code
	})
}
