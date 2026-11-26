#define QUAILCRATE_PLUGIN_BUILD
#include "quailcrate_plugin.h"

#include "audio_player.hpp"
#include "bvid.hpp"
#include "http_client.hpp"
#include "json.hpp"
#include "media_store.hpp"

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <condition_variable>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <map>
#include <memory>
#include <mutex>
#include <set>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace music {

static constexpr const char* kTabID = "library";
static constexpr const char* kWidgetType = "mini-player";
static constexpr int kStateVersion = 1;

static const std::vector<std::string> kPageActions = {
    "library.refresh",
    "playlist.create",
    "playlist.update",
    "playlist.delete",
    "playlist.addTracks",
    "playlist.removeTrack",
    "track.toggleFavorite",
    "track.delete",
    "download.start",
    "download.remove",
    "view.select",
    "bilibili.loginStart",
    "bilibili.loginCancel",
    "bilibili.loginRefresh",
    "bilibili.logout",
    "bilibili.setQuality",
    "bilibili.preview",
    "bilibili.import",
    "bilibili.clearPreview",
    "playback.playTrack",
    "playback.playPlaylist",
    "playback.toggle",
    "playback.next",
    "playback.prev",
    "playback.stop",
    "playback.seek",
    "playback.setVolume",
    "playback.cycleMode",
    "playback.setMode",
    "queue.add",
    "queue.playNext",
    "queue.remove",
    "queue.clear",
};

struct Track {
    std::string id;
    std::string dedupe_key;
    std::string bvid;
    int64_t aid = 0;
    int64_t cid = 0;
    int page = 1;
    int total_pages = 1;
    std::string title;
    std::string artist = "Bilibili";
    int64_t duration_ms = 0;
    bool favorite = false;
    int64_t added_at = 0;

    Json to_json() const {
        Json o = Json::object();
        o["id"] = id;
        o["dedupeKey"] = dedupe_key;
        o["bvid"] = bvid;
        o["aid"] = aid;
        o["cid"] = cid;
        o["page"] = page;
        o["totalPages"] = total_pages;
        o["title"] = title;
        o["artist"] = artist;
        o["durationMs"] = duration_ms;
        o["favorite"] = favorite;
        o["addedAt"] = added_at;
        return o;
    }
};

struct Playlist {
    std::string id;
    std::string name;
    std::string desc;
    std::string color = "accent";
    std::vector<std::string> track_ids;

    Json to_json() const {
        Json o = Json::object();
        o["id"] = id;
        o["name"] = name;
        o["desc"] = desc;
        o["color"] = color;
        Json tids = Json::array();
        for (const auto& tid : track_ids) tids.push_back(tid);
        o["trackIds"] = std::move(tids);
        return o;
    }
};

struct PersistedState {
    int64_t next_playlist_id = 1;
    int64_t next_track_id = 1;
    std::string active_view = "smart_all";
    std::string sort_by = "custom";
    std::string preferred_quality = "192k";
    std::string play_mode = "sequence";
    double volume = 0.8;
    bool muted = false;
    std::vector<Playlist> playlists;
    std::vector<Track> tracks;
    std::vector<std::string> recent_ids;
    std::vector<std::string> queue;
    std::string last_track_id;
    int64_t last_position_ms = 0;
    int64_t last_duration_ms = 0;
    bili::AccountInfo account;
    std::string api_base;
    int64_t poll_delay_ms = 0;
};

struct LoginRuntime {
    uint64_t flow_id = 0;
    std::string phase = "idle";
    std::string qr_key;
    std::string qr_url;
    std::string message;
};

struct ImportPreviewState {
    bool present = false;
    std::string status = "ready";
    std::string input;
    std::string source_title;
    std::vector<bili::PreviewTrack> items;
    std::vector<std::string> errors;
};

struct PlaybackRuntime {
    uint64_t play_seq = 0;
    uint64_t audio_session_id = 0;
    uint64_t shuffle_seed = 0;
    bool has_shuffle_seed = false;
    std::string track_id;
    std::string status = "idle";
    int64_t position_ms = 0;
    int64_t duration_ms = 0;
    bool seekable = false;
    std::string quality_label;
    std::string error;
    int64_t resume_position_ms = 0;
    std::shared_ptr<std::atomic<bool>> cancel_flag;
};

struct NoticeState {
    bool present = false;
    std::string kind = "info";
    std::string message;
};

struct ResolvedMedia {
    bool ok = false;
    std::vector<bili::AudioCandidate> candidates;
    int64_t duration_ms = 0;
    std::string error;
};

struct ActiveResolve {
    std::mutex mu;
    std::condition_variable cv;
    bool done = false;
    ResolvedMedia result;
};

inline std::wstring get_env_wide(const wchar_t* name) {
    DWORD n = GetEnvironmentVariableW(name, nullptr, 0);
    if (n == 0) return L"";
    std::wstring val(n, L'\0');
    DWORD got = GetEnvironmentVariableW(name, &val[0], n);
    val.resize(got);
    return val;
}

inline std::string fmt_ms(int64_t ms) {
    int64_t total = std::max<int64_t>(0, ms) / 1000;
    int64_t mins = total / 60;
    int64_t secs = total % 60;
    char buf[32];
    std::snprintf(buf, sizeof(buf), "%02lld:%02lld", static_cast<long long>(mins), static_cast<long long>(secs));
    return buf;
}

inline double clamp_d(double v, double lo, double hi) {
    return std::max(lo, std::min(hi, v));
}

inline std::string utf8_prefix_bytes(const std::string& s, size_t max_bytes) {
    if (s.size() <= max_bytes) return s;
    size_t n = max_bytes;
    while (n > 0 && (static_cast<unsigned char>(s[n]) & 0xC0) == 0x80) --n;
    return s.substr(0, n);
}

class MusicEngine {
public:
    MusicEngine()
        : m_player([this](const AudioProgress& prog) { on_audio_progress(prog); }) {
        configure_data_dir();
    }

    ~MusicEngine() {
        shutdown();
    }

    void bind_host(quailcrate_host_notify_fn fn) {
        std::lock_guard<std::mutex> lock(m_notify_mu);
        m_notify = fn;
    }

    std::string handle_request(const uint8_t* data, uint32_t len) {
        Json req;
        if (!Json::parse(reinterpret_cast<const char*>(data), len, req) || !req.is_object()) {
            return "";
        }
        std::string req_id = req.str("requestId");
        std::string op = req.str("op");
        const Json& payload = req.get("payload");

        if (op == "init") {
            std::lock_guard<std::mutex> lock(m_mu);
            m_locale = payload.str("locale", "zh-CN");
            sync_state_from_payload_locked(payload);
            refresh_downloads_from_store_locked();
            return ok_response(req_id, build_init_data(payload));
        }

        if (op == "stop") {
            shutdown();
            return ok_response(req_id, Json::object());
        }

        if (op == "tab_state") {
            std::lock_guard<std::mutex> lock(m_mu);
            m_locale = payload.str("locale", m_locale);
            sync_state_from_payload_locked(payload);
            Json d = Json::object();
            d["pageState"] = build_page_state_locked(payload);
            return ok_response(req_id, std::move(d));
        }

        if (op == "widget_state") {
            std::lock_guard<std::mutex> lock(m_mu);
            m_locale = payload.str("locale", m_locale);
            sync_state_from_payload_locked(payload);
            Json d = Json::object();
            d["view"] = build_mini_player_view_locked(payload);
            return ok_response(req_id, std::move(d));
        }

        if (op == "command") {
            std::lock_guard<std::mutex> lock(m_mu);
            m_locale = payload.str("locale", m_locale);
            sync_state_from_payload_locked(payload);
            int64_t unix_time = current_unix(payload);
            exec_playback_op_locked(payload.str("commandId"), payload.get("args"), unix_time, payload);
            Json d = Json::object();
            d["state"] = serialize_and_record_state_locked();
            Json actions = Json::array();
            Json wref = Json::object();
            wref["id"] = next_id_locked("wref");
            wref["type"] = "widget.refresh";
            actions.push_back(std::move(wref));
            d["actions"] = std::move(actions);
            return ok_response(req_id, std::move(d));
        }

        if (op == "event") {
            std::lock_guard<std::mutex> lock(m_mu);
            m_locale = payload.str("locale", m_locale);
            sync_state_from_payload_locked(payload);
            const Json& ev = payload.get("event");
            std::string ev_type = ev.str("type");
            int64_t unix_time = current_unix(payload);
            bool dirty = false;
            Json page_reply;
            bool has_page_reply = false;

            if (ev_type == "page.message") {
                page_reply = handle_page_message_locked(ev.str("controlId"), ev.get("detail"), unix_time, payload, dirty);
                has_page_reply = true;
            } else if (ev_type == "widget.action") {
                std::string cid = ev.str("controlId");
                if (cid == "seek" && !ev.get("inputValue").is_null()) {
                    Json seek_args = Json::object();
                    seek_args["ratio"] = ev.get("inputValue").as_num(0.0);
                    exec_playback_op_locked("seek", seek_args, unix_time, payload);
                } else {
                    exec_playback_op_locked(cid, Json::object(), unix_time, payload);
                }
                dirty = true;
            }

            Json d = Json::object();
            if (dirty) {
                d["state"] = serialize_and_record_state_locked();
                Json actions = Json::array();
                Json wref = Json::object();
                wref["id"] = next_id_locked("wref");
                wref["type"] = "widget.refresh";
                actions.push_back(std::move(wref));
                d["actions"] = std::move(actions);
            }
            if (has_page_reply) {
                d["pageReply"] = std::move(page_reply);
            }
            return ok_response(req_id, std::move(d));
        }

        Json err = Json::object();
        err["code"] = "unsupported_op";
        err["message"] = "unsupported operation: " + op;
        Json resp = Json::object();
        resp["requestId"] = req_id;
        resp["ok"] = false;
        resp["error"] = std::move(err);
        return resp.dump();
    }

private:
    mutable std::mutex m_mu;
    std::mutex m_notify_mu;
    quailcrate_host_notify_fn m_notify = nullptr;

    struct WorkerSlot {
        std::atomic<bool> done{ false };
        std::thread th;
    };

    std::atomic<bool> m_shutting_down{ false };
    std::condition_variable m_login_cv;
    std::mutex m_threads_mu;
    std::vector<std::unique_ptr<WorkerSlot>> m_workers;

    HttpClient m_http;
    MediaStore m_store;
    NativeAudioPlayer m_player;

    PersistedState m_state;
    int64_t m_state_rev = 0;
    std::string m_last_host_state_json;
    std::string m_locale = "zh-CN";
    uint64_t m_action_seq = 0;

    bool m_checked_nav = false;
    std::string m_wbi_key;
    int64_t m_wbi_fetched_at = 0;

    LoginRuntime m_login;
    ImportPreviewState m_preview;
    uint64_t m_import_job_id = 0;
    NoticeState m_notice;
    PlaybackRuntime m_play;

    std::map<std::string, DownloadStatus> m_downloads;
    std::map<std::string, std::shared_ptr<std::atomic<bool>>> m_download_cancels;
    std::map<std::string, std::shared_ptr<ActiveResolve>> m_resolving;

    std::string m_prefetch_track_id;
    std::string m_prefetch_quality;
    std::shared_ptr<std::atomic<bool>> m_prefetch_cancel;

    void configure_data_dir() {
        std::wstring data_dir = get_env_wide(L"QUAILCRATE_PLUGIN_DATA_DIR");
        if (data_dir.empty()) {
            std::wstring plug_dir = get_env_wide(L"QUAILCRATE_PLUGIN_DIR");
            if (!plug_dir.empty()) data_dir = plug_dir + L"\\data";
            else data_dir = L".\\data\\plugins\\quailcrate.music";
        }
        ensure_dir_recursive(data_dir);
        m_http.cookies().set_storage_path(data_dir + L"\\cookies.dat");
        m_store.configure(data_dir);
    }

