package registry

import "testing"

func TestOpenCodeThinkingSupport(t *testing.T) {
	cases := []struct {
		model  string
		levels []string
	}{
		// Present in https://opencode.ai/zen/v1/models.
		{"deepseek-v4.1-flash-free", []string{"low", "high", "max"}},
		{"glm-5.3-flash-free", []string{"minimal", "low", "medium", "high", "xhigh"}},
		{"mimo-v2.6-flash-free", []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
		{"big-pickle", []string{"none", "minimal", "low", "medium", "high", "xhigh"}},
		{"nemotron-3.5-super-vl-free", []string{"none", "low", "medium", "high"}},
		// Family rules (catalogue may expose these as paid ids too).
		{"deepseek-v4-flash", []string{"none", "high", "max"}},
		{"deepseek-v4-pro", []string{"none", "high", "max"}},
		{"deepseek-v4.1-flash", []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"kimi-k2.7-code", []string{"none", "low", "medium", "high", "max"}},
		{"mimo-v2.5-pro", []string{"none", "low", "medium", "high", "xhigh"}},
		{"space-bunny-free", []string{"none", "minimal", "low", "medium", "high", "xhigh"}},
		// No reasoning metadata upstream.
		{"beam-free", nil},
		{"flint-40b-free", nil},
		{"jev-1.13-free", nil},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := openCodeThinkingSupport(tc.model)
			if tc.levels == nil {
				if got != nil {
					t.Fatalf("openCodeThinkingSupport(%q) = %v, want nil", tc.model, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("openCodeThinkingSupport(%q) = nil, want %v", tc.model, tc.levels)
			}
			if len(got.Levels) != len(tc.levels) {
				t.Fatalf("levels = %v, want %v", got.Levels, tc.levels)
			}
			for i := range tc.levels {
				if got.Levels[i] != tc.levels[i] {
					t.Errorf("levels = %v, want %v", got.Levels, tc.levels)
				}
			}
		})
	}
}
