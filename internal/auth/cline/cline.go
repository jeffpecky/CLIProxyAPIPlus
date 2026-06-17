// Package cline provides authentication and token management for Cline API.
// It handles the OAuth2 authorization code flow with PKCE for secure authentication.
package cline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	// clineAuthURL is the authorization endpoint for Cline.
	clineAuthURL = "https://api.cline.bot/api/v1/auth/authorize"
	// clineTokenURL is the token endpoint for Cline.
	clineTokenURL = "https://api.cline.bot/api/v1/auth/token"
)

// ClineAuth handles Cline authentication flow.
type ClineAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewClineAuth creates a new ClineAuth service instance.
func NewClineAuth(cfg *config.Config) *ClineAuth {
	return NewClineAuthWithProxyURL(cfg, "")
}

// NewClineAuthWithProxyURL creates a new ClineAuth service instance.
// proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewClineAuthWithProxyURL(cfg *config.Config, proxyURL string) *ClineAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &ClineAuth{
		httpClient: util.SetProxy(&sdkCfg, &http.Client{}),
		cfg:        cfg,
	}
}

// GenerateAuthURL creates the OAuth authorization URL.
func (c *ClineAuth) GenerateAuthURL(redirectURI string) (string, error) {
	if strings.TrimSpace(redirectURI) == "" {
		return "", fmt.Errorf("redirect URI is required")
	}

	params := url.Values{
		"client_type":  {"extension"},
		"callback_url": {redirectURI},
		"redirect_uri": {redirectURI},
	}

	authURL := fmt.Sprintf("%s?%s", clineAuthURL, params.Encode())
	return authURL, nil
}

// ExchangeCodeForTokens exchanges an authorization code for access and refresh tokens.
// It first tries to base64 decode the code to see if it contains token data directly.
// If that fails, it POSTs to the token endpoint.
func (c *ClineAuth) ExchangeCodeForTokens(ctx context.Context, code string, redirectURI string) (*ClineAuthBundle, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("authorization code is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return nil, fmt.Errorf("redirect URI is required")
	}

	// Try base64 decode first
	tokenData, err := tryBase64DecodeCode(code)
	if err == nil && tokenData != nil {
		return &ClineAuthBundle{
			TokenData: tokenData,
		}, nil
	}

	// Fall back to POST to token endpoint
	return c.exchangeCodeViaPost(ctx, code, redirectURI)
}

// tryBase64DecodeCode attempts to decode the code as base64 and parse JSON.
func tryBase64DecodeCode(code string) (*ClineTokenData, error) {
	decoded, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		// Try URL-safe base64
		decoded, err = base64.URLEncoding.DecodeString(code)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64: %w", err)
		}
	}

	var tokenResp struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		Email        string `json:"email"`
		ExpiresAt    int64  `json:"expiresAt"`
	}

	if err = json.Unmarshal(decoded, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in decoded response")
	}

	return &ClineTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Email:        tokenResp.Email,
		ExpiresAt:    tokenResp.ExpiresAt,
	}, nil
}

// exchangeCodeViaPost exchanges the authorization code via POST to the token endpoint.
func (c *ClineAuth) exchangeCodeViaPost(ctx context.Context, code string, redirectURI string) (*ClineAuthBundle, error) {
	data := url.Values{
		"code":         {code},
		"redirect_uri": {redirectURI},
		"grant_type":   {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, clineTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Email        string `json:"email"`
		ExpiresAt    int64  `json:"expires_at"`
	}

	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response")
	}

	return &ClineAuthBundle{
		TokenData: &ClineTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			Email:        tokenResp.Email,
			ExpiresAt:    tokenResp.ExpiresAt,
		},
	}, nil
}

// CreateTokenStorage creates a new ClineTokenStorage from auth bundle.
func (c *ClineAuth) CreateTokenStorage(bundle *ClineAuthBundle) *ClineTokenStorage {
	expired := ""
	if bundle.TokenData.ExpiresAt > 0 {
		expired = time.Unix(bundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	return &ClineTokenStorage{
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		TokenType:    "Bearer",
		Email:        bundle.TokenData.Email,
		Expired:      expired,
		Type:         "cline",
	}
}
