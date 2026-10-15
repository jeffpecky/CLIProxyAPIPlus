package config

import (
	"reflect"
	"testing"
)

func TestRelayMultiYAMLParseAndSanitize(t *testing.T) {
	raw := `relay-url: https://legacy.example.workers.dev
relay-urls:
  - https://relay-a.example.workers.dev
  - "  https://relay-b.example.workers.dev  "
  - https://relay-a.example.workers.dev
  - ""
relay-strategy: ROUND-ROBIN
relay-strict: true
relay-provider-urls:
  Claude: https://pinned.example.workers.dev
  codex: " "
`
	cfg, err := ParseConfigBytes([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Trimmed, deduplicated, and emptied entries dropped: a duplicate in the
	// list would silently halve round-robin's spread.
	wantURLs := []string{
		"https://relay-a.example.workers.dev",
		"https://relay-b.example.workers.dev",
	}
	if !reflect.DeepEqual(cfg.RelayURLs, wantURLs) {
		t.Fatalf("relay-urls = %v, want %v", cfg.RelayURLs, wantURLs)
	}
	if cfg.RelayURL != "https://legacy.example.workers.dev" {
		t.Fatalf("relay-url = %q, want the untouched legacy value", cfg.RelayURL)
	}
	if cfg.RelayStrategy != "round-robin" {
		t.Fatalf("relay-strategy = %q, want lower-cased round-robin", cfg.RelayStrategy)
	}
	if !cfg.RelayStrict {
		t.Fatal("relay-strict = false, want true")
	}

	wantPins := map[string]string{"claude": "https://pinned.example.workers.dev"}
	if !reflect.DeepEqual(cfg.RelayProviderURLs, wantPins) {
		t.Fatalf("relay-provider-urls = %v, want %v (lower-cased key, empty pin dropped)", cfg.RelayProviderURLs, wantPins)
	}

	// The rotation list must win over the legacy single URL once it is present.
	plan := cfg.RelayPlan()
	if !reflect.DeepEqual(plan.URLs, wantURLs) {
		t.Fatalf("plan URLs = %v, want %v", plan.URLs, wantURLs)
	}
	if !reflect.DeepEqual(plan.Options("gemini").URLs, wantURLs) {
		t.Fatalf("unpinned provider options = %+v, want the rotation list", plan.Options("gemini"))
	}
	if got := plan.Options("claude").URLs; !reflect.DeepEqual(got, []string{wantPins["claude"]}) {
		t.Fatalf("pinned provider options = %v, want the pinned relay %v", got, wantPins["claude"])
	}
}

func TestSanitizeRelayConfigBlanksUnknownStrategy(t *testing.T) {
	raw := "relay-strategy: fastest\nrelay-urls:\n  - https://relay-a.example.workers.dev\n"
	cfg, err := ParseConfigBytes([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RelayStrategy != "" {
		t.Fatalf("relay-strategy = %q, want blanked so an unknown mode behaves like none", cfg.RelayStrategy)
	}
	if plan := cfg.RelayPlan(); plan.Options("gemini").Strategy != "" {
		t.Fatalf("plan strategy = %q, want none", plan.Options("gemini").Strategy)
	}
}
