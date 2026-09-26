package registry

import "testing"

func TestOpenCodeGoFallbackModelCatalog(t *testing.T) {
	models := openCodeGoFallbackModels()
	if len(models) < 20 {
		t.Fatalf("fallback models = %d, want >= 20", len(models))
	}
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if model == nil || model.ID == "" {
			t.Fatalf("fallback model entry is empty: %+v", model)
		}
		if model.OwnedBy != "opencode-go" {
			t.Errorf("model %s OwnedBy = %q, want opencode-go", model.ID, model.OwnedBy)
		}
		if seen[model.ID] {
			t.Errorf("duplicate fallback model %s", model.ID)
		}
		seen[model.ID] = true
	}
	for _, id := range []string{"glm-5.3", "minimax-m3", "grok-4.6", "deepseek-v4-pro"} {
		if !seen[id] {
			t.Errorf("fallback catalog missing %s", id)
		}
	}
}
