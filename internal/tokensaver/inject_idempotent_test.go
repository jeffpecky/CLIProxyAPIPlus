package tokensaver

import (
	"strings"
	"testing"
)

// TestHasPrompt_ExactMatch verifies exact string matching works
func TestHasPrompt_ExactMatch(t *testing.T) {
	if !hasPrompt("RULE", "RULE") {
		t.Error("exact match should return true")
	}
}

// TestHasPrompt_SegmentMatch verifies SEP-delimited segment matching
func TestHasPrompt_SegmentMatch(t *testing.T) {
	haystack := "base" + promptSeparator + "RULE"
	if !hasPrompt(haystack, "RULE") {
		t.Error("segment match should return true")
	}
}

// TestHasPrompt_SubstringNoMatch is the critical test for issue #3202.
// Substring matches must NOT be detected as present.
func TestHasPrompt_SubstringNoMatch(t *testing.T) {
	if hasPrompt("You are RULE follower", "RULE") {
		t.Error("substring should NOT match - this is the #3202 bug")
	}
}

// TestHasPrompt_LongPrefixDistinct tests the exact scenario from issue #3202:
// Two prompts with 150-char shared prefix must be treated as distinct.
func TestHasPrompt_LongPrefixDistinct(t *testing.T) {
	longA := strings.Repeat("X", 150) + "_A"
	longB := strings.Repeat("X", 150) + "_B"

	haystack := "base" + promptSeparator + longA

	// longB should NOT match even though it shares 150 chars with longA
	if hasPrompt(haystack, longB) {
		t.Error("distinct prompts should not match (fixes #3202)")
	}

	// longA should still match
	if !hasPrompt(haystack, longA) {
		t.Error("should match exact prompt")
	}
}

// TestHasPrompt_MultipleSegments verifies searching in multi-segment strings
func TestHasPrompt_MultipleSegments(t *testing.T) {
	haystack := "A" + promptSeparator + "B" + promptSeparator + "C"

	if !hasPrompt(haystack, "A") {
		t.Error("should find first segment")
	}
	if !hasPrompt(haystack, "B") {
		t.Error("should find middle segment")
	}
	if !hasPrompt(haystack, "C") {
		t.Error("should find last segment")
	}
	if hasPrompt(haystack, "D") {
		t.Error("should not find non-existent segment")
	}
}

// TestHasPrompt_EmptyStrings verifies empty string handling
func TestHasPrompt_EmptyStrings(t *testing.T) {
	if hasPrompt("", "anything") {
		t.Error("empty haystack should return false")
	}
	if hasPrompt("anything", "") {
		t.Error("empty prompt should return false")
	}
	if hasPrompt("", "") {
		t.Error("both empty should return false")
	}
}

// TestDedupStringAppend_FirstAppend verifies initial append
func TestDedupStringAppend_FirstAppend(t *testing.T) {
	result := dedupStringAppend("base", "PROMPT")
	expected := "base" + promptSeparator + "PROMPT"
	if result != expected {
		t.Errorf("first append failed: got %q, want %q", result, expected)
	}
}

// TestDedupStringAppend_EmptyBase verifies appending to empty string
func TestDedupStringAppend_EmptyBase(t *testing.T) {
	result := dedupStringAppend("", "PROMPT")
	if result != "PROMPT" {
		t.Errorf("append to empty failed: got %q, want %q", result, "PROMPT")
	}
}

// TestDedupStringAppend_Idempotent is the key idempotency test.
// Retrying the same prompt should not duplicate it.
func TestDedupStringAppend_Idempotent(t *testing.T) {
	result := dedupStringAppend("base", "PROMPT")
	expected := "base" + promptSeparator + "PROMPT"
	if result != expected {
		t.Errorf("first append failed: got %q, want %q", result, expected)
	}

	// Retry should be idempotent
	result2 := dedupStringAppend(result, "PROMPT")
	if result2 != expected {
		t.Errorf("retry not idempotent: got %q, want %q", result2, expected)
	}

	// Third retry
	result3 := dedupStringAppend(result2, "PROMPT")
	if result3 != expected {
		t.Errorf("third retry not idempotent: got %q, want %q", result3, expected)
	}
}

// TestDedupStringAppend_MultipleDistinct verifies multiple distinct prompts
func TestDedupStringAppend_MultipleDistinct(t *testing.T) {
	var result string

	result = dedupStringAppend(result, "A")
	result = dedupStringAppend(result, "B")
	result = dedupStringAppend(result, "A") // Retry A
	result = dedupStringAppend(result, "C")
	result = dedupStringAppend(result, "B") // Retry B

	expected := "A" + promptSeparator + "B" + promptSeparator + "C"
	if result != expected {
		t.Errorf("multiple distinct prompts failed: got %q, want %q", result, expected)
	}

	// Verify each segment is present exactly once
	segments := strings.Split(result, promptSeparator)
	if len(segments) != 3 {
		t.Errorf("expected 3 segments, got %d", len(segments))
	}

	countA := 0
	countB := 0
	countC := 0
	for _, seg := range segments {
		switch seg {
		case "A":
			countA++
		case "B":
			countB++
		case "C":
			countC++
		}
	}

	if countA != 1 || countB != 1 || countC != 1 {
		t.Errorf("expected each segment once, got A=%d B=%d C=%d", countA, countB, countC)
	}
}

