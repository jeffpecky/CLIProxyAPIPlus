package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenCodeNeedsMessages(t *testing.T) {
	cases := map[string]bool{
		"union-alpha":             true,
		"union-alpha(high)":       true,
		"opencode/union-alpha":    true,
		"claude-haiku-5-5":        false,
		"deepseek-v4-flash":       false,
		"minimax-m3":              false,
		"":                        false,
		"union-alpha-2":           false,
		"opencode-go/union-alpha": true,
	}
	for model, want := range cases {
		if got := openCodeNeedsMessages(model); got != want {
			t.Errorf("openCodeNeedsMessages(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestOpenCodeGoNeedsMessages(t *testing.T) {
	cases := map[string]bool{
		"claude-haiku-5-5":             true,
		"claude-haiku-5-5(high)":       true,
		"opencode-go/claude-haiku-5-5": true,
		"claude-sonnet-4-6":            true,
		"glm-5.3":                      false,
		"deepseek-v4-pro":              false,
		"minimax-m3":                   false,
		"grok-4.6":                     false,
		"":                             false,
	}
	for model, want := range cases {
		if got := openCodeGoNeedsMessages(model); got != want {
			t.Errorf("openCodeGoNeedsMessages(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestOpenCodeNormalizeClaudeAdaptiveThinking(t *testing.T) {
	t.Run("enabled budget becomes adaptive effort", func(t *testing.T) {
		out := openCodeNormalizeClaudeAdaptiveThinking(
			[]byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`),
			"claude-haiku-5-5",
		)
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
			t.Fatalf("thinking.type = %q, want adaptive (%s)", got, out)
		}
		if got := gjson.GetBytes(out, "thinking.budget_tokens"); got.Exists() {
			t.Fatalf("budget_tokens should be removed: %s", out)
		}
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "medium" {
			t.Fatalf("output_config.effort = %q, want medium (%s)", got, out)
		}
	})

	t.Run("enabled without budget becomes adaptive", func(t *testing.T) {
		out := openCodeNormalizeClaudeAdaptiveThinking(
			[]byte(`{"thinking":{"type":"enabled"}}`),
			"claude-haiku-5-5",
		)
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
			t.Fatalf("thinking.type = %q, want adaptive (%s)", got, out)
		}
		if got := gjson.GetBytes(out, "output_config.effort"); got.Exists() {
			t.Fatalf("output_config.effort should be absent: %s", out)
		}
	})

	t.Run("enabled zero budget disables thinking", func(t *testing.T) {
		out := openCodeNormalizeClaudeAdaptiveThinking(
			[]byte(`{"thinking":{"type":"enabled","budget_tokens":0}}`),
			"claude-haiku-5-5",
		)
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
			t.Fatalf("thinking.type = %q, want disabled (%s)", got, out)
		}
		if got := gjson.GetBytes(out, "output_config"); got.Exists() {
			t.Fatalf("output_config should be absent: %s", out)
		}
	})

	t.Run("adaptive drops stale budget", func(t *testing.T) {
		out := openCodeNormalizeClaudeAdaptiveThinking(
			[]byte(`{"thinking":{"type":"adaptive","budget_tokens":1024},"output_config":{"effort":"low"}}`),
			"claude-haiku-5-5",
		)
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
			t.Fatalf("thinking.type = %q, want adaptive (%s)", got, out)
		}
		if got := gjson.GetBytes(out, "thinking.budget_tokens"); got.Exists() {
			t.Fatalf("budget_tokens should be removed: %s", out)
		}
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "low" {
			t.Fatalf("output_config.effort = %q, want low (%s)", got, out)
		}
	})

	t.Run("non claude model untouched", func(t *testing.T) {
		in := []byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`)
		out := openCodeNormalizeClaudeAdaptiveThinking(in, "deepseek-v4-flash")
		if string(out) != string(in) {
			t.Fatalf("payload changed for non-claude model: %s", out)
		}
	})

	t.Run("no thinking untouched", func(t *testing.T) {
		in := []byte(`{"messages":[]}`)
		out := openCodeNormalizeClaudeAdaptiveThinking(in, "claude-haiku-5-5")
		if string(out) != string(in) {
			t.Fatalf("payload changed without thinking: %s", out)
		}
	})
}

func TestOpenCodeApplyMessagesThinkingSuffix(t *testing.T) {
	cases := []struct {
		model      string
		wantType   string
		wantEffort string
	}{
		{"claude-haiku-5-5(high)", "adaptive", "high"},
		{"claude-haiku-5-5(8192)", "adaptive", "medium"},
		{"claude-haiku-5-5(auto)", "adaptive", ""},
		{"claude-haiku-5-5(none)", "disabled", ""},
		{"claude-haiku-5-5", "", ""},
		{"union-alpha(high)", "", ""},
		{"deepseek-v4-flash(high)", "", ""},
	}
	for _, tc := range cases {
		body := openCodeApplyMessagesThinkingSuffix([]byte(`{"model":"x","messages":[]}`), tc.model)
		body = openCodeNormalizeClaudeAdaptiveThinking(body, thinking.ParseSuffix(tc.model).ModelName)
		if got := gjson.GetBytes(body, "thinking.type").String(); got != tc.wantType {
			t.Errorf("%s: thinking.type = %q, want %q (%s)", tc.model, got, tc.wantType, body)
		}
		if got := gjson.GetBytes(body, "output_config.effort").String(); got != tc.wantEffort {
			t.Errorf("%s: output_config.effort = %q, want %q (%s)", tc.model, got, tc.wantEffort, body)
		}
		if got := gjson.GetBytes(body, "thinking.budget_tokens"); got.Exists() {
			t.Errorf("%s: budget_tokens should not be sent upstream: %s", tc.model, body)
		}
	}
}

func TestOpenCodeGoMessagesNonStream(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody []byte
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeSSEFixture("claude-haiku-5-5", "hello")))
	})
	defer server.Close()

	executor := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "go-secret",
		},
	}
	payload := []byte(`{"model":"claude-haiku-5-5","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          false,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotPath != "/messages" {
		t.Errorf("path = %q, want /messages", gotPath)
	}
	if got := gotHeaders.Get("x-api-key"); got != "go-secret" {
		t.Errorf("x-api-key = %q, want go-secret", got)
	}
	if got := gotHeaders.Get("anthropic-version"); got != openCodeAnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", got, openCodeAnthropicVersion)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}

	if got := gjson.GetBytes(gotBody, "model").String(); got != "claude-haiku-5-5" {
		t.Errorf("upstream model = %q", got)
	}
	if got := gjson.GetBytes(gotBody, "stream").Bool(); !got {
		t.Errorf("non-stream client should still stream upstream: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "thinking.type").String(); got != "adaptive" {
		t.Errorf("thinking.type = %q, want adaptive (%s)", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "output_config.effort").String(); got != "high" {
		t.Errorf("output_config.effort = %q, want high (%s)", got, gotBody)
	}

	body := string(resp.Payload)
	if !strings.Contains(body, `"chat.completion"`) || !strings.Contains(body, "hello") {
		t.Errorf("response not translated to OpenAI chat completion: %s", body)
	}
}

func TestOpenCodeFreeMessagesNonStreamAggregatesSSE(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeSSEFixture("union-alpha", "hello")))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "public",
		},
	}
	payload := []byte(`{"model":"union-alpha","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "union-alpha",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          false,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotPath != "/messages" {
		t.Errorf("path = %q, want /messages", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "stream").Bool(); !got {
		t.Errorf("free lane must force stream=true upstream: %s", gotBody)
	}
	body := string(resp.Payload)
	if !strings.Contains(body, `"chat.completion"`) || !strings.Contains(body, "hello") {
		t.Errorf("aggregated SSE not translated to OpenAI chat completion: %s", body)
	}
}

func TestOpenCodeGoMessagesStreamTranslatesToOpenAI(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeSSEFixture("claude-haiku-5-5", "hello")))
	})
	defer server.Close()

	executor := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "go-secret",
		},
	}
	payload := []byte(`{"model":"claude-haiku-5-5","messages":[{"role":"user","content":"hi"}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	out := combined.String()
	if out == "" {
		t.Fatal("no stream chunks emitted")
	}
	if !strings.Contains(out, `"chat.completion.chunk"`) || !strings.Contains(out, "hello") {
		t.Errorf("stream not translated to OpenAI chunks: %s", out)
	}
}

func TestOpenCodeGoMessagesStreamClaudePassthrough(t *testing.T) {
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeSSEFixture("claude-haiku-5-5", "hello")))
	})
	defer server.Close()

	executor := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "opencode-go",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "go-secret",
		},
	}
	payload := []byte(`{"model":"claude-haiku-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	out := combined.String()
	if !strings.Contains(out, "event: message_start") || !strings.Contains(out, "event: message_stop") {
		t.Errorf("claude SSE frames not passed through verbatim: %s", out)
	}
}

func TestOpenCodeAggregateClaudeSSE(t *testing.T) {
	body, err := openCodeAggregateClaudeSSE(strings.NewReader(claudeSSEFixture("union-alpha", "hello")))
	if err != nil {
		t.Fatalf("aggregate error: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("aggregated body is not JSON: %v (%s)", err, body)
	}
	if message["type"] != "message" || message["role"] != "assistant" {
		t.Errorf("unexpected message envelope: %s", body)
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected content: %s", body)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello" {
		t.Errorf("unexpected text block: %v", block)
	}
	if message["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", message["stop_reason"])
	}
}

// claudeSSEFixture returns a minimal Claude Messages SSE stream with one text
// block. The free tier and the Go Claude lane both speak this wire format.
func claudeSSEFixture(model, text string) string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":0}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}
