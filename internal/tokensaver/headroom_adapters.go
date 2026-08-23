package tokensaver

import "encoding/json"

type headroomProjection struct {
	messages []any
	apply    func([]any) (map[string]any, string)
}

func projectHeadroomMessages(root map[string]any, format string) (*headroomProjection, string) {
	switch format {
	case "claude":
		return projectClaudeHeadroom(root)
	case "openai-response":
		return projectResponsesHeadroom(root)
	case "kiro":
		return projectKiroHeadroom(root)
	default:
		if messages, ok := root["messages"].([]any); ok && len(messages) > 0 {
			return directHeadroomProjection(root, "messages", messages), ""
		}
		if input, ok := root["input"].([]any); ok && len(input) > 0 {
			return directHeadroomProjection(root, "input", input), ""
		}
		return nil, "messages or input missing"
	}
}

func directHeadroomProjection(root map[string]any, key string, messages []any) *headroomProjection {
	return &headroomProjection{messages: messages, apply: func(compressed []any) (map[string]any, string) {
		if !messagesStructurallyMatch(messages, compressed) {
			return nil, "compressed message structure changed"
		}
		next := cloneObject(root)
		next[key] = compressed
		return next, ""
	}}
}

func projectClaudeHeadroom(root map[string]any) (*headroomProjection, string) {
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, "claude messages missing"
	}
	return &headroomProjection{messages: messages, apply: func(compressed []any) (map[string]any, string) {
		if !messagesStructurallyMatch(messages, compressed) {
			return nil, "compressed Claude message structure changed"
		}
		next := cloneObject(root)
		next["messages"] = compressed
		return next, ""
	}}, ""
}

func projectResponsesHeadroom(root map[string]any) (*headroomProjection, string) {
	input, ok := root["input"].([]any)
	if !ok || len(input) == 0 {
		return nil, "responses input missing"
	}
	instructions, hasInstructions := root["instructions"].(string)
	messages := make([]any, 0, len(input)+1)
	if hasInstructions && instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	for _, item := range input {
		message, ok := item.(map[string]any)
		if !ok || message["type"] != "message" {
			return nil, "unsafe Responses input item"
		}
		messages = append(messages, map[string]any{"role": message["role"], "content": message["content"]})
	}
	return &headroomProjection{messages: messages, apply: func(compressed []any) (map[string]any, string) {
		if !messagesStructurallyMatch(messages, compressed) {
			return nil, "compressed Responses message structure changed"
		}
		messageOffset := 0
		nextInstructions := instructions
		if hasInstructions && instructions != "" {
			message, ok := compressed[0].(map[string]any)
			if !ok {
				return nil, "compressed Responses instructions invalid"
			}
			text, ok := messageText(message["content"])
			if !ok {
				return nil, "compressed Responses instructions missing"
			}
			nextInstructions = text
			messageOffset = 1
		}
		restored := make([]any, len(input))
		for i, item := range input {
			original := cloneObject(item.(map[string]any))
			message, ok := compressed[i+messageOffset].(map[string]any)
			if !ok {
				return nil, "compressed Responses message invalid"
			}
			original["content"] = message["content"]
			restored[i] = original
		}
		next := cloneObject(root)
		if hasInstructions && instructions != "" {
			next["instructions"] = nextInstructions
		}
		next["input"] = restored
		return next, ""
	}}, ""
}

type kiroTextTarget struct {
	role string
	path []any
}

func projectKiroHeadroom(root map[string]any) (*headroomProjection, string) {
	messages, targets := collectKiroMessages(root)
	if len(messages) == 0 {
		return nil, "Kiro messages missing"
	}
	return &headroomProjection{messages: messages, apply: func(compressed []any) (map[string]any, string) {
		if !messagesStructurallyMatch(messages, compressed) {
			return nil, "compressed Kiro message structure changed"
		}
		next := deepCopyObject(root)
		for i, target := range targets {
			message := compressed[i].(map[string]any)
			text, ok := messageText(message["content"])
			if !ok || !setStringPath(next, target.path, text) {
				return nil, "compressed Kiro text missing"
			}
		}
		return next, ""
	}}, ""
}

