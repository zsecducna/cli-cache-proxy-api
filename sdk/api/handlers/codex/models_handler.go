// Package codex provides Codex-specific HTTP handlers.
package codex

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
)

// APIHandler contains handlers for Codex-specific API endpoints.
type APIHandler struct {
	*handlers.BaseAPIHandler
}

// NewAPIHandler creates a new Codex API handler set.
func NewAPIHandler(apiHandlers *handlers.BaseAPIHandler) *APIHandler {
	return &APIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// Models returns a Codex-compatible model catalog.
func (h *APIHandler) Models(c *gin.Context) {
	if body, err := h.FetchCodexModelsUpstream(c.Request.Context(), c.GetHeader("User-Agent")); err == nil {
		body = injectTemporaryGPT55ModelIntoBody(body)
		c.Data(http.StatusOK, "application/json", body)
		return
	} else {
		log.WithError(err).Debug("codex models: falling back to local registry catalog")
	}
	c.JSON(http.StatusOK, gin.H{
		"models": mustInjectTemporaryGPT55Model(buildCodexModelCatalog()),
	})
}

func mustInjectTemporaryGPT55Model(models []map[string]any) []map[string]any {
	models, _ = injectTemporaryGPT55Model(models)
	return models
}

func injectTemporaryGPT55ModelIntoBody(body []byte) []byte {
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	models, changed := injectTemporaryGPT55Model(payload.Models)
	if !changed {
		return body
	}
	payload.Models = models
	patched, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return patched
}

func injectTemporaryGPT55Model(models []map[string]any) ([]map[string]any, bool) {
	if hasCodexModelSlug(models, "gpt-5.5") {
		return models, false
	}
	for index, model := range models {
		if modelSlugFromMap(model) != "gpt-5.4" {
			continue
		}
		clone := cloneCodexModelMap(model)
		clone["slug"] = "gpt-5.5"
		clone["display_name"] = "gpt-5.5"
		if _, ok := clone["id"]; ok {
			clone["id"] = "gpt-5.5"
		}
		clone["context_window"] = 400000
		if _, ok := clone["context_length"]; ok {
			clone["context_length"] = 400000
		}
		if _, ok := clone["max_context_window"]; ok {
			clone["max_context_window"] = 400000
		}
		models = append(models, nil)
		copy(models[index+2:], models[index+1:])
		models[index+1] = clone
		return models, true
	}
	return models, false
}

func hasCodexModelSlug(models []map[string]any, slug string) bool {
	for _, model := range models {
		if modelSlugFromMap(model) == slug {
			return true
		}
	}
	return false
}

func cloneCodexModelMap(model map[string]any) map[string]any {
	clone := make(map[string]any, len(model))
	for key, value := range model {
		clone[key] = value
	}
	return clone
}

func buildCodexModelCatalog() []map[string]any {
	modelRegistry := registry.GetGlobalRegistry()
	models := modelRegistry.GetAvailableModels("codex")
	sort.Slice(models, func(i, j int) bool {
		return modelSlugFromMap(models[i]) < modelSlugFromMap(models[j])
	})
	return models
}

func modelSlugFromMap(model map[string]any) string {
	if model == nil {
		return ""
	}
	value, _ := model["slug"].(string)
	return value
}