// TestDedupStringAppend_LongPromptIdempotency tests idempotency with long prompts
func TestDedupStringAppend_LongPromptIdempotency(t *testing.T) {
	longPrompt := strings.Repeat("X", 200)

	result := dedupStringAppend("base", longPrompt)
	expected := "base" + promptSeparator + longPrompt
	if result != expected {
		t.Error("first append of long prompt failed")
	}

	// Retry should be idempotent
	result2 := dedupStringAppend(result, longPrompt)
	if result2 != expected {
		t.Error("retry of long prompt not idempotent")
	}
}

// TestDedupStringAppend_PrefixSimilarity verifies prompts with shared prefixes
// are treated as distinct (the core fix for #3202)
func TestDedupStringAppend_PrefixSimilarity(t *testing.T) {
	promptA := "You are a helpful assistant. Be concise."
	promptB := "You are a helpful assistant. Be detailed and thorough."

	result := dedupStringAppend("", promptA)
	result = dedupStringAppend(result, promptB)

	// Both prompts should be present
	if !hasPrompt(result, promptA) {
		t.Error("promptA should be present")
	}
	if !hasPrompt(result, promptB) {
		t.Error("promptB should be present")
	}

	// Result should have both prompts
	expected := promptA + promptSeparator + promptB
	if result != expected {
		t.Errorf("prefix-similar prompts not both present:\ngot:  %q\nwant: %q", result, expected)
	}
}

// TestExtractTextFromContentParts verifies text extraction from content arrays
func TestExtractTextFromContentParts(t *testing.T) {
	tests := []struct {
		name     string
		parts    []any
		prompt   string
		expected bool
	}{
		{
			name: "text block with prompt",
			parts: []any{
				map[string]any{"type": "text", "text": "base" + promptSeparator + "PROMPT"},
			},
			prompt:   "PROMPT",
			expected: true,
		},
		{
			name: "text block without prompt",
			parts: []any{
				map[string]any{"type": "text", "text": "base"},
			},
			prompt:   "PROMPT",
			expected: false,
		},
		{
			name: "multiple blocks, prompt in second",
			parts: []any{
				map[string]any{"type": "text", "text": "first"},
				map[string]any{"type": "text", "text": "PROMPT"},
			},
			prompt:   "PROMPT",
			expected: true,
		},
		{
			name: "input_text block (Responses format)",
			parts: []any{
				map[string]any{"type": "input_text", "text": "PROMPT"},
			},
			prompt:   "PROMPT",
			expected: true,
		},
		{
			name:     "empty parts",
			parts:    []any{},
			prompt:   "PROMPT",
			expected: false,
		},
		{
			name: "non-map items ignored",
			parts: []any{
				"string item",
				123,
				map[string]any{"type": "text", "text": "PROMPT"},
			},
			prompt:   "PROMPT",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTextFromContentParts(tt.parts, tt.prompt)
			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestContainsPromptInStringArray verifies string array checking
func TestContainsPromptInStringArray(t *testing.T) {
	tests := []struct {
		name     string
		items    []string
		prompt   string
		expected bool
	}{
		{
			name:     "prompt in first item",
			items:    []string{"PROMPT", "other"},
			prompt:   "PROMPT",
			expected: true,
		},
		{
			name:     "prompt in second item",
			items:    []string{"other", "PROMPT"},
			prompt:   "PROMPT",
			expected: true,
		},
		{
			name:     "prompt not present",
			items:    []string{"foo", "bar"},
			prompt:   "PROMPT",
			expected: false,
		},
		{
			name:     "prompt as segment in item",
			items:    []string{"base" + promptSeparator + "PROMPT"},
			prompt:   "PROMPT",
			expected: true,
		},
		{
			name:     "empty array",
			items:    []string{},
			prompt:   "PROMPT",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsPromptInStringArray(tt.items, tt.prompt)
			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// BenchmarkHasPrompt benchmarks the hasPrompt function
func BenchmarkHasPrompt(b *testing.B) {
	haystack := "segment1" + promptSeparator + "segment2" + promptSeparator + "segment3"
	prompt := "segment2"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hasPrompt(haystack, prompt)
	}
}

// BenchmarkDedupStringAppend benchmarks the dedupStringAppend function
func BenchmarkDedupStringAppend(b *testing.B) {
	base := "base"
	prompt := "PROMPT"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dedupStringAppend(base, prompt)
	}
}
