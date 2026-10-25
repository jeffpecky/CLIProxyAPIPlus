package executor

import (
	"encoding/json"
	"testing"
)

func TestOpenCodeGoStripUnsupportedParams(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		payload     string
		wantPresent bool
	}{
		{
			name:        "deepseek json_object is dropped",
			model:       "deepseek-v4.1-flash",
			payload:     `{"model":"deepseek-v4.1-flash","messages":[],"response_format":{"type":"json_object"}}`,
			wantPresent: false,
		},
		{
			name:        "deepseek json_schema is dropped",
			model:       "opencode-go/deepseek-v4-pro(high)",
			payload:     `{"model":"deepseek-v4-pro","messages":[],"response_format":{"type":"json_schema","json_schema":{"name":"x"}}}`,
			wantPresent: false,
		},
		{
			name:        "glm keeps response_format",
			model:       "glm-5.3",
			payload:     `{"model":"glm-5.3","messages":[],"response_format":{"type":"json_object"}}`,
			wantPresent: true,
		},
		{
			name:        "deepseek without response_format is unchanged",
			model:       "deepseek-v4-flash",
			payload:     `{"model":"deepseek-v4-flash","messages":[]}`,
			wantPresent: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := openCodeGoStripUnsupportedParams(tc.model, []byte(tc.payload))
			var body map[string]any
			if err := json.Unmarshal(out, &body); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			_, present := body["response_format"]
			if present != tc.wantPresent {
				t.Errorf("response_format present = %v, want %v (body: %s)", present, tc.wantPresent, out)
			}
		})
	}
}

func TestNormalizeResponsesReasoning(t *testing.T) {
	t.Run("nests reasoning_effort", func(t *testing.T) {
		body := map[string]any{"reasoning_effort": "high"}
		normalizeResponsesReasoning(body)

		if _, ok := body["reasoning_effort"]; ok {
			t.Error("reasoning_effort should be deleted")
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok {
			t.Fatal("reasoning object not created")
		}
		if reasoning["effort"] != "high" {
			t.Errorf("effort = %v, want high", reasoning["effort"])
		}
		if reasoning["summary"] != "auto" {
			t.Errorf("summary = %v, want auto", reasoning["summary"])
		}
	})

	t.Run("preserves existing reasoning and fills summary", func(t *testing.T) {
		body := map[string]any{
			"reasoning_effort": "low",
			"reasoning":        map[string]any{"effort": "max"},
		}
		normalizeResponsesReasoning(body)

		reasoning := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "max" {
			t.Errorf("effort = %v, want the existing max", reasoning["effort"])
		}
		if reasoning["summary"] != "auto" {
			t.Errorf("summary = %v, want auto", reasoning["summary"])
		}
		if _, ok := body["reasoning_effort"]; ok {
			t.Error("reasoning_effort should be deleted")
		}
	})

	t.Run("keeps explicit summary", func(t *testing.T) {
		body := map[string]any{
			"reasoning_effort": "high",
			"reasoning":        map[string]any{"effort": "high", "summary": "concise"},
		}
		normalizeResponsesReasoning(body)

		reasoning := body["reasoning"].(map[string]any)
		if reasoning["summary"] != "concise" {
			t.Errorf("summary = %v, want concise", reasoning["summary"])
		}
	})
}

func TestOpenCodeChatToResponsesCarriesReasoning(t *testing.T) {
	chatBody := []byte(`{
		"model": "deepseek-v4.1-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"reasoning_effort": "high"
	}`)
	converted := openCodeChatToResponses(chatBody)
	if converted == nil {
		t.Fatal("openCodeChatToResponses returned nil")
	}

	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", body["reasoning_effort"])
	}

	normalizeResponsesReasoning(body)
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatal("reasoning object not created")
	}
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Errorf("reasoning = %v, want effort high / summary auto", reasoning)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("reasoning_effort should be deleted")
	}
}
