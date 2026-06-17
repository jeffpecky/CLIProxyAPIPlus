package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xiaomimimo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// xiaomiMimoRefreshLead is the duration before token expiry when refresh should occur.
var xiaomiMimoRefreshLead = 24 * time.Hour

// XiaomiMimoAuthenticator implements ECDH OAuth login for Xiaomi MiMo.
type XiaomiMimoAuthenticator struct{}

// NewXiaomiMimoAuthenticator constructs a new Xiaomi MiMo authenticator.
func NewXiaomiMimoAuthenticator() Authenticator {
	return &XiaomiMimoAuthenticator{}
}

// Provider returns the provider key for xiaomi-mimo.
func (XiaomiMimoAuthenticator) Provider() string {
	return "xiaomi-mimo"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
func (XiaomiMimoAuthenticator) RefreshLead() *time.Duration {
	return &xiaomiMimoRefreshLead
}

// Login initiates the Xiaomi MiMo ECDH OAuth flow.
func (a XiaomiMimoAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := xiaomimimo.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	publicKey, privateKeyDer, err := xiaomimimo.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("xiaomi-mimo: key generation failed: %w", err)
	}

	keyName := xiaomimimo.GenerateKeyName()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/", callbackPort)
	authURL := xiaomimimo.BuildAuthorizeURL(publicKey, redirectURI, keyName)

	// Start local OAuth server to receive callback
	codeCh := make(chan string, 1)

	oauthServer, err := xiaomimimo.StartOAuthServer(callbackPort, func(code string) {
		codeCh <- code
	})
	if err != nil {
		return nil, fmt.Errorf("xiaomi-mimo: failed to start OAuth server: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if errShutdown := oauthServer.Shutdown(shutdownCtx); errShutdown != nil {
			log.Warnf("xiaomi-mimo oauth server shutdown error: %v", errShutdown)
		}
	}()

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", authURL)

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(authURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		} else {
			fmt.Println("No browser available; please open the URL manually.")
		}
	} else {
		fmt.Println("Please open the URL above in your browser.")
	}

	fmt.Println("Waiting for authorization...")

	var encryptedBase64 string
	select {
	case encryptedBase64 = <-codeCh:
	case <-ctx.Done():
		return nil, fmt.Errorf("xiaomi-mimo: context cancelled: %w", ctx.Err())
	case <-time.After(15 * time.Minute):
		return nil, fmt.Errorf("xiaomi-mimo: authentication timed out")
	}

	sk, uid, baseURL, errDecrypt := xiaomimimo.Decrypt(privateKeyDer, encryptedBase64)
	if errDecrypt != nil {
		return nil, fmt.Errorf("xiaomi-mimo: decrypt failed: %w", errDecrypt)
	}

	if baseURL == "" {
		baseURL = xiaomimimo.PlatformURL
	}

	tokenStorage := &xiaomimimo.XiaomiMimoTokenStorage{
		AccessToken: sk,
		SK:          sk,
		UID:         uid,
		BaseURL:     baseURL,
		TokenType:   "Bearer",
		Type:        "xiaomi-mimo",
		Expired:     time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}

	fileName := fmt.Sprintf("xiaomi-mimo-%s-%d.json", uid, time.Now().Unix())
	label := strings.TrimSpace(uid)
	if label == "" {
		label = "Xiaomi MiMo User"
	}

	metadata := map[string]any{
		"type":       "xiaomi-mimo",
		"sk":         sk,
		"uid":        uid,
		"base_url":   baseURL,
		"token_type": "Bearer",
		"expired":    tokenStorage.Expired,
	}

	fmt.Println("Xiaomi MiMo authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
