package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/openaiprovider"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoOpenAIProviderLogin triggers the OpenAI OAuth flow through the shared authentication manager.
// It initiates the OAuth authentication process for OpenAI native services and saves
// the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoOpenAIProviderLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "openai", cfg, authOpts)
	if err != nil {
		if authErr, ok := errors.AsType[*openaiprovider.AuthenticationError](err); ok {
			log.Error(openaiprovider.GetUserFriendlyMessage(authErr))
			if authErr.Type == openaiprovider.ErrPortInUse.Type {
				os.Exit(openaiprovider.ErrPortInUse.Code)
			}
			return
		}
		fmt.Printf("OpenAI authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Println("OpenAI authentication successful!")
}
