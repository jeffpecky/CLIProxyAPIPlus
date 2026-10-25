package executor

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestNormalizeOpencodeReasoning(t *testing.T) {
	// Register a test model with thinking levels
	testModel := "opencode/test-model-free"
	registry.GetGlobalRegistry().RegisterClient("test-client", "opencode", []*registry.ModelInfo{
		{
			ID:   testModel,
			Type: "opencode",
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "high", "max"},
			},
		},
	})
	defer registry.GetGlobalRegistry().UnregisterClient("test-client")

	tests := []struct {
		name             string
		model            string
		inputBody        string
		wantEffort       string
		wantHasReasoning bool
		wantHasEffort    bool
		wantSummary      string
	}{
		{
			name:             "reasoning_effort high -> reasoning:{effort:high,summary:auto}",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			wantEffort:       "high",
			wantHasReasoning: true,
			wantHasEffort:    false, // reasoning_effort is deleted
		},
		{
			name:             "reasoning_effort max clamped to max (supported)",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max"}`,
			wantEffort:       "max",
			wantHasReasoning: true,
			wantHasEffort:    false, // reasoning_effort is deleted
		},
		{
			name:             "reasoning_effort ultra clamped to max (supported)",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"ultra"}`,
			wantEffort:       "max",
			wantHasReasoning: true,
			wantHasEffort:    false, // reasoning_effort is deleted
		},
		{
			name:             "reasoning_effort xhigh clamped to high (not supported)",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			wantEffort:       "high", // xhigh not in supported, falls to high
			wantHasReasoning: true,
			wantHasEffort:    false, // reasoning_effort is deleted
		},
		{
			name:             "reasoning_effort auto removed (dropAdaptiveReasoningEffort)",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			wantEffort:       "",
			wantHasReasoning: false,
			wantHasEffort:    false,
		},
		{
			name:             "existing reasoning object with effort updated, summary preserved",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"low","summary":"concise"}}`,
			wantEffort:       "low",
			wantHasReasoning: true,
			wantHasEffort:    false, // reasoning_effort is deleted
			wantSummary:      "concise",
		},
		{
			name:             "no reasoning_effort -> no change",
			model:            testModel,
			inputBody:        `{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}]}`,
			wantEffort:       "",
			wantHasReasoning: false,
			wantHasEffort:    false,
		},
		{
			name:             "model with no thinking levels configured -> passthrough",
			model:            "opencode/unknown-model",
			inputBody:        `{"model":"opencode/unknown-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			wantEffort:       "high",
			wantHasReasoning: true,
			wantHasEffort:    false, // reasoning_effort is deleted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal([]byte(tt.inputBody), &body); err != nil {
				t.Fatalf("unmarshal input: %v", err)
			}

			normalizeOpencodeReasoning(tt.model, body)
			dropAdaptiveReasoningEffort(body)

			// Check reasoning object
			hasReasoning := false
			effort := ""
			summary := ""
			if reasoning, ok := body["reasoning"].(map[string]any); ok {
				hasReasoning = true
				if v, ok := reasoning["effort"].(string); ok {
					effort = v
				}
				if v, ok := reasoning["summary"].(string); ok {
					summary = v
				}
				// Check summary matches expected (default "auto" if not specified)
				expectedSummary := "auto"
				if tt.wantSummary != "" {
					expectedSummary = tt.wantSummary
				}
				if summary != expectedSummary {
					t.Errorf("summary = %v, want %v", summary, expectedSummary)
				}
			}

			// Check reasoning_effort is deleted
			_, hasEffort := body["reasoning_effort"]

			if tt.wantHasReasoning != hasReasoning {
				t.Errorf("hasReasoning = %v, want %v", hasReasoning, tt.wantHasReasoning)
			}
			if tt.wantHasEffort != hasEffort {
				t.Errorf("hasEffort = %v, want %v (body: %v)", hasEffort, tt.wantHasEffort, body)
			}
			if tt.wantEffort != "" && effort != tt.wantEffort {
				t.Errorf("effort = %q, want %q", effort, tt.wantEffort)
			}
		})
	}
}

func TestNormalizeOpencodeReasoningWithModelSuffix(t *testing.T) {
	// Test with model suffix like (high)
	testModel := "opencode/test-model-free"
	registry.GetGlobalRegistry().RegisterClient("test-client2", "opencode", []*registry.ModelInfo{
		{
			ID:   testModel,
			Type: "opencode",
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "high"},
			},
		},
	})
	defer registry.GetGlobalRegistry().UnregisterClient("test-client2")

	body := map[string]any{
		"model":            testModel + "(high)",
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
		"reasoning_effort": "max",
	}

	normalizeOpencodeReasoning(testModel+"(high)", body)
	dropAdaptiveReasoningEffort(body)

	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatal("reasoning object not created")
	}
	effort, _ := reasoning["effort"].(string)
	// max should be clamped to high (highest supported)
	if effort != "high" {
		t.Errorf("effort = %q, want 'high' (clamped from max)", effort)
	}
}
