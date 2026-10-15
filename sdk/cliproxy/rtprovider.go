package cliproxy

import (
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. It caches transports per proxy URL string, scoped
// by the resolved relay plan so relay toggles, rotation changes, and
// per-provider pins never share a transport.
type defaultRoundTripperProvider struct {
	mu    sync.RWMutex
	cache map[string]http.RoundTripper
	plan  config.RelayPlan
}

func newDefaultRoundTripperProvider(plan config.RelayPlan) *defaultRoundTripperProvider {
	return &defaultRoundTripperProvider{
		cache: make(map[string]http.RoundTripper),
		plan:  plan,
	}
}

// RoundTripperFor implements coreauth.RoundTripperProvider.
func (p *defaultRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	proxyStr := strings.TrimSpace(auth.ProxyURL)
	if proxyStr == "" {
		return nil
	}
	providerKey := coreauth.ProviderKey(auth)
	relayOpts := p.plan.Options(providerKey)
	// The relay plan is part of the transport's identity: key on proxy + relay
	// identity so a cached relay-wrapped or plain transport cannot leak across
	// relay toggles, strategy edits, or provider pins.
	cacheKey := proxyStr + "|" + p.plan.Identity(providerKey)
	p.mu.RLock()
	rt := p.cache[cacheKey]
	p.mu.RUnlock()
	if rt != nil {
		return rt
	}
	built, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	if built == nil {
		return nil
	}
	var transport http.RoundTripper = built
	if len(relayOpts.URLs) > 0 || relayOpts.Strict {
		transport = proxyutil.WrapRelayWith(built, relayOpts)
	}
	p.mu.Lock()
	p.cache[cacheKey] = transport
	p.mu.Unlock()
	return transport
}
