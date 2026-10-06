package tokensaver

func applyRTK(root any) int {
	m, ok := root.(map[string]any)
	if !ok {
		return 0
	}
	// Kiro bodies carry tool results under conversationState (9router
	// compressKiroFormat); they never carry a top-level messages array.
	if state, ok := m["conversationState"].(map[string]any); ok {
		return compressKiroRTK(state)
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

// compressKiroRTK walks
// conversationState.{history[],currentMessage}.userInputMessage.
// userInputMessageContext.toolResults[].content[].text and compresses each
// text blob, skipping error tool results.
func compressKiroRTK(state map[string]any) int {
	var items []any
	if history, ok := state["history"].([]any); ok {
		items = append(items, history...)
	}
	if current, ok := state["currentMessage"]; ok && current != nil {
		items = append(items, current)
	}
	hits := 0
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		um, ok := msg["userInputMessage"].(map[string]any)
		if !ok {
			continue
		}
		ctx, ok := um["userInputMessageContext"].(map[string]any)
		if !ok {
			continue
		}
		toolResults, ok := ctx["toolResults"].([]any)
		if !ok {
			continue
		}
		for _, trItem := range toolResults {
			tr, ok := trItem.(map[string]any)
			if !ok || tr["status"] == "error" {
				continue
			}
			parts, ok := tr["content"].([]any)
			if !ok {
				continue
			}
			for _, partItem := range parts {
				part, ok := partItem.(map[string]any)
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

// rtkMinCompressSize is CLIProxy's per-blob gate. 9router uses 500 bytes, but
// CLIProxy historically compresses from 40; the shrink-or-reject safety check
// below keeps small blobs from ever growing.
const rtkMinCompressSize = 40

// compressToolText runs the autodetect filter suite over one tool_result
// blob. Port of 9router index.js compressText: size gates, filter selection,
// panic-safe apply, and a never-grow safety net.
func compressToolText(text string) (string, bool) {
	if len(text) < rtkMinCompressSize || len(text) > rtkRawCap {
		return text, false
	}
	filter := autoDetectFilter(text)
	if filter == nil {
		return text, false
	}
	out := safeApplyRTK(filter, text)
	if out == "" || len(out) >= len(text) {
		return text, false
	}
	return out, true
}

// safeApplyRTK mirrors 9router safeApply: a panicking filter passes the raw
// input through instead of failing the request.
func safeApplyRTK(filter rtkFilterFunc, text string) (out string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = text
		}
	}()
	return filter(text)
}
