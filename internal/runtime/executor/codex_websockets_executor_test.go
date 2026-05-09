package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestApplyCodexPromptCacheHeadersReusesPreviousResponsePromptCacheKey(t *testing.T) {
	recordCodexPromptCacheResponse(context.Background(), []byte(`{"type":"response.completed","response":{"id":"resp-1"}}`), "session-cache", codexPromptCache24hTTL)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","previous_response_id":"resp-1"}`),
	}

	body, headers, selection := applyCodexPromptCacheHeaders(context.Background(), sdktranslator.FromString("openai-response"), req, req.Payload, "https://api.openai.com/v1/responses")
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "session-cache" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "session-cache")
	}
	if got := gjson.GetBytes(body, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %q, want %q", got, "resp-1")
	}
	if got := headers.Get("Conversation_id"); got != "session-cache" {
		t.Fatalf("Conversation_id = %q, want %q", got, "session-cache")
	}
	if selection.Key != "session-cache" {
		t.Fatalf("selection.Key = %q, want %q", selection.Key, "session-cache")
	}
}

func TestApplyCodexPromptCacheHeadersPreservesPromptCacheRetentionForNonBackendAPIURL(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_retention":"24h"}`),
	}

	body, _, selection := applyCodexPromptCacheHeaders(context.Background(), sdktranslator.FromString("openai-response"), req, req.Payload, "https://api.openai.com/v1/responses")
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "24h" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
	}
	if selection.TTL != codexPromptCache24hTTL {
		t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCache24hTTL)
	}
}

func TestApplyCodexPromptCacheHeadersStripsPromptCacheRetentionForBackendWebsocketURL(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_retention":"24h"}`),
	}

	body, _, selection := applyCodexPromptCacheHeaders(context.Background(), sdktranslator.FromString("openai-response"), req, req.Payload, "https://chatgpt.com/backend-api/codex/responses")
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "" {
		t.Fatalf("prompt_cache_retention = %q, want empty", got)
	}
	if selection.TTL != codexPromptCacheDefaultTTL {
		t.Fatalf("selection.TTL = %s, want %s", selection.TTL, codexPromptCacheDefaultTTL)
	}
}

func TestBuildCodexWebsocketRequestBodyPreservesPreviousResponseID(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	if got := gjson.GetBytes(wsReqBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %s, want response.create", got)
	}
	if got := gjson.GetBytes(wsReqBody, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %s, want resp-1", got)
	}
	if gjson.GetBytes(wsReqBody, "input.0.id").String() != "msg-1" {
		t.Fatalf("input item id mismatch")
	}
	if got := gjson.GetBytes(wsReqBody, "type").String(); got == "response.append" {
		t.Fatalf("unexpected websocket request type: %s", got)
	}
}

func TestApplyCodexWebsocketClientStore(t *testing.T) {
	cases := []struct {
		name     string
		original string
		want     string
	}{
		{
			name:     "preserves explicit true",
			original: `{"store":true}`,
			want:     "true",
		},
		{
			name:     "preserves explicit false",
			original: `{"store":false}`,
			want:     "false",
		},
		{
			name:     "defaults missing store to false",
			original: `{}`,
			want:     "false",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := applyCodexWebsocketClientStore([]byte(`{"store":false}`), []byte(tc.original))
			if store := gjson.GetBytes(got, "store").Raw; store != tc.want {
				t.Fatalf("store = %s, want %s in %s", store, tc.want, got)
			}
		})
	}
}

func TestApplyCodexWebsocketHeadersDefaultsToCurrentResponsesBeta(t *testing.T) {
	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, nil, "", nil)

	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func TestCodexWebsocketsEnabledAcceptsSingularAuthMetadata(t *testing.T) {
	cases := []struct {
		name string
		auth *cliproxyauth.Auth
	}{
		{
			name: "singular metadata bool",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"websocket": true}},
		},
		{
			name: "singular metadata string",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"websocket": "true"}},
		},
		{
			name: "singular attribute",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"websocket": "true"}},
		},
		{
			name: "plural attribute",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"websockets": "true"}},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !codexWebsocketsEnabled(tc.auth) {
				t.Fatalf("codexWebsocketsEnabled() = false, want true")
			}
		})
	}
}

