package auth

import (
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRefreshRegistry_IFlowLeadRegistered(t *testing.T) {
	lead := cliproxyauth.ProviderRefreshLead("iflow", nil)
	if lead == nil {
		t.Fatalf("ProviderRefreshLead(iflow) = nil, want the authenticator's 24h lead")
	}
	if *lead != 24*time.Hour {
		t.Fatalf("ProviderRefreshLead(iflow) = %s, want 24h", *lead)
	}
}

func TestRefreshRegistry_CodexLeadAndMaxAgeRegistered(t *testing.T) {
	lead := cliproxyauth.ProviderRefreshLead("codex", nil)
	if lead == nil || *lead != 5*24*time.Hour {
		t.Fatalf("ProviderRefreshLead(codex) = %v, want 120h", lead)
	}
	maxAge := cliproxyauth.ProviderRefreshMaxAge("codex", nil)
	if maxAge == nil || *maxAge != 8*24*time.Hour {
		t.Fatalf("ProviderRefreshMaxAge(codex) = %v, want 192h", maxAge)
	}
}
