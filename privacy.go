package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type privacyGuard struct {
	patterns  []*regexp.Regexp
	temporary []string
	ui        *console
	host      string
}

func within(path, dir string) bool {
	if runtime.GOOS == "windows" {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Resolve symlinks in the non-glob prefix, so an alias in settings cannot
// accidentally authorize a different directory after path resolution.
func whitelistPattern(pattern string) (string, error) {
	if pattern == "~" || strings.HasPrefix(pattern, "~/") || strings.HasPrefix(pattern, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		pattern = filepath.Join(home, strings.TrimLeft(pattern[1:], "/\\"))
	}
	if !filepath.IsAbs(pattern) {
		return "", fmt.Errorf("whitelist path must be absolute or start with ~/: %q", pattern)
	}
	pattern = filepath.Clean(pattern)
	slash := filepath.ToSlash(pattern)
	wild := strings.IndexAny(slash, "*?[")
	prefix, suffix := pattern, ""
	if wild >= 0 {
		last := strings.LastIndex(slash[:wild], "/")
		prefix = filepath.FromSlash(slash[:last+1])
		suffix = slash[last+1:]
	}
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// A nonexistent configured directory authorizes nothing until it exists.
		real = prefix
	}
	if suffix != "" {
		return strings.TrimRight(filepath.ToSlash(real), "/") + "/" + suffix, nil
	}
	return filepath.ToSlash(real), nil
}

func newPrivacy(whitelist []string, ui *console, host string) (*privacyGuard, error) {
	guard := &privacyGuard{ui: ui, host: host}
	for _, entry := range whitelist {
		pattern, err := whitelistPattern(entry)
		if err != nil {
			return nil, err
		}
		compiled, err := globRE(pattern, runtime.GOOS == "windows")
		if err != nil {
			return nil, fmt.Errorf("whitelist: %w", err)
		}
		guard.patterns = append(guard.patterns, compiled)
	}
	return guard, nil
}

func (p *privacyGuard) allowed(real string) bool {
	for _, dir := range p.temporary {
		if within(real, dir) {
			return true
		}
	}
	for dir := real; ; dir = filepath.Dir(dir) {
		for _, pattern := range p.patterns {
			if pattern.MatchString(filepath.ToSlash(dir)) {
				return true
			}
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

func (p *privacyGuard) authorize(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if p.allowed(real) {
		return nil
	}
	p.ui.warn("Privacy warning: %s is not whitelisted. Indexing uploads file contents to %s. Approval covers this directory and its descendants for this run only.", real, p.host)
	if !p.ui.interactive {
		return errors.New("upload approval requires an interactive terminal; add a trusted directory to settings.json whitelist")
	}
	fmt.Fprint(p.ui.err, "Proceed? [y/N] ")
	input, err := p.ui.readPrompt(ctx, false)
	fmt.Fprintln(p.ui.err)
	if err != nil {
		return fmt.Errorf("read upload approval: %w", err)
	}
	defer clear(input)
	if err := ctx.Err(); err != nil {
		return err
	}
	answer := strings.ToLower(strings.TrimSpace(string(input)))
	if answer != "y" && answer != "yes" {
		return errors.New("indexing cancelled; no approval given")
	}
	p.temporary = append(p.temporary, real)
	return nil
}
