package config

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// RelayPlan is an immutable snapshot of every relay setting that affects
// transport construction: the global candidate list, the rotation strategy,
// the strict flag, and the per-provider pins. A single plan is resolved once
// per config load and then handed to transport builders, so a mid-flight
// config edit cannot produce a client built from half of one relay config and
// half of another.
type RelayPlan struct {
	// URLs are the global candidates in priority order.
	URLs []string
	// Strategy is the rotation strategy: "", "round-robin", or "random".
	Strategy string
	// Strict fails closed when no candidate resolves.
	Strict bool
	// Pins maps lower-cased provider name to a single relay URL that
	// short-circuits the global list for that provider.
	Pins map[string]string
}

// RelayPlan snapshots the current relay configuration for transport building.
func (c *SDKConfig) RelayPlan() RelayPlan {
	if c == nil {
		return RelayPlan{}
	}
	var pins map[string]string
	for provider, pin := range c.RelayProviderURLs {
		trimmed := strings.TrimSpace(pin)
		if provider = normalizeRelayProvider(provider); provider == "" || trimmed == "" {
			continue
		}
		if pins == nil {
			pins = make(map[string]string, len(c.RelayProviderURLs))
		}
		pins[provider] = trimmed
	}
	return RelayPlan{
		URLs:     relayBaseCandidates(c),
		Strategy: strings.TrimSpace(c.RelayStrategy),
		Strict:   c.RelayStrict,
		Pins:     pins,
	}
}

// relayBaseCandidates resolves the provider-agnostic candidate list: the
// rotation list when present, otherwise the legacy single relay-url. Invalid
// entries are filtered later by the transport, which is the layer that knows
// whether a bad URL still has to degrade to direct egress or fail closed.
func relayBaseCandidates(c *SDKConfig) []string {
	if urls := c.RelayURLs; len(urls) > 0 {
		return urls
	}
	if trimmed := strings.TrimSpace(c.RelayURL); trimmed != "" {
		return []string{trimmed}
	}
	return nil
}

// Options resolves the transport-level relay selection for a provider: a pin
// short-circuits the global list, everything else inherits the plan's
// strategy and strict flag.
func (p RelayPlan) Options(provider string) proxyutil.RelayOptions {
	if pin := p.Pins[normalizeRelayProvider(provider)]; pin != "" {
		return proxyutil.RelayOptions{URLs: []string{pin}, Strategy: p.Strategy, Strict: p.Strict}
	}
	return proxyutil.RelayOptions{URLs: p.URLs, Strategy: p.Strategy, Strict: p.Strict}
}

// Identity returns the canonical transport cache-key fragment for a provider's
// relay egress. Distinct candidate sets, strategies, or strict flags must
// produce distinct keys, or a cached transport built for one relay setting
// would keep serving another.
func (p RelayPlan) Identity(provider string) string {
	opts := p.Options(provider)
	return proxyutil.RelayIdentity(opts.URLs, opts.Strategy, opts.Strict)
}

// Key returns a stable description of the whole plan. Config reloads compare
// it to decide whether transports must be rebuilt; equal keys keep the
// existing connection pools.
func (p RelayPlan) Key() string {
	pins := make([]string, 0, len(p.Pins))
	for provider, pin := range p.Pins {
		pins = append(pins, provider+"="+pin)
	}
	sort.Strings(pins)
	return proxyutil.RelayIdentity(p.URLs, p.Strategy, p.Strict) + " {" + strings.Join(pins, ",") + "}"
}

// Providers returns the pinned providers in sorted order, for diagnostics and
// change-diff logging.
func (p RelayPlan) Providers() []string {
	providers := make([]string, 0, len(p.Pins))
	for provider := range p.Pins {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func normalizeRelayProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// SanitizeRelayConfig normalizes the relay section after a YAML load: it
// trims candidates and pins, drops empty and duplicate entries, lower-cases
// pin keys, and blanks an unrecognized rotation strategy so a typo behaves
// like "none" (single active candidate) rather than inventing a mode.
func (c *SDKConfig) SanitizeRelayConfig() {
	if c == nil {
		return
	}
	c.RelayURL = strings.TrimSpace(c.RelayURL)

	if len(c.RelayURLs) > 0 {
		seen := make(map[string]struct{}, len(c.RelayURLs))
		normalized := make([]string, 0, len(c.RelayURLs))
		for _, raw := range c.RelayURLs {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			if _, duplicate := seen[trimmed]; duplicate {
				continue
			}
			seen[trimmed] = struct{}{}
			normalized = append(normalized, trimmed)
		}
		c.RelayURLs = normalized
	}

	switch strategy := strings.ToLower(strings.TrimSpace(c.RelayStrategy)); strategy {
	case "":
		c.RelayStrategy = ""
	case proxyutil.RelayStrategyNone, proxyutil.RelayStrategyRoundRobin, proxyutil.RelayStrategyRandom:
		c.RelayStrategy = strategy
	default:
		c.RelayStrategy = ""
	}

	if len(c.RelayProviderURLs) > 0 {
		pins := make(map[string]string, len(c.RelayProviderURLs))
		for provider, pin := range c.RelayProviderURLs {
			provider = normalizeRelayProvider(provider)
			pin = strings.TrimSpace(pin)
			if provider == "" || pin == "" {
				continue
			}
			pins[provider] = pin
		}
		c.RelayProviderURLs = pins
	}
}
