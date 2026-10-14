package cliproxy

import (
	"net/http"
	"testing"

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
