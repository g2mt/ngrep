package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTopSectionsAndMerge(t *testing.T) {
	top := newTopSections(2)
	for _, score := range []float64{0.1, 0.9, -0.2, 0.7} {
		top.add(match{score: score})
	}
	got := top.sorted()
	if len(got) != 2 || got[0].score != 0.9 || got[1].score != 0.7 {
		t.Fatalf("wrong top sections: %+v", got)
	}
	ranges := mergeRanges([]byteRange{{10, 20}, {0, 5}, {4, 12}, {30, 35}})
	if !reflect.DeepEqual(ranges, []byteRange{{0, 20}, {30, 35}}) {
		t.Fatalf("merge: %+v", ranges)
	}
}

func TestOutputLinesAndSafeText(t *testing.T) {
	root := t.TempDir()
	file := writeSource(t, filepath.Join(root, "text"), []byte("first\nsecond\nthird\nfourth\n"))
	info, err := os.Stat(file.real)
	if err != nil {
		t.Fatal(err)
	}
	matches := []match{{source: file, info: info, start: 7, end: 14, score: 1}, {source: file, info: info, start: 13, end: 15, score: 0.9}}
	var out, stderr bytes.Buffer
	ui := &console{out: &out, err: &stderr}
	if err := printMatches(ui, root, matches); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "text\n2:second\n3:third\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	out.Reset()
	ui.outColor = true
	if err := printMatches(ui, root, matches); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[35mtext\x1b[0m") || !strings.Contains(out.String(), "\x1b[32m2\x1b[0m") {
		t.Fatal("missing colors")
	}
	if got := safeText("hello\x1b[31m\x00\tworld\xff", true); got != "hello\\x1b[31m\\x00\tworld\\xff" {
		t.Fatalf("unsafe terminal text: %q", got)
	}
}

func TestTopSectionsTieBreak(t *testing.T) {
	top := newTopSections(2)
	for _, path := range []string{"c", "b", "a"} {
		top.add(match{source: sourceFile{path: path}, score: 1})
	}
	got := top.sorted()
	if got[0].source.path != "a" || got[1].source.path != "b" {
		t.Fatalf("unstable tie order: %+v", got)
	}
}
