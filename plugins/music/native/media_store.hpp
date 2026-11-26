#pragma once

#include "http_client.hpp"
#include "json.hpp"

#include <algorithm>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <cstring>
#include <map>
#include <memory>
#include <mutex>
#include <string>
#include <vector>

namespace music {

inline bool ensure_dir_recursive(const std::wstring& dir) {
    if (dir.empty()) return false;
    DWORD attrs = GetFileAttributesW(dir.c_str());
    if (attrs != INVALID_FILE_ATTRIBUTES && (attrs & FILE_ATTRIBUTE_DIRECTORY)) {
        return true;
    }
    size_t pos = dir.find_last_of(L"\\/");
    if (pos != std::wstring::npos && pos > 2) {
        ensure_dir_recursive(dir.substr(0, pos));
    }
    if (CreateDirectoryW(dir.c_str(), nullptr)) return true;
    return GetLastError() == ERROR_ALREADY_EXISTS;
}

inline std::wstring detect_audio_extension(const uint8_t* data, size_t len) {
    if (len >= 12 && std::memcmp(data, "RIFF", 4) == 0 && std::memcmp(data + 8, "WAVE", 4) == 0) {
        return L".wav";
    }
    if (len >= 8 && std::memcmp(data + 4, "ftyp", 4) == 0) {
        return L".m4a";
    }
    if (len >= 4 && std::memcmp(data, "fLaC", 4) == 0) {
        return L".flac";
    }
    if (len >= 4 && std::memcmp(data, "OggS", 4) == 0) {
        return L".ogg";
    }
    if (len >= 3 && std::memcmp(data, "ID3", 3) == 0) {
        return L".mp3";
    }
    if (len >= 2 && data[0] == 0xFF && (data[1] & 0xE0) == 0xE0) {
        return L".mp3";
    }
    return L".m4a";
}

inline std::string safe_filename_hash(const std::string& track_id, const std::string& quality) {
    return md5_hex(track_id + "\n" + quality);
}

struct MediaTransfer {
    std::string track_id;
    std::string quality;
    std::string label;
    std::wstring part_path;

    std::atomic<bool> cancel{ false };
    std::atomic<bool> want_download{ false };

    mutable std::mutex mu;
    std::condition_variable cv;
    int64_t bytes_written = 0;
    int64_t total_bytes = -1;
    std::vector<uint8_t> header_prefix;
    bool done = false;
    bool succeeded = false;
    std::string error_code;
    std::wstring final_path;
    int percent = 0;

    std::wstring readable_path() const {
        std::lock_guard<std::mutex> lock(mu);
        return !final_path.empty() ? final_path : part_path;
    }

    bool wait_for_bytes(int64_t needed, const std::atomic<bool>& stop_flag, int64_t& avail, bool& is_done, bool& ok) {
        std::unique_lock<std::mutex> lock(mu);
        cv.wait(lock, [&]() {
            return stop_flag.load() || done || bytes_written >= needed;
        });
        avail = bytes_written;
        is_done = done;
        ok = succeeded;
        return !stop_flag.load();
    }

    bool wait_complete(const std::atomic<bool>& stop_flag, std::wstring& out_path, std::string& out_err) {
        std::unique_lock<std::mutex> lock(mu);
        cv.wait(lock, [&]() {
            return stop_flag.load() || done;
        });
        if (stop_flag.load()) {
            out_err = "canceled";
            return false;
        }
        if (!succeeded) {
            out_err = error_code.empty() ? "network_error" : error_code;
            return false;
        }
        out_path = !final_path.empty() ? final_path : part_path;
        return true;
    }
};

struct MediaEntry {
    std::string track_id;
    std::string quality;
    std::string label;
    std::string filename; // relative filename inside cache/ or downloads/
    int64_t bytes = 0;
    int64_t updated_at = 0;
};

struct LocalMediaHit {
    bool found = false;
    bool downloaded = false;
    std::wstring path;
    std::string quality;
    std::string label;
    int64_t bytes = 0;
};

struct DownloadStatus {
    std::string status; // "downloading", "downloaded", "error"
    int percent = 0;
    std::string error;
    bool has_percent = false;
};

class MediaStore {
public:
    static constexpr size_t kMaxRecentCache = 30;

    void configure(const std::wstring& data_dir) {
        std::lock_guard<std::mutex> lock(m_mu);
        m_root = data_dir;
        m_cache_dir = data_dir + L"\\cache";
        m_downloads_dir = data_dir + L"\\downloads";
        m_part_dir = data_dir + L"\\cache\\part";
        ensure_dir_recursive(m_cache_dir);
        ensure_dir_recursive(m_downloads_dir);
        ensure_dir_recursive(m_part_dir);
        cleanup_stale_parts_locked();
        load_index_locked(m_cache_dir + L"\\index.json", m_cache_dir, m_cache);
        load_index_locked(m_downloads_dir + L"\\index.json", m_downloads_dir, m_downloads);
    }