    void shutdown() {
        if (m_shutting_down.exchange(true)) return;
        uint64_t stop_seq;
        {
            std::lock_guard<std::mutex> lock(m_mu);
            ++m_login.flow_id;
            ++m_import_job_id;
            stop_seq = ++m_play.play_seq;
            if (m_play.cancel_flag) m_play.cancel_flag->store(true);
            if (m_prefetch_cancel) m_prefetch_cancel->store(true);
            for (auto& kv : m_download_cancels) {
                if (kv.second) kv.second->store(true);
            }
        }
        m_login_cv.notify_all();
        m_store.cancel_all_transfers();
        m_player.stop_through(stop_seq);

        std::vector<std::unique_ptr<WorkerSlot>> to_join;
        {
            std::lock_guard<std::mutex> lock(m_threads_mu);
            to_join.swap(m_workers);
        }
        for (auto& slot : to_join) {
            if (slot && slot->th.joinable()) slot->th.join();
        }
    }

    void spawn_worker(std::function<void()> fn) {
        if (m_shutting_down.load()) return;
        std::lock_guard<std::mutex> lock(m_threads_mu);
        if (m_shutting_down.load()) return;
        for (auto it = m_workers.begin(); it != m_workers.end(); ) {
            if ((*it)->done.load()) {
                if ((*it)->th.joinable()) (*it)->th.join();
                it = m_workers.erase(it);
            } else {
                ++it;
            }
        }
        auto slot = std::make_unique<WorkerSlot>();
        WorkerSlot* raw = slot.get();
        slot->th = std::thread([fn = std::move(fn), raw]() {
            fn();
            raw->done.store(true);
        });
        m_workers.push_back(std::move(slot));
    }

    void emit_notify(const Json& note) {
        if (m_shutting_down.load()) return;
        quailcrate_host_notify_fn fn = nullptr;
        {
            std::lock_guard<std::mutex> lock(m_notify_mu);
            fn = m_notify;
        }
        if (!fn) return;
        std::string payload = note.dump();
        fn(reinterpret_cast<const uint8_t*>(payload.data()), static_cast<uint32_t>(payload.size()));
    }

    void push_page_notify() {
        Json n = Json::object();
        n["type"] = "page.notify";
        n["tabId"] = kTabID;
        emit_notify(n);
    }

    void push_widget_and_page(bool persist_state) {
        if (persist_state) {
            Json st;
            {
                std::lock_guard<std::mutex> lock(m_mu);
                st = serialize_and_record_state_locked();
            }
            Json n = Json::object();
            n["type"] = "state.set";
            n["state"] = std::move(st);
            emit_notify(n);
            return;
        }
        Json p = Json::object();
        p["type"] = "page.notify";
        p["tabId"] = kTabID;
        emit_notify(p);

        Json w = Json::object();
        w["type"] = "widget.refresh";
        w["widgetId"] = kWidgetType;
        emit_notify(w);
    }

    static std::string ok_response(const std::string& req_id, Json data) {
        Json r = Json::object();
        r["requestId"] = req_id;
        r["ok"] = true;
        r["data"] = std::move(data);
        return r.dump();
    }

    static int64_t current_unix(const Json& payload) {
        int64_t t = payload.i64("unixTime", 0);
        if (t > 0) return t;
        return std::chrono::duration_cast<std::chrono::seconds>(
                   std::chrono::system_clock::now().time_since_epoch())
            .count();
    }

    static bool is_zh(const Json& payload, const std::string& fallback_locale) {
        std::string loc = payload.str("locale", fallback_locale);
        return loc == "zh-CN";
    }

    static std::string tr_loc(const std::string& locale, const std::string& zh, const std::string& en) {
        return locale == "zh-CN" ? zh : en;
    }

    static std::string str_t(const Json& payload, const std::string& key, const std::string& fallback) {
        const Json& strings = payload.get("strings");
        if (strings.is_object()) {
            std::string v = strings.str(key);
            if (!v.empty()) return v;
        }
        return fallback;
    }

    std::string next_id_locked(const std::string& prefix) {
        return prefix + "-" + std::to_string(++m_action_seq);
    }

    void refresh_downloads_from_store_locked() {
        auto disk = m_store.downloaded_snapshot();
        for (const auto& kv : disk) {
            if (m_download_cancels.find(kv.first) == m_download_cancels.end()) {
                m_downloads[kv.first] = kv.second;
            }
        }
    }

    void sync_state_from_payload_locked(const Json& payload) {
        const Json& raw = payload.get("state");
        if (!raw.is_object() || raw.as_object().empty()) return;
        int64_t host_rev = raw.i64("_rev", 0);
        if (host_rev > 0) {
            if (host_rev <= m_state_rev) return;
            load_state_locked(raw);
            m_state_rev = host_rev;
            return;
        }
        // External / legacy / test-injected state without _rev:
        // only load before the DLL has committed its own revisions.
        if (m_state_rev > 0) return;
        std::string dumped = raw.dump();
        if (dumped == m_last_host_state_json) return;
        load_state_locked(raw);
        m_last_host_state_json = dumped;
    }

    void load_state_locked(const Json& s) {
        PersistedState out;
        out.next_playlist_id = std::max<int64_t>(1, s.i64("nextPlaylistId", 1));
        out.next_track_id = std::max<int64_t>(1, s.i64("nextTrackId", 1));
        out.active_view = s.str("activeView", "smart_all");
        if (out.active_view.empty()) out.active_view = "smart_all";
        out.sort_by = s.str("sortBy", "custom");
        if (out.sort_by.empty()) out.sort_by = "custom";
        out.preferred_quality = s.str("preferredQuality", "192k");
        if (out.preferred_quality.empty()) out.preferred_quality = "192k";
        out.play_mode = s.str("playMode", "sequence");
        if (out.play_mode.empty()) out.play_mode = "sequence";
        out.volume = clamp_d(s.num("volume", 0.8), 0.0, 1.0);
        out.muted = s.boolean("muted", false);
        out.last_track_id = s.str("lastTrackId");
        out.last_position_ms = std::max<int64_t>(0, s.i64("lastPositionMs", 0));
        out.last_duration_ms = std::max<int64_t>(0, s.i64("lastDurationMs", 0));
        out.api_base = s.str("_apiBase");
        out.poll_delay_ms = s.i64("_pollDelayMs", 0);

        if (s.get("account").is_object()) {
            const Json& a = s.get("account");
            out.account.logged_in = a.boolean("loggedIn", false);
            out.account.nickname = a.str("nickname");
            out.account.mid = a.i64("mid", 0);
            out.account.level = static_cast<int>(a.i64("level", 0));
            out.account.vip = a.boolean("vip", false);
        }

        std::set<std::string> valid_tracks;
        if (s.get("tracks").is_array()) {
            for (const Json& t : s.get("tracks").as_array()) {
                if (!t.is_object()) continue;
                std::string id = t.str("id");
                if (id.empty()) continue;
                int pnum = static_cast<int>(t.i64("page", 1));
                std::string bv = t.str("bvid");
                std::string dedupe = t.str("dedupeKey");
                if (dedupe.empty()) dedupe = bv + ":p" + std::to_string(pnum);
                Track tr;
                tr.id = id;
                tr.dedupe_key = dedupe;
                tr.bvid = bv;
                tr.aid = t.i64("aid", 0);
                tr.cid = t.i64("cid", 0);
                tr.page = pnum;
                tr.total_pages = static_cast<int>(t.i64("totalPages", 1));
                tr.title = t.str("title", bv);
                tr.artist = t.str("artist", "Bilibili");
                tr.duration_ms = std::max<int64_t>(0, t.i64("durationMs", 0));
                tr.favorite = t.boolean("favorite", false);
                tr.added_at = t.i64("addedAt", 0);
                valid_tracks.insert(tr.id);
                out.tracks.push_back(std::move(tr));
            }
        }

        if (s.get("playlists").is_array()) {
            for (const Json& pl : s.get("playlists").as_array()) {
                if (!pl.is_object()) continue;
                std::string id = pl.str("id");
                if (id.empty()) continue;
                Playlist p;
                p.id = id;
                p.name = pl.str("name", "歌单");
                p.desc = pl.str("desc");
                p.color = pl.str("color", "accent");
                std::set<std::string> seen;
                if (pl.get("trackIds").is_array()) {
                    for (const Json& tid : pl.get("trackIds").as_array()) {
                        if (tid.is_string() && valid_tracks.count(tid.as_str()) && seen.insert(tid.as_str()).second) {
                            p.track_ids.push_back(tid.as_str());
                        }
                    }
                }
                out.playlists.push_back(std::move(p));
            }
        }

        auto load_id_list = [&](const char* key, std::vector<std::string>& dst) {
            std::set<std::string> seen;
            if (s.get(key).is_array()) {
                for (const Json& tid : s.get(key).as_array()) {
                    if (tid.is_string() && valid_tracks.count(tid.as_str()) && seen.insert(tid.as_str()).second) {
                        dst.push_back(tid.as_str());
                    }
                }
            }
        };
        load_id_list("recentIds", out.recent_ids);
        load_id_list("queue", out.queue);

        if (!out.last_track_id.empty() && !valid_tracks.count(out.last_track_id)) {
            out.last_track_id.clear();
            out.last_position_ms = 0;
            out.last_duration_ms = 0;
        }

        m_state = std::move(out);
        m_player.set_volume(effective_volume_locked());
        if (m_play.track_id.empty() && !m_state.last_track_id.empty() && m_play.status == "idle") {
            m_play.track_id = m_state.last_track_id;
            m_play.position_ms = m_state.last_position_ms;
            m_play.duration_ms = m_state.last_duration_ms;
            m_play.status = "paused";
        }
        if (m_state.account.logged_in && m_login.phase == "idle") {
            m_login.phase = "logged_in";
        }
    }

    Json serialize_state_locked() const {
        Json out = Json::object();
        out["version"] = kStateVersion;
        out["nextPlaylistId"] = m_state.next_playlist_id;
        out["nextTrackId"] = m_state.next_track_id;
        out["activeView"] = m_state.active_view;
        out["sortBy"] = m_state.sort_by;
        out["preferredQuality"] = m_state.preferred_quality;
        out["playMode"] = m_state.play_mode;
        out["volume"] = m_state.volume;
        out["muted"] = m_state.muted;

        Json playlists = Json::array();
        for (const auto& pl : m_state.playlists) playlists.push_back(pl.to_json());
        out["playlists"] = std::move(playlists);

        Json tracks = Json::array();
        for (const auto& tr : m_state.tracks) tracks.push_back(tr.to_json());
        out["tracks"] = std::move(tracks);

        Json recents = Json::array();
        for (const auto& id : m_state.recent_ids) recents.push_back(id);
        out["recentIds"] = std::move(recents);

        Json queue = Json::array();
        for (const auto& id : m_state.queue) queue.push_back(id);
        out["queue"] = std::move(queue);

        out["lastTrackId"] = m_state.last_track_id;
        out["lastPositionMs"] = m_state.last_position_ms;
        out["lastDurationMs"] = m_state.last_duration_ms;
        out["account"] = m_state.account.to_json();
        if (!m_state.api_base.empty()) out["_apiBase"] = m_state.api_base;
        if (m_state.poll_delay_ms > 0) out["_pollDelayMs"] = m_state.poll_delay_ms;
        return out;
    }

    Json serialize_and_record_state_locked() {
        ++m_state_rev;
        Json st = serialize_state_locked();
        st["_rev"] = m_state_rev;
        return st;
    }

    Track* find_track_locked(const std::string& id) {
        for (auto& t : m_state.tracks) {
            if (t.id == id) return &t;
        }
        return nullptr;
    }

    const Track* find_track_const_locked(const std::string& id) const {
        for (const auto& t : m_state.tracks) {
            if (t.id == id) return &t;
        }
        return nullptr;
    }

