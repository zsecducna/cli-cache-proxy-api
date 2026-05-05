package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_StablePromptCacheKeyFromAPIKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4"}`),
	}
	url := "https://example.com/responses"

	httpReq, selection, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	expectedKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:api-key:test-api-key")).String()
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedKey)
	}
	if selection.Key != expectedKey {
		t.Fatalf("selection.Key = %q, want %q", selection.Key, expectedKey)
	}
	if gotConversation := httpReq.Header.Get("Conversation_id"); gotConversation != "" {
		t.Fatalf("Conversation_id = %q, want empty", gotConversation)
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != expectedKey {
		t.Fatalf("Session_id = %q, want %q", gotSession, expectedKey)
	}

	httpReq2, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error (second call): %v", err)
	}
	body2, errRead2 := io.ReadAll(httpReq2.Body)
	if errRead2 != nil {
		t.Fatalf("read request body (second call): %v", errRead2)
	}
	gotKey2 := gjson.GetBytes(body2, "prompt_cache_key").String()
	if gotKey2 != expectedKey {
		t.Fatalf("prompt_cache_key (second call) = %q, want %q", gotKey2, expectedKey)
	}
}

func TestCodexExecutorCacheHelper_ReusesPromptCacheKeyFromPreviousResponseID(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5.4", Protocol: "openai-response"}},
					Params: map[string]any{
						"max_tokens":            4096,
						"max_output_tokens":     8192,
						"max_completion_tokens": 16384,
					},
				},
			},
		},
	})
	ctx := context.Background()
	url := "https://example.com/responses"
	firstReq := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"session-cache","prompt_cache_retention":"24h"}`),
	}

	_, firstSelection, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, firstReq, firstReq.Payload)
	if err != nil {
		t.Fatalf("cacheHelper first call: %v", err)
	}
	recordCodexPromptCacheResponse(ctx, []byte(`{"type":"response.completed","response":{"id":"resp-prev"}}`), firstSelection.Key, firstSelection.TTL)

	secondReq := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","previous_response_id":"resp-prev"}`),
	}
	httpReq, secondSelection, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, secondReq, secondReq.Payload)
	if err != nil {
		t.Fatalf("cacheHelper second call: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read second request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "session-cache" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "session-cache")
	}
	if got := gjson.GetBytes(body, "previous_response_id").String(); got != "" {
		t.Fatalf("previous_response_id = %q, want empty", got)
	}
	if secondSelection.Key != "session-cache" {
		t.Fatalf("selection.Key = %q, want %q", secondSelection.Key, "session-cache")
	}
	if got := httpReq.Header.Get("Session_id"); got != "session-cache" {
		t.Fatalf("Session_id = %q, want %q", got, "session-cache")
	}
}

func TestCodexExecutorCacheHelper_StripsUnmappedPreviousResponseIDAndDerivesPromptCacheKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","previous_response_id":"resp-not-known"}`),
	}

	httpReq, selection, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), "https://example.com/responses", req, req.Payload)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}

	if got := gjson.GetBytes(body, "previous_response_id").String(); got != "" {
		t.Fatalf("previous_response_id = %q, want empty", got)
	}
	expectedKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:api-key:test-api-key")).String()
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", got, expectedKey)
	}
	if selection.Key != expectedKey {
		t.Fatalf("selection.Key = %q, want %q", selection.Key, expectedKey)
	}
}

func TestCodexExecutorCacheHelper_OverridesPromptCacheRetentionForSupportedModels(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_retention":"1h"}`),
	}

	httpReq, selection, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://api.openai.com/v1/responses", req, req.Payload)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "24h" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
	}
	if selection.TTL != codexPromptCache24hTTL {
		t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCache24hTTL)
	}
}

func TestCodexExecutorCacheHelper_AddsPromptCacheRetentionWhenMissingForSupportedModels(t *testing.T) {
	executor := &CodexExecutor{}
	tests := []struct {
		name  string
		model string
	}{
		{name: "gpt-5.4", model: "gpt-5.4"},
		{name: "gpt-5.4-mini", model: "gpt-5.4-mini"},
		{name: "gpt-5.5", model: "gpt-5.5"},
		{name: "gpt-5.3-codex", model: "gpt-5.3-codex"},
		{name: "gpt-5.3-codex-spark", model: "gpt-5.3-codex-spark"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := cliproxyexecutor.Request{
				Model:   tt.model,
				Payload: []byte(`{"model":"` + tt.model + `"}`),
			}

			httpReq, selection, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://api.openai.com/v1/responses", req, req.Payload)
			if err != nil {
				t.Fatalf("cacheHelper error: %v", err)
			}

			body, err := io.ReadAll(httpReq.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "24h" {
				t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
			}
			if selection.TTL != codexPromptCache24hTTL {
				t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCache24hTTL)
			}
		})
	}
}

