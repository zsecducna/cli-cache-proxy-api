package helps

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRecordAPIRequestIncludesCodexCacheSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	SetCodexCacheRequestObservability(ctx, []byte(`{"previous_response_id":"resp-prev","prompt_cache_retention":"24h"}`), "session-cache")
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://example.com/responses",
		Method: "POST",
		Body:   []byte(`{"model":"gpt-5.4"}`),
	})

	value, exists := ginCtx.Get(apiRequestKey)
	if !exists {
		t.Fatal("API_REQUEST missing")
	}
	text := string(value.([]byte))
	for _, want := range []string{
		"Codex Cache:",
		"prompt_cache_key: session-cache",
		"previous_response_id: resp-prev",
		"prompt_cache_retention: 24h",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request log missing %q:\n%s", want, text)
		}
	}
}

func TestAppendAPIResponseChunkIncludesCodexCacheSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://example.com/responses", Method: "POST", Body: []byte(`{"model":"gpt-5.4"}`)})
	RecordAPIResponseMetadata(ctx, cfg, 200, nil)
	SetCodexCacheRequestObservability(ctx, []byte(`{"previous_response_id":"resp-prev","prompt_cache_retention":"24h"}`), "session-cache")
	SetCodexCacheResponseObservability(ctx, []byte(`{"response":{"id":"resp-1","usage":{"input_tokens_details":{"cached_tokens":42}}}}`), "session-cache")
	AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"response.completed"}`))

	value, exists := ginCtx.Get(apiResponseKey)
	if !exists {
		t.Fatal("API_RESPONSE missing")
	}
	text := string(value.([]byte))
	for _, want := range []string{
		"Codex Cache:",
		"prompt_cache_key: session-cache",
		"response_id: resp-1",
		"cached_tokens: 42",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("response log missing %q:\n%s", want, text)
		}
	}
}

func TestSetCodexCacheResponseObservabilityWithoutPromptCacheKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	SetCodexCacheResponseObservability(ctx, []byte(`{"response":{"id":"resp-1","usage":{"input_tokens_details":{"cached_tokens":7}}}}`), "")

	obs, ok := GetCodexCacheObservability(ctx)
	if !ok {
		t.Fatal("missing codex cache observability")
	}
	if obs.ResponseID != "resp-1" {
		t.Fatalf("response_id = %q, want %q", obs.ResponseID, "resp-1")
	}
	if obs.CachedTokens != 7 {
		t.Fatalf("cached_tokens = %d, want 7", obs.CachedTokens)
	}
	if obs.PromptCacheKey != "" {
		t.Fatalf("prompt_cache_key = %q, want empty", obs.PromptCacheKey)
	}
}

func TestRecordAPIRequestEnabledByDebugWithoutRequestLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{Debug: true}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:    "https://example.com/responses",
		Method: "POST",
		Body:   []byte(`{"model":"gpt-5.4"}`),
	})
	RecordAPIResponseMetadata(ctx, cfg, 200, nil)
	AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"response.completed"}`))

	requestValue, exists := ginCtx.Get(apiRequestKey)
	if !exists {
		t.Fatal("API_REQUEST missing")
	}
	if got := string(requestValue.([]byte)); !strings.Contains(got, "https://example.com/responses") {
		t.Fatalf("request log missing upstream URL:\n%s", got)
	}

	responseValue, exists := ginCtx.Get(apiResponseKey)
	if !exists {
		t.Fatal("API_RESPONSE missing")
	}
	responseText := string(responseValue.([]byte))
	for _, want := range []string{"Status: 200", `data: {"type":"response.completed"}`} {
		if !strings.Contains(responseText, want) {
			t.Fatalf("response log missing %q:\n%s", want, responseText)
		}
	}
}