func TestCodexWebsocketsExecuteStreamAddsEmptyInstructions(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.4-mini","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":[{"role":"user","content":[{"type":"input_text","text":"ok"}]}],"stream":true,"store":true,"max_tokens":16,"max_output_tokens":32,"max_completion_tokens":64}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		if !gjson.GetBytes(payload, "instructions").Exists() {
			t.Fatalf("websocket request missing instructions: %s", payload)
		}
		if got := gjson.GetBytes(payload, "instructions").String(); got != "" {
			t.Fatalf("instructions = %q, want empty", got)
		}
		if got := gjson.GetBytes(payload, "store").Raw; got != "true" {
			t.Fatalf("store = %s, want true in %s", got, payload)
		}
		for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
			if gjson.GetBytes(payload, field).Exists() {
				t.Fatalf("websocket request still has unsupported %s: %s", field, payload)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestCodexWebsocketsExecuteStreamShortensLongCallIDs(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	longCallID := "call_" + strings.Repeat("a", 73)
	exec := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"function_call","call_id":"` + longCallID + `","name":"tool","arguments":"{}"},{"type":"function_call_output","call_id":"` + longCallID + `","output":"ok"}],"stream":true,"store":true}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		callID := gjson.GetBytes(payload, "input.0.call_id").String()
		outputCallID := gjson.GetBytes(payload, "input.1.call_id").String()
		if len(callID) > 64 {
			t.Fatalf("call_id length = %d, want <= 64: %q", len(callID), callID)
		}
		if callID == longCallID {
			t.Fatal("call_id was not shortened")
		}
		if outputCallID != callID {
			t.Fatalf("function_call_output call_id = %q, want %q", outputCallID, callID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestCodexWebsocketsExecuteStreamPreservesIncrementalOutputOnlyCallID(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	longCallID := "call_" + strings.Repeat("b", 73)
	exec := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","previous_response_id":"resp-prev","input":[{"type":"function_call_output","call_id":"` + longCallID + `","output":"ok"}],"stream":true,"store":true}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-prev" {
			t.Fatalf("previous_response_id = %q, want resp-prev", got)
		}
		if got := gjson.GetBytes(payload, "input.0.call_id").String(); got != longCallID {
			t.Fatalf("incremental output call_id = %q, want original %q in %s", got, longCallID, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestCodexWebsocketsExecuteStreamTranslatesOpenAIResponsesPayload(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"be exact"}]}],
			"context_management":{"compaction":{"type":"auto"}},
			"truncation":"disabled",
			"temperature":0.2,
			"top_p":0.8,
			"stream":true,
			"store":true
		}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		if got := gjson.GetBytes(payload, "input.0.role").String(); got != "developer" {
			t.Fatalf("input.0.role = %q, want developer; payload=%s", got, payload)
		}
		for _, field := range []string{"context_management", "truncation", "temperature", "top_p"} {
			if gjson.GetBytes(payload, field).Exists() {
				t.Fatalf("websocket request still has unsupported %s: %s", field, payload)
			}
		}
		if got := gjson.GetBytes(payload, "store").Raw; got != "true" {
			t.Fatalf("store = %s, want true in %s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestCodexWebsocketsExecuteStreamTranslatesOpenAIChatPayload(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.4-mini","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Reply with OK only."}],"stream":true}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		if gjson.GetBytes(payload, "messages").Exists() {
			t.Fatalf("websocket request still has chat messages: %s", payload)
		}
		if got := gjson.GetBytes(payload, "input.0.role").String(); got != "user" {
			t.Fatalf("input.0.role = %q, want user; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "input.0.content.0.text").String(); got != "Reply with OK only." {
			t.Fatalf("input.0.content.0.text = %q, want prompt; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestCodexWebsocketsExecuteStreamStripsPrefixedPayloadModel(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gate1/gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}],"stream":true,"store":true}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"model":"gate1/gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}],"stream":true,"store":true}`),
		SourceFormat:    sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		if got := gjson.GetBytes(payload, "model").String(); got != "gpt-5.5" {
			t.Fatalf("upstream websocket model = %q, want %q; payload=%s", got, "gpt-5.5", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestOpenAICompatWebsocketsExecuteStreamStripsPrefixedPayloadModel(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("Upgrade() error = %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("Close() error = %v", errClose)
			}
		}()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("ReadMessage() error = %v", errRead)
			return
		}
		received <- payload

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-test","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("WriteMessage() error = %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewOpenAICompatWebsocketsExecutor("test-provider", nil)
	auth := &cliproxyauth.Auth{
		ID:       "compat-test",
		Provider: "test-provider",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gate1/gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}],"stream":true,"store":true}`),
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"model":"gate1/gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}],"stream":true,"store":true}`),
		SourceFormat:    sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-received:
		if got := gjson.GetBytes(payload, "model").String(); got != "gpt-5.5" {
			t.Fatalf("upstream websocket model = %q, want %q; payload=%s", got, "gpt-5.5", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket request")
	}
}

func TestApplyCodexWebsocketHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := headers.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexWebsocketHeadersUsesConfigDefaultsForOAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "my-codex-client/1.0",
			BetaFeatures: "feature-a,feature-b",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "feature-a,feature-b" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "feature-a,feature-b")
	}
	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
}

func TestApplyCodexWebsocketHeadersPrefersExistingHeadersOverClientAndConfig(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")
	headers.Set("X-Codex-Beta-Features", "existing-beta")

	got := applyCodexWebsocketHeaders(ctx, headers, auth, "", cfg)

	if gotVal := got.Get("User-Agent"); gotVal != "" {
		t.Fatalf("User-Agent = %s, want empty", gotVal)
	}
	if gotVal := got.Get("x-codex-beta-features"); gotVal != "existing-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", gotVal, "existing-beta")
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesClientHeader(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "client-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "client-beta")
	}
}

func TestApplyCodexWebsocketHeadersIgnoresConfigForAPIKeyAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "sk-test", cfg)

	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("Originator"); got != "" {
		t.Fatalf("Originator = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersUsesCanonicalAccountHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", nil)

	if got := headerValueCaseInsensitive(headers, "ChatGPT-Account-ID"); got != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID = %s, want acct-1", got)
	}
	values, ok := headers["ChatGPT-Account-ID"]
	if !ok {
		t.Fatalf("expected exact ChatGPT-Account-ID key, got %#v", headers)
	}
	if len(values) != 1 || values[0] != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID values = %#v, want [acct-1]", values)
	}
}

func TestBuildCodexResponsesWebsocketURLRequiresHTTPURL(t *testing.T) {
	if got, err := buildCodexResponsesWebsocketURL("https://example.com/backend/responses"); err != nil || got != "wss://example.com/backend/responses" {
		t.Fatalf("https URL = %q, %v; want wss URL", got, err)
	}
	if _, err := buildCodexResponsesWebsocketURL("ftp://example.com/responses"); err == nil {
		t.Fatalf("expected unsupported scheme error")
	}
	if _, err := buildCodexResponsesWebsocketURL("https:///responses"); err == nil {
		t.Fatalf("expected empty host error")
	}
}

func TestParseCodexWebsocketErrorMarksConnectionLimitRetryable(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many websockets"},"headers":{"retry-after":"1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable websocket connection limit error")
	}
	if got := *retryable.RetryAfter(); got != 0 {
		t.Fatalf("retryAfter = %v, want connection-limit fallback 0", got)
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("retry-after") != "1" {
		t.Fatalf("headers = %#v, want retry-after", err)
	}
}

func TestParseCodexWebsocketErrorUsesUsageLimitRetryMetadata(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":7}}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable usage limit websocket error")
	}
	if got := *retryable.RetryAfter(); got != 7*time.Second {
		t.Fatalf("retryAfter = %v, want 7s", got)
	}
}

