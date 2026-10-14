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
}
