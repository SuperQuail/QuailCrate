package plugins

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	v1 "quailcrate/pluginapi/v1"
)

const (
	NativeWorkerArgument = "--quailcrate-native-worker"
	nativeEntryEnv       = "QUAILCRATE_NATIVE_ENTRY"

	nativeFrameRequest  byte = 1
	nativeFrameResponse byte = 2
	nativeFrameNotify   byte = 3
)

func readNativeFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	kind := header[0]
	if kind != nativeFrameRequest && kind != nativeFrameResponse && kind != nativeFrameNotify {
		return 0, nil, fmt.Errorf("invalid native frame kind %d", kind)
	}
	size := binary.LittleEndian.Uint32(header[1:])
	if size == 0 || size > v1.MaxMessageSize {
		return 0, nil, fmt.Errorf("invalid native frame length %d", size)
	}
	frame := make([]byte, size)
	_, err := io.ReadFull(r, frame)
	return kind, frame, err
}

func writeNativeFrame(w io.Writer, kind byte, message []byte) error {
	if kind != nativeFrameRequest && kind != nativeFrameResponse && kind != nativeFrameNotify {
		return fmt.Errorf("invalid native frame kind %d", kind)
	}
	if len(message) == 0 || len(message) > v1.MaxMessageSize {
		return errors.New("invalid native frame length")
	}
	var header [5]byte
	header[0] = kind
	binary.LittleEndian.PutUint32(header[1:], uint32(len(message)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(message)
	return err
}

func resolveWorkerEntryPath() string {
	if len(os.Args) >= 3 && os.Args[1] == NativeWorkerArgument {
		return os.Args[2]
	}
	return os.Getenv(nativeEntryEnv)
}

// RunNativeWorker serves one private pipe pair for the lifetime of one native DLL module.
// The worker runs with the current user's standard desktop permissions while keeping
// native crashes and file locks isolated from the host process.
func RunNativeWorker() error {
	entryPath := resolveWorkerEntryPath()
	if entryPath == "" {
		return errors.New("missing native plugin entry path")
	}
	var stdoutMu sync.Mutex
	var closing atomic.Bool
	writeOut := func(kind byte, payload []byte) error {
		stdoutMu.Lock()
		defer stdoutMu.Unlock()
		return writeNativeFrame(os.Stdout, kind, payload)
	}

	dll, err := loadWorkerDLL(entryPath)
	if err != nil {
		_ = writeOut(nativeFrameResponse, []byte(err.Error()))
		return err
	}
	defer func() {
		closing.Store(true)
		_ = dll.Close()
	}()
	dll.BindHost(func(msg []byte) int32 {
		if closing.Load() || len(msg) == 0 || len(msg) > v1.MaxMessageSize {
			return -1
		}
		if err := writeOut(nativeFrameNotify, msg); err != nil {
			return -1
		}
		return 0
	})
	if err := writeOut(nativeFrameResponse, []byte("ready")); err != nil {
		return err
	}
	for {
		kind, input, err := readNativeFrame(os.Stdin)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if kind != nativeFrameRequest {
			return fmt.Errorf("unexpected frame kind %d on worker stdin", kind)
		}
		output, err := dll.Call(context.Background(), input)
		if err != nil {
			return err
		}
		if err := writeOut(nativeFrameResponse, output); err != nil {
			return err
		}
	}
}