func TestParseCodexWebsocketErrorPreservesWrappedBodyAndHeaders(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"code":"websocket_connection_limit_reached","type":"server_error","message":"too many websocket connections"}},"headers":{"x-request-id":"req-1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	parsed := gjson.Parse(err.Error())
	if got := parsed.Get("status").Int(); got != http.StatusTooManyRequests {
		t.Fatalf("wrapped status = %d, want 429; payload=%s", got, err.Error())
	}
	if got := parsed.Get("body.error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("wrapped body error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	if got := parsed.Get("error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("surface error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected body.error.code websocket connection limit to be retryable")
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("x-request-id") != "req-1" {
		t.Fatalf("headers = %#v, want x-request-id", err)
	}
}

func TestApplyCodexHeadersUsesConfigUserAgentForOAuth(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
}

func TestApplyCodexHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, nil)

	if got := req.Header.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := req.Header.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexHeadersDoesNotInjectClientOnlyHeadersByDefault(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, nil, "oauth-token", true, nil)

	if got := req.Header.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func contextWithGinHeaders(headers map[string]string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ginCtx.Request.Header = make(http.Header, len(headers))
	for key, value := range headers {
		ginCtx.Request.Header.Set(key, value)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestNewProxyAwareWebsocketDialerDirectDisablesProxy(t *testing.T) {
	t.Parallel()

	dialer := newProxyAwareWebsocketDialer(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
	)

	if dialer.Proxy != nil {
		t.Fatal("expected websocket proxy function to be nil for direct mode")
	}
}

func TestCodexPreferWSUpstream(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		auth *cliproxyauth.Auth
		want bool
	}{
		{
			name: "nil auth",
			auth: nil,
			want: false,
		},
		{
			name: "no attributes or metadata",
			auth: &cliproxyauth.Auth{},
			want: false,
		},
		{
			name: "ws_upstream attribute true",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"ws_upstream": "true"}},
			want: true,
		},
		{
			name: "ws_upstream attribute false",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"ws_upstream": "false"}},
			want: false,
		},
		{
			name: "websocket_upstream attribute true",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"websocket_upstream": "true"}},
			want: true,
		},
		{
			name: "ws_upstream metadata bool",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"ws_upstream": true}},
			want: true,
		},
		{
			name: "ws_upstream metadata string",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"ws_upstream": "true"}},
			want: true,
		},
		{
			name: "websocket_upstream metadata bool",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"websocket_upstream": true}},
			want: true,
		},
		{
			name: "websockets enabled but no ws_upstream",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"websockets": "true"}},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := codexPreferWSUpstream(tc.auth)
			if got != tc.want {
				t.Fatalf("codexPreferWSUpstream() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsWSUpstreamFallbackEligible(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "dial error",
			err:  fmt.Errorf("websocket dial tcp: connection refused"),
			want: true,
		},
		{
			name: "handshake error",
			err:  fmt.Errorf("websocket handshake failed: 502"),
			want: true,
		},
		{
			name: "conn nil",
			err:  fmt.Errorf("codex websockets executor: websocket conn is nil"),
			want: true,
		},
		{
			name: "i/o timeout",
			err:  fmt.Errorf("read tcp: i/o timeout"),
			want: true,
		},
		{
			name: "no such host",
			err:  fmt.Errorf("dial tcp: no such host"),
			want: true,
		},
		{
			name: "auth error not eligible",
			err:  fmt.Errorf("401 unauthorized"),
			want: false,
		},
		{
			name: "rate limit not eligible",
			err:  fmt.Errorf("429 rate limit exceeded"),
			want: false,
		},
		{
			name: "model not supported not eligible",
			err:  fmt.Errorf("model gpt-5.5 is not supported"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isWSUpstreamFallbackEligible(tc.err)
			if got != tc.want {
				t.Fatalf("isWSUpstreamFallbackEligible() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeLongCodexCallIDsInBodyShortensAndPreservesPairs(t *testing.T) {
	longCallID := "call_" + strings.Repeat("a", 73)
	body := []byte(`{"input":[{"type":"function_call","call_id":"` + longCallID + `","name":"tool","arguments":"{}"},{"type":"function_call_output","call_id":"` + longCallID + `","output":"ok"}]}`)

	result := normalizeLongCodexCallIDsInBody(body)
	callID := gjson.GetBytes(result, "input.0.call_id").String()
	outputCallID := gjson.GetBytes(result, "input.1.call_id").String()

	if len(callID) > 64 {
		t.Fatalf("call_id length = %d, want <= 64: %q", len(callID), callID)
	}
	if callID == longCallID {
		t.Fatal("call_id was not shortened")
	}
	if outputCallID != callID {
		t.Fatalf("function_call_output call_id = %q, want %q", outputCallID, callID)
	}
}

var benchmarkCodexExecutorNormalizedBody []byte

func BenchmarkNormalizeLongCodexCallIDsInBodyLargeInput(b *testing.B) {
	body := largeCodexExecutorInputBody(1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkCodexExecutorNormalizedBody = normalizeLongCodexCallIDsInBody(body)
	}
}

func largeCodexExecutorInputBody(items int) []byte {
	longCallID := "call_" + strings.Repeat("b", 73)
	var builder strings.Builder
	builder.Grow(items * 120)
	builder.WriteString(`{"input":[`)
	for i := 0; i < items; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		switch i % 4 {
		case 0:
			builder.WriteString(`{"type":"message","role":"user","content":"hello"}`)
		case 1:
			builder.WriteString(`{"type":"function_call","call_id":"`)
			builder.WriteString(longCallID)
			builder.WriteString(`","name":"tool","arguments":"{}"}`)
		case 2:
			builder.WriteString(`{"type":"function_call_output","call_id":"`)
			builder.WriteString(longCallID)
			builder.WriteString(`","output":"ok"}`)
		default:
			builder.WriteString(`{"type":"message","role":"assistant","content":"ok"}`)
		}
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

func TestSanitizeCodexWebsocketToolPairsDropsOrphanedOutput(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"call-1","name":"tool"},
		{"type":"function_call_output","call_id":"call-1","output":"ok"},
		{"type":"function_call_output","call_id":"call-orphan","output":"stale"}
	]}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	for _, item := range items {
		if item.Get("call_id").String() == "call-orphan" {
			t.Fatal("orphaned output not removed")
		}
	}
}

func TestSanitizeCodexWebsocketToolPairsPreservesIncrementalToolOutput(t *testing.T) {
	body := []byte(`{"previous_response_id":"resp-1","input":[
		{"type":"function_call_output","call_id":"call-prev","output":"ok"},
		{"type":"custom_tool_call_output","call_id":"call-custom-prev","output":"ok"}
	]}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 2 {
		t.Fatalf("incremental output-only payload should be preserved, got %d items", len(items))
	}
	if got := items[0].Get("call_id").String(); got != "call-prev" {
		t.Fatalf("first output call_id = %q", got)
	}
	if got := items[1].Get("call_id").String(); got != "call-custom-prev" {
		t.Fatalf("second output call_id = %q", got)
	}
}

func TestResetCodexWebsocketContinuationForAuthSwitchClearsPreviousResponseAndStaleToolOutputs(t *testing.T) {
	exec := NewCodexWebsocketsExecutor(nil)
	sess := &codexWebsocketSession{sessionID: "session-1", authID: "auth-a"}
	body := []byte(`{"previous_response_id":"resp-old","prompt_cache_key":"old-cache","input":[
		{"type":"function_call_output","call_id":"call-prev","output":"stale"},
		{"type":"custom_tool_call_output","call_id":"custom-prev","output":"stale"},
		{"type":"function_call","call_id":"call-local","name":"tool","arguments":"{}"},
		{"type":"function_call_output","call_id":"call-local","output":"ok"}
	]}`)

	result, reset := exec.resetCodexWebsocketContinuationForAuthSwitch(sess, "auth-b", body)

	if !reset {
		t.Fatal("reset = false, want true")
	}
	if got := gjson.GetBytes(result, "previous_response_id").Raw; got != "null" {
		t.Fatalf("previous_response_id = %s, want null in %s", got, result)
	}
	if gjson.GetBytes(result, "prompt_cache_key").Exists() {
		t.Fatalf("prompt_cache_key should be removed on auth switch: %s", result)
	}
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 2 {
		t.Fatalf("expected stale output-only item to be removed, got %d items in %s", len(items), result)
	}
	for _, item := range items {
		if got := item.Get("call_id").String(); got == "call-prev" || got == "custom-prev" {
			t.Fatalf("stale tool output was preserved: %s", result)
		}
	}
	if got := items[0].Get("type").String(); got != "function_call" {
		t.Fatalf("first remaining item type = %q, want function_call", got)
	}
	if got := items[1].Get("type").String(); got != "function_call_output" {
		t.Fatalf("second remaining item type = %q, want function_call_output", got)
	}
}

func TestResetCodexWebsocketContinuationForSameAuthPreservesIncrementalToolOutput(t *testing.T) {
	exec := NewCodexWebsocketsExecutor(nil)
	sess := &codexWebsocketSession{sessionID: "session-1", authID: "auth-a"}
	body := []byte(`{"previous_response_id":"resp-old","input":[
		{"type":"function_call_output","call_id":"call-prev","output":"ok"}
	]}`)

	result, reset := exec.resetCodexWebsocketContinuationForAuthSwitch(sess, "auth-a", body)

	if reset {
		t.Fatal("reset = true, want false")
	}
	if got := gjson.GetBytes(result, "previous_response_id").String(); got != "resp-old" {
		t.Fatalf("previous_response_id = %q, want resp-old", got)
	}
	if got := gjson.GetBytes(result, "input.0.call_id").String(); got != "call-prev" {
		t.Fatalf("tool output call_id = %q, want call-prev", got)
	}
}

func TestSanitizeCodexWebsocketToolPairsStripsTrailingCalls(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"call-1","name":"tool"},
		{"type":"function_call","call_id":"call-2","name":"tool2"}
	]}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 1 {
		t.Fatalf("orphaned trailing calls should be stripped, got %d items", len(items))
	}
	if items[0].Get("role").String() != "user" {
		t.Fatalf("remaining item should be user message, got %s", items[0].Raw)
	}
}

func TestSanitizeCodexWebsocketToolPairsStripsTrailingCustomCalls(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","content":"hi"},
		{"type":"custom_tool_call","call_id":"call-custom","name":"tool"}
	]}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 1 {
		t.Fatalf("orphaned custom call should be stripped, got %d items", len(items))
	}
	if items[0].Get("role").String() != "user" {
		t.Fatalf("remaining item should be user message, got %s", items[0].Raw)
	}
}

func TestSanitizeCodexWebsocketToolPairsStripsNonTrailingOrphanCall(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call","call_id":"call-orphan","name":"tool"},
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"call-trailing","name":"tool2"}
	]}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 1 {
		t.Fatalf("expected 1 item (orphans removed), got %d", len(items))
	}
	if items[0].Get("role").String() != "user" {
		t.Fatalf("first item should be user message, got %s", items[0].Raw)
	}
}

func TestSanitizeCodexWebsocketToolPairsStripsInterleavedTrailing(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"call-a","name":"tool"},
		{"type":"message","role":"assistant","content":"thinking"},
		{"type":"function_call","call_id":"call-b","name":"tool2"}
	]}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	items := gjson.GetBytes(result, "input").Array()
	if len(items) != 2 {
		t.Fatalf("expected 2 message items, got %d", len(items))
	}
	for _, item := range items {
		if callID := item.Get("call_id").String(); callID != "" {
			t.Fatalf("orphan call should have been removed: %s", item.Raw)
		}
	}
}

func TestSanitizeCodexWebsocketToolPairsEmptyInput(t *testing.T) {
	body := []byte(`{"input":[],"model":"gpt-4"}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	if string(result) != string(body) {
		t.Fatalf("empty input should return unchanged body")
	}
}

func TestSanitizeCodexWebsocketToolPairsNoInput(t *testing.T) {
	body := []byte(`{"model":"gpt-4"}`)
	result := sanitizeCodexWebsocketToolPairs(body)
	if string(result) != string(body) {
		t.Fatalf("missing input should return unchanged body")
	}
}
