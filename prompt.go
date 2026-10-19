package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/term"
)

const maxPromptBytes = 4096

type promptReader interface {
	Read(context.Context, []byte) (int, error)
}

type openPromptFunc func() (promptReader, func() error, error)

func (ui *console) readPrompt(ctx context.Context, hidden bool) ([]byte, error) {
	return readPrompt(ctx, hidden, ui.err, func() (promptReader, func() error, error) {
		return openPromptTerminal(ui.in)
	})
}

func openPromptTerminal(in *os.File) (promptReader, func() error, error) {
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, nil, err
	}
	// Set terminal modes in the caller, not in a background reader. Restoration
	// is synchronous, so a cancelled read cannot later disable echo again.
	return newTerminalInput(in), func() error { return term.Restore(fd, state) }, nil
}

func readPrompt(ctx context.Context, hidden bool, out io.Writer, open openPromptFunc) (answer []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, restore, err := open()
	if err != nil {
		return nil, fmt.Errorf("prepare terminal: %w", err)
	}
	defer func() {
		if restoreErr := restore(); restoreErr != nil {
			clear(answer)
			answer = nil
			err = errors.Join(err, fmt.Errorf("restore terminal: %w", restoreErr))
		}
	}()
	return readPromptLine(ctx, input, out, hidden)
}

func readPromptLine(ctx context.Context, input promptReader, out io.Writer, hidden bool) ([]byte, error) {
	var line []byte
	var buffer [128]byte
	accepted := false
	defer func() {
		clear(buffer[:])
		if !accepted {
			clear(line)
		}
	}()
	erase := func() error {
		if len(line) == 0 {
			return nil
		}
		_, size := utf8.DecodeLastRune(line)
		clear(line[len(line)-size:])
		line = line[:len(line)-size]
		if !hidden {
			_, err := io.WriteString(out, "\b \b")
			return err
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := input.Read(ctx, buffer[:])
		for _, b := range buffer[:n] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch b {
			case 3: // Raw mode delivers Ctrl-C as input, not as an OS signal.
				return nil, context.Canceled
			case 4: // Ctrl-D declines rather than accepting a partial answer.
				return nil, io.EOF
			case '\r', '\n':
				accepted = true
				return line, nil
			case '\b', 127:
				if err := erase(); err != nil {
					return nil, err
				}
			case 21: // Ctrl-U clears the current line.
				for len(line) > 0 {
					if err := erase(); err != nil {
						return nil, err
					}
				}
			default:
				if b < 32 {
					continue
				}
				if len(line) >= maxPromptBytes {
					return nil, fmt.Errorf("prompt input exceeds %d bytes", maxPromptBytes)
				}
				line = append(line, b)
				if !hidden {
					if _, err := io.WriteString(out, safeText(string([]byte{b}), false)); err != nil {
						return nil, err
					}
				}
			}
		}
		if readErr != nil {
			return nil, readErr
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
}
