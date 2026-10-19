package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

type embeddingSettings struct {
	APIHost string `json:"api_host"`
	Model   string `json:"model"`
}

type settings struct {
	Embedding                embeddingSettings `json:"embedding"`
	EstimatedCharsPerSection int               `json:"estimated_chars_per_section"`
	MaxFilesInCache          int               `json:"max_files_in_cache"`
	Whitelist                []string          `json:"whitelist"`
}

func defaultSettings() settings {
	return settings{Embedding: embeddingSettings{APIHost: "https://api.openai.com/v1", Model: "text-embedding-3-small"},
		EstimatedCharsPerSection: 4000, MaxFilesInCache: 1000, Whitelist: []string{}}
}

func stateDirectory() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			var err error
			base, err = os.UserCacheDir()
			if err != nil {
				return "", err
			}
		}
		return filepath.Join(base, "ngrep"), nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "ngrep"), nil
}

func loadSettings(dir string) (settings, error) {
	s := defaultSettings()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return s, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return s, err
	}
	name := filepath.Join(dir, "settings.json")
	f, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		data, marshalErr := json.MarshalIndent(s, "", "  ")
		if marshalErr != nil {
			return s, marshalErr
		}
		// O_EXCL prevents one process from replacing another's configuration.
		created, createErr := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(createErr, os.ErrExist) {
			return loadSettings(dir)
		}
		if createErr != nil {
			return s, createErr
		}
		_, writeErr := created.Write(append(data, '\n'))
		closeErr := created.Close()
		if writeErr != nil {
			return s, writeErr
		}
		return s, closeErr
	}
	if err != nil {
		return s, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return s, err
	}
	if info.Size() > 1<<20 {
		return s, fmt.Errorf("%s: settings exceed 1 MiB", name)
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return s, fmt.Errorf("%s: %w", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("%s: expected a single JSON object", name)
	}
	if s.EstimatedCharsPerSection < 1 || s.EstimatedCharsPerSection > 1_000_000 {
		return s, errors.New("estimated_chars_per_section must be between 1 and 1000000")
	}
	if s.MaxFilesInCache < 0 {
		return s, errors.New("max_files_in_cache cannot be negative")
	}
	return s, nil
}

// Files are private and replaced only after a complete, synced write.
func atomicFile(name string, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".ngrep-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := write(f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
