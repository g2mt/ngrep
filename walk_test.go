package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func fixtureFile(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoreRules(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		pattern, path   string
		directory, want bool
	}{
		{"*.log", "a/b.log", false, true},
		{"/root.txt", "a/root.txt", false, false},
		{"/root.txt", "root.txt", false, true},
		{"build/", "build/file", false, true},
		{"build/", "build", false, false},
		{"build/", "build", true, true},
		{"a/**/z?.[ch]", "a/z1.c", false, true},
		{"a/**/z?.[ch]", "a/b/d/z2.h", false, true},
		{"a/**/z?.[ch]", "x/a/z1.c", false, false},
		{"\\#file", "#file", false, true},
		{"\\!file", "!file", false, true},
		{"name\\ ", "name ", false, true},
		{"name   ", "name", false, true},
	}
	for _, test := range tests {
		rule, ok, err := parseIgnoreLine(root, test.pattern)
		if err != nil || !ok {
			t.Fatalf("parse %q: %v", test.pattern, err)
		}
		if got := rule.matches(filepath.Join(root, filepath.FromSlash(test.path)), test.directory); got != test.want {
			t.Errorf("%q on %q = %v, want %v", test.pattern, test.path, got, test.want)
		}
	}
}

func TestDiscoverFilteringAndStateExclusion(t *testing.T) {
	root := t.TempDir()
	for path, text := range map[string]string{
		".gitignore":   "*.log\nignored/\n",
		".ngrepignore": "!keep.log\n!.visible\n",
		"file.txt":     "text", "drop.log": "log", "keep.log": "log",
		"ignored/file.txt": "ignored", ".visible": "visible", ".hidden": "hidden",
		".git/config": "git", "nested/a.txt": "nested", "nested/b.txt": "nested",
		"nested/.ngrepignore": "b.txt\n", "state/auth": "secret", "state/cache/hash": "secret",
	} {
		fixtureFile(t, root, path, text)
	}
	warn := func(string, ...any) {}
	get := func(opts options) []string {
		files, err := discover(context.Background(), root, filepath.Join(root, "state"), opts, warn)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range files {
			name, _ := filepath.Rel(root, f.path)
			names = append(names, filepath.ToSlash(name))
		}
		sort.Strings(names)
		return names
	}
	want := []string{".visible", "file.txt", "keep.log", "nested/a.txt"}
	if got := get(options{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("default: %q, want %q", got, want)
	}
	contains := func(names []string, target string) bool {
		for _, name := range names {
			if name == target {
				return true
			}
		}
		return false
	}
	u := get(options{Unrestricted: 1})
	if !contains(u, "drop.log") || !contains(u, "ignored/file.txt") || contains(u, ".hidden") || contains(u, ".visible") {
		t.Fatalf("-u: %q", u)
	}
	uu := get(options{Unrestricted: 2})
	if !contains(uu, ".git/config") || !contains(uu, ".hidden") || !contains(uu, "nested/b.txt") {
		t.Fatalf("-uu: %q", uu)
	}
	for _, name := range get(options{Unrestricted: 3}) {
		if name == "state/auth" || name == "state/cache/hash" {
			t.Fatal("ngrep state leaked")
		}
	}
	if got := get(options{Hidden: true}); !contains(got, ".git/config") || contains(got, "drop.log") {
		t.Fatalf("--hidden: %q", got)
	}
}

func TestDiscoverSymlinkCyclesAndAliases(t *testing.T) {
	root := t.TempDir()
	fixtureFile(t, root, "dir/text", "text")
	fixtureFile(t, root, "state/auth", "secret")
	if err := os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "alias")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(root, filepath.Join(root, "dir", "cycle")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "state"), filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	for _, follow := range []bool{false, true} {
		files, err := discover(context.Background(), root, filepath.Join(root, "state"), options{Follow: follow, Unrestricted: 3}, func(string, ...any) {})
		if err != nil || len(files) != 1 {
			t.Fatalf("follow=%v: files=%+v err=%v", follow, files, err)
		}
		if filepath.Base(files[0].real) != "text" {
			t.Fatal("state alias was indexed")
		}
	}
}
