package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetStaticModelDefinitions returns static model metadata for a given channel.
// Channel is provided via path param (:channel) or query param (?channel=...).
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	// Handle openai-compatibility channel from config
	if strings.ToLower(channel) == "openai-compatibility" {
		models := h.buildOpenAICompatModelDefinitions()
		c.JSON(http.StatusOK, gin.H{
			"channel": "openai-compatibility",
			"models":  models,
		})
		return
	}

	models := registry.GetStaticModelDefinitionsByChannel(channel)
	if models == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": strings.ToLower(strings.TrimSpace(channel)),
		"models":  models,
	})
}

// buildOpenAICompatModelDefinitions builds model definitions from configured openai-compatibility entries.
func (h *Handler) buildOpenAICompatModelDefinitions() []*registry.ModelInfo {
	if h.cfg == nil {
		return nil
	}
	var allModels []*registry.ModelInfo
	now := time.Now().Unix()
	for _, entry := range h.cfg.OpenAICompatibility {
		if entry.Disabled {
			continue
		}
		for _, model := range entry.Models {
			info := &registry.ModelInfo{
				ID:         model.Name,
				Object:     "model",
				Created:    now,
				OwnedBy:    "openai-compatibility",
				Type:       "openai-compatibility",
				IsCompat:   true,
			}
			if model.Alias != "" {
				info.Name = model.Alias
			}
			if model.DisplayName != "" {
				info.DisplayName = model.DisplayName
			}
			if model.MaxContextLength > 0 {
				info.ContextLength = model.MaxContextLength
			}
			if model.Thinking != nil {
				info.Thinking = model.Thinking
			}
			allModels = append(allModels, info)
		}
	}
	return allModels
}
