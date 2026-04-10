package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type routeMetadataCaptureExecutor struct {
	calls int
	req   coreexecutor.Request
	opts  coreexecutor.Options
}

func (e *routeMetadataCaptureExecutor) Identifier() string { return OpenAICompatibilityProvider }

func (e *routeMetadataCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.req = req
	e.opts = opts
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *routeMetadataCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *routeMetadataCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *routeMetadataCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *routeMetadataCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type routeMetadataTestHandler struct{}

func (routeMetadataTestHandler) HandlerType() string { return "claude" }

func (routeMetadataTestHandler) Models() []map[string]any { return nil }

func TestExecuteWithAuthManager_ThreadsRouteAndRequestIDMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &routeMetadataCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "openai-compat-auth", Provider: OpenAICompatibilityProvider, Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.4-custom"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4-custom"}`))
	req = req.WithContext(logging.WithRequestID(req.Context(), "req-lane1"))
	ginCtx.Request = req

	cliCtx, cliCancel := base.GetContextWithCancel(routeMetadataTestHandler{}, ginCtx, context.Background())
	defer cliCancel()

	resp, _, errMsg := base.ExecuteWithAuthManager(cliCtx, "claude", "  gpt-5.4-custom  ", []byte(`{"model":"gpt-5.4-custom"}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %v, want nil", errMsg)
	}
	if strings.TrimSpace(string(resp)) != `{"ok":true}` {
		t.Fatalf("response = %s", string(resp))
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if got, want := executor.req.Model, "gpt-5.4-custom"; got != want {
		t.Fatalf("executor request model = %q, want %q", got, want)
	}
	if got, want := executor.opts.Metadata[coreexecutor.RequestRouteMetadataKey], string(RequestRouteClaudeViaOpenAICompat); got != want {
		t.Fatalf("route metadata = %v, want %q", got, want)
	}
	if got, want := executor.opts.Metadata[coreexecutor.RequestIDMetadataKey], "req-lane1"; got != want {
		t.Fatalf("request_id metadata = %v, want %q", got, want)
	}
	if got, want := executor.opts.Metadata[coreexecutor.RequestedModelMetadataKey], "gpt-5.4-custom"; got != want {
		t.Fatalf("requested_model metadata = %v, want %q", got, want)
	}
}

func TestGetContextWithCancelMarksCanceledRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4"}`))

	_, cancel := base.GetContextWithCancel(routeMetadataTestHandler{}, ginCtx, context.Background())
	cancel(context.Canceled)

	if !logging.WasRequestCanceled(ginCtx) {
		t.Fatal("expected canceled request marker to be stored on gin context")
	}
}
