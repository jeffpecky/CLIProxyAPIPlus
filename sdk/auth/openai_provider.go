package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/openaiprovider"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// OpenAIProviderAuthenticator implements the OAuth login flow for OpenAI native accounts.
type OpenAIProviderAuthenticator struct {
	CallbackPort int
}

// NewOpenAIProviderAuthenticator constructs an OpenAI provider authenticator with default settings.
func NewOpenAIProviderAuthenticator() *OpenAIProviderAuthenticator {
	return &OpenAIProviderAuthenticator{CallbackPort: 1455}
}

func (a *OpenAIProviderAuthenticator) Provider() string {
	return "openai"
}

var openaiRefreshLead = 5 * 24 * time.Hour

func (a *OpenAIProviderAuthenticator) RefreshLead() *time.Duration {
	return &openaiRefreshLead
}

func (a *OpenAIProviderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	pkceCodes, err := openaiprovider.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("openai pkce generation failed: %w", err)
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("openai state generation failed: %w", err)
	}

	oauthServer := openaiprovider.NewOAuthServer(callbackPort)
	if err = oauthServer.Start(); err != nil {
		if strings.Contains(err.Error(), "already in use") {
			return nil, openaiprovider.NewAuthenticationError(openaiprovider.ErrPortInUse, err)
		}
		return nil, openaiprovider.NewAuthenticationError(openaiprovider.ErrServerStartFailed, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("openai oauth server stop error: %v", stopErr)
		}
	}()

	authSvc := openaiprovider.NewOpenAIAuth(cfg)

	authURL, err := authSvc.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("openai authorization url generation failed: %w", err)
	}

	if !opts.NoBrowser {
		fmt.Println("Opening browser for OpenAI authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(callbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for OpenAI authentication callback...")

	callbackCh := make(chan *openaiprovider.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	manualDescription := ""

	go func() {
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
	}()

	var result *openaiprovider.OAuthResult
	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer = time.NewTimer(15 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

	var manualInputCh <-chan string
	var manualInputErrCh <-chan error

waitForCallback:
	for {
		select {
		case result = <-callbackCh:
			break waitForCallback
		case err = <-callbackErrCh:
			if strings.Contains(err.Error(), "timeout") {
				return nil, openaiprovider.NewAuthenticationError(openaiprovider.ErrCallbackTimeout, err)
			}
			return nil, err
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case result = <-callbackCh:
				break waitForCallback
			case err = <-callbackErrCh:
				if strings.Contains(err.Error(), "timeout") {
					return nil, openaiprovider.NewAuthenticationError(openaiprovider.ErrCallbackTimeout, err)
				}
				return nil, err
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, "Paste the OpenAI callback URL (or press Enter to keep waiting): ")
			continue
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			parsed, errParse := misc.ParseOAuthCallback(input)
			if errParse != nil {
				return nil, errParse
			}
			if parsed == nil {
				continue
			}
			manualDescription = parsed.ErrorDescription
			result = &openaiprovider.OAuthResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		case errManual := <-manualInputErrCh:
			return nil, errManual
		}
	}

	if result.Error != "" {
		return nil, openaiprovider.NewOAuthError(result.Error, manualDescription, 400)
	}

	if result.State != state {
		return nil, openaiprovider.NewAuthenticationError(openaiprovider.ErrInvalidState, fmt.Errorf("state mismatch"))
	}

	log.Debug("OpenAI authorization code received; exchanging for tokens")

	authBundle, err := authSvc.ExchangeCodeForTokens(ctx, result.Code, pkceCodes)
	if err != nil {
		return nil, openaiprovider.NewAuthenticationError(openaiprovider.ErrCodeExchangeFailed, err)
	}

	return a.buildAuthRecord(authSvc, authBundle)
}

func (a *OpenAIProviderAuthenticator) buildAuthRecord(authSvc *openaiprovider.OpenAIAuth, authBundle *openaiprovider.OpenAIAuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	if tokenStorage == nil || tokenStorage.Email == "" {
		return nil, fmt.Errorf("openai token storage missing account information")
	}

	planType := ""
	hashAccountID := ""
	if tokenStorage.IDToken != "" {
		if claims, errParse := openaiprovider.ParseJWTToken(tokenStorage.IDToken); errParse == nil && claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			accountID := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
			if accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}
	}

	fileName := openaiprovider.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
	metadata := map[string]any{
		"email":      tokenStorage.Email,
		"originator": "openai_native",
	}

	fmt.Println("OpenAI authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
		Label:    "OpenAI User",
		Attributes: map[string]string{
			"plan_type":  planType,
			"originator": "openai_native",
		},
	}, nil
}
