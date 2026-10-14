package util

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestSetProxyRelayWrapsDefaultTransportWhenTransportNil(t *testing.T) {
	cfg := &config.SDKConfig{}
	cfg.RelayURL = "https://relay-setproxy-test.example.workers.dev"

	client := SetProxy(cfg, &http.Client{})
	if !proxyutil.IsRelayRoundTripper(client.Transport) {
		t.Fatalf("transport %T is not relay-wrapped", client.Transport)
	}

	// Zero-relay configs must leave the client untouched.
	plain := SetProxy(&config.SDKConfig{}, &http.Client{})
	if plain.Transport != nil {
		t.Fatalf("zero-relay SetProxy produced transport %T, want none", plain.Transport)
	}
}
