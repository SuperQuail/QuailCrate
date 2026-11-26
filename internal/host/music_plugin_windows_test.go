package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/pluginapps/pagetest"
	"quailcrate/internal/plugins"
	"quailcrate/internal/store"
	"quailcrate/internal/testaudio"
	v1 "quailcrate/pluginapi/v1"
)

type musicPageState struct {
	Version          int    `json:"version"`
	Locale           string `json:"locale"`
	ActiveView       string `json:"activeView"`
	SortBy           string `json:"sortBy"`
	PreferredQuality string `json:"preferredQuality"`
	Account          struct {
		LoggedIn bool   `json:"loggedIn"`
		Nickname string `json:"nickname"`
		Mid      int64  `json:"mid"`
	} `json:"account"`
	Login struct {
		Phase   string `json:"phase"`
		QRUrl   string `json:"qrUrl"`
		Message string `json:"message"`
	} `json:"login"`
	Playlists []struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Desc     string   `json:"desc"`
		TrackIDs []string `json:"trackIds"`
	} `json:"playlists"`
	Tracks []struct {
		ID         string `json:"id"`
		DedupeKey  string `json:"dedupeKey"`
		Bvid       string `json:"bvid"`
		Cid        int64  `json:"cid"`
		Page       int    `json:"page"`
		Title      string `json:"title"`
		Artist     string `json:"artist"`
		DurationMs int    `json:"durationMs"`
		Favorite   bool   `json:"favorite"`
	} `json:"tracks"`
	RecentIDs []string `json:"recentIds"`
	Downloads map[string]struct {
		Status string `json:"status"`
	} `json:"downloads"`
	Queue    []string `json:"queue"`
	Playback struct {
		TrackID      string  `json:"trackId"`
		Status       string  `json:"status"`
		PositionMs   int     `json:"positionMs"`
		DurationMs   int     `json:"durationMs"`
		Seekable     bool    `json:"seekable"`
		Volume       float64 `json:"volume"`
		Muted        bool    `json:"muted"`
		Mode         string  `json:"mode"`
		QualityLabel string  `json:"qualityLabel"`
		Error        string  `json:"error"`
	} `json:"playback"`
	ImportPreview *struct {
		Status      string `json:"status"`
		SourceTitle string `json:"sourceTitle"`
		Items       []struct {
			DedupeKey        string `json:"dedupeKey"`
			Bvid             string `json:"bvid"`
			Aid              int64  `json:"aid"`
			Cid              int64  `json:"cid"`
			Page             int    `json:"page"`
			TotalPages       int    `json:"totalPages"`
			Title            string `json:"title"`
			Artist           string `json:"artist"`
			DurationMs       int    `json:"durationMs"`
			Selected         bool   `json:"selected"`
			AlreadyInLibrary bool   `json:"alreadyInLibrary"`
		} `json:"items"`
		Errors []string `json:"errors"`
	} `json:"importPreview"`
}

type musicTestPage struct {
	pagetest.Browser
	t   *testing.T
	seq int
}

func (b *musicTestPage) readState() musicPageState {
	t := b.t
	t.Helper()
	page, err := b.Service.LoadTab(b.Page.Session)
	if err != nil {
		t.Fatalf("LoadTab: %v", err)
	}
	b.Page = page
	var st musicPageState
	if err := json.Unmarshal(page.Document.State, &st); err != nil {
		t.Fatalf("unmarshal music page state: %v (%s)", err, page.Document.State)
	}
	return st
}