    std::wstring make_part_path(const std::string& track_id, const std::string& quality, uint64_t seq) {
        std::lock_guard<std::mutex> lock(m_mu);
        ensure_dir_recursive(m_part_dir);
        return m_part_dir + L"\\" + utf8_to_wide(safe_filename_hash(track_id, quality) + "-" + std::to_string(seq) + ".part");
    }

    LocalMediaHit lookup_local(const std::string& track_id, const std::string& preferred_quality) {
        std::lock_guard<std::mutex> lock(m_mu);
        // 1. Prefer manual download (exact quality, else any downloaded quality for track)
        for (int pass = 0; pass < 2; ++pass) {
            for (const auto& kv : m_downloads) {
                const MediaEntry& e = kv.second;
                if (e.track_id != track_id) continue;
                if (pass == 0 && !preferred_quality.empty() && e.quality != preferred_quality) continue;
                std::wstring full = m_downloads_dir + L"\\" + utf8_to_wide(e.filename);
                if (file_exists(full)) {
                    return { true, true, full, e.quality, e.label, e.bytes };
                }
            }
        }
        // 2. Fall back to recent cache (exact quality first, else any cached quality)
        for (int pass = 0; pass < 2; ++pass) {
            for (auto& kv : m_cache) {
                MediaEntry& e = kv.second;
                if (e.track_id != track_id) continue;
                if (pass == 0 && !preferred_quality.empty() && e.quality != preferred_quality) continue;
                std::wstring full = m_cache_dir + L"\\" + utf8_to_wide(e.filename);
                if (file_exists(full)) {
                    e.updated_at = now_unix();
                    save_index_locked(m_cache_dir + L"\\index.json", m_cache);
                    return { true, false, full, e.quality, e.label, e.bytes };
                }
            }
        }
        return {};
    }

    std::shared_ptr<MediaTransfer> find_active_transfer(const std::string& track_id, const std::string& quality) {
        std::lock_guard<std::mutex> lock(m_mu);
        return find_active_transfer_locked(track_id, quality);
    }

    std::shared_ptr<MediaTransfer> find_or_create_transfer(const std::string& track_id,
                                                           const std::string& quality,
                                                           uint64_t part_seq,
                                                           bool& created) {
        std::lock_guard<std::mutex> lock(m_mu);
        if (auto existing = find_active_transfer_locked(track_id, quality)) {
            created = false;
            return existing;
        }
        ensure_dir_recursive(m_part_dir);
        auto t = std::make_shared<MediaTransfer>();
        t->track_id = track_id;
        t->quality = quality;
        t->part_path = m_part_dir + L"\\" + utf8_to_wide(safe_filename_hash(track_id, quality) + "-" + std::to_string(part_seq) + ".part");
        m_active[track_id + ":" + quality] = t;
        created = true;
        return t;
    }

    void register_transfer(const std::shared_ptr<MediaTransfer>& t) {
        std::lock_guard<std::mutex> lock(m_mu);
        m_active[t->track_id + ":" + t->quality] = t;
    }

    void unregister_transfer(const std::shared_ptr<MediaTransfer>& t) {
        std::lock_guard<std::mutex> lock(m_mu);
        std::string key = t->track_id + ":" + t->quality;
        auto it = m_active.find(key);
        if (it != m_active.end() && it->second == t) {
            m_active.erase(it);
        }
    }

