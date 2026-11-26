package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeProcess struct {
	job       windows.Handle
	process   windows.Handle
	write     *os.File
	read      *os.File
	done      chan struct{}
	readDone  chan struct{}
	responses chan []byte
	notifyCh  chan []byte
	readMu    sync.Mutex
	readErr   error
	once      sync.Once
	closeErr  error
}

// The release builder pins the companion it places next to QuailCrate.exe.
var nativeRunnerSHA256 string

var nativeExecutable = func() (string, error) {
	if nativeRunnerSHA256 == "" {
		if exe := os.Getenv("QUAILCRATE_NATIVE_RUNNER"); exe != "" {
			return exe, nil
		}
	}
	host, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(host), nativeRunner), nil
}

func openDLL(ctx context.Context, pkg Package) (Runtime, error) {
	if pkg.runner != nil {
		return openNativeProcess(ctx, pkg, filepath.Join(pkg.runner.dir, nativeRunner))
	}
	exe, err := nativeExecutable()
	if err != nil {
		return nil, err
	}
	return openNativeProcess(ctx, pkg, exe)
}

func openNativeProcess(ctx context.Context, pkg Package, exe string) (result *nativeProcess, err error) {
	if runtime.GOARCH != "amd64" {
		return nil, errors.New("native plugins require Windows x64")
	}
	if pkg.Bundled && pkg.dllHash == "" {
		return nil, errors.New("missing bundled DLL checksum")
	}
	entryPath := pkg.sourceBuiltEntry
	if entryPath == "" {
		entryPath, err = resolveFile(pkg.Dir, pkg.Manifest.Entry)
		if err != nil {
			return nil, fmt.Errorf("resolve native plugin entry: %w", err)
		}
	}
	if pkg.Bundled {
		digest, hashErr := hashFileSHA256(entryPath)
		if hashErr != nil {
			return nil, fmt.Errorf("hash bundled native plugin: %w", hashErr)
		}
		if digest != pkg.dllHash {
			return nil, errors.New("DLL checksum does not match the release manifest")
		}
	}
	expectedRunner := nativeRunnerSHA256
	if pkg.runner != nil {
		expectedRunner = pkg.runner.runnerHash
	}
	if expectedRunner != "" {
		digest, hashErr := hashFileSHA256(exe)
		if hashErr != nil {
			return nil, fmt.Errorf("hash native worker: %w", hashErr)
		}
		if digest != expectedRunner {
			return nil, errors.New("native worker checksum does not match the release manifest")
		}
	}
	if pkg.DataDir != "" {
		if err := os.MkdirAll(pkg.DataDir, 0700); err != nil {
			return nil, fmt.Errorf("prepare plugin data directory: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	worker := &nativeProcess{
		done:      make(chan struct{}),
		responses: make(chan []byte, 1),
		notifyCh:  make(chan []byte, 64),
	}
	defer func() {
		if err != nil {
			worker.Close()
		}
	}()
	if err := worker.launch(exe, entryPath, pkg); err != nil {
		return nil, fmt.Errorf("start native worker: %w", err)
	}
	ready, err := worker.exchange(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("initialize native worker: %w", err)
	}
	if !bytes.Equal(ready, []byte("ready")) {
		return nil, fmt.Errorf("native worker rejected module: %.512s", ready)
	}
	return worker, nil
}

func buildWorkerEnv(pkg Package) []uint16 {
	overrides := map[string]string{
		"QUAILCRATE_PLUGIN_ID":  pkg.Manifest.ID,
		"QUAILCRATE_PLUGIN_DIR": pkg.Dir,
	}
	if pkg.DataDir != "" {
		overrides["QUAILCRATE_PLUGIN_DATA_DIR"] = pkg.DataDir
	}
	seen := make(map[string]bool, len(overrides))
	var block []uint16
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		upper := strings.ToUpper(key)
		if replacement, replaced := overrides[upper]; replaced {
			if !seen[upper] {
				block = append(block, syscall.StringToUTF16(upper+"="+replacement)...)
				seen[upper] = true
			}
			continue
		}
		block = append(block, syscall.StringToUTF16(entry)...)
	}
	for k, v := range overrides {
		if !seen[k] {
			block = append(block, syscall.StringToUTF16(k+"="+v)...)
		}
	}
	block = append(block, 0)
	return block
}

// launch starts an isolated worker process with the current user's standard
// desktop permissions and a kill-on-close Job Object for clean teardown.
func (p *nativeProcess) launch(exe, entryPath string, pkg Package) error {
	childIn, parentWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer childIn.Close()
	p.write = parentWrite
	parentRead, childOut, err := os.Pipe()
	if err != nil {
		return err
	}
	defer childOut.Close()
	p.read = parentRead
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer discard.Close()
	p.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var limit windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limit.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(p.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limit)), uint32(unsafe.Sizeof(limit))); err != nil {
		return err
	}
	list, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return err
	}
	defer list.Delete()
	const jobList = 0x0002000d
	if err := list.Update(jobList, unsafe.Pointer(&p.job), unsafe.Sizeof(p.job)); err != nil {
		return err
	}
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	var handles [3]windows.Handle
	for i, file := range []*os.File{childIn, childOut, discard} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(),
			&handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return err
		}
		defer windows.CloseHandle(handles[i])
	}
	if err := list.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return err
	}
	start := windows.StartupInfoEx{ProcThreadAttributeList: list.List()}
	start.Cb = uint32(unsafe.Sizeof(start))
	start.Flags = windows.STARTF_USESTDHANDLES
	start.StdInput, start.StdOutput, start.StdErr = handles[0], handles[1], handles[2]
	path, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cwd, err := windows.UTF16PtrFromString(pkg.Dir)
	if err != nil {
		return err
	}
	cmd, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{exe, NativeWorkerArgument, entryPath}))
	if err != nil {
		return err
	}
	env := buildWorkerEnv(pkg)
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(path, cmd, nil, nil, true, flags, &env[0], cwd, &start.StartupInfo, &pi); err != nil {
		return fmt.Errorf("CreateProcess: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	p.process = pi.Process
	p.readDone = make(chan struct{})
	go func() {
		windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		close(p.done)
	}()
	go p.readLoop()
	return nil
}

func (p *nativeProcess) readLoop() {
	defer func() {
		close(p.notifyCh)
		close(p.readDone)
	}()
	for {
		kind, data, err := readNativeFrame(p.read)
		if err != nil {
			p.readMu.Lock()
			p.readErr = err
			p.readMu.Unlock()
			return
		}
		switch kind {
		case nativeFrameResponse:
			select {
			case p.responses <- data:
			default:
				p.readMu.Lock()
				p.readErr = errors.New("unexpected native worker response frame")
				p.readMu.Unlock()
				return
			}
		case nativeFrameNotify:
			select {
			case p.notifyCh <- data:
			default:
				// Drop oldest overflow safely rather than deadlocking the pipe reader.
			}
		default:
			p.readMu.Lock()
			p.readErr = fmt.Errorf("unexpected native frame kind %d", kind)
			p.readMu.Unlock()
			return
		}
	}
}

func (p *nativeProcess) Done() <-chan struct{}          { return p.done }
func (p *nativeProcess) Notifications() <-chan []byte   { return p.notifyCh }

func (p *nativeProcess) Call(ctx context.Context, input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("empty DLL request")
	}
	return p.exchange(ctx, input)
}

