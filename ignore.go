package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ignoreRule struct {
	base                        string
	pattern                     *regexp.Regexp
	negate, directory, basename bool
}

// globRE implements path-aware *, ?, [], escapes, and whole-component **.
// It is shared by ignore files and the directory whitelist.
func globRE(glob string, fold bool) (*regexp.Regexp, error) {
	var out strings.Builder
	if fold {
		out.WriteString("(?i)")
	}
	out.WriteByte('^')
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' && (i == 0 || glob[i-1] == '/') && (i+2 == len(glob) || glob[i+2] == '/') {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					out.WriteString("(?:[^/]+/)*")
					i++
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		case '[':
			j := i + 1
			if j < len(glob) && (glob[j] == '!' || glob[j] == '^') {
				j++
			}
			if j < len(glob) && glob[j] == ']' {
				j++
			}
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			if j == len(glob) {
				return nil, fmt.Errorf("unterminated character class in %q", glob)
			}
			class := glob[i+1 : j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			out.WriteByte('[')
			out.WriteString(class)
			out.WriteByte(']')
			i = j
		case '\\':
			i++
			if i == len(glob) {
				return nil, fmt.Errorf("trailing escape in %q", glob)
			}
			out.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		default:
			out.WriteString(regexp.QuoteMeta(glob[i : i+1]))
		}
	}
	out.WriteByte('$')
	return regexp.Compile(out.String())
}

func trimIgnoreSpace(line string) string {
	for strings.HasSuffix(line, " ") {
		backslashes := 0
		for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			break
		}
		line = line[:len(line)-1]
	}
	return line
}

func parseIgnoreLine(base, line string) (ignoreRule, bool, error) {
	line = trimIgnoreSpace(strings.TrimSuffix(line, "\r"))
	if line == "" || line[0] == '#' {
		return ignoreRule{}, false, nil
	}
	rule := ignoreRule{base: base}
	if line[0] == '!' {
		rule.negate = true
		line = line[1:]
	}
	if line == "" {
		return rule, false, nil
	}
	rule.directory = strings.HasSuffix(line, "/")
	line = strings.TrimSuffix(line, "/")
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return rule, false, nil
	}
	rule.basename = !anchored && !strings.Contains(line, "/")
	var err error
	rule.pattern, err = globRE(line, false)
	return rule, err == nil, err
}

func readIgnoreFiles(dir string, rules []ignoreRule, warn func(string, ...any)) []ignoreRule {
	// ngrep-specific rules take precedence over Git rules at the same depth.
	for _, name := range []string{".gitignore", ".ngrepignore"} {
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			warn("cannot read %s: %v", path, err)
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		line := 0
		for scanner.Scan() {
			line++
			rule, ok, err := parseIgnoreLine(dir, scanner.Text())
			if err != nil {
				warn("%s:%d: %v", path, line, err)
			}
			if ok {
				rules = append(rules, rule)
			}
		}
		if err := scanner.Err(); err != nil {
			warn("cannot read %s: %v", path, err)
		}
		f.Close()
	}
	return rules
}

func (r ignoreRule) matches(path string, isDir bool) bool {
	rel, err := filepath.Rel(r.base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		if r.directory && i == len(parts)-1 && !isDir {
			continue
		}
		candidate := part
		if !r.basename {
			candidate = strings.Join(parts[:i+1], "/")
		}
		if r.pattern.MatchString(candidate) {
			return true
		}
	}
	return false
}

func ignoreDecision(rules []ignoreRule, path string, isDir bool) (ignored, whitelisted bool) {
	for _, rule := range rules {
		if rule.matches(path, isDir) {
			ignored, whitelisted = !rule.negate, rule.negate
		}
	}
	return
}
