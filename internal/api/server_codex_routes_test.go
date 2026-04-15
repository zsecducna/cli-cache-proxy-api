package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestV1ModelsKeepsMinimalOpenAISchemaForNormalClients(t *testing.T) {
	server := newTestServer(t)
	registerCodexCatalogTestModel(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload struct {
		Object string                   `json:"object"`
		Data   []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v body=%s", err, resp.Body.String())
	}
	if payload.Object != "list" {
		t.Fatalf("object = %q, want %q", payload.Object, "list")
	}

	model := findModelByID(t, payload.Data, codexCatalogTestModelID)
	if _, ok := model["display_name"]; ok {
		t.Fatalf("normal OpenAI model list unexpectedly exposed display_name: %+v", model)
	}
	if _, ok := model["description"]; ok {
		t.Fatalf("normal OpenAI model list unexpectedly exposed description: %+v", model)
	}
	if _, ok := model["context_length"]; ok {
		t.Fatalf("normal OpenAI model list unexpectedly exposed context_length: %+v", model)
	}
	if _, ok := model["supported_reasoning_levels"]; ok {
		t.Fatalf("normal OpenAI model list unexpectedly exposed supported_reasoning_levels: %+v", model)
	}
}

func TestCodexModelsAliasReturnsRichCatalog(t *testing.T) {
	server := newTestServer(t)
	registerCodexCatalogTestModel(t)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload codexCatalogPayload
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v body=%s", err, resp.Body.String())
	}
	if len(payload.Models) == 0 {
		t.Fatalf("models payload is empty: %s", resp.Body.String())
	}

	model := findCodexModelBySlug(t, payload.Models, codexCatalogTestModelID)
	if got := model.DisplayName; got != "Test Codex Model" {
		t.Fatalf("display_name = %q, want %q", got, "Test Codex Model")
	}
	if got := model.Description; got != "codex route coverage" {
		t.Fatalf("description = %q, want %q", got, "codex route coverage")
	}
	if got := model.ContextWindow; got != 200000 {
		t.Fatalf("context_window = %d, want %d", got, 200000)
	}
	if got := model.MaxCompletionTokens; got != 32000 {
		t.Fatalf("max_completion_tokens = %d, want %d", got, 32000)
	}
	if got := model.DefaultReasoningLevel; got != "medium" {
		t.Fatalf("default_reasoning_level = %q, want %q", got, "medium")
	}
	if got := model.SupportedReasoningLevels; len(got) != 4 || got[0].Effort != "low" || got[3].Effort != "xhigh" {
		t.Fatalf("supported_reasoning_levels = %#v, want low..xhigh presets", got)
	}
	if got := model.Visibility; got != "list" {
		t.Fatalf("visibility = %q, want %q", got, "list")
	}
	if !model.SupportedInAPI {
		t.Fatal("supported_in_api = false, want true")
	}
	if model.TruncationPolicy.Mode == "" || model.TruncationPolicy.Limit == 0 {
		t.Fatalf("truncation_policy = %#v, want populated policy", model.TruncationPolicy)
	}
	if len(model.InputModalities) == 0 {
		t.Fatalf("input_modalities empty in %#v", model)
	}
}

func TestV1ModelsReturnsCodexCatalogForCodexClients(t *testing.T) {
	server := newTestServer(t)
	registerCodexCatalogTestModel(t)
	registerCodexCatalogOtherProviderModel(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "codex_cli_rs/0.0.0")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	var payload codexCatalogPayload
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v body=%s", err, resp.Body.String())
	}
	if len(payload.Models) == 0 {
		t.Fatalf("models payload is empty: %s", resp.Body.String())
	}
	findCodexModelBySlug(t, payload.Models, codexCatalogTestModelID)
	findCodexModelBySlug(t, payload.Models, "test-gemini-catalog-model")
}

func TestResponsesAliasMatchesV1ResponsesBehavior(t *testing.T) {
	server := newTestServer(t)

	body := []byte(`{"model":"missing-test-model","input":"hello"}`)
	v1Req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	v1Req.Header.Set("Authorization", "Bearer test-key")
	v1Req.Header.Set("Content-Type", "application/json")
	v1Resp := httptest.NewRecorder()
	server.engine.ServeHTTP(v1Resp, v1Req)

	aliasReq := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	aliasReq.Header.Set("Authorization", "Bearer test-key")
	aliasReq.Header.Set("Content-Type", "application/json")
	aliasResp := httptest.NewRecorder()
	server.engine.ServeHTTP(aliasResp, aliasReq)

	if aliasResp.Code != v1Resp.Code {
		t.Fatalf("alias status = %d, want %d alias_body=%s canonical_body=%s", aliasResp.Code, v1Resp.Code, aliasResp.Body.String(), v1Resp.Body.String())
	}
	if aliasResp.Body.String() != v1Resp.Body.String() {
		t.Fatalf("alias body = %s, want canonical body %s", aliasResp.Body.String(), v1Resp.Body.String())
	}
}

