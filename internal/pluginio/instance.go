// Package pluginio brokers network requests and timers for individual plugin instances.
package pluginio

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"sync"
	"time"
)

const maxRequests = 8

var (
	ErrBusy    = errors.New("plugin resource limit reached")
	ErrInvalid = errors.New("invalid plugin I/O request")
	ErrDenied  = errors.New("network destination denied")
)

type Service struct {
	dir      string
	mu       sync.Mutex
	sessions map[sessionKey]*session
	slots    chan struct{}
}

func New(profileDir string) *Service {
	return &Service{dir: filepath.Join(profileDir, "plugin-sessions"), sessions: make(map[sessionKey]*session), slots: make(chan struct{}, 32)}
}

type Instance struct {
	service    *Service
	ctx        context.Context
	plugin     string
	allowLocal bool
	mu         sync.Mutex
	requests   map[string]context.CancelFunc
	timers     map[string]*pendingTimer
	slots      chan struct{}
}

func (s *Service) Instance(ctx context.Context, pluginID string, allowLocal bool) *Instance {
	i := &Instance{service: s, ctx: ctx, plugin: pluginID, allowLocal: allowLocal, requests: make(map[string]context.CancelFunc), timers: make(map[string]*pendingTimer), slots: make(chan struct{}, maxRequests)}
	context.AfterFunc(ctx, func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		for _, timer := range i.timers {
			timer.clock.Stop()
			timer.stopScope()
		}
		clear(i.timers)
	})
	return i
}

func validID(id string) bool {
	return len(id) > 0 && len(id) <= 128
}

// After delivers at most one notification. Expiration racing a successful cancel
// cannot fire the callback for a replacement timer with the same ID.
func (i *Instance) After(id string, delay time.Duration, fired func()) error {
	return i.AfterContext(i.ctx, id, delay, fired)
}

type pendingTimer struct {
	clock     *time.Timer
	stopScope func() bool
}

func (i *Instance) AfterContext(ctx context.Context, id string, delay time.Duration, fired func()) error {
	if !validID(id) || fired == nil || delay < 10*time.Millisecond || delay > 24*time.Hour {
		return ErrInvalid
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(i.timers) >= 32 || i.timers[id] != nil {
		return ErrBusy
	}
	t := new(pendingTimer)
	t.clock = time.AfterFunc(delay, func() {
		i.mu.Lock()
		deliver := i.timers[id] == t && i.ctx.Err() == nil && ctx.Err() == nil
		if i.timers[id] == t {
			delete(i.timers, id)
		}
		t.stopScope()
		i.mu.Unlock()
		if deliver {
			fired()
		}
	})
	t.stopScope = context.AfterFunc(ctx, func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.timers[id] == t {
			t.clock.Stop()
			delete(i.timers, id)
		}
	})
	i.timers[id] = t
	return nil
}

func (i *Instance) CancelTimer(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	t := i.timers[id]
	if t == nil {
		return false
	}
	t.clock.Stop()
	t.stopScope()
	delete(i.timers, id)
	return true
}

// Request performs bounded I/O asynchronously. Once admitted, it calls done
// exactly once (including cancellation); the host routes it by generation.
func (i *Instance) Request(id string, r Request, done func(Result, error)) error {
	return i.RequestContext(i.ctx, id, r, done)
}

func (i *Instance) RequestContext(scope context.Context, id string, r Request, done func(Result, error)) error {
	if !validID(id) || done == nil {
		return ErrInvalid
	}
	if err := r.validate(); err != nil {
		return err
	}
	r.Headers = maps.Clone(r.Headers)
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.ctx.Err(); err != nil {
		return err
	}
	if err := scope.Err(); err != nil {
		return err
	}
	if len(i.requests) >= maxRequests || i.requests[id] != nil {
		return ErrBusy
	}
	timeout := time.Duration(r.TimeoutMs) * time.Millisecond
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(i.ctx, timeout)
	stopScope := context.AfterFunc(scope, cancel)
	i.requests[id] = cancel
	go func() {
		result, err := i.fetch(ctx, r)
		i.mu.Lock()
		if ctx.Err() != nil {
			result, err = Result{}, ctx.Err()
		}
		delete(i.requests, id)
		i.mu.Unlock()
		stopScope()
		cancel()
		done(result, err)
	}()
	return nil
}

func (i *Instance) Cancel(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	cancel := i.requests[id]
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
