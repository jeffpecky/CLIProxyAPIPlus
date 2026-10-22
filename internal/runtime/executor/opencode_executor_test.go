package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestOpenCodeSessionFormat(t *testing.T) {
	for i := 0; i < 20; i++ {
		id := openCodeGenerateSessionId()
		if !strings.HasPrefix(id, "ses_") {
			t.Errorf("session ID %q missing ses_ prefix", id)
		}
		if len(id) != 30 { // 4 prefix + 12 hex + 14 base62 = 30
			t.Errorf("session ID %q has length %d, want 30", id, len(id))
		}
		hexPart := id[4:16]
		for _, c := range hexPart {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("session ID %q hex part %q contains non-hex char %c", id, hexPart, c)
				break
			}
		}
		base62Part := id[16:30]
		for _, c := range base62Part {
			if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
				t.Errorf("session ID %q base62 part %q contains non-base62 char %c", id, base62Part, c)
				break
			}
		}
	}
}

func TestOpenCodeRequestFormat(t *testing.T) {
	for i := 0; i < 20; i++ {
		id := openCodeGenerateRequestId()
		if !strings.HasPrefix(id, "msg_") {
			t.Errorf("request ID %q missing msg_ prefix", id)
		}
		if len(id) != 30 { // 4 prefix + 12 hex + 14 base62 = 30
			t.Errorf("request ID %q has length %d, want 30", id, len(id))
		}
	}
}

