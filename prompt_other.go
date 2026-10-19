//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !illumos && !windows

package main

import (
	"context"
	"errors"
	"os"
)

type unsupportedTerminalInput struct{}

func newTerminalInput(*os.File) promptReader {
	return unsupportedTerminalInput{}
}

func (unsupportedTerminalInput) Read(context.Context, []byte) (int, error) {
	return 0, errors.New("interactive prompts are supported on Unix and Windows")
}
