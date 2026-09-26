package config

import "testing"

func TestAPIKeyProviderModelEntries(t *testing.T) {
	cfg := &Config{
		NVIDIAKey:     []CodexKey{{APIKey: " nv-key "}},
		OpenRouterKey: []CodexKey{{APIKey: "or-key"}, {APIKey: " "}},
		OpenCodeGoKey: []CodexKey{{APIKey: "ocg-key"}},
		CloudflareKey: []CodexKey{
			{APIKey: "cf-key", BaseURL: "cf-account-id"},
			{APIKey: "cf-key-2", BaseURL: "https://api.cloudflare.com/client/v4/accounts/acct-123/"},
			{APIKey: "cf-key-3", BaseURL: "https://example.com/no-account"},
			{APIKey: " "},
		},
	}
	nvidia, cloudflare, openrouter, openCodeGo := cfg.APIKeyProviderModelEntries()
	if len(nvidia) != 1 || nvidia[0].APIKey != "nv-key" {
		t.Fatalf("nvidia = %+v, want one trimmed key", nvidia)
	}
	if len(openrouter) != 1 || openrouter[0].APIKey != "or-key" {
		t.Fatalf("openrouter = %+v, want one non-empty key", openrouter)
	}
	if len(openCodeGo) != 1 || openCodeGo[0].APIKey != "ocg-key" {
		t.Fatalf("openCodeGo = %+v, want one key", openCodeGo)
	}
	if len(cloudflare) != 2 {
		t.Fatalf("cloudflare len = %d, want 2", len(cloudflare))
	}
	if cloudflare[0].AccountID != "cf-account-id" {
		t.Fatalf("cloudflare[0].AccountID = %q, want bare base-url account id", cloudflare[0].AccountID)
	}
	if cloudflare[1].AccountID != "acct-123" {
		t.Fatalf("cloudflare[1].AccountID = %q, want path account id", cloudflare[1].AccountID)
	}
}

func TestAPIKeyProviderModelEntriesNil(t *testing.T) {
	var cfg *Config
	nvidia, cloudflare, openrouter, openCodeGo := cfg.APIKeyProviderModelEntries()
	if nvidia != nil || cloudflare != nil || openrouter != nil || openCodeGo != nil {
		t.Fatalf("nil Config returned non-nil entries: %v %v %v %v", nvidia, cloudflare, openrouter, openCodeGo)
	}
}

func TestCloudflareAccountIDFromBaseURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "  ", want: ""},
		{in: "account-abc", want: "account-abc"},
		{in: "https://api.cloudflare.com/client/v4/accounts/acct-9/", want: "acct-9"},
		{in: "https://api.cloudflare.com/client/v4/accounts/acct-9/ai", want: "acct-9"},
		{in: "https://example.com/nope", want: ""},
	}
	for _, tt := range tests {
		if got := cloudflareAccountIDFromBaseURL(tt.in); got != tt.want {
			t.Fatalf("cloudflareAccountIDFromBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
