package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xiaomitokenplan"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// XiaomiTokenPlanAuthenticator implements API key login for Xiaomi TokenPlan.
type XiaomiTokenPlanAuthenticator struct{}

// NewXiaomiTokenPlanAuthenticator constructs a new Xiaomi TokenPlan authenticator.
func NewXiaomiTokenPlanAuthenticator() Authenticator {
	return &XiaomiTokenPlanAuthenticator{}
}

// Provider returns the provider key for xiaomi-tokenplan.
func (XiaomiTokenPlanAuthenticator) Provider() string {
	return "xiaomi-tokenplan"
}

// RefreshLead returns nil — API keys don't need refresh.
func (XiaomiTokenPlanAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login prompts the user for an API key and optional region, then saves the credential.
func (a XiaomiTokenPlanAuthenticator) Login(_ context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	promptFn := opts.Prompt
	if promptFn == nil {
		return nil, fmt.Errorf("xiaomi-tokenplan: interactive prompt is required for API key input")
	}

	apiKey, err := promptFn("Enter your Xiaomi TokenPlan API key: ")
	if err != nil {
		return nil, fmt.Errorf("xiaomi-tokenplan: failed to read API key: %w", err)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("xiaomi-tokenplan: API key cannot be empty")
	}

	region, err := promptFn("Enter region (cn/global, default: cn): ")
	if err != nil {
		return nil, fmt.Errorf("xiaomi-tokenplan: failed to read region: %w", err)
	}
	region = strings.TrimSpace(region)
	if region == "" {
		region = xiaomitokenplan.DefaultRegion
	}

	baseURL := xiaomitokenplan.RegionBaseURL(region)

	tokenStorage := &xiaomitokenplan.XiaomiTokenPlanStorage{
		APIKey:  apiKey,
		Region:  region,
		BaseURL: baseURL,
		Type:    "xiaomi-tokenplan",
	}

	fileName := fmt.Sprintf("xiaomi-tokenplan-%d.json", time.Now().Unix())

	metadata := map[string]any{
		"type":     "xiaomi-tokenplan",
		"api_key":  apiKey,
		"region":   region,
		"base_url": baseURL,
	}

	fmt.Println("Xiaomi TokenPlan authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    fmt.Sprintf("Xiaomi TokenPlan (%s)", region),
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"api_key":  apiKey,
			"base_url": baseURL,
		},
	}, nil
}
