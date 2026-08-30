package tokensaver

import "strings"

// Kiro format handlers for atomic systemPrompt + user content updates

// isKiroBody checks if the body has Kiro wire shape
func isKiroBody(m map[string]any) bool {
	if _, ok := m["systemPrompt"].(string); !ok {
		return false
	}
	cs, ok := m["conversationState"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := cs["history"].([]any); ok {
		return true
	}
	if _, ok := cs["currentMessage"].(map[string]any); ok {
		return true
	}
	return false
}

// injectKiroSystem handles Kiro format with atomic systemPrompt + user content updates.
// Kiro mirrors systemPrompt into the first user message content.
// Must update BOTH or rollback to maintain consistency.
func injectKiroSystem(m map[string]any, prompt string) bool {
	oldPrompt, _ := m["systemPrompt"].(string)

	// Check idempotency
	if oldPrompt != "" && hasPrompt(oldPrompt, prompt) {
		return false
	}

	next := dedupStringAppend(oldPrompt, prompt)

	// Find first user message in conversationState
	cs, ok := m["conversationState"].(map[string]any)
	if !ok {
		// No conversationState - just update systemPrompt
		m["systemPrompt"] = next
		return true
	}

	targetMsg := findFirstKiroUserMessage(cs)

	// Step 1: Update systemPrompt
	m["systemPrompt"] = next

	// Step 2: Update mirrored content (if exists)
	if targetMsg != nil {
		if !updateKiroUserContent(targetMsg, oldPrompt, next) {
			// Rollback on failure
			m["systemPrompt"] = oldPrompt
			return false
		}
	}

	return true
}

// findFirstKiroUserMessage finds the first user message in Kiro conversationState
func findFirstKiroUserMessage(cs map[string]any) map[string]any {
	// Check history first
	if history, ok := cs["history"].([]any); ok {
		for _, item := range history {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if userMsg, ok := itemMap["userInputMessage"].(map[string]any); ok {
				return userMsg
			}
		}
	}

	// Check currentMessage
	if current, ok := cs["currentMessage"].(map[string]any); ok {
		if userMsg, ok := current["userInputMessage"].(map[string]any); ok {
			return userMsg
		}
	}

	return nil
}

// updateKiroUserContent updates the mirrored user content with atomic rollback
func updateKiroUserContent(msg map[string]any, oldPrompt, newPrompt string) bool {
	content, ok := msg["content"].(string)
	if !ok {
		return true // No content to update
	}

	if oldPrompt == "" {
		// First injection - prepend if not already at head
		if content == newPrompt || strings.HasPrefix(content, newPrompt+promptSeparator) {
			return true // Already applied (idempotent)
		}
		if content == "" {
			msg["content"] = newPrompt
		} else {
			msg["content"] = newPrompt + promptSeparator + content
		}
		return true
	}

	// Update existing prefix
	if !strings.HasPrefix(content, oldPrompt) {
		return true // Not mirrored - leave alone
	}

	if strings.HasPrefix(content, newPrompt) {
		return true // Already updated (idempotent)
	}

	tail := content[len(oldPrompt):]
	msg["content"] = newPrompt + tail

	// Verify convergence
	updated, _ := msg["content"].(string)
	if !strings.HasPrefix(updated, newPrompt) {
		return false // Failed to converge
	}

	return true
}
