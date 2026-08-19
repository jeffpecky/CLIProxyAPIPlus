package tokensaver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestHeadroomParsesOfficialResponseAndRejectsCCR(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"long long long"}]}`)
	out, stats := applyHeadroomResponse(t, body, "openai", `{"messages":[{"role":"user","content":"short"}],"compression_ratio":0.25,"transforms_applied":["smart"],"transforms_summary":{"smart":1},"ccr_hashes":["abc"]}`)
	if !bytes.Equal(out, body) || stats.Headroom || stats.HeadroomSkip != "CCR output unsupported" {
		t.Fatalf("CCR response accepted: stats=%+v body=%s", stats, out)
	}
	if stats.HeadroomStats.Ratio != 0.25 || len(stats.HeadroomStats.Transforms) != 1 || stats.HeadroomStats.TransformSummary["smart"] != 1 || len(stats.HeadroomStats.CCRHashes) != 1 {
		t.Fatalf("official diagnostics missing: %+v", stats.HeadroomStats)
	}
}

func TestApplyDisabledReturnsOriginal(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{}})
	if !bytes.Equal(out, body) {
		t.Fatalf("body changed: %s", out)
	}
	if stats.Applied {
		t.Fatal("stats applied for disabled saver")
	}
}

func TestApplyHeaderOptOutReturnsOriginal(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"base"}]}`)
	headers := http.Header{OptOutHeader: []string{"off"}}
	out, stats := Apply(Options{Body: body, Format: "openai", Headers: headers, Config: config.TokenSaverConfig{Enabled: true, Caveman: config.TokenSaverPromptConfig{Enabled: true, Level: "terse"}}})
	if !bytes.Equal(out, body) {
		t.Fatalf("body changed: %s", out)
	}
	if stats.Applied {
		t.Fatal("stats applied despite opt-out")
	}
}

func TestApplyHeaderOptOutChecksAllValues(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"base"}]}`)
	headers := http.Header{OptOutHeader: []string{"on", "off"}}
	out, _ := Apply(Options{Body: body, Format: "openai", Headers: headers, Config: config.TokenSaverConfig{Enabled: true, Caveman: config.TokenSaverPromptConfig{Enabled: true, Level: "terse"}}})
	if !bytes.Equal(out, body) {
		t.Fatalf("body changed despite duplicate opt-out: %s", out)
	}
}

func TestApplyInvalidJSONReturnsOriginal(t *testing.T) {
	body := []byte(`{"model":`)
	out, _ := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, RTK: true}})
	if !bytes.Equal(out, body) {
		t.Fatalf("invalid json changed: %s", out)
	}
}

func TestCavemanInjectsOpenAISystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"base"},{"role":"user","content":"hi"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, Caveman: config.TokenSaverPromptConfig{Enabled: true, Level: "terse"}}})
	if !stats.Caveman || !bytes.Contains(out, []byte("Respond like terse caveman")) {
		t.Fatalf("caveman not injected: stats=%+v body=%s", stats, out)
	}
}

func TestOpenAISystemArrayUsesProtocolContentType(t *testing.T) {
	tests := []struct{ name, format, body, want string }{
		{"chat system", "openai", `{"messages":[{"role":"system","content":[{"type":"text","text":"base"}]}]}`, "text"},
		{"chat developer", "openai", `{"messages":[{"role":"developer","content":[{"type":"text","text":"base"}]}]}`, "text"},
		{"responses input", "openai-response", `{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"base"}]}]}`, "input_text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, _ := Apply(Options{Body: []byte(test.body), Format: test.format, Config: config.TokenSaverConfig{Enabled: true, Caveman: config.TokenSaverPromptConfig{Enabled: true, Level: "terse"}}})
			if got := gjson.GetBytes(out, map[string]string{"openai": "messages.0.content.1.type", "openai-response": "input.0.content.1.type"}[test.format]).String(); got != test.want {
				t.Fatalf("content type = %q, want %q; body=%s", got, test.want, out)
			}
		})
	}
}

func TestHeadroomRejectsStructuralMessageChanges(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"assistant","name":"keep","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],"content":[{"type":"text","text":"long"},{"type":"tool_use","id":"use-1","name":"lookup","input":{"q":"x"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`)
	responses := map[string]string{
		"message field":  `{"messages":[{"role":"assistant","name":"changed","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],"content":[{"type":"text","text":"short"},{"type":"tool_use","id":"use-1","name":"lookup","input":{"q":"x"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`,
		"tool call id":   `{"messages":[{"role":"assistant","name":"keep","tool_calls":[{"id":"evil","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],"content":[{"type":"text","text":"short"},{"type":"tool_use","id":"use-1","name":"lookup","input":{"q":"x"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`,
		"block type":     `{"messages":[{"role":"assistant","name":"keep","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],"content":[{"type":"image","text":"short"},{"type":"tool_use","id":"use-1","name":"lookup","input":{"q":"x"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`,
		"tool use id":    `{"messages":[{"role":"assistant","name":"keep","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],"content":[{"type":"text","text":"short"},{"type":"tool_use","id":"evil","name":"lookup","input":{"q":"x"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`,
		"tool result id": `{"messages":[{"role":"assistant","name":"keep","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}],"content":[{"type":"text","text":"short"},{"type":"tool_use","id":"use-1","name":"lookup","input":{"q":"x"}}]},{"role":"tool","tool_call_id":"evil","content":"result"}]}`,
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			out, stats := applyHeadroomResponse(t, body, "openai", response)
			if !bytes.Equal(out, body) || stats.Headroom || !strings.Contains(stats.HeadroomSkip, "structure") {
				t.Fatalf("malicious replacement accepted: stats=%+v body=%s", stats, out)
			}
		})
	}
}

