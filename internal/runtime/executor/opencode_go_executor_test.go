package executor

import (
	"testing"
)

func TestOpenCodeGoNeedsResponsesOnlyModels(t *testing.T) {
	cases := map[string]bool{
		"grok-4.6":                   true,
		"gpt-5.6-luna":               true,
		"gpt-5.6-luna(high)":         true,
		"muse-spark-1.2-contributor": true,
		"muse-spark-1.3-contributor": true,
		"opencode-go/grok-4.6":       true,
		"glm-5.3":                    false,
		"minimax-m3":                 false,
		"deepseek-v4-pro":            false,
	}
	for model, want := range cases {
		if got := openCodeGoNeedsResponses(model); got != want {
			t.Errorf("openCodeGoNeedsResponses(%q) = %v, want %v", model, got, want)
		}
	}
}
