package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qwen"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// qwenRefreshLead is the duration before token expiry when refresh should occur.
var qwenRefreshLead = 5 * time.Minute

// QwenAuthenticator implements the OAuth device flow login for Qwen.
type QwenAuthenticator struct{}

// NewQwenAuthenticator constructs a new Qwen authenticator.
func NewQwenAuthenticator() Authenticator {
	return &QwenAuthenticator{}
}

// Provider returns the provider key for qwen.
func (QwenAuthenticator) Provider() string {
	return "qwen"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
// Qwen tokens expire and need to be refreshed before expiry.
func (QwenAuthenticator) RefreshLead() *time.Duration {
	return &qwenRefreshLead
}

// Login initiates the Qwen device flow authentication.
func (a QwenAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := qwen.NewQwenAuth(cfg)

	// Start the device flow
	fmt.Println("Starting Qwen authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("qwen: failed to start device flow: %w", err)
	}

	// Display the verification URL
	verificationURL := deviceCode.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = deviceCode.VerificationURI
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("User code: %s\n\n", deviceCode.UserCode)
	}

	// Try to open the browser automatically
	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	// Wait for user authorization
	authBundle, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("qwen: %w", err)
	}

	// Create the token storage
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	// Build metadata with token information
	metadata := map[string]any{
		"type":          "qwen",
		"access_token":  authBundle.TokenData.AccessToken,
		"refresh_token": authBundle.TokenData.RefreshToken,
		"token_type":    authBundle.TokenData.TokenType,
		"scope":         authBundle.TokenData.Scope,
		"timestamp":     time.Now().UnixMilli(),
	}

	if authBundle.TokenData.ExpiresAt > 0 {
		exp := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
		metadata["expired"] = exp
	}
	if strings.TrimSpace(authBundle.DeviceID) != "" {
		metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
	}

	// Generate a unique filename
	fileName := fmt.Sprintf("qwen-%d.json", time.Now().UnixMilli())

	fmt.Println("\nQwen authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    "Qwen User",
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
