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
	if s, ok := m["instructions"].(string); ok {
		m["instructions"] = appendPromptString(s, prompt)
		return true
	}
	items, key, ok := firstArray(m, "messages", "input")
	if !ok {
		return false
	}
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
	m[key] = append([]any{map[string]any{"role": "system", "content": prompt}}, items...)
	return true
}

func injectClaudeSystem(m map[string]any, prompt string) bool {
	switch sys := m["system"].(type) {
	case string:
		m["system"] = appendPromptString(sys, prompt)
		return true
	case []any:
		m["system"] = append(sys, map[string]any{"type": "text", "text": prompt})
		return true
	default:
		m["system"] = prompt
		return true
	}
}

func injectGeminiSystem(m map[string]any, prompt string) bool {
	target := m
	if req, ok := m["request"].(map[string]any); ok {
		target = req
	}
	key := "systemInstruction"
	if _, ok := target["system_instruction"]; ok {
		key = "system_instruction"
	}
	if sys, ok := target[key].(map[string]any); ok {
		if parts, ok := sys["parts"].([]any); ok {
			sys["parts"] = append(parts, map[string]any{"text": prompt})
			return true
		}
	}
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
	if base == "" {
		return prompt
	}
	return base + promptSeparator + prompt
}
