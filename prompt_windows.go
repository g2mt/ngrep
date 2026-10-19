package main

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

var readConsoleInputW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReadConsoleInputW")

// INPUT_RECORD's event union begins at offset 4; KEY_EVENT_RECORD occupies
// 16 bytes. Reading events (rather than ReadFile) also consumes resize/mouse
// events, which can signal the console handle without providing text.
type consoleInputRecord struct {
	eventType       uint16
	padding         uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	character       uint16
	controlKeyState uint32
}

type terminalInput struct {
	file    *os.File
	pending []byte
	high    uint16
}

func newTerminalInput(file *os.File) promptReader {
	return &terminalInput{file: file}
}

func (input *terminalInput) Read(ctx context.Context, buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			clear(input.pending)
			input.pending = nil
			return 0, err
		}
		if len(input.pending) > 0 {
			n := copy(buffer, input.pending)
			clear(input.pending[:n])
			input.pending = input.pending[n:]
			return n, nil
		}
		status, err := syscall.WaitForSingleObject(syscall.Handle(input.file.Fd()), 50)
		if err != nil {
			return 0, err
		}
		if status == uint32(syscall.WAIT_TIMEOUT) {
			continue
		}
		if status != uint32(syscall.WAIT_OBJECT_0) {
			return 0, errors.New("unexpected terminal wait status")
		}
		var record consoleInputRecord
		var count uint32
		ok, _, callErr := readConsoleInputW.Call(input.file.Fd(), uintptr(unsafe.Pointer(&record)), 1, uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			if callErr == syscall.Errno(0) {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, callErr
		}
		if count == 0 || record.eventType != 1 || record.keyDown == 0 || record.character == 0 {
			continue
		}
		for i := uint16(0); i < record.repeatCount; i++ {
			c := record.character
			if c >= 0xd800 && c <= 0xdbff {
				if input.high != 0 {
					input.pending = utf8.AppendRune(input.pending, utf8.RuneError)
				}
				input.high = c
				continue
			}
			if input.high != 0 {
				r := utf16.DecodeRune(rune(input.high), rune(c))
				input.pending = utf8.AppendRune(input.pending, r)
				input.high = 0
				if c >= 0xdc00 && c <= 0xdfff {
					continue
				}
			}
			if c >= 0xdc00 && c <= 0xdfff {
				input.pending = utf8.AppendRune(input.pending, utf8.RuneError)
			} else {
				input.pending = utf8.AppendRune(input.pending, rune(c))
			}
		}
	}
}