func TestCodexExecutorCacheHelper_StripsExtendedPromptCacheRetentionForUnsupportedModels(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: []byte(`{"model":"gpt-4o-mini","prompt_cache_retention":"24h"}`),
	}

	httpReq, selection, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://api.openai.com/v1/responses", req, req.Payload)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "" {
		t.Fatalf("prompt_cache_retention = %q, want empty", got)
	}
	if selection.TTL != codexPromptCacheDefaultTTL {
		t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCacheDefaultTTL)
	}
}

func TestCodexExecutorCacheHelper_LeavesPromptCacheRetentionUnsetWhenMissingForUnsupportedModels(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: []byte(`{"model":"gpt-4o-mini"}`),
	}

	httpReq, selection, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://api.openai.com/v1/responses", req, req.Payload)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "" {
		t.Fatalf("prompt_cache_retention = %q, want empty", got)
	}
	if selection.TTL != codexPromptCacheDefaultTTL {
		t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCacheDefaultTTL)
	}
}

func TestCodexExecutorCacheHelper_StripsPromptCacheRetentionForChatGPTCodexBackend(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_retention":"24h"}`),
	}

	httpReq, selection, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://chatgpt.com/backend-api/codex/responses", req, req.Payload)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "" {
		t.Fatalf("prompt_cache_retention = %q, want empty", got)
	}
	if selection.TTL != codexPromptCacheDefaultTTL {
		t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCacheDefaultTTL)
	}
}

func TestCodexExecutorCacheHelper_NormalizesPromptCacheRetentionFormattingForSupportedModels(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_retention":"24H"}`),
	}

	httpReq, _, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://api.openai.com/v1/responses", req, req.Payload)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "24h" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
	}
}

func TestCodexExecutorExecuteAddsPromptCacheRetentionForSupportedModels(t *testing.T) {
	tests := []struct {
		name  string
		model string
	}{
		{name: "gpt-5.4", model: "gpt-5.4"},
		{name: "gpt-5.4-mini", model: "gpt-5.4-mini"},
		{name: "gpt-5.5", model: "gpt-5.5"},
		{name: "gpt-5.3-codex", model: "gpt-5.3-codex"},
		{name: "gpt-5.3-codex-spark", model: "gpt-5.3-codex-spark"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				capturedBody, err = io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"output\":[]}}\n\n"))
			}))
			defer server.Close()

			executor := &CodexExecutor{}
			auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
			req := cliproxyexecutor.Request{
				Model:   tt.model,
				Payload: []byte(`{"model":"` + tt.model + `","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
			}
			_, err := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := gjson.GetBytes(capturedBody, "prompt_cache_retention").String(); got != "24h" {
				t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
			}
		})
	}
}

func TestCodexExecutorExecuteTranslatesPreviousResponseIDToPromptCacheKey(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := &CodexExecutor{}
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	recordCodexPromptCacheResponse(context.Background(), []byte(`{"type":"response.completed","response":{"id":"resp-exec-prev"}}`), "session-cache", codexPromptCache24hTTL)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","previous_response_id":"resp-exec-prev","prompt_cache_retention":"24h","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}
	_, err := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(capturedBody, "previous_response_id").String(); got != "" {
		t.Fatalf("previous_response_id = %q, want empty", got)
	}
	if got := gjson.GetBytes(capturedBody, "prompt_cache_key").String(); got != "session-cache" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "session-cache")
	}
	if got := gjson.GetBytes(capturedBody, "prompt_cache_retention").String(); got != "24h" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
	}
}

func TestCodexExecutorExecuteStripsUnsupportedTokenLimitFields(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := &CodexExecutor{}
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"max_tokens":256,
			"max_output_tokens":512,
			"max_completion_tokens":1024,
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
		}`),
	}

	_, err := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
		if gjson.GetBytes(capturedBody, field).Exists() {
			t.Fatalf("%s should be stripped from Codex request: %s", field, capturedBody)
		}
	}
}

func TestCodexExecutorExecuteCompactStripsUnsupportedTokenLimitFields(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-compact","object":"response","status":"completed","model":"gpt-5.4","output":[]}`))
	}))
	defer server.Close()

	executor := &CodexExecutor{}
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"max_tokens":256,
			"max_output_tokens":512,
			"max_completion_tokens":1024,
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
		}`),
	}

	_, err := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
		if gjson.GetBytes(capturedBody, field).Exists() {
			t.Fatalf("%s should be stripped from compact Codex request: %s", field, capturedBody)
		}
	}
}
