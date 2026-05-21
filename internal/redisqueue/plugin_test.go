package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestUsageQueuePluginPayloadIncludesStableFieldsAndSuccess verifies that stable request
// metadata and cloned upstream response headers survive payload serialization.
func TestUsageQueuePluginPayloadIncludesStableFieldsAndSuccess(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithRequestID(context.Background(), "ctx-request-id")
		ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
		ctx = internallogging.WithResponseStatusHolder(ctx)
		internallogging.SetResponseStatus(ctx, http.StatusOK)

		// responseHeaders is cloned into the usage record so later mutation cannot
		// change the queued payload that the plugin serializes.
		responseHeaders := http.Header{}
		responseHeaders.Set("X-Upstream-Request-Id", "upstream-req-1")
		responseHeaders.Set("Retry-After", "30")

		plugin := &usageQueuePlugin{}
		plugin.HandleUsage(ctx, coreusage.Record{
			Provider:        "openai",
			Model:           "gpt-5.4",
			Alias:           "client-gpt",
			APIKey:          "test-key",
			AuthIndex:       "0",
			AuthType:        "apikey",
			CustomerID:      "customer-1",
			CustomerEmail:   "customer@example.com",
			ReasoningEffort: "medium",
			Source:          "user@example.com",
			RequestedAt:     time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			Latency:         1500 * time.Millisecond,
			Detail: coreusage.Detail{
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
			ResponseHeaders: responseHeaders.Clone(),
		})
		responseHeaders.Set("Retry-After", "999")

		payload := popSinglePayload(t)
		requireStringField(t, payload, "provider", "openai")
		requireStringField(t, payload, "model", "gpt-5.4")
		requireStringField(t, payload, "alias", "client-gpt")
		requireStringField(t, payload, "customer_id", "customer-1")
		requireStringField(t, payload, "customer_email", "customer@example.com")
		requireStringField(t, payload, "endpoint", "POST /v1/chat/completions")
		requireStringField(t, payload, "auth_type", "apikey")
		requireMissingField(t, payload, "user_api_key")
		requireStringField(t, payload, "request_id", "ctx-request-id")
		requireStringField(t, payload, "reasoning_effort", "medium")
		requireHeaderField(t, payload, "response_headers", "X-Upstream-Request-Id", []string{"upstream-req-1"})
		requireHeaderField(t, payload, "response_headers", "Retry-After", []string{"30"})
		requireBoolField(t, payload, "failed", false)
		requireFailField(t, payload, http.StatusOK, "")
	})
}

// TestUsageQueuePluginAsyncUsesRecordResponseHeaders verifies async delivery uses
// the immutable header snapshot copied into the usage record.
func TestUsageQueuePluginAsyncUsesRecordResponseHeaders(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithRequestID(context.Background(), "ctx-request-id")
		ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
		ctx = internallogging.WithResponseStatusHolder(ctx)
		ctx = internallogging.WithResponseHeadersHolder(ctx)
		internallogging.SetResponseStatus(ctx, http.StatusOK)
		initialHeaders := http.Header{}
		initialHeaders.Set("X-Upstream-Request-Id", "upstream-req-1")
		internallogging.SetResponseHeaders(ctx, initialHeaders)

		mgr := coreusage.NewManager(16)
		defer mgr.Stop()

		mgr.Register(pluginFunc(func(ctx context.Context, _ coreusage.Record) {
			nextHeaders := http.Header{}
			nextHeaders.Set("X-Upstream-Request-Id", "upstream-req-2")
			internallogging.SetResponseHeaders(ctx, nextHeaders)
		}))
		mgr.Register(&usageQueuePlugin{})

		mgr.Publish(ctx, coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.4",
			APIKey:      "test-key",
			AuthIndex:   "0",
			AuthType:    "apikey",
			Source:      "user@example.com",
			RequestedAt: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			Latency:     1500 * time.Millisecond,
			Detail: coreusage.Detail{
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
			ResponseHeaders: internallogging.GetResponseHeaders(ctx),
		})

		payload := waitForSinglePayload(t, 2*time.Second)
		requireHeaderField(t, payload, "response_headers", "X-Upstream-Request-Id", []string{"upstream-req-1"})
	})
}

