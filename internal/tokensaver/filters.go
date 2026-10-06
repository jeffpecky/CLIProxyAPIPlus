package tokensaver

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// rtkFilterFunc transforms a tool_result blob. Ported from the 9router
// open-sse/rtk/filters/*.js suite (mirrors Rust rtk filters).
type rtkFilterFunc func(string) string

// ---- git diff -------------------------------------------------------------

// filterGitDiff compacts unified diffs: file headers, hunk truncation at 100
// lines, +/− counts per file. Port of 9router gitDiff.js (Rust compact_diff).
func filterGitDiff(diff string) string {
	const (
		maxLines    = 500
		maxHunkLine = 100
	)
	var result []string
	currentFile := ""
	added, removed := 0, 0
	inHunk := false
	hunkShown, hunkSkipped := 0, 0
	wasTruncated := false

	flushSkip := func() {
		if hunkSkipped > 0 {
			result = append(result, fmt.Sprintf("  ... (%d lines truncated)", hunkSkipped))
			wasTruncated = true
			hunkSkipped = 0
		}
	}
	flushCounts := func() {
		if currentFile != "" && (added > 0 || removed > 0) {
			result = append(result, fmt.Sprintf("  +%d -%d", added, removed))
		}
	}

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			flushSkip()
			flushCounts()
			parts := strings.Split(line, " b/")
			if len(parts) > 1 {
				currentFile = strings.Join(parts[1:], " b/")
			} else {
				currentFile = "unknown"
			}
			result = append(result, "\n"+currentFile)
			added, removed = 0, 0
			inHunk = false
			hunkShown = 0
		case strings.HasPrefix(line, "@@"):
			flushSkip()
			inHunk = true
			hunkShown = 0
			result = append(result, "  "+line)
		case inHunk:
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				added++
				if hunkShown < maxHunkLine {
					result = append(result, "  "+line)
					hunkShown++
				} else {
					hunkSkipped++
				}
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				removed++
				if hunkShown < maxHunkLine {
					result = append(result, "  "+line)
					hunkShown++
				} else {
					hunkSkipped++
				}
			case hunkShown < maxHunkLine && !strings.HasPrefix(line, "\\"):
				if hunkShown > 0 {
					result = append(result, "  "+line)
					hunkShown++
				}
			}
		}
		if len(result) >= maxLines {
			result = append(result, "\n... (more changes truncated)")
			wasTruncated = true
			break
		}
	}

	flushSkip()
	flushCounts()
	if wasTruncated {
		result = append(result, "[full diff: rtk git diff --no-compact]")
	}
	return strings.Join(result, "\n")
}

// ---- git status -----------------------------------------------------------

var (
	rtkReStatusBranch    = regexp.MustCompile(`^On branch (\S+)`)
	rtkReStatusHash      = regexp.MustCompile(`^##\s*`)
	rtkReStatusPorcelain = regexp.MustCompile(`^[ MADRCU?!][ MADRCU?!] `)
	rtkReStatusLongForm  = regexp.MustCompile(`^\s*(modified|new file|deleted|renamed|both modified):\s+(.+)$`)
)

