package tokensaver

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// RTK filter-suite constants, mirrored from 9router open-sse/rtk/constants.js
// (which itself mirrors the Rust rtk defaults).
const (
	rtkRawCap             = 10 * 1024 * 1024 // raw blobs above 10 MiB are passed through
	rtkDetectWindow       = 1024             // autodetect peeks first N characters
	rtkReadNumberedRatio  = 0.7              // min hit ratio for numbered file dumps
	rtkSmartTruncateMin   = 250              // min lines for read-numbered / smart-truncate
	rtkPorcelainMin       = 3                // min porcelain lines before majority check
	rtkPorcelainRatio     = 0.6
	rtkGrepFirstLines     = 5 // grep rule inspects first N non-empty lines
	rtkFindMinLines       = 3
	rtkDedupMinLines      = 5
	rtkLineNumberedSample = 100
	rtkLineNumberedMin    = 5
)

var (
	rtkReGitDiff      = regexp.MustCompile(`(?m)^diff --git `)
	rtkReGitDiffHunk  = regexp.MustCompile(`(?m)^@@ `)
	rtkReGitStatus    = regexp.MustCompile(`(?m)^On branch |^nothing to commit|^Changes (not |to be )|^Untracked files:`)
	rtkReGitLog       = regexp.MustCompile(`(?m)^[*|/\\ ]*commit [0-9a-f]{7,40}$`)
	rtkRePorcelain    = regexp.MustCompile(`(?m)^[ MADRCU?!][ MADRCU?!] \S`)
	rtkReBuildOutput  = regexp.MustCompile(`(?im)^(npm (warn|error|ERR!)|yarn (warn|error)|\s*Compiling\s+\S+|\s*Downloading\s+\S+|added \d+ package|\[ERROR\]|BUILD (SUCCESS|FAILED)|\s*Finished\s+|Successfully (installed|built)|ERROR:)`)
	rtkReTreeGlyph    = regexp.MustCompile(`[\x{251C}\x{2514}]\x{2500}\x{2500}|\x{2502}  `)
	rtkReLSRow        = regexp.MustCompile(`(?m)^[-dlbcps][rwx-]{9}`)
	rtkReLSTotal      = regexp.MustCompile(`(?m)^total \d+$`)
	rtkReSearchList   = regexp.MustCompile(`^Result of search in '[^']*' \(total (\d+) files?\):`)
	rtkReLineNumbered = regexp.MustCompile(`^\s*\d+\|`)
	rtkReDriveLetter  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	rtkReGrepNumbered = regexp.MustCompile(`^\d+$`)
)

