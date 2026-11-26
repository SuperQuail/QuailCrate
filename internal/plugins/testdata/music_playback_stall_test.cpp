#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <mmsystem.h>

#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <iostream>
#include <mutex>
#include <thread>

using namespace std::chrono_literals;

// makeTestWAV produces 16 kHz mono, 16-bit PCM. Only the player thread
// touches this state until stop() joins it.
static constexpr DWORD samples_per_ms = 16;
static constexpr DWORD bytes_per_ms = samples_per_ms * sizeof(int16_t);
static WAVEHDR* last_submitted = nullptr;
static size_t played_bytes = 0;
static int64_t device_advance_ms = -1;
static bool stalled = false;
static bool tail_pending = false;
static bool force_software_volume = false;
static bool fail_restart = false;
static bool restart_attempted = false;

static MMRESULT test_restart(HWAVEOUT device) {
    restart_attempted = true;
    return fail_restart ? MMSYSERR_NODRIVER : waveOutRestart(device);
}

[[maybe_unused]] static MMRESULT test_volume(HWAVEOUT device, DWORD volume) {
    return force_software_volume ? MMSYSERR_NOTSUPPORTED : waveOutSetVolume(device, volume);
}

static MMRESULT observe_write(HWAVEOUT device, WAVEHDR* header, UINT size) {
    const MMRESULT result = waveOutWrite(device, header, size);
    if (result == MMSYSERR_NOERROR) last_submitted = header;
    return result;
}

static int64_t device_position_ms(HWAVEOUT device) {
    MMTIME position{};
    position.wType = TIME_SAMPLES;
    if (waveOutGetPosition(device, &position, sizeof(position)) != MMSYSERR_NOERROR) return -1;
    switch (position.wType) {
    case TIME_SAMPLES: return position.u.sample / samples_per_ms;
    case TIME_BYTES: return position.u.cb / bytes_per_ms;
    case TIME_MS: return position.u.ms;
    default: return -1;
    }
}

static MMRESULT observe_unprepare(HWAVEOUT device, WAVEHDR* header, UINT size) {
    if (last_submitted && (header->dwFlags & WHDR_DONE)) {
        played_bytes += header->dwBufferLength;
        if (!fail_restart && !stalled && played_bytes >= 600 * bytes_per_ms) {
            stalled = true;
            const int64_t before = device_position_ms(device);
            // No headers can be submitted during this stall. An outstanding tail
            // when it ends proves the device never exhausted the queue.
            std::this_thread::sleep_for(350ms);
            const int64_t after = device_position_ms(device);
            if (before >= 0 && after >= before) device_advance_ms = after - before;
            tail_pending = !(last_submitted->dwFlags & WHDR_DONE);
        }
    }
    if (header == last_submitted) last_submitted = nullptr;
    return waveOutUnprepareHeader(device, header, size);
}

#define waveOutWrite observe_write
#define waveOutUnprepareHeader observe_unprepare
#define waveOutSetVolume test_volume
#define waveOutRestart test_restart
#include "audio_player.hpp"
#undef waveOutRestart
#undef waveOutSetVolume
#undef waveOutUnprepareHeader
#undef waveOutWrite

int wmain(int argc, wchar_t** argv) {
    if (argc != 3) return 1;
    force_software_volume = std::wstring(argv[2]) == L"software";
    fail_restart = std::wstring(argv[2]) == L"fail-restart";
    if (waveOutGetNumDevs() == 0) return 77;

    std::mutex mu;
    std::condition_variable changed;
    bool ended = false;
    bool playing = false;
    std::string error;
    music::NativeAudioPlayer player([&](const music::AudioProgress& progress) {
        std::lock_guard<std::mutex> lock(mu);
        if (progress.status == "playing") playing = true;
        if (progress.status == "ended") ended = true;
        if (progress.status == "error") error = progress.error_code;
        changed.notify_all();
    });
    player.set_volume(0.5);
    player.play_file(argv[1], 3000, 0);
    {
        std::unique_lock<std::mutex> lock(mu);
        changed.wait_for(lock, 10s, [&] { return ended || !error.empty(); });
    }
    player.stop();
    if (!restart_attempted && error == "audio_device_unavailable") return 77;
    if (fail_restart) {
        std::cout << "Restart failed: attempted=" << restart_attempted << ", playing=" << playing
                  << ", ended=" << ended << ", error=" << error << '\n';
        return restart_attempted && !playing && !ended && error == "playback_failed" ? 0 : 1;
    }
    std::cout << "350 ms refill stall: device advanced " << device_advance_ms
              << " ms, audio still queued=" << tail_pending
              << ", ended=" << ended << ", error=" << error << '\n';
    // Allow device-clock reporting jitter while requiring at least 300 ms
    // of rendered PCM, independent of how samples are grouped into headers.
    return ended && error.empty() && stalled && tail_pending && device_advance_ms >= 300 ? 0 : 1;
}
