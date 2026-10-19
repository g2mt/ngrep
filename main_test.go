package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

func TestCLIFlags(t *testing.T) {
	var opts options
	parser, err := kong.New(&opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"-uuu", "-.", "-L", "--binary", "--top", "4", "query"}); err != nil {
		t.Fatal(err)
	}
	if opts.Unrestricted != 3 || !opts.Hidden || !opts.Follow || !opts.Binary || opts.Top != 4 || opts.Query != "query" {
		t.Fatalf("wrong options: %+v", opts)
	}
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ngrep")
	s, err := loadSettings(dir)
	if err != nil || s.EstimatedCharsPerSection != 4000 || s.Embedding.Model != "text-embedding-3-small" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	path := filepath.Join(dir, "settings.json")
	for _, text := range []string{`{"max_files_in_cache":-1}`, `{"estimated_chars_per_section":0}`, `{"unknown":true}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettings(dir); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}

func TestRunIndexSearchAndAutomaticUpdate(t *testing.T) {
	// This server is local to the test; no OpenAI or other external service is used.
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong API request: %s", r.URL.Path)
		}
		var body struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		vector := []float32{0, 1}
		if strings.Contains(body.Input, "capital") {
			vector = []float32{1, 0}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}})
	}))
	defer server.Close()
	root := t.TempDir()
	t.Chdir(root)
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	t.Setenv("LOCALAPPDATA", base)
	state, err := stateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(state)
	if err != nil {
		t.Fatal(err)
	}
	s.Embedding.APIHost = server.URL + "/v1"
	s.Whitelist = []string{root}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "auth"), []byte("test-key"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, root, "capital.txt", "Paris is the capital of France.\n")
	fixtureFile(t, root, "other.txt", "Cooking instructions.\n")
	var out, stderr bytes.Buffer
	ui := &console{out: &out, err: &stderr}
	if err := run(context.Background(), options{Index: true, Top: 10}, ui); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || out.Len() != 0 {
		t.Fatalf("index: calls=%d stdout=%q stderr=%q", calls, out.String(), stderr.String())
	}
	stderr.Reset()
	if err := run(context.Background(), options{Query: "capital", Top: 1}, ui); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || out.String() != "capital.txt\n1:Paris is the capital of France.\n" {
		t.Fatalf("search: calls=%d output=%q", calls, out.String())
	}
	fixtureFile(t, root, "capital.txt", "Paris remains the capital of France today.\n")
	out.Reset()
	if err := run(context.Background(), options{Query: "capital", Top: 1}, ui); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || !strings.Contains(out.String(), "remains") {
		t.Fatalf("automatic update: calls=%d output=%q", calls, out.String())
	}
}

func TestRunRejectsInvalidModesBeforeIO(t *testing.T) {
	for _, opts := range []options{{Top: 0}, {Top: 10}, {Top: 10, Index: true, Query: "query"}, {Top: 10, Key: true, Index: true}} {
		if err := run(context.Background(), opts, &console{out: io.Discard, err: io.Discard}); err == nil {
			t.Fatal(fmt.Sprintf("accepted %+v", opts))
		}
	}
}