func (b *musicTestPage) waitState(desc string, pred func(musicPageState) bool) musicPageState {
	t := b.t
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last musicPageState
	for time.Now().Before(deadline) {
		last = b.readState()
		if pred(last) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s; last state = %+v", desc, last)
	return last
}

func (b *musicTestPage) sendOp(op string, args any) json.RawMessage {
	t := b.t
	t.Helper()
	rawArgs, _ := json.Marshal(args)
	for attempt := range 5 {
		b.seq++
		b.readState()
		reply, err := b.Send(fmt.Sprintf("req-%d", b.seq), op, rawArgs)
		if err == nil {
			return reply.Value
		}
		if !errors.Is(err, pluginapps.ErrUnavailable) || attempt == 4 {
			t.Fatalf("send %s: %v", op, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	return nil
}

var (
	hostMusicNativeOnce   sync.Once
	hostMusicDLLPath      string
	hostMusicRunnerPath   string
	hostMusicNativeErr    error
)

func ensureHostMusicNative(t *testing.T) (string, string) {
	t.Helper()
	if dll := os.Getenv("QUAILCRATE_MUSIC_DLL"); dll != "" {
		if runner := os.Getenv("QUAILCRATE_NATIVE_RUNNER"); runner != "" {
			return dll, runner
		}
	}
	hostMusicNativeOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			hostMusicNativeErr = err
			return
		}
		outDir := filepath.Join(root, "build", "test-native-host")
		if err := os.MkdirAll(outDir, 0700); err != nil {
			hostMusicNativeErr = err
			return
		}
		script := filepath.Join(root, "scripts", "build-native.ps1")
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", script, "-OutputDir", outDir, "-ObjectDir", outDir)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			hostMusicNativeErr = fmt.Errorf("build native music plugin: %w\n%s", err, strings.TrimSpace(string(out)))
			return
		}
		hostMusicDLLPath = filepath.Join(outDir, "music.dll")
		hostMusicRunnerPath = filepath.Join(outDir, "QuailCrateNative.exe")
		_ = os.Setenv("QUAILCRATE_NATIVE_RUNNER", hostMusicRunnerPath)
	})
	if hostMusicNativeErr != nil {
		t.Fatalf("failed to build music.dll for host tests: %v", hostMusicNativeErr)
	}
	return hostMusicDLLPath, hostMusicRunnerPath
}

func copyMusicPluginForLocalTest(t *testing.T, dstDir string) string {
	t.Helper()
	dllPath, _ := ensureHostMusicNative(t)
	srcDir := filepath.Join("..", "..", "plugins", "music")
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0600)
	})
	if err != nil {
		t.Fatalf("copy music plugin: %v", err)
	}
	image, err := os.ReadFile(dllPath)
	if err != nil {
		t.Fatalf("read music.dll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "music.dll"), image, 0600); err != nil {
		t.Fatalf("write music.dll: %v", err)
	}
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:])
}

const testMusicSampleRate = 16000

func musicTestWAV() []byte {
	samples := make([]byte, 8*testMusicSampleRate*2)
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	binary.Write(&wav, binary.LittleEndian, uint32(36+len(samples)))
	wav.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(1), uint32(testMusicSampleRate), uint32(2 * testMusicSampleRate), uint16(2), uint16(16)} {
		binary.Write(&wav, binary.LittleEndian, field)
	}
	wav.WriteString("data")
	binary.Write(&wav, binary.LittleEndian, uint32(len(samples)))
	wav.Write(samples)
	return wav.Bytes()
}

