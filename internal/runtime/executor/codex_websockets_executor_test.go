package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	body, headers, selection := applyCodexPromptCacheHeaders(context.Background(), sdktranslator.FromString("openai-response"), req, req.Payload)
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

func TestApplyCodexPromptCacheHeadersStripsPromptCacheRetentionForWebsocketRequests(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_retention":"24h"}`),
	}

	body, _, selection := applyCodexPromptCacheHeaders(context.Background(), sdktranslator.FromString("openai-response"), req, req.Payload)
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