func TestOpenCodeTranslateSessionDeterministic(t *testing.T) {
	first := openCodeTranslateSession("conversation-a", "claude")
	second := openCodeTranslateSession("conversation-a", "claude")
	if first != second {
		t.Errorf("same input produced different sessions: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "ses_") {
		t.Errorf("translated session %q missing ses_ prefix", first)
	}
	if len(first) != 30 {
		t.Errorf("translated session %q has length %d, want 30", first, len(first))
	}
}

func TestOpenCodeTranslateSessionIsolation(t *testing.T) {
	convA := openCodeTranslateSession("conversation-a", "claude")
	convB := openCodeTranslateSession("conversation-b", "claude")
	toolClaude := openCodeTranslateSession("same", "claude")
	toolCodex := openCodeTranslateSession("same", "codex")

	if convA == convB {
		t.Error("different conversations produced same session")
	}
	if toolClaude == toolCodex {
		t.Error("different tools produced same session")
	}
}

func TestOpenCodeTranslateSessionEmpty(t *testing.T) {
	result := openCodeTranslateSession("", "claude")
	if result != "" {
		t.Errorf("empty session ID should produce empty result, got %q", result)
	}
}

func TestOpenCodeIsValidSessionFormat(t *testing.T) {
	valid := "ses_f534dfae8ffeCy4Ee4tLWNygDc"
	if !openCodeIsValidSessionFormat(valid) {
		t.Errorf("valid session %q rejected", valid)
	}

	invalid := []string{
		"",
		"ses_",
		"ses_12345",
		"msg_f534dfae8ffeCy4Ee4tLWNygDc",
		"ses_g534dfae8ffeCy4Ee4tLWNygDc",  // g is not hex
		"ses_f534dfae8ffeCy4Ee4tLWNygDcX", // too long
	}
	for _, s := range invalid {
		if openCodeIsValidSessionFormat(s) {
			t.Errorf("invalid session %q accepted", s)
		}
	}
}

func TestOpenCodeNativeSession(t *testing.T) {
	valid := "ses_f534dfae8ffeCy4Ee4tLWNygDc"

	// Valid session in header
	headers := http.Header{}
	headers.Set("x-opencode-session", valid)
	if got := openCodeNativeSession(headers); got != valid {
		t.Errorf("openCodeNativeSession = %q, want %q", got, valid)
	}

	// Case-insensitive
	headers2 := http.Header{}
	headers2.Set("X-OpenCode-Session", valid)
	if got := openCodeNativeSession(headers2); got != valid {
		t.Errorf("openCodeNativeSession case-insensitive = %q, want %q", got, valid)
	}

	// Invalid format rejected
	headers3 := http.Header{}
	headers3.Set("x-opencode-session", "invalid-session")
	if got := openCodeNativeSession(headers3); got != "" {
		t.Errorf("openCodeNativeSession accepted invalid format: %q", got)
	}

	// Nil headers
	if got := openCodeNativeSession(nil); got != "" {
		t.Errorf("openCodeNativeSession(nil) = %q, want empty", got)
	}
}

func TestOpenCodeResolveSessionPriority(t *testing.T) {
	valid := "ses_f534dfae8ffeCy4Ee4tLWNygDc"

	// 1. Native header takes priority
	headers := http.Header{}
	headers.Set("x-opencode-session", valid)
	session := openCodeResolveSession(headers, nil, nil, "desktop")
	if session != valid {
		t.Errorf("native header not respected: got %q, want %q", session, valid)
	}

	// 4. Fallback generates new session when no other source
	session2 := openCodeResolveSession(nil, nil, nil, "desktop")
	if !strings.HasPrefix(session2, "ses_") {
		t.Errorf("fallback session %q missing ses_ prefix", session2)
	}
}

func TestOpenCodeDeriveRequestId(t *testing.T) {
	session := "ses_f534dfae8ffeCy4Ee4tLWNygDc"
	payload := []byte(`{"messages":[{"role":"user","content":"ping"}]}`)

	first := openCodeDeriveRequestId(session, payload)
	second := openCodeDeriveRequestId(session, payload)

	if first != second {
		t.Errorf("same input produced different request IDs: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "msg_") {
		t.Errorf("request ID %q missing msg_ prefix", first)
	}
	if len(first) != 30 {
		t.Errorf("request ID %q has length %d, want 30", first, len(first))
	}

	// Different content produces different ID
	different := openCodeDeriveRequestId(session, []byte(`{"messages":[{"role":"user","content":"different"}]}`))
	if first == different {
		t.Error("different content produced same request ID")
	}
}

func TestOpenCodeDeriveRequestIdEmptyPayload(t *testing.T) {
	session := "ses_f534dfae8ffeCy4Ee4tLWNygDc"
	id := openCodeDeriveRequestId(session, nil)
	if !strings.HasPrefix(id, "msg_") {
		t.Errorf("fallback request ID %q missing msg_ prefix", id)
	}
}

func TestOpenCodeForceStream(t *testing.T) {
	payload := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`)
	result := openCodeForceStream(payload)

	var body map[string]any
	if err := json.Unmarshal(result, &body); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if stream, ok := body["stream"].(bool); !ok || !stream {
		t.Errorf("stream = %v, want true", body["stream"])
	}
}

func TestOpenCodeFingerprintTools(t *testing.T) {
	// No caller tools -> append read + shell with the exact upstream definitions.
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	updated := payload
	names := openCodeApplyFingerprintIfFree(true, &updated, false)
	if len(names) != 0 {
		t.Errorf("no caller tools should alias anything, got %v", names)
	}
	var body map[string]any
	if err := json.Unmarshal(updated, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected 2 fingerprint tools, got %v", body["tools"])
	}
	for i, want := range []string{"read", "shell"} {
		fn, ok := tools[i].(map[string]any)["function"].(map[string]any)
		if !ok {
			t.Fatalf("tool %d missing function wrapper: %v", i, tools[i])
		}
		if fn["name"] != want {
			t.Errorf("tool %d name = %v, want %s", i, fn["name"], want)
		}
		params, _ := fn["parameters"].(map[string]any)
		if params == nil || params["type"] != "object" {
			t.Errorf("tool %s parameters = %v, want object schema", want, fn["parameters"])
		}
		if _, has := params["required"]; !has {
			t.Errorf("tool %s parameters missing required list", want)
		}
	}
	if _, has := body["tool_choice"]; has {
		t.Errorf("chat tool_choice should stay unset, got %v", body["tool_choice"])
	}
}

func TestOpenCodeFingerprintAliasesConflictingCallerTool(t *testing.T) {
	// A caller tool named exactly "shell" with a different definition must be
	// aliased, so the official definition can be appended without a duplicate.
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"shell","description":"caller","parameters":{"type":"object"}}}]}`)
	updated := payload
	names := openCodeApplyFingerprintIfFree(true, &updated, false)
	if names["shell_client"] != "shell" {
		t.Fatalf("expected shell_client -> shell, got %v", names)
	}
	var body map[string]any
	if err := json.Unmarshal(updated, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range body["tools"].([]any) {
		fn, _ := tool.(map[string]any)["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			got[name] = true
		}
	}
	if !got["shell"] || !got["shell_client"] || !got["read"] {
		t.Errorf("tools = %v, want shell + shell_client + read", got)
	}
}

func TestOpenCodeFingerprintResponsesTools(t *testing.T) {
	// Flat Responses tools get the fingerprint too, and tool_choice defaults to auto.
	payload := []byte(`{"input":[{"type":"message","role":"user","content":"hi"}]}`)
	updated := payload
	names := openCodeApplyFingerprintIfFree(true, &updated, true)
	if len(names) != 0 {
		t.Errorf("unexpected aliases: %v", names)
	}
	var body map[string]any
	if err := json.Unmarshal(updated, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected 2 fingerprint tools, got %v", body["tools"])
	}
	for i, want := range []string{"read", "shell"} {
		if tools[i].(map[string]any)["name"] != want {
			t.Errorf("tool %d = %v, want flat name %s", i, tools[i], want)
		}
	}
	if body["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", body["tool_choice"])
	}
}

func TestOpenCodeRestoreToolNames(t *testing.T) {
	names := map[string]string{"shell_client": "shell"}
	payload := []byte(`data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"shell_client"}}]}}]}`)
	updated := helps.RestoreOpenCodeToolNames(payload, names)
	if !strings.Contains(string(updated), `"name":"shell"`) {
		t.Errorf("restored payload = %s, want name shell", updated)
	}
	if strings.Contains(string(updated), "shell_client") {
		t.Errorf("alias leaked into restored payload: %s", updated)
	}
	// No names -> untouched.
	same := helps.RestoreOpenCodeToolNames(payload, nil)
	if string(same) != string(payload) {
		t.Errorf("empty map should be a no-op, got %s", same)
	}
}

func TestOpenCodeExecutorHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode",
		Attributes: map[string]string{
			"base_url":                  server.URL,
			"api_key":                   "test",
			"header:Authorization":      "Bearer public",
			"header:User-Agent":         openCodeFreeUA,
			"header:x-opencode-client":  "desktop",
			"header:x-opencode-project": "global",
		},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		Stream: false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Check User-Agent
	if ua := gotHeaders.Get("User-Agent"); ua != openCodeFreeUA {
		t.Errorf("User-Agent = %q, want %q", ua, openCodeFreeUA)
	}

	// Check Authorization
	if auth := gotHeaders.Get("Authorization"); auth != "Bearer public" {
		t.Errorf("Authorization = %q, want 'Bearer public'", auth)
	}

	// Check session header and its v2 aliases
	session := gotHeaders.Get("x-opencode-session")
	if !strings.HasPrefix(session, "ses_") {
		t.Errorf("x-opencode-session %q missing ses_ prefix", session)
	}
	if got := gotHeaders.Get("x-opencode-session-id"); got != session {
		t.Errorf("x-opencode-session-id = %q, want %q", got, session)
	}
	if got := gotHeaders.Get("x-session-affinity"); got != session {
		t.Errorf("x-session-affinity = %q, want %q", got, session)
	}
	if got := gotHeaders.Get("x-session-id"); got != session {
		t.Errorf("x-session-id = %q, want %q", got, session)
	}

	// v1 x-opencode-request is rejected upstream with 403; it must not be sent.
	if got := gotHeaders.Get("x-opencode-request"); got != "" {
		t.Errorf("x-opencode-request = %q, want empty", got)
	}

	// Check other headers
	if gotHeaders.Get("x-opencode-project") != "global" {
		t.Errorf("x-opencode-project = %q, want 'global'", gotHeaders.Get("x-opencode-project"))
	}
	if gotHeaders.Get("x-opencode-client") != "desktop" {
		t.Errorf("x-opencode-client = %q, want 'desktop'", gotHeaders.Get("x-opencode-client"))
	}
}

