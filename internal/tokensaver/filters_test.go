package tokensaver

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func filterName(f rtkFilterFunc) string {
	name := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

func TestAutoDetectFilter(t *testing.T) {
	lines := func(n int, format string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, format+"\n", i)
		}
		return b.String()
	}
	manyBlanks := "keep1\nkeep2\nkeep3\nkeep4" + strings.Repeat("\n", 246)

	tests := []struct {
		name  string
		input string
		want  rtkFilterFunc
	}{
		{"git diff", "diff --git a/src/x.go b/src/x.go\nindex 111..222 100644\n--- a/src/x.go\n+++ b/src/x.go\n@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n", filterGitDiff},
		{"git status long form", "On branch main\nChanges not staged for commit:\n  modified:   src/x.go\n", filterGitStatus},
		{"git status porcelain", " M src/a.go\n M src/b.go\n?? src/c.go\n", filterGitStatus},
		{"git log", "commit 1234567890abcdef\nAuthor: Dev <dev@example.com>\nDate: Mon Jan 1 00:00:00 2024 +0000\n\n    fix the thing\n", filterGitLog},
		{"build output", "npm warn deprecated left-pad@1.0.0\nadded 1 package in 2s\n", filterBuildOutput},
		{"grep", "src/a.go:1: alpha\nsrc/b.go:2: beta\n", filterGrep},
		{"find", "./src/a.go\n./src/b.go\n./src/c.go\n", filterFind},
		{"tree", ".\n\u251c\u2500\u2500 src\n\u2502   \u2514\u2500\u2500 index.js\n\u2514\u2500\u2500 package.json\n", filterTree},
		{"ls", "total 64\ndrwxr-xr-x 2 user staff 64 Jan  1 2024 src\n-rw-r--r-- 1 user staff 10 Jan  1 2024 a.txt\n-rw-r--r-- 1 user staff 20 Jan  1 2024 b.txt\n", filterLS},
		{"search list", "Result of search in 'src' (total 3 files):\n- src/a.go\n- src/b.go\n- src/c.go\n", filterSearchList},
		{"read numbered", lines(300, "  %d|content line %d"), filterReadNumbered},
		{"dedup log", "foo\nbar\nfoo\nbaz\nqux\nfoo\n", filterDedupLog},
		{"smart truncate", manyBlanks, filterSmartTruncate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := autoDetectFilter(test.input)
			if got == nil {
				t.Fatalf("no filter detected")
			}
			if filterName(got) != filterName(test.want) {
				t.Fatalf("detected %s, want %s", filterName(got), filterName(test.want))
			}
		})
	}
}

func TestFilterGitStatusSummary(t *testing.T) {
	input := "On branch main\nChanges to be committed:\n  new file:   src/new.go\nChanges not staged for commit:\n  modified:   src/a.go\n  modified:   src/b.go\nUntracked files:\n  src/c.go\n"
	out := filterGitStatus(input)
	for _, want := range []string{"* main", "+ Staged: 1 files", "~ Modified: 2 files", "src/new.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("git status output missing %q: %s", want, out)
		}
	}
	if len(out) >= len(input) {
		t.Fatalf("git status grew: %d -> %d", len(input), len(out))
	}
}

func TestFilterGrepGroupsByFile(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 15; i++ {
		fmt.Fprintf(&b, "src/parser.go:%d: unexpected token in expression\n", i)
	}
	out := filterGrep(b.String())
	if !strings.Contains(out, "15 matches in 1F:") {
		t.Fatalf("missing header: %s", out)
	}
	if !strings.Contains(out, "  +5") {
		t.Fatalf("missing per-file cap marker: %s", out)
	}
	if len(out) >= len(b.String()) {
		t.Fatalf("grep did not shrink: %d -> %d", len(b.String()), len(out))
	}
}

func TestFilterDedupCollapsesDuplicates(t *testing.T) {
	input := strings.Repeat("same repeated log line here\n", 8)
	out := filterDedupLog(input)
	if !strings.Contains(out, "  ... (7 duplicate lines)") {
		t.Fatalf("duplicates not collapsed: %q", out)
	}
	if len(out) >= len(input) {
		t.Fatalf("dedup grew output: %d -> %d", len(input), len(out))
	}
}

func TestFilterSmartTruncateKeepsHeadAndTail(t *testing.T) {
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	out := filterSmartTruncate(strings.Join(lines, "\n"))
	if !strings.Contains(out, "... +120 lines truncated") {
		t.Fatalf("missing truncate marker: %q", out)
	}
	if !strings.Contains(out, "line 0\n") || !strings.Contains(out, "line 299") {
		t.Fatalf("head/tail not kept: %q", out)
	}
	if len(out) >= len(strings.Join(lines, "\n")) {
		t.Fatalf("smart truncate grew output")
	}
}

func TestFilterBuildOutputKeepsErrorsAndSummary(t *testing.T) {
	input := strings.Join([]string{
		"npm warn deprecated a@1.0.0",
		"npm warn deprecated b@1.0.0",
		"npm warn deprecated c@1.0.0",
		"npm warn deprecated d@1.0.0",
		"npm warn deprecated e@1.0.0",
		"npm warn deprecated f@1.0.0",
		"added 42 packages in 5s",
		"npm ERR! code ELOCKED",
	}, "\n") + "\n"
	out := filterBuildOutput(input)
	if !strings.Contains(out, "npm ERR! code ELOCKED") || !strings.Contains(out, "added 42 packages in 5s") {
		t.Fatalf("errors/summary dropped: %s", out)
	}
	if !strings.Contains(out, "... +3 more deprecated packages") {
		t.Fatalf("deprecation cap missing: %s", out)
	}
	if len(out) >= len(input) {
		t.Fatalf("build output grew: %d -> %d", len(input), len(out))
	}
}

func TestCompressToolTextRejectsGrowingOutput(t *testing.T) {
	if out, changed := compressToolText("tiny\n"); changed || out != "tiny\n" {
		t.Fatalf("tiny blob must pass through: %q changed=%v", out, changed)
	}
	structured := "src/a.go:1: x\n"
	if out, changed := compressToolText(structured); changed && len(out) >= len(structured) {
		t.Fatalf("growing compression accepted: %q", out)
	}
}