    Track* find_track_by_dedupe_locked(const std::string& key) {
        for (auto& t : m_state.tracks) {
            if (t.dedupe_key == key) return &t;
        }
        return nullptr;
    }

    Playlist* find_playlist_locked(const std::string& id) {
        for (auto& pl : m_state.playlists) {
            if (pl.id == id) return &pl;
        }
        return nullptr;
    }

    static void remove_str(std::vector<std::string>& v, const std::string& val) {
        v.erase(std::remove(v.begin(), v.end(), val), v.end());
    }

    static int index_of_str(const std::vector<std::string>& v, const std::string& val) {
        for (size_t i = 0; i < v.size(); ++i) {
            if (v[i] == val) return static_cast<int>(i);
        }
        return -1;
    }

    void push_recent_locked(const std::string& tid) {
        remove_str(m_state.recent_ids, tid);
        m_state.recent_ids.insert(m_state.recent_ids.begin(), tid);
        if (m_state.recent_ids.size() > 50) m_state.recent_ids.resize(50);
    }

    double effective_volume_locked() const {
        return m_state.muted ? 0.0 : clamp_d(m_state.volume, 0.0, 1.0);
    }

    std::string pick_next_track_locked(int direction, int64_t unix_time) {
        if (m_state.queue.empty()) {
            return m_state.tracks.empty() ? "" : m_state.tracks.front().id;
        }
        std::string cur = !m_play.track_id.empty() ? m_play.track_id : m_state.last_track_id;
        int idx = index_of_str(m_state.queue, cur);
        if (idx < 0) return m_state.queue.front();

        int n = static_cast<int>(m_state.queue.size());
        if (m_state.play_mode == "shuffle" && n > 1) {
            uint64_t seed = 0;
            if (direction == 1) {
                if (!m_play.has_shuffle_seed) {
                    m_play.shuffle_seed = static_cast<uint64_t>(unix_time > 0 ? unix_time : 1) + m_action_seq * 31ULL;
                    m_play.has_shuffle_seed = true;
                }
                seed = m_play.shuffle_seed + static_cast<uint64_t>(idx + 1) * 17ULL;
            } else {
                seed = static_cast<uint64_t>(unix_time > 0 ? unix_time : 1) + m_action_seq * 31ULL +
                       static_cast<uint64_t>(idx + 1) * 17ULL;
            }
            int pick = static_cast<int>(seed % static_cast<uint64_t>(n - 1));
            if (pick >= idx) ++pick;
            return m_state.queue[static_cast<size_t>(pick)];
        }
        int next = idx + direction;
        if (next >= n) next = 0;
        if (next < 0) next = n - 1;
        return m_state.queue[static_cast<size_t>(next)];
    }

    Json build_init_data(const Json& payload) const {
        Json data = Json::object();
        Json tabs = Json::array();
        Json tab = Json::object();
        tab["id"] = kTabID;
        tab["title"] = str_t(payload, "app.title", "音乐库");
        tab["icon"] = "music";
        tab["page"] = "web/index.html";
        Json pa = Json::array();
        for (const auto& a : kPageActions) pa.push_back(a);
        tab["pageActions"] = std::move(pa);
        tabs.push_back(std::move(tab));
        data["appTabs"] = std::move(tabs);

        Json widgets = Json::array();
        Json w = Json::object();
        w["id"] = kWidgetType;
        w["title"] = str_t(payload, "widget.title", "迷你播放器");
        w["width"] = 320;
        w["height"] = 148;
        w["default"] = true;
        widgets.push_back(std::move(w));
        data["widgets"] = std::move(widgets);

        Json commands = Json::array();
        auto add_cmd = [&](const char* id, const char* key, const char* fb) {
            Json c = Json::object();
            c["id"] = id;
            c["title"] = str_t(payload, key, fb);
            commands.push_back(std::move(c));
        };
        add_cmd("play_pause", "cmd.playPause", "音乐：播放 / 暂停");
        add_cmd("next", "cmd.next", "音乐：下一首");
        add_cmd("prev", "cmd.prev", "音乐：上一首");
        add_cmd("stop", "cmd.stop", "音乐：停止播放");
        add_cmd("cycle_mode", "cmd.cycleMode", "音乐：切换循环模式");
        data["commands"] = std::move(commands);

        Json tray = Json::array();
        Json sep = Json::object();
        sep["separator"] = true;
        sep["order"] = 60;
        tray.push_back(std::move(sep));

        auto add_tray = [&](const char* id, const char* key, const char* fb, const char* icon, const char* cmd, int order) {
            Json t = Json::object();
            t["id"] = id;
            t["title"] = str_t(payload, key, fb);
            t["icon"] = icon;
            t["intent"] = "command";
            t["command"] = cmd;
            t["order"] = order;
            tray.push_back(std::move(t));
        };
        add_tray("music_play", "tray.playPause", "音乐：播放 / 暂停", "music", "play_pause", 61);
        add_tray("music_prev", "tray.prev", "音乐：上一首", "skip-prev", "prev", 62);
        add_tray("music_next", "tray.next", "音乐：下一首", "skip-next", "next", 63);
        data["tray"] = std::move(tray);
        return data;
    }

    Json build_page_state_locked(const Json& payload) const {
        Json out = Json::object();
        out["version"] = kStateVersion;
        out["locale"] = payload.str("locale", m_locale);
        out["activeView"] = m_state.active_view;
        out["sortBy"] = m_state.sort_by;
        out["preferredQuality"] = m_state.preferred_quality;
        out["account"] = m_state.account.to_json();

        Json login = Json::object();
        login["phase"] = m_login.phase;
        login["qrUrl"] = m_login.qr_url;
        login["message"] = m_login.message;
        out["login"] = std::move(login);

        Json playlists = Json::array();
        for (const auto& pl : m_state.playlists) playlists.push_back(pl.to_json());
        out["playlists"] = std::move(playlists);

        Json downloads = Json::object();
        for (const auto& kv : m_downloads) {
            if (!find_track_const_locked(kv.first)) continue;
            Json rec = Json::object();
            rec["status"] = kv.second.status;
            if (kv.second.has_percent) rec["percent"] = kv.second.percent;
            if (!kv.second.error.empty()) rec["error"] = kv.second.error;
            downloads[kv.first] = std::move(rec);
        }
        out["downloads"] = std::move(downloads);

        Json tracks = Json::array();
        for (const auto& tr : m_state.tracks) tracks.push_back(tr.to_json());
        out["tracks"] = std::move(tracks);

        Json recents = Json::array();
        for (const auto& id : m_state.recent_ids) recents.push_back(id);
        out["recentIds"] = std::move(recents);

        Json queue = Json::array();
        for (const auto& id : m_state.queue) queue.push_back(id);
        out["queue"] = std::move(queue);

        const Track* cur_track = m_play.track_id.empty() ? nullptr : find_track_const_locked(m_play.track_id);
        int64_t duration = m_play.duration_ms > 0 ? m_play.duration_ms : (cur_track ? cur_track->duration_ms : 0);

        Json playback = Json::object();
        playback["trackId"] = m_play.track_id;
        playback["status"] = m_play.status;
        playback["positionMs"] = m_play.position_ms;
        playback["durationMs"] = duration;
        playback["seekable"] = m_play.seekable;
        playback["volume"] = m_state.volume;
        playback["muted"] = m_state.muted;
        playback["mode"] = m_state.play_mode;
        playback["qualityLabel"] = m_play.quality_label;
        playback["error"] = m_play.error;
        out["playback"] = std::move(playback);

        if (m_preview.present) {
            Json prev = Json::object();
            prev["status"] = m_preview.status;
            prev["input"] = m_preview.input;
            prev["sourceTitle"] = m_preview.source_title;
            Json items = Json::array();
            for (const auto& it : m_preview.items) {
                Json item = Json::object();
                item["dedupeKey"] = it.dedupe_key;
                item["bvid"] = it.bvid;
                item["aid"] = it.aid;
                item["cid"] = it.cid;
                item["page"] = it.page;
                item["totalPages"] = it.total_pages;
                item["title"] = it.title;
                item["artist"] = it.artist;
                item["durationMs"] = it.duration_ms;
                item["selected"] = it.selected;
                bool exists = false;
                for (const auto& tr : m_state.tracks) {
                    if (tr.dedupe_key == it.dedupe_key) {
                        exists = true;
                        break;
                    }
                }
                item["alreadyInLibrary"] = exists;
                items.push_back(std::move(item));
            }
            prev["items"] = std::move(items);
            Json errs = Json::array();
            for (const auto& e : m_preview.errors) errs.push_back(e);
            prev["errors"] = std::move(errs);
            out["importPreview"] = std::move(prev);
        } else {
            out["importPreview"] = nullptr;
        }

        if (m_notice.present) {
            Json n = Json::object();
            n["kind"] = m_notice.kind;
            n["message"] = m_notice.message;
            out["notice"] = std::move(n);
        } else {
            out["notice"] = nullptr;
        }
        return out;
    }

    static Json ui_node(const std::string& type, const std::string& id, Json props, Json events = Json(), Json children = Json()) {
        Json n = Json::object();
        n["type"] = type;
        n["id"] = id;
        if (props.is_object() && !props.as_object().empty()) n["props"] = std::move(props);
        if (events.is_object() && !events.as_object().empty()) n["events"] = std::move(events);
        if (children.is_array() && !children.as_array().empty()) n["children"] = std::move(children);
        return n;
    }

    static Json ui_button(const std::string& id, const std::string& text, const std::string& icon,
                          const std::string& action, const std::string& variant = "") {
        Json props = Json::object();
        if (!text.empty()) props["text"] = text;
        if (!icon.empty()) props["icon"] = icon;
        if (!variant.empty()) props["variant"] = variant;
        Json events = Json::object();
        events["activate"] = action;
        return ui_node("button", id, std::move(props), std::move(events));
    }

    Json build_mini_player_view_locked(const Json& payload) const {
        const Track* track = m_play.track_id.empty() ? nullptr : find_track_const_locked(m_play.track_id);
        std::string title_text = str_t(payload, "widget.empty", "暂未播放曲目");
        std::string sub_text = str_t(payload, "widget.emptySub", "在应用 → 音乐库中导入 BV 号或收藏夹");
        std::string loc = payload.str("locale", m_locale);
        int64_t duration = m_play.duration_ms;
        double ratio = 0.0;

        if (track) {
            title_text = track->title;
            if (duration <= 0) duration = track->duration_ms;
            if (duration > 0) {
                ratio = clamp_d(static_cast<double>(m_play.position_ms) / static_cast<double>(duration), 0.0, 1.0);
            }
            if (m_play.status == "loading") {
                sub_text = track->artist + " · " + tr_loc(loc, "缓冲中…", "Loading…");
            } else if (m_play.status == "error" && !m_play.error.empty()) {
                sub_text = m_play.error;
            } else {
                sub_text = track->artist + "  ·  " + fmt_ms(m_play.position_ms) + " / " + fmt_ms(duration);
            }
        }

        bool playing = (m_play.status == "playing");
        std::string mode_label = str_t(payload, "mode." + m_state.play_mode, m_state.play_mode);
        std::string mode_icon = (m_state.play_mode == "shuffle") ? "shuffle" : "repeat";

        Json row_children = Json::array();
        row_children.push_back(ui_button("prev", tr_loc(loc, "上一首", "Prev"), "skip-prev", "prev"));
        row_children.push_back(ui_button("play_pause",
                                         playing ? tr_loc(loc, "暂停", "Pause") : tr_loc(loc, "播放", "Play"),
                                         playing ? "pause" : "play", "play_pause", "accent"));
        row_children.push_back(ui_button("next", tr_loc(loc, "下一首", "Next"), "skip-next", "next"));
        row_children.push_back(ui_button("cycle_mode", mode_label, mode_icon, "cycle_mode"));

        Json row_props = Json::object();
        row_props["gap"] = 6;
        row_props["height"] = 28;
        Json controls_row = ui_node("row", "transport", std::move(row_props), Json(), std::move(row_children));

        Json slider_props = Json::object();
        slider_props["value"] = ratio;
        slider_props["height"] = 22;
        Json slider_events = Json::object();
        slider_events["submit"] = "seek";
        Json slider = ui_node("slider", "position", std::move(slider_props), std::move(slider_events));

        Json title_props = Json::object();
        title_props["text"] = title_text;
        if (playing) title_props["variant"] = "accent";
        title_props["height"] = 20;

        Json sub_props = Json::object();
        sub_props["text"] = sub_text;
        sub_props["variant"] = (m_play.status == "error") ? "warning" : "muted";
        sub_props["height"] = 18;

        Json root_children = Json::array();
        root_children.push_back(ui_node("text", "track_title", std::move(title_props)));
        root_children.push_back(ui_node("text", "track_sub", std::move(sub_props)));
        root_children.push_back(std::move(slider));
        root_children.push_back(std::move(controls_row));

        Json root_props = Json::object();
        root_props["gap"] = 5;
        Json root = ui_node("column", "mini_root", std::move(root_props), Json(), std::move(root_children));

        Json view = Json::object();
        view["uiVersion"] = 2;
        view["title"] = str_t(payload, "widget.title", "迷你播放器");
        view["root"] = std::move(root);
        return view;
    }

