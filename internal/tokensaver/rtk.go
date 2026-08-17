package tokensaver

func applyRTK(root any) int {
	m, ok := root.(map[string]any)
	if !ok {
		return 0
	}
	hits := 0
	if messages, _, ok := firstArray(m, "messages", "input"); ok {
		for _, item := range messages {
			msg, ok := item.(map[string]any)
			if !ok {
				continue
			}
			hits += compressMessageToolContent(msg)
		}
	}
	return hits
}

func compressMessageToolContent(msg map[string]any) int {
	if msg["type"] == "function_call_output" {
		return compressOpenAIResponsesOutput(msg)
	}
	if msg["role"] == "tool" {
		return compressContentValue(msg, "content")
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return 0
	}
	hits := 0
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || block["type"] != "tool_result" || block["is_error"] == true {
			continue
		}
		hits += compressContentValue(block, "content")
	}
	return hits
}

func compressOpenAIResponsesOutput(msg map[string]any) int {
	if hits := compressContentValue(msg, "output"); hits > 0 {
		return hits
	}
	return 0
}

func compressContentValue(m map[string]any, key string) int {
	s, ok := m[key].(string)
	if ok {
		if out, changed := compressToolText(s); changed {
			m[key] = out
			return 1
		}
		return 0
	}
	parts, ok := m[key].([]any)
	if !ok {
		return 0
	}
	hits := 0
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, ok := part["text"].(string)
		if !ok {
			continue
		}
		if out, changed := compressToolText(text); changed {
			part["text"] = out
			hits++
		}
	}
	return hits
}

func compressToolText(text string) (string, bool) {
	if len(text) < 40 {
		return text, false
	}
	for _, filter := range []func(string) (string, bool){compactGrepOutput, compactLongLineOutput} {
		out, ok := filter(text)
		if ok && out != "" && len(out) < len(text) {
			return out, true
		}
	}
	return text, false
}
