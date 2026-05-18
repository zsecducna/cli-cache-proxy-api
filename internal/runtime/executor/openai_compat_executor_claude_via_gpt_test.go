package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func collectOpenAICompatStreamChunks(t *testing.T, result *cliproxyexecutor.StreamResult) []byte {
	t.Helper()

	if result == nil {
		t.Fatal("stream result is nil")
	}

	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		if len(payload) > 0 {
			payload = append(payload, '\n', '\n')
		}
		payload = append(payload, chunk.Payload...)
	}
	return payload
}

func TestOpenAICompatExecutorClaudeViaGPTPrefersResponsesSurface(t *testing.T) {
	var gotPath string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"stop_reason\":\"stop\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                  server.URL + "/v1",
		"api_key":                   "test",
		"provider_key":              "openrouter",
		"supports_openai_responses": "true",
		"supports_chat_completions": "true",
		"supports_tools":            "true",
		"supports_streaming":        "true",
	}}

	payload := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          true,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	translated := collectOpenAICompatStreamChunks(t, result)
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q", got, "message")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat-completions messages payload: %s", string(gotBody))
	}
	events := parseAnthropicSSEEvents(t, translated)
	if got := findAnthropicMessageDeltaStopReason(t, events); got != "end_turn" {
		t.Fatalf("message_delta stop_reason = %q, want %q", got, "end_turn")
	}
}

// TestOpenAICompatExecutorClaudeViaGPTDefaultsToResponsesSurface locks the
// default Claude-to-GPT surface to Responses when the backend has not opted out.
func TestOpenAICompatExecutorClaudeViaGPTDefaultsToResponsesSurface(t *testing.T) {
	var gotPath string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"stop_reason\":\"stop\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"provider_key": "openrouter",
	}}

	payload := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          true,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	_ = collectOpenAICompatStreamChunks(t, result)
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q", got, "message")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat-completions messages payload: %s", string(gotBody))
	}
}

// TestOpenAICompatAutoExecutorClaudeViaGPTBypassesWSUpstream verifies the
// auto executor ignores ws_upstream for translated Claude Messages streaming.
func TestOpenAICompatAutoExecutorClaudeViaGPTBypassesWSUpstream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var wsAttempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			atomic.AddInt32(&wsAttempts, 1)
			http.Error(w, "websocket disabled in test", http.StatusBadGateway)
			return
		}
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"stop_reason\":\"stop\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatAutoExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"provider_key": "openrouter",
		"ws_upstream":  "true",
	}}

	payload := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          true,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	_ = collectOpenAICompatStreamChunks(t, result)
	if got := atomic.LoadInt32(&wsAttempts); got != 0 {
		t.Fatalf("websocket attempts = %d, want 0", got)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat-completions messages payload: %s", string(gotBody))
	}
}