// The plugin manager serializes calls. Terminating the job also unblocks either pipe.
func (p *nativeProcess) exchange(ctx context.Context, input []byte) ([]byte, error) {
	if input != nil {
		writeErr := make(chan error, 1)
		go func() {
			writeErr <- writeNativeFrame(p.write, nativeFrameRequest, input)
		}()
		select {
		case <-ctx.Done():
			p.Close()
			<-writeErr
			return nil, ctx.Err()
		case err := <-writeErr:
			if err != nil {
				p.Close()
				return nil, err
			}
		}
	}
	select {
	case <-ctx.Done():
		p.Close()
		if p.readDone != nil {
			<-p.readDone
		}
		return nil, ctx.Err()
	case data := <-p.responses:
		return data, nil
	case <-p.readDone:
		select {
		case data := <-p.responses:
			return data, nil
		default:
		}
		p.readMu.Lock()
		err := p.readErr
		p.readMu.Unlock()
		if err == nil {
			err = errors.New("native worker pipe closed")
		}
		p.Close()
		return nil, err
	}
}

func (p *nativeProcess) Close() error {
	p.once.Do(func() {
		if p.job != 0 {
			windows.CloseHandle(p.job)
		}
		if p.write != nil {
			p.write.Close()
		}
		if p.process != 0 {
			<-p.done
			windows.CloseHandle(p.process)
		}
		if p.read != nil {
			p.read.Close()
		}
		if p.readDone != nil {
			<-p.readDone
		}
	})
	return p.closeErr
}
