// Package plugins discovers and runs QuailCrate packages through the public API.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

const invocationTimeout = 2 * time.Second

type Runtime interface {
	Call(context.Context, []byte) ([]byte, error)
	Close() error
}

type NotifyHandler func(pluginID string, generation uint64, notification v1.Notification)

type Manager struct {
	mu            sync.RWMutex
	active        map[string]*plugin
	stopping      map[string]*plugin
	notifyHandler NotifyHandler
	sequence      atomic.Uint64
}

type plugin struct {
	pkg        Package
	grants     []string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	ready      bool // guarded by Manager.mu
	jobs       chan job
	done       chan struct{}
}

type job struct {
	ctx     context.Context
	request v1.Request
	reply   chan outcome
}

type outcome struct {
	response v1.Response
	err      error
}

func NewManager() *Manager {
	return &Manager{active: make(map[string]*plugin), stopping: make(map[string]*plugin)}
}

// SetNotifyHandler installs the callback that receives validated asynchronous
// push notifications emitted by native plugins via quailcrate_bind_host.
func (manager *Manager) SetNotifyHandler(fn NotifyHandler) {
	manager.mu.Lock()
	manager.notifyHandler = fn
	manager.mu.Unlock()
}

func (manager *Manager) Enable(ctx context.Context, pkg Package, grants []string, payload v1.Payload) (v1.Response, error) {
	if pkg.Err != nil {
		return v1.Response{}, pkg.Err
	}
	if err := ValidateManifest(pkg.Manifest); err != nil {
		return v1.Response{}, err
	}
	for _, grant := range grants {
		if !contains(RequestedGrants(pkg.Manifest), grant) {
			return v1.Response{}, fmt.Errorf("grant %q is not declared", grant)
		}
	}
	id := pkg.Manifest.ID
	if pkg.sourceBuild != nil {
		// Build before starting the two-second native initialization deadline.
		if err := pkg.sourceBuild.prepare(ctx, &pkg); err != nil {
			return v1.Response{}, err
		}
	}
	manager.mu.Lock()
	if manager.active[id] != nil || manager.stopping[id] != nil {
		manager.mu.Unlock()
		return v1.Response{}, fmt.Errorf("plugin %q is already enabled or stopping", id)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	worker := &plugin{
		pkg: pkg, grants: append([]string(nil), grants...), generation: manager.sequence.Add(1),
		ctx: lifetime, cancel: cancel, jobs: make(chan job), done: make(chan struct{}),
	}
	manager.active[id] = worker
	manager.mu.Unlock()

	call, release := context.WithTimeout(ctx, invocationTimeout)
	defer release()
	payload.HostAPIVersion = pkg.Manifest.HostAPIVersion
	init := manager.makeJob(call, "init", payload)
	go manager.run(worker, init)
	response, err := await(worker, init)
	if err != nil {
		manager.stop(id, worker)
		return response, err
	}
	return response, nil
}

func (manager *Manager) Call(ctx context.Context, id, op string, payload v1.Payload) (v1.Response, error) {
	return manager.call(ctx, id, 0, op, payload)
}

// CallGeneration queues work only on the particular running instance the caller observed.
func (manager *Manager) CallGeneration(ctx context.Context, id string, generation uint64, op string, payload v1.Payload) (v1.Response, error) {
	if generation == 0 {
		return v1.Response{}, errors.New("a plugin generation is required")
	}
	return manager.call(ctx, id, generation, op, payload)
}

func (manager *Manager) call(ctx context.Context, id string, generation uint64, op string, payload v1.Payload) (v1.Response, error) {
	if op == "init" || op == "stop" {
		return v1.Response{}, fmt.Errorf("operation %q is controlled by the plugin lifecycle", op)
	}
	manager.mu.RLock()
	worker := manager.active[id]
	if worker != nil && (!worker.ready || (generation != 0 && worker.generation != generation)) {
		worker = nil
	}
	manager.mu.RUnlock()
	if worker == nil {
		return v1.Response{}, fmt.Errorf("plugin %q is not running", id)
	}
	call, cancel := context.WithTimeout(ctx, invocationTimeout)
	defer cancel()
	queued := manager.makeJob(call, op, payload)
	select {
	case worker.jobs <- queued:
	case <-worker.done:
		return v1.Response{}, errors.New("plugin worker stopped")
	case <-worker.ctx.Done():
		return v1.Response{}, worker.ctx.Err()
	case <-call.Done():
		return v1.Response{}, call.Err()
	}
	return await(worker, queued)
}

func await(worker *plugin, call job) (v1.Response, error) {
	select {
	case result := <-call.reply:
		if err := worker.ctx.Err(); err != nil {
			return v1.Response{}, err
		}
		if err := call.ctx.Err(); err != nil {
			return v1.Response{}, err
		}
		return result.response, result.err
	case <-worker.ctx.Done():
		return v1.Response{}, worker.ctx.Err()
	case <-call.ctx.Done():
		return v1.Response{}, call.ctx.Err()
	}
}

func (manager *Manager) Disable(ctx context.Context, id string) error {
	worker := manager.stop(id, nil)
	if worker == nil {
		return nil
	}
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) stop(id string, expected *plugin) *plugin {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	worker := manager.active[id]
	if worker == nil {
		return manager.stopping[id]
	}
	if expected != nil && worker != expected {
		return nil
	}
	delete(manager.active, id)
	manager.stopping[id] = worker
	worker.cancel()
	return worker
}

func (manager *Manager) Active(id string) bool {
	return manager.Generation(id) != 0
}

// GenerationDone closes after the exact worker has released its resources.
func (manager *Manager) GenerationDone(id string, generation uint64) <-chan struct{} {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if worker := manager.active[id]; worker != nil && worker.generation == generation {
		return worker.done
	}
	done := make(chan struct{})
	close(done)
	return done
}

func (manager *Manager) Stopping(id string) bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.stopping[id] != nil
}