func TestPonytailInjectsClaudeSystem(t *testing.T) {
	body := []byte(`{"model":"m","system":"base","messages":[{"role":"user","content":"hi"}]}`)
	out, stats := Apply(Options{Body: body, Format: "claude", Config: config.TokenSaverConfig{Enabled: true, Ponytail: config.TokenSaverPromptConfig{Enabled: true, Level: "standard"}}})
	if !stats.Ponytail || !bytes.Contains(out, []byte("lazy senior developer")) {
		t.Fatalf("ponytail not injected: stats=%+v body=%s", stats, out)
	}
}

func TestCavemanInjectsGeminiSystemInstruction(t *testing.T) {
	body := []byte(`{"model":"m","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	out, stats := Apply(Options{Body: body, Format: "gemini", Config: config.TokenSaverConfig{Enabled: true, Caveman: config.TokenSaverPromptConfig{Enabled: true, Level: "terse"}}})
	if !stats.Caveman || !bytes.Contains(out, []byte("systemInstruction")) || !bytes.Contains(out, []byte("Respond like terse caveman")) {
		t.Fatalf("gemini caveman not injected: stats=%+v body=%s", stats, out)
	}
}

func TestRTKCompressesOpenAIToolGrep(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"tool","content":"src/a.go:10: alpha\nsrc/a.go:11: beta\nsrc/b.go:2: gamma\nsrc/b.go:3: delta\n"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, RTK: true}})
	if stats.RTKHits == 0 {
		t.Fatalf("no rtk hit: body=%s", out)
	}
	if len(out) >= len(body) {
		t.Fatalf("output did not shrink: before=%d after=%d body=%s", len(body), len(out), out)
	}
}

func TestRTKCompressesOpenAIResponsesOutput(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"function_call_output","output":"src/a.go:10: alpha\nsrc/a.go:11: beta\nsrc/b.go:2: gamma\nsrc/b.go:3: delta\n"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, RTK: true}})
	if stats.RTKHits == 0 || len(out) >= len(body) {
		t.Fatalf("responses output not compressed: stats=%+v body=%s", stats, out)
	}
}

func TestRTKPreservesClaudeErrorToolResult(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","is_error":true,"content":"src/a.go:10: alpha\nsrc/a.go:11: beta\n"}]}]}`)
	out, stats := Apply(Options{Body: body, Format: "claude", Config: config.TokenSaverConfig{Enabled: true, RTK: true}})
	if stats.RTKHits != 0 {
		t.Fatalf("error tool compressed: stats=%+v body=%s", stats, out)
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("error body changed: %s", out)
	}
}

func TestHeadroomFailureReturnsOriginal(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, Headroom: config.TokenSaverHeadroomConfig{Enabled: true, URL: "http://127.0.0.1:1", TimeoutMS: 1}}})
	if !bytes.Equal(out, body) {
		t.Fatalf("headroom failure changed body: %s", out)
	}
	if stats.Headroom {
		t.Fatal("headroom marked applied on failure")
	}
	if stats.HeadroomSkip == "" {
		t.Fatalf("headroom failure skip reason missing: %+v", stats)
	}
}

func TestHeadroomReplacesMessagesWhenSmaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/compress" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	}))
	t.Cleanup(server.Close)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello hello hello hello hello hello"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, Headroom: config.TokenSaverHeadroomConfig{Enabled: true, URL: server.URL, TimeoutMS: 1000}}})
	if !stats.Headroom || !bytes.Contains(out, []byte(`"content":"hi"`)) {
		t.Fatalf("headroom not applied: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomCompressesInput(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":"hello hello hello hello hello"}]}`)
	out, stats := applyHeadroomResponse(t, body, "openai", `{"messages":[{"role":"user","content":"hi"}]}`)
	if !stats.Headroom || !bytes.Contains(out, []byte(`"input":[{"content":"hi","role":"user"}]`)) {
		t.Fatalf("input not compressed: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomCompressesClaude(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":777,"metadata":{"trace":"keep"},"provider_extension":{"future":true},"system":"long system long system long system long system long system long system long system long system","messages":[{"role":"user","content":"long user long user long user long user long user long user long user long user"}]}`)
	out, stats := applyHeadroomResponse(t, body, "claude", `{"messages":[{"role":"user","content":"usr"}]}`)
	if !stats.Headroom || !bytes.Contains(out, []byte(`"content":"usr"`)) || !bytes.Contains(out, []byte(`"system":"long system`)) {
		t.Fatalf("claude not compressed: stats=%+v body=%s", stats, out)
	}
	if !bytes.Contains(out, []byte(`"max_tokens":777`)) || !bytes.Contains(out, []byte(`"trace":"keep"`)) || !bytes.Contains(out, []byte(`"future":true`)) {
		t.Fatalf("claude unrelated fields changed: %s", out)
	}
}

func TestHeadroomSkipsUnsafeResponsesInput(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":"large output"}]}`)
	out, stats := applyHeadroomResponse(t, body, "openai-response", `{"messages":[{"role":"user","content":"bad"}]}`)
	if stats.Headroom || !bytes.Equal(out, body) || !bytes.Contains([]byte(stats.HeadroomSkip), []byte("unsafe")) {
		t.Fatalf("unsafe responses changed: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomCompressesSafeResponsesInput(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hello hello hello hello hello"}]}`)
	out, stats := applyHeadroomResponse(t, body, "openai-response", `{"messages":[{"role":"user","content":"hi"}]}`)
	if !stats.Headroom || !bytes.Contains(out, []byte(`"type":"message"`)) || !bytes.Contains(out, []byte(`"content":"hi"`)) {
		t.Fatalf("responses not compressed: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomCompressesKiroProjection(t *testing.T) {
	body := []byte(`{"model":"m","conversationState":{"history":[{"userInputMessage":{"content":"long long long long"}}],"currentMessage":{"assistantResponseMessage":{"content":"answer answer answer"}}}}`)
	out, stats := applyHeadroomResponse(t, body, "kiro", `{"messages":[{"role":"user","content":"short"},{"role":"assistant","content":"ok"}]}`)
	if !stats.Headroom || !bytes.Contains(out, []byte(`"content":"short"`)) || !bytes.Contains(out, []byte(`"content":"ok"`)) {
		t.Fatalf("kiro not compressed: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomCompressesKiroToolResult(t *testing.T) {
	body := []byte(`{"model":"m","conversationState":{"currentMessage":{"userInputMessage":{"content":"question","userInputMessageContext":{"toolResults":[{"toolUseId":"call-1","content":[{"text":"long tool output long tool output"}]}]}}}}}`)
	out, stats := applyHeadroomResponse(t, body, "kiro", `{"messages":[{"role":"user","content":"question"},{"role":"tool","tool_call_id":"call-1","content":"short"}]}`)
	if !stats.Headroom || !bytes.Contains(out, []byte(`"text":"short"`)) {
		t.Fatalf("kiro tool result not compressed: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomProjectsKiroAssistantToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"tool_calls"`)) || !bytes.Contains(raw, []byte(`"call-1"`)) || !bytes.Contains(raw, []byte(`"lookup"`)) {
			t.Errorf("Kiro tool calls missing from Headroom request: %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":"short","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}]}`))
	}))
	t.Cleanup(server.Close)
	body := []byte(`{"model":"m","conversationState":{"currentMessage":{"assistantResponseMessage":{"content":"long answer long answer","toolUses":[{"toolUseId":"call-1","name":"lookup","input":{"q":"x"}}]}}}}`)
	out, stats := Apply(Options{Body: body, Format: "kiro", Config: config.TokenSaverConfig{Enabled: true, Headroom: config.TokenSaverHeadroomConfig{Enabled: true, URL: server.URL, TimeoutMS: 1000}}})
	if !stats.Headroom || !bytes.Contains(out, []byte(`"content":"short"`)) {
		t.Fatalf("Kiro assistant not compressed: stats=%+v body=%s", stats, out)
	}
}