// filterGitStatus condenses long-form and porcelain git status output into a
// staged/modified/untracked summary. Port of 9router gitStatus.js.
func filterGitStatus(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 || (len(lines) == 1 && strings.TrimSpace(lines[0]) == "") {
		return "Clean working tree"
	}

	branch := ""
	var stagedFiles, modifiedFiles, untrackedFiles []string
	staged, modified, untracked, conflicts := 0, 0, 0, 0

	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if m := rtkReStatusBranch.FindStringSubmatch(raw); m != nil {
			branch = m[1]
			continue
		}
		if strings.HasPrefix(raw, "##") {
			branch = rtkReStatusHash.ReplaceAllString(raw, "")
			continue
		}
		if len(raw) >= 3 && rtkReStatusPorcelain.MatchString(raw) {
			x, y := raw[0], raw[1]
			file := raw[3:]
			if raw[:2] == "??" {
				untracked++
				untrackedFiles = append(untrackedFiles, file)
				continue
			}
			if strings.ContainsRune("MADRC", rune(x)) {
				staged++
				stagedFiles = append(stagedFiles, file)
			} else if x == 'U' {
				conflicts++
			}
			if y == 'M' || y == 'D' {
				modified++
				modifiedFiles = append(modifiedFiles, file)
			}
			continue
		}
		if m := rtkReStatusLongForm.FindStringSubmatch(raw); m != nil {
			kind, path := m[1], strings.TrimSpace(m[2])
			switch kind {
			case "both modified":
				conflicts++
			case "modified", "deleted":
				modified++
				modifiedFiles = append(modifiedFiles, path)
			case "new file", "renamed":
				staged++
				stagedFiles = append(stagedFiles, path)
			}
			continue
		}
	}

	var b strings.Builder
	if branch != "" {
		fmt.Fprintf(&b, "* %s\n", branch)
	}
	writeStatusSection := func(prefix string, files []string, count, max int) {
		if count == 0 {
			return
		}
		fmt.Fprintf(&b, "%s%d files\n", prefix, count)
		shown := files
		if len(shown) > max {
			shown = shown[:max]
		}
		for _, f := range shown {
			b.WriteString("   " + f + "\n")
		}
		if len(files) > max {
			fmt.Fprintf(&b, "   ... +%d more\n", len(files)-max)
		}
	}
	writeStatusSection("+ Staged: ", stagedFiles, staged, 10)
	writeStatusSection("~ Modified: ", modifiedFiles, modified, 10)
	writeStatusSection("? Untracked: ", untrackedFiles, untracked, 10)
	if conflicts > 0 {
		fmt.Fprintf(&b, "conflicts: %d files\n", conflicts)
	}
	if staged == 0 && modified == 0 && untracked == 0 && conflicts == 0 {
		b.WriteString("clean - nothing to commit\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- git log --------------------------------------------------------------

var (
	rtkReLogCommitPlain  = regexp.MustCompile(`(?i)^commit [0-9a-f]{7,40}$`)
	rtkReLogCommitGraph  = regexp.MustCompile(`(?i)^[*|/\\ ]+commit [0-9a-f]{7,40}`)
	rtkReLogAuthorDate   = regexp.MustCompile(`(?i)^[*|/\\ ]*(Author|Date):`)
	rtkReLogSubject      = regexp.MustCompile(`^[*|/\\ ]*    \S`)
	rtkReLogStat         = regexp.MustCompile(`^\d+ file\w* changed`)
	rtkReLogDiffHeader   = regexp.MustCompile(`^diff --git `)
	rtkReLogGraphSHA     = regexp.MustCompile(`(?i)^[*|/\\ ]+([0-9a-f]{7,40}\s+.+)`)
	rtkReLogOneline      = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}\s+`)
	rtkReLogPureDecor    = regexp.MustCompile(`^[*|/\\ ]+$`)
	rtkReLogDecorCharSet = regexp.MustCompile(`[*|/\\]`)
)

// filterGitLog compresses `git log` output: keeps commit headers, subjects,
// Author/Date and stat lines; drops body padding, decoration and embedded
// diffs. Port of 9router gitLog.js.
func filterGitLog(text string) string {
	const maxLines = 200
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	skipped := 0
	inCommit := false
	subjectSeen := false

	pushLine := func(l string) bool {
		if len(out) < maxLines {
			out = append(out, l)
			return true
		}
		skipped++
		return false
	}

	for _, raw := range lines {
		line := strings.TrimRightFunc(raw, unicode.IsSpace)
		trimmed := strings.TrimSpace(line)

		if rtkReLogCommitPlain.MatchString(trimmed) || rtkReLogCommitGraph.MatchString(trimmed) {
			inCommit = true
			subjectSeen = false
			pushLine(line)
			continue
		}

		if inCommit {
			if rtkReLogAuthorDate.MatchString(trimmed) {
				pushLine(trimmed)
				continue
			}
			if trimmed == "" {
				continue
			}
			if !subjectSeen && rtkReLogSubject.MatchString(line) {
				pushLine("  Subject: " + trimmed)
				subjectSeen = true
				continue
			}
			if rtkReLogStat.MatchString(trimmed) {
				pushLine("  " + trimmed)
				continue
			}
			if rtkReLogDiffHeader.MatchString(trimmed) {
				pushLine("  ... diff body omitted")
				continue
			}
			continue
		}

		if m := rtkReLogGraphSHA.FindStringSubmatch(trimmed); m != nil {
			pushLine(m[1])
			continue
		}
		if rtkReLogOneline.MatchString(trimmed) {
			pushLine(trimmed)
			continue
		}
		if rtkReLogPureDecor.MatchString(trimmed) && rtkReLogDecorCharSet.MatchString(trimmed) {
			continue
		}
		pushLine(trimmed)
	}

	if skipped > 0 {
		out = append(out, fmt.Sprintf("... (%d more lines)", skipped))
	}
	result := strings.Join(out, "\n")
	if result == "" {
		return text
	}
	if len(result) > len(text) {
		return text
	}
	return result
}

// ---- grep -----------------------------------------------------------------

const rtkGrepPerFileMax = 10

// filterGrep groups "file:lineno:content" lines by file, keeping the first 10
// matches per file. Port of 9router grep.js (Rust grep_wrapper).
func filterGrep(input string) string {
	type grepMatch struct {
		lineNum string
		content string
	}
	byFile := make(map[string][]grepMatch)
	total := 0

	for _, line := range strings.Split(input, "\n") {
		first := strings.Index(line, ":")
		if first < 0 {
			continue
		}
		second := strings.Index(line[first+1:], ":")
		if second < 0 {
			continue
		}
		file := line[:first]
		lineNum := line[first+1 : first+1+second]
		content := line[first+1+second+1:]
		if !rtkReGrepNumbered.MatchString(lineNum) {
			continue
		}
		total++
		byFile[file] = append(byFile[file], grepMatch{lineNum: lineNum, content: content})
	}
	if total == 0 {
		return input
	}

	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)

	var b strings.Builder
	fmt.Fprintf(&b, "%d matches in %dF:\n\n", total, len(files))
	for _, file := range files {
		matches := byFile[file]
		fmt.Fprintf(&b, "[file] %s (%d):\n", file, len(matches))
		shown := matches
		if len(shown) > rtkGrepPerFileMax {
			shown = shown[:rtkGrepPerFileMax]
		}
		for _, m := range shown {
			fmt.Fprintf(&b, "  %4s: %s\n", m.lineNum, strings.TrimSpace(m.content))
		}
		if len(matches) > rtkGrepPerFileMax {
			fmt.Fprintf(&b, "  +%d\n", len(matches)-rtkGrepPerFileMax)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ---- find -----------------------------------------------------------------

const (
	rtkFindPerDirMax    = 10
	rtkFindTotalDirMax  = 20
	rtkSearchListPerDir = 10
	rtkSearchListDirs   = 20
)

// filterFind groups bare paths by parent directory, keeping 10 files per dir
// and 20 dirs total. Port of 9router find.js (Rust find_wrapper).
func filterFind(input string) string {
	lines := splitNonEmptyLines(input)
	if len(lines) == 0 {
		return input
	}
	byDir := make(map[string][]string)
	for _, path := range lines {
		lastSep := strings.LastIndexAny(path, "/\\")
		var dir, base string
		if lastSep == -1 {
			dir, base = ".", path
		} else {
			dir = path[:lastSep]
			if dir == "" {
				dir = "/"
			}
			base = path[lastSep+1:]
		}
		byDir[dir] = append(byDir[dir], base)
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var b strings.Builder
	fmt.Fprintf(&b, "%d files in %d dirs:\n\n", len(lines), len(dirs))
	showDirs := dirs
	if len(showDirs) > rtkFindTotalDirMax {
		showDirs = showDirs[:rtkFindTotalDirMax]
	}
	for _, dir := range showDirs {
		files := byDir[dir]
		fmt.Fprintf(&b, "%s/  (%d)\n", strings.ReplaceAll(dir, "\\", "/"), len(files))
		shown := files
		if len(shown) > rtkFindPerDirMax {
			shown = shown[:rtkFindPerDirMax]
		}
		for _, f := range shown {
			b.WriteString("  " + f + "\n")
		}
		if len(files) > rtkFindPerDirMax {
			fmt.Fprintf(&b, "  +%d\n", len(files)-rtkFindPerDirMax)
		}
	}
	if len(dirs) > rtkFindTotalDirMax {
		fmt.Fprintf(&b, "\n+%d more dirs\n", len(dirs)-rtkFindTotalDirMax)
	}
	return b.String()
}

// ---- build output ---------------------------------------------------------

var (
	rtkReCargoCont    = regexp.MustCompile(`^\s*(-->|\||\d+\s*\||=)`)
	rtkReNpmErr       = regexp.MustCompile(`(?i)^npm (ERR!|error)`)
	rtkReYarnErr      = regexp.MustCompile(`(?i)^yarn error`)
	rtkReNpmDeprec    = regexp.MustCompile(`(?i)^npm warn deprecated`)
	rtkReNpmWarn      = regexp.MustCompile(`(?i)^npm warn`)
	rtkReYarnWarn     = regexp.MustCompile(`(?i)^yarn warn`)
	rtkReErrLine      = regexp.MustCompile(`(?i)^error(\[|:)`)
	rtkReWarnLine     = regexp.MustCompile(`(?i)^warning(\[|:)`)
	rtkReErrPrefix    = regexp.MustCompile(`(?i)^ERROR:`)
	rtkReBuildFailed  = regexp.MustCompile(`(?i)^\[ERROR\]|^BUILD FAILED`)
	rtkReBrackWarn    = regexp.MustCompile(`(?i)^\[WARNING\]`)
	rtkReCompiling    = regexp.MustCompile(`(?i)^\s*Compiling\s+\S+`)
	rtkReDownloading  = regexp.MustCompile(`(?i)^\s*Downloading\s+\S+`)
	rtkReFetching     = regexp.MustCompile(`(?i)^Fetching\s+`)
	rtkReSummaryLines = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(added|removed|changed|audited|installed)\s+\d+\s+package`),
		regexp.MustCompile(`(?i)^\s*Finished\s+`),
		regexp.MustCompile(`(?i)^BUILD SUCCESS`),
		regexp.MustCompile(`(?i)^\d+\s+(vulnerabilities|packages?|warnings?|errors?)`),
		regexp.MustCompile(`(?i)^Successfully (installed|built)`),
		regexp.MustCompile(`(?i)^To address .* issues`),
		regexp.MustCompile(`(?i)packages are looking for funding`),
	}
)

// filterBuildOutput keeps errors, warnings and the final summary from
// npm/yarn/cargo/maven build logs; drops progress lines. Port of 9router
// buildOutput.js.
func filterBuildOutput(input string) string {
	const deprecationKeep = 3
	const warningKeep = 5

	var errors, warnings, deprecations []string
	summary := ""
	compilingCount, downloadingCount := 0, 0
	inCargoError := false

	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)

		if inCargoError {
			if trimmed == "" {
				inCargoError = false
				continue
			}
			if rtkReCargoCont.MatchString(line) {
				errors = append(errors, line)
				continue
			}
			inCargoError = false
		}
		if trimmed == "" {
			continue
		}

		switch {
		case rtkReNpmErr.MatchString(trimmed) || rtkReYarnErr.MatchString(trimmed):
			errors = append(errors, line)
			continue
		case rtkReNpmDeprec.MatchString(trimmed):
			deprecations = append(deprecations, line)
			continue
		case rtkReNpmWarn.MatchString(trimmed) || rtkReYarnWarn.MatchString(trimmed):
			warnings = append(warnings, line)
			continue
		case rtkReErrLine.MatchString(trimmed) || strings.HasPrefix(trimmed, "error -->"):
			errors = append(errors, line)
			inCargoError = true
			continue
		case rtkReWarnLine.MatchString(trimmed) || strings.HasPrefix(trimmed, "warning -->"):
			warnings = append(warnings, line)
			inCargoError = true
			continue
		case rtkReErrPrefix.MatchString(trimmed):
			errors = append(errors, line)
			continue
		case rtkReBuildFailed.MatchString(trimmed):
			errors = append(errors, line)
			continue
		case rtkReBrackWarn.MatchString(trimmed):
			warnings = append(warnings, line)
			continue
		case rtkReCompiling.MatchString(trimmed):
			compilingCount++
			continue
		case rtkReDownloading.MatchString(trimmed) || rtkReFetching.MatchString(trimmed):
			downloadingCount++
			continue
		}
		isSummary := false
		for _, re := range rtkReSummaryLines {
			if re.MatchString(trimmed) {
				isSummary = true
				break
			}
		}
		if !isSummary && (strings.HasPrefix(trimmed, "Run `npm audit`") || strings.HasPrefix(trimmed, "Run `npm fund`")) {
			isSummary = true
		}
		if isSummary {
			if summary == "" {
				summary = line
			} else {
				summary += "\n" + line
			}
		}
	}

	var b strings.Builder
	keptDep := deprecations
	if len(keptDep) > deprecationKeep {
		keptDep = keptDep[:deprecationKeep]
	}
	for _, d := range keptDep {
		b.WriteString(d + "\n")
	}
	if len(deprecations) > deprecationKeep {
		fmt.Fprintf(&b, "... +%d more deprecated packages\n", len(deprecations)-deprecationKeep)
	}
	if compilingCount > 0 {
		fmt.Fprintf(&b, "Compiled %d packages\n", compilingCount)
	}
	if downloadingCount > 0 {
		fmt.Fprintf(&b, "Downloaded %d packages\n", downloadingCount)
	}
	for _, e := range errors {
		b.WriteString(e + "\n")
	}
	keptWarn := warnings
	if len(keptWarn) > warningKeep {
		keptWarn = keptWarn[:warningKeep]
	}
	for _, w := range keptWarn {
		b.WriteString(w + "\n")
	}
	if len(warnings) > warningKeep {
		fmt.Fprintf(&b, "... +%d more warnings\n", len(warnings)-warningKeep)
	}
	if summary != "" {
		b.WriteString(summary + "\n")
	}

	result := strings.TrimRight(b.String(), "\n")
	if result == "" {
		return input
	}
	return result
}

