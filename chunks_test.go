package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkerPreservesOriginalBytes(t *testing.T) {
	cases := [][]byte{
		[]byte("one two three four five\nsix seven eight"),
		[]byte("世界こんにちは emoji🙂 repeated words"),
		[]byte(strings.Repeat("unbroken", 100)),
		{'a', 0xff, 'b', 0xc0, 0xaf, ' ', 'c', 0},
		{},
	}
	for _, input := range cases {
		for _, target := range []int{1, 2, 7, 4000} {
			c := newChunker(bytes.NewReader(input), target)
			var got []byte
			for {
				piece, err := c.next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(piece) == 0 {
					t.Fatal("empty chunk")
				}
				if utf8.Valid(input) && !utf8.Valid(piece) {
					t.Fatalf("split UTF-8: %q", piece)
				}
				if utf8.RuneCount(piece) > target+min(256, max(1, target/4)) {
					t.Fatalf("oversized chunk: %d", len(piece))
				}
				got = append(got, piece...)
			}
			if !bytes.Equal(got, input) {
				t.Fatalf("bytes changed: %q != %q", got, input)
			}
		}
	}
}

func TestChunkWhitespaceBoundary(t *testing.T) {
	if got := boundary([]byte("hello world again"), 7); got != 6 {
		t.Fatalf("boundary = %d, want 6", got)
	}
	if got := boundary([]byte("abcdefghij"), 4); got != 4 {
		t.Fatalf("unbroken boundary = %d", got)
	}
}

func TestNULPreflight(t *testing.T) {
	input := strings.Repeat("safe text", 20000) + "\x00"
	got, err := containsNUL(context.Background(), strings.NewReader(input))
	if err != nil || !got {
		t.Fatalf("NUL not found: %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := containsNUL(ctx, strings.NewReader("text")); err == nil {
		t.Fatal("ignored cancellation")
	}
}
