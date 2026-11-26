#pragma once

#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <avrt.h>
#include <mfapi.h>
#include <mferror.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <mmsystem.h>
#include <objbase.h>
#include <propvarutil.h>

#pragma comment(lib, "mfplat.lib")
#pragma comment(lib, "avrt.lib")
#pragma comment(lib, "mfreadwrite.lib")
#pragma comment(lib, "mfuuid.lib")
#pragma comment(lib, "ole32.lib")
#pragma comment(lib, "winmm.lib")

#include "media_store.hpp"

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <condition_variable>
#include <cstdint>
#include <cstring>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

namespace music {

struct AudioProgress {
    uint64_t session_id = 0;
    std::string status; // "loading", "playing", "paused", "stopped", "ended", "error"
    int64_t position_ms = 0;
    int64_t duration_ms = 0;
    bool seekable = false;
    std::string error_code;
};

struct WaveChunk {
    WAVEHDR hdr{};
    std::vector<uint8_t> pcm;
};

class WaveOutDevice {
public:
    ~WaveOutDevice() { close(); }

    bool open(const WAVEFORMATEX& fmt, HANDLE wake, double volume, bool local_file) {
        close();
        if (!wake) return false;
        m_control_failed = false;
        m_fmt = fmt;
        MMRESULT res = waveOutOpen(&m_handle, WAVE_MAPPER, &m_fmt,
                                   reinterpret_cast<DWORD_PTR>(wake), 0, CALLBACK_EVENT);
        if (res != MMSYSERR_NOERROR) {
            m_handle = nullptr;
            return false;
        }
        if (waveOutPause(m_handle) != MMSYSERR_NOERROR) {
            close();
            return false;
        }
        m_priming = true;
        m_base_ms = 0;
        m_volume = volume;
        m_device_volume = set_device_volume(volume);
        // Local playback can read ahead without delaying a growing download.
        // Bound the latency of software gain when stream volume is unavailable.
        const unsigned reserve_ms = local_file && m_device_volume ? 2000 : 600;
        m_reserve_bytes = (uint64_t(fmt.nAvgBytesPerSec) * reserve_ms + 999) / 1000;
        DWORD task_index = 0;
        m_mm_task = AvSetMmThreadCharacteristicsW(L"Audio", &task_index);
        return true;
    }

    void close() {
        if (!m_handle) return;
        waveOutReset(m_handle);
        reap(true);
        waveOutClose(m_handle);
        m_handle = nullptr;
        m_chunks.clear();
        m_staging.clear();
        if (m_mm_task) {
            AvRevertMmThreadCharacteristics(m_mm_task);
            m_mm_task = nullptr;
        }
    }

    void set_volume(double volume) {
        if (volume == m_volume) return;
        if (!m_device_volume || set_device_volume(volume)) m_volume = volume;
    }

    bool submit(std::vector<uint8_t> pcm) {
        if (!m_handle || pcm.empty()) return false;
        // Give WinMM fewer, longer blocks than the individual decoder samples.
        const size_t target = (size_t(m_fmt.nSamplesPerSec) + 9) / 10 * m_fmt.nBlockAlign;
        if (m_staging.empty() && pcm.size() >= target) {
            m_staging = std::move(pcm);
        } else {
            if (m_staging.empty()) m_staging.reserve(target + pcm.size());
            m_staging.insert(m_staging.end(), pcm.begin(), pcm.end());
        }
        return m_staging.size() < target || flush_staging();
    }

    void reap(bool force_all = false) {
        if (!m_handle) return;
        while (!m_chunks.empty()) {
            auto& front = m_chunks.front();
            if (!force_all && (front->hdr.dwFlags & WHDR_DONE) == 0) break;
            waveOutUnprepareHeader(m_handle, &front->hdr, sizeof(WAVEHDR));
            m_queued_bytes -= front->pcm.size();
            m_chunks.pop_front();
        }
    }

    bool empty() {
        reap();
        return m_chunks.empty() && m_staging.empty();
    }

    bool needs_audio() {
        reap(); // Decoding or a scheduling delay may have drained several headers.
        if (m_chunks.empty() && !m_priming) {
            if (!pause()) return false;
            m_priming = true;
        }
        return !has_reserve();
    }

