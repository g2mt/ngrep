package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type fakeEmbedder struct {
	calls []string
	limit int
}

func (f *fakeEmbedder) embed(_ context.Context, text string) ([]float32, error) {
	f.calls = append(f.calls, text)
	if f.limit > 0 && utf8.RuneCountInString(text) > f.limit {
		return nil, &apiError{status: 400, code: "context_length_exceeded"}
	}
	return []float32{1, 0}, nil
}

func newTestIndexer(t *testing.T, root string, fake *fakeEmbedder) *indexer {
	t.Helper()
	ui := &console{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	guard, err := newPrivacy([]string{root}, ui, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	return &indexer{cache: &fileCache{dir: filepath.Join(root, "state", "cache"), limit: 100, embedding: defaultSettings().Embedding},
		api: fake, chars: 12, privacy: guard, warn: func(string, ...any) {}}
}

func writeSource(t *testing.T, path string, data []byte) sourceFile {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return sourceFile{path: path, real: real}
}

func TestIndexCacheForceAndChunkSettings(t *testing.T) {
	root := t.TempDir()
	fake := &fakeEmbedder{}
	idx := newTestIndexer(t, root, fake)
	file := writeSource(t, filepath.Join(root, "text"), []byte("abcdefghijklmnoPQRSTUVWXYZ"))
	sections, _, skipped, err := idx.file(context.Background(), file)
	if err != nil || skipped || len(sections) < 2 {
		t.Fatalf("index: %v %v %v", sections, skipped, err)
	}
	calls := len(fake.calls)
	idx.chars = 2
	cached, _, _, err := idx.file(context.Background(), file)
	if err != nil || len(fake.calls) != calls || len(cached) != len(sections) {
		t.Fatal("chunk setting change invalidated cache", err)
	}
	idx.force = true
	if _, _, _, err := idx.file(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) <= calls {
		t.Fatal("--force reused cache")
	}
	idx.force = false
	calls = len(fake.calls)
	idx.cache.embedding.Model = "different-model"
	if _, _, _, err := idx.file(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) <= calls {
		t.Fatal("model change reused cache")
	}
}

func TestOversizedChunkSplitsSerially(t *testing.T) {
	root := t.TempDir()
	fake := &fakeEmbedder{limit: 3}
	idx := newTestIndexer(t, root, fake)
	input := []byte("abcdefghijklmnopqrstuvwxyz")
	file := writeSource(t, filepath.Join(root, "text"), input)
	sections, _, skipped, err := idx.file(context.Background(), file)
	if err != nil || skipped {
		t.Fatalf("%v %v", skipped, err)
	}
	var reconstructed []byte
	var end uint64
	for _, s := range sections {
		if s.Start != end || s.End <= s.Start || s.End-s.Start > 3 {
			t.Fatalf("bad retry offsets: %+v", s)
		}
		reconstructed = append(reconstructed, input[s.Start:s.End]...)
		end = s.End
	}
	if !bytes.Equal(reconstructed, input) {
		t.Fatalf("retry dropped text: %q", reconstructed)
	}
	if len(fake.calls) < 3 || fake.calls[0] != "abcdefghijkl" || fake.calls[1] != "abcdef" || fake.calls[2] != "abc" {
		t.Fatalf("not serial splitting: %q", fake.calls)
	}
}

func TestBinarySkippedBeforeAnyEmbeddingAndAfterBinaryCache(t *testing.T) {
	root := t.TempDir()
	fake := &fakeEmbedder{}
	idx := newTestIndexer(t, root, fake)
	input := []byte(strings.Repeat("safe text", 10000) + "\x00")
	file := writeSource(t, filepath.Join(root, "binary"), input)
	if _, _, skipped, err := idx.file(context.Background(), file); err != nil || !skipped || len(fake.calls) != 0 {
		t.Fatalf("binary was uploaded: %v %v calls=%d", skipped, err, len(fake.calls))
	}
	idx.binary = true
	idx.chars = 100000
	if _, _, skipped, err := idx.file(context.Background(), file); err != nil || skipped {
		t.Fatalf("--binary: %v %v", skipped, err)
	}
	calls := len(fake.calls)
	idx.binary = false
	if _, _, skipped, err := idx.file(context.Background(), file); err != nil || !skipped || len(fake.calls) != calls {
		t.Fatal("binary cache bypassed default filter")
	}
}

func TestWhitespaceAndEmptyFilesNeedNoEmbeddings(t *testing.T) {
	root := t.TempDir()
	fake := &fakeEmbedder{}
	idx := newTestIndexer(t, root, fake)
	for i, data := range [][]byte{nil, []byte(" \n\t ")} {
		file := writeSource(t, filepath.Join(root, string(rune('a'+i))), data)
		sections, _, skipped, err := idx.file(context.Background(), file)
		if err != nil || skipped || len(sections) != 0 || len(fake.calls) != 0 {
			t.Fatalf("empty file: %v %v %v", sections, skipped, err)
		}
	}
}
