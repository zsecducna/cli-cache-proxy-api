package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestCodexModelsAliasFetchesUpstreamCatalogWhenCodexAuthAvailable(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.5","context_window":272000,"base_instructions":"official"}]}`))
	}))
	defer upstream.Close()

	server := newTestServer(t)
	registerCodexModelsFetchTestAuth(t, server, upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "codex_cli_rs/0.0.0")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/backend-api/codex/models" {
		t.Fatalf("upstream path = %q, want %q", gotPath, "/backend-api/codex/models")
	}
	if gotQuery != "client_version=0.0.0" {
		t.Fatalf("upstream query = %q, want %q", gotQuery, "client_version=0.0.0")
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("upstream authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
	if got := resp.Body.String(); got != `{"models":[{"slug":"gpt-5.5","context_window":272000,"base_instructions":"official"}]}` {
		t.Fatalf("body = %s", got)
	}
}

func TestCodexModelsAliasInjectsTemporaryGPT55MetadataFromUpstreamGPT54(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4","display_name":"gpt-5.4","description":"Strong model for everyday coding.","default_reasoning_level":"medium","context_window":272000,"max_context_window":1000000,"base_instructions":"official"}]}`))
	}))
	defer upstream.Close()

	server := newTestServer(t)
	registerCodexModelsFetchTestAuth(t, server, upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "codex_cli_rs/0.118.0")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var payload codexCatalogPayload
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v body=%s", err, resp.Body.String())
	}
	gpt54 := findCodexModelBySlug(t, payload.Models, "gpt-5.4")
	gpt55 := findCodexModelBySlug(t, payload.Models, "gpt-5.5")
	if gpt55.DisplayName != "gpt-5.5" {
		t.Fatalf("gpt-5.5 display_name = %q, want gpt-5.5", gpt55.DisplayName)
	}
	if gpt55.ContextWindow != 400000 {
		t.Fatalf("gpt-5.5 context_window = %d, want 400000", gpt55.ContextWindow)
	}
	if gpt55.MaxContextWindow != 400000 {
		t.Fatalf("gpt-5.5 max_context_window = %d, want 400000", gpt55.MaxContextWindow)
	}
	if gpt55.Description != gpt54.Description || gpt55.DefaultReasoningLevel != gpt54.DefaultReasoningLevel {
		t.Fatalf("gpt-5.5 metadata = %#v, want clone of gpt-5.4 %#v with patched identity/context", gpt55, gpt54)
	}
}

func TestV1ModelsFetchesUpstreamChatGPTCatalogWhenCodexAuthAvailable(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")

	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/backend-api/models" {
		t.Fatalf("upstream path = %q, want %q", gotPath, "/backend-api/models")
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
	if len(payload.Data) != 2 {
		t.Fatalf("data len = %d, want 2 body=%s", len(payload.Data), resp.Body.String())
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