    bool priming() const { return m_priming; }
    bool control_failed() const { return m_control_failed; }

    bool start_if_ready(bool end_of_source, bool awaiting_source = false) {
        if (m_control_failed) return false;
        // Submit the short tail even if it cannot fill a header. A growing WAV
        // can also use the partial block before its last running header drains.
        if ((end_of_source || (awaiting_source && !m_priming && m_chunks.size() <= 1)) && !flush_staging()) {
            m_control_failed = true;
            return false;
        }
        if (m_priming && !m_chunks.empty() && (end_of_source || has_reserve())) {
            if (!check_control(waveOutRestart(m_handle))) return false;
            m_priming = false;
        }
        return !m_priming;
    }

    bool pause() {
        return check_control(waveOutPause(m_handle));
    }

    bool resume() {
        return m_priming || check_control(waveOutRestart(m_handle));
    }

    bool reset_for_seek(int64_t new_base_ms) {
        if (!check_control(waveOutReset(m_handle))) return false;
        reap(true);
        m_staging.clear();
        m_base_ms = new_base_ms;
        m_priming = true;
        return pause();
    }

    int64_t position_ms() const {
        if (!m_handle || m_fmt.nAvgBytesPerSec == 0) return m_base_ms;
        MMTIME mm{};
        mm.wType = TIME_BYTES;
        if (waveOutGetPosition(m_handle, &mm, sizeof(mm)) != MMSYSERR_NOERROR) {
            return m_base_ms;
        }
        switch (mm.wType) {
        case TIME_MS:
            return m_base_ms + static_cast<int64_t>(mm.u.ms);
        case TIME_SAMPLES:
            return m_base_ms + static_cast<int64_t>(mm.u.sample) * 1000LL / static_cast<int64_t>(m_fmt.nSamplesPerSec);
        case TIME_BYTES:
            return m_base_ms + static_cast<int64_t>(mm.u.cb) * 1000LL / static_cast<int64_t>(m_fmt.nAvgBytesPerSec);
        default:
            return m_base_ms;
        }
    }

    const WAVEFORMATEX& format() const { return m_fmt; }

private:
    bool flush_staging() {
        if (m_staging.empty()) return true;
        auto chunk = std::make_unique<WaveChunk>();
        chunk->pcm.swap(m_staging);
        if (!m_device_volume && m_volume < 0.9999) {
            int16_t* samples = reinterpret_cast<int16_t*>(chunk->pcm.data());
            size_t count = chunk->pcm.size() / 2;
            for (size_t i = 0; i < count; ++i) {
                samples[i] = static_cast<int16_t>(std::lround(static_cast<double>(samples[i]) * m_volume));
            }
        }
        chunk->hdr.lpData = reinterpret_cast<LPSTR>(chunk->pcm.data());
        chunk->hdr.dwBufferLength = static_cast<DWORD>(chunk->pcm.size());
        if (waveOutPrepareHeader(m_handle, &chunk->hdr, sizeof(WAVEHDR)) != MMSYSERR_NOERROR) {
            return false;
        }
        if (waveOutWrite(m_handle, &chunk->hdr, sizeof(WAVEHDR)) != MMSYSERR_NOERROR) {
            waveOutUnprepareHeader(m_handle, &chunk->hdr, sizeof(WAVEHDR));
            return false;
        }
        m_queued_bytes += chunk->pcm.size();
        m_chunks.push_back(std::move(chunk));
        return true;
    }

    bool check_control(MMRESULT result) {
        if (result != MMSYSERR_NOERROR) m_control_failed = true;
        return !m_control_failed;
    }

    bool set_device_volume(double volume) {
        // Use the stream handle. A numeric device ID would change all its streams.
        const DWORD gain = static_cast<DWORD>(std::lround(volume * 65535.0));
        return waveOutSetVolume(m_handle, MAKELONG(gain, gain)) == MMSYSERR_NOERROR;
    }

    bool has_reserve() const {
        if (m_chunks.empty()) return false;
        // The head may be almost finished.
        return m_queued_bytes - m_chunks.front()->pcm.size() >= m_reserve_bytes;
    }