    // --- Background Resolution & Transfer ---

    std::string friendly_error(const std::string& code, const std::string& locale) const {
        if (code == "timeout") return tr_loc(locale, "请求超时，请检查网络连接后重试。", "Request timed out. Please retry.");
        if (code == "canceled") return tr_loc(locale, "操作已取消。", "Operation canceled.");
        if (code == "disk_full") return tr_loc(locale, "磁盘空间不足，请释放空间后重试。", "The disk is full. Free some space and retry.");
        if (code == "storage_unavailable") return tr_loc(locale, "无法写入音乐文件，请检查数据目录后重试。", "Cannot save the audio file. Check the data directory and retry.");
        return tr_loc(locale, "操作失败（" + code + "）", "Operation failed (" + code + ")");
    }

    static bool is_retryable_error(const std::string& code) {
        return code != "disk_full" && code != "storage_unavailable" && code != "canceled" && code != "invalid_url";
    }

    ResolvedMedia resolve_media_blocking(Track track, const std::string& quality, const std::string& api_base,
                                         const std::atomic<bool>& cancel, bool force_refresh) {
        std::string key = track.id + ":" + quality;
        std::shared_ptr<ActiveResolve> existing;
        bool owner = false;
        if (!force_refresh) {
            std::lock_guard<std::mutex> lock(m_mu);
            auto it = m_resolving.find(key);
            if (it != m_resolving.end()) {
                existing = it->second;
            } else {
                existing = std::make_shared<ActiveResolve>();
                m_resolving[key] = existing;
                owner = true;
            }
        } else {
            existing = std::make_shared<ActiveResolve>();
            owner = true;
        }

        if (!owner) {
            std::unique_lock<std::mutex> lk(existing->mu);
            existing->cv.wait(lk, [&]() { return existing->done || cancel.load() || m_shutting_down.load(); });
            if (existing->done) return existing->result;
            return { false, {}, 0, "canceled" };
        }

        ResolvedMedia res;
        auto finish = [&]() {
            {
                std::lock_guard<std::mutex> lk(existing->mu);
                existing->result = res;
                existing->done = true;
            }
            existing->cv.notify_all();
            if (!force_refresh) {
                std::lock_guard<std::mutex> lock(m_mu);
                auto it = m_resolving.find(key);
                if (it != m_resolving.end() && it->second == existing) m_resolving.erase(it);
            }
        };

        // 1. Resolve missing cid via /x/web-interface/view
        if (track.cid <= 0) {
            HttpResponse vr = m_http.get(bili::view_url(track.bvid, track.aid, api_base), &cancel);
            Json vj;
            std::vector<bili::PreviewTrack> items;
            std::string verr;
            if (!vr.ok || !Json::parse(vr.body, vj) || !bili::parse_view(vj, track.page, items, verr) || items.empty()) {
                res.error = !verr.empty() ? verr : (!vr.error_code.empty() ? vr.error_code : "network_error");
                finish();
                return res;
            }
            bili::PreviewTrack matched = items.front();
            for (const auto& item : items) {
                if (item.page == track.page) {
                    matched = item;
                    break;
                }
            }
            track.cid = matched.cid;
            if (matched.duration_ms > 0) track.duration_ms = matched.duration_ms;
            {
                std::lock_guard<std::mutex> lock(m_mu);
                if (Track* t = find_track_locked(track.id)) {
                    t->cid = track.cid;
                    if (t->duration_ms == 0 && matched.duration_ms > 0) t->duration_ms = matched.duration_ms;
                }
            }
        }

        // 2. Refresh WBI key if missing or > 2 hours old
        std::string wbi_key;
        int64_t now = std::chrono::duration_cast<std::chrono::seconds>(
                          std::chrono::system_clock::now().time_since_epoch())
                          .count();
        {
            std::lock_guard<std::mutex> lock(m_mu);
            if (!m_wbi_key.empty() && (now - m_wbi_fetched_at) <= 7200) {
                wbi_key = m_wbi_key;
            }
        }
        if (wbi_key.empty()) {
            HttpResponse nr = m_http.get(bili::nav_url(api_base), &cancel);
            Json nj;
            bili::NavResult parsed;
            if (nr.ok && Json::parse(nr.body, nj) && bili::parse_nav(nj, parsed)) {
                std::lock_guard<std::mutex> lock(m_mu);
                if (!parsed.wbi_key_str.empty()) {
                    m_wbi_key = parsed.wbi_key_str;
                    m_wbi_fetched_at = now;
                    wbi_key = m_wbi_key;
                }
                m_state.account = parsed.account;
                if (m_state.account.logged_in) m_login.phase = "logged_in";
            }
        }

        // 3. Call /x/player/wbi/playurl (or fallback /x/player/playurl)
        std::string purl = !wbi_key.empty()
                               ? bili::playurl_wbi(track.bvid, track.cid, wbi_key, now, api_base)
                               : bili::playurl_fallback(track.bvid, track.cid, api_base);
        HttpResponse pr = m_http.get(purl, &cancel);
        if (!pr.ok) {
            res.error = pr.error_code.empty() ? "network_error" : pr.error_code;
            finish();
            return res;
        }
        Json pj;
        if (!Json::parse(pr.body, pj)) {
            res.error = "播放地址响应无效";
            finish();
            return res;
        }
        std::string perr;
        if (!bili::select_audio_candidates(pj, quality, res.candidates, res.duration_ms, perr)) {
            res.error = perr;
            finish();
            return res;
        }
        res.ok = true;
        if (res.duration_ms > 0) {
            std::lock_guard<std::mutex> lock(m_mu);
            if (Track* t = find_track_locked(track.id)) {
                if (t->duration_ms == 0) t->duration_ms = res.duration_ms;
            }
        }
        finish();
        return res;
    }

