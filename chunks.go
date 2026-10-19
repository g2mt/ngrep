package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"unicode"
	"unicode/utf8"
)

// chunker retains at most one chunk plus a bounded look-ahead. Byte offsets
// always refer to the original file, including malformed UTF-8 bytes.
type chunker struct {
	r       *bufio.Reader
	pending []byte
	target  int
	eof     bool
}

func newChunker(r io.Reader, target int) *chunker {
	return &chunker{r: bufio.NewReader(r), target: target}
}

func (c *chunker) next() ([]byte, error) {
	lookahead := min(256, max(1, c.target/4))
	count := utf8.RuneCount(c.pending)
	for !c.eof && count < c.target+lookahead {
		r, size, err := c.r.ReadRune()
		if err == io.EOF {
			c.eof = true
			break
		}
		if err != nil {
			return nil, err
		}
		if r == utf8.RuneError && size == 1 {
			if err := c.r.UnreadRune(); err != nil {
				return nil, err
			}
			b, err := c.r.ReadByte()
			if err != nil {
				return nil, err
			}
			c.pending = append(c.pending, b)
		} else {
			c.pending = utf8.AppendRune(c.pending, r)
		}
		count++
	}
	if len(c.pending) == 0 {
		return nil, io.EOF
	}
	end := len(c.pending)
	if count > c.target {
		end = boundary(c.pending, c.target)
	}
	result := bytes.Clone(c.pending[:end])
	c.pending = bytes.Clone(c.pending[end:])
	return result, nil
}

// Prefer the closest whitespace boundary within 25% (at most 256 characters).
// Unbroken strings are cut at a UTF-8 boundary rather than growing unbounded.
func boundary(data []byte, target int) int {
	window := min(256, max(1, target/4))
	best, distance, hard, count := 0, window+1, 0, 0
	for pos := 0; pos < len(data); {
		r, size := utf8.DecodeRune(data[pos:])
		pos += size
		count++
		if count == target {
			hard = pos
		}
		if unicode.IsSpace(r) && abs(count-target) < distance {
			best, distance = pos, abs(count-target)
		}
		if count > target+window {
			break
		}
	}
	if best > 0 && distance <= window {
		return best
	}
	if hard > 0 {
		return hard
	}
	return len(data)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func containsNUL(ctx context.Context, r io.Reader) (bool, error) {
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := r.Read(buf)
		if bytes.IndexByte(buf[:n], 0) >= 0 {
			return true, nil
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

func isTooLong(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.tooLong()
}
