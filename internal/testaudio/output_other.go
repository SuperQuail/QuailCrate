//go:build !windows

package testaudio

// HasOutput is false on platforms without WinMM.
func HasOutput() bool { return false }
