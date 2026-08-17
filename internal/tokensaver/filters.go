package tokensaver

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var grepLinePattern = regexp.MustCompile(`^(.+?):(\d+):`)

func compactGrepOutput(text string) (string, bool) {
	lines := splitNonEmptyLines(text)
	if len(lines) < 3 {
		return text, false
	}
	type group struct {
		first int
		last  int
		count int
	}
	groups := map[string]*group{}
	for _, line := range lines {
		match := grepLinePattern.FindStringSubmatch(line)
		if match == nil {
			return text, false
		}
		lineNo := atoi(match[2])
		g := groups[match[1]]
		if g == nil {
			groups[match[1]] = &group{first: lineNo, last: lineNo, count: 1}
			continue
		}
		if lineNo < g.first {
			g.first = lineNo
		}
		if lineNo > g.last {
			g.last = lineNo
		}
		g.count++
	}
	paths := make([]string, 0, len(groups))
	for path := range groups {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		g := groups[path]
		out = append(out, fmt.Sprintf("%s:%d-%d: %d matches", path, g.first, g.last, g.count))
	}
	return strings.Join(out, "\n"), true
}

func compactLongLineOutput(text string) (string, bool) {
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\n")
	if len(lines) <= 80 {
		return text, false
	}
	// ponytail: first-pass generic truncation; upgrade path is command-specific build/tree filters.
	kept := append([]string{}, lines[:40]...)
	kept = append(kept, fmt.Sprintf("[... %d lines omitted ...]", len(lines)-80))
	kept = append(kept, lines[len(lines)-40:]...)
	return strings.Join(kept, "\n"), true
}

func splitNonEmptyLines(text string) []string {
	raw := strings.Split(strings.TrimRight(text, "\r\n"), "\n")
	lines := raw[:0]
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
