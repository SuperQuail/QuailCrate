// Package testaudio checks the hardware prerequisites of audio integration tests.
package testaudio

import "golang.org/x/sys/windows"

var waveOutGetNumDevs = windows.NewLazySystemDLL("winmm.dll").NewProc("waveOutGetNumDevs")

// HasOutput reports whether WinMM enumerates a playback device on this machine.
func HasOutput() bool {
	count, _, _ := waveOutGetNumDevs.Call()
	return count != 0
}