    // Commit a completed .part file into cache/ (and downloads/ if want_download).
    // Returns final usable path.
    std::wstring commit_transfer(const std::shared_ptr<MediaTransfer>& t, bool to_cache, bool to_download, std::string& out_err) {
        std::wstring ext = L".m4a";
        int64_t bytes = 0;
        {
            std::lock_guard<std::mutex> tl(t->mu);
            ext = detect_audio_extension(t->header_prefix.data(), t->header_prefix.size());
            bytes = t->bytes_written;
        }
        std::lock_guard<std::mutex> lock(m_mu);
        std::string base_name = safe_filename_hash(t->track_id, t->quality) + wide_to_utf8(ext);
        std::wstring final_result;

        if (to_cache) {
            ensure_dir_recursive(m_cache_dir);
            std::wstring cache_target = m_cache_dir + L"\\" + utf8_to_wide(base_name);
            {
                std::lock_guard<std::mutex> tl(t->mu);
                if (!MoveFileExW(t->part_path.c_str(), cache_target.c_str(), MOVEFILE_REPLACE_EXISTING)) {
                    if (!CopyFileW(t->part_path.c_str(), cache_target.c_str(), FALSE)) {
                        out_err = (GetLastError() == ERROR_DISK_FULL) ? "disk_full" : "storage_unavailable";
                        return L"";
                    }
                    DeleteFileW(t->part_path.c_str());
                }
                t->final_path = cache_target;
            }
            MediaEntry entry{ t->track_id, t->quality, t->label, base_name, bytes, now_unix() };
            m_cache[t->track_id + ":" + t->quality] = entry;
            evict_cache_locked();
            save_index_locked(m_cache_dir + L"\\index.json", m_cache);
            final_result = cache_target;
        }

        if (to_download) {
            ensure_dir_recursive(m_downloads_dir);
            std::wstring dl_target = m_downloads_dir + L"\\" + utf8_to_wide(base_name);
            std::wstring src = !final_result.empty() ? final_result : t->part_path;
            // Remove older download for same track if different filename
            auto existing = m_downloads.find(t->track_id);
            if (existing != m_downloads.end() && existing->second.filename != base_name) {
                DeleteFileW((m_downloads_dir + L"\\" + utf8_to_wide(existing->second.filename)).c_str());
            }
            bool copied = false;
            if (!final_result.empty()) {
                copied = CopyFileW(src.c_str(), dl_target.c_str(), FALSE) != 0;
            } else {
                std::lock_guard<std::mutex> tl(t->mu);
                copied = MoveFileExW(src.c_str(), dl_target.c_str(), MOVEFILE_REPLACE_EXISTING) != 0;
                if (!copied) {
                    copied = CopyFileW(src.c_str(), dl_target.c_str(), FALSE) != 0;
                    if (copied) DeleteFileW(src.c_str());
                }
                if (copied) t->final_path = dl_target;
            }
            if (!copied) {
                out_err = (GetLastError() == ERROR_DISK_FULL) ? "disk_full" : "storage_unavailable";
                return final_result;
            }
            MediaEntry entry{ t->track_id, t->quality, t->label, base_name, bytes, now_unix() };
            m_downloads[t->track_id] = entry;
            save_index_locked(m_downloads_dir + L"\\index.json", m_downloads);
            if (final_result.empty()) final_result = dl_target;
        }

        return final_result;
    }

    // Promote an existing cached file directly to downloads/ offline.
    bool promote_cached_to_download(const std::string& track_id, const std::string& quality, std::string& out_err) {
        std::lock_guard<std::mutex> lock(m_mu);
        if (m_downloads.find(track_id) != m_downloads.end()) {
            std::wstring full = m_downloads_dir + L"\\" + utf8_to_wide(m_downloads[track_id].filename);
            if (file_exists(full)) return true;
        }
        MediaEntry* found = nullptr;
        for (int pass = 0; pass < 2 && !found; ++pass) {
            for (auto& kv : m_cache) {
                if (kv.second.track_id != track_id) continue;
                if (pass == 0 && !quality.empty() && kv.second.quality != quality) continue;
                std::wstring full = m_cache_dir + L"\\" + utf8_to_wide(kv.second.filename);
                if (file_exists(full)) {
                    found = &kv.second;
                    break;
                }
            }
        }
        if (!found) return false;
        ensure_dir_recursive(m_downloads_dir);
        std::wstring src = m_cache_dir + L"\\" + utf8_to_wide(found->filename);
        std::wstring dst = m_downloads_dir + L"\\" + utf8_to_wide(found->filename);
        if (!CopyFileW(src.c_str(), dst.c_str(), FALSE)) {
            out_err = (GetLastError() == ERROR_DISK_FULL) ? "disk_full" : "storage_unavailable";
            return false;
        }
        MediaEntry copy = *found;
        copy.updated_at = now_unix();
        m_downloads[track_id] = copy;
        save_index_locked(m_downloads_dir + L"\\index.json", m_downloads);
        return true;
    }

    void remove_download(const std::string& track_id) {
        std::lock_guard<std::mutex> lock(m_mu);
        auto it = m_downloads.find(track_id);
        if (it != m_downloads.end()) {
            std::wstring full = m_downloads_dir + L"\\" + utf8_to_wide(it->second.filename);
            DeleteFileW(full.c_str());
            m_downloads.erase(it);
            save_index_locked(m_downloads_dir + L"\\index.json", m_downloads);
        }
    }

    void reject_cached(const std::string& track_id, const std::string& quality) {
        std::lock_guard<std::mutex> lock(m_mu);
        std::string key = track_id + ":" + quality;
        auto it = m_cache.find(key);
        if (it != m_cache.end()) {
            DeleteFileW((m_cache_dir + L"\\" + utf8_to_wide(it->second.filename)).c_str());
            m_cache.erase(it);
            save_index_locked(m_cache_dir + L"\\index.json", m_cache);
        }
    }

