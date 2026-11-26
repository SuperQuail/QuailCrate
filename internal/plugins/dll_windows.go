package plugins

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	v1 "quailcrate/pluginapi/v1"
)

type dllRuntime struct {
	module   windows.Handle
	invoke   uintptr
	free     uintptr
	bindHost uintptr
	hasSink  bool
}

var (
	kernel32DLL       = windows.NewLazySystemDLL("kernel32.dll")
	setDllDirectoryW  = kernel32DLL.NewProc("SetDllDirectoryW")
	addDllDirectoryW  = kernel32DLL.NewProc("AddDllDirectory")
	notifyCallbackMu  sync.Mutex
	notifyCallbackPtr uintptr
	activeNotifySink  atomic.Pointer[func([]byte) int32]
)

func hostNotifyTrampoline(ptr, length uintptr) uintptr {
	if ptr == 0 || length == 0 || length > v1.MaxMessageSize {
		return ^uintptr(0) // -1
	}
	sink := activeNotifySink.Load()
	if sink == nil || *sink == nil {
		return ^uintptr(0)
	}
	data := append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(length))...)
	return uintptr(int64((*sink)(data)))
}

func getHostNotifyCallback() uintptr {
	notifyCallbackMu.Lock()
	defer notifyCallbackMu.Unlock()
	if notifyCallbackPtr == 0 {
		notifyCallbackPtr = syscall.NewCallback(hostNotifyTrampoline)
	}
	return notifyCallbackPtr
}

func configureDLLSearchDir(dir string) {
	if dir == "" {
		return
	}
	ptr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	if setDllDirectoryW.Find() == nil {
		_, _, _ = setDllDirectoryW.Call(uintptr(unsafe.Pointer(ptr)))
	}
	if addDllDirectoryW.Find() == nil {
		_, _, _ = addDllDirectoryW.Call(uintptr(unsafe.Pointer(ptr)))
	}
}

// loadWorkerDLL loads a Windows x64 plugin DLL in-place from its package
// directory, allowing companion DLL dependencies in the same directory.
func loadWorkerDLL(path string) (*dllRuntime, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve DLL path: %w", err)
	}
	image, err := pe.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("read DLL image: %w", err)
	}
	valid := image.Machine == pe.IMAGE_FILE_MACHINE_AMD64 && image.Characteristics&pe.IMAGE_FILE_DLL != 0
	image.Close()
	if !valid {
		return nil, errors.New("plugin entry is not a Windows x64 DLL")
	}
	configureDLLSearchDir(filepath.Dir(absPath))
	const searchFlags = windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS
	module, err := windows.LoadLibraryEx(absPath, 0, searchFlags)
	if err != nil {
		return nil, fmt.Errorf("load DLL: %w", err)
	}
	worker := &dllRuntime{module: module}
	if err := worker.loadExports(); err != nil {
		worker.Close()
		return nil, err
	}
	return worker, nil
}

func (worker *dllRuntime) loadExports() error {
	var version uintptr
	for _, export := range []struct {
		name   string
		target *uintptr
	}{{"quailcrate_abi_version", &version}, {"quailcrate_invoke", &worker.invoke}, {"quailcrate_free", &worker.free}} {
		address, err := windows.GetProcAddress(worker.module, export.name)
		if err != nil {
			return fmt.Errorf("missing DLL export %s: %w", export.name, err)
		}
		*export.target = address
	}
	result, _, _ := syscall.SyscallN(version)
	if uint32(result) != v1.ABIVersion && uint32(result) != v1.ABIVersionV2 {
		return fmt.Errorf("DLL ABI version %d is unsupported", uint32(result))
	}
	if address, err := windows.GetProcAddress(worker.module, "quailcrate_bind_host"); err == nil {
		worker.bindHost = address
	}
	return nil
}

// BindHost attaches an optional thread-safe host notification sink if the DLL
// exports quailcrate_bind_host.
func (worker *dllRuntime) BindHost(notify func([]byte) int32) {
	if worker.bindHost == 0 || notify == nil {
		return
	}
	sink := notify
	activeNotifySink.Store(&sink)
	worker.hasSink = true
	_, _, _ = syscall.SyscallN(worker.bindHost, getHostNotifyCallback())
}

func (worker *dllRuntime) Call(ctx context.Context, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input) == 0 || len(input) > v1.MaxMessageSize {
		return nil, errors.New("invalid DLL request length")
	}
	var response *byte
	var length uint32
	status, _, _ := syscall.SyscallN(worker.invoke,
		uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)),
		uintptr(unsafe.Pointer(&response)), uintptr(unsafe.Pointer(&length)))
	runtime.KeepAlive(input)
	if response != nil {
		defer syscall.SyscallN(worker.free, uintptr(unsafe.Pointer(response)))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int32(status) != 0 {
		return nil, fmt.Errorf("DLL invocation failed with status %d", int32(status))
	}
	if response == nil || length == 0 || length > v1.MaxMessageSize {
		return nil, errors.New("DLL returned an invalid response buffer")
	}
	return append([]byte(nil), unsafe.Slice(response, int(length))...), nil
}

func (worker *dllRuntime) Close() error {
	if worker.hasSink {
		activeNotifySink.Store(nil)
		worker.hasSink = false
	}
	return windows.FreeLibrary(worker.module)
}