func TestResponsesCompactAliasMatchesV1ResponsesCompactBehavior(t *testing.T) {
	server := newTestServer(t)

	body := []byte(`{"model":"missing-test-model","input":"hello"}`)
	v1Req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	v1Req.Header.Set("Authorization", "Bearer test-key")
	v1Req.Header.Set("Content-Type", "application/json")
	v1Resp := httptest.NewRecorder()
	server.engine.ServeHTTP(v1Resp, v1Req)

	aliasReq := httptest.NewRequest(http.MethodPost, "/responses/compact", bytes.NewReader(body))
	aliasReq.Header.Set("Authorization", "Bearer test-key")
	aliasReq.Header.Set("Content-Type", "application/json")
	aliasResp := httptest.NewRecorder()
	server.engine.ServeHTTP(aliasResp, aliasReq)

	if aliasResp.Code != v1Resp.Code {
		t.Fatalf("alias status = %d, want %d alias_body=%s canonical_body=%s", aliasResp.Code, v1Resp.Code, aliasResp.Body.String(), v1Resp.Body.String())
	}
	if aliasResp.Body.String() != v1Resp.Body.String() {
		t.Fatalf("alias body = %s, want canonical body %s", aliasResp.Body.String(), v1Resp.Body.String())
	}
}

const codexCatalogTestClientID = "test-codex-catalog-routes"
const codexCatalogTestModelID = "test-codex-catalog-model"

type codexCatalogPayload struct {
	Models []codexCatalogModel `json:"models"`
}

type codexCatalogModel struct {
	Slug                     string                 `json:"slug"`
	DisplayName              string                 `json:"display_name"`
	Description              string                 `json:"description"`
	DefaultReasoningLevel    string                 `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexReasoningPreset `json:"supported_reasoning_levels"`
	ContextWindow            int64                  `json:"context_window"`
	MaxCompletionTokens      int64                  `json:"max_completion_tokens"`
	Visibility               string                 `json:"visibility"`
	SupportedInAPI           bool                   `json:"supported_in_api"`
	InputModalities          []string               `json:"input_modalities"`
	TruncationPolicy         codexTruncationPolicy  `json:"truncation_policy"`
}

type codexReasoningPreset struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int64  `json:"limit"`
}

func registerCodexCatalogTestModel(t *testing.T) {
	t.Helper()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(codexCatalogTestClientID, "codex", []*registry.ModelInfo{
		{
			ID:                  codexCatalogTestModelID,
			OwnedBy:             "openai",
			Type:                "codex",
			DisplayName:         "Test Codex Model",
			Description:         "codex route coverage",
			ContextLength:       200000,
			MaxCompletionTokens: 32000,
			SupportedParameters: []string{"reasoning.effort", "reasoning.summary"},
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "medium", "high", "xhigh"},
			},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(codexCatalogTestClientID)
	})
}

func registerCodexCatalogOtherProviderModel(t *testing.T) {
	t.Helper()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-codex-catalog-routes-gemini", "gemini", []*registry.ModelInfo{
		{
			ID:                  "test-gemini-catalog-model",
			OwnedBy:             "google",
			Type:                "gemini",
			DisplayName:         "Test Gemini Model",
			Description:         "cross-provider codex route coverage",
			ContextLength:       100000,
			MaxCompletionTokens: 8000,
			SupportedParameters: []string{"reasoning.effort"},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("test-codex-catalog-routes-gemini")
	})
}

func findModelByID(t *testing.T, models []map[string]interface{}, modelID string) map[string]interface{} {
	t.Helper()

	for _, model := range models {
		if got, _ := model["id"].(string); got == modelID {
			return model
		}
	}
	t.Fatalf("model %q not found in %#v", modelID, models)
	return nil
}

func findCodexModelBySlug(t *testing.T, models []codexCatalogModel, slug string) codexCatalogModel {
	t.Helper()

	for _, model := range models {
		if model.Slug == slug {
			return model
		}
	}
	t.Fatalf("model %q not found in %#v", slug, models)
	return codexCatalogModel{}
}