// ---- ls -------------------------------------------------------------------

var (
	rtkReLSDate = regexp.MustCompile(`\s+(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+(\d{4}|\d{2}:\d{2})\s+`)
	rtkLSNoise  = []string{
		"node_modules", ".git", "target", "__pycache__",
		".next", "dist", "build", ".cache", ".turbo",
		".vercel", ".pytest_cache", ".mypy_cache", ".tox",
		".venv", "venv", "env", "coverage", ".nyc_output",
		".DS_Store", "Thumbs.db", ".idea", ".vscode", ".vs",
		"*.egg-info", ".eggs",
	}
	rtkLSSummaryTop = 5
)

type rtkLSLine struct {
	fileType string
	size     int
	name     string
}

func rtkParseLSLine(line string) (rtkLSLine, bool) {
	loc := rtkReLSDate.FindStringSubmatchIndex(line)
	if loc == nil {
		return rtkLSLine{}, false
	}
	name := line[loc[1]:]
	beforeParts := strings.Fields(line[:loc[0]])
	if len(beforeParts) < 4 {
		return rtkLSLine{}, false
	}
	perms := beforeParts[0]
	fileType := ""
	if perms != "" {
		fileType = perms[:1]
	}
	size := 0
	for i := len(beforeParts) - 1; i >= 0; i-- {
		if n, err := strconv.Atoi(beforeParts[i]); err == nil && strconv.Itoa(n) == beforeParts[i] {
			size = n
			break
		}
	}
	return rtkLSLine{fileType: fileType, size: size, name: name}, true
}

