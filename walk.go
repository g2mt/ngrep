package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type sourceFile struct {
	path string // absolute display path, possibly through a followed symlink
	real string // absolute resolved path used for cache identity and authorization
}

func discover(ctx context.Context, root, state string, opts options, warn func(string, ...any)) ([]sourceFile, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(state); err == nil {
		state = real
	}
	var initial []ignoreRule
	if opts.Unrestricted == 0 {
		var parents []string
		for dir := filepath.Dir(root); dir != root; dir = filepath.Dir(dir) {
			parents = append(parents, dir)
			if filepath.Dir(dir) == dir {
				break
			}
		}
		for i := len(parents) - 1; i >= 0; i-- {
			initial = readIgnoreFiles(parents[i], initial, warn)
		}
	}
	seenDirs, seenFiles := map[string]bool{}, map[string]bool{}
	var files []sourceFile
	var visit func(string, []ignoreRule, bool) error
	visit = func(path string, inherited []ignoreRule, isRoot bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			warn("cannot inspect %s: %v", path, err)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !isRoot && !opts.Follow {
				return nil
			}
			info, err = os.Stat(path)
			if err != nil {
				warn("cannot follow %s: %v", path, err)
				return nil
			}
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			warn("cannot resolve %s: %v", path, err)
			return nil
		}
		if within(real, state) {
			return nil
		}
		if !isRoot {
			ignored, whitelisted := false, false
			if opts.Unrestricted == 0 {
				ignored, whitelisted = ignoreDecision(inherited, path, info.IsDir())
			}
			if ignored {
				return nil
			}
			if !opts.Hidden && opts.Unrestricted < 2 && !whitelisted && (strings.HasPrefix(filepath.Base(path), ".") || hiddenAttribute(path, info)) {
				return nil
			}
		}
		identity := real
		if platformCaseInsensitive {
			identity = strings.ToLower(identity)
		}
		if info.IsDir() {
			if seenDirs[identity] {
				return nil
			}
			seenDirs[identity] = true
			entries, err := os.ReadDir(path)
			if err != nil {
				warn("cannot read directory %s: %v", path, err)
				return nil
			}
			// Copy before appending so child rules cannot alter a sibling's rules.
			rules := append([]ignoreRule(nil), inherited...)
			if opts.Unrestricted == 0 {
				rules = readIgnoreFiles(path, rules, warn)
			}
			for _, entry := range entries {
				if err := visit(filepath.Join(path, entry.Name()), rules, false); err != nil {
					return err
				}
			}
		} else if info.Mode().IsRegular() && !seenFiles[identity] {
			seenFiles[identity] = true
			files = append(files, sourceFile{path: path, real: real})
		}
		return nil
	}
	if err := visit(root, initial, true); err != nil {
		return nil, err
	}
	return files, nil
}
