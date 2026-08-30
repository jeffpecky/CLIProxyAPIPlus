package tokensaver

const promptSeparator = "\n\n"

func injectCaveman(root any, format, level string) bool {
	prompt := cavemanPrompt(level)
	if prompt == "" {
		return false
	}
	return injectSystemPrompt(root, format, prompt)
}

func injectPonytail(root any, format, level string) bool {
	prompt := ponytailPrompt(level)
	if prompt == "" {
		return false
	}
	return injectSystemPrompt(root, format, prompt)
}

func injectSystemPrompt(root any, format, prompt string) bool {
	m, ok := root.(map[string]any)
	if !ok || prompt == "" {
		return false
	}

	// Kiro wire shape is unique - handle directly
	if isKiroBody(m) || format == "kiro" {
		return injectKiroSystem(m, prompt)
	}

	// Format-specific dispatch BEFORE shape sniff
	switch format {
	case "claude":
		return injectClaudeSystem(m, prompt)
	case "gemini", "gemini-cli", "vertex", "antigravity":
		return injectGeminiSystem(m, prompt)
	default:
		return injectOpenAISystem(m, prompt)
	}
}

func injectOpenAISystem(m map[string]any, prompt string) bool {
	// 1. Check instructions first (Responses API string field)
	if s, ok := m["instructions"].(string); ok {
		if hasPrompt(s, prompt) {
			return false // Already present - idempotent
		}
		m["instructions"] = dedupStringAppend(s, prompt)
		return true
	}

	// 2. Dispatch by array key: messages[] vs input[]
	items, key, ok := firstArray(m, "messages", "input")
	if !ok {
		return false
	}

	// 3. Check if already present (idempotent check)
	if key == "input" {
		if containsPromptInResponsesInput(items, prompt) {
			return false
		}
	} else {
		if containsPromptInChatMessages(items, prompt) {
			return false
		}
	}

	// 4. Find existing system/developer message
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "system" || role == "developer" {
			appendMessageContent(msg, prompt, key == "input")
			return true
		}
	}

	// 5. Create new system message at index 0
	m[key] = append([]any{map[string]any{"role": "system", "content": prompt}}, items...)
	return true
}

func injectClaudeSystem(m map[string]any, prompt string) bool {
	switch sys := m["system"].(type) {
	case string:
		// Check if already present (idempotent)
		if hasPrompt(sys, prompt) {
			return false
		}
		m["system"] = dedupStringAppend(sys, prompt)
		return true
	case []any:
		// Check if already present in array
		for _, block := range sys {
			if blockMap, ok := block.(map[string]any); ok {
				if text, ok := blockMap["text"].(string); ok && text == prompt {
					return false // Already has exact block
				}
			}
		}

		// Insert BEFORE last cache_control block to keep injection inside cached prefix
		newBlock := map[string]any{"type": "text", "text": prompt}
		lastCacheIdx := -1
		for i := len(sys) - 1; i >= 0; i-- {
			if blockMap, ok := sys[i].(map[string]any); ok {
				if _, hasCache := blockMap["cache_control"]; hasCache {
					lastCacheIdx = i
					break
				}
			}
		}

		if lastCacheIdx >= 0 {
			// Insert before cache_control marker
			newSys := make([]any, 0, len(sys)+1)
			newSys = append(newSys, sys[:lastCacheIdx]...)
			newSys = append(newSys, newBlock)
			newSys = append(newSys, sys[lastCacheIdx:]...)
			m["system"] = newSys
		} else {
			// No cache markers - append to end
			m["system"] = append(sys, newBlock)
		}
		return true
	default:
		// nil or other type - set as string
		m["system"] = prompt
		return true
	}
}

func injectGeminiSystem(m map[string]any, prompt string) bool {
	target := m
	if req, ok := m["request"].(map[string]any); ok {
		target = req
	}

	// Handle both snake_case and camelCase
	key := "systemInstruction"
	if _, ok := target["system_instruction"]; ok {
		key = "system_instruction"
	}

	if sys, ok := target[key].(map[string]any); ok {
		if parts, ok := sys["parts"].([]any); ok {
			// Check if already present (idempotent)
			for _, part := range parts {
				if partMap, ok := part.(map[string]any); ok {
					if text, ok := partMap["text"].(string); ok && text == prompt {
						return false // Already has it
					}
				}
			}
			// Append new part
			sys["parts"] = append(parts, map[string]any{"text": prompt})
			return true
		}
	}

	// Create new systemInstruction
	target[key] = map[string]any{"parts": []any{map[string]any{"text": prompt}}}
	return true
}

func firstArray(m map[string]any, keys ...string) ([]any, string, bool) {
	for _, key := range keys {
		items, ok := m[key].([]any)
		if ok {
			return items, key, true
		}
	}
	return nil, "", false
}

func appendMessageContent(msg map[string]any, prompt string, responses bool) {
	switch content := msg["content"].(type) {
	case string:
		msg["content"] = appendPromptString(content, prompt)
	case []any:
		blockType := "text"
		if responses {
			blockType = "input_text"
		}
		msg["content"] = append(content, map[string]any{"type": blockType, "text": prompt})
	default:
		msg["content"] = prompt
	}
}

func appendPromptString(base, prompt string) string {
	// Use idempotent deduplication to prevent duplicates
	return dedupStringAppend(base, prompt)
}

// containsPromptInChatMessages checks if prompt exists in any system/developer message (Chat format)
func containsPromptInChatMessages(items []any, prompt string) bool {
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}

		// Check string content
		if content, ok := msg["content"].(string); ok {
			if hasPrompt(content, prompt) {
				return true
			}
		}

		// Check array content
		if contentArray, ok := msg["content"].([]any); ok {
			if extractTextFromContentParts(contentArray, prompt) {
				return true
			}
		}
	}
	return false
}

// containsPromptInResponsesInput checks if prompt exists in any MESSAGE item (Responses format)
func containsPromptInResponsesInput(items []any, prompt string) bool {
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Responses format requires type:"message" check
		msgType, _ := msg["type"].(string)
		if msgType != "message" {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}

		// Check string content
		if content, ok := msg["content"].(string); ok {
			if hasPrompt(content, prompt) {
				return true
			}
		}

		// Check array content
		if contentArray, ok := msg["content"].([]any); ok {
			if extractTextFromContentParts(contentArray, prompt) {
				return true
			}
		}
	}
	return false
}