    HWAVEOUT m_handle = nullptr;
    HANDLE m_mm_task = nullptr;
    bool m_priming = true;
    bool m_control_failed = false;
    bool m_device_volume = false;
    double m_volume = 1.0;
    WAVEFORMATEX m_fmt{};
    std::deque<std::unique_ptr<WaveChunk>> m_chunks;
    std::vector<uint8_t> m_staging;
    uint64_t m_queued_bytes = 0;
    uint64_t m_reserve_bytes = 0;
    int64_t m_base_ms = 0;
};

class NativeAudioPlayer {
public:
    using ProgressCallback = std::function<void(const AudioProgress&)>;

    explicit NativeAudioPlayer(ProgressCallback cb) : m_cb(std::move(cb)) {}

    ~NativeAudioPlayer() {
        stop();
        if (m_wake) CloseHandle(m_wake);
    }

    uint64_t play_file(const std::wstring& path, int64_t fallback_duration_ms, int64_t start_position_ms, uint64_t gen = 0) {
        return start_session(path, nullptr, fallback_duration_ms, start_position_ms, gen);
    }

    uint64_t play_transfer(const std::shared_ptr<MediaTransfer>& transfer, int64_t fallback_duration_ms, int64_t start_position_ms, uint64_t gen = 0) {
        return start_session(L"", transfer, fallback_duration_ms, start_position_ms, gen);
    }

    void stop_through(uint64_t gen) {
        std::lock_guard<std::mutex> lock(m_lifecycle_mu);
        if (gen < m_active_gen) return;
        m_active_gen = gen;
        stop_locked();
    }

    void pause() {
        m_paused.store(true);
        wake_commands();
    }

    void resume() {
        m_paused.store(false);
        wake_commands();
    }

    void seek(int64_t ms) {
        m_seek_ms.store(std::max<int64_t>(0, ms));
        wake_commands();
    }

    void set_volume(double v) {
        const double volume = std::max(0.0, std::min(1.0, v));
        if (m_volume.exchange(volume) != volume && m_wake) SetEvent(m_wake);
    }

    void stop() {
        std::lock_guard<std::mutex> lock(m_lifecycle_mu);
        stop_locked();
    }

private:
    uint64_t start_session(const std::wstring& path, const std::shared_ptr<MediaTransfer>& transfer,
                           int64_t duration_ms, int64_t start_position_ms, uint64_t gen) {
        std::lock_guard<std::mutex> lock(m_lifecycle_mu);
        if (gen != 0 && gen < m_active_gen) return 0;
        if (gen > m_active_gen) m_active_gen = gen;
        stop_locked();
        // The owner can register its generation before the first audio callback,
        // including an immediate open error. Standalone callers can omit it.
        const uint64_t sid = gen ? gen : m_session_seq + 1;
        m_session_seq = std::max(m_session_seq, sid);
        m_stop.store(false);
        m_paused.store(false);
        m_seek_ms.store(start_position_ms > 0 ? start_position_ms : -1);
        std::atomic_store(&m_active_transfer, transfer);
        m_reports_done = false;
        m_thread = std::thread([this, sid, path, transfer, duration_ms]() {
            run_session(sid, path, transfer, duration_ms);
            {
                std::lock_guard<std::mutex> report_lock(m_report_mu);
                m_reports_done = true;
            }
            m_report_cv.notify_one();
        });
        m_report_thread = std::thread([this]() { deliver_progress(); });
        return sid;
    }

    void stop_locked() {
        m_stop.store(true);
        wake_commands();
        if (m_thread.joinable()) {
            m_thread.join();
        }
        if (m_report_thread.joinable()) {
            m_report_thread.join();
        }
        std::atomic_store(&m_active_transfer, std::shared_ptr<MediaTransfer>{});
    }

    void wake_commands() {
        if (m_wake) SetEvent(m_wake);
        if (auto transfer = std::atomic_load(&m_active_transfer)) {
            // Serialize with the transfer's predicate check before it goes to sleep.
            std::lock_guard<std::mutex> lock(transfer->mu);
            transfer->cv.notify_all();
        }
    }

    bool can_feed() const {
        return !m_stop.load() && !m_paused.load() && m_seek_ms.load() < 0;
    }

