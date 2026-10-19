package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type fileCache struct {
	dir       string
	limit     int
	embedding embeddingSettings
}

func cacheKey(real string, info os.FileInfo) string {
	// NUL delimiters avoid ambiguous concatenation. Paths cannot contain NUL.
	identity := fmt.Sprintf("%s\x00%d\x00%s", filepath.Clean(real), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func (c *fileCache) load(real string, info os.FileInfo) ([]section, bool, error) {
	if c.limit == 0 {
		return nil, false, nil
	}
	name := filepath.Join(c.dir, cacheKey(real, info))
	f, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	sections, err := readV0(f, c.embedding, info.Size())
	f.Close() // Windows cannot remove an open file.
	if err != nil {
		_ = os.Remove(name)
		return nil, false, err
	}
	// Actual reads update the filesystem's access time according to mount policy.
	return sections, true, nil
}

func (c *fileCache) store(real string, info os.FileInfo, sections []section) error {
	if c.limit == 0 {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(c.dir, 0700); err != nil {
		return err
	}
	if err := atomicFile(filepath.Join(c.dir, cacheKey(real, info)), func(w io.Writer) error {
		return writeV0(w, c.embedding, sections)
	}); err != nil {
		return err
	}
	return c.evict()
}

func cacheFilename(name string) bool {
	if len(name) != sha256.Size*2 {
		return false
	}
	for _, b := range name {
		if !(b >= '0' && b <= '9') && !(b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func (c *fileCache) evict() error {
	entries, err := os.ReadDir(c.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type entry struct {
		name  string
		atime time.Time
	}
	var files []entry
	for _, item := range entries {
		if !cacheFilename(item.Name()) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, entry{item.Name(), accessTime(info)})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].atime.Equal(files[j].atime) {
			return files[i].name < files[j].name
		}
		return files[i].atime.Before(files[j].atime)
	})
	var errs []error
	for i := 0; i < len(files)-c.limit; i++ {
		if err := os.Remove(filepath.Join(c.dir, files[i].name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
