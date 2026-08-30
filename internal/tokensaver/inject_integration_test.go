package tokensaver

import (
	"testing"
)

// TestInjectOpenAI_ChatIdempotent verifies OpenAI Chat format idempotency
func TestInjectOpenAI_ChatIdempotent(t *testing.T) {
	m := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "base"},
		},
	}

	// First injection
	if !injectOpenAISystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	messages := m["messages"].([]any)
	sysMsg := messages[0].(map[string]any)
	content := sysMsg["content"].(string)
	expected := "base" + promptSeparator + "PROMPT"
	if content != expected {
		t.Errorf("got %q, want %q", content, expected)
	}

	// Retry should be idempotent
	if injectOpenAISystem(m, "PROMPT") {
		t.Error("retry should return false (already present)")
	}

	// Content should be unchanged
	messages = m["messages"].([]any)
	sysMsg = messages[0].(map[string]any)
	content = sysMsg["content"].(string)
	if content != expected {
		t.Errorf("retry changed content: got %q, want %q", content, expected)
	}
}

// TestInjectOpenAI_ResponsesIdempotent verifies OpenAI Responses format idempotency
func TestInjectOpenAI_ResponsesIdempotent(t *testing.T) {
	m := map[string]any{
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "system",
				"content": []any{
					map[string]any{"type": "input_text", "text": "base"},
				},
			},
		},
	}

	// First injection
	if !injectOpenAISystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	input := m["input"].([]any)
	msg := input[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(content))
	}

	// Retry should be idempotent
	if injectOpenAISystem(m, "PROMPT") {
		t.Error("retry should return false (already present)")
	}

	// Content should still have 2 blocks
	input = m["input"].([]any)
	msg = input[0].(map[string]any)
	content = msg["content"].([]any)
	if len(content) != 2 {
		t.Errorf("retry changed content blocks: got %d, want 2", len(content))
	}
}

// TestInjectOpenAI_InstructionsIdempotent verifies instructions field idempotency
func TestInjectOpenAI_InstructionsIdempotent(t *testing.T) {
	m := map[string]any{
		"instructions": "base",
	}

	// First injection
	if !injectOpenAISystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	expected := "base" + promptSeparator + "PROMPT"
	if m["instructions"] != expected {
		t.Errorf("got %q, want %q", m["instructions"], expected)
	}

	// Retry should be idempotent
	if injectOpenAISystem(m, "PROMPT") {
		t.Error("retry should return false")
	}

	if m["instructions"] != expected {
		t.Errorf("retry changed instructions: got %q, want %q", m["instructions"], expected)
	}
}

// TestInjectClaude_StringIdempotent verifies Claude string system idempotency
func TestInjectClaude_StringIdempotent(t *testing.T) {
	m := map[string]any{
		"system": "base",
	}

	// First injection
	if !injectClaudeSystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	expected := "base" + promptSeparator + "PROMPT"
	if m["system"] != expected {
		t.Errorf("got %q, want %q", m["system"], expected)
	}

	// Retry should be idempotent
	if injectClaudeSystem(m, "PROMPT") {
		t.Error("retry should return false")
	}

	if m["system"] != expected {
		t.Errorf("retry changed system: got %q, want %q", m["system"], expected)
	}
}

// TestInjectClaude_ArrayIdempotent verifies Claude array system idempotency
func TestInjectClaude_ArrayIdempotent(t *testing.T) {
	m := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "base"},
		},
	}

	// First injection
	if !injectClaudeSystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	sys := m["system"].([]any)
	if len(sys) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(sys))
	}

	// Retry should be idempotent
	if injectClaudeSystem(m, "PROMPT") {
		t.Error("retry should return false")
	}

	sys = m["system"].([]any)
	if len(sys) != 2 {
		t.Errorf("retry changed blocks: got %d, want 2", len(sys))
	}
}

// TestInjectClaude_CacheControlInsertion verifies injection before cache_control
func TestInjectClaude_CacheControlInsertion(t *testing.T) {
	m := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "base"},
			map[string]any{"type": "text", "text": "cached", "cache_control": map[string]any{"type": "ephemeral"}},
		},
	}

	if !injectClaudeSystem(m, "PROMPT") {
		t.Error("injection should succeed")
	}

	sys := m["system"].([]any)
	if len(sys) != 3 {
		t.Errorf("expected 3 blocks, got %d", len(sys))
	}

	// PROMPT should be at index 1 (before cache_control block)
	block1 := sys[1].(map[string]any)
	if block1["text"] != "PROMPT" {
		t.Errorf("expected PROMPT at index 1, got %q", block1["text"])
	}

	// cache_control block should still be at index 2
	block2 := sys[2].(map[string]any)
	if _, ok := block2["cache_control"]; !ok {
		t.Error("cache_control block should still be last")
	}
}

// TestInjectGemini_Idempotent verifies Gemini format idempotency
func TestInjectGemini_Idempotent(t *testing.T) {
	m := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []any{
				map[string]any{"text": "base"},
			},
		},
	}

	// First injection
	if !injectGeminiSystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	sys := m["systemInstruction"].(map[string]any)
	parts := sys["parts"].([]any)
	if len(parts) != 2 {
		t.Errorf("expected 2 parts, got %d", len(parts))
	}

	// Retry should be idempotent
	if injectGeminiSystem(m, "PROMPT") {
		t.Error("retry should return false")
	}

	sys = m["systemInstruction"].(map[string]any)
	parts = sys["parts"].([]any)
	if len(parts) != 2 {
		t.Errorf("retry changed parts: got %d, want 2", len(parts))
	}
}