    ProgressCallback m_cb;
    std::mutex m_lifecycle_mu;
    uint64_t m_active_gen = 0;
    uint64_t m_session_seq = 0; // guarded by m_lifecycle_mu
    std::atomic<bool> m_stop{ false };
    std::atomic<bool> m_paused{ false };
    std::atomic<double> m_volume{ 0.8 };
    std::atomic<int64_t> m_seek_ms{ -1 };
    // WinMM buffer completions and player commands both wake the sole feeder.
    // Reset before examining device/command state, so arrivals during refill stick.
    HANDLE m_wake = CreateEventW(nullptr, TRUE, FALSE, nullptr);
    std::shared_ptr<MediaTransfer> m_active_transfer;
    std::thread m_thread;
    std::mutex m_report_mu;
    std::condition_variable m_report_cv;
    std::optional<AudioProgress> m_pending_report;
    bool m_reports_done = true;
    std::thread m_report_thread;

    struct CanonicalWAV {
        bool valid = false;
        WAVEFORMATEX fmt{};
        int64_t data_bytes = 0;
    };

    static CanonicalWAV parse_canonical_wav(const uint8_t* b, size_t len) {
        CanonicalWAV out;
        if (len < 44) return out;
        if (std::memcmp(b, "RIFF", 4) != 0 || std::memcmp(b + 8, "WAVEfmt ", 8) != 0 || std::memcmp(b + 36, "data", 4) != 0) {
            return out;
        }
        auto u16 = [&](size_t off) -> uint16_t {
            return static_cast<uint16_t>(b[off]) | (static_cast<uint16_t>(b[off + 1]) << 8);
        };
        auto u32 = [&](size_t off) -> uint32_t {
            return static_cast<uint32_t>(b[off]) | (static_cast<uint32_t>(b[off + 1]) << 8) |
                   (static_cast<uint32_t>(b[off + 2]) << 16) | (static_cast<uint32_t>(b[off + 3]) << 24);
        };
        if (u32(16) != 16) return out;
        WAVEFORMATEX f{};
        f.wFormatTag = u16(20);
        f.nChannels = u16(22);
        f.nSamplesPerSec = u32(24);
        f.nAvgBytesPerSec = u32(28);
        f.nBlockAlign = u16(32);
        f.wBitsPerSample = u16(34);
        uint32_t data_len = u32(40);
        if (f.wFormatTag != WAVE_FORMAT_PCM || f.nChannels < 1 || f.nChannels > 2 || f.wBitsPerSample != 16 ||
            f.nSamplesPerSec < 8000 || f.nSamplesPerSec > 192000 || f.nBlockAlign != f.nChannels * 2 ||
            f.nAvgBytesPerSec != f.nSamplesPerSec * f.nBlockAlign || data_len == 0 || (data_len % f.nBlockAlign) != 0) {
            return out;
        }
        out.valid = true;
        out.fmt = f;
        out.data_bytes = static_cast<int64_t>(data_len);
        return out;
    }

    void emit(uint64_t sid, const std::string& status, int64_t pos, int64_t dur, bool seekable, const std::string& err = "") {
        {
            std::lock_guard<std::mutex> lock(m_report_mu);
            m_pending_report = AudioProgress{ sid, status, pos, dur, seekable, err };
        }
        m_report_cv.notify_one();
    }

    // Host refreshes can block on a full IPC pipe. Deliver the latest playback
    // snapshot separately so even a blocked callback cannot starve waveOut.
    void deliver_progress() {
        for (;;) {
            AudioProgress report;
            {
                std::unique_lock<std::mutex> lock(m_report_mu);
                m_report_cv.wait(lock, [this]() { return m_reports_done || m_pending_report.has_value(); });
                if (!m_pending_report) return;
                report = std::move(*m_pending_report);
                m_pending_report.reset();
            }
            if (m_cb) m_cb(report);
        }
    }

