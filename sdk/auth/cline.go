package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ClineAuthenticator implements the OAuth login flow for Cline.
type ClineAuthenticator struct{}

// NewClineAuthenticator constructs a new Cline authenticator.
func NewClineAuthenticator() Authenticator {
	return &ClineAuthenticator{}
}

// Provider returns the provider key for cline.
func (ClineAuthenticator) Provider() string {
	return "cline"
}

// RefreshLead returns nil because Cline tokens do not expire.
func (ClineAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login initiates the Cline OAuth authentication.
func (a ClineAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := 1455
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", callbackPort)

	authSvc := cline.NewClineAuth(cfg)

	// Start local HTTP server
	oauthServer := cline.NewOAuthServer(callbackPort)
	if err := oauthServer.Start(); err != nil {
		return nil, fmt.Errorf("cline: failed to start OAuth server: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("cline oauth server stop error: %v", stopErr)
		}
	}()

	// Generate auth URL
	authURL, err := authSvc.GenerateAuthURL(redirectURI)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to generate auth URL: %w", err)
	}

	// Open browser
	if !opts.NoBrowser {
		fmt.Println("Opening browser for Cline authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Cline authentication callback...")

	// Wait for callback
	callbackCh := make(chan *cline.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)

	go func() {
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
	}()

	var result *cline.OAuthResult
	select {
	case result = <-callbackCh:
	case err = <-callbackErrCh:
		return nil, fmt.Errorf("cline: %w", err)
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("cline: timeout waiting for authentication callback")
	}

	if result.Error != "" {
		return nil, fmt.Errorf("cline: OAuth error: %s", result.Error)
	}

	// Exchange code for tokens
	authBundle, err := authSvc.ExchangeCodeForTokens(ctx, result.Code, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("cline: %w", err)
	}

	// Create token storage
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	// Build metadata
	metadata := map[string]any{
		"type":          "cline",
		"access_token":  authBundle.TokenData.AccessToken,
		"refresh_token": authBundle.TokenData.RefreshToken,
		"token_type":    "Bearer",
		"email":         authBundle.TokenData.Email,
		"timestamp":     time.Now().UnixMilli(),
	}

	if authBundle.TokenData.ExpiresAt > 0 {
		exp := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
		metadata["expired"] = exp
	}

	// Generate a unique filename
	fileName := fmt.Sprintf("cline-%d.json", time.Now().UnixMilli())

	fmt.Println("\nCline authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    authBundle.TokenData.Email,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