    // Stream candidates sequentially into a shared MediaTransfer.
    // Returns true if the transfer succeeded.
    bool download_candidates_into_transfer(const std::shared_ptr<MediaTransfer>& transfer,
                                           const std::vector<bili::AudioCandidate>& candidates,
                                           bool save_to_cache,
                                           const std::function<void(int)>& on_progress,
                                           std::string& out_err) {
        for (size_t idx = 0; idx < candidates.size(); ++idx) {
            if (transfer->cancel.load() || m_shutting_down.load()) {
                out_err = "canceled";
                break;
            }
            const auto& cand = candidates[idx];
            {
                std::lock_guard<std::mutex> tl(transfer->mu);
                transfer->label = cand.label;
                transfer->bytes_written = 0;
                transfer->total_bytes = -1;
                transfer->header_prefix.clear();
                transfer->percent = 0;
            }
            HANDLE f = CreateFileW(transfer->part_path.c_str(), GENERIC_WRITE,
                                   FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                                   nullptr, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
            if (f == INVALID_HANDLE_VALUE) {
                out_err = (GetLastError() == ERROR_DISK_FULL) ? "disk_full" : "storage_unavailable";
                break;
            }

            std::string stream_err;
            int last_pct = -1;
            bool ok = m_http.stream_get(
                cand.url, transfer->cancel,
                [&](const uint8_t* chunk, size_t n, int64_t total_len) -> bool {
                    DWORD written = 0;
                    if (!WriteFile(f, chunk, static_cast<DWORD>(n), &written, nullptr) || written != n) {
                        stream_err = (GetLastError() == ERROR_DISK_FULL) ? "disk_full" : "storage_unavailable";
                        return false;
                    }
                    int pct = 0;
                    {
                        std::lock_guard<std::mutex> tl(transfer->mu);
                        if (transfer->header_prefix.size() < 64) {
                            size_t take = std::min<size_t>(64 - transfer->header_prefix.size(), n);
                            transfer->header_prefix.insert(transfer->header_prefix.end(), chunk, chunk + take);
                        }
                        transfer->bytes_written += static_cast<int64_t>(n);
                        if (total_len > 0) {
                            transfer->total_bytes = total_len;
                            pct = static_cast<int>(std::min<int64_t>(99, transfer->bytes_written * 100 / total_len));
                            transfer->percent = pct;
                        }
                    }
                    transfer->cv.notify_all();
                    if (on_progress && total_len > 0 && pct != last_pct && (pct - last_pct >= 5 || last_pct < 0)) {
                        last_pct = pct;
                        on_progress(pct);
                    }
                    return true;
                },
                stream_err);
            CloseHandle(f);

            if (ok) {
                std::string commit_err;
                bool want_dl = transfer->want_download.load();
                std::wstring final_p = m_store.commit_transfer(transfer, save_to_cache, want_dl, commit_err);
                if (!final_p.empty() || commit_err.empty()) {
                    {
                        std::lock_guard<std::mutex> tl(transfer->mu);
                        transfer->final_path = final_p;
                        transfer->percent = 100;
                        transfer->succeeded = true;
                        transfer->done = true;
                    }
                    transfer->cv.notify_all();
                    return true;
                }
                out_err = commit_err;
                break;
            }

            DeleteFileW(transfer->part_path.c_str());
            out_err = stream_err;
            if (!is_retryable_error(stream_err)) break;
        }

        {
            std::lock_guard<std::mutex> tl(transfer->mu);
            transfer->error_code = out_err;
            transfer->succeeded = false;
            transfer->done = true;
        }
        transfer->cv.notify_all();
        return false;
    }

    // --- Playback Orchestration ---

    void cancel_prefetch_locked() {
        if (m_prefetch_cancel) {
            m_prefetch_cancel->store(true);
            m_prefetch_cancel.reset();
        }
        m_prefetch_track_id.clear();
        m_prefetch_quality.clear();
    }

    void update_prefetch_locked(int64_t unix_time) {
        if (m_play.status == "loading" && !m_prefetch_track_id.empty() && m_play.track_id == m_prefetch_track_id) {
            return; // Let foreground playback take over the active prefetch transfer!
        }
        std::string next_id;
        if (m_play.status == "playing" && m_state.queue.size() > 1) {
            next_id = pick_next_track_locked(1, unix_time);
            if (next_id == m_play.track_id) next_id.clear();
        } else if (m_play.status == "paused" && !m_prefetch_track_id.empty()) {
            next_id = m_state.queue.size() > 1 ? pick_next_track_locked(1, unix_time) : "";
        }
        if (!m_prefetch_track_id.empty() && m_prefetch_track_id == next_id && m_prefetch_quality == m_state.preferred_quality) {
            return;
        }
        cancel_prefetch_locked();
        if (next_id.empty() || m_play.status != "playing") return;

        const Track* next_track = find_track_const_locked(next_id);
        if (!next_track) return;
        std::string quality = m_state.preferred_quality;
        if (m_store.lookup_local(next_id, quality).found) return;
        if (m_store.find_active_transfer(next_id, quality)) return;

        Track copy = *next_track;
        std::string api_base = m_state.api_base;
        auto cancel = std::make_shared<std::atomic<bool>>(false);
        m_prefetch_track_id = next_id;
        m_prefetch_quality = quality;
        m_prefetch_cancel = cancel;
        uint64_t seq = ++m_action_seq;

        spawn_worker([this, copy, quality, api_base, cancel, seq]() {
            ResolvedMedia resolved = resolve_media_blocking(copy, quality, api_base, *cancel, false);
            if (!resolved.ok || cancel->load() || m_shutting_down.load()) return;

            bool created = false;
            auto transfer = m_store.find_or_create_transfer(copy.id, quality, seq, created);
            if (!created) return;

            // Only cancel the underlying transfer if prefetch was canceled AND nobody took it over!
            std::string err;
            download_candidates_into_transfer(transfer, resolved.candidates, true, nullptr, err);
            m_store.unregister_transfer(transfer);
        });
    }

    void attach_audio_session(uint64_t sid, const std::string& label) {
        std::lock_guard<std::mutex> lock(m_mu);
        if (sid == 0 || sid != m_play.play_seq) return;
        // Allow seeks while the decoder is loading. An early error or end report
        // may already have closed this session by the time play_file returns.
        if (m_play.status == "loading") m_play.audio_session_id = sid;
        if (!label.empty()) m_play.quality_label = label;
    }

    void begin_play_track_locked(const Track& track, int64_t resume_pos_ms) {
        if (m_play.cancel_flag) m_play.cancel_flag->store(true);
        ++m_play.play_seq;
        uint64_t seq = m_play.play_seq;

        auto cancel = std::make_shared<std::atomic<bool>>(false);
        m_play.cancel_flag = cancel;
        m_play.has_shuffle_seed = false;
        m_play.track_id = track.id;
        m_play.audio_session_id = 0;
        m_play.status = "loading";
        m_play.position_ms = resume_pos_ms;
        m_play.duration_ms = track.duration_ms;
        m_play.seekable = false;
        m_play.error.clear();
        m_play.quality_label.clear();
        m_play.resume_position_ms = resume_pos_ms;

        m_state.last_track_id = track.id;
        m_state.last_position_ms = resume_pos_ms;
        m_state.last_duration_ms = track.duration_ms;
        push_recent_locked(track.id);
        if (index_of_str(m_state.queue, track.id) < 0) {
            m_state.queue.push_back(track.id);
        }

        Track copy = track;
        std::string quality = m_state.preferred_quality;
        std::string api_base = m_state.api_base;
        std::string locale = m_locale;

        spawn_worker([this, seq, copy, quality, api_base, resume_pos_ms, locale, cancel]() {
            m_player.stop_through(seq);
            if (cancel->load() || m_shutting_down.load()) return;

            auto play_transfer = [&](const std::shared_ptr<MediaTransfer>& transfer, int64_t duration_ms) {
                uint64_t sid = m_player.play_transfer(transfer, duration_ms, resume_pos_ms, seq);
                std::string label;
                {
                    std::lock_guard<std::mutex> lock(transfer->mu);
                    label = transfer->label;
                }
                attach_audio_session(sid, label);
            };

            // 1. Check local downloaded/cached audio first (works completely offline!)
            LocalMediaHit local = m_store.lookup_local(copy.id, quality);
            if (local.found) {
                uint64_t sid = m_player.play_file(local.path, copy.duration_ms, resume_pos_ms, seq);
                attach_audio_session(sid, local.label);
                return;
            }

            // 2. Check if prefetch or download is already streaming this track!
            auto active = m_store.find_active_transfer(copy.id, quality);
            if (active) {
                play_transfer(active, copy.duration_ms);
                return;
            }

            // 3. Resolve & stream with 1 automatic re-resolve retry on expired CDN URL!
            for (int attempt = 0; attempt < 2; ++attempt) {
                if (cancel->load() || m_shutting_down.load()) return;
                ResolvedMedia resolved = resolve_media_blocking(copy, quality, api_base, *cancel, attempt > 0);
                if (cancel->load() || m_shutting_down.load()) return;
                if (!resolved.ok) {
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        if (m_play.play_seq != seq) return;
                        m_play.status = "error";
                        m_play.error = resolved.error;
                    }
                    push_widget_and_page(false);
                    return;
                }
                int64_t dur = resolved.duration_ms > 0 ? resolved.duration_ms : copy.duration_ms;
                {
                    std::lock_guard<std::mutex> lock(m_mu);
                    if (m_play.play_seq != seq) return;
                    if (dur > 0) {
                        m_play.duration_ms = dur;
                        m_state.last_duration_ms = dur;
                    }
                }

                // Re-check local cache in case a concurrent download already finished while resolving.
                LocalMediaHit hit_after = m_store.lookup_local(copy.id, quality);
                if (hit_after.found) {
                    uint64_t sid = m_player.play_file(hit_after.path, dur, resume_pos_ms, seq);
                    attach_audio_session(sid, hit_after.label);
                    return;
                }

                // Atomically reuse an existing in-flight transfer (e.g. from download.start or prefetch) or create a new one!
                uint64_t part_seq = 0;
                {
                    std::lock_guard<std::mutex> lock(m_mu);
                    part_seq = ++m_action_seq;
                }
                bool created = false;
                auto transfer = m_store.find_or_create_transfer(copy.id, quality, part_seq, created);
                if (!created) {
                    play_transfer(transfer, dur);
                    return;
                }

                // Start downloading in a dedicated thread so progressive WAV can play immediately!
                std::shared_ptr<MediaTransfer> dl_t = transfer;
                std::vector<bili::AudioCandidate> cands = resolved.candidates;
                std::string dl_err;
                std::atomic<bool> dl_ok{ false };
                std::thread dl_thread([this, dl_t, cands, &dl_err, &dl_ok]() {
                    dl_ok.store(download_candidates_into_transfer(dl_t, cands, true, nullptr, dl_err));
                    m_store.unregister_transfer(dl_t);
                });

                // Wait for either enough bytes (44 bytes for streaming WAV) or full download failure.
                int64_t avail = 0;
                bool is_done = false, ok = false;
                transfer->wait_for_bytes(44, *cancel, avail, is_done, ok);
                if (cancel->load() || m_shutting_down.load()) {
                    if (!transfer->want_download.load()) transfer->cancel.store(true);
                    if (dl_thread.joinable()) dl_thread.join();
                    return;
                }
                if (is_done && !ok) {
                    if (dl_thread.joinable()) dl_thread.join();
                    if (attempt == 0 && is_retryable_error(dl_err)) {
                        continue; // Expired CDN link -> re-resolve once!
                    }
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        if (m_play.play_seq != seq) return;
                        m_play.status = "error";
                        m_play.error = tr_loc(locale, "音频播放失败，请重试或切换其他音质。", "Audio playback failed. Retry or switch quality.");
                    }
                    push_widget_and_page(false);
                    return;
                }

                // Hand off live transfer to audio player and let download thread finish in background!
                play_transfer(transfer, dur);
                if (dl_thread.joinable()) {
                    dl_thread.join();
                }
                return;
            }
        });
    }

    void on_audio_progress(const AudioProgress& prog) {
        if (m_shutting_down.load()) return;
        bool need_persist = false;
        bool should_notify = false;
        {
            std::lock_guard<std::mutex> lock(m_mu);
            // The first device report can arrive before play_file/play_transfer returns.
            if (prog.session_id == 0 || prog.session_id != m_play.play_seq) return;

            if (prog.status == "playing" || prog.status == "paused") {
                m_play.audio_session_id = prog.session_id;
                bool entered_playing = (prog.status == "playing" && m_play.status != "playing");
                m_play.status = prog.status;
                m_play.position_ms = prog.position_ms;
                m_state.last_position_ms = prog.position_ms;
                if (prog.duration_ms > 0) {
                    m_play.duration_ms = prog.duration_ms;
                    m_state.last_duration_ms = prog.duration_ms;
                }
                m_play.seekable = prog.seekable;
                if (prog.status == "playing") m_play.error.clear();
                if (entered_playing) {
                    int64_t now = std::chrono::duration_cast<std::chrono::seconds>(
                                      std::chrono::system_clock::now().time_since_epoch())
                                      .count();
                    update_prefetch_locked(now);
                    need_persist = true;
                }
                should_notify = true;
            } else if (prog.status == "ended") {
                m_play.audio_session_id = 0;
                m_play.status = "ended";
                m_play.position_ms = prog.duration_ms;
                m_state.last_position_ms = 0;
                need_persist = true;
                should_notify = true;

                int64_t now = std::chrono::duration_cast<std::chrono::seconds>(
                                  std::chrono::system_clock::now().time_since_epoch())
                                  .count();
                if (m_state.play_mode == "repeat_one") {
                    if (Track* cur = find_track_locked(m_play.track_id)) {
                        begin_play_track_locked(*cur, 0);
                    }
                } else {
                    std::string next_id = pick_next_track_locked(1, now);
                    if (!next_id.empty() && (m_state.queue.size() > 1 || m_state.play_mode == "sequence")) {
                        if (Track* nxt = find_track_locked(next_id)) {
                            begin_play_track_locked(*nxt, 0);
                        }
                    }
                }
            } else if (prog.status == "error") {
                m_play.audio_session_id = 0;
                m_play.status = "error";
                m_play.error = tr_loc(m_locale, "音频播放失败，请重试或切换其他音质。", "Audio playback failed. Retry or switch quality.");
                should_notify = true;
            }
        }
        if (should_notify) {
            push_widget_and_page(need_persist);
        }
    }

    Json exec_playback_op_locked(const std::string& op_name, const Json& args, int64_t unix_time, const Json& payload) {
        std::string loc = payload.str("locale", m_locale);
        if (op_name == "play_pause" || op_name == "playback.toggle") {
            if (m_play.status == "loading") {
                Json r = Json::object();
                r["pending"] = true;
                r["trackId"] = m_play.track_id;
                return r;
            }
            if (m_play.status == "playing" && m_play.audio_session_id != 0) {
                m_play.status = "paused";
                m_player.pause();
                Json r = Json::object();
                r["ok"] = true;
                r["status"] = "paused";
                return r;
            }
            if (m_play.status == "paused" && m_play.audio_session_id != 0) {
                m_play.status = "playing";
                m_player.resume();
                update_prefetch_locked(unix_time);
                Json r = Json::object();
                r["ok"] = true;
                r["status"] = "playing";
                return r;
            }
            std::string tid = !m_play.track_id.empty() ? m_play.track_id : m_state.last_track_id;
            Track* track = !tid.empty() ? find_track_locked(tid) : nullptr;
            if (!track && !m_state.queue.empty()) track = find_track_locked(m_state.queue.front());
            if (!track && !m_state.tracks.empty()) track = &m_state.tracks.front();
            if (!track) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_library";
                r["message"] = tr_loc(loc, "曲库为空，请先导入曲目。", "Library is empty.");
                return r;
            }
            int64_t resume = (m_play.status == "paused" && m_play.track_id == track->id) ? m_play.position_ms : 0;
            begin_play_track_locked(*track, resume);
            Json r = Json::object();
            r["pending"] = true;
            r["trackId"] = track->id;
            return r;
        }
        if (op_name == "next" || op_name == "playback.next") {
            std::string tid = pick_next_track_locked(1, unix_time);
            Track* track = !tid.empty() ? find_track_locked(tid) : nullptr;
            if (!track) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_queue";
                return r;
            }
            begin_play_track_locked(*track, 0);
            Json r = Json::object();
            r["pending"] = true;
            r["trackId"] = track->id;
            return r;
        }
        if (op_name == "prev" || op_name == "playback.prev") {
            std::string tid = pick_next_track_locked(-1, unix_time);
            Track* track = !tid.empty() ? find_track_locked(tid) : nullptr;
            if (!track) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_queue";
                return r;
            }
            begin_play_track_locked(*track, 0);
            Json r = Json::object();
            r["pending"] = true;
            r["trackId"] = track->id;
            return r;
        }
        if (op_name == "stop" || op_name == "playback.stop") {
            if (m_play.cancel_flag) m_play.cancel_flag->store(true);
            uint64_t stop_seq = ++m_play.play_seq;
            m_play.audio_session_id = 0;
            m_play.status = "stopped";
            m_play.position_ms = 0;
            m_state.last_position_ms = 0;
            cancel_prefetch_locked();
            spawn_worker([this, stop_seq]() { m_player.stop_through(stop_seq); });
            Json r = Json::object();
            r["ok"] = true;
            r["status"] = "stopped";
            return r;
        }
        if (op_name == "cycle_mode" || op_name == "playback.cycleMode") {
            if (m_state.play_mode == "sequence") m_state.play_mode = "shuffle";
            else if (m_state.play_mode == "shuffle") m_state.play_mode = "repeat_one";
            else m_state.play_mode = "sequence";
            Json r = Json::object();
            r["ok"] = true;
            r["mode"] = m_state.play_mode;
            return r;
        }
        if (op_name == "seek" || op_name == "playback.seek") {
            const Track* track = !m_play.track_id.empty() ? find_track_const_locked(m_play.track_id) : nullptr;
            int64_t duration = m_play.duration_ms > 0 ? m_play.duration_ms : (track ? track->duration_ms : 0);
            int64_t target_ms = -1;
            if (args.is_object() && !args.get("positionMs").is_null()) {
                target_ms = static_cast<int64_t>(clamp_d(args.num("positionMs", 0.0), 0.0, static_cast<double>(std::max<int64_t>(0, duration))));
            } else if (args.is_object() && !args.get("ratio").is_null() && duration > 0) {
                target_ms = static_cast<int64_t>(clamp_d(args.num("ratio", 0.0), 0.0, 1.0) * static_cast<double>(duration));
            }
            if (target_ms < 0) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "invalid_seek";
                return r;
            }
            m_play.position_ms = target_ms;
            m_state.last_position_ms = target_ms;
            if (m_play.audio_session_id != 0) {
                m_player.seek(target_ms);
            }
            Json r = Json::object();
            r["ok"] = true;
            r["positionMs"] = target_ms;
            return r;
        }
        Json r = Json::object();
        r["ok"] = false;
        r["code"] = "unknown_operation";
        return r;
    }

    // --- Background Login & Import Flows ---

    void start_login_flow_locked(const std::string& locale) {
        uint64_t fid = ++m_login.flow_id;
        m_login.phase = "generating";
        m_login.qr_key.clear();
        m_login.qr_url.clear();
        m_login.message = tr_loc(locale, "正在获取 Bilibili 登录二维码…", "Generating Bilibili QR code…");
        m_login_cv.notify_all();

        std::string api_base = m_state.api_base;
        int64_t custom_delay = m_state.poll_delay_ms;

        spawn_worker([this, fid, api_base, custom_delay, locale]() {
            auto still_active = [&]() -> bool {
                std::lock_guard<std::mutex> lock(m_mu);
                return !m_shutting_down.load() && m_login.flow_id == fid;
            };
            std::atomic<bool> cancel{ false };
            HttpResponse gen = m_http.get(bili::qr_generate_url(api_base), &cancel);
            Json gj;
            if (!still_active()) return;
            if (!gen.ok || !Json::parse(gen.body, gj) || gj.i64("code", -1) != 0 || !gj.get("data").is_object()) {
                {
                    std::lock_guard<std::mutex> lock(m_mu);
                    if (m_login.flow_id != fid) return;
                    m_login.phase = "error";
                    m_login.message = gen.ok ? tr_loc(locale, "Bilibili 登录二维码接口返回异常", "Failed to generate QR code")
                                             : friendly_error(gen.error_code, locale);
                }
                push_page_notify();
                return;
            }
            std::string qr_key = gj.get("data").str("qrcode_key");
            std::string qr_url = gj.get("data").str("url");
            {
                std::lock_guard<std::mutex> lock(m_mu);
                if (m_login.flow_id != fid) return;
                m_login.phase = "waiting_scan";
                m_login.qr_key = qr_key;
                m_login.qr_url = qr_url;
                m_login.message = tr_loc(locale, "请使用哔哩哔哩 App 扫描二维码", "Scan with Bilibili mobile app");
            }
            push_page_notify();

            int64_t delay_ms = custom_delay > 0 ? std::max<int64_t>(10, custom_delay) : 2500;
            for (;;) {
                {
                    std::unique_lock<std::mutex> lock(m_mu);
                    m_login_cv.wait_for(lock, std::chrono::milliseconds(delay_ms), [&]() {
                        return m_shutting_down.load() || m_login.flow_id != fid;
                    });
                    if (m_shutting_down.load() || m_login.flow_id != fid) return;
                }

                HttpResponse pr = m_http.get(bili::qr_poll_url(qr_key, api_base), &cancel);
                Json pj;
                if (!still_active()) return;
                if (!pr.ok || !Json::parse(pr.body, pj) || !pj.get("data").is_object()) {
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        if (m_login.flow_id != fid) return;
                        m_login.phase = "error";
                        m_login.message = pr.ok ? tr_loc(locale, "轮询登录状态失败", "Failed to poll QR status")
                                                : friendly_error(pr.error_code, locale);
                    }
                    push_page_notify();
                    return;
                }
                int64_t code = pj.get("data").i64("code", -1);
                if (code == 0) {
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        if (m_login.flow_id != fid) return;
                        m_login.phase = "confirming";
                        m_login.qr_url.clear();
                        m_login.qr_key.clear();
                        m_login.message = tr_loc(locale, "登录成功，正在同步账号信息…", "Signed in, syncing profile…");
                    }
                    push_page_notify();
                    sync_nav_blocking(api_base, locale, true);
                    return;
                }
                if (code == 86090) {
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        if (m_login.flow_id != fid) return;
                        m_login.phase = "scanned_confirm";
                        m_login.message = tr_loc(locale, "已扫码，请在手机端点击确认登录", "Scanned! Confirm sign-in on your phone");
                    }
                    delay_ms = custom_delay > 0 ? std::max<int64_t>(10, custom_delay) : 2000;
                    push_page_notify();
                } else if (code == 86038) {
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        if (m_login.flow_id != fid) return;
                        m_login.phase = "expired";
                        m_login.qr_url.clear();
                        m_login.qr_key.clear();
                        m_login.message = tr_loc(locale, "二维码已过期，请点击重新获取", "QR code expired. Click refresh to retry.");
                    }
                    push_page_notify();
                    return;
                } else {
                    delay_ms = custom_delay > 0 ? std::max<int64_t>(10, custom_delay) : 2500;
                }
            }
        });
    }

    void sync_nav_blocking(const std::string& api_base, const std::string& locale, bool from_confirm) {
        std::atomic<bool> cancel{ false };
        HttpResponse nr = m_http.get(bili::nav_url(api_base), &cancel);
        Json nj;
        bili::NavResult parsed;
        if (!nr.ok || !Json::parse(nr.body, nj) || !bili::parse_nav(nj, parsed)) {
            if (from_confirm) {
                {
                    std::lock_guard<std::mutex> lock(m_mu);
                    if (m_login.phase == "confirming") {
                        m_login.phase = "error";
                        m_login.message = friendly_error(nr.error_code, locale);
                    }
                }
                push_page_notify();
            }
            return;
        }
        int64_t now = std::chrono::duration_cast<std::chrono::seconds>(
                          std::chrono::system_clock::now().time_since_epoch())
                          .count();
        {
            std::lock_guard<std::mutex> lock(m_mu);
            if (!parsed.wbi_key_str.empty()) {
                m_wbi_key = parsed.wbi_key_str;
                m_wbi_fetched_at = now;
            }
            bool was_logged = m_state.account.logged_in;
            m_state.account = parsed.account;
            if (m_state.account.logged_in) {
                m_login.phase = "logged_in";
                m_login.qr_url.clear();
                m_login.message = tr_loc(locale, "已登录：" + m_state.account.nickname, "Signed in as " + m_state.account.nickname);
            } else if (was_logged) {
                m_login.phase = "expired";
                m_login.message = tr_loc(locale, "Bilibili 登录态已过期，请重新扫码登录。", "Bilibili session expired. Please sign in again.");
            } else if (m_login.phase == "confirming") {
                m_login.phase = "idle";
            }
        }
        push_widget_and_page(true);
    }

    void start_import_preview_locked(const std::string& input, const std::vector<bili::ParsedTarget>& targets, const std::string& locale) {
        uint64_t job_id = ++m_import_job_id;
        m_preview = ImportPreviewState{};
        m_preview.present = true;
        m_preview.status = "loading";
        m_preview.input = input;

        std::string api_base = m_state.api_base;
        spawn_worker([this, job_id, input, targets, api_base, locale]() {
            ImportPreviewState result;
            result.present = true;
            result.status = "ready";
            result.input = input;
            std::set<std::string> seen;
            std::atomic<bool> cancel{ false };

            auto is_current = [&]() -> bool {
                std::lock_guard<std::mutex> lock(m_mu);
                return !m_shutting_down.load() && m_import_job_id == job_id;
            };

            auto fetch_video = [&](const std::string& bvid_str, int64_t aid, int req_page, const std::string& raw) {
                HttpResponse r = m_http.get(bili::view_url(bvid_str, aid, api_base), &cancel);
                if (!r.ok) {
                    result.errors.push_back(raw + "：" + friendly_error(r.error_code, locale));
                    return;
                }
                Json j;
                std::vector<bili::PreviewTrack> items;
                std::string err;
                if (!Json::parse(r.body, j) || !bili::parse_view(j, req_page, items, err)) {
                    result.errors.push_back(raw + "：" + (err.empty() ? "视频信息响应无效" : err));
                    return;
                }
                for (auto& it : items) {
                    if (seen.insert(it.dedupe_key).second) {
                        result.items.push_back(std::move(it));
                    }
                }
            };

            auto fetch_fav = [&](const std::string& media_id, const std::string& raw) {
                for (int pn = 1; pn <= 3; ++pn) {
                    if (!is_current()) return;
                    HttpResponse r = m_http.get(bili::favlist_url(media_id, pn, 20, api_base), &cancel);
                    if (!r.ok) {
                        result.errors.push_back(raw + "：" + friendly_error(r.error_code, locale));
                        return;
                    }
                    Json j;
                    bili::FavlistPage fav;
                    std::string err;
                    if (!Json::parse(r.body, j) || !bili::parse_favlist(j, fav, err)) {
                        result.errors.push_back(raw + "：" + (err.empty() ? "收藏夹响应无效" : err));
                        return;
                    }
                    if (result.source_title.empty() && !fav.title.empty()) result.source_title = fav.title;
                    for (auto& it : fav.items) {
                        if (seen.insert(it.dedupe_key).second) {
                            result.items.push_back(std::move(it));
                        }
                    }
                    for (auto& sk : fav.skipped) result.errors.push_back(std::move(sk));
                    if (!fav.has_more) break;
                }
            };

            for (const auto& tgt : targets) {
                if (!is_current()) return;
                if (tgt.kind == "invalid") {
                    result.errors.push_back(tgt.raw + "：" + (tgt.reason.empty() ? "格式无效" : tgt.reason));
                } else if (tgt.kind == "video") {
                    fetch_video(tgt.bvid, tgt.aid, tgt.page, tgt.raw);
                } else if (tgt.kind == "favlist") {
                    fetch_fav(tgt.media_id, tgt.raw);
                } else if (tgt.kind == "shorturl") {
                    std::string short_url = tgt.url;
                    if (!api_base.empty()) {
                        size_t b23 = tgt.url.find("b23.tv/");
                        if (b23 != std::string::npos) {
                            std::string code = tgt.url.substr(b23 + 7);
                            short_url = api_base + "/b23/" + code;
                        }
                    }
                    HttpResponse r = m_http.get(short_url, &cancel);
                    if (!r.ok) {
                        result.errors.push_back(tgt.raw + "：" + friendly_error(r.error_code, locale));
                        continue;
                    }
                    // Try final redirected URL first, then body text for BV/fid
                    bili::ParsedTarget resolved = bili::parse_token(r.final_url);
                    if (resolved.kind != "video" && resolved.kind != "favlist") {
                        // Search body for bilibili video URL, BV ID, or fid=
                        std::string candidate;
                        size_t bv_pos = std::string::npos;
                        for (size_t i = 0; i + 12 <= r.body.size(); ++i) {
                            if ((r.body[i] == 'B' || r.body[i] == 'b') && (r.body[i + 1] == 'V' || r.body[i + 1] == 'v') && r.body[i + 2] == '1') {
                                bool ok = true;
                                for (size_t j = 0; j < 9; ++j) {
                                    if (!bili::is_alnum_ascii(r.body[i + 3 + j])) { ok = false; break; }
                                }
                                if (ok) { bv_pos = i; break; }
                            }
                        }
                        if (bv_pos != std::string::npos) {
                            size_t end = bv_pos + 12;
                            while (end < r.body.size() && r.body[end] != '"' && r.body[end] != '\'' &&
                                   r.body[end] != '<' && r.body[end] != '>' && !std::isspace(static_cast<unsigned char>(r.body[end]))) {
                                ++end;
                            }
                            candidate = r.body.substr(bv_pos, end - bv_pos);
                        } else {
                            std::string fid = bili::match_digits_after(r.body, "fid=");
                            if (!fid.empty()) candidate = "?fid=" + fid;
                        }
                        if (!candidate.empty()) resolved = bili::parse_token(candidate);
                    }
                    if (resolved.kind == "video") {
                        fetch_video(resolved.bvid, resolved.aid, resolved.page, tgt.raw);
                    } else if (resolved.kind == "favlist") {
                        fetch_fav(resolved.media_id, tgt.raw);
                    } else {
                        result.errors.push_back(tgt.raw + "：未能从短链中解析出视频或收藏夹");
                    }
                }
            }

            {
                std::lock_guard<std::mutex> lock(m_mu);
                if (m_import_job_id != job_id) return;
                m_preview = std::move(result);
            }
            push_page_notify();
        });
    }

    void start_download_locked(const Track& track, const std::string& locale) {
        std::string tid = track.id;
        auto it = m_downloads.find(tid);
        if (it != m_downloads.end() && it->second.status != "error") {
            return;
        }
        std::string quality = m_state.preferred_quality;
        std::string err;
        // Fast path: promote already-cached file offline!
        if (m_store.promote_cached_to_download(tid, quality, err)) {
            m_downloads[tid] = { "downloaded", 100, "", true };
            return;
        }
        if (!err.empty()) {
            m_downloads[tid] = { "error", 0, friendly_error(err, locale), false };
            return;
        }

        auto cancel = std::make_shared<std::atomic<bool>>(false);
        m_download_cancels[tid] = cancel;
        m_downloads[tid] = { "downloading", 0, "", true };

        Track copy = track;
        std::string api_base = m_state.api_base;
        spawn_worker([this, copy, quality, api_base, locale, cancel]() {
            auto is_active = [&]() -> bool {
                std::lock_guard<std::mutex> lock(m_mu);
                auto it = m_download_cancels.find(copy.id);
                return !m_shutting_down.load() && !cancel->load() && it != m_download_cancels.end() && it->second == cancel;
            };
            auto finish_err = [&](const std::string& code_or_msg) {
                {
                    std::lock_guard<std::mutex> lock(m_mu);
                    auto it = m_download_cancels.find(copy.id);
                    if (it != m_download_cancels.end() && it->second == cancel) {
                        m_download_cancels.erase(it);
                        m_downloads[copy.id] = { "error", 0, friendly_error(code_or_msg, locale), false };
                    }
                }
                push_page_notify();
            };

            // Attach to an in-flight transfer if already running for this track!
            auto active = m_store.find_active_transfer(copy.id, quality);
            if (active) {
                active->want_download.store(true);
                std::wstring path;
                std::string wait_err;
                if (active->wait_complete(*cancel, path, wait_err)) {
                    if (!is_active()) return;
                    std::string prom_err;
                    m_store.promote_cached_to_download(copy.id, quality, prom_err);
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        auto it = m_download_cancels.find(copy.id);
                        if (it != m_download_cancels.end() && it->second == cancel) {
                            m_download_cancels.erase(it);
                            m_downloads[copy.id] = { "downloaded", 100, "", true };
                        }
                    }
                    push_page_notify();
                    return;
                }
                if (!is_active()) return;
                if (!is_retryable_error(wait_err)) {
                    finish_err(wait_err);
                    return;
                }
            }

            for (int attempt = 0; attempt < 2; ++attempt) {
                if (!is_active()) return;
                ResolvedMedia resolved = resolve_media_blocking(copy, quality, api_base, *cancel, attempt > 0);
                if (!is_active()) return;
                if (!resolved.ok) {
                    finish_err(resolved.error);
                    return;
                }
                uint64_t part_seq = 0;
                {
                    std::lock_guard<std::mutex> lock(m_mu);
                    part_seq = ++m_action_seq;
                }
                bool created = false;
                auto transfer = m_store.find_or_create_transfer(copy.id, quality, part_seq, created);
                transfer->want_download.store(true);

                std::string dl_err;
                bool ok = false;
                if (!created) {
                    std::wstring done_path;
                    ok = transfer->wait_complete(*cancel, done_path, dl_err);
                    if (ok && is_active()) {
                        std::string prom_err;
                        m_store.promote_cached_to_download(copy.id, quality, prom_err);
                    }
                } else {
                    ok = download_candidates_into_transfer(
                        transfer, resolved.candidates, true,
                        [this, tid = copy.id, cancel](int pct) {
                            bool updated = false;
                            {
                                std::lock_guard<std::mutex> lock(m_mu);
                                auto it = m_download_cancels.find(tid);
                                if (it != m_download_cancels.end() && it->second == cancel) {
                                    m_downloads[tid] = { "downloading", pct, "", true };
                                    updated = true;
                                }
                            }
                            if (updated) push_page_notify();
                        },
                        dl_err);
                    m_store.unregister_transfer(transfer);
                }

                if (!is_active()) return;
                if (ok) {
                    {
                        std::lock_guard<std::mutex> lock(m_mu);
                        auto it = m_download_cancels.find(copy.id);
                        if (it != m_download_cancels.end() && it->second == cancel) {
                            m_download_cancels.erase(it);
                            m_downloads[copy.id] = { "downloaded", 100, "", true };
                        }
                    }
                    push_page_notify();
                    return;
                }
                if (!is_retryable_error(dl_err) || attempt == 1) {
                    finish_err(dl_err);
                    return;
                }
            }
        });
    }

    // --- Page Message Dispatcher ---

    Json handle_page_message_locked(const std::string& op, const Json& args, int64_t unix_time,
                                    const Json& payload, bool& dirty) {
        std::string loc = payload.str("locale", m_locale);
        m_notice = NoticeState{};

        if (op == "library.refresh") {
            refresh_downloads_from_store_locked();
            if (!m_checked_nav) {
                m_checked_nav = true;
                std::string api_base = m_state.api_base;
                spawn_worker([this, api_base, loc]() { sync_nav_blocking(api_base, loc, false); });
            }
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "view.select") {
            std::string vid = args.str("viewId");
            if (!vid.empty()) m_state.active_view = vid;
            std::string sb = args.str("sortBy");
            if (!sb.empty()) m_state.sort_by = sb;
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "playlist.create") {
            std::string name = trim_str(args.str("name"));
            if (name.empty()) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "invalid_name";
                r["message"] = tr_loc(loc, "歌单名称不能为空", "Playlist name is required");
                return r;
            }
            std::string id = "pl_" + std::to_string(m_state.next_playlist_id++);
            Playlist pl;
            pl.id = id;
            pl.name = utf8_prefix_bytes(name, 80);
            pl.desc = utf8_prefix_bytes(trim_str(args.str("desc")), 200);
            std::string color = args.str("color");
            pl.color = color.empty() ? "accent" : color;
            m_state.playlists.push_back(std::move(pl));
            m_state.active_view = id;
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            r["playlistId"] = id;
            return r;
        }

        if (op == "playlist.update") {
            Playlist* pl = find_playlist_locked(args.str("playlistId"));
            if (!pl) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            std::string name = trim_str(args.str("name"));
            if (name.empty()) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "invalid_name";
                return r;
            }
            pl->name = utf8_prefix_bytes(name, 80);
            pl->desc = utf8_prefix_bytes(trim_str(args.str("desc")), 200);
            std::string color = args.str("color");
            if (!color.empty()) pl->color = color;
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            r["playlistId"] = pl->id;
            return r;
        }

        if (op == "playlist.delete") {
            std::string pid = args.str("playlistId");
            auto it = std::find_if(m_state.playlists.begin(), m_state.playlists.end(),
                                   [&](const Playlist& p) { return p.id == pid; });
            if (it == m_state.playlists.end()) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            m_state.playlists.erase(it);
            if (m_state.active_view == pid) m_state.active_view = "smart_all";
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "playlist.addTracks") {
            Playlist* pl = find_playlist_locked(args.str("playlistId"));
            if (!pl) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            int added = 0;
            if (args.get("trackIds").is_array()) {
                for (const Json& tid : args.get("trackIds").as_array()) {
                    if (tid.is_string() && find_track_locked(tid.as_str()) && index_of_str(pl->track_ids, tid.as_str()) < 0) {
                        pl->track_ids.push_back(tid.as_str());
                        ++added;
                    }
                }
            }
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            r["added"] = added;
            return r;
        }

        if (op == "playlist.removeTrack") {
            Playlist* pl = find_playlist_locked(args.str("playlistId"));
            if (!pl) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            remove_str(pl->track_ids, args.str("trackId"));
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "track.toggleFavorite") {
            Track* tr = find_track_locked(args.str("trackId"));
            if (!tr) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            tr->favorite = !tr->favorite;
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            r["favorite"] = tr->favorite;
            return r;
        }

        if (op == "download.start" || op == "download.remove") {
            Track* tr = find_track_locked(args.str("trackId"));
            if (!tr) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            if (op == "download.remove") {
                auto cit = m_download_cancels.find(tr->id);
                if (cit != m_download_cancels.end()) {
                    if (cit->second) cit->second->store(true);
                    m_download_cancels.erase(cit);
                }
                m_downloads.erase(tr->id);
                m_store.remove_download(tr->id);
            } else {
                start_download_locked(*tr, loc);
            }
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "track.delete") {
            std::string tid = args.str("trackId");
            auto it = std::find_if(m_state.tracks.begin(), m_state.tracks.end(),
                                   [&](const Track& t) { return t.id == tid; });
            if (it == m_state.tracks.end()) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            m_state.tracks.erase(it);
            auto cit = m_download_cancels.find(tid);
            if (cit != m_download_cancels.end()) {
                if (cit->second) cit->second->store(true);
                m_download_cancels.erase(cit);
            }
            m_downloads.erase(tid);
            m_store.remove_download(tid);
            for (auto& pl : m_state.playlists) remove_str(pl.track_ids, tid);
            remove_str(m_state.recent_ids, tid);
            remove_str(m_state.queue, tid);
            if (m_play.track_id == tid) {
                exec_playback_op_locked("stop", Json::object(), unix_time, payload);
                m_play.track_id.clear();
                m_state.last_track_id.clear();
            }
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "bilibili.loginStart") {
            start_login_flow_locked(loc);
            Json r = Json::object();
            r["pending"] = true;
            return r;
        }

        if (op == "bilibili.loginCancel") {
            ++m_login.flow_id;
            m_login.qr_key.clear();
            m_login.qr_url.clear();
            m_login.phase = m_state.account.logged_in ? "logged_in" : "idle";
            m_login.message.clear();
            m_login_cv.notify_all();
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "bilibili.loginRefresh") {
            if (m_state.account.logged_in) {
                std::string api_base = m_state.api_base;
                spawn_worker([this, api_base, loc]() { sync_nav_blocking(api_base, loc, false); });
            } else {
                start_login_flow_locked(loc);
            }
            Json r = Json::object();
            r["pending"] = true;
            return r;
        }

        if (op == "bilibili.logout") {
            ++m_login.flow_id;
            m_login.qr_key.clear();
            m_login.qr_url.clear();
            m_login_cv.notify_all();
            m_wbi_key.clear();
            m_wbi_fetched_at = 0;
            m_http.cookies().clear();
            m_state.account = bili::AccountInfo{};
            m_login.phase = "idle";
            m_login.message = tr_loc(loc, "已退出 Bilibili 登录", "Signed out from Bilibili");
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "bilibili.setQuality") {
            std::string q = args.str("quality");
            if (q == "flac" || q == "192k" || q == "132k" || q == "64k") {
                m_state.preferred_quality = q;
                dirty = true;
                Json r = Json::object();
                r["ok"] = true;
                r["quality"] = q;
                return r;
            }
            Json r = Json::object();
            r["ok"] = false;
            r["code"] = "invalid_quality";
            return r;
        }

        if (op == "bilibili.clearPreview") {
            ++m_import_job_id;
            m_preview = ImportPreviewState{};
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "bilibili.preview") {
            std::string input = trim_str(args.str("input"));
            auto targets = bili::parse_input(input);
            if (targets.empty()) {
                ++m_import_job_id;
                m_preview = ImportPreviewState{};
                m_preview.present = true;
                m_preview.status = "ready";
                m_preview.input = input;
                m_preview.errors.push_back(tr_loc(loc,
                    "请输入至少一个 BV/AV 号、B站视频链接或收藏夹链接。",
                    "Enter at least one BV/AV ID, Bilibili video URL, or favorites URL."));
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_input";
                return r;
            }
            start_import_preview_locked(input, targets, loc);
            Json r = Json::object();
            r["pending"] = true;
            return r;
        }

        if (op == "bilibili.import") {
            const Json& items = args.get("items");
            if (!items.is_array() || items.as_array().empty()) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_selection";
                r["message"] = tr_loc(loc, "请至少勾选一首曲目", "Select at least one track");
                return r;
            }
            std::string pl_id = args.str("playlistId");
            Playlist* pl = pl_id.empty() ? nullptr : find_playlist_locked(pl_id);
            int added_new = 0;
            int added_to_pl = 0;
            Json imported_ids = Json::array();
            int imported_count = 0;

            for (const Json& it : items.as_array()) {
                if (!it.is_object()) continue;
                std::string bv = it.str("bvid");
                if (bv.empty()) continue;
                int pnum = static_cast<int>(it.i64("page", 1));
                std::string dedupe = it.str("dedupeKey");
                if (dedupe.empty()) dedupe = bv + ":p" + std::to_string(pnum);
                Track* track = find_track_by_dedupe_locked(dedupe);
                if (!track) {
                    Track tr;
                    tr.id = "tr_" + std::to_string(m_state.next_track_id++);
                    tr.dedupe_key = dedupe;
                    tr.bvid = bv;
                    int64_t aid = it.i64("aid", 0);
                    if (aid <= 0) bili::decode_bv(bv, aid);
                    tr.aid = aid;
                    tr.cid = it.i64("cid", 0);
                    tr.page = pnum;
                    tr.total_pages = static_cast<int>(it.i64("totalPages", 1));
                    std::string title = trim_str(it.str("title"));
                    tr.title = title.empty() ? bv : title;
                    std::string artist = trim_str(it.str("artist"));
                    tr.artist = artist.empty() ? "Bilibili" : artist;
                    tr.duration_ms = std::max<int64_t>(0, it.i64("durationMs", 0));
                    tr.favorite = false;
                    tr.added_at = unix_time;
                    m_state.tracks.push_back(std::move(tr));
                    track = &m_state.tracks.back();
                    ++added_new;
                }
                imported_ids.push_back(track->id);
                ++imported_count;
                if (pl && index_of_str(pl->track_ids, track->id) < 0) {
                    pl->track_ids.push_back(track->id);
                    ++added_to_pl;
                }
            }
            m_preview = ImportPreviewState{};
            ++m_import_job_id;
            if (pl) m_state.active_view = pl->id;
            m_notice.present = true;
            m_notice.kind = "info";
            if (loc == "zh-CN") {
                m_notice.message = "已导入 " + std::to_string(imported_count) + " 首（新增 " + std::to_string(added_new) + " 首" +
                                   (pl ? ("，加入歌单 " + std::to_string(added_to_pl) + " 首") : "") + "）";
            } else {
                m_notice.message = "Imported " + std::to_string(imported_count) + " track(s) (" + std::to_string(added_new) + " new)";
            }
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            r["imported"] = imported_count;
            r["addedNew"] = added_new;
            r["trackIds"] = std::move(imported_ids);
            return r;
        }

        if (op == "playback.playTrack") {
            Track* track = find_track_locked(args.str("trackId"));
            if (!track) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            if (args.boolean("toggleCurrent", false) && track->id == m_play.track_id) {
                if (m_play.status == "playing" || m_play.status == "paused" || m_play.status == "loading") {
                    dirty = true;
                    return exec_playback_op_locked("playback.toggle", Json::object(), unix_time, payload);
                }
            }
            if (args.get("queueTrackIds").is_array() && !args.get("queueTrackIds").as_array().empty()) {
                std::vector<std::string> q;
                std::set<std::string> seen;
                for (const Json& id : args.get("queueTrackIds").as_array()) {
                    if (id.is_string() && find_track_locked(id.as_str()) && seen.insert(id.as_str()).second) {
                        q.push_back(id.as_str());
                    }
                }
                if (!q.empty()) m_state.queue = std::move(q);
            }
            begin_play_track_locked(*track, 0);
            dirty = true;
            Json r = Json::object();
            r["pending"] = true;
            r["trackId"] = track->id;
            return r;
        }

        if (op == "playback.playPlaylist") {
            std::string vid = args.str("viewId");
            if (vid.empty()) vid = m_state.active_view;
            std::vector<std::string> list;
            if (vid == "smart_all") {
                for (const auto& t : m_state.tracks) list.push_back(t.id);
            } else if (vid == "smart_fav") {
                for (const auto& t : m_state.tracks) {
                    if (t.favorite) list.push_back(t.id);
                }
            } else if (vid == "smart_recent") {
                list = m_state.recent_ids;
            } else if (vid == "smart_downloaded") {
                refresh_downloads_from_store_locked();
                for (const auto& t : m_state.tracks) {
                    auto it = m_downloads.find(t.id);
                    if (it != m_downloads.end() && it->second.status == "downloaded") list.push_back(t.id);
                }
            } else if (Playlist* pl = find_playlist_locked(vid)) {
                list = pl->track_ids;
            }
            if (list.empty()) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_playlist";
                r["message"] = tr_loc(loc, "当前歌单暂无曲目", "Playlist is empty");
                return r;
            }
            m_state.queue = list;
            if (args.boolean("shuffle", false)) m_state.play_mode = "shuffle";
            Track* first = find_track_locked(list.front());
            if (!first) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "empty_playlist";
                return r;
            }
            begin_play_track_locked(*first, 0);
            dirty = true;
            Json r = Json::object();
            r["pending"] = true;
            r["trackId"] = first->id;
            return r;
        }

        if (op == "playback.toggle" || op == "playback.next" || op == "playback.prev" ||
            op == "playback.stop" || op == "playback.seek" || op == "playback.cycleMode") {
            dirty = true;
            return exec_playback_op_locked(op, args, unix_time, payload);
        }

        if (op == "playback.setMode") {
            std::string m = args.str("mode");
            if (m == "sequence" || m == "shuffle" || m == "repeat_one") {
                m_state.play_mode = m;
                dirty = true;
                Json r = Json::object();
                r["ok"] = true;
                r["mode"] = m;
                return r;
            }
            Json r = Json::object();
            r["ok"] = false;
            r["code"] = "invalid_mode";
            return r;
        }

        if (op == "playback.setVolume") {
            const double previous_volume = m_state.volume;
            const bool was_muted = m_state.muted;
            if (!args.get("volume").is_null()) {
                m_state.volume = clamp_d(args.num("volume", m_state.volume), 0.0, 1.0);
            }
            if (!args.get("muted").is_null()) {
                m_state.muted = args.boolean("muted", false);
            }
            dirty = m_state.volume != previous_volume || m_state.muted != was_muted;
            if (dirty) m_player.set_volume(effective_volume_locked());
            Json r = Json::object();
            r["ok"] = true;
            r["volume"] = m_state.volume;
            r["muted"] = m_state.muted;
            return r;
        }

        if (op == "queue.add") {
            std::string tid = args.str("trackId");
            if (!find_track_locked(tid)) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            if (index_of_str(m_state.queue, tid) < 0) m_state.queue.push_back(tid);
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "queue.playNext") {
            std::string tid = args.str("trackId");
            if (!find_track_locked(tid)) {
                Json r = Json::object();
                r["ok"] = false;
                r["code"] = "not_found";
                return r;
            }
            remove_str(m_state.queue, tid);
            int cur = index_of_str(m_state.queue, m_play.track_id);
            if (cur >= 0) m_state.queue.insert(m_state.queue.begin() + cur + 1, tid);
            else m_state.queue.insert(m_state.queue.begin(), tid);
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "queue.remove") {
            remove_str(m_state.queue, args.str("trackId"));
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        if (op == "queue.clear") {
            m_state.queue.clear();
            dirty = true;
            Json r = Json::object();
            r["ok"] = true;
            return r;
        }

        Json r = Json::object();
        r["ok"] = false;
        r["code"] = "unknown_operation";
        return r;
    }
};

