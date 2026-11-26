//go:build !windows

package pluginio

// The desktop host currently supports protected persistent sessions on Windows.
func seal(_, _ []byte) ([]byte, error)   { return nil, errSession }
func unseal(_, _ []byte) ([]byte, error) { return nil, errSession }