func applyHeadroomResponse(t *testing.T, body []byte, format, response string) ([]byte, Stats) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return Apply(Options{Body: body, Format: format, Config: config.TokenSaverConfig{Enabled: true, Headroom: config.TokenSaverHeadroomConfig{Enabled: true, URL: server.URL, TimeoutMS: 1000}}})
}

func TestHeadroomMasksEndpointAndReportsStats(t *testing.T) {
	masked := maskEndpoint("https://user:secret@example.com:8787/proxy/v1/compress?token=abc#x")
	if masked != "https://example.com:8787/proxy/v1/compress" {
		t.Fatalf("masked endpoint = %q", masked)
	}
	body := []byte(`{"model":"m","tools":[{"name":"x","description":"long long"}],"messages":[{"role":"tool","content":"long long long long long long"}]}`)
	out, stats := applyHeadroomResponse(t, body, "openai", `{"messages":[{"role":"tool","content":"short"}],"tokens_before":100,"tokens_after":90,"tokens_saved":10}`)
	if !stats.Headroom || stats.HeadroomStats.TokensSaved != 10 || stats.HeadroomBefore.BodyBytes == 0 || stats.HeadroomAfter.BodyBytes == 0 {
		t.Fatalf("diagnostics missing: stats=%+v body=%s", stats, out)
	}
	if stats.HeadroomBefore.ToolSchemaBytes == 0 || stats.HeadroomBefore.ToolHistoryBytes == 0 {
		t.Fatalf("tool diagnostics missing: %+v", stats.HeadroomBefore)
	}
}

func TestHeadroomDetectsPhantomSavings(t *testing.T) {
	if !isPhantomHeadroomSavings(HeadroomStats{TokensSaved: 10}, SizeSnapshot{BodyBytes: 100}, SizeSnapshot{BodyBytes: 97}) {
		t.Fatal("expected phantom savings")
	}
	if isPhantomHeadroomSavings(HeadroomStats{TokensSaved: 10}, SizeSnapshot{BodyBytes: 100}, SizeSnapshot{BodyBytes: 80}) {
		t.Fatal("unexpected phantom savings")
	}
}

func TestHeadroomEndpointPreservesQueryAfterPath(t *testing.T) {
	endpoint := headroomEndpoint("https://user:secret@example.com/proxy?token=abc#fragment")
	if endpoint != "https://user:secret@example.com/proxy/v1/compress?token=abc" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}

func TestHeadroomHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"long long long"}]}`)
	out, stats := Apply(Options{Context: ctx, Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, Headroom: config.TokenSaverHeadroomConfig{Enabled: true, URL: server.URL, TimeoutMS: 1000}}})
	if !bytes.Equal(out, body) || stats.Headroom || stats.HeadroomSkip == "" {
		t.Fatalf("cancelled Headroom changed request: stats=%+v body=%s", stats, out)
	}
}

func TestHeadroomRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "16777217")
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	t.Cleanup(server.Close)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"long long long"}]}`)
	out, stats := Apply(Options{Body: body, Format: "openai", Config: config.TokenSaverConfig{Enabled: true, Headroom: config.TokenSaverHeadroomConfig{Enabled: true, URL: server.URL, TimeoutMS: 1000}}})
	if !bytes.Equal(out, body) || stats.Headroom || !strings.Contains(stats.HeadroomSkip, "large") {
		t.Fatalf("oversized Headroom response not rejected: stats=%+v body=%s", stats, out)
	}
}