static std::mutex g_engine_mu;
static std::unique_ptr<MusicEngine> g_engine;

static MusicEngine& engine() {
    std::lock_guard<std::mutex> lock(g_engine_mu);
    if (!g_engine) g_engine = std::make_unique<MusicEngine>();
    return *g_engine;
}

} // namespace music

extern "C" {

uint32_t quailcrate_abi_version(void) {
    return QUAILCRATE_ABI_VERSION_2;
}

void quailcrate_bind_host(quailcrate_host_notify_fn notify) {
    music::engine().bind_host(notify);
}

int32_t quailcrate_invoke(
    const uint8_t* request,
    uint32_t request_length,
    uint8_t** response,
    uint32_t* response_length) {
    if (!response || !response_length) return -1;
    *response = nullptr;
    *response_length = 0;
    if (!request || request_length == 0 || request_length > 4 * 1024 * 1024) return -1;

    try {
        std::string out = music::engine().handle_request(request, request_length);
        if (out.empty() || out.size() > 4 * 1024 * 1024) return -2;
        uint8_t* buf = static_cast<uint8_t*>(std::malloc(out.size()));
        if (!buf) return -2;
        std::memcpy(buf, out.data(), out.size());
        *response = buf;
        *response_length = static_cast<uint32_t>(out.size());
        return 0;
    } catch (...) {
        return -3;
    }
}

void quailcrate_free(uint8_t* response) {
    std::free(response);
}

} // extern "C"
