package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivacyContainmentAndGlob(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"safe/docs", "safe-other", "projects/a/docs/sub", "projects/b/private"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ui := &console{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	guard, err := newPrivacy([]string{filepath.Join(root, "safe"), filepath.Join(root, "projects", "*", "docs")}, ui, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		dir     string
		allowed bool
	}{
		{"safe/docs", true}, {"safe-other", false}, {"projects/a/docs/sub", true}, {"projects/b/private", false},
	} {
		real, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(test.dir)))
		if err != nil {
			t.Fatal(err)
		}
		if got := guard.allowed(real); got != test.allowed {
			t.Errorf("%s allowed=%v", test.dir, got)
		}
	}
	if err := guard.authorize(context.Background(), filepath.Join(root, "safe-other")); err == nil {
		t.Fatal("noninteractive approval was bypassed")
	}
	guard.temporary = append(guard.temporary, root)
	if err := guard.authorize(context.Background(), filepath.Join(root, "safe-other")); err != nil {
		t.Fatal("temporary approval not inherited:", err)
	}
}

func TestPrivacyChecksResolvedSymlinkDestination(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(root, "escape")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	guard, err := newPrivacy([]string{root}, &console{out: &bytes.Buffer{}, err: &bytes.Buffer{}}, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.authorize(context.Background(), alias); err == nil {
		t.Fatal("whitelisted parent authorized an outside symlink")
	}
	if _, err := newPrivacy([]string{"relative/path"}, guard.ui, guard.host); err == nil {
		t.Fatal("accepted cwd-dependent whitelist")
	}
}
