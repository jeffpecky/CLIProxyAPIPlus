// Package openaiprovider provides authentication and token management for OpenAI's native services.
// It handles the OAuth2 flow, including generating authorization URLs, exchanging
// authorization codes for tokens, and refreshing expired tokens. The package also
// defines data structures for storing and managing OpenAI authentication credentials.
package openaiprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

// OAuth configuration constants for OpenAI native provider
const (
	AuthURL     = "https://auth.openai.com/oauth/authorize"
	TokenURL    = "https://auth.openai.com/oauth/token"
	ClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectURI = "http://localhost:1455/auth/callback"
)

// PKCECodes holds the verification codes for the OAuth2 PKCE (Proof Key for Code Exchange) flow.
// PKCE is an extension to the Authorization Code flow to prevent CSRF and authorization code injection attacks.
type PKCECodes struct {
	// CodeVerifier is the cryptographically random string used to correlate
	// the authorization request to the token request
	CodeVerifier string `json:"code_verifier"`
	// CodeChallenge is the SHA256 hash of the code verifier, base64url-encoded
	CodeChallenge string `json:"code_challenge"`
}

// OpenAITokenData holds the OAuth token information obtained from OpenAI.
// It includes the ID token, access token, refresh token, and associated user details.
type OpenAITokenData struct {
	// IDToken is the JWT ID token containing user claims
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// AccountID is the OpenAI account identifier
	AccountID string `json:"account_id"`
	// Email is the OpenAI account email
	Email string `json:"email"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
}

// OpenAIAuthBundle aggregates all authentication-related data after the OAuth flow is complete.
// This includes the API key, token data, and the timestamp of the last refresh.
type OpenAIAuthBundle struct {
	// APIKey is the OpenAI API key obtained from token exchange
	APIKey string `json:"api_key"`
	// TokenData contains the OAuth tokens from the authentication flow
	TokenData OpenAITokenData `json:"token_data"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
}

// OpenAIAuth handles the OpenAI OAuth2 authentication flow.
// It manages the HTTP client and provides methods for generating authorization URLs,
// exchanging authorization codes for tokens, and refreshing access tokens.
type OpenAIAuth struct {
	httpClient *http.Client
}

var openaiRefreshGroup singleflight.Group

// NewOpenAIAuth creates a new OpenAIAuth service instance.
// It initializes an HTTP client with proxy settings from the provided configuration.
func NewOpenAIAuth(cfg *config.Config) *OpenAIAuth {
	return NewOpenAIAuthWithProxyURL(cfg, "")
}

// NewOpenAIAuthWithProxyURL creates a new OpenAIAuth service instance.
// proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewOpenAIAuthWithProxyURL(cfg *config.Config, proxyURL string) *OpenAIAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &OpenAIAuth{
		httpClient: util.SetProxy(&sdkCfg, &http.Client{}),
	}
}

// GenerateAuthURL creates the OAuth authorization URL with PKCE (Proof Key for Code Exchange).
// It constructs the URL with the necessary parameters, including the client ID,
// response type, redirect URI, scopes, and PKCE challenge.
func (o *OpenAIAuth) GenerateAuthURL(state string, pkceCodes *PKCECodes) (string, error) {
	if pkceCodes == nil {
		return "", fmt.Errorf("PKCE codes are required")
	}

	params := url.Values{
		"client_id":                  {ClientID},
		"response_type":              {"code"},
		"redirect_uri":               {RedirectURI},
		"scope":                      {"openid email profile offline_access"},
		"state":                      {state},
		"code_challenge":             {pkceCodes.CodeChallenge},
		"code_challenge_method":      {"S256"},
		"prompt":                     {"login"},
		"id_token_add_organizations": {"true"},
		"originator":                 {"openai_native"},
	}

	authURL := fmt.Sprintf("%s?%s", AuthURL, params.Encode())
	return authURL, nil
}

// ExchangeCodeForTokens exchanges an authorization code for access and refresh tokens.
// It performs an HTTP POST request to the OpenAI token endpoint with the provided
// authorization code and PKCE verifier.
func (o *OpenAIAuth) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *PKCECodes) (*OpenAIAuthBundle, error) {
	return o.ExchangeCodeForTokensWithRedirect(ctx, code, RedirectURI, pkceCodes)
}

