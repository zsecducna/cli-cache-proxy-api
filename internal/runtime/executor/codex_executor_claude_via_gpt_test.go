package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestCodexExecutorClaudeViaGPTNonStreamUsesResponsesInput locks the Claude-via-GPT
// contract for Codex HTTP /responses requests so Anthropic messages never leak
// through as chat-completions `messages`.
func TestCodexExecutorClaudeViaGPTNonStreamUsesResponsesInput(t *testing.T) {
	var gotPath string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"stop_reason\":\"stop\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	payload := []byte(`{"model":"gpt-5.4","max_tokens":64,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"Reply with OK only."}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
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

	if gotPath != "/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/responses")
	}
	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q; body=%s", got, "message", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat-completions messages payload: %s", string(gotBody))
	}
}

// TestCodexExecutorClaudeViaGPTStreamUsesResponsesInput locks the streaming
// Claude-via-GPT contract for Codex HTTP /responses requests so Anthropic
// messages are lifted onto Responses `input` items before upstream execution.
func TestCodexExecutorClaudeViaGPTStreamUsesResponsesInput(t *testing.T) {
	var gotPath string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"stop_reason\":\"stop\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":7,\"total_tokens\":12}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	payload := []byte(`{"model":"gpt-5.4","max_tokens":64,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"Reply with OK only."}]}]}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
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
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	if gotPath != "/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/responses")
	}
	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q; body=%s", got, "message", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat-completions messages payload: %s", string(gotBody))
	}
}
