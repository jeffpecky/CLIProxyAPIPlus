package tokensaver

import "strings"

// promptSeparator is already declared in inject.go
// Used for exact-idempotent deduplication to prevent prefix collapse (issue #3202).

// hasPrompt checks if prompt exists as an exact SEP-delimited segment in haystack.
// This prevents the prefix-collapse bug where "You are RULE follower" would
// incorrectly match the substring "RULE".
//
// Examples:
//   hasPrompt("base", "base") = true (exact match)
//   hasPrompt("base\n\nRULE", "RULE") = true (segment match)
//   hasPrompt("You are RULE follower", "RULE") = false (substring, not segment)
//   hasPrompt("X"*150+"_A", "X"*150+"_B") = false (distinct despite 150-char prefix)
//
// This fixes issue #3202 where distinct prompts sharing a long prefix were collapsed.
func hasPrompt(haystack, prompt string) bool {
	if haystack == "" || prompt == "" {
		return false
	}

	// Fast path: exact match
	if haystack == prompt {
		return true
	}

	// Split by separator and check for exact segment match
	segments := strings.Split(haystack, promptSeparator)
	for _, seg := range segments {
		if seg == prompt {
			return true
		}
	}

	return false
}

// dedupStringAppend appends prompt to curr only if not already present as a segment.
// Idempotent: calling multiple times with the same prompt produces no duplicates.
//
// Examples:
//   dedupStringAppend("", "A") = "A"
//   dedupStringAppend("A", "B") = "A\n\nB"
//   dedupStringAppend("A\n\nB", "A") = "A\n\nB" (idempotent, A already present)
//   dedupStringAppend("A\n\nB", "C") = "A\n\nB\n\nC"
func dedupStringAppend(curr, prompt string) string {
	if curr == "" {
		return prompt
	}
	if hasPrompt(curr, prompt) {
		return curr // Already has it - idempotent
	}
	return curr + promptSeparator + prompt
}

// containsPromptInStringArray checks if prompt exists in any string within the array.
// Used for checking message content arrays where each item might be a string.
func containsPromptInStringArray(items []string, prompt string) bool {
	for _, item := range items {
		if hasPrompt(item, prompt) {
			return true
		}
	}
	return false
}

// extractTextFromContentParts extracts all text fields from content part arrays.
// Handles both OpenAI ({type:"text", text:"..."}) and Responses ({type:"input_text", text:"..."}) formats.
func extractTextFromContentParts(parts []any, prompt string) bool {
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}

		// Check text field
		if text, ok := partMap["text"].(string); ok {
			if hasPrompt(text, prompt) {
				return true
			}
		}
	}
	return false
}
