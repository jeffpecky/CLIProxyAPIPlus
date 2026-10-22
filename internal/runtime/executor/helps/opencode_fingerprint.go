package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// OpenCodeFingerprintToolNames are the tool names the OpenCode free-tier gate
// requires on every request (OpenCode v2, /inference/v1). Port of 9router
// open-sse/utils/opencodeFingerprint.js.
var OpenCodeFingerprintToolNames = []string{"read", "shell"}

// openCodeFingerprintDescription returns the verbatim tool description the
// upstream gate compares. Do not reword or reformat.
func openCodeFingerprintDescription(name string) string {
	switch name {
	case "read":
		return "Read the contents of a file or directory. Supports text files, images, and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content. Directory entries are returned one per line. Use offset and limit to read large files or directories in sections. Prefer one larger read over many small slices, and use grep to find specific content in large files."
	case "shell":
		return "Execute a shell command and return its output. Commands run on Linux using bash. Quote file paths containing spaces or special characters. Prefer dedicated tools over shell commands when possible. When output is large, the full result is saved to a file and a truncated preview is returned. Rely on automatic truncation unless filtering the output is more useful. Commands accept an optional timeout, background commands have no timeout by default. Background commands return immediately, and you will be notified when they complete."
	}
	return ""
}

// openCodeFingerprintParameters returns a fresh copy of the verbatim parameter
// schema the upstream gate compares.
func openCodeFingerprintParameters(name string) map[string]any {
	switch name {
	case "read":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "File or directory to read"},
				"offset": map[string]any{"type": "integer", "minimum": 0, "description": "The line or directory entry to start reading from (1-based)"},
				"limit":  map[string]any{"type": "integer", "minimum": 0, "description": "The maximum number of lines or directory entries to read (defaults to and capped at 2000)"},
			},
			"required":             []any{"path"},
			"additionalProperties": false,
		}
	case "shell":
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string", "description": "Shell command string to execute"},
				"workdir":    map[string]any{"type": "string", "description": "Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead."},
				"timeout":    map[string]any{"type": "integer", "minimum": 0, "description": "Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default."},
				"background": map[string]any{"type": "boolean", "description": "Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion."},
			},
			"required":             []any{"command"},
			"additionalProperties": false,
		}
	}
	return nil
}

func openCodeToolNameOf(tool any) string {
	tm, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if name, ok := tm["name"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if fn, ok := tm["function"].(map[string]any); ok {
		if name, ok := fn["name"].(string); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

func openCodeFingerprintToolKey(name string) string {
	trimmed := strings.TrimSpace(name)
	for _, candidate := range OpenCodeFingerprintToolNames {
		if candidate == trimmed {
			return trimmed
		}
	}
	return ""
}

func openCodeIsFingerprintDefinition(tool map[string]any, key string) bool {
	def := tool
	if fn, ok := tool["function"].(map[string]any); ok {
		def = fn
	}
	description, _ := def["description"].(string)
	if description != openCodeFingerprintDescription(key) {
		return false
	}
	got, errGot := json.Marshal(def["parameters"])
	want, errWant := json.Marshal(openCodeFingerprintParameters(key))
	return errGot == nil && errWant == nil && string(got) == string(want)
}

func openCodeWithToolName(tool map[string]any, name string) map[string]any {
	clone := cloneOpenCodeMap(tool)
	if fn, ok := tool["function"].(map[string]any); ok {
		nested := cloneOpenCodeMap(fn)
		nested["name"] = name
		clone["function"] = nested
		return clone
	}
	clone["name"] = name
	return clone
}

func cloneOpenCodeMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// openCodeConcealFingerprintToolNames moves caller tools off the fingerprint
// names when their definition differs from OpenCode's, so the exact definitions
// can be appended without a duplicate name. Returns alias -> original name.
func openCodeConcealFingerprintToolNames(tools []any) ([]any, map[string]string) {
	names := map[string]string{}
	if len(tools) == 0 {
		return tools, names
	}
	taken := map[string]bool{}
	for _, tool := range tools {
		if name := openCodeToolNameOf(tool); name != "" {
			taken[name] = true
		}
	}
	seen := map[string]bool{}
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		tm, ok := tool.(map[string]any)
		if !ok {
			out = append(out, tool)
			continue
		}
		key := openCodeFingerprintToolKey(openCodeToolNameOf(tool))
		if key == "" {
			out = append(out, tool)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if openCodeIsFingerprintDefinition(tm, key) {
			out = append(out, tool)
			continue
		}
		alias := key + "_client"
		for i := 2; taken[alias]; i++ {
			alias = fmt.Sprintf("%s_client%d", key, i)
		}
		taken[alias] = true
		names[alias] = key
		out = append(out, openCodeWithToolName(tm, alias))
	}
	return out, names
}

func openCodeAppendMissingFingerprintTools(tools []any, flat bool) ([]any, []string) {
	var appended []string
	for _, name := range OpenCodeFingerprintToolNames {
		present := false
		for _, tool := range tools {
			if openCodeFingerprintToolKey(openCodeToolNameOf(tool)) == name {
				present = true
				break
			}
		}
		if present {
			continue
		}
		if flat {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        name,
				"description": openCodeFingerprintDescription(name),
				"parameters":  openCodeFingerprintParameters(name),
				"strict":      false,
			})
		} else {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": openCodeFingerprintDescription(name),
					"parameters":  openCodeFingerprintParameters(name),
				},
			})
		}
		appended = append(appended, name)
	}
	return tools, appended
}

