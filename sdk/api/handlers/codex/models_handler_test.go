package codex

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

func TestModelsFetchesOfficialSchemaAndSavesCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const upstreamBody = `{"models":[{"slug":"fresh","display_name":"Fresh"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	cachePath := filepath.Join(t.TempDir(), "models.json")
	t.Setenv(codexModelsURLOverrideEnv, upstream.URL)
	t.Setenv(codexModelsCachePathEnv, cachePath)

	resp := httptest.NewRecorder()
	ctx, engine := gin.CreateTestContext(resp)
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	ctx.Request = req
	handler := NewAPIHandler(&handlers.BaseAPIHandler{})

	engine.GET("/models", handler.Models)
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := resp.Body.String(); got != upstreamBody {
		t.Fatalf("body = %s, want %s", got, upstreamBody)
	}
	cached, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("ReadFile(cache) error = %v", err)
	}
	if string(cached) != upstreamBody {
		t.Fatalf("cache = %s, want %s", string(cached), upstreamBody)
	}
}

func TestModelsFallsBackToCacheWhenOfficialSchemaUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	const cachedBody = `{"models":[{"slug":"cached","display_name":"Cached"}]}`
	cachePath := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cachePath, []byte(cachedBody), 0o600); err != nil {
		t.Fatalf("WriteFile(cache) error = %v", err)
	}
	t.Setenv(codexModelsURLOverrideEnv, upstream.URL)
	t.Setenv(codexModelsCachePathEnv, cachePath)

	resp := httptest.NewRecorder()
	ctx, engine := gin.CreateTestContext(resp)
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	ctx.Request = req
	handler := NewAPIHandler(&handlers.BaseAPIHandler{})

	engine.GET("/models", handler.Models)
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := resp.Body.String(); got != cachedBody {
		t.Fatalf("body = %s, want %s", got, cachedBody)
	}
}