func rtkHumanSize(bytes int) string {
	switch {
	case bytes >= 1_048_576:
		return fmt.Sprintf("%.1fM", float64(bytes)/1_048_576)
	case bytes >= 1024:
		return fmt.Sprintf("%.1fK", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// filterLS condenses `ls -la` output to names/sizes plus an extension summary.
// Port of 9router ls.js (Rust compact_ls).
func filterLS(input string) string {
	var dirs []string
	var fileNames, fileSizes []string
	extCount := make(map[string]int)
	var extOrder []string

	for _, line := range strings.Split(input, "\n") {
		if strings.HasPrefix(line, "total ") || len(line) == 0 {
			continue
		}
		parsed, ok := rtkParseLSLine(line)
		if !ok {
			continue
		}
		if parsed.name == "." || parsed.name == ".." {
			continue
		}
		noise := false
		for _, n := range rtkLSNoise {
			if parsed.name == n {
				noise = true
				break
			}
		}
		if noise {
			continue
		}
		switch parsed.fileType {
		case "d":
			dirs = append(dirs, parsed.name)
		case "-", "l":
			ext := "no ext"
			if dot := strings.LastIndex(parsed.name, "."); dot > 0 {
				ext = parsed.name[dot:]
			}
			if _, seen := extCount[ext]; !seen {
				extOrder = append(extOrder, ext)
			}
			extCount[ext]++
			fileNames = append(fileNames, parsed.name)
			fileSizes = append(fileSizes, rtkHumanSize(parsed.size))
		}
	}
	if len(dirs) == 0 && len(fileNames) == 0 {
		return input
	}

	var b strings.Builder
	for _, d := range dirs {
		b.WriteString(d + "/\n")
	}
	for i, name := range fileNames {
		fmt.Fprintf(&b, "%s  %s\n", name, fileSizes[i])
	}
	fmt.Fprintf(&b, "\nSummary: %d files, %d dirs", len(fileNames), len(dirs))
	if len(extOrder) > 0 {
		sorted := append([]string(nil), extOrder...)
		sort.SliceStable(sorted, func(i, j int) bool { return extCount[sorted[i]] > extCount[sorted[j]] })
		parts := sorted
		if len(parts) > rtkLSSummaryTop {
			parts = parts[:rtkLSSummaryTop]
		}
		phrases := make([]string, 0, len(parts))
		for _, e := range parts {
			phrases = append(phrases, fmt.Sprintf("%d %s", extCount[e], e))
		}
		b.WriteString(" (" + strings.Join(phrases, ", "))
		if len(sorted) > rtkLSSummaryTop {
			fmt.Fprintf(&b, ", +%d more", len(sorted)-rtkLSSummaryTop)
		}
		b.WriteString(")")
	}
	return b.String()
}

// ---- tree -----------------------------------------------------------------

const rtkTreeMaxLines = 200

// filterTree drops tree(1) summary lines and trailing blanks, capping output
// at 200 lines. Port of 9router tree.js.
func filterTree(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return input
	}
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "director") && strings.Contains(line, "file") {
			continue
		}
		if strings.TrimSpace(line) == "" && len(filtered) == 0 {
			continue
		}
		filtered = append(filtered, line)
	}
	for len(filtered) > 0 && strings.TrimSpace(filtered[len(filtered)-1]) == "" {
		filtered = filtered[:len(filtered)-1]
	}
	if len(filtered) > rtkTreeMaxLines {
		cut := len(filtered) - rtkTreeMaxLines
		return strings.Join(filtered[:rtkTreeMaxLines], "\n") + fmt.Sprintf("\n... +%d more lines", cut)
	}
	return strings.Join(filtered, "\n")
}

