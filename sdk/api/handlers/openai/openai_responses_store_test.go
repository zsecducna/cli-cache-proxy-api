package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type responsesStoreCaptureExecutor struct {
	payload []byte
}

func (e *responsesStoreCaptureExecutor) Identifier() string { return "test-provider" }

func (e *responsesStoreCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.payload = append([]byte(nil), req.Payload...)
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *responsesStoreCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesStoreCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesStoreCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesStoreCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestDefaultStoreForWebsocketPreviousResponseID(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantStore string
		wantExist bool
	}{
		{
			name:      "defaults missing store",
			raw:       `{"model":"test-model","previous_response_id":"resp-1","input":[]}`,
			wantStore: "false",
			wantExist: true,
		},
		{
			name:      "preserves explicit false",
			raw:       `{"model":"test-model","previous_response_id":"resp-1","store":false,"input":[]}`,
			wantStore: "false",
			wantExist: true,
		},
		{
			name:      "does not set without previous response",
			raw:       `{"model":"test-model","input":[]}`,
			wantExist: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultStoreForWebsocketPreviousResponseID([]byte(tt.raw))
			store := gjson.GetBytes(got, "store")
			if store.Exists() != tt.wantExist {
				t.Fatalf("store exists = %v, want %v in %s", store.Exists(), tt.wantExist, got)
			}
			if tt.wantExist && store.Raw != tt.wantStore {
				t.Fatalf("store = %s, want %s in %s", store.Raw, tt.wantStore, got)
			}
		})
	}
}

func TestOpenAIResponsesHTTPDoesNotDefaultStoreForPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &responsesStoreCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "store-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","previous_response_id":"resp-1","input":[]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := gjson.GetBytes(executor.payload, "store"); got.Exists() {
		t.Fatalf("HTTP forwarded unexpected store = %s in %s", got.Raw, executor.payload)
	}
}
