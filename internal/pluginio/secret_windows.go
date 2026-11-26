//go:build windows

package pluginio

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func seal(data, entropy []byte) ([]byte, error)   { return crypt(data, entropy, true) }
func unseal(data, entropy []byte) ([]byte, error) { return crypt(data, entropy, false) }

func crypt(data, entropy []byte, encode bool) ([]byte, error) {
	if len(data) == 0 || len(entropy) == 0 {
		return nil, errSession
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	scope := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var output windows.DataBlob
	var err error
	if encode {
		err = windows.CryptProtectData(&input, nil, &scope, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptUnprotectData(&input, nil, &scope, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}
