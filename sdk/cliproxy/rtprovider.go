package cliproxy

import (
	"net/http"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. It caches transports per proxy URL string, scoped
// by the configured edge relay URL so relay toggles never share a transport.
type defaultRoundTripperProvider struct {
	mu       sync.RWMutex
	cache    map[string]http.RoundTripper
	relayURL string
}

func newDefaultRoundTripperProvider(relayURL string) *defaultRoundTripperProvider {
	return &defaultRoundTripperProvider{
		cache:    make(map[string]http.RoundTripper),
		relayURL: strings.TrimSpace(relayURL),
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
	// The relay is part of the transport's identity: key on proxy + relay so a
	// cached relay-wrapped or plain transport cannot leak across relay toggles.
	cacheKey := proxyStr + "|" + p.relayURL
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
	if p.relayURL != "" {
		transport = proxyutil.WrapRelay(built, p.relayURL)
	}
	p.mu.Lock()
	p.cache[cacheKey] = transport
	p.mu.Unlock()
	return transport
}