    void run_session(uint64_t sid, std::wstring file_path, std::shared_ptr<MediaTransfer> transfer, int64_t fallback_duration_ms) {
        if (transfer) {
            int64_t avail = 0;
            bool is_done = false, ok = false;
            if (!transfer->wait_for_bytes(44, m_stop, avail, is_done, ok)) {
                return;
            }
            if (is_done && !ok) {
                std::string err;
                {
                    std::lock_guard<std::mutex> tl(transfer->mu);
                    err = transfer->error_code;
                }
                emit(sid, "error", 0, fallback_duration_ms, false, err.empty() ? "network_error" : err);
                return;
            }
            std::vector<uint8_t> prefix;
            {
                std::lock_guard<std::mutex> tl(transfer->mu);
                prefix = transfer->header_prefix;
            }
            CanonicalWAV wav = parse_canonical_wav(prefix.data(), prefix.size());
            if (wav.valid) {
                play_streaming_wav(sid, transfer, L"", wav);
                return;
            }
            // Non-WAV compressed audio (M4A/AAC/FLAC/MP3): wait for full transfer then decode via Media Foundation.
            std::string err;
            if (!transfer->wait_complete(m_stop, file_path, err)) {
                if (!m_stop.load()) {
                    emit(sid, "error", 0, fallback_duration_ms, false, err);
                }
                return;
            }
        } else {
            // Direct local file: check if canonical WAV first (fast zero-COM path)
            uint8_t hdr[44]{};
            HANDLE f = CreateFileW(file_path.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                                   nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
            if (f == INVALID_HANDLE_VALUE) {
                emit(sid, "error", 0, fallback_duration_ms, false, "not_found");
                return;
            }
            DWORD got = 0;
            BOOL ok = ReadFile(f, hdr, 44, &got, nullptr);
            CloseHandle(f);
            if (ok && got == 44) {
                CanonicalWAV wav = parse_canonical_wav(hdr, 44);
                if (wav.valid) {
                    play_streaming_wav(sid, nullptr, file_path, wav);
                    return;
                }
            }
        }
        play_media_foundation(sid, file_path, fallback_duration_ms);
    }

    void play_streaming_wav(uint64_t sid, const std::shared_ptr<MediaTransfer>& transfer,
                            const std::wstring& static_path, const CanonicalWAV& wav) {
        WaveOutDevice dev;
        if (!dev.open(wav.fmt, m_wake, m_volume.load(), !transfer)) {
            emit(sid, "error", 0, 0, false, "audio_device_unavailable");
            return;
        }
        int64_t duration_ms = wav.data_bytes * 1000LL / static_cast<int64_t>(wav.fmt.nAvgBytesPerSec);
        int64_t block_bytes = static_cast<int64_t>(wav.fmt.nSamplesPerSec / 25) * static_cast<int64_t>(wav.fmt.nBlockAlign);
        if (block_bytes < wav.fmt.nBlockAlign) block_bytes = wav.fmt.nBlockAlign;

        HANDLE file = INVALID_HANDLE_VALUE;
        if (transfer) {
            // Hold transfer->mu across CreateFileW so commit_transfer cannot rename part_path -> final_path mid-open.
            std::lock_guard<std::mutex> tl(transfer->mu);
            const std::wstring& open_path = !transfer->final_path.empty() ? transfer->final_path : transfer->part_path;
            file = CreateFileW(open_path.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                               nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
        } else {
            file = CreateFileW(static_path.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                               nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
        }
        if (file == INVALID_HANDLE_VALUE) {
            emit(sid, "error", 0, duration_ms, false, "storage_unavailable");
            return;
        }

        int64_t offset = 0;
        bool was_paused = false;
        bool started = false;
        auto last_report = std::chrono::steady_clock::now();

        int64_t initial_seek = m_seek_ms.exchange(-1);
        if (initial_seek > 0 && duration_ms > 0) {
            int64_t clamped = std::min(duration_ms, initial_seek);
            int64_t byte_off = (clamped * static_cast<int64_t>(wav.fmt.nSamplesPerSec) / 1000LL) * wav.fmt.nBlockAlign;
            offset = std::min(wav.data_bytes, byte_off);
            dev.reset_for_seek(clamped);
        }

        for (;;) {
            ResetEvent(m_wake);
            if (m_stop.load() || dev.control_failed()) break;
            dev.set_volume(m_volume.load());

            bool paused = m_paused.load();
            if (paused != was_paused) {
                if (!(paused ? dev.pause() : dev.resume())) break;
                was_paused = paused;
                emit(sid, paused ? "paused" : "playing", std::min(duration_ms, dev.position_ms()), duration_ms, true);
                last_report = std::chrono::steady_clock::now();
            }

            int64_t req_seek = m_seek_ms.exchange(-1);
            if (req_seek >= 0) {
                int64_t clamped = std::min(duration_ms, req_seek);
                int64_t byte_off = (clamped * static_cast<int64_t>(wav.fmt.nSamplesPerSec) / 1000LL) * wav.fmt.nBlockAlign;
                offset = std::min(wav.data_bytes, byte_off);
                if (!dev.reset_for_seek(clamped)) break;
                emit(sid, paused ? "paused" : "playing", clamped, duration_ms, true);
                last_report = std::chrono::steady_clock::now();
            }

            int64_t avail_data = wav.data_bytes;
            bool transfer_done = true;
            bool transfer_ok = true;
            if (transfer) {
                std::lock_guard<std::mutex> tl(transfer->mu);
                avail_data = std::max<int64_t>(0, transfer->bytes_written - 44);
                avail_data -= avail_data % wav.fmt.nBlockAlign;
                transfer_done = transfer->done;
                transfer_ok = transfer->succeeded;
                if (transfer_done && !transfer_ok) {
                    CloseHandle(file);
                    emit(sid, "error", std::min(duration_ms, dev.position_ms()), duration_ms, true,
                         transfer->error_code.empty() ? "network_error" : transfer->error_code);
                    return;
                }
            }

            while (can_feed() && offset < wav.data_bytes && dev.needs_audio()) {
                int64_t can_read = std::min(block_bytes, std::min(wav.data_bytes, avail_data) - offset);
                can_read -= (can_read % wav.fmt.nBlockAlign);
                if (can_read <= 0) break;

                std::vector<uint8_t> buf(static_cast<size_t>(can_read));
                LARGE_INTEGER pos{};
                pos.QuadPart = 44 + offset;
                DWORD got = 0;
                if (!SetFilePointerEx(file, pos, nullptr, FILE_BEGIN) ||
                    !ReadFile(file, buf.data(), static_cast<DWORD>(can_read), &got, nullptr) ||
                    got < wav.fmt.nBlockAlign) {
                    CloseHandle(file);
                    emit(sid, "error", std::min(duration_ms, dev.position_ms()), duration_ms, true, "playback_failed");
                    return;
                }
                got -= (got % wav.fmt.nBlockAlign);
                buf.resize(got);
                if (!dev.submit(std::move(buf))) {
                    CloseHandle(file);
                    emit(sid, "error", std::min(duration_ms, dev.position_ms()), duration_ms, true, "playback_failed");
                    return;
                }
                offset += got;
            }

            const bool draining = offset >= wav.data_bytes || (transfer_done && offset >= avail_data);
            if (can_feed() && dev.start_if_ready(draining, transfer && offset >= avail_data) && !started) {
                started = true;
                emit(sid, "playing", std::min(duration_ms, dev.position_ms()), duration_ms, true);
                last_report = std::chrono::steady_clock::now();
            }
            if (dev.control_failed()) break;
            if (can_feed() && draining && dev.empty()) {
                CloseHandle(file);
                emit(sid, "ended", duration_ms, duration_ms, false);
                return;
            }

            auto now = std::chrono::steady_clock::now();
            if (started && !paused && now - last_report >= std::chrono::seconds(1)) {
                emit(sid, "playing", std::min(duration_ms, dev.position_ms()), duration_ms, true);
                last_report = now;
            }

            if (!paused && transfer && !transfer_done && !draining && offset >= avail_data && dev.priming()) {
                // While priming a growing file the device is paused. Only the
                // producer or a control change can make more work available.
                std::unique_lock<std::mutex> lock(transfer->mu);
                transfer->cv.wait(lock, [&]() {
                    return !can_feed() || transfer->done ||
                           transfer->bytes_written >= 44 + offset + wav.fmt.nBlockAlign;
                });
            } else {
                WaitForSingleObject(m_wake, INFINITE);
            }
        }
        CloseHandle(file);
        if (dev.control_failed()) {
            emit(sid, "error", dev.position_ms(), duration_ms, true, "playback_failed");
        }
    }

    static HRESULT copy_pcm(IMFSample* sample, std::vector<uint8_t>& pcm) {
        IMFMediaBuffer* buffer = nullptr;
        HRESULT hr = sample->ConvertToContiguousBuffer(&buffer);
        if (FAILED(hr)) return hr;
        BYTE* data = nullptr;
        DWORD length = 0;
        hr = buffer->Lock(&data, nullptr, &length);
        if (SUCCEEDED(hr)) {
            if (length > 0) pcm.assign(data, data + length);
            hr = buffer->Unlock();
        }
        buffer->Release();
        return hr;
    }

    void play_media_foundation(uint64_t sid, const std::wstring& file_path, int64_t fallback_duration_ms) {
        HRESULT hr_co = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
        bool co_inited = SUCCEEDED(hr_co);
        HRESULT hr_mf = MFStartup(MF_VERSION, MFSTARTUP_NOSOCKET);
        if (FAILED(hr_mf)) {
            if (co_inited) CoUninitialize();
            emit(sid, "error", 0, fallback_duration_ms, false, "mf_unavailable");
            return;
        }

        // The synchronous reader also depends on the Media Foundation work queues.
        DWORD task_index = 0;
        const bool queues_registered = SUCCEEDED(MFRegisterPlatformWithMMCSS(L"Audio", &task_index, 0));
        auto cleanup = [&]() {
            if (queues_registered) MFUnregisterPlatformFromMMCSS();
            MFShutdown();
            if (co_inited) CoUninitialize();
        };

        IMFSourceReader* reader = nullptr;
        HRESULT hr = MFCreateSourceReaderFromURL(file_path.c_str(), nullptr, &reader);
        if (FAILED(hr) || !reader) {
            cleanup();
            emit(sid, "error", 0, fallback_duration_ms, false, "playback_failed");
            return;
        }

        reader->SetStreamSelection(static_cast<DWORD>(MF_SOURCE_READER_ALL_STREAMS), FALSE);
        reader->SetStreamSelection(static_cast<DWORD>(MF_SOURCE_READER_FIRST_AUDIO_STREAM), TRUE);

        IMFMediaType* partial = nullptr;
        hr = MFCreateMediaType(&partial);
        if (SUCCEEDED(hr) && partial) {
            partial->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Audio);
            partial->SetGUID(MF_MT_SUBTYPE, MFAudioFormat_PCM);
            partial->SetUINT32(MF_MT_AUDIO_BITS_PER_SAMPLE, 16);
            hr = reader->SetCurrentMediaType(static_cast<DWORD>(MF_SOURCE_READER_FIRST_AUDIO_STREAM), nullptr, partial);
            partial->Release();
        }
        if (FAILED(hr)) {
            reader->Release();
            cleanup();
            emit(sid, "error", 0, fallback_duration_ms, false, "playback_failed");
            return;
        }

        IMFMediaType* actual = nullptr;
        WAVEFORMATEX* wfx = nullptr;
        UINT32 wfx_size = 0;
        hr = reader->GetCurrentMediaType(static_cast<DWORD>(MF_SOURCE_READER_FIRST_AUDIO_STREAM), &actual);
        if (SUCCEEDED(hr) && actual) {
            hr = MFCreateWaveFormatExFromMFMediaType(actual, &wfx, &wfx_size, MFWaveFormatExConvertFlag_Normal);
            actual->Release();
        }
        if (FAILED(hr) || !wfx) {
            if (wfx) CoTaskMemFree(wfx);
            reader->Release();
            cleanup();
            emit(sid, "error", 0, fallback_duration_ms, false, "playback_failed");
            return;
        }

        WAVEFORMATEX fmt = *wfx;
        fmt.cbSize = 0;
        CoTaskMemFree(wfx);

        int64_t duration_ms = fallback_duration_ms;
        PROPVARIANT var{};
        PropVariantInit(&var);
        if (SUCCEEDED(reader->GetPresentationAttribute(static_cast<DWORD>(MF_SOURCE_READER_MEDIASOURCE), MF_PD_DURATION, &var))) {
            if ((var.vt == VT_UI8 || var.vt == VT_I8) && var.hVal.QuadPart > 0) {
                duration_ms = static_cast<int64_t>(var.hVal.QuadPart / 10000ULL);
            }
        }
        PropVariantClear(&var);

        WaveOutDevice dev;
        if (!dev.open(fmt, m_wake, m_volume.load(), true)) {
            reader->Release();
            cleanup();
            emit(sid, "error", 0, duration_ms, false, "audio_device_unavailable");
            return;
        }

        auto seek_reader = [&](int64_t ms) {
            int64_t clamped = duration_ms > 0 ? std::min(duration_ms, ms) : ms;
            PROPVARIANT pv{};
            PropVariantInit(&pv);
            pv.vt = VT_I8;
            pv.hVal.QuadPart = clamped * 10000LL;
            if (SUCCEEDED(reader->SetCurrentPosition(GUID_NULL, pv))) {
                dev.reset_for_seek(clamped);
            }
            PropVariantClear(&pv);
            return clamped;
        };

        int64_t initial_seek = m_seek_ms.exchange(-1);
        if (initial_seek > 0) {
            seek_reader(initial_seek);
        }

        bool eof = false;
        bool was_paused = false;
        bool started = false;
        auto last_report = std::chrono::steady_clock::now();

        for (;;) {
            ResetEvent(m_wake);
            if (m_stop.load() || dev.control_failed()) break;
            dev.set_volume(m_volume.load());

            bool paused = m_paused.load();
            if (paused != was_paused) {
                if (!(paused ? dev.pause() : dev.resume())) break;
                was_paused = paused;
                int64_t cur = duration_ms > 0 ? std::min(duration_ms, dev.position_ms()) : dev.position_ms();
                emit(sid, paused ? "paused" : "playing", cur, duration_ms, true);
                last_report = std::chrono::steady_clock::now();
            }

            int64_t req_seek = m_seek_ms.exchange(-1);
            if (req_seek >= 0) {
                int64_t target = seek_reader(req_seek);
                if (dev.control_failed()) break;
                eof = false;
                emit(sid, paused ? "paused" : "playing", target, duration_ms, true);
                last_report = std::chrono::steady_clock::now();
            }

            while (can_feed() && !eof && dev.needs_audio()) {
                DWORD flags = 0;
                LONGLONG ts = 0;
                IMFSample* sample = nullptr;
                hr = reader->ReadSample(static_cast<DWORD>(MF_SOURCE_READER_FIRST_AUDIO_STREAM), 0, nullptr, &flags, &ts, &sample);
                if (flags & MF_SOURCE_READERF_ENDOFSTREAM) {
                    eof = true;
                }
                if (SUCCEEDED(hr) && sample) {
                    std::vector<uint8_t> pcm;
                    hr = copy_pcm(sample, pcm);
                    if (SUCCEEDED(hr) && !pcm.empty()) {
                        if (!dev.submit(std::move(pcm))) {
                            hr = E_FAIL;
                        }
                    }
                }
                if (sample) sample->Release();
                if (FAILED(hr)) {
                    const int64_t position = dev.position_ms();
                    dev.close();
                    reader->Release();
                    cleanup();
                    emit(sid, "error", position, duration_ms, true, "playback_failed");
                    return;
                }
            }

            if (can_feed() && dev.start_if_ready(eof) && !started) {
                started = true;
                emit(sid, "playing", dev.position_ms(), duration_ms, true);
                last_report = std::chrono::steady_clock::now();
            }
            if (dev.control_failed()) break;
            if (can_feed() && eof && dev.empty()) {
                int64_t end_pos = duration_ms > 0 ? duration_ms : dev.position_ms();
                dev.close();
                reader->Release();
                cleanup();
                emit(sid, "ended", end_pos, end_pos, false);
                return;
            }

            auto now = std::chrono::steady_clock::now();
            if (started && !paused && now - last_report >= std::chrono::seconds(1)) {
                int64_t cur = duration_ms > 0 ? std::min(duration_ms, dev.position_ms()) : dev.position_ms();
                emit(sid, "playing", cur, duration_ms, true);
                last_report = now;
            }

            WaitForSingleObject(m_wake, INFINITE);
        }

        const int64_t error_position = dev.control_failed() ? dev.position_ms() : 0;
        dev.close();
        reader->Release();
        cleanup();
        if (dev.control_failed()) {
            emit(sid, "error", error_position, duration_ms, true, "playback_failed");
        }
    }
};

} // namespace music
