package handlers

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/tokensaver"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

func (h *BaseAPIHandler) tokenSaverFinalHook(inboundHeaders http.Header) coreexecutor.FinalProviderRequestHook {
	if h == nil || h.Cfg == nil || !tokenSaverActive(h.Cfg.TokenSaver) || tokenSaverOptedOut(inboundHeaders) {
		return nil
	}
	return func(ctx context.Context, request coreexecutor.FinalProviderRequest) (coreexecutor.FinalProviderRequestResult, error) {
		headers := request.Headers.Clone()
		for _, value := range inboundHeaders.Values(tokensaver.OptOutHeader) {
			headers.Add(tokensaver.OptOutHeader, value)
		}
		session := metadataString(request.Metadata, coreexecutor.ExecutionSessionMetadataKey)
		if session == "" {
			session = metadataString(request.Metadata, coreexecutor.DerivedSessionIDMetadataKey)
		}
		out, stats := tokensaver.Apply(tokensaver.Options{
			Context: ctx, Body: request.Body, Model: request.Model, Format: request.Format.String(),
			Headers: headers, Session: session, Config: h.Cfg.TokenSaver,
		})
		if stats.Headroom {
			fields := log.Fields{
				"endpoint": stats.HeadroomEndpoint, "tokens_before": stats.HeadroomStats.TokensBefore,
				"tokens_after": stats.HeadroomStats.TokensAfter, "tokens_saved": stats.HeadroomStats.TokensSaved,
				"ratio": stats.HeadroomStats.Ratio, "transforms": stats.HeadroomStats.Transforms,
				"body_bytes_before": stats.HeadroomBefore.BodyBytes, "body_bytes_after": stats.HeadroomAfter.BodyBytes,
			}
			if stats.HeadroomPhantom {
				log.WithFields(fields).Warn("headroom reported token savings but request body shrank less than 5 percent")
			} else {
				log.WithFields(fields).Debug("headroom compression applied")
			}
		} else if h.Cfg.TokenSaver.Headroom.Enabled && stats.HeadroomSkip != "" {
			log.WithFields(log.Fields{"endpoint": stats.HeadroomEndpoint, "reason": stats.HeadroomSkip}).Debug("headroom compression skipped")
		}
		return coreexecutor.FinalProviderRequestResult{Body: out}, nil
	}
}

func tokenSaverActive(cfg config.TokenSaverConfig) bool {
	return cfg.Enabled && (cfg.RTK || cfg.Caveman.Enabled || cfg.Ponytail.Enabled || cfg.Headroom.Enabled)
}

func tokenSaverOptedOut(headers http.Header) bool {
	for name, values := range headers {
		if !strings.EqualFold(name, tokensaver.OptOutHeader) {
			continue
		}
		for _, value := range values {
			if strings.EqualFold(strings.TrimSpace(value), "off") {
				return true
			}
		}
	}
	return false
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}
