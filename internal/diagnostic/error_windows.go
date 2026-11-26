package diagnostic

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func English(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	var code syscall.Errno
	if !errors.As(err, &code) {
		return message
	}
	var buffer [1024]uint16
	const fromSystem = 0x1000
	const ignoreInserts = 0x200
	const englishUS = 0x0409
	n, formatErr := windows.FormatMessage(fromSystem|ignoreInserts, 0, uint32(code), englishUS, buffer[:], nil)
	replacement := fmt.Sprintf("Windows error %d", uint32(code))
	if formatErr == nil && n > 0 {
		replacement = strings.TrimSpace(windows.UTF16ToString(buffer[:n]))
	}
	return strings.ReplaceAll(message, code.Error(), replacement)
}
