package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type promptReaderFunc func(context.Context, []byte) (int, error)

func (f promptReaderFunc) Read(ctx context.Context, buffer []byte) (int, error) {
	return f(ctx, buffer)
}

func TestPromptCancellationRestoresTerminal(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		name := "confirmation"
		if hidden {
			name = "key"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiting := make(chan struct{})
			restored := false
			reads := 0
			input := promptReaderFunc(func(ctx context.Context, buffer []byte) (int, error) {
				reads++
				if reads == 1 {
					return copy(buffer, "partial-input"), nil
				}
				close(waiting)
				<-ctx.Done()
				return 0, ctx.Err()
			})
			var output bytes.Buffer
			done := make(chan error, 1)
			go func() {
				answer, err := readPrompt(ctx, hidden, &output, func() (promptReader, func() error, error) {
					return input, func() error { restored = true; return nil }, nil
				})
				if len(answer) != 0 {
					done <- errors.New("cancelled prompt returned partial input")
					return
				}
				done <- err
			}()
			select {
			case <-waiting:
			case <-time.After(time.Second):
				t.Fatal("prompt never started reading")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("prompt remained blocked after cancellation")
			}
			if !restored {
				t.Fatal("prompt returned before restoring terminal modes")
			}
			if hidden && output.Len() != 0 {
				t.Fatalf("key input was echoed: %q", output.String())
			}
		})
	}
}

func TestPromptCtrlCAndEditing(t *testing.T) {
	cases := []struct {
		name, input, want string
		wantErr           error
	}{
		{name: "enter", input: "yes\r", want: "yes"},
		{name: "newline", input: "yes\n", want: "yes"},
		{name: "backspace", input: "abc\bde\r", want: "abde"},
		{name: "unicode backspace", input: "a🙂\x7fb\n", want: "ab"},
		{name: "clear line", input: "discard\x15new\n", want: "new"},
		{name: "ctrl c", input: "partial\x03", wantErr: context.Canceled},
		{name: "ctrl d", input: "partial\x04", wantErr: io.EOF},
		{name: "eof", input: "partial", wantErr: io.EOF},
		{name: "limit", input: strings.Repeat("a", maxPromptBytes+1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, hidden := range []bool{false, true} {
				reader := strings.NewReader(test.input)
				input := promptReaderFunc(func(_ context.Context, buffer []byte) (int, error) {
					return reader.Read(buffer)
				})
				restores := 0
				var output bytes.Buffer
				answer, err := readPrompt(context.Background(), hidden, &output, func() (promptReader, func() error, error) {
					return input, func() error { restores++; return nil }, nil
				})
				if test.name == "limit" {
					if err == nil {
						t.Fatal("accepted unlimited prompt input")
					}
				} else if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				if string(answer) != test.want {
					t.Fatalf("answer = %q, want %q", answer, test.want)
				}
				if restores != 1 {
					t.Fatalf("terminal restored %d times", restores)
				}
				if hidden && output.Len() != 0 {
					t.Fatalf("key input was echoed: %q", output.String())
				}
			}
		})
	}
}

func TestAlreadyCancelledPromptDoesNotChangeTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readPrompt(ctx, true, io.Discard, func() (promptReader, func() error, error) {
		t.Fatal("opened terminal after cancellation")
		return nil, nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCancellationDuringTerminalSetupRestoresModes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restored := false
	input := promptReaderFunc(func(context.Context, []byte) (int, error) {
		t.Fatal("read after cancellation during terminal setup")
		return 0, nil
	})
	_, err := readPrompt(ctx, true, io.Discard, func() (promptReader, func() error, error) {
		cancel()
		return input, func() error { restored = true; return nil }, nil
	})
	if !errors.Is(err, context.Canceled) || !restored {
		t.Fatalf("setup cancellation did not restore modes: restored=%v err=%v", restored, err)
	}
}

func TestPromptEchoFailureRestoresTerminal(t *testing.T) {
	want := errors.New("output failed")
	reader := strings.NewReader("yes\n")
	input := promptReaderFunc(func(_ context.Context, buffer []byte) (int, error) { return reader.Read(buffer) })
	restored := false
	answer, err := readPrompt(context.Background(), false, failingPromptWriter{err: want}, func() (promptReader, func() error, error) {
		return input, func() error { restored = true; return nil }, nil
	})
	if !errors.Is(err, want) || !restored || len(answer) != 0 {
		t.Fatalf("echo failure did not restore modes: answer=%q restored=%v err=%v", answer, restored, err)
	}
}

type failingPromptWriter struct {
	err error
}

func (w failingPromptWriter) Write([]byte) (int, error) { return 0, w.err }

func TestPromptRestoreFailureDiscardsAnswer(t *testing.T) {
	want := errors.New("restore failed")
	reader := strings.NewReader("secret\n")
	input := promptReaderFunc(func(_ context.Context, buffer []byte) (int, error) { return reader.Read(buffer) })
	answer, err := readPrompt(context.Background(), true, io.Discard, func() (promptReader, func() error, error) {
		return input, func() error { return want }, nil
	})
	if !errors.Is(err, want) || len(answer) != 0 {
		t.Fatalf("restore failure accepted input: %q, %v", answer, err)
	}
}

func TestCancelledKeySetupPreservesExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth")
	if err := os.WriteFile(path, []byte("existing-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	ui := &console{out: &output, err: &output, interactive: true}
	if err := setupKey(ctx, dir, ui); !errors.Is(err, context.Canceled) {
		t.Fatalf("key cancellation: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing-key\n" || output.Len() != 0 {
		t.Fatalf("cancelled setup changed state: %q, %v, output=%q", data, err, output.String())
	}
}

func TestCancelledApprovalAddsNoTrust(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	guard, err := newPrivacy(nil, &console{out: &output, err: &output, interactive: true}, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard.authorize(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("approval cancellation: %v", err)
	}
	if len(guard.temporary) != 0 || output.Len() != 0 {
		t.Fatal("cancelled approval prompted or granted trust")
	}
}