func TestOpenAICompatExecutorClaudeViaGPTNonStreamUsesResponsesSurface(t *testing.T) {
	var gotPath string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                  server.URL + "/v1",
		"api_key":                   "test",
		"provider_key":              "openrouter",
		"supports_openai_responses": "true",
		"supports_chat_completions": "true",
		"supports_tools":            "true",
		"supports_streaming":        "true",
	}}

	payload := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          false,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q; body=%s", got, "message", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat-completions messages payload: %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "type").String(); got != "message" {
		t.Fatalf("response type = %q, want %q; body=%s", got, "message", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("response content = %q, want %q; body=%s", got, "hello", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want %q; body=%s", got, "end_turn", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorClaudeViaGPTNonStreamFallsBackToChatCompletions(t *testing.T) {
	var gotPath string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1773896263,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                  server.URL + "/v1",
		"api_key":                   "test",
		"provider_key":              "openrouter",
		"supports_openai_responses": "false",
		"supports_chat_completions": "true",
		"supports_tools":            "true",
		"supports_streaming":        "false",
	}}

	payload := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          false,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want %q; body=%s", got, "user", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("unexpected responses input payload: %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "type").String(); got != "message" {
		t.Fatalf("response type = %q, want %q; body=%s", got, "message", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("response content = %q, want %q; body=%s", got, "hello", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want %q; body=%s", got, "end_turn", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "usage.input_tokens").Int(); got != 5 {
		t.Fatalf("usage.input_tokens = %d, want 5; body=%s", got, string(resp.Payload))
	}
}

func TestOpenAICompatExecutorClaudeViaGPTResponsesPersistsNestedUsage(t *testing.T) {
	t.Cleanup(func() {
		_ = internalusage.ClosePersistentStore()
	})
	cfg := &config.Config{UsageStatisticsEnabled: true}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := internalusage.ConfigurePersistentStore(cfg, configPath); err != nil {
		t.Fatalf("ConfigurePersistentStore() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"stop_reason\":\"stop\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12,\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                  server.URL + "/v1",
		"api_key":                   "test",
		"provider_key":              "openrouter",
		"supports_openai_responses": "true",
		"supports_chat_completions": "true",
		"supports_tools":            "true",
		"supports_streaming":        "true",
	}}

	payload := []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	ctx := context.Background()
	ctx = context.WithValue(ctx, "apiKey", "test-admin-key")
	ctx = helps.WithUsageReasoningEffort(ctx, "xhigh")
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          true,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	_ = collectOpenAICompatStreamChunks(t, result)

	store := internalusage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := store.Snapshot(context.Background(), 10, 10, 1)
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if snapshot.Summary.TotalRequests == 1 {
			if snapshot.Summary.TotalTokens != 12 {
				t.Fatalf("total_tokens = %d, want 12", snapshot.Summary.TotalTokens)
			}
			if len(snapshot.RecentRequests) != 1 {
				t.Fatalf("recent requests len = %d, want 1", len(snapshot.RecentRequests))
			}
			item := snapshot.RecentRequests[0]
			if item.Provider != "openrouter" {
				t.Fatalf("provider = %q, want %q", item.Provider, "openrouter")
			}
			if item.ReasoningEffort != "xhigh" {
				t.Fatalf("reasoning_effort = %q, want %q", item.ReasoningEffort, "xhigh")
			}
			if item.InputTokens != 5 || item.OutputTokens != 7 || item.TotalTokens != 12 {
				t.Fatalf("usage = %+v, want input/output/total 5/7/12", item)
			}
			if item.ReasoningTokens != 3 {
				t.Fatalf("reasoning_tokens = %d, want 3", item.ReasoningTokens)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for persisted usage snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpenAICompatExecutorResponsesNonStreamPersistsNestedUsage(t *testing.T) {
	t.Cleanup(func() {
		_ = internalusage.ClosePersistentStore()
	})
	cfg := &config.Config{UsageStatisticsEnabled: true}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := internalusage.ConfigurePersistentStore(cfg, configPath); err != nil {
		t.Fatalf("ConfigurePersistentStore() error = %v", err)
	}

	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"response":{"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12,"output_tokens_details":{"reasoning_tokens":3}}}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                  server.URL + "/v1",
		"api_key":                   "test",
		"provider_key":              "openrouter",
		"supports_openai_responses": "true",
		"supports_chat_completions": "true",
		"supports_tools":            "true",
		"supports_streaming":        "true",
	}}

	payload := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	ctx := context.Background()
	ctx = context.WithValue(ctx, "apiKey", "test-admin-key")
	ctx = helps.WithUsageReasoningEffort(ctx, "xhigh")
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		Stream:          false,
		OriginalRequest: payload,
		Metadata:        nil,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses input payload, got %s", string(gotBody))
	}

	store := internalusage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := store.Snapshot(context.Background(), 10, 10, 1)
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if snapshot.Summary.TotalRequests == 1 {
			if snapshot.Summary.TotalTokens != 12 {
				t.Fatalf("total_tokens = %d, want 12", snapshot.Summary.TotalTokens)
			}
			if len(snapshot.RecentRequests) != 1 {
				t.Fatalf("recent requests len = %d, want 1", len(snapshot.RecentRequests))
			}
			item := snapshot.RecentRequests[0]
			if item.Provider != "openrouter" {
				t.Fatalf("provider = %q, want %q", item.Provider, "openrouter")
			}
			if item.ReasoningEffort != "xhigh" {
				t.Fatalf("reasoning_effort = %q, want %q", item.ReasoningEffort, "xhigh")
			}
			if item.InputTokens != 5 || item.OutputTokens != 7 || item.TotalTokens != 12 {
				t.Fatalf("usage = %+v, want input/output/total 5/7/12", item)
			}
			if item.ReasoningTokens != 3 {
				t.Fatalf("reasoning_tokens = %d, want 3", item.ReasoningTokens)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for persisted usage snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpenAICompatExecutorClaudeViaGPTRejectsToolTurnsWithoutResponsesSurface(t *testing.T) {
	var upstreamCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1773896263,\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"unexpected\"},\"finish_reason\":null}]}\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                  server.URL + "/v1",
		"api_key":                   "test",
		"provider_key":              "openrouter",
		"supports_openai_responses": "false",
		"supports_chat_completions": "true",
		"supports_tools":            "true",
		"supports_streaming":        "true",
	}}

	payload := []byte(`{
		"model":"gpt-5.4",
		"stream":true,
		"tools":[{"name":"lookup","description":"lookup","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"lookup","input":{"id":"123"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":{"ok":true}}]}
		]
	}`)

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		Stream:          true,
		OriginalRequest: payload,
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err == nil {
		t.Fatal("ExecuteStream error = nil, want non-nil")
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %v, want %d", err, http.StatusBadRequest)
	}
}