func collectKiroMessages(root map[string]any) ([]any, []kiroTextTarget) {
	state, _ := root["conversationState"].(map[string]any)
	if state == nil {
		return nil, nil
	}
	messages := make([]any, 0)
	targets := make([]kiroTextTarget, 0)
	visit := func(item map[string]any, prefix []any) {
		if user, ok := item["userInputMessage"].(map[string]any); ok {
			if text, ok := user["systemInstruction"].(string); ok {
				messages = append(messages, map[string]any{"role": "system", "content": text})
				targets = append(targets, kiroTextTarget{role: "system", path: appendPath(prefix, "userInputMessage", "systemInstruction")})
			}
			if text, ok := user["content"].(string); ok {
				messages = append(messages, map[string]any{"role": "user", "content": text})
				targets = append(targets, kiroTextTarget{role: "user", path: appendPath(prefix, "userInputMessage", "content")})
			}
			if context, ok := user["userInputMessageContext"].(map[string]any); ok {
				if results, ok := context["toolResults"].([]any); ok {
					for resultIndex, rawResult := range results {
						result, ok := rawResult.(map[string]any)
						if !ok || result["status"] == "error" {
							continue
						}
						parts, _ := result["content"].([]any)
						for partIndex, rawPart := range parts {
							part, ok := rawPart.(map[string]any)
							text, textOK := part["text"].(string)
							if !ok || !textOK {
								continue
							}
							message := map[string]any{"role": "tool", "content": text}
							if toolUseID, ok := result["toolUseId"].(string); ok && toolUseID != "" {
								message["tool_call_id"] = toolUseID
							}
							messages = append(messages, message)
							targets = append(targets, kiroTextTarget{role: "tool", path: appendPath(prefix, "userInputMessage", "userInputMessageContext", "toolResults", resultIndex, "content", partIndex, "text")})
						}
					}
				}
			}
		}
		if assistant, ok := item["assistantResponseMessage"].(map[string]any); ok {
			if text, ok := assistant["content"].(string); ok {
				message := map[string]any{"role": "assistant", "content": text}
				if uses, ok := assistant["toolUses"].([]any); ok {
					calls := make([]any, 0, len(uses))
					for _, rawUse := range uses {
						use, ok := rawUse.(map[string]any)
						if !ok {
							continue
						}
						arguments, _ := json.Marshal(use["input"])
						calls = append(calls, map[string]any{"id": use["toolUseId"], "type": "function", "function": map[string]any{"name": use["name"], "arguments": string(arguments)}})
					}
					if len(calls) > 0 {
						message["tool_calls"] = calls
					}
				}
				messages = append(messages, message)
				targets = append(targets, kiroTextTarget{role: "assistant", path: appendPath(prefix, "assistantResponseMessage", "content")})
			}
		}
	}
	if history, ok := state["history"].([]any); ok {
		for i, raw := range history {
			if item, ok := raw.(map[string]any); ok {
				visit(item, []any{"conversationState", "history", i})
			}
		}
	}
	if current, ok := state["currentMessage"].(map[string]any); ok {
		visit(current, []any{"conversationState", "currentMessage"})
	}
	return messages, targets
}

func appendPath(prefix []any, parts ...any) []any {
	path := make([]any, 0, len(prefix)+len(parts))
	path = append(path, prefix...)
	return append(path, parts...)
}

func messagesStructurallyMatch(expected, actual []any) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		a, okA := expected[i].(map[string]any)
		b, okB := actual[i].(map[string]any)
		if !okA || !okB || len(a) != len(b) {
			return false
		}
		for key, value := range a {
			other, exists := b[key]
			if !exists {
				return false
			}
			if key == "content" {
				if !textContentStructureMatches(value, other) {
					return false
				}
				continue
			}
			if !jsonEqual(value, other) {
				return false
			}
		}
	}
	return true
}

func textContentStructureMatches(expected, actual any) bool {
	switch value := expected.(type) {
	case string:
		_, ok := actual.(string)
		return ok
	case []any:
		other, ok := actual.([]any)
		if !ok || len(value) != len(other) {
			return false
		}
		for i := range value {
			if !textContentStructureMatches(value[i], other[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := actual.(map[string]any)
		if !ok || len(value) != len(other) {
			return false
		}
		for key, field := range value {
			otherField, exists := other[key]
			if !exists {
				return false
			}
			switch key {
			case "text", "content":
				if !textContentStructureMatches(field, otherField) {
					return false
				}
			default:
				if !jsonEqual(field, otherField) {
					return false
				}
			}
		}
		return true
	default:
		return jsonEqual(expected, actual)
	}
}

func messageText(content any) (string, bool) {
	if text, ok := content.(string); ok {
		return text, true
	}
	return "", false
}

func deepCopyObject(src map[string]any) map[string]any {
	raw, _ := json.Marshal(src)
	var dst map[string]any
	_ = json.Unmarshal(raw, &dst)
	return dst
}

func setStringPath(root map[string]any, path []any, value string) bool {
	var current any = root
	for i, part := range path {
		last := i == len(path)-1
		switch key := part.(type) {
		case string:
			m, ok := current.(map[string]any)
			if !ok {
				return false
			}
			if last {
				m[key] = value
				return true
			}
			current = m[key]
		case int:
			a, ok := current.([]any)
			if !ok || key < 0 || key >= len(a) {
				return false
			}
			current = a[key]
		}
	}
	return false
}
