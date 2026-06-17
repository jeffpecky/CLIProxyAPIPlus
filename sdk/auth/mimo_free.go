package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/mimofree"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// mimoFreeRefreshLead is the duration before token expiry when refresh should occur.
var mimoFreeRefreshLead = 24 * time.Hour

// MimoFreeAuthenticator implements auto-bootstrap login for MiMo Free.
type MimoFreeAuthenticator struct{}

// NewMimoFreeAuthenticator constructs a new MiMo Free authenticator.
func NewMimoFreeAuthenticator() Authenticator {
	return &MimoFreeAuthenticator{}
}

// Provider returns the provider key for mimo-free.
func (MimoFreeAuthenticator) Provider() string {
	return "mimo-free"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
func (MimoFreeAuthenticator) RefreshLead() *time.Duration {
	return &mimoFreeRefreshLead
}

// Login auto-bootstraps credentials without user interaction.
func (a MimoFreeAuthenticator) Login(ctx context.Context, cfg *config.Config, _ *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	deviceID := mimofree.GenerateDeviceID()
	fmt.Printf("Bootstrapping MiMo Free with device ID: %s\n", deviceID)

	bootstrapResp, err := mimofree.Bootstrap(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("mimo-free: bootstrap failed: %w", err)
	}

	baseURL := bootstrapResp.BaseURL
	if baseURL == "" {
		baseURL = mimofree.PlatformURL
	}

	expired := ""
	if bootstrapResp.ExpiresIn > 0 {
		expired = time.Now().Add(time.Duration(bootstrapResp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	tokenStorage := &mimofree.MimoFreeTokenStorage{
		AccessToken:  bootstrapResp.AccessToken,
		RefreshToken: bootstrapResp.RefreshToken,
		TokenType:    bootstrapResp.TokenType,
		Expired:      expired,
		Type:         "mimo-free",
		BaseURL:      baseURL,
		DeviceID:     deviceID,
	}

	fileName := fmt.Sprintf("mimo-free-%s-%d.json", deviceID, time.Now().Unix())
	label := "MiMo Free"

	metadata := map[string]any{
		"type":          "mimo-free",
		"access_token":  bootstrapResp.AccessToken,
		"refresh_token": bootstrapResp.RefreshToken,
		"token_type":    bootstrapResp.TokenType,
		"base_url":      baseURL,
		"device_id":     deviceID,
		"expired":       expired,
	}

	fmt.Println("MiMo Free authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