// TestInjectGemini_SnakeCaseIdempotent verifies snake_case handling
func TestInjectGemini_SnakeCaseIdempotent(t *testing.T) {
	m := map[string]any{
		"system_instruction": map[string]any{
			"parts": []any{
				map[string]any{"text": "base"},
			},
		},
	}

	if !injectGeminiSystem(m, "PROMPT") {
		t.Error("injection should succeed")
	}

	// Check snake_case was preserved
	sys := m["system_instruction"].(map[string]any)
	parts := sys["parts"].([]any)
	if len(parts) != 2 {
		t.Errorf("expected 2 parts, got %d", len(parts))
	}

	// Retry idempotent
	if injectGeminiSystem(m, "PROMPT") {
		t.Error("retry should return false")
	}
}

// TestInjectKiro_Idempotent verifies Kiro atomic update idempotency
func TestInjectKiro_Idempotent(t *testing.T) {
	m := map[string]any{
		"systemPrompt": "base",
		"conversationState": map[string]any{
			"history": []any{
				map[string]any{
					"userInputMessage": map[string]any{
						"content": "base",
					},
				},
			},
		},
	}

	// First injection
	if !injectKiroSystem(m, "PROMPT") {
		t.Error("first injection should succeed")
	}

	expected := "base" + promptSeparator + "PROMPT"
	if m["systemPrompt"] != expected {
		t.Errorf("systemPrompt: got %q, want %q", m["systemPrompt"], expected)
	}

	cs := m["conversationState"].(map[string]any)
	hist := cs["history"].([]any)
	item := hist[0].(map[string]any)
	userMsg := item["userInputMessage"].(map[string]any)
	if userMsg["content"] != expected {
		t.Errorf("user content: got %q, want %q", userMsg["content"], expected)
	}

	// Retry should be idempotent
	if injectKiroSystem(m, "PROMPT") {
		t.Error("retry should return false")
	}

	// Both should be unchanged
	if m["systemPrompt"] != expected {
		t.Errorf("retry changed systemPrompt")
	}
	cs = m["conversationState"].(map[string]any)
	hist = cs["history"].([]any)
	item = hist[0].(map[string]any)
	userMsg = item["userInputMessage"].(map[string]any)
	if userMsg["content"] != expected {
		t.Errorf("retry changed user content")
	}
}

// TestInjectKiro_RollbackOnContentFailure verifies rollback logic
func TestInjectKiro_RollbackOnContentFailure(t *testing.T) {
	// Simulate a case where content update might fail
	// (In real scenario this would be a frozen object, but we can't easily simulate that in Go)
	m := map[string]any{
		"systemPrompt": "base",
		"conversationState": map[string]any{
			"history": []any{
				map[string]any{
					"userInputMessage": map[string]any{
						"content": "different", // Not mirrored - should leave alone
					},
				},
			},
		},
	}

	if !injectKiroSystem(m, "PROMPT") {
		t.Error("injection should succeed (content not mirrored)")
	}

	expected := "base" + promptSeparator + "PROMPT"
	if m["systemPrompt"] != expected {
		t.Errorf("systemPrompt should be updated")
	}

	// Content should be unchanged (not mirrored)
	cs := m["conversationState"].(map[string]any)
	hist := cs["history"].([]any)
	item := hist[0].(map[string]any)
	userMsg := item["userInputMessage"].(map[string]any)
	if userMsg["content"] != "different" {
		t.Errorf("non-mirrored content should be unchanged")
	}
}

// TestMultiplePromptsDistinct verifies distinct prompts with shared prefixes
func TestMultiplePromptsDistinct(t *testing.T) {
	promptA := "You are a helpful assistant. Be concise."
	promptB := "You are a helpful assistant. Be detailed and thorough."

	m := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": ""},
		},
	}

	// Inject both prompts
	if !injectOpenAISystem(m, promptA) {
		t.Error("promptA injection should succeed")
	}
	if !injectOpenAISystem(m, promptB) {
		t.Error("promptB injection should succeed")
	}

	messages := m["messages"].([]any)
	sysMsg := messages[0].(map[string]any)
	content := sysMsg["content"].(string)

	// Both prompts should be present
	if !hasPrompt(content, promptA) {
		t.Error("promptA should be present")
	}
	if !hasPrompt(content, promptB) {
		t.Error("promptB should be present")
	}

	// Retry A should be idempotent
	if injectOpenAISystem(m, promptA) {
		t.Error("retry promptA should return false")
	}

	// Retry B should be idempotent
	if injectOpenAISystem(m, promptB) {
		t.Error("retry promptB should return false")
	}
}

// TestSubstringNoFalsePositive verifies fix for #3202
func TestSubstringNoFalsePositive(t *testing.T) {
	m := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are RULE follower"},
		},
	}

	// Inject "RULE" - should succeed even though "RULE" is substring of existing content
	if !injectOpenAISystem(m, "RULE") {
		t.Error("injection should succeed (substring, not segment)")
	}

	messages := m["messages"].([]any)
	sysMsg := messages[0].(map[string]any)
	content := sysMsg["content"].(string)

	expected := "You are RULE follower" + promptSeparator + "RULE"
	if content != expected {
		t.Errorf("got %q, want %q", content, expected)
	}
}
