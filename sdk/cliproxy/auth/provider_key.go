package auth

import "strings"

// ProviderKey returns the normalized provider name of a credential ("claude",
// "codex", "gemini", ...) or "" when auth is missing. Layers that select
// behavior per provider — relay pinning, capability lookups — use it so casing
// or stray whitespace in a credential file cannot split one provider into two
// selections.
func ProviderKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}
