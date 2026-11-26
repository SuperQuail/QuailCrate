// Package pluginaudio plays host-authorized streams in a single plugin instance.
package pluginaudio

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"runtime"
	"sync"
	"time"
)

// Each slot bounds a cache file and a native decoder/device session. Superseded
// streams acquire no further resources while waiting for a previous one to exit.
var playerSlots = make(chan struct{}, 8)

type State struct {
	SessionID  string  `json:"sessionId"`
	Status     string  `json:"status"`
	PositionMS int64   `json:"positionMs"`
	DurationMS int64   `json:"durationMs"`
	Volume     float64 `json:"volume"`
	Seekable   bool    `json:"seekable"`
}

type Event struct {
	Type  string
	State State
	Error string
}

type Engine struct {
	mu         sync.Mutex
	active     *session
	closed     bool
	emit       func(Event)
	jobs       sync.WaitGroup
	pending    []Event
	delivering bool
}

type session struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	state    State
	commands chan command
	done     chan struct{}
}

type command struct {
	action   string
	position int64
	volume   float64
	result   chan error
}

func New(emit func(Event)) *Engine { return &Engine{emit: emit} }

// Play obtains all media bytes through open. The caller supplies the identity,
// authorization, headers and network policy in that function. Play replaces
// the previous session and returns while the new stream is being prepared.
func (e *Engine) Play(ctx context.Context, sessionID string, open func(context.Context) (io.ReadCloser, error)) error {
	if sessionID == "" || open == nil || ctx == nil {
		return errors.New("invalid playback request")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("audio engine closed")
	}
	if e.active != nil {
		if e.active.state.SessionID == sessionID {
			return errors.New("duplicate playback session")
		}
		e.active.cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &session{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), commands: make(chan command),
		state: State{SessionID: sessionID, Status: "loading", Volume: 1},
	}
	e.active = s
	e.pending = nil
	e.jobs.Add(1)
	go func() {
		defer e.jobs.Done()
		defer close(s.done)
		defer s.cancel()
		notify := func(ev Event) { e.enqueue(s, ev) }
		runErr := s.run(open, notify)
		if ctx.Err() != nil {
			s.update("stopped", 0, 0, false)
			e.enqueue(s, Event{Type: "audio.stateChanged", State: s.snapshot()})
		} else if runErr != nil {
			s.update("error", 0, 0, false)
			s.emit(notify, "audio.error", runErr.Error())
		}
	}()
	return nil
}

// Close cancels outstanding reads and releases the native playback resources.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	e.pending = nil
	if e.active != nil {
		e.active.cancel()
	}
	e.mu.Unlock()
	e.jobs.Wait()
}

// Host callbacks can synchronously issue a control action or disable the
// plugin. Deliver them in order on a separate worker so the media thread
// remains available for both actions. A replaced session loses its backlog.
func (e *Engine) enqueue(s *session, ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || s != e.active || e.emit == nil {
		return
	}
	if len(e.pending) >= 32 && e.pending[len(e.pending)-1].Type == "audio.stateChanged" {
		e.pending[len(e.pending)-1] = ev
	} else {
		e.pending = append(e.pending, ev)
	}
	if !e.delivering {
		e.delivering = true
		go e.deliver()
	}
}

func (e *Engine) deliver() {
	for {
		e.mu.Lock()
		if len(e.pending) == 0 {
			e.delivering = false
			e.mu.Unlock()
			return
		}
		ev := e.pending[0]
		e.pending = e.pending[1:]
		e.mu.Unlock()
		e.emit(ev)
	}
}

func (s *session) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *session) update(status string, position, duration int64, seekable bool) {
	s.mu.Lock()
	s.state.Status = status
	s.state.PositionMS = position
	s.state.DurationMS = duration
	s.state.Seekable = seekable
	s.mu.Unlock()
}

func (s *session) emit(notify func(Event), kind, detail string) {
	if notify != nil && s.ctx.Err() == nil {
		notify(Event{Type: kind, State: s.snapshot(), Error: detail})
	}
}