func TestOpenCodeExecutorStreamHeaders(t *testing.T) {
	var gotHeaders http.Header
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode",
		Attributes: map[string]string{
			"base_url":                  server.URL,
			"api_key":                   "test",
			"header:Authorization":      "Bearer public",
			"header:User-Agent":         openCodeFreeUA,
			"header:x-opencode-client":  "desktop",
			"header:x-opencode-project": "global",
		},
	}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		Stream: true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}

	if ua := gotHeaders.Get("User-Agent"); ua != openCodeFreeUA {
		t.Errorf("User-Agent = %q, want %q", ua, openCodeFreeUA)
	}
	if auth := gotHeaders.Get("Authorization"); auth != "Bearer public" {
		t.Errorf("Authorization = %q, want 'Bearer public'", auth)
	}
}

func TestOpenCodeSessionReuse(t *testing.T) {
	executor := NewOpenCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "opencode",
		Attributes: map[string]string{"connection_id": "conn-1"},
	}

	session1 := openCodeResolveSession(nil, nil, auth, "desktop")
	session2 := openCodeResolveSession(nil, nil, auth, "desktop")

	if session1 != session2 {
		t.Errorf("same connection produced different sessions: %q vs %q", session1, session2)
	}

	// Different connection -> different session
	auth2 := &cliproxyauth.Auth{
		Provider:   "opencode",
		Attributes: map[string]string{"connection_id": "conn-2"},
	}
	session3 := openCodeResolveSession(nil, nil, auth2, "desktop")
	if session1 == session3 {
		t.Error("different connections produced same session")
	}

	_ = executor
}