// ExchangeCodeForTokensWithRedirect exchanges an authorization code for tokens using
// a caller-provided redirect URI. This supports alternate auth flows such as device
// login while preserving the existing token parsing and storage behavior.
func (o *OpenAIAuth) ExchangeCodeForTokensWithRedirect(ctx context.Context, code, redirectURI string, pkceCodes *PKCECodes) (*OpenAIAuthBundle, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return nil, fmt.Errorf("redirect URI is required for token exchange")
	}

	// Prepare token exchange request
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"code_verifier": {pkceCodes.CodeVerifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
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

	// Parse token response
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Extract account ID from ID token
	claims, err := ParseJWTToken(tokenResp.IDToken)
	if err != nil {
		log.Warnf("Failed to parse ID token: %v", err)
	}

	accountID := ""
	email := ""
	if claims != nil {
		accountID = claims.GetAccountID()
		email = claims.GetUserEmail()
	}

	// Create token data
	tokenData := OpenAITokenData{
		IDToken:      tokenResp.IDToken,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		AccountID:    accountID,
		Email:        email,
		Expire:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}

	// Create auth bundle
	bundle := &OpenAIAuthBundle{
		TokenData:   tokenData,
		LastRefresh: time.Now().Format(time.RFC3339),
	}

	return bundle, nil
}

// RefreshTokens refreshes an access token using a refresh token.
// This method is called when an access token has expired. It makes a request to the
// token endpoint to obtain a new set of tokens.
func (o *OpenAIAuth) RefreshTokens(ctx context.Context, refreshToken string) (*OpenAITokenData, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	result, err, _ := openaiRefreshGroup.Do(refreshToken, func() (interface{}, error) {
		return o.refreshTokensSingleFlight(context.WithoutCancel(ctx), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	tokenData, ok := result.(*OpenAITokenData)
	if !ok || tokenData == nil {
		return nil, fmt.Errorf("token refresh failed: invalid single-flight result")
	}
	return tokenData, nil
}

func (o *OpenAIAuth) refreshTokensSingleFlight(ctx context.Context, refreshToken string) (*OpenAITokenData, error) {
	data := url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {"openid profile email"},
	}

	req, errReq := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(data.Encode()))
	if errReq != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", errReq)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("token refresh response body close error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", errRead)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if errUnmarshal := json.Unmarshal(body, &tokenResp); errUnmarshal != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", errUnmarshal)
	}

	// Extract account ID from ID token
	claims, errParseJWT := ParseJWTToken(tokenResp.IDToken)
	if errParseJWT != nil {
		log.Warnf("Failed to parse refreshed ID token: %v", errParseJWT)
	}

	accountID := ""
	email := ""
	if claims != nil {
		accountID = claims.GetAccountID()
		email = claims.Email
	}

	return &OpenAITokenData{
		IDToken:      tokenResp.IDToken,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		AccountID:    accountID,
		Email:        email,
		Expire:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}, nil
}

// CreateTokenStorage creates a new OpenAITokenStorage from a OpenAIAuthBundle.
// It populates the storage struct with token data, user information, and timestamps.
func (o *OpenAIAuth) CreateTokenStorage(bundle *OpenAIAuthBundle) *OpenAITokenStorage {
	storage := &OpenAITokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
	}

	return storage
}

// RefreshTokensWithRetry refreshes tokens with a built-in retry mechanism.
// It attempts to refresh the tokens up to a specified maximum number of retries,
// with an exponential backoff strategy to handle transient network errors.
func (o *OpenAIAuth) RefreshTokensWithRetry(ctx context.Context, refreshToken string, maxRetries int) (*OpenAITokenData, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		tokenData, err := o.RefreshTokens(ctx, refreshToken)
		if err == nil {
			return tokenData, nil
		}
		if isNonRetryableRefreshErr(err) {
			log.Warnf("Token refresh attempt %d failed with non-retryable error: %v", attempt+1, err)
			return nil, err
		}

		lastErr = err
		log.Warnf("Token refresh attempt %d failed: %v", attempt+1, err)
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxRetries, lastErr)
}

func isNonRetryableRefreshErr(err error) bool {
	if err == nil {
		return false
	}
	raw := strings.ToLower(err.Error())
	return strings.Contains(raw, "refresh_token_reused")
}

// UpdateTokenStorage updates an existing OpenAITokenStorage with new token data.
// This is typically called after a successful token refresh to persist the new credentials.
func (o *OpenAIAuth) UpdateTokenStorage(storage *OpenAITokenStorage, tokenData *OpenAITokenData) {
	storage.IDToken = tokenData.IDToken
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.AccountID = tokenData.AccountID
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.Email = tokenData.Email
	storage.Expire = tokenData.Expire
}
