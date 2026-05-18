package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityExecuteClaudeNonStream_DoesNotLeakCacheControl(t *testing.T) {
	largeText := strings.Repeat("cache me through antigravity ", 900)

	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body error: %v", err)
		}
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}}` + "\n"))
	}))
	defer server.Close()

	originalOrder := antigravityBaseURLFallbackOrder
	antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
		return []string{server.URL}
	}
	defer func() {
		antigravityBaseURLFallbackOrder = originalOrder
	}()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "antigravity-claude-cache",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	payload := []byte(fmt.Sprintf(`{
		"model":"claude-sonnet-4-5",
		"system":[
			{"type":"text","text":"%s"},
			{"type":"text","text":"system tail"}
		],
		"tools":[
			{"name":"tool_a","description":"%s","input_schema":{"type":"object"}},
			{"name":"tool_b","description":"secondary tool","input_schema":{"type":"object"}}
		],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"%s"}]},
			{"role":"assistant","content":[{"type":"text","text":"reply"}]},
			{"role":"user","content":[{"type":"text","text":"follow up"}]}
		]
	}`, largeText, largeText, largeText))

	_, err := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("executeClaudeNonStream() error = %v", err)
	}

	if gjson.GetBytes(capturedBody, "request.tools.0.functionDeclarations.1.cache_control").Exists() {
		t.Fatalf("tool cache_control should be stripped; body=%s", string(capturedBody))
	}
	if gjson.GetBytes(capturedBody, "request.systemInstruction.parts.1.cache_control").Exists() {
		t.Fatalf("system cache_control should be stripped; body=%s", string(capturedBody))
	}
	if gjson.GetBytes(capturedBody, "request.contents.0.parts.0.cache_control").Exists() {
		t.Fatalf("message cache_control should be stripped; body=%s", string(capturedBody))
	}
}

func TestAntigravityCountTokens_ClaudeDoesNotLeakCacheControl(t *testing.T) {
	largeText := strings.Repeat("count my claude cache tokens ", 900)

	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body error: %v", err)
		}
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":4096}` + "\n"))
	}))
	defer server.Close()

	originalOrder := antigravityBaseURLFallbackOrder
	antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
		return []string{server.URL}
	}
	defer func() {
		antigravityBaseURLFallbackOrder = originalOrder
	}()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "antigravity-claude-count-cache",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	payload := []byte(fmt.Sprintf(`{
		"model":"claude-sonnet-4-6",
		"system":[
			{"type":"text","text":"%s"},
			{"type":"text","text":"system tail"}
		],
		"tools":[
			{"name":"tool_a","description":"%s","input_schema":{"type":"object"}},
			{"name":"tool_b","description":"secondary tool","input_schema":{"type":"object"}}
		],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"%s"}]},
			{"role":"assistant","content":[{"type":"text","text":"reply"}]},
			{"role":"user","content":[{"type":"text","text":"follow up"}]}
		]
	}`, largeText, largeText, largeText))

	resp, err := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}

	if gjson.GetBytes(capturedBody, "request.tools.0.functionDeclarations.1.cache_control").Exists() {
		t.Fatalf("tool cache_control should be stripped; body=%s", string(capturedBody))
	}
	if gjson.GetBytes(capturedBody, "request.systemInstruction.parts.1.cache_control").Exists() {
		t.Fatalf("system cache_control should be stripped; body=%s", string(capturedBody))
	}
	if gjson.GetBytes(capturedBody, "request.contents.0.parts.0.cache_control").Exists() {
		t.Fatalf("message cache_control should be stripped; body=%s", string(capturedBody))
	}
	if got := gjson.GetBytes(resp.Payload, "input_tokens").Int(); got != 4096 {
		t.Fatalf("token count payload input_tokens = %d, want %d; payload=%s", got, 4096, string(resp.Payload))
	}
}

func TestAntigravityExecute_NonClaudeDoesNotApplyClaudeCachePolicy(t *testing.T) {
	largeText := strings.Repeat("non claude payload ", 900)

	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body error: %v", err)
		}
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}}` + "\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "antigravity-non-claude-cache",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	payload := []byte(fmt.Sprintf(`{
		"model":"gemini-2.5-pro",
		"system":[
			{"type":"text","text":"%s"},
			{"type":"text","text":"system tail"}
		],
		"tools":[
			{"name":"tool_a","description":"%s","input_schema":{"type":"object"}},
			{"name":"tool_b","description":"secondary tool","input_schema":{"type":"object"}}
		],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"%s"}]},
			{"role":"assistant","content":[{"type":"text","text":"reply"}]},
			{"role":"user","content":[{"type":"text","text":"follow up"}]}
		]
	}`, largeText, largeText, largeText))

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-pro",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if strings.Contains(string(capturedBody), `"cache_control"`) {
		t.Fatalf("non-claude request should not get anthropic cache rewrite; body=%s", string(capturedBody))
	}
}
