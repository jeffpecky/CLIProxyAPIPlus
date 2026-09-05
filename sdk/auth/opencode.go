package auth

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// OpenCodeAuthenticator implements the Authenticator interface for opencode.
// Since opencode is a free service, this authenticator simply creates a
// placeholder auth record without requiring any credentials.
type OpenCodeAuthenticator struct{}

// NewOpenCodeAuthenticator creates a new opencode authenticator.
func NewOpenCodeAuthenticator() *OpenCodeAuthenticator {
	return &OpenCodeAuthenticator{}
}

// Provider returns the provider identifier.
func (a *OpenCodeAuthenticator) Provider() string {
	return "opencode"
}

// Login creates a basic auth record for the opencode provider.
// Since opencode doesn't require authentication, this is a no-op that
// creates a minimal auth record.
func (a *OpenCodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         "opencode-free",
		Provider:   "opencode",
		Label:      "OpenCode Free",
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{
			"header:Authorization":      "Bearer public",
			"header:User-Agent":         "opencode",
			"header:x-opencode-client":  "desktop",
			"header:x-opencode-project": "global",
		},
		Metadata: map[string]any{
			"email": "opencode-free",
			"type":  "free",
		},
	}
	return auth, nil
}

// RefreshLead returns nil since opencode doesn't support token refresh.
func (a *OpenCodeAuthenticator) RefreshLead() *time.Duration {
	return nil
}
