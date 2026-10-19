package main

import (
	"bufio"
	"container/heap"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type match struct {
	source     sourceFile
	info       os.FileInfo
	start, end uint64
	score      float64
}

func better(a, b match) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.source.path != b.source.path {
		return a.source.path < b.source.path
	}
	return a.start < b.start
}

type matchHeap []match

func (h matchHeap) Len() int           { return len(h) }
func (h matchHeap) Less(i, j int) bool { return better(h[j], h[i]) }
func (h matchHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *matchHeap) Push(x any)        { *h = append(*h, x.(match)) }
func (h *matchHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

type topSections struct {
	n     int
	items matchHeap
}

func newTopSections(n int) *topSections { return &topSections{n: n} }
func (t *topSections) add(m match) {
	if len(t.items) < t.n {
		heap.Push(&t.items, m)
		return
	}
	if better(m, t.items[0]) {
		t.items[0] = m
		heap.Fix(&t.items, 0)
	}
}
func (t *topSections) sorted() []match {
	result := append([]match(nil), t.items...)
	sort.Slice(result, func(i, j int) bool { return better(result[i], result[j]) })
	return result
}

type byteRange struct{ start, end uint64 }

func mergeRanges(ranges []byteRange) []byteRange {
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		return ranges[i].end < ranges[j].end
	})
	var merged []byteRange
	for _, r := range ranges {
		if len(merged) > 0 && r.start <= merged[len(merged)-1].end {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, r.end)
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

func printMatches(ui *console, root string, matches []match) error {
	type group struct {
		first  match
		ranges []byteRange
	}
	var groups []group
	positions := map[string]int{}
	for _, m := range matches {
		pos, ok := positions[m.source.real]
		if !ok {
			pos = len(groups)
			positions[m.source.real] = pos
			groups = append(groups, group{first: m})
		}
		groups[pos].ranges = append(groups[pos].ranges, byteRange{m.start, m.end})
	}
	printedFile := false
	for _, group := range groups {
		f, err := os.Open(group.first.source.real)
		if err != nil {
			ui.warn("cannot display %s: %v", group.first.source.path, err)
			continue
		}
		info, err := f.Stat()
		if err != nil || !sameSnapshot(group.first.info, info) {
			f.Close()
			ui.warn("cannot display %s: file changed since indexing", group.first.source.path)
			continue
		}
		ranges := mergeRanges(group.ranges)
		r := bufio.NewReader(f)
		var offset uint64
		lineNumber, rangeIndex := 0, 0
		printedHeader := false
		for rangeIndex < len(ranges) {
			line, readErr := r.ReadString('\n')
			lineNumber++
			end := offset + uint64(len(line))
			for rangeIndex < len(ranges) && ranges[rangeIndex].end <= offset {
				rangeIndex++
			}
			if rangeIndex == len(ranges) {
				break
			}
			if len(line) > 0 && offset < ranges[rangeIndex].end && end > ranges[rangeIndex].start {
				if !printedHeader {
					if printedFile {
						if _, err := fmt.Fprintln(ui.out); err != nil {
							f.Close()
							return err
						}
					}
					name, err := filepath.Rel(root, group.first.source.path)
					if err != nil {
						name = group.first.source.path
					}
					name = safeText(name, false)
					if ui.outColor {
						name = "\x1b[35m" + name + "\x1b[0m"
					}
					if _, err := fmt.Fprintln(ui.out, name); err != nil {
						f.Close()
						return err
					}
					printedHeader, printedFile = true, true
				}
				text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
				number := fmt.Sprint(lineNumber)
				if ui.outColor {
					number = "\x1b[32m" + number + "\x1b[0m"
				}
				if _, err := fmt.Fprintf(ui.out, "%s:%s\n", number, safeText(text, true)); err != nil {
					f.Close()
					return err
				}
			}
			offset = end
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				ui.warn("cannot read %s: %v", group.first.source.path, readErr)
				break
			}
		}
		f.Close()
	}
	return nil
}

// Never let file contents, names, or server error messages inject terminal
// control sequences. Ordinary text is not highlighted or otherwise changed.
func safeText(text string, tabs bool) string {
	var out strings.Builder
	for len(text) > 0 {
		r, n := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && n == 1 {
			fmt.Fprintf(&out, "\\x%02x", text[0])
		} else if (unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '\u202e' || r == '\u202d' || (r >= '\u2066' && r <= '\u2069')) && !(tabs && r == '\t') {
			if r < 256 {
				fmt.Fprintf(&out, "\\x%02x", r)
			} else {
				fmt.Fprintf(&out, "\\u%04x", r)
			}
		} else {
			out.WriteRune(r)
		}
		text = text[n:]
	}
	return out.String()
}
