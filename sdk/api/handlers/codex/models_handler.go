// Package codex provides Codex-specific HTTP handlers.
package codex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	officialCodexModelsURL    = "https://raw.githubusercontent.com/openai/codex/0db6811b7cb443499c27393494abb035bb7be62d/codex-rs/models-manager/models.json"
	codexModelsURLOverrideEnv = "CLIPROXY_CODEX_MODELS_URL"
	codexModelsCachePathEnv   = "CLIPROXY_CODEX_MODELS_CACHE"
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
	body, err := fetchOfficialCodexModels(c.Request.Context(), codexModelsURL())
	if err == nil {
		if errCache := saveCodexModelsCache(body); errCache != nil {
			log.WithError(errCache).Debug("codex models: failed to save official schema cache")
		}
		c.Data(http.StatusOK, gin.MIMEJSON, body)
		return
	}

	cached, errCache := readCodexModelsCache()
	if errCache == nil {
		log.WithError(err).Debug("codex models: official schema unavailable, using cache")
		c.Data(http.StatusOK, gin.MIMEJSON, cached)
		return
	}

	log.WithError(err).Warn("codex models: official schema unavailable and cache missing")
	c.JSON(http.StatusBadGateway, gin.H{
		"error": "codex models schema unavailable and no cache is present",
	})
}

func codexModelsURL() string {
	if value := strings.TrimSpace(os.Getenv(codexModelsURLOverrideEnv)); value != "" {
		return value
	}
	return officialCodexModelsURL
}

func codexModelsCachePath() (string, error) {
	if value := strings.TrimSpace(os.Getenv(codexModelsCachePathEnv)); value != "" {
		return value, nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		root = os.TempDir()
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("cache directory unavailable")
	}
	return filepath.Join(root, "cli-proxy-api", "codex-models.json"), nil
}

func fetchOfficialCodexModels(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("codex models: failed to close official schema response body")
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("official codex models request failed with status %d", resp.StatusCode)
	}
	if !validCodexModelsPayload(body) {
		return nil, fmt.Errorf("official codex models response is not a valid models schema")
	}
	return body, nil
}

func readCodexModelsCache() ([]byte, error) {
	path, err := codexModelsCachePath()
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !validCodexModelsPayload(body) {
		return nil, fmt.Errorf("cached codex models schema is invalid")
	}
	return body, nil
}

func saveCodexModelsCache(body []byte) error {
	if !validCodexModelsPayload(body) {
		return fmt.Errorf("codex models schema is invalid")
	}
	path, err := codexModelsCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "codex-models-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func validCodexModelsPayload(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	models := gjson.GetBytes(body, "models")
	return models.Exists() && models.IsArray()
}