// Control operates on the named session only. It accepts the public audio.*
// actions. The volume is in [0,1] and positions are expressed in milliseconds.
func (e *Engine) Control(sessionID, action string, positionMS int64, volume float64) (State, error) {
	e.mu.Lock()
	s := e.active
	closed := e.closed
	e.mu.Unlock()
	if closed || s == nil || sessionID != s.state.SessionID {
		return State{}, errors.New("unknown playback session")
	}
	if action == "audio.stop" {
		s.cancel()
		<-s.done
		return s.snapshot(), nil
	}
	cmd := command{action: action, position: positionMS, volume: volume, result: make(chan error, 1)}
	state := s.snapshot()
	switch action {
	case "audio.getState":
		if state.Status != "playing" {
			return state, nil
		}
	case "audio.pause", "audio.resume":
	case "audio.seek":
		if positionMS < 0 || !state.Seekable || positionMS > state.DurationMS {
			return state, errors.New("invalid playback position")
		}
	case "audio.setVolume":
		if math.IsNaN(volume) || volume < 0 || volume > 1 {
			return state, errors.New("invalid playback volume")
		}
	default:
		return state, errors.New("unknown audio action")
	}
	if state.Status != "playing" && state.Status != "paused" {
		return state, errors.New("playback session is not ready")
	}
	select {
	case s.commands <- cmd:
		return s.snapshotAfter(cmd.result)
	case <-s.done:
		return s.snapshot(), errors.New("playback session ended")
	}
}

func (s *session) snapshotAfter(result <-chan error) (State, error) {
	err := <-result
	return s.snapshot(), err
}

func (s *session) run(open func(context.Context) (io.ReadCloser, error), notify func(Event)) error {
	select {
	case playerSlots <- struct{}{}:
		defer func() { <-playerSlots }()
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-time.After(3 * time.Second):
		return errors.New("audio playback capacity exhausted")
	}
	download, finishDownload := context.WithTimeout(s.ctx, 2*time.Minute)
	defer finishDownload()
	reader, closeInput, err := authorizedInput(download, open)
	if err != nil {
		return err
	}
	defer closeInput()
	if pcm, ok := peekPCM(reader); ok {
		return s.playPCM(reader, pcm, closeInput, notify)
	}
	if handled, fallback, err := s.tryStreamFragmentedMP4(download, reader, closeInput, notify); handled {
		return err
	} else {
		reader = fallback
	}
	var fragmentMS int64
	path, err := cacheStream(download, reader, &fragmentMS)
	closeInput()
	finishDownload()
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err := s.ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	player, err := openPlayer(path)
	if err != nil {
		return err
	}
	defer player.close()
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-player.callback.ready:
			event := player.callback.pop()
			if err := event.err(); err != nil {
				return err
			}
			switch event.kind {
			case eventItemSet:
				if err := player.invoke(mfPlay); err != nil {
					return err
				}
			case eventPlay, eventPause:
				mode := "playing"
				if event.kind == eventPause {
					mode = "paused"
				}
				duration := max(fragmentMS, player.time(mfGetDuration))
				s.update(mode, player.time(mfGetPosition), duration, player.seekable && duration > 0)
				s.emit(notify, "audio.stateChanged", "")
			case eventEnded:
				duration := max(fragmentMS, player.time(mfGetDuration))
				s.update("ended", duration, duration, false)
				s.emit(notify, "audio.ended", "")
				return nil
			case eventPositionSet:
				s.mu.Lock()
				s.state.PositionMS = player.time(mfGetPosition)
				s.mu.Unlock()
				s.emit(notify, "audio.stateChanged", "")
			}
		case cmd := <-s.commands:
			var err error
			switch cmd.action {
			case "audio.pause":
				err = player.invoke(mfPause)
			case "audio.resume":
				err = player.invoke(mfPlay)
			case "audio.seek":
				err = player.seek(cmd.position)
			case "audio.setVolume":
				err = player.volume(cmd.volume)
			}
			if err == nil {
				s.mu.Lock()
				s.state.PositionMS = player.time(mfGetPosition)
				if cmd.action == "audio.setVolume" {
					s.state.Volume = cmd.volume
				}
				s.mu.Unlock()
				if cmd.action == "audio.setVolume" {
					s.emit(notify, "audio.stateChanged", "")
				}
			}
			cmd.result <- err
		}
	}
}