func (manager *Manager) Generation(id string) uint64 {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if worker := manager.active[id]; worker != nil && worker.ready {
		return worker.generation
	}
	return 0
}

func (manager *Manager) makeJob(ctx context.Context, op string, payload v1.Payload) job {
	return job{
		ctx: ctx, request: v1.Request{RequestID: strconv.FormatUint(manager.sequence.Add(1), 10), Op: op, Payload: payload},
		reply: make(chan outcome, 1),
	}
}

func (manager *Manager) run(worker *plugin, init job) {
	id := worker.pkg.Manifest.ID
	defer func() {
		manager.mu.Lock()
		if manager.active[id] == worker {
			delete(manager.active, id)
		}
		if manager.stopping[id] == worker {
			delete(manager.stopping, id)
		}
		close(worker.done)
		manager.mu.Unlock()
	}()
	initCtx, cancel := joinedContext(init.ctx, worker.ctx)
	runtime, err := openRuntime(initCtx, worker.pkg)
	if err != nil {
		cancel()
		init.reply <- outcome{err: err}
		return
	}
	defer runtime.Close()
	inited := invokeRuntime(initCtx, worker, runtime, init.request, v1.Data{})
	cancel()
	if inited.err != nil {
		init.reply <- inited
		return
	}
	manager.mu.Lock()
	if manager.active[id] == worker {
		worker.ready = true
	}
	manager.mu.Unlock()
	init.reply <- inited
	locale := init.request.Payload.Locale
	var exited <-chan struct{}
	if process, ok := runtime.(interface{ Done() <-chan struct{} }); ok {
		exited = process.Done()
	}
	if notifier, ok := runtime.(interface{ Notifications() <-chan []byte }); ok {
		go manager.watchNotifications(worker, inited.response.Data, notifier.Notifications())
	}
	defer func() {
		select {
		case <-exited:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), invocationTimeout)
		defer cancel()
		stop := manager.makeJob(ctx, "stop", v1.Payload{Locale: locale})
		invokeRuntime(ctx, worker, runtime, stop.request, inited.response.Data)
	}()
	for {
		select {
		case <-exited:
			return
		case <-worker.ctx.Done():
			return
		case call := <-worker.jobs:
			if err := call.ctx.Err(); err != nil {
				call.reply <- outcome{err: err}
				continue
			}
			ctx, cancel := worker.executionContext(call.ctx)
			locale = call.request.Payload.Locale
			call.reply <- invokeRuntime(ctx, worker, runtime, call.request, inited.response.Data)
			cancel()
		}
	}
}

func (worker *plugin) executionContext(caller context.Context) (context.Context, context.CancelFunc) {
	if worker.pkg.Manifest.Runtime == "dll" {
		// The C ABI cannot interrupt one request. Let an obsolete search finish
		// in its existing time budget and drain its reply before the next job.
		// The caller can leave immediately. A deadline or disable still kills
		// a hung native process through its job object.
		if deadline, ok := caller.Deadline(); ok {
			return context.WithDeadline(worker.ctx, deadline)
		}
		return context.WithTimeout(worker.ctx, invocationTimeout)
	}
	return joinedContext(caller, worker.ctx)
}