    std::map<std::string, DownloadStatus> downloaded_snapshot() const {
        std::lock_guard<std::mutex> lock(m_mu);
        std::map<std::string, DownloadStatus> out;
        for (const auto& kv : m_downloads) {
            std::wstring full = m_downloads_dir + L"\\" + utf8_to_wide(kv.second.filename);
            if (file_exists(full)) {
                out[kv.first] = { "downloaded", 100, "", true };
            }
        }
        return out;
    }

    void cancel_all_transfers() {
        std::lock_guard<std::mutex> lock(m_mu);
        for (auto& kv : m_active) {
            if (kv.second) {
                kv.second->cancel.store(true);
                kv.second->cv.notify_all();
            }
        }
    }

private:
    mutable std::mutex m_mu;
    std::wstring m_root;
    std::wstring m_cache_dir;
    std::wstring m_downloads_dir;
    std::wstring m_part_dir;
    std::map<std::string, MediaEntry> m_cache;     // key = track_id + ":" + quality
    std::map<std::string, MediaEntry> m_downloads; // key = track_id
    std::map<std::string, std::shared_ptr<MediaTransfer>> m_active;

    std::shared_ptr<MediaTransfer> find_active_transfer_locked(const std::string& track_id, const std::string& quality) const {
        std::string key = track_id + ":" + quality;
        auto it = m_active.find(key);
        if (it != m_active.end()) return it->second;
        for (const auto& kv : m_active) {
            if (kv.second && kv.second->track_id == track_id) return kv.second;
        }
        return nullptr;
    }

    static int64_t now_unix() {
        return std::chrono::duration_cast<std::chrono::seconds>(
                   std::chrono::system_clock::now().time_since_epoch())
            .count();
    }

    static bool file_exists(const std::wstring& path) {
        DWORD attrs = GetFileAttributesW(path.c_str());
        return attrs != INVALID_FILE_ATTRIBUTES && !(attrs & FILE_ATTRIBUTE_DIRECTORY);
    }

    void cleanup_stale_parts_locked() {
        WIN32_FIND_DATAW fd{};
        HANDLE h = FindFirstFileW((m_part_dir + L"\\*.part").c_str(), &fd);
        if (h == INVALID_HANDLE_VALUE) return;
        do {
            if (!(fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)) {
                DeleteFileW((m_part_dir + L"\\" + fd.cFileName).c_str());
            }
        } while (FindNextFileW(h, &fd));
        FindClose(h);
    }

    static void load_index_locked(const std::wstring& index_path, const std::wstring& dir,
                                  std::map<std::string, MediaEntry>& dst) {
        dst.clear();
        std::string raw;
        if (!read_file_all(index_path, raw, 4 << 20)) return;
        Json parsed;
        if (!Json::parse(raw, parsed) || !parsed.is_object()) return;
        for (const auto& kv : parsed.as_object()) {
            const Json& v = kv.second;
            if (!v.is_object()) continue;
            MediaEntry e;
            e.track_id = v.str("trackId");
            e.quality = v.str("quality");
            e.label = v.str("label");
            e.filename = v.str("filename");
            e.bytes = v.i64("bytes");
            e.updated_at = v.i64("updatedAt");
            if (!e.track_id.empty() && !e.filename.empty() && file_exists(dir + L"\\" + utf8_to_wide(e.filename))) {
                dst[kv.first] = std::move(e);
            }
        }
    }

    static void save_index_locked(const std::wstring& index_path, const std::map<std::string, MediaEntry>& src) {
        Json root = Json::object();
        for (const auto& kv : src) {
            Json o = Json::object();
            o["trackId"] = kv.second.track_id;
            o["quality"] = kv.second.quality;
            o["label"] = kv.second.label;
            o["filename"] = kv.second.filename;
            o["bytes"] = kv.second.bytes;
            o["updatedAt"] = kv.second.updated_at;
            root[kv.first] = std::move(o);
        }
        std::string dumped = root.dump();
        write_file_atomic(index_path, dumped.data(), dumped.size());
    }

    void evict_cache_locked() {
        while (m_cache.size() > kMaxRecentCache) {
            auto oldest = m_cache.begin();
            for (auto it = m_cache.begin(); it != m_cache.end(); ++it) {
                if (it->second.updated_at < oldest->second.updated_at) oldest = it;
            }
            DeleteFileW((m_cache_dir + L"\\" + utf8_to_wide(oldest->second.filename)).c_str());
            m_cache.erase(oldest);
        }
    }
};

} // namespace music