func TestRegisterUsagePluginDeliversDefaultUsageRecords(t *testing.T) {
	withEnabledQueue(t, func() {
		RegisterUsagePlugin()
		coreusage.PublishRecord(context.Background(), coreusage.Record{
			Provider:        "codex",
			Model:           "gpt-5.5",
			AuthType:        "oauth",
			CustomerID:      "customer-default",
			CustomerEmail:   "default@example.com",
			ReasoningEffort: "medium",
			RequestedAt:     time.Date(2026, 4, 25, 1, 0, 0, 0, time.UTC),
			Detail: coreusage.Detail{
				InputTokens:  1,
				OutputTokens: 2,
				TotalTokens:  3,
			},
		})

		payload := waitForSinglePayload(t, 2*time.Second)
		requireStringField(t, payload, "provider", "codex")
		requireStringField(t, payload, "model", "gpt-5.5")
		requireStringField(t, payload, "auth_type", "oauth")
		requireStringField(t, payload, "reasoning_effort", "medium")
		requireStringField(t, payload, "customer_id", "customer-default")
		requireStringField(t, payload, "customer_email", "default@example.com")
	})
}

// TestUsageQueuePluginPayloadIncludesStableFieldsAndFailureAndGinRequestID verifies
// failure payloads include stable request metadata without exposing user API keys.
func TestUsageQueuePluginPayloadIncludesStableFieldsAndFailureAndGinRequestID(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithRequestID(context.Background(), "gin-request-id")
		ctx = internallogging.WithEndpoint(ctx, "GET /v1/responses")
		ctx = internallogging.WithResponseStatusHolder(ctx)
		internallogging.SetResponseStatus(ctx, http.StatusInternalServerError)

		plugin := &usageQueuePlugin{}
		plugin.HandleUsage(ctx, coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.4-mini",
			APIKey:      "test-key",
			AuthIndex:   "0",
			AuthType:    "apikey",
			Source:      "user@example.com",
			RequestedAt: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			Latency:     2500 * time.Millisecond,
			Fail: coreusage.Failure{
				StatusCode: http.StatusInternalServerError,
				Body:       "upstream failed",
			},
			Detail: coreusage.Detail{
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
		})

		payload := popSinglePayload(t)
		requireStringField(t, payload, "provider", "openai")
		requireStringField(t, payload, "model", "gpt-5.4-mini")
		requireStringField(t, payload, "endpoint", "GET /v1/responses")
		requireStringField(t, payload, "auth_type", "apikey")
		requireMissingField(t, payload, "user_api_key")
		requireStringField(t, payload, "request_id", "gin-request-id")
		requireBoolField(t, payload, "failed", true)
		requireFailField(t, payload, http.StatusInternalServerError, "upstream failed")
	})
}

// TestUsageQueuePluginAsyncIgnoresRecycledGinContext proves async publication uses the
// captured record/context values instead of rereading a mutated Gin context.
func TestUsageQueuePluginAsyncIgnoresRecycledGinContext(t *testing.T) {
	withEnabledQueue(t, func() {
		ginCtx := newTestGinContext(t, http.MethodPost, "/v1/chat/completions", http.StatusOK)
		ctx := context.WithValue(context.Background(), "gin", ginCtx)
		ctx = internallogging.WithRequestID(ctx, "ctx-request-id")
		ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
		ctx = internallogging.WithResponseStatusHolder(ctx)
		internallogging.SetResponseStatus(ctx, http.StatusInternalServerError)

		mgr := coreusage.NewManager(16)
		defer mgr.Stop()

		mgr.Register(pluginFunc(func(_ context.Context, _ coreusage.Record) {
			ginCtx.Request = httptest.NewRequest(http.MethodGet, "http://example.com/v1/responses", nil)
			ginCtx.Status(http.StatusOK)
		}))
		mgr.Register(&usageQueuePlugin{})

		mgr.Publish(ctx, coreusage.Record{
			Provider:    "openai",
			Model:       "gpt-5.4",
			APIKey:      "test-key",
			AuthIndex:   "0",
			AuthType:    "apikey",
			Source:      "user@example.com",
			RequestedAt: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			Latency:     1500 * time.Millisecond,
			Fail: coreusage.Failure{
				StatusCode: http.StatusBadGateway,
				Body:       "bad gateway",
			},
			Detail: coreusage.Detail{
				InputTokens:  10,
				OutputTokens: 20,
				TotalTokens:  30,
			},
		})

		payload := waitForSinglePayload(t, 2*time.Second)
		requireStringField(t, payload, "endpoint", "POST /v1/chat/completions")
		requireMissingField(t, payload, "user_api_key")
		requireStringField(t, payload, "request_id", "ctx-request-id")
		requireBoolField(t, payload, "failed", true)
		requireFailField(t, payload, http.StatusBadGateway, "bad gateway")
	})
}

func withEnabledQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := Enabled()
	prevUsageEnabled := UsageStatisticsEnabled()

	SetEnabled(false)
	SetEnabled(true)
	SetUsageStatisticsEnabled(true)

	defer func() {
		SetEnabled(false)
		SetEnabled(prevQueueEnabled)
		SetUsageStatisticsEnabled(prevUsageEnabled)
	}()

	fn()
}

func newTestGinContext(t *testing.T, method, path string, status int) *gin.Context {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(method, "http://example.com"+path, nil)
	if status != 0 {
		ginCtx.Status(status)
	}
	return ginCtx
}

func popSinglePayload(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	items := PopOldest(10)
	if len(items) != 1 {
		t.Fatalf("PopOldest() items = %d, want 1", len(items))
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(items[0], &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload
}

func waitForSinglePayload(t *testing.T, timeout time.Duration) map[string]json.RawMessage {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		items := PopOldest(10)
		if len(items) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if len(items) != 1 {
			t.Fatalf("PopOldest() items = %d, want 1", len(items))
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(items[0], &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		return payload
	}
	t.Fatalf("timeout waiting for queued payload")
	return nil
}

func requireStringField(t *testing.T, payload map[string]json.RawMessage, key, want string) {
	t.Helper()

	raw, ok := payload[key]
	if !ok {
		t.Fatalf("payload missing %q", key)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

// requireMissingField keeps negative payload assertions explicit when sensitive
// fields must be omitted rather than serialized as empty values.
func requireMissingField(t *testing.T, payload map[string]json.RawMessage, key string) {
	t.Helper()

	if _, ok := payload[key]; ok {
		t.Fatalf("payload contains %q, want missing", key)
	}
}

type pluginFunc func(context.Context, coreusage.Record)

func (fn pluginFunc) HandleUsage(ctx context.Context, record coreusage.Record) {
	fn(ctx, record)
}

func requireBoolField(t *testing.T, payload map[string]json.RawMessage, key string, want bool) {
	t.Helper()

	raw, ok := payload[key]
	if !ok {
		t.Fatalf("payload missing %q", key)
	}
	var got bool
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", key, err)
	}
	if got != want {
		t.Fatalf("%s = %t, want %t", key, got, want)
	}
}

func requireFailField(t *testing.T, payload map[string]json.RawMessage, wantStatus int, wantBody string) {
	t.Helper()

	raw, ok := payload["fail"]
	if !ok {
		t.Fatalf("payload missing %q", "fail")
	}
	var got struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal fail: %v", err)
	}
	if got.StatusCode != wantStatus || got.Body != wantBody {
		t.Fatalf("fail = {status_code:%d body:%q}, want {status_code:%d body:%q}", got.StatusCode, got.Body, wantStatus, wantBody)
	}
}

func requireHeaderField(t *testing.T, payload map[string]json.RawMessage, field, key string, want []string) {
	t.Helper()

	raw, ok := payload[field]
	if !ok {
		t.Fatalf("payload missing %q", field)
	}
	var headers map[string][]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		t.Fatalf("unmarshal %q: %v", field, err)
	}
	got, ok := headers[key]
	if !ok {
		t.Fatalf("%s missing header %q", field, key)
	}
	if len(got) != len(want) {
		t.Fatalf("%s[%q] = %v, want %v", field, key, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%q] = %v, want %v", field, key, got, want)
		}
	}
}
