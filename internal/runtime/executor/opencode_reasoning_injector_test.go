package executor

import (
	"encoding/json"
	"testing"
)

func TestOpenCodeReasoningContentInjector(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		provider     string
		inputBody    string
		wantInjected bool
		wantScope    string
	}{
		{
			name:         "DeepSeek model - all scope injects on assistant without reasoning_content",
			model:        "deepseek-v4-flash-free",
			provider:     "opencode",
			inputBody:    `{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantInjected: true,
			wantScope:    "all",
		},
		{
			name:         "DeepSeek model - all scope does not inject when reasoning_content present",
			model:        "deepseek-v4-flash-free",
			provider:     "opencode",
			inputBody:    `{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":"thinking"}]}`,
			wantInjected: false,
			wantScope:    "all",
		},
		{
			name:         "Kimi model - toolCalls scope injects on assistant with tool_calls",
			model:        "kimi-k2-free",
			provider:     "moonshot",
			inputBody:    `{"model":"kimi-k2-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}]}`,
			wantInjected: true,
			wantScope:    "toolCalls",
		},
		{
			name:         "Kimi model - toolCalls scope does not inject on assistant without tool_calls",
			model:        "kimi-k2-free",
			provider:     "moonshot",
			inputBody:    `{"model":"kimi-k2-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantInjected: false,
			wantScope:    "toolCalls",
		},
		{
			name:         "Non-matching model on a provider without a rule - no injection",
			model:        "gpt-4o",
			provider:     "generic",
			inputBody:    `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantInjected: false,
			wantScope:    "",
		},
		{
			name:         "Provider-level rule covers any OpenCode model",
			model:        "glm-5.3-flash-free",
			provider:     "opencode",
			inputBody:    `{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantInjected: true,
			wantScope:    "all",
		},
		{
			name:         "Empty body - no injection",
			model:        "deepseek-v4-flash-free",
			provider:     "opencode",
			inputBody:    "",
			wantInjected: false,
			wantScope:    "",
		},
		{
			name:         "Invalid JSON - no injection",
			model:        "deepseek-v4-flash-free",
			provider:     "opencode",
			inputBody:    "not json",
			wantInjected: false,
			wantScope:    "",
		},
		{
			name:         "No messages array - no injection",
			model:        "deepseek-v4-flash-free",
			provider:     "opencode",
			inputBody:    `{"model":"deepseek-v4-flash-free"}`,
			wantInjected: false,
			wantScope:    "",
		},
		{
			name:         "Kimi case insensitive",
			model:        "KIMI-K2-FREE",
			provider:     "moonshot",
			inputBody:    `{"model":"KIMI-K2-FREE","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}]}`,
			wantInjected: true,
			wantScope:    "toolCalls",
		},
		{
			name:         "DeepSeek case insensitive",
			model:        "DEEPSEEK-V4-PRO",
			provider:     "opencode",
			inputBody:    `{"model":"DEEPSEEK-V4-PRO","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantInjected: true,
			wantScope:    "all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.inputBody)
			cfg := DefaultOpenCodeReasoningInjectorConfig()
			result := injectReasoningContentIntoBody(body, tt.model, tt.provider, cfg)

			if tt.wantInjected {
				if string(result) == tt.inputBody {
					t.Errorf("expected reasoning_content to be injected but body unchanged")
				}
				var resultMap map[string]any
				if err := json.Unmarshal(result, &resultMap); err != nil {
					t.Fatalf("result not valid JSON: %v", err)
				}
				messages, ok := resultMap["messages"].([]any)
				if !ok {
					t.Fatalf("messages not an array")
				}
				found := false
				for _, msg := range messages {
					msgMap, ok := msg.(map[string]any)
					if !ok {
						continue
					}
					if role, _ := msgMap["role"].(string); role == "assistant" {
						if rc, ok := msgMap["reasoning_content"].(string); ok && rc == openCodeReasoningPlaceholder {
							found = true
							break
						}
					}
				}
				if !found {
					t.Errorf("expected reasoning_content placeholder in assistant message, got: %s", string(result))
				}
			} else {
				if string(result) != tt.inputBody {
					t.Errorf("expected no injection but body changed: %s", string(result))
				}
			}
		})
	}
}

func TestOpenCodeReasoningContentInjectorMultiTurn(t *testing.T) {
	// Test multi-turn conversation where assistant messages accumulate
	model := "deepseek-v4-flash-free"
	provider := "opencode"

	// First turn - no assistant message yet
	body1 := []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}]}`)
	cfg := DefaultOpenCodeReasoningInjectorConfig()
	result1 := injectReasoningContentIntoBody(body1, model, provider, cfg)
	if string(result1) != string(body1) {
		t.Errorf("first turn should not inject (no assistant message)")
	}

	// Second turn - assistant responds
	body2 := []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
	result2 := injectReasoningContentIntoBody(body2, model, provider, cfg)
	var resultMap2 map[string]any
	if err := json.Unmarshal(result2, &resultMap2); err != nil {
		t.Fatalf("result2 not valid JSON: %v", err)
	}
	messages2 := resultMap2["messages"].([]any)
	assistantMsg := messages2[1].(map[string]any)
	if rc, ok := assistantMsg["reasoning_content"].(string); !ok || rc != openCodeReasoningPlaceholder {
		t.Errorf("second turn should inject reasoning_content, got: %v", assistantMsg["reasoning_content"])
	}

	// Third turn - user asks again, assistant should get new placeholder
	body3 := []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":" "},{"role":"user","content":"how are you"},{"role":"assistant","content":"good"}]}`)
	result3 := injectReasoningContentIntoBody(body3, model, provider, cfg)
	var resultMap3 map[string]any
	if err := json.Unmarshal(result3, &resultMap3); err != nil {
		t.Fatalf("result3 not valid JSON: %v", err)
	}
	messages3 := resultMap3["messages"].([]any)
	// First assistant already has reasoning_content (space), should not be overwritten
	assistantMsg1 := messages3[1].(map[string]any)
	if rc, ok := assistantMsg1["reasoning_content"].(string); !ok || rc != openCodeReasoningPlaceholder {
		t.Errorf("first assistant should keep placeholder, got: %v", assistantMsg1["reasoning_content"])
	}
	// Second assistant should get new placeholder
	assistantMsg2 := messages3[3].(map[string]any)
	if rc, ok := assistantMsg2["reasoning_content"].(string); !ok || rc != openCodeReasoningPlaceholder {
		t.Errorf("second assistant should get placeholder, got: %v", assistantMsg2["reasoning_content"])
	}
}

func TestOpenCodeMaybeInjectReasoningContent(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
	result := OpenCodeMaybeInjectReasoningContent(nil, "opencode", "deepseek-v4-flash-free", body)
	var resultMap map[string]any
	if err := json.Unmarshal(result, &resultMap); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	messages := resultMap["messages"].([]any)
	assistantMsg := messages[1].(map[string]any)
	if rc, ok := assistantMsg["reasoning_content"].(string); !ok || rc != openCodeReasoningPlaceholder {
		t.Errorf("OpenCodeMaybeInjectReasoningContent should inject, got: %v", assistantMsg["reasoning_content"])
	}

	// A provider without an injection rule passes through unchanged.
	body2 := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
	result2 := OpenCodeMaybeInjectReasoningContent(nil, "generic", "gpt-4o", body2)
	if string(result2) != string(body2) {
		t.Errorf("provider without a rule should not inject")
	}
}
