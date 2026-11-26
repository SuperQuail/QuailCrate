//go:build !windows

package plugins

import (
	"context"
	"errors"
)

func openDLL(context.Context, Package) (Runtime, error) {
	return nil, errors.New("native plugins are supported on Windows only")
}
