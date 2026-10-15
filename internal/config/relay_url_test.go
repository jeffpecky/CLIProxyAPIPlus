package config

import "testing"

func TestRelayURLYAMLParse(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("relay-url: https://relay.example.workers.dev\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RelayURL != "https://relay.example.workers.dev" {
		t.Fatalf("got %q", cfg.RelayURL)
	}

	// The CCS cross-language serializer emits an explicitly empty scalar when
	// no relay is configured. Round-trip pin: `relay-url: ""` must parse to an
	// empty string (disabling the relay), not to a bogus value.
	t.Run("empty string literal", func(t *testing.T) {
		empty, errEmpty := ParseConfigBytes([]byte("relay-url: \"\"\n"))
		if errEmpty != nil {
			t.Fatalf("parse: %v", errEmpty)
		}
		if empty.RelayURL != "" {
			t.Fatalf("got %q, want empty string", empty.RelayURL)
		}
	})
}
