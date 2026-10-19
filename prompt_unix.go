//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || illumos

package main

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type terminalInput struct {
	file *os.File
}

func newTerminalInput(file *os.File) promptReader {
	return &terminalInput{file: file}
}

func (input *terminalInput) Read(ctx context.Context, buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	fds := []unix.PollFd{{Fd: int32(input.file.Fd()), Events: unix.POLLIN}}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		// Raw mode makes each byte readable immediately. Poll bounds the wait
		// for external cancellation without closing stdin or leaking a reader.
		n, err := unix.Poll(fds, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return 0, os.ErrInvalid
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return input.file.Read(buffer)
		}
	}
}
