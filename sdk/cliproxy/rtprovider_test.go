package cliproxy

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestRoundTripperForDirectBypassesProxy(t *testing.T) {
	t.Parallel()

	provider := newDefaultRoundTripperProvider("")
	rt := provider.RoundTripperFor(&coreauth.Auth{ProxyURL: "direct"})
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", rt)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestApplyManagerConfigRefreshesRelayRoundTripperProvider(t *testing.T) {
	t.Parallel()

	// The provider Build() attaches is simulated here: a fresh instance scoped
	// to the old relay, exactly what builder.go installs at construction.
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{
		coreManager:          manager,
		roundTripperProvider: newDefaultRoundTripperProvider("https://relay-a.example.workers.dev"),
	}
	manager.SetRoundTripperProvider(service.roundTripperProvider)

	auth := &coreauth.Auth{ProxyURL: "http://hot-reload-proxy.example:8080"}

	// A hot config commit changes relay-url: applyManagerConfig must attach a
	// fresh provider scoped to the new relay, so ctx-injected transports stop
	// routing via the stale one.
	newCfg := &internalconfig.Config{}
	newCfg.RelayURL = "https://relay-b.example.workers.dev"
	if !service.applyManagerConfig(context.Background(), configCommit{cfg: newCfg, sequence: 1}) {
		t.Fatal("applyManagerConfig failed")
	}
	provider := service.roundTripperProvider
	if provider == nil {
		t.Fatal("roundTripperProvider not set after config apply")
	}
	if provider.relayURL != "https://relay-b.example.workers.dev" {
		t.Fatalf("provider relay = %q, want %q", provider.relayURL, "https://relay-b.example.workers.dev")
	}
	if rt := provider.RoundTripperFor(auth); !proxyutil.IsRelayRoundTripper(rt) {
		t.Fatalf("provider transport %T is not relay-wrapped after relay change", rt)
	}

	// A commit with an unchanged relay-url must keep the same provider
	// instance, preserving its transport cache.
	same := provider
	if !service.applyManagerConfig(context.Background(), configCommit{cfg: newCfg, sequence: 2}) {
		t.Fatal("applyManagerConfig failed")
	}
	if service.roundTripperProvider != same {
		t.Fatal("provider replaced despite unchanged relay-url (transport cache dropped)")
	}

	// Disabling the relay must again refresh the provider so transports stop
	// being relay-wrapped.
	disabled := &internalconfig.Config{}
	if !service.applyManagerConfig(context.Background(), configCommit{cfg: disabled, sequence: 3}) {
		t.Fatal("applyManagerConfig failed")
	}
	if service.roundTripperProvider == same {
		t.Fatal("provider not replaced after relay-url disabled")
	}
	if rt := service.roundTripperProvider.RoundTripperFor(auth); proxyutil.IsRelayRoundTripper(rt) {
		t.Fatalf("transport %T still relay-wrapped after relay-url disabled", rt)
	}
}

func TestRoundTripperProviderRelaySeparatesCacheAndWraps(t *testing.T) {
	t.Parallel()

	const proxyURL = "http://rtprovider-relay-proxy.example:8080"
	auth := &coreauth.Auth{ProxyURL: proxyURL}

	plain := newDefaultRoundTripperProvider("")
	relayed := newDefaultRoundTripperProvider("https://relay.example.workers.dev")
	other := newDefaultRoundTripperProvider("https://other-relay.example.workers.dev")

	plainRT := plain.RoundTripperFor(auth)
	relayedRT := relayed.RoundTripperFor(auth)
	otherRT := other.RoundTripperFor(auth)

	if plainRT == nil || relayedRT == nil || otherRT == nil {
		t.Fatalf("providers returned nil transports: %v %v %v", plainRT, relayedRT, otherRT)
	}
	if plainRT == relayedRT {
		t.Fatal("relay and non-relay providers share a transport")
	}
	if relayedRT == otherRT {
		t.Fatal("providers with distinct relay URLs share a transport")
	}
	if proxyutil.IsRelayRoundTripper(plainRT) {
		t.Fatal("plain provider transport must not be relay-wrapped")
	}
	if !proxyutil.IsRelayRoundTripper(relayedRT) {
		t.Fatalf("relayed provider transport %T is not relay-wrapped", relayedRT)
	}
	if relayed.RoundTripperFor(auth) != relayedRT {
		t.Fatal("relayed provider must cache and reuse its transport")
	}
}
