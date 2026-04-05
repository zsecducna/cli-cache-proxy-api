package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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