func TestOpenCodeIsMuseSpark(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"muse-spark-1.3-contributor-free", true},
		{"muse-spark-1.2-contributor-free", true},
		{"meta/muse-spark-1.3", true},
		{"mimo-v2.5-free", false},
		{"gpt-4o", false},
		{"claude-3-5-sonnet", false},
	}
	for _, tt := range tests {
		if got := openCodeIsMuseSpark(tt.model); got != tt.want {
			t.Errorf("openCodeIsMuseSpark(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestOpenCodeChatToResponses(t *testing.T) {
	chatBody := []byte(`{
		"model": "muse-spark-1.3-contributor-free",
		"messages": [
			{"role": "system", "content": "You are helpful"},
			{"role": "user", "content": "hi"}
		],
		"max_tokens": 1000,
		"tools": [
			{"type": "function", "function": {"name": "my_tool", "description": "desc", "parameters": {"type": "object"}}}
		]
	}`)
	converted := openCodeChatToResponses(chatBody)
	if converted == nil {
		t.Fatal("openCodeChatToResponses returned nil")
	}

	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if body["model"] != "muse-spark-1.3-contributor-free" {
		t.Errorf("model = %v, want muse-spark-1.3-contributor-free", body["model"])
	}
	if body["instructions"] != "You are helpful" {
		t.Errorf("instructions = %v, want 'You are helpful'", body["instructions"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("expected 1 input item, got %v", body["input"])
	}
	item := input[0].(map[string]any)
	if item["role"] != "user" {
		t.Errorf("input role = %v, want user", item["role"])
	}
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	if body["store"] != false {
		t.Errorf("store = %v, want false", body["store"])
	}
}

func TestOpenCodeChatToResponsesWithContentParts(t *testing.T) {
	chatBody := []byte(`{
		"model": "muse-spark-1.3-contributor-free",
		"system": "System instructions here",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello world"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "hi back"}]}
		],
		"tools": [
			{"name": "test_tool", "description": "desc", "input_schema": {"type": "object", "properties": {}}}
		]
	}`)
	converted := openCodeChatToResponses(chatBody)
	if converted == nil {
		t.Fatal("openCodeChatToResponses returned nil")
	}

	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if body["instructions"] != "System instructions here" {
		t.Errorf("instructions = %v, want 'System instructions here'", body["instructions"])
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected 2 input items, got %v", body["input"])
	}

	userItem := input[0].(map[string]any)
	userContent := userItem["content"].([]any)
	part0 := userContent[0].(map[string]any)
	if part0["type"] != "input_text" || part0["text"] != "hello world" {
		t.Errorf("user part0 = %v, want type: input_text, text: hello world", part0)
	}

	asstItem := input[1].(map[string]any)
	asstContent := asstItem["content"].([]any)
	asstPart0 := asstContent[0].(map[string]any)
	if asstPart0["type"] != "output_text" || asstPart0["text"] != "hi back" {
		t.Errorf("asst part0 = %v, want type: output_text, text: hi back", asstPart0)
	}

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", body["tools"])
	}
	tool0 := tools[0].(map[string]any)
	if tool0["name"] != "test_tool" {
		t.Errorf("tool name = %v, want test_tool", tool0["name"])
	}
}

// newTestServer creates a test HTTP server with the given handler.
func newTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}
