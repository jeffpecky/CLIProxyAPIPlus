package helps

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	tools, _ := body["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		tm, _ := tool.(map[string]any)
		if fn, ok := tm["function"].(map[string]any); ok {
			name, _ := fn["name"].(string)
			names = append(names, name)
			continue
		}
		name, _ := tm["name"].(string)
		names = append(names, name)
	}
	return names
}

func TestApplyOpenCodeFingerprintToolsAliasesAndNotes(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "tool_calls": [{"function": {"name": "shell"}}]}
		],
		"tools": [
			{"type": "function", "function": {"name": "shell", "description": "caller", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "read", "description": "caller", "parameters": {"type": "object"}}}
		]
	}`)

	updated, names := ApplyOpenCodeFingerprintTools(payload, false)
	if names["shell_client"] != "shell" || names["read_client"] != "read" {
		t.Fatalf("aliases = %v, want shell_client->shell, read_client->read", names)
	}

	var body map[string]any
	if err := json.Unmarshal(updated, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	gotNames := toolNames(t, body)
	joined := strings.Join(gotNames, ",")
	for _, want := range []string{"shell_client", "read_client", "read", "shell"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tools %v missing %s", gotNames, want)
		}
	}

	// Prior-turn call to the concealed caller tool must follow the alias.
	messages, _ := body["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("expected placeholder note as first system message, got %v", messages[0])
	}
	note, _ := first["content"].(string)
	if !strings.Contains(note, "placeholder") {
		t.Errorf("note = %q, want placeholder warning", note)
	}
	assistant, _ := messages[len(messages)-1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	fn, _ := call["function"].(map[string]any)
	if fn["name"] != "shell_client" {
		t.Errorf("history tool_call name = %v, want shell_client", fn["name"])
	}
}

func TestApplyOpenCodeFingerprintToolsResponsesDefault(t *testing.T) {
	payload := []byte(`{"input": [{"type": "message", "role": "user", "content": "hi"}]}`)
	updated, names := ApplyOpenCodeFingerprintTools(payload, true)
	if len(names) != 0 {
		t.Fatalf("unexpected aliases: %v", names)
	}
	var body map[string]any
	if err := json.Unmarshal(updated, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if body["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", body["tool_choice"])
	}
	instructions, _ := body["instructions"].(string)
	if !strings.Contains(instructions, "read") {
		t.Errorf("instructions = %q, want placeholder note", instructions)
	}
	if names := toolNames(t, body); len(names) != 2 {
		t.Errorf("tools = %v, want read + shell", names)
	}
}

func TestApplyOpenCodeFingerprintToolsSkipsNoteForClaudeShape(t *testing.T) {
	payload := []byte(`{"system": "sys", "messages": [{"role": "user", "content": "hi"}]}`)
	updated, _ := ApplyOpenCodeFingerprintTools(payload, false)
	var body map[string]any
	if err := json.Unmarshal(updated, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Errorf("claude-shaped body must not gain a system message, got %v", messages)
	}
}

func TestRestoreOpenCodeToolNamesClaudeAndResponses(t *testing.T) {
	names := map[string]string{"shell_client": "shell", "read_client": "read"}

	claude := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"shell_client"}}`)
	restored := RestoreOpenCodeToolNames(claude, names)
	if !strings.Contains(string(restored), `"name":"shell"`) || strings.Contains(string(restored), "shell_client") {
		t.Errorf("claude restore = %s", restored)
	}

	responses := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"read_client"}}`)
	restored = RestoreOpenCodeToolNames(responses, names)
	if !strings.Contains(string(restored), `"name":"read"`) || strings.Contains(string(restored), "read_client") {
		t.Errorf("responses restore = %s", restored)
	}

	// [DONE] and unmatched payloads stay byte-identical.
	for _, payload := range [][]byte{[]byte("[DONE]"), []byte(`{"choices":[{"delta":{"content":"hi"}}]}`)} {
		if got := RestoreOpenCodeToolNames(payload, names); string(got) != string(payload) {
			t.Errorf("unexpected rewrite: %s -> %s", payload, got)
		}
	}
}
