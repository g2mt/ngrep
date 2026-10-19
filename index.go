package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

type embedder interface {
	embed(context.Context, string) ([]float32, error)
}

type indexer struct {
	cache             *fileCache
	api               embedder
	chars, dimensions int
	force, binary     bool
	privacy           *privacyGuard
	warn              func(string, ...any)
}

func sameSnapshot(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func (idx *indexer) file(ctx context.Context, source sourceFile) ([]section, os.FileInfo, bool, error) {
	skip := func(err error) ([]section, os.FileInfo, bool, error) {
		idx.warn("skipping %s: %v", source.path, err)
		return nil, nil, true, nil
	}
	resolved, err := filepath.EvalSymlinks(source.real)
	if err != nil {
		return skip(err)
	}
	if resolved != source.real {
		return skip(errors.New("resolved path changed during traversal"))
	}
	if err := idx.privacy.authorize(ctx, filepath.Dir(resolved)); err != nil {
		return nil, nil, false, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return skip(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return skip(err)
	}
	if !info.Mode().IsRegular() {
		return skip(errors.New("not a regular file"))
	}

	// Preflight the whole file, even for cache hits: an earlier --binary run
	// must not make a binary file visible under normal filtering.
	if !idx.binary {
		binary, err := containsNUL(ctx, f)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			return skip(err)
		}
		if binary {
			return nil, nil, true, nil
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return skip(err)
		}
	}
	var sections []section
	hit := false
	if !idx.force {
		sections, hit, err = idx.cache.load(source.real, info)
		if err != nil {
			idx.warn("rebuilding cache for %s: %v", source.path, err)
		}
		if hit && len(sections) > 0 {
			dimensions := len(sections[0].Vector)
			if idx.dimensions != 0 && idx.dimensions != dimensions {
				hit = false
			}
		}
	}
	if !hit {
		sections = nil
		chunks := newChunker(f, idx.chars)
		var offset uint64
		for {
			if err := ctx.Err(); err != nil {
				return nil, nil, false, err
			}
			chunk, err := chunks.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return skip(err)
			}
			pieces, err := idx.embedChunk(ctx, chunk, offset, &chunks.target)
			if err != nil {
				return nil, nil, false, fmt.Errorf("index %s: %w", source.path, err)
			}
			sections = append(sections, pieces...)
			offset += uint64(len(chunk))
		}
	}
	current, err := f.Stat()
	if err != nil {
		return skip(err)
	}
	pathInfo, err := os.Stat(source.real)
	if err != nil {
		return skip(err)
	}
	if !sameSnapshot(info, current) || !sameSnapshot(info, pathInfo) {
		return skip(errors.New("file changed during indexing; rerun to retry"))
	}
	if len(sections) > 0 && idx.dimensions == 0 {
		idx.dimensions = len(sections[0].Vector)
	}
	if !hit {
		if err := idx.cache.store(source.real, info, sections); err != nil {
			idx.warn("cannot cache %s: %v", source.path, err)
		}
	}
	return sections, info, false, nil
}

func (idx *indexer) embedChunk(ctx context.Context, data []byte, offset uint64, target *int) ([]section, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	vector, err := idx.api.embed(ctx, string(data))
	if isTooLong(err) && utf8.RuneCount(data) > 1 {
		half := max(1, utf8.RuneCount(data)/2)
		end := boundary(data, half)
		if end >= len(data) {
			end = 0
			for n := 0; n < half; n++ {
				_, size := utf8.DecodeRune(data[end:])
				end += size
			}
		}
		if end <= 0 || end >= len(data) {
			return nil, err
		}
		*target = min(*target, half)
		// Complete the first half before making any request for the second half.
		left, err := idx.embedChunk(ctx, data[:end], offset, target)
		if err != nil {
			return nil, err
		}
		right, err := idx.embedChunk(ctx, data[end:], offset+uint64(end), target)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
	}
	if err != nil {
		return nil, err
	}
	if !validVector(vector) {
		return nil, errors.New("embedding API returned an invalid vector")
	}
	if idx.dimensions != 0 && len(vector) != idx.dimensions {
		return nil, errors.New("embedding dimensions changed within this run")
	}
	idx.dimensions = len(vector)
	return []section{{Start: offset, End: offset + uint64(len(data)), Vector: vector}}, nil
}
