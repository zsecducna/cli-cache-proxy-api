// Package codex provides Codex-specific HTTP handlers.
package codex

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
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
	c.JSON(http.StatusOK, gin.H{
		"models": buildCodexModelCatalog(),
	})
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