func openCodeAliasesByOriginal(names map[string]string) map[string]string {
	out := make(map[string]string, len(names))
	for alias, original := range names {
		out[original] = alias
	}
	return out
}

func openCodeRetargetToolChoice(body map[string]any, names map[string]string) {
	if len(names) == 0 {
		return
	}
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	aliases := openCodeAliasesByOriginal(names)
	if name, ok := choice["name"].(string); ok {
		if alias, found := aliases[strings.TrimSpace(name)]; found {
			next := cloneOpenCodeMap(choice)
			next["name"] = alias
			body["tool_choice"] = next
		}
		return
	}
	fn, ok := choice["function"].(map[string]any)
	if !ok {
		return
	}
	name, ok := fn["name"].(string)
	if !ok {
		return
	}
	if alias, found := aliases[strings.TrimSpace(name)]; found {
		nested := cloneOpenCodeMap(fn)
		nested["name"] = alias
		next := cloneOpenCodeMap(choice)
		next["function"] = nested
		body["tool_choice"] = next
	}
}

// openCodeAliasHistoryToolCalls renames prior-turn calls to concealed caller
// tools, so the model does not read its own earlier calls as OpenCode's tool.
func openCodeAliasHistoryToolCalls(body map[string]any, names map[string]string) {
	if len(names) == 0 {
		return
	}
	aliases := openCodeAliasesByOriginal(names)

	if input, ok := body["input"].([]any); ok {
		for i, item := range input {
			im, ok := item.(map[string]any)
			if !ok || im["type"] != "function_call" {
				continue
			}
			name, _ := im["name"].(string)
			if alias, found := aliases[name]; found {
				next := cloneOpenCodeMap(im)
				next["name"] = alias
				input[i] = next
			}
		}
	}

	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	for i, msg := range messages {
		mm, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		calls, ok := mm["tool_calls"].([]any)
		if !ok || len(calls) == 0 {
			continue
		}
		nextCalls := make([]any, len(calls))
		copy(nextCalls, calls)
		changed := false
		for j, call := range calls {
			cm, ok := call.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := cm["function"].(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			if alias, found := aliases[name]; found {
				nested := cloneOpenCodeMap(fn)
				nested["name"] = alias
				next := cloneOpenCodeMap(cm)
				next["function"] = nested
				nextCalls[j] = next
				changed = true
			}
		}
		if changed {
			next := cloneOpenCodeMap(mm)
			next["tool_calls"] = nextCalls
			messages[i] = next
		}
	}
}

// openCodeNoteUnavailableTools tells the model the appended fingerprint tools
// are placeholders. Upstream does not gate on the prompt text.
func openCodeNoteUnavailableTools(body map[string]any, flat bool, names []string) {
	if len(names) == 0 {
		return
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "`" + name + "`"
	}
	list := strings.Join(quoted, " or ")
	note := fmt.Sprintf("Do not call the %s tool; it is a placeholder that does nothing. Use the other tools you have been given instead.", list)
	if len(names) > 1 {
		note = fmt.Sprintf("Do not call the %s tools; they are placeholders that do nothing. Use the other tools you have been given instead.", list)
	}
	if flat {
		instructions, _ := body["instructions"].(string)
		if strings.TrimSpace(instructions) != "" {
			body["instructions"] = instructions + "\n\n" + note
		} else {
			body["instructions"] = note
		}
		return
	}
	if _, hasSystem := body["system"]; hasSystem {
		return
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return
	}
	if first, ok := messages[0].(map[string]any); ok {
		if role, _ := first["role"].(string); role == "system" {
			if content, ok := first["content"].(string); ok {
				next := cloneOpenCodeMap(first)
				next["content"] = content + "\n\n" + note
				body["messages"] = append([]any{next}, messages[1:]...)
				return
			}
		}
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": note}}, messages...)
}

// ApplyOpenCodeFingerprintTools aliases conflicting caller tools, appends the
// OpenCode fingerprint definitions and returns the alias -> original rename map.
// flat is true for Responses tools ({name}), false for chat tools
// ({function:{name}}).
func ApplyOpenCodeFingerprintTools(payload []byte, flat bool) ([]byte, map[string]string) {
	if len(payload) == 0 {
		return payload, nil
	}
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return payload, nil
	}

	tools, _ := body["tools"].([]any)
	tools, names := openCodeConcealFingerprintToolNames(tools)
	if tools == nil {
		tools = []any{}
	}
	tools, appended := openCodeAppendMissingFingerprintTools(tools, flat)
	body["tools"] = tools
	openCodeRetargetToolChoice(body, names)
	openCodeAliasHistoryToolCalls(body, names)
	if len(appended) > 0 {
		openCodeNoteUnavailableTools(body, flat, appended)
	}

	// Never default to "none": upstream answers tool_choice "none" with 403.
	if flat {
		if choice, ok := body["tool_choice"]; !ok || choice == nil || choice == "" {
			body["tool_choice"] = "auto"
		}
	}

	out, err := json.Marshal(body)
	if err != nil {
		return payload, names
	}
	return out, names
}

// RestoreOpenCodeToolNames maps concealed alias names back to the caller's
// original tool names in supported response/event shapes. A no-op when names is
// empty.
func RestoreOpenCodeToolNames(payload []byte, names map[string]string) []byte {
	if len(names) == 0 || len(payload) == 0 {
		return payload
	}
	prefix := ""
	data := payload
	trimmed := bytes.TrimSpace(payload)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		rest := bytes.TrimSpace(trimmed[len("data:"):])
		if len(rest) == 0 || bytes.Equal(rest, []byte("[DONE]")) {
			return payload
		}
		prefix = "data: "
		data = rest
	}
	if !json.Valid(data) {
		return payload
	}
	var node any
	if json.Unmarshal(data, &node) != nil {
		return payload
	}
	restored, changed := openCodeRestoreNode(node, names)
	if !changed {
		return payload
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(restored) != nil {
		return payload
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if prefix != "" {
		return append([]byte(prefix), out...)
	}
	return out
}

func openCodeRestoreNode(node any, names map[string]string) (any, bool) {
	switch v := node.(type) {
	case []any:
		changed := false
		for i := range v {
			next, itemChanged := openCodeRestoreNode(v[i], names)
			if itemChanged {
				v[i] = next
				changed = true
			}
		}
		return v, changed
	case map[string]any:
		return openCodeRestoreObject(v, names)
	default:
		return node, false
	}
}

func openCodeRestoreObject(obj map[string]any, names map[string]string) (any, bool) {
	changed := false

	renameOriginal := func(value any) (string, bool) {
		name, ok := value.(string)
		if !ok {
			return "", false
		}
		original, found := names[name]
		return original, found
	}

	// Claude streaming content_block_start event.
	if obj["type"] == "content_block_start" {
		if block, ok := obj["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
			if original, found := renameOriginal(block["name"]); found {
				nextBlock := cloneOpenCodeMap(block)
				nextBlock["name"] = original
				obj = cloneOpenCodeMap(obj)
				obj["content_block"] = nextBlock
				changed = true
			}
		}
	}

	// Claude non-streaming message body.
	if content, ok := obj["content"].([]any); ok {
		nextContent := make([]any, len(content))
		copy(nextContent, content)
		for i, block := range content {
			bm, ok := block.(map[string]any)
			if !ok || bm["type"] != "tool_use" {
				continue
			}
			if original, found := renameOriginal(bm["name"]); found {
				next := cloneOpenCodeMap(bm)
				next["name"] = original
				nextContent[i] = next
				if !changed {
					obj = cloneOpenCodeMap(obj)
				}
				changed = true
			}
		}
		if changed {
			obj["content"] = nextContent
		}
	}

	// OpenAI Chat Completions, streaming delta and JSON message shapes.
	if choices, ok := obj["choices"].([]any); ok {
		nextChoices := make([]any, len(choices))
		copy(nextChoices, choices)
		for i, choice := range choices {
			cm, ok := choice.(map[string]any)
			if !ok {
				continue
			}
			nextChoice := cloneOpenCodeMap(cm)
			choiceChanged := false
			for _, holder := range []string{"delta", "message"} {
				hv, ok := cm[holder].(map[string]any)
				if !ok {
					continue
				}
				calls, ok := hv["tool_calls"].([]any)
				if !ok || len(calls) == 0 {
					continue
				}
				nextCalls := make([]any, len(calls))
				copy(nextCalls, calls)
				callsChanged := false
				for j, call := range calls {
					clm, ok := call.(map[string]any)
					if !ok {
						continue
					}
					fn, ok := clm["function"].(map[string]any)
					if !ok {
						continue
					}
					if original, found := renameOriginal(fn["name"]); found {
						nextFn := cloneOpenCodeMap(fn)
						nextFn["name"] = original
						nextCall := cloneOpenCodeMap(clm)
						nextCall["function"] = nextFn
						nextCalls[j] = nextCall
						callsChanged = true
					}
				}
				if callsChanged {
					nextHolder := cloneOpenCodeMap(hv)
					nextHolder["tool_calls"] = nextCalls
					nextChoice[holder] = nextHolder
					choiceChanged = true
				}
			}
			nextChoices[i] = nextChoice
			if choiceChanged && !changed {
				obj = cloneOpenCodeMap(obj)
				changed = true
			}
			if choiceChanged {
				obj["choices"] = nextChoices
			}
		}
	}

	// OpenAI Responses final JSON body.
	if output, ok := obj["output"].([]any); ok {
		nextOutput := make([]any, len(output))
		copy(nextOutput, output)
		outputChanged := false
		for i, item := range output {
			im, ok := item.(map[string]any)
			if !ok || im["type"] != "function_call" {
				continue
			}
			if original, found := renameOriginal(im["name"]); found {
				next := cloneOpenCodeMap(im)
				next["name"] = original
				nextOutput[i] = next
				outputChanged = true
			}
		}
		if outputChanged {
			if !changed {
				obj = cloneOpenCodeMap(obj)
				changed = true
			}
			obj["output"] = nextOutput
		}
	}

	// OpenAI Responses SSE events such as response.output_item.added/done.
	if item, ok := obj["item"].(map[string]any); ok && item["type"] == "function_call" {
		if original, found := renameOriginal(item["name"]); found {
			nextItem := cloneOpenCodeMap(item)
			nextItem["name"] = original
			if !changed {
				obj = cloneOpenCodeMap(obj)
				changed = true
			}
			obj["item"] = nextItem
		}
	}

	return obj, changed
}