// autoDetectFilter picks the RTK filter for a tool_result blob.
// Port of 9router open-sse/rtk/autodetect.js (Rust pipe_cmd.rs auto_detect_filter).
// Detection order: git-log -> git-diff -> git-status -> build-output -> porcelain
// -> grep -> find -> tree -> ls -> search-list -> read-numbered -> dedup-log
// -> smart-truncate -> nil.
func autoDetectFilter(text string) rtkFilterFunc {
	head := truncateDetectWindow(text)

	if rtkReGitLog.MatchString(head) {
		return filterGitLog
	}
	if rtkReGitDiff.MatchString(head) || rtkReGitDiffHunk.MatchString(head) {
		return filterGitDiff
	}
	if rtkReGitStatus.MatchString(head) {
		return filterGitStatus
	}
	// Build output is checked before porcelain so cargo "Compiling" lines are
	// not mistaken for git status.
	if rtkReBuildOutput.MatchString(head) {
		return filterBuildOutput
	}
	if isMostlyPorcelain(head) {
		return filterGitStatus
	}

	nonEmpty := splitNonEmptyLines(head)

	// Rust grep rule: any of the first non-empty lines looks like "file:lineno:content".
	first := nonEmpty
	if len(first) > rtkGrepFirstLines {
		first = first[:rtkGrepFirstLines]
	}
	for _, line := range first {
		if isGrepLine(line) {
			return filterGrep
		}
	}

	// Rust find rule: all non-empty lines are path-like (no colon), >= 3 lines.
	if len(nonEmpty) >= rtkFindMinLines {
		allPathLike := true
		for _, line := range nonEmpty {
			if !isPathLike(line) {
				allPathLike = false
				break
			}
		}
		if allPathLike {
			return filterFind
		}
	}

	if rtkReTreeGlyph.MatchString(head) {
		return filterTree
	}
	if rtkReLSTotal.MatchString(head) || countMatches(head, rtkReLSRow) >= 3 {
		return filterLS
	}
	if rtkReSearchList.MatchString(head) {
		return filterSearchList
	}

	// Line-numbered file dump ("  N|content"), checked against the full text
	// rather than the detect window: a 1024-char head cannot hold 250 lines,
	// which would make this branch unreachable (9router's own suite only
	// exercises readNumbered directly).
	fullLines := splitLines(text)
	if len(fullLines) >= rtkSmartTruncateMin && isLineNumbered(fullLines) {
		return filterReadNumbered
	}

	// Fallback for generic multi-line noise with duplicates.
	if len(nonEmpty) >= rtkDedupMinLines {
		return filterDedupLog
	}

	// Last resort: big unstructured blob.
	if len(fullLines) >= rtkSmartTruncateMin {
		return filterSmartTruncate
	}
	return nil
}

// isGrepLine reports whether a line matches Rust's "file:lineno:content" rule:
// splitn(3, ':') with parts[1] parsing as an integer.
func isGrepLine(line string) bool {
	first := strings.IndexRune(line, ':')
	if first < 0 {
		return false
	}
	second := strings.Index(line[first+1:], ":")
	if second < 0 {
		return false
	}
	lineno := line[first+1 : first+1+second]
	return lineno != "" && rtkReGrepNumbered.MatchString(lineno)
}

// isPathLike reports whether a line is a bare filesystem path (find-style).
func isPathLike(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	// Windows drive-letter paths are path-like even with a trailing colon suffix.
	if rtkReDriveLetter.MatchString(t) {
		return true
	}
	if strings.IndexRune(t, ':') >= 0 {
		return false
	}
	return t[0] == '.' || t[0] == '/' || strings.IndexRune(t, '/') >= 0
}

// isMostlyPorcelain reports whether >= 60% of lines look like `git status --porcelain`.
func isMostlyPorcelain(head string) bool {
	lines := splitNonEmptyLines(head)
	if len(lines) < rtkPorcelainMin {
		return false
	}
	hits := 0
	for _, line := range lines {
		if rtkRePorcelain.MatchString(line) {
			hits++
		}
	}
	return float64(hits)/float64(len(lines)) >= rtkPorcelainRatio
}

// isLineNumbered reports whether a file dump is mostly "N|content" lines.
func isLineNumbered(lines []string) bool {
	sample := lines
	if len(sample) > rtkLineNumberedSample {
		sample = sample[:rtkLineNumberedSample]
	}
	hits, nonEmpty := 0, 0
	for _, line := range sample {
		if len(line) == 0 {
			continue
		}
		nonEmpty++
		if rtkReLineNumbered.MatchString(line) {
			hits++
		}
	}
	if nonEmpty < rtkLineNumberedMin {
		return false
	}
	return float64(hits)/float64(nonEmpty) >= rtkReadNumberedRatio
}

func countMatches(text string, re *regexp.Regexp) int {
	return len(re.FindAllStringIndex(text, -1))
}

// truncateDetectWindow returns the first rtkDetectWindow characters of text
// without splitting a UTF-8 rune (JS slices by character).
func truncateDetectWindow(text string) string {
	if len(text) <= rtkDetectWindow {
		return text
	}
	head := text[:rtkDetectWindow]
	for !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	return head
}
