package plugins

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "quailcrate/pluginapi/v1"
)

type musicMediaPage struct {
	Queue     []string `json:"queue"`
	Downloads map[string]struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"downloads"`
	Playback struct {
		TrackID    string `json:"trackId"`
		Status     string `json:"status"`
		PositionMs int64  `json:"positionMs"`
	} `json:"playback"`
}

type musicMediaHarness struct {
	t       *testing.T
	manager *Manager
	pkg     Package
	id      string
	state   json.RawMessage
	now     int64
}

func makeTestWAV(seconds int) []byte {
	const sr = 16000
	samples := make([]byte, seconds*sr*2)
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(36+len(samples)))
	wav.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(1), uint32(sr), uint32(2 * sr), uint16(2), uint16(16)} {
		_ = binary.Write(&wav, binary.LittleEndian, field)
	}
	wav.WriteString("data")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(len(samples)))
	wav.Write(samples)
	return wav.Bytes()
}

func newMusicMediaHarness(t *testing.T, apiBase string) *musicMediaHarness {
	t.Helper()
	pkg := stageMusicPackage(t)
	h := &musicMediaHarness{t: t, manager: NewManager(), pkg: pkg, id: pkg.Manifest.ID, now: 1700000000}
	h.state = json.RawMessage(fmt.Sprintf(`{"_apiBase":%q,"playMode":"shuffle","queue":["tr_1","tr_2","tr_3"],"tracks":[
		{"id":"tr_1","bvid":"BV1GJ411x7h7","cid":123,"durationMs":6000},
		{"id":"tr_2","bvid":"BV17x411w7KC","cid":456,"durationMs":6000},
		{"id":"tr_3","bvid":"BV1GJ411x7h7","cid":789,"durationMs":6000}]}`, apiBase))
	if _, err := h.manager.Enable(context.Background(), pkg, nil, v1.Payload{State: h.state, Locale: "zh-CN"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.manager.Disable(context.Background(), h.id) })
	return h
}

func (h *musicMediaHarness) call(op string, event *v1.Event) v1.Data {
	h.t.Helper()
	h.now++
	out, err := h.manager.Call(context.Background(), h.id, op, v1.Payload{
		State: h.state, TabID: "library", Locale: "zh-CN", Event: event, UnixTime: h.now,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	if len(out.Data.State) > 0 {
		h.state = out.Data.State
	}
	return out.Data
}

func (h *musicMediaHarness) page(op, detail string) v1.Data {
	h.t.Helper()
	return h.call("event", &v1.Event{Type: "page.message", TabID: "library", ActionID: "page-1", ControlID: op, Detail: json.RawMessage(detail)})
}

func (h *musicMediaHarness) tabState() musicMediaPage {
	h.t.Helper()
	data := h.call("tab_state", nil)
	var st musicMediaPage
	if err := json.Unmarshal(data.PageState, &st); err != nil {
		h.t.Fatalf("unmarshal tab_state: %v (%s)", err, data.PageState)
	}
	return st
}

func (h *musicMediaHarness) waitPage(desc string, pred func(musicMediaPage) bool) musicMediaPage {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last musicMediaPage
	for time.Now().Before(deadline) {
		last = h.tabState()
		if pred(last) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s; last = %+v", desc, last)
	return last
}

func TestMusicRowToggleAndPrefetchedShuffleStayInSync(t *testing.T) {
	wav := makeTestWAV(6)
	tr1Gate := make(chan struct{})
	releaseTr1 := sync.OnceFunc(func() { close(tr1Gate) })
	defer releaseTr1()

	var tr1AudioReqs atomic.Int32
	var prefetchedCid atomic.Value
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, `{"code":0,"data":{"isLogin":false}}`)
		case "/x/player/playurl", "/x/player/wbi/playurl":
			cid := r.URL.Query().Get("cid")
			if cid == "456" || cid == "789" {
				prefetchedCid.CompareAndSwap(nil, cid)
			}
			fmt.Fprintf(w, `{"code":0,"data":{"timelength":6000,"dash":{"audio":[{"id":30280,"baseUrl":%q}]}}}`,
				server.URL+"/audio.wav?cid="+cid)
		case "/audio.wav":
			cid := r.URL.Query().Get("cid")
			if cid == "123" {
				tr1AudioReqs.Add(1)
				select {
				case <-tr1Gate:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "audio.wav", time.Time{}, bytes.NewReader(wav))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := newMusicMediaHarness(t, server.URL)
	h.page("playback.playTrack", `{"trackId":"tr_1","toggleCurrent":true}`)

	// While tr_1 is loading, clicking its row again with toggleCurrent must be a no-op and not restart fetch.
	row := `{"trackId":"tr_1","queueTrackIds":["tr_1"],"toggleCurrent":true}`
	loadingReply := h.page("playback.playTrack", row)
	var pending struct{ Pending bool }
	if err := json.Unmarshal(loadingReply.PageReply, &pending); err != nil || !pending.Pending {
		t.Fatalf("second loading click reply = %s", loadingReply.PageReply)
	}
	releaseTr1()

	h.waitPage("tr_1 playing and next shuffle track prefetched", func(s musicMediaPage) bool {
		return s.Playback.TrackID == "tr_1" && s.Playback.Status == "playing" && prefetchedCid.Load() != nil
	})
	wantNext := "tr_2"
	if prefetchedCid.Load().(string) == "789" {
		wantNext = "tr_3"
	}

	// Seek, then toggle pause and resume from a different filtered row list: queue & position must survive without refetch.
	h.page("playback.seek", `{"positionMs":1500}`)
	h.page("playback.playTrack", row)
	paused := h.waitPage("tr_1 paused", func(s musicMediaPage) bool { return s.Playback.Status == "paused" })
	if paused.Playback.PositionMs < 1400 || len(paused.Queue) != 3 {
		t.Fatalf("row pause lost position or queue: %+v", paused)
	}
	h.page("playback.playTrack", row)
	resumed := h.waitPage("tr_1 resumed", func(s musicMediaPage) bool { return s.Playback.Status == "playing" })
	if resumed.Playback.PositionMs < 1400 || len(resumed.Queue) != 3 || tr1AudioReqs.Load() != 1 {
		t.Fatalf("row resume restarted audio or replaced queue: state=%+v tr1Reqs=%d", resumed, tr1AudioReqs.Load())
	}

	// Advance unixTime significantly: next must STILL choose the exact track prefetched for shuffle!
	h.now += 435
	h.page("playback.next", `{}`)
	nextSt := h.waitPage("next chose prefetched shuffle track", func(s musicMediaPage) bool {
		return s.Playback.TrackID == wantNext && s.Playback.Status == "playing"
	})
	if nextSt.Playback.TrackID != wantNext {
		t.Fatalf("prefetched %s but next chose %s", wantNext, nextSt.Playback.TrackID)
	}
}

func TestMusicDownloadAndPlaybackShareResolution(t *testing.T) {
	wav := makeTestWAV(4)
	resolveEntered := make(chan struct{}, 4)
	resolveGate := make(chan struct{})
	releaseResolve := sync.OnceFunc(func() { close(resolveGate) })
	defer releaseResolve()

	var playurlCalls, primaryHits, backupHits atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, `{"code":0,"data":{"isLogin":false}}`)
		case "/x/player/playurl", "/x/player/wbi/playurl":
			if r.URL.Query().Get("cid") == "123" {
				playurlCalls.Add(1)
				select {
				case resolveEntered <- struct{}{}:
				default:
				}
				select {
				case <-resolveGate:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprintf(w, `{"code":0,"data":{"durl":[{"url":%q,"backup_url":[%q]}]}}`,
				server.URL+"/primary.wav?cid="+r.URL.Query().Get("cid"),
				server.URL+"/backup.wav?cid="+r.URL.Query().Get("cid"))
		case "/primary.wav":
			if r.URL.Query().Get("cid") == "123" {
				primaryHits.Add(1)
			}
			http.Error(w, "primary failed", http.StatusServiceUnavailable)
		case "/backup.wav":
			if r.URL.Query().Get("cid") == "123" {
				backupHits.Add(1)
			}
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "backup.wav", time.Time{}, bytes.NewReader(wav))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := newMusicMediaHarness(t, server.URL)
	h.page("download.start", `{"trackId":"tr_1"}`)
	select {
	case <-resolveEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("download.start did not enter playurl resolution")
	}
	// Trigger playback while download is in-flight resolving the same track.
	h.page("playback.playTrack", `{"trackId":"tr_1"}`)
	time.Sleep(50 * time.Millisecond)
	releaseResolve()

	h.waitPage("tr_1 downloaded and playing from shared backup CDN", func(s musicMediaPage) bool {
		return s.Downloads["tr_1"].Status == "downloaded" && s.Playback.TrackID == "tr_1" &&
			(s.Playback.Status == "playing" || s.Playback.Status == "ended")
	})
	if playurlCalls.Load() != 1 || backupHits.Load() != 1 {
		t.Fatalf("download and playback duplicated resolution or transfer: playurl=%d primary=%d backup=%d",
			playurlCalls.Load(), primaryHits.Load(), backupHits.Load())
	}

	h.page("playback.stop", `{}`)
	playDownloaded := h.page("playback.playPlaylist", `{"viewId":"smart_downloaded"}`)
	var playReply struct {
		Pending bool   `json:"pending"`
		TrackID string `json:"trackId"`
	}
	if err := json.Unmarshal(playDownloaded.PageReply, &playReply); err != nil || !playReply.Pending || playReply.TrackID != "tr_1" {
		t.Fatalf("downloaded playlist did not start tr_1: %s", playDownloaded.PageReply)
	}

	h.page("download.remove", `{"trackId":"tr_1"}`)
	removed := h.page("playback.playPlaylist", `{"viewId":"smart_downloaded"}`)
	var emptyReply struct{ Code string }
	if err := json.Unmarshal(removed.PageReply, &emptyReply); err != nil || emptyReply.Code != "empty_playlist" {
		t.Fatalf("removed download is still playable in downloaded view: %s", removed.PageReply)
	}
}

func TestMusicDownloadStorageFailureStopsCDNRetries(t *testing.T) {
	var resolveCalls, cdnHits atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, `{"code":0,"data":{"isLogin":false}}`)
		case "/x/player/playurl", "/x/player/wbi/playurl":
			resolveCalls.Add(1)
			fmt.Fprintf(w, `{"code":0,"data":{"durl":[{"url":%q,"backup_url":[%q]}]}}`,
				server.URL+"/a.wav", server.URL+"/b.wav")
		case "/a.wav", "/b.wav":
			cdnHits.Add(1)
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(makeTestWAV(1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := newMusicMediaHarness(t, server.URL)
	// Replace cache/part directory with a regular file so CreateFileW fails with storage_unavailable.
	partDir := filepath.Join(h.pkg.DataDir, "cache", "part")
	if err := os.RemoveAll(partDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partDir, []byte("block-dir"), 0400); err != nil {
		t.Fatal(err)
	}

	h.page("download.start", `{"trackId":"tr_2"}`)
	st := h.waitPage("storage failure recorded without retries", func(s musicMediaPage) bool {
		return s.Downloads["tr_2"].Status == "error"
	})
	item := st.Downloads["tr_2"]
	if item.Error == "" || cdnHits.Load() != 0 || resolveCalls.Load() != 1 {
		t.Fatalf("storage failure retried or lacked message: item=%+v resolves=%d cdnHits=%d",
			item, resolveCalls.Load(), cdnHits.Load())
	}
}