func newFixtureMusicPage(t *testing.T, apiBase string) (*Host, *musicTestPage) {
	t.Helper()
	const pluginID = "quailcrate.music"
	h := newTabHost(t)
	digest := copyMusicPluginForLocalTest(t, filepath.Join(h.bundledDir, "music"))
	h.pluginCatalog = plugins.Catalog{TrustedDLLs: map[string]string{plugins.MusicDLLID: digest}}
	if err := h.db.SetPreference(h.ctx, pluginID, true, nil); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"_apiBase":%q,"tracks":[
		{"id":"part1","bvid":"BV17x411w7KC","cid":10001,"page":1,"totalPages":2,"durationMs":8000},
		{"id":"part2","bvid":"BV17x411w7KC","cid":10002,"page":2,"totalPages":2,"durationMs":8000}
	]}`, apiBase)
	if err := h.db.Apply(h.ctx, pluginID, store.Mutations{State: json.RawMessage(state)}); err != nil {
		t.Fatal(err)
	}
	h.scan()
	page := &musicTestPage{Browser: pagetest.Browser{Service: h}, t: t}
	page.openLibrary(h)
	t.Cleanup(page.Close)
	return h, page
}

func (page *musicTestPage) openLibrary(h *Host) {
	t := page.t
	t.Helper()
	tab, ok := h.AppCatalog().ResolveTab("quailcrate.music", "library")
	if !ok {
		t.Fatal("missing music library tab")
	}
	if err := page.Open(tab.TabRef, nil); err != nil {
		t.Fatal(err)
	}
	page.sendOp("library.refresh", map[string]any{})
}

func TestMusicPluginNextPlaysPrefetchedPrefixWhileTailIsBlocked(t *testing.T) {
	if !testaudio.HasOutput() {
		t.Skip("prefetch handoff requires a playing track and an audio output device")
	}
	wav := musicTestWAV()
	const prefixBytes = 44 + 5*testMusicSampleRate*2 // WAV header and five seconds of 16-bit mono audio
	tail := make(chan struct{})
	releaseTail := sync.OnceFunc(func() { close(tail) })
	var resolutions, primaryRequests, fallbackRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isNext := r.URL.Query().Get("cid") == "10002"
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, `{"code":0,"data":{"isLogin":false}}`)
		case "/x/player/playurl", "/x/player/wbi/playurl":
			if isNext {
				resolutions.Add(1)
			}
			fmt.Fprintf(w, `{"code":0,"data":{"timelength":8000,"dash":{"audio":[{"id":30280,"baseUrl":%q,"backupUrl":[%q]}]}}}`,
				server.URL+"/audio.wav?cid="+r.URL.Query().Get("cid"), server.URL+"/backup.wav")
		case "/audio.wav":
			w.Header().Set("Content-Type", "audio/wav")
			w.Header().Set("Content-Length", fmt.Sprint(len(wav)))
			if !isNext {
				w.Write(wav)
				return
			}
			primaryRequests.Add(1)
			w.Write(wav[:prefixBytes])
			w.(http.Flusher).Flush()
			select {
			case <-tail:
				w.Write(wav[prefixBytes:])
			case <-r.Context().Done():
			}
		case "/backup.wav":
			fallbackRequests.Add(1)
			http.Error(w, "unnecessary backup", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer releaseTail()
	h, page := newFixtureMusicPage(t, server.URL)
	musicDataDir := h.pluginDataDir("quailcrate.music")
	partDir := filepath.Join(musicDataDir, "cache", "part")
	cacheIndex := filepath.Join(musicDataDir, "cache", "index.json")
	partReachedPrefix := func() bool {
		entries, err := os.ReadDir(partDir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			f, err := os.Open(filepath.Join(partDir, e.Name()))
			if err != nil {
				continue
			}
			info, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && info.Size() == prefixBytes {
				return true
			}
		}
		return false
	}
	cacheCommitted := func() bool {
		raw, err := os.ReadFile(cacheIndex)
		if err != nil {
			return false
		}
		var idx map[string]struct {
			TrackID string `json:"trackId"`
			Bytes   int64  `json:"bytes"`
		}
		if json.Unmarshal(raw, &idx) != nil {
			return false
		}
		for _, item := range idx {
			if item.TrackID == "part2" && item.Bytes == int64(len(wav)) {
				return true
			}
		}
		return false
	}
	page.sendOp("playback.playTrack", map[string]any{"trackId": "part1", "queueTrackIds": []string{"part1", "part2"}})
	page.waitState("five seconds of next track prefetched", func(s musicPageState) bool {
		return primaryRequests.Load() == 1 && partReachedPrefix() && s.Playback.TrackID == "part1" && s.Playback.Status == "playing"
	})
	page.sendOp("playback.next", map[string]any{})
	page.waitState("next track advancing while tail remains blocked", func(s musicPageState) bool {
		return s.Playback.TrackID == "part2" && s.Playback.Status == "playing" && s.Playback.PositionMs > 0
	})
	releaseTail()
	page.waitState("same source committed after tail release", func(s musicPageState) bool {
		return cacheCommitted()
	})
	if resolutions.Load() != 1 || primaryRequests.Load() != 1 || fallbackRequests.Load() != 0 {
		t.Fatalf("prefetch handoff repeated source selection or audio transfer: resolve=%d primary=%d backup=%d",
			resolutions.Load(), primaryRequests.Load(), fallbackRequests.Load())
	}
}

func TestMusicPluginPlaybackCacheAndDownloads(t *testing.T) {
	const pluginID = "quailcrate.music"
	wav := musicTestWAV()

	var audioRequests, resolverRequests atomic.Int32
	var offline atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			if offline.Load() {
				http.Error(w, "offline", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"isLogin":false}}`)
		case "/x/player/playurl", "/x/player/wbi/playurl":
			resolverRequests.Add(1)
			if offline.Load() {
				http.Error(w, "offline", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"timelength":8000,"dash":{"audio":[{"id":30280,"baseUrl":%q}]}}}`, server.URL+"/audio.wav?cid="+r.URL.Query().Get("cid"))
		case "/audio.wav":
			audioRequests.Add(1)
			if offline.Load() {
				http.Error(w, "offline", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "audio.wav", time.Time{}, bytes.NewReader(wav))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h, page := newFixtureMusicPage(t, server.URL)
	restart := func() {
		t.Helper()
		page.Close()
		h.toggle(pluginID, false)
		h.toggle(pluginID, true)
		page.openLibrary(h)
	}
	waitDownloaded := func(id string) musicPageState {
		t.Helper()
		return page.waitState(id+" downloaded", func(s musicPageState) bool { return s.Downloads[id].Status == "downloaded" })
	}
	waitPlaying := func(id string) {
		t.Helper()
		page.waitState(id+" playing", func(s musicPageState) bool { return s.Playback.TrackID == id && s.Playback.Status == "playing" })
	}
	trackArgs := func(id string) map[string]any { return map[string]any{"trackId": id} }

	page.sendOp("download.start", trackArgs("part2"))
	st := waitDownloaded("part2")
	if len(st.RecentIDs) != 0 || len(st.Queue) != 0 || st.Playback.TrackID != "" || audioRequests.Load() != 1 {
		t.Fatalf("direct download changed playback or fetched duplicate audio: state=%+v requests=%d", st, audioRequests.Load())
	}
	if !testaudio.HasOutput() {
		// Downloads, including their index across a plugin restart, work offline without an audio device.
		offline.Store(true)
		restart()
		waitDownloaded("part2")
		page.sendOp("download.remove", trackArgs("part2"))
		page.waitState("download removed", func(s musicPageState) bool {
			_, present := s.Downloads["part2"]
			return !present
		})
		restart()
		st = page.readState()
		if len(st.Downloads) != 0 || len(st.Tracks) != 2 || audioRequests.Load() != 1 || resolverRequests.Load() != 1 {
			t.Fatalf("offline download removal was not persisted: state=%+v media=%d resolver=%d", st, audioRequests.Load(), resolverRequests.Load())
		}
		t.Log("skipped real playback, seek, and playback-cache promotion: no audio output device")
		return
	}
	row := map[string]any{"trackId": "part1", "queueTrackIds": []string{"part1"}, "toggleCurrent": true}
	page.sendOp("playback.playTrack", row)
	waitPlaying("part1")
	page.sendOp("playback.seek", map[string]any{"positionMs": 1200})
	page.waitState("seek before row toggle", func(s musicPageState) bool { return s.Playback.PositionMs >= 1100 })
	// The same track can be clicked from a different filtered list.
	row["queueTrackIds"] = []string{"part2", "part1"}
	page.sendOp("playback.playTrack", row)
	st = page.waitState("row pause", func(s musicPageState) bool { return s.Playback.Status == "paused" })
	if st.Playback.PositionMs < 1100 || !slices.Equal(st.Queue, []string{"part1"}) || !slices.Equal(st.RecentIDs, st.Queue) {
		t.Fatalf("row pause lost progress or changed queue/history: %+v", st)
	}
	page.sendOp("playback.playTrack", row)
	st = page.waitState("row resume", func(s musicPageState) bool {
		return s.Playback.Status == "playing" && s.Playback.PositionMs >= 1100
	})
	if !slices.Equal(st.Queue, []string{"part1"}) || audioRequests.Load() != 2 || resolverRequests.Load() != 2 {
		t.Fatalf("resume restarted source or replaced queue: queue=%v media=%d resolver=%d", st.Queue, audioRequests.Load(), resolverRequests.Load())
	}
	page.sendOp("playback.stop", map[string]any{})

	// A new plugin generation gets fresh audio and media store instances.
	offline.Store(true)
	restart()
	waitDownloaded("part2")
	page.sendOp("view.select", map[string]any{"viewId": "smart_downloaded"})
	page.sendOp("playback.playPlaylist", map[string]any{"viewId": "smart_downloaded"})
	waitPlaying("part2")
	page.sendOp("playback.playTrack", map[string]any{"trackId": "part1", "queueTrackIds": []string{"part1"}})
	waitPlaying("part1")
	page.sendOp("download.start", trackArgs("part1"))
	waitDownloaded("part1")
	page.sendOp("download.remove", trackArgs("part1"))
	st = page.waitState("only part2 remains downloaded", func(s musicPageState) bool {
		_, present := s.Downloads["part1"]
		return !present && s.Downloads["part2"].Status == "downloaded"
	})
	if st.Playback.TrackID != "part1" || st.Playback.Status != "playing" || len(st.Tracks) != 2 {
		t.Fatalf("removing download interrupted active playback or changed library: %+v", st)
	}
	page.sendOp("playback.stop", map[string]any{})
	restart()
	st = waitDownloaded("part2")
	if _, exists := st.Downloads["part1"]; exists {
		t.Fatal("removed download reappeared after restart")
	}
	if audioRequests.Load() != 2 || resolverRequests.Load() != 2 {
		t.Fatalf("local replay or promotion requested network: media=%d resolver=%d", audioRequests.Load(), resolverRequests.Load())
	}
}

func TestMusicPluginEndToEndWithLocalBilibiliAndAudioFixtures(t *testing.T) {
	const pluginID = "quailcrate.music"
	audioFixture, err := os.ReadFile(filepath.Join("..", "pluginaudio", "testdata", "silence-fragmented.m4a"))
	if err != nil {
		t.Fatalf("read audio fixture: %v", err)
	}

	var pollCalls atomic.Int32
	var forceExpiredQR atomic.Bool
	var playurlCalls atomic.Int32
	var forceExpiredFirstURL atomic.Bool
	forceExpiredFirstURL.Store(true)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/passport-login/web/qrcode/generate":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"url":        "https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=testkey",
					"qrcode_key": "testkey",
				},
			})
		case "/x/passport-login/web/qrcode/poll":
			if forceExpiredQR.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"code": 86038, "message": "expired"}})
				return
			}
			n := pollCalls.Add(1)
			switch {
			case n == 1:
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"code": 86101, "message": "waiting"}})
			case n == 2:
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"code": 86090, "message": "scanned"}})
			default:
				http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "fixture_sess", Path: "/"})
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"code": 0, "message": "ok"}})
			}
		case "/x/web-interface/nav":
			logged := strings.Contains(r.Header.Get("Cookie"), "SESSDATA=fixture_sess")
			data := map[string]any{
				"isLogin": logged,
				"wbi_img": map[string]any{
					"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
					"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
				},
			}
			if logged {
				data["uname"] = "测试用户"
				data["mid"] = 424242
				data["level_info"] = map[string]any{"current_level": 6}
				data["vipStatus"] = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
		case "/b23/short1":
			http.Redirect(w, r, server.URL+"/redirected-video", http.StatusFound)
		case "/redirected-video":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<link rel="canonical" href="https://www.bilibili.com/video/BV1Q541167Qg?p=1">`))
		case "/x/web-interface/view":
			bv := r.URL.Query().Get("bvid")
			if bv == "BV1Q541167Qg" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"bvid":     "BV1Q541167Qg",
						"aid":      541167,
						"cid":      90001,
						"title":    "短链解析曲目",
						"duration": 180,
						"owner":    map[string]any{"name": "UP_Short"},
						"pages":    []map[string]any{{"cid": 90001, "page": 1, "part": "主曲", "duration": 180}},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":     "BV17x411w7KC",
					"aid":      170001,
					"cid":      10001,
					"title":    "测试合集",
					"duration": 240,
					"owner":    map[string]any{"name": "UP_Multi"},
					"pages": []map[string]any{
						{"cid": 10001, "page": 1, "part": "第一乐章", "duration": 120},
						{"cid": 10002, "page": 2, "part": "第二乐章", "duration": 120},
					},
				},
			})
		case "/x/v3/fav/resource/list":
			pn := r.URL.Query().Get("pn")
			if pn == "2" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"info":     map[string]any{"title": "我的收藏夹"},
						"has_more": false,
						"medias": []map[string]any{
							{"bvid": "BV1xx411c7mD", "id": 1002, "title": "收藏第二页", "duration": 95, "attr": 0, "upper": map[string]any{"name": "UP_Fav2"}},
						},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"info":     map[string]any{"title": "我的收藏夹"},
					"has_more": true,
					"medias": []map[string]any{
						{"bvid": "BV1xx411c7mC", "id": 1001, "title": "收藏第一页", "duration": 90, "attr": 0, "upper": map[string]any{"name": "UP_Fav1"}},
						{"bvid": "BV1dead00000", "id": 1009, "title": "已失效视频", "duration": 0, "attr": 9, "upper": map[string]any{"name": "Deleted"}},
					},
				},
			})
		case "/x/player/wbi/playurl":
			if r.URL.Query().Get("w_rid") == "" || r.URL.Query().Get("wts") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			callNo := playurlCalls.Add(1)
			var audioList []map[string]any
			if forceExpiredFirstURL.Load() && callNo == 1 {
				// First call returns only an expired URL so plugin tests re-resolve on audio.error.
				audioList = []map[string]any{
					{"id": 30280, "baseUrl": server.URL + "/audio/expired"},
				}
			} else {
				// Primary fails, backup serves real fragmented m4a fixture!
				audioList = []map[string]any{
					{"id": 30280, "baseUrl": server.URL + "/audio/fail", "backupUrl": []string{server.URL + "/audio/ok.m4a"}},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"timelength": 4000,
					"dash": map[string]any{
						"duration": 4,
						"audio":    audioList,
					},
				},
			})
		case "/audio/fail", "/audio/expired":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("forbidden"))
		case "/audio/ok.m4a":
			w.Header().Set("Content-Type", "audio/mp4")
			http.ServeContent(w, r, "ok.m4a", time.Unix(1700000000, 0), bytes.NewReader(audioFixture))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	h := newTabHost(t)
	h.mu.Lock()
	h.locale = "zh-CN"
	h.mu.Unlock()
	digest := copyMusicPluginForLocalTest(t, filepath.Join(h.bundledDir, "music"))
	h.pluginCatalog = plugins.Catalog{TrustedDLLs: map[string]string{plugins.MusicDLLID: digest}}
	if err := h.db.SetPreference(h.ctx, pluginID, true, nil); err != nil {
		t.Fatal(err)
	}
	h.scan()
	initState, _ := json.Marshal(map[string]any{
		"_apiBase":     server.URL,
		"_pollDelayMs": 20,
	})
	if err := h.db.Apply(h.ctx, pluginID, store.Mutations{State: initState}); err != nil {
		t.Fatal(err)
	}

	tab, ok := h.AppCatalog().ResolveTab(pluginID, "library")
	if !ok {
		rec := h.records[pluginID]
		t.Fatalf("music tab not registered: rec=%+v issues=%+v", rec, h.issues)
	}

	b := musicTestPage{Browser: pagetest.Browser{Service: h}, t: t}
	if err := b.Open(tab.TabRef, nil); err != nil {
		t.Fatalf("open packaged music page: %v", err)
	}
	defer b.Close()

	// Verify pnpm/Vite packaged web assets (index.html, assets/app.js, assets/app.css) all load with strict CSP.
	base := strings.TrimSuffix(b.Page.Document.URL, "index.html")
	for _, assetName := range []string{"index.html", "assets/app.js", "assets/app.css"} {
		asset, err := h.ReadPageAsset(b.Page.Session, base+assetName)
		if err != nil || len(asset.Body) == 0 || asset.Headers()["Content-Security-Policy"] == "" {
			t.Fatalf("packaged asset %s failed: err=%v len=%d", assetName, err, len(asset.Body))
		}
	}

	readState, waitState, sendOp := b.readState, b.waitState, b.sendOp

	// 1. QR Login flow: waiting_scan -> scanned_confirm -> logged_in (with Cookie session!).
	sendOp("bilibili.loginStart", map[string]any{})
	st := waitState("logged_in after QR poll", func(s musicPageState) bool {
		return s.Account.LoggedIn && s.Login.Phase == "logged_in"
	})
	if st.Account.Nickname != "测试用户" || st.Account.Mid != 424242 {
		t.Fatalf("unexpected account after QR login: %+v", st.Account)
	}

	// Logout clears session cookie.
	sendOp("bilibili.logout", map[string]any{})
	st = readState()
	if st.Account.LoggedIn || st.Login.Phase != "idle" {
		t.Fatalf("expected logged out state, got %+v", st.Account)
	}

	// Expired QR test (code 86038).
	forceExpiredQR.Store(true)
	sendOp("bilibili.loginStart", map[string]any{})
	st = waitState("expired QR phase", func(s musicPageState) bool {
		return s.Login.Phase == "expired"
	})
	sendOp("bilibili.loginCancel", map[string]any{})
	forceExpiredQR.Store(false)

	// 2. Preview multi-part BV + b23.tv redirect + paginated favlist + invalid input.
	sendOp("bilibili.preview", map[string]any{
		"input": "BV17x411w7KC\nhttps://b23.tv/short1\nhttps://space.bilibili.com/1/favlist?fid=42\nbad-entry",
	})
	st = waitState("import preview ready", func(s musicPageState) bool {
		return s.ImportPreview != nil && s.ImportPreview.Status == "ready"
	})
	// Should have 2 parts from BV17x411w7KC + 1 from short1 + 2 from favlist pages 1 & 2 = 5 valid items,
	// plus 2 errors (invalid input + deleted favlist item).
	if len(st.ImportPreview.Items) != 5 || len(st.ImportPreview.Errors) < 2 {
		t.Fatalf("unexpected import preview: items=%d errors=%v", len(st.ImportPreview.Items), st.ImportPreview.Errors)
	}

	// 3. Playlist CRUD & selective import + deduplication.
	createReply := sendOp("playlist.create", map[string]any{"name": "深夜代码", "desc": "Focus", "color": "accent"})
	var created struct {
		PlaylistID string `json:"playlistId"`
	}
	_ = json.Unmarshal(createReply, &created)
	if created.PlaylistID == "" {
		t.Fatalf("playlist.create did not return playlistId: %s", createReply)
	}
	sendOp("playlist.update", map[string]any{"playlistId": created.PlaylistID, "name": "深夜代码 · 精选", "desc": "Updated"})

	// Import only the first 2 items (P1 & P2 of multi-part BV).
	chosen := st.ImportPreview.Items[:2]
	impReply := sendOp("bilibili.import", map[string]any{
		"playlistId": created.PlaylistID,
		"items":      chosen,
	})
	var impResult struct {
		Imported int      `json:"imported"`
		AddedNew int      `json:"addedNew"`
		TrackIDs []string `json:"trackIds"`
	}
	_ = json.Unmarshal(impReply, &impResult)
	if impResult.Imported != 2 || impResult.AddedNew != 2 || len(impResult.TrackIDs) != 2 {
		t.Fatalf("unexpected import reply: %s", impReply)
	}
	// Re-importing the same first item deduplicates (addedNew == 0).
	dupReply := sendOp("bilibili.import", map[string]any{
		"playlistId": created.PlaylistID,
		"items":      chosen[:1],
	})
	var dupResult struct {
		AddedNew int `json:"addedNew"`
	}
	_ = json.Unmarshal(dupReply, &dupResult)
	if dupResult.AddedNew != 0 {
		t.Fatalf("duplicate import created new track: %s", dupReply)
	}

	// Favorite & queue operations.
	sendOp("track.toggleFavorite", map[string]any{"trackId": impResult.TrackIDs[0]})
	sendOp("queue.add", map[string]any{"trackId": impResult.TrackIDs[0]})
	sendOp("queue.add", map[string]any{"trackId": impResult.TrackIDs[1]})

	// Resolve an expired URL, then fetch from the backup CDN. A download exercises
	// the same local fixture on machines with no device for the mini-player.
	hasOutput := testaudio.HasOutput()
	if hasOutput {
		sendOp("playback.playTrack", map[string]any{"trackId": impResult.TrackIDs[0]})
		st = waitState("playback started via re-resolve + backup CDN", func(s musicPageState) bool {
			return s.Playback.TrackID == impResult.TrackIDs[0] && (s.Playback.Status == "playing" || s.Playback.Status == "ended")
		})
	} else {
		sendOp("download.start", map[string]any{"trackId": impResult.TrackIDs[0]})
		waitState("download completed via re-resolve + backup CDN", func(s musicPageState) bool {
			return s.Downloads[impResult.TrackIDs[0]].Status == "downloaded"
		})
	}
	if playurlCalls.Load() < 2 {
		t.Fatalf("expected playurl re-resolve after expired URL, got %d calls", playurlCalls.Load())
	}

	h.mu.Lock()
	rec := h.records[pluginID]
	var miniWidgetID string
	var miniRev, miniGen uint64
	for _, w := range rec.widgets {
		miniWidgetID, miniRev, miniGen = w.ID, w.Revision, w.Generation
	}
	h.mu.Unlock()
	if miniWidgetID == "" {
		t.Fatal("default desktop mini-player widget not created")
	}
	if hasOutput {
		if err := h.invoke(pluginID, miniGen, "event", v1.Payload{
			WidgetID: miniWidgetID,
			Event: &v1.Event{
				Type:         "widget.action",
				WidgetID:     miniWidgetID,
				ControlID:    "seek",
				ViewRevision: miniRev,
				Generation:   miniGen,
				InputValue:   json.RawMessage("0.5"),
			},
		}); err != nil {
			t.Fatalf("mini-player slider seek failed: %v", err)
		}
		st = readState()
		if st.Playback.PositionMs == 0 && st.Playback.DurationMs > 0 {
			t.Fatalf("expected mini-player slider seek to update positionMs, got %+v", st.Playback)
		}
	}

	b.Close()
	if hasOutput {
		if err := h.invoke(pluginID, 0, "command", v1.Payload{CommandID: "next"}); err != nil {
			t.Fatalf("background next command failed: %v", err)
		}
	}
	if err := b.Open(tab.TabRef, nil); err != nil {
		t.Fatalf("reopen music page: %v", err)
	}
	st = readState()
	if hasOutput {
		st = waitState("reopened page synced to track 2", func(s musicPageState) bool {
			return s.Playback.TrackID == impResult.TrackIDs[1] && (s.Playback.Status == "playing" || s.Playback.Status == "ended")
		})
	}
	if len(st.Playlists) != 1 || st.Playlists[0].Name != "深夜代码 · 精选" || !st.Tracks[0].Favorite {
		t.Fatalf("reopened page lost playlist/favorite state: %+v", st)
	}

	// 5. Verify bundled native DLL music plugin requires zero host permissions and runs as bundled DLL.
	reqPerms, grantedPerms, err := h.PluginPermissions(pluginID)
	if err != nil {
		t.Fatalf("PluginPermissions: %v", err)
	}
	if len(reqPerms) != 0 || len(grantedPerms) != 0 {
		t.Fatalf("expected zero host permissions for self-contained native music DLL: requested=%v granted=%v", reqPerms, grantedPerms)
	}
	h.mu.Lock()
	infoAfter := h.records[pluginID].info
	h.mu.Unlock()
	if !infoAfter.Enabled || !infoAfter.Bundled || infoAfter.Runtime != "dll" || infoAfter.FullSystemAccess {
		t.Fatalf("unexpected SettingsPlugin info for bundled native music DLL: %+v", infoAfter)
	}
}
