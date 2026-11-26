#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <mmsystem.h>

#include <chrono>
#include <condition_variable>
#include <iostream>
#include <mutex>

using namespace std::chrono_literals;

static std::mutex state_mu;
static std::condition_variable changed;
static size_t played_bytes = 0;

// Count PCM buffers actually completed by the device, including while the host
// is busy processing the first "playing" notification.
static MMRESULT observe_unprepare(HWAVEOUT device, WAVEHDR* header, UINT size) {
    const bool completed = (header->dwFlags & WHDR_DONE) != 0;
    const DWORD bytes = header->dwBufferLength;
    const MMRESULT result = waveOutUnprepareHeader(device, header, size);
    if (completed && result == MMSYSERR_NOERROR) {
        std::lock_guard<std::mutex> lock(state_mu);
        played_bytes += bytes;
        changed.notify_all();
    }
    return result;
}

#define waveOutUnprepareHeader observe_unprepare
#include "audio_player.hpp"
#undef waveOutUnprepareHeader

int wmain(int argc, wchar_t** argv) {
    if (argc != 2) return 1;
    if (waveOutGetNumDevs() == 0) return 77;

    // The checked-in AAC fixture contains 320 ms of 48 kHz stereo silence.
    constexpr size_t required_bytes = 48000 * 2 * sizeof(int16_t) / 4; // 250 ms
    bool callback_seen = false;
    bool drained_while_blocked = false;
    bool finished = false;
    std::string error;
    music::NativeAudioPlayer player([&](const music::AudioProgress& progress) {
        std::unique_lock<std::mutex> lock(state_mu);
        if (progress.status == "playing" && !callback_seen) {
            callback_seen = true;
            drained_while_blocked = changed.wait_for(lock, 3s, [&] { return played_bytes >= required_bytes; });
        }
        if (progress.status == "error" || progress.status == "ended") {
            finished = true;
            error = progress.error_code;
            changed.notify_all();
        }
    });
    player.play_file(argv[1], 320, 0);
    {
        std::unique_lock<std::mutex> lock(state_mu);
        changed.wait_for(lock, 8s, [&] { return finished; });
    }
    player.stop();
    if (!finished || !error.empty() || !drained_while_blocked) {
        std::cerr << "AAC playback: ended=" << finished << " error=" << error
                  << " completed bytes=" << played_bytes << " continued during callback=" << drained_while_blocked << '\n';
        return 1;
    }
    std::cout << "AAC playback completed " << played_bytes << " PCM bytes, including at least "
              << required_bytes << " bytes before the first progress callback returned.\n";
    return 0;
}
