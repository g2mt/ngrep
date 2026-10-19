package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestV0RoundTrip(t *testing.T) {
	settings := defaultSettings().Embedding
	sections := []section{{Start: 0, End: 9, Vector: []float32{1, 2, -0.5}}, {Start: 8, End: 20, Vector: []float32{0, 1, 0}}}
	var data bytes.Buffer
	if err := writeV0(&data, settings, sections); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(data.Bytes()[:4]) != 0 {
		t.Fatal("wrong version")
	}
	terminator := bytes.IndexByte(data.Bytes()[4:], 0) + 4
	if terminator <= 4 {
		t.Fatal("no JSON terminator")
	}
	if !json.Valid(data.Bytes()[4:terminator]) {
		t.Fatal("invalid JSON metadata")
	}
	if binary.BigEndian.Uint32(data.Bytes()[terminator+1:terminator+5]) != 3 {
		t.Fatal("wrong dimensions or byte order")
	}
	got, err := readV0(bytes.NewReader(data.Bytes()), settings, 20)
	if err != nil || !reflect.DeepEqual(got, sections) {
		t.Fatalf("roundtrip: %#v, %v", got, err)
	}
	for _, n := range []int{0, 3, terminator, terminator + 2, data.Len() - 1} {
		if _, err := readV0(bytes.NewReader(data.Bytes()[:n]), settings, 20); !errors.Is(err, errInvalidCache) {
			t.Fatalf("accepted truncated data of length %d: %v", n, err)
		}
	}
	changed := settings
	changed.APIHost = "https://other.example/v1"
	if _, err := readV0(bytes.NewReader(data.Bytes()), changed, 20); err == nil {
		t.Fatal("accepted different host")
	}
	changed = settings
	changed.Model = "other"
	if _, err := readV0(bytes.NewReader(data.Bytes()), changed, 20); err == nil {
		t.Fatal("accepted different model")
	}
	if _, err := readV0(bytes.NewReader(data.Bytes()), settings, 19); err == nil {
		t.Fatal("accepted out-of-range offsets")
	}
	badVersion := bytes.Clone(data.Bytes())
	badVersion[3] = 1
	if _, err := readV0(bytes.NewReader(badVersion), settings, 20); err == nil {
		t.Fatal("accepted unknown version")
	}
	badDimensions := bytes.Clone(data.Bytes())
	binary.BigEndian.PutUint32(badDimensions[terminator+1:], maxDimensions+1)
	if _, err := readV0(bytes.NewReader(badDimensions), settings, 20); err == nil {
		t.Fatal("accepted excessive dimensions")
	}
}

func TestV0EmptyAndSemanticJSONComparison(t *testing.T) {
	s := embeddingSettings{APIHost: "https://example.test/v1", Model: "a"}
	var b bytes.Buffer
	if err := writeV0(&b, s, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := readV0(&b, s, 0); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	b.Reset()
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	_, _ = io.WriteString(&b, "{ \"model\": \"a\", \"api_host\": \"https://example.test/v1\" }\x00")
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	if _, err := readV0(&b, s, 0); err != nil {
		t.Fatal("compared JSON formatting instead of values:", err)
	}
}

func TestCacheAndFilesystemAtimeEviction(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "source")
	if err := os.WriteFile(file, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	cache := fileCache{dir: filepath.Join(root, "cache"), limit: 10, embedding: defaultSettings().Embedding}
	sections := []section{{Start: 0, End: 5, Vector: []float32{1, 0}}}
	if err := cache.store(file, info, sections); err != nil {
		t.Fatal(err)
	}
	got, hit, err := cache.load(file, info)
	if err != nil || !hit || !reflect.DeepEqual(got, sections) {
		t.Fatalf("load: %v %v %v", got, hit, err)
	}
	oldName, newerName := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, name := range []string{oldName, newerName} {
		if err := os.WriteFile(filepath.Join(cache.dir, name), []byte("entry"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	newer := now.Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(cache.dir, oldName), old, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(cache.dir, newerName), newer, old); err != nil {
		t.Fatal(err)
	}
	cache.limit = 2
	if err := cache.evict(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache.dir, oldName)); !os.IsNotExist(err) {
		t.Fatalf("oldest atime was not evicted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache.dir, newerName)); err != nil {
		t.Fatal("evicted by mtime, not atime:", err)
	}
	cache.limit = 0
	if _, hit, err := cache.load(file, info); err != nil || hit {
		t.Fatal("zero cache limit did not disable caching")
	}
	if err := cache.evict(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("zero limit did not clear entries: %v %v", entries, err)
	}
}

func TestAtomicWritePreservesOldFileOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("write failed")
	err := atomicFile(path, func(w io.Writer) error { _, _ = io.WriteString(w, "partial"); return want })
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("old file changed: %q %v", got, err)
	}
}
