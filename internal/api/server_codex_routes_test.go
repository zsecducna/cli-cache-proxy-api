package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
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
	setCodexModelsEndpointTestEnv(t, `{"models":[{"slug":"route-fresh","display_name":"Route Fresh","context_window":123,"supported_in_api":true}]}`)
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	assertOfficialCodexModelsResponse(t, resp.Body.Bytes())
}

func TestV1ModelsReturnsCodexCatalogForCodexClients(t *testing.T) {
	setCodexModelsEndpointTestEnv(t, `{"models":[{"slug":"route-v1","display_name":"Route V1","context_window":456,"supported_in_api":true}]}`)
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "codex_cli_rs/0.0.0")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	assertOfficialCodexModelsResponse(t, resp.Body.Bytes())
}

func TestCodexModelsAliasUsesOfficialCatalogEvenWhenCodexAuthAvailable(t *testing.T) {
	setCodexModelsEndpointTestEnv(t, `{"models":[{"slug":"route-auth","display_name":"Route Auth","context_window":789,"supported_in_api":true}]}`)

	server := newTestServer(t)
	registerCodexModelsFetchTestAuth(t, server, "http://unused.invalid")

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "codex_cli_rs/0.0.0")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	assertOfficialCodexModelsResponse(t, resp.Body.Bytes())
}

func assertOfficialCodexModelsResponse(t *testing.T, body []byte) {
	t.Helper()

	var payload codexCatalogPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Models) == 0 {
		t.Fatalf("models payload is empty")
	}
	model := payload.Models[0]
	if model.Slug == "" || model.DisplayName == "" || model.ContextWindow == 0 || !model.SupportedInAPI {
		t.Fatalf("model = %#v, want fetched official-schema metadata", model)
	}
}

func setCodexModelsEndpointTestEnv(t *testing.T, body string) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("CLIPROXY_CODEX_MODELS_URL", upstream.URL)
	t.Setenv("CLIPROXY_CODEX_MODELS_CACHE", filepath.Join(t.TempDir(), "models.json"))
}

func TestCodexModelsAliasUsesOfficialCatalogForOldCodexUserAgent(t *testing.T) {
	setCodexModelsEndpointTestEnv(t, `{"models":[{"slug":"route-old","display_name":"Route Old","context_window":321,"supported_in_api":true}]}`)

	server := newTestServer(t)
	registerCodexModelsFetchTestAuth(t, server, "http://unused.invalid")

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "codex_cli_rs/0.118.0")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	assertOfficialCodexModelsResponse(t, resp.Body.Bytes())
}

func TestV1ModelsFetchesCodexCatalogAndKeepsLocalClaudeCapableModels(t *testing.T) {
	var gotPath string
	var gotAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"Latest","models":[{"slug":"gpt-5-5-thinking"},{"slug":"gpt-5.4"}]}`))
	}))
	defer upstream.Close()

	server := newTestServer(t)
	registerCodexModelsFetchTestAuth(t, server, upstream.URL)
	registerV1ModelsClaudeCapableProviderModel(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/backend-api/codex/models" {
		t.Fatalf("upstream path = %q, want %q", gotPath, "/backend-api/codex/models")
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("upstream authorization = %q, want %q", gotAuth, "Bearer test-token")
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
	if len(payload.Data) != 3 {
		t.Fatalf("data len = %d, want 3 body=%s", len(payload.Data), resp.Body.String())
	}
	if got := payload.Data[0]["id"]; got != "gpt-5.5" {
		t.Fatalf("data[0].id = %v, want %q", got, "gpt-5.5")
	}
	if got := payload.Data[0]["object"]; got != "model" {
		t.Fatalf("data[0].object = %v, want %q", got, "model")
	}
	if got := payload.Data[0]["owned_by"]; got != "openai" {
		t.Fatalf("data[0].owned_by = %v, want %q", got, "openai")
	}
	if _, ok := payload.Data[0]["display_name"]; ok {
		t.Fatalf("normal OpenAI model list unexpectedly exposed display_name: %+v", payload.Data[0])
	}
	claudeModel := findModelByID(t, payload.Data, "claude-sonnet-4-6")
	if got := claudeModel["owned_by"]; got != "anthropic" {
		t.Fatalf("claude owned_by = %v, want anthropic", got)
	}
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
	MaxContextWindow         int64                  `json:"max_context_window"`
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

func registerV1ModelsClaudeCapableProviderModel(t *testing.T) {
	t.Helper()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-v1-models-antigravity", "antigravity", []*registry.ModelInfo{
		{
			ID:      "claude-sonnet-4-6",
			OwnedBy: "anthropic",
			Type:    "antigravity",
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient("test-v1-models-antigravity")
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

func registerCodexModelsFetchTestAuth(t *testing.T, server *Server, upstreamURL string) {
	t.Helper()

	fake := &codexModelsFetchTestExecutor{upstreamURL: upstreamURL}
	server.handlers.AuthManager.RegisterExecutor(fake)
	_, err := server.handlers.AuthManager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-models-auth",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "test-token",
		},
		UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

type codexModelsFetchTestExecutor struct {
	upstreamURL string
}

func (e *codexModelsFetchTestExecutor) Identifier() string { return "codex" }

func (e *codexModelsFetchTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexModelsFetchTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *codexModelsFetchTestExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (e *codexModelsFetchTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexModelsFetchTestExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	target, err := url.Parse(e.upstreamURL)
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(ctx)
	cloned.URL.Scheme = target.Scheme
	cloned.URL.Host = target.Host
	cloned.Host = target.Host
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}
	if cloned.Header.Get("Authorization") == "" && auth != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok && token != "" {
			cloned.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return http.DefaultClient.Do(cloned)
}
