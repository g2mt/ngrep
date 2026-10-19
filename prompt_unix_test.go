//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || illumos

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestNativePromptReadCancellationDoesNotLeaveReader(t *testing.T) {
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer out.Close()
	input := newTerminalInput(in)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var buffer [128]byte
		close(started)
		_, err := input.Read(ctx, buffer[:])
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("native terminal read did not observe cancellation")
	}

	// The cancelled read must not remain in the background and steal input.
	if _, err := out.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	var buffer [128]byte
	n, err := input.Read(nextCtx, buffer[:])
	if err != nil || string(buffer[:n]) != "next" {
		t.Fatalf("stdin unusable after cancellation: %q, %v", buffer[:n], err)
	}
}
