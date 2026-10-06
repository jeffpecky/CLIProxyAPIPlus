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
	if lead == nil || *lead != 24*time.Hour {
		t.Fatalf("ProviderRefreshLead(codex) = %v, want 24h", lead)
	}
	maxAge := cliproxyauth.ProviderRefreshMaxAge("codex", nil)
	if maxAge == nil || *maxAge != 8*24*time.Hour {
		t.Fatalf("ProviderRefreshMaxAge(codex) = %v, want 192h", maxAge)
	}
}

func TestProviderRefreshLeads(t *testing.T) {
	tests := []struct {
		name          string
		authenticator Authenticator
		want          time.Duration
		wantNil       bool
	}{
		{name: "codex", authenticator: NewCodexAuthenticator(), want: 24 * time.Hour},
		{name: "claude", authenticator: NewClaudeAuthenticator(), want: 4 * time.Hour},
		{name: "antigravity", authenticator: NewAntigravityAuthenticator(), want: 30 * time.Minute},
		{name: "kimi", authenticator: NewKimiAuthenticator(), want: 5 * time.Minute},
		{name: "kimi-ai", authenticator: NewKimiAIAuthenticator(), want: 5 * time.Minute},
		{name: "kimi.ai", authenticator: NewKimiAIDotAuthenticator(), want: 5 * time.Minute},
		{name: "xai", authenticator: NewXAIAuthenticator(), want: 5 * time.Minute},
		{name: "devin", authenticator: NewDevinAuthenticator(), wantNil: true},
		{name: "meta", authenticator: NewMetaAuthenticator(), wantNil: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.authenticator.Provider(); got != test.name {
				t.Fatalf("Provider() = %q, want %q", got, test.name)
			}
			lead := test.authenticator.RefreshLead()
			if test.wantNil {
				if lead != nil {
					t.Fatalf("RefreshLead() = %v, want nil", lead)
				}
				return
			}
			if lead == nil || *lead != test.want {
				t.Fatalf("RefreshLead() = %v, want %v", lead, test.want)
			}
		})
	}
}
