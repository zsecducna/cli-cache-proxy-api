package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type claudeLocalRejectExecutor struct {
	calls int
}

func (e *claudeLocalRejectExecutor) Identifier() string { return handlers.OpenAICompatibilityProvider }

func (e *claudeLocalRejectExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{}, claudeStatusError{status: http.StatusBadRequest, msg: "unsupported feature"}
}

func (e *claudeLocalRejectExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *claudeLocalRejectExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *claudeLocalRejectExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *claudeLocalRejectExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type claudeStatusError struct {
	status int
	msg    string
}

func (e claudeStatusError) Error() string   { return e.msg }
func (e claudeStatusError) StatusCode() int { return e.status }

func TestWriteClaudeErrorResponse_UsesAnthropicJSONShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	h := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	h.writeClaudeErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New("unsupported feature"),
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var body claudeErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Type != "error" {
		t.Fatalf("type = %q, want %q", body.Type, "error")
	}
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want %q", body.Error.Type, "invalid_request_error")
	}
	if body.Error.Message != "unsupported feature" {
		t.Fatalf("error.message = %q, want %q", body.Error.Message, "unsupported feature")
	}
}

func TestClaudeMessages_NonStreamingOpenAICompatErrorsUseAnthropicJSONShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &claudeLocalRejectExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "claude-local-reject", Provider: handlers.OpenAICompatibilityProvider, Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.4-custom"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4-custom","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	var body claudeErrorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Type != "error" {
		t.Fatalf("type = %q, want %q", body.Type, "error")
	}
	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want %q", body.Error.Type, "invalid_request_error")
	}
	const wantMessage = "unsupported feature"
	if body.Error.Message != wantMessage {
		t.Fatalf("error.message = %q, want %q", body.Error.Message, wantMessage)
	}
}
