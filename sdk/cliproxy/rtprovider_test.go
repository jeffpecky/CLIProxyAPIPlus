package cliproxy

import (
	"context"
	"net/http"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
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

// setupRelayWarnHook attaches a capture hook to the standard logger so the
// tests can assert on syncRoundTripperProvider's invalid relay-url warning.
// Mirrors the auth package's setupTestLoggerHook idiom: capture at Warn,
// restore level and hooks on cleanup. Not for parallel tests — it swaps the
// standard logger's hook set.
func setupRelayWarnHook(t *testing.T) *logtest.Hook {
	t.Helper()
	_, hook := logtest.NewNullLogger()
	oldLevel := log.GetLevel()
	log.SetLevel(log.WarnLevel)
	savedHooks := make(log.LevelHooks)
	for lvl, hs := range log.StandardLogger().Hooks {
		savedHooks[lvl] = append([]log.Hook(nil), hs...)
	}
	log.AddHook(hook)
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		log.StandardLogger().ReplaceHooks(savedHooks)
	})
	return hook
}

// TestSyncRoundTripperProviderWarnsOnInvalidRelayURL pins the service-layer
// warning for a non-empty but malformed relay-url: WrapRelay degrades to
// direct egress by design, so syncRoundTripperProvider must say so instead of
// staying silent. A valid relay-url (and an empty one) must stay quiet.
func TestSyncRoundTripperProviderWarnsOnInvalidRelayURL(t *testing.T) {
	invalid := []string{
		"ftp://relay.example.workers.dev", // scheme is not http/https
		"https://",                        // parseable but host is empty
		"http://[::1",                     // url.Parse error (missing ']')
	}
	for _, relay := range invalid {
		t.Run("invalid "+relay, func(t *testing.T) {
			hook := setupRelayWarnHook(t)
			service := &Service{
				coreManager: coreauth.NewManager(nil, nil, nil),
			}
			cfg := &internalconfig.Config{}
			cfg.RelayURL = relay

			hook.Reset()
			service.syncRoundTripperProvider(cfg)

			warns := 0
			for _, entry := range hook.AllEntries() {
				if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, "invalid relay-url") {
					continue
				}
				warns++
				if !strings.Contains(entry.Message, strings.TrimSpace(relay)) {
					t.Fatalf("warning %q does not quote the invalid relay-url %q", entry.Message, relay)
				}
				if !strings.Contains(entry.Message, "egress stays direct") {
					t.Fatalf("warning %q does not state that egress stays direct", entry.Message)
				}
			}
			if warns != 1 {
				t.Fatalf("invalid relay-url warnings = %d, want 1; logs: %#v", warns, hook.AllEntries())
			}
		})
	}

	t.Run("valid relay-url stays silent", func(t *testing.T) {
		hook := setupRelayWarnHook(t)
		service := &Service{
			coreManager: coreauth.NewManager(nil, nil, nil),
		}
		cfg := &internalconfig.Config{}
		cfg.RelayURL = "https://relay.example.workers.dev"

		hook.Reset()
		service.syncRoundTripperProvider(cfg)

		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "invalid relay-url") {
				t.Fatalf("valid relay-url must not warn, got: %s", entry.Message)
			}
		}
		if service.roundTripperProvider == nil || service.roundTripperProvider.relayURL != cfg.RelayURL {
			t.Fatalf("provider = %+v, want one scoped to %q", service.roundTripperProvider, cfg.RelayURL)
		}
	})
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
