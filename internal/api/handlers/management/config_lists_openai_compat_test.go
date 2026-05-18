package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchOpenAICompatUpdatesAppendReasoningEffortPercent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    append-reasoning-effort-to-model: true
    models:
      - name: gpt-5.4
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	handler := NewHandler(cfg, configPath, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"name":"openrouter",
		"value":{
			"append-reasoning-effort-to-model":true,
			"append-reasoning-effort-to-model-percent":35
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.PatchOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := handler.cfg.OpenAICompatibility[0].AppendReasoningEffortToModelPercent; got == nil || *got != 35 {
		t.Fatalf("AppendReasoningEffortToModelPercent = %v, want 35", got)
	}
	if !handler.cfg.OpenAICompatibility[0].AppendReasoningEffortToModelEnabled() {
		t.Fatal("AppendReasoningEffortToModelEnabled() = false, want true")
	}

	reloaded, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() reload error = %v", err)
	}
	if got := reloaded.OpenAICompatibility[0].AppendReasoningEffortToModelPercent; got == nil || *got != 35 {
		t.Fatalf("reloaded AppendReasoningEffortToModelPercent = %v, want 35", got)
	}
}

func TestPatchOpenAICompatPersistsAppendReasoningEffortDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    models:
      - name: gpt-5.4
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	handler := NewHandler(cfg, configPath, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"name":"openrouter",
		"value":{
			"append-reasoning-effort-to-model":false
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	handler.PatchOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if handler.cfg.OpenAICompatibility[0].AppendReasoningEffortToModelEnabled() {
		t.Fatal("AppendReasoningEffortToModelEnabled() = true, want false")
	}

	reloaded, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() reload error = %v", err)
	}
	if reloaded.OpenAICompatibility[0].AppendReasoningEffortToModelEnabled() {
		t.Fatal("reloaded AppendReasoningEffortToModelEnabled() = true, want false")
	}
}

func TestGetOpenAICompatIncludesAppendReasoningEffortDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "openrouter",
				BaseURL: "https://openrouter.ai/api/v1",
				Models:  []config.OpenAICompatibilityModel{{Name: "gpt-5.4"}},
			},
		},
	}
	cfg.SanitizeOpenAICompatibility()

	handler := NewHandler(cfg, "", nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)

	handler.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OpenAICompatibility []struct {
			AppendReasoningEffortToModel bool `json:"append-reasoning-effort-to-model"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(payload.OpenAICompatibility) != 1 {
		t.Fatalf("openai-compatibility length = %d, want 1", len(payload.OpenAICompatibility))
	}
	if !payload.OpenAICompatibility[0].AppendReasoningEffortToModel {
		t.Fatal("append-reasoning-effort-to-model = false, want true")
	}
}
