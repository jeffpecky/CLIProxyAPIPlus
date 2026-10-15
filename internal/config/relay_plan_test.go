package config_test

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRelayPlanResolution(t *testing.T) {
	t.Parallel()

	cfg := &config.SDKConfig{
		RelayURL:      "https://legacy.example.workers.dev",
		RelayURLs:     []string{"https://relay-a.example.workers.dev", "https://relay-b.example.workers.dev"},
		RelayStrategy: "round-robin",
		RelayStrict:   true,
		RelayProviderURLs: map[string]string{
			" Claude ": "https://pinned.example.workers.dev",
			"codex":    "",
		},
	}

	plan := cfg.RelayPlan()

	// The rotation list wins over the legacy single relay-url.
	if !reflect.DeepEqual(plan.URLs, cfg.RelayURLs) {
		t.Fatalf("plan.URLs = %v, want %v", plan.URLs, cfg.RelayURLs)
	}
	if plan.Strategy != "round-robin" || !plan.Strict {
		t.Fatalf("plan = %+v, want round-robin and strict", plan)
	}

	// A provider pin short-circuits the global list; the empty codex pin is
	// dropped, so codex falls back to the rotation list.
	pinned := plan.Options("  claude ")
	if !reflect.DeepEqual(pinned.URLs, []string{"https://pinned.example.workers.dev"}) {
		t.Fatalf("claude options = %+v, want the pinned relay", pinned)
	}
	fallback := plan.Options("codex")
	if !reflect.DeepEqual(fallback.URLs, cfg.RelayURLs) {
		t.Fatalf("codex options = %+v, want the global list", fallback)
	}

	// Pins must change the identity, or a cached transport built for the
	// global list would keep serving the pinned provider.
	if plan.Identity("claude") == plan.Identity("codex") {
		t.Fatalf("pinned identity %q equals unpinned identity", plan.Identity("claude"))
	}
}

func TestRelayPlanFallsBackToLegacyRelayURL(t *testing.T) {
	t.Parallel()

	cfg := &config.SDKConfig{RelayURL: "  https://legacy.example.workers.dev  "}
	plan := cfg.RelayPlan()

	if !reflect.DeepEqual(plan.URLs, []string{"https://legacy.example.workers.dev"}) {
		t.Fatalf("plan.URLs = %v, want the trimmed legacy relay-url", plan.URLs)
	}
	if plan.Key() == (config.RelayPlan{}).Key() {
		t.Fatal("configured plan must not share a key with an empty plan")
	}
}

func TestRelayPlanEmptyConfigDisablesRelay(t *testing.T) {
	t.Parallel()

	var cfg *config.SDKConfig
	plan := cfg.RelayPlan()

	if len(plan.URLs) != 0 || plan.Strict {
		t.Fatalf("plan = %+v, want relay disabled", plan)
	}
	if identity := plan.Identity("gemini"); identity != "" {
		t.Fatalf("identity = %q, want empty", identity)
	}
	opts := plan.Options("gemini")
	if len(opts.URLs) != 0 || opts.Strict {
		t.Fatalf("options = %+v, want a disabled relay", opts)
	}
}