// ---- search list ----------------------------------------------------------

// filterSearchList regroups a Cursor Glob result list
// ("Result of search in '...' (total N files):" + "- path" lines) by parent
// directory. Port of 9router searchList.js.
func filterSearchList(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return input
	}
	header := lines[0]
	paths := make([]string, 0, len(lines)-1)
	for _, raw := range lines[1:] {
		t := strings.TrimSpace(raw)
		if !strings.HasPrefix(t, "- ") {
			continue
		}
		paths = append(paths, t[2:])
	}
	if len(paths) == 0 {
		return input
	}

	byDir := make(map[string][]string)
	for _, p := range paths {
		slash := strings.LastIndex(p, "/")
		dir, name := ".", p
		if slash != -1 {
			dir = p[:slash]
			if dir == "" {
				dir = "/"
			}
			name = p[slash+1:]
		}
		byDir[dir] = append(byDir[dir], name)
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%d files in %d dirs:\n\n", header, len(paths), len(dirs))
	showDirs := dirs
	if len(showDirs) > rtkSearchListDirs {
		showDirs = showDirs[:rtkSearchListDirs]
	}
	for _, dir := range showDirs {
		names := byDir[dir]
		fmt.Fprintf(&b, "%s/ (%d):\n", dir, len(names))
		shown := names
		if len(shown) > rtkSearchListPerDir {
			shown = shown[:rtkSearchListPerDir]
		}
		for _, n := range shown {
			b.WriteString("  " + n + "\n")
		}
		if len(names) > rtkSearchListPerDir {
			fmt.Fprintf(&b, "  +%d\n", len(names)-rtkSearchListPerDir)
		}
		b.WriteString("\n")
	}
	if len(dirs) > rtkSearchListDirs {
		fmt.Fprintf(&b, "+%d more dirs\n", len(dirs)-rtkSearchListDirs)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- numbered dumps -------------------------------------------------------

// filterReadNumbered truncates a numbered file dump ("N|content" lines) to
// head+tail with a marker. Port of 9router readNumbered.js.
func filterReadNumbered(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < rtkSmartTruncateMin {
		return input
	}
	return rtkHeadTailTruncate(lines, "lines truncated (file continues)")
}

// ---- dedup log ------------------------------------------------------------

const rtkDedupLineMax = 2000

// filterDedupLog collapses consecutive duplicate lines, dedupes blank streaks
// and caps output at 2000 lines. Port of 9router dedupLog.js.
func filterDedupLog(input string) string {
	var out []string
	prev := ""
	prevSet := false
	runCount := 0
	blankStreak := 0

	flushRun := func() {
		if prevSet && runCount > 1 {
			out = append(out, fmt.Sprintf("  ... (%d duplicate lines)", runCount-1))
		}
	}
	for _, line := range strings.Split(input, "\n") {
		if strings.TrimSpace(line) == "" {
			if blankStreak < 1 {
				out = append(out, line)
			}
			blankStreak++
			flushRun()
			prevSet = false
			runCount = 0
			continue
		}
		blankStreak = 0
		if prevSet && line == prev {
			runCount++
			continue
		}
		flushRun()
		out = append(out, line)
		prev, prevSet = line, true
		runCount = 1
		if len(out) >= rtkDedupLineMax {
			out = append(out, fmt.Sprintf("... (truncated at %d lines)", rtkDedupLineMax))
			return strings.Join(out, "\n")
		}
	}
	flushRun()
	return strings.Join(out, "\n")
}

// ---- smart truncate -------------------------------------------------------

const (
	rtkSmartTruncateHead = 120
	rtkSmartTruncateTail = 60
)

// filterSmartTruncate keeps head+tail lines of a large unstructured blob.
// Port of 9router smartTruncate.js (Rust smart_truncate).
func filterSmartTruncate(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < rtkSmartTruncateMin {
		return input
	}
	return rtkHeadTailTruncate(lines, "lines truncated")
}

// rtkHeadTailTruncate keeps the first/last lines and replaces the middle with
// a "... +N <marker>" line; marker describes what was cut.
func rtkHeadTailTruncate(lines []string, marker string) string {
	head := lines
	if len(head) > rtkSmartTruncateHead {
		head = head[:rtkSmartTruncateHead]
	}
	tail := lines
	if len(tail) > rtkSmartTruncateTail {
		tail = tail[len(tail)-rtkSmartTruncateTail:]
	}
	cut := len(lines) - len(head) - len(tail)
	parts := make([]string, 0, len(head)+len(tail)+1)
	parts = append(parts, head...)
	parts = append(parts, fmt.Sprintf("... +%d %s", cut, marker))
	parts = append(parts, tail...)
	return strings.Join(parts, "\n")
}

// ---- shared helpers -------------------------------------------------------

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

func splitLines(text string) []string {
	return strings.Split(text, "\n")
}