func joinedContext(first, second context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(first)
	stop := context.AfterFunc(second, cancel)
	if second.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func openRuntime(ctx context.Context, pkg Package) (Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pkg.Manifest.Runtime == "lua" {
		return openLua(ctx, pkg)
	}
	return openDLL(ctx, pkg)
}

func invokeRuntime(ctx context.Context, worker *plugin, runtime Runtime, request v1.Request, init v1.Data) outcome {
	if err := prepareDelivery(worker.pkg.Manifest, worker.grants, &request); err != nil {
		return outcome{err: err}
	}
	if err := ctx.Err(); err != nil {
		return outcome{err: err}
	}
	if !contains(worker.grants, "apps.read") {
		request.Payload.Apps = nil
	}
	if !contains(worker.grants, "paths.open") {
		request.Payload.Tools = nil
	}
	request.Payload.Locale = v1.NormalizeLocale(request.Payload.Locale)
	request.Payload.Strings = worker.pkg.localizedStrings[request.Payload.Locale]
	if request.Payload.Strings == nil {
		request.Payload.Strings = map[string]string{}
	}
	if err := validateRequest(request, worker.grants, init); err != nil {
		return outcome{err: err}
	}
	input, err := json.Marshal(request)
	if err != nil {
		return outcome{err: err}
	}
	if len(input) > v1.MaxMessageSize {
		return outcome{err: errors.New("request exceeds size limit")}
	}
	output, err := runtime.Call(ctx, input)
	if ctx.Err() != nil {
		return outcome{err: ctx.Err()}
	}
	if err != nil {
		return outcome{err: fmt.Errorf("%s: %w", request.Op, err)}
	}
	if len(output) > v1.MaxMessageSize || !utf8.Valid(output) {
		return outcome{err: errors.New("response exceeds size limit or is not UTF-8")}
	}
	var response v1.Response
	if err := json.Unmarshal(output, &response); err != nil {
		return outcome{err: fmt.Errorf("response JSON: %w", err)}
	}
	if err := validateResponse(worker.pkg.Manifest, worker.grants, request, response, init); err != nil {
		return outcome{err: err}
	}
	if !worker.pkg.Bundled && response.OK {
		if err := authorizeIcons(worker.grants, request.Payload.Apps, &response.Data); err != nil {
			return outcome{err: err}
		}
	}
	response.Generation = worker.generation
	if !response.OK {
		return outcome{response: response, err: response.Error}
	}
	return outcome{response: response}
}

func (manager *Manager) watchNotifications(worker *plugin, init v1.Data, ch <-chan []byte) {
	id := worker.pkg.Manifest.ID
	for {
		select {
		case <-worker.ctx.Done():
			return
		case raw, ok := <-ch:
			if !ok {
				return
			}
			if len(raw) == 0 || len(raw) > v1.MaxMessageSize || !utf8.Valid(raw) {
				continue
			}
			var note v1.Notification
			if err := json.Unmarshal(raw, &note); err != nil {
				continue
			}
			if err := validateNotification(worker.pkg.Manifest, init, note); err != nil {
				continue
			}
			manager.mu.RLock()
			handler := manager.notifyHandler
			stillCurrent := manager.active[id] == worker && worker.ready
			manager.mu.RUnlock()
			if handler != nil && stillCurrent {
				handler(id, worker.generation, note)
			}
		}
	}
}

func validateNotification(manifest v1.Manifest, init v1.Data, note v1.Notification) error {
	if len(note.State) != 0 && !json.Valid(note.State) {
		return errors.New("invalid notification state JSON")
	}
	switch note.Type {
	case "widget.refresh":
		if note.WidgetID != "" && !validLocalID(note.WidgetID) {
			return fmt.Errorf("invalid widget id %q", note.WidgetID)
		}
		if len(init.Widgets) == 0 {
			return errors.New("widget.refresh requires a registered widget")
		}
		return nil
	case "page.notify":
		if len(init.AppTabs) == 0 {
			return errors.New("page.notify requires a registered app tab")
		}
		if note.TabID != "" && !hasAppTab(init.AppTabs, note.TabID) {
			return fmt.Errorf("unregistered tab id %q", note.TabID)
		}
		if len(note.Detail) != 0 && (len(note.Detail) > v2.MaxPageArgsBytes || !json.Valid(note.Detail)) {
			return errors.New("invalid page.notify detail")
		}
		return nil
	case "event.publish":
		payload := note.Args
		if len(payload) == 0 {
			payload = note.Detail
		}
		if !slices.Contains(manifest.Publishes, note.Topic) || !validArgs(payload) {
			return errors.New("undeclared topic or invalid event.publish payload")
		}
		return nil
	case "state.set":
		if len(note.State) == 0 {
			return errors.New("state.set requires state")
		}
		return nil
	default:
		return fmt.Errorf("unknown notification type %q", note.Type)
	}
}
