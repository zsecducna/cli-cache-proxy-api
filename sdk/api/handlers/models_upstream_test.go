package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestMapUpstreamChatGPTModelsToOpenAIList_MapsGPT55ThinkingToGPT55(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5-5-thinking"},{"slug":"gpt-5.4"}]}`)

	models, err := MapUpstreamChatGPTModelsToOpenAIList(body)
	if err != nil {
		t.Fatalf("MapUpstreamChatGPTModelsToOpenAIList() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if got := models[0]["id"]; got != "gpt-5.5" {
		t.Fatalf("models[0].id = %v, want %q", got, "gpt-5.5")
	}
	if got := models[1]["id"]; got != "gpt-5.4" {
		t.Fatalf("models[1].id = %v, want %q", got, "gpt-5.4")
	}
}

func TestMapUpstreamChatGPTModelsToOpenAIList_DeDuplicatesMappedGPT55(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5-5-thinking"},{"slug":"gpt-5.5"}]}`)

	models, err := MapUpstreamChatGPTModelsToOpenAIList(body)
	if err != nil {
		t.Fatalf("MapUpstreamChatGPTModelsToOpenAIList() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if got := models[0]["id"]; got != "gpt-5.5" {
		t.Fatalf("models[0].id = %v, want %q", got, "gpt-5.5")
	}
}

func TestSelectCodexModelsAuthSkipsQuotaCooldownMetadataAfterLoad(t *testing.T) {
	now := time.Now().UTC()
	resetAt := now.Add(time.Hour).Truncate(time.Second)
	store := &modelsAuthStore{
		items: []*coreauth.Auth{
			{
				ID:        "newer-exhausted",
				Provider:  "codex",
				Status:    coreauth.StatusActive,
				UpdatedAt: now,
				Metadata: map[string]any{
					"quota": map[string]any{
						"5hrs":  map[string]any{"remaining_percent": 0, "reset_at": resetAt.Format(time.RFC3339)},
						"7days": map[string]any{"remaining_percent": 80},
					},
				},
			},
			{
				ID:        "older-ready",
				Provider:  "codex",
				Status:    coreauth.StatusActive,
				UpdatedAt: now.Add(-time.Hour),
				Metadata: map[string]any{
					"quota": map[string]any{
						"5hrs":  map[string]any{"remaining_percent": 50},
						"7days": map[string]any{"remaining_percent": 80},
					},
				},
			},
		},
	}
	manager := coreauth.NewManager(store, nil, nil)
	manager.RegisterExecutor(modelsAuthExecutor{})
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	handler := &BaseAPIHandler{AuthManager: manager}

	got, _, err := handler.selectCodexModelsAuth()
	if err != nil {
		t.Fatalf("selectCodexModelsAuth() error = %v", err)
	}
	if got.ID != "older-ready" {
		t.Fatalf("selectCodexModelsAuth() auth = %q, want older-ready", got.ID)
	}
}

type modelsAuthStore struct {
	items []*coreauth.Auth
}

func (s *modelsAuthStore) List(context.Context) ([]*coreauth.Auth, error) {
	return s.items, nil
}

func (s *modelsAuthStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}

func (s *modelsAuthStore) Delete(context.Context, string) error {
	return nil
}

type modelsAuthExecutor struct{}

func (modelsAuthExecutor) Identifier() string { return "codex" }

func (modelsAuthExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (modelsAuthExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (modelsAuthExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (modelsAuthExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (modelsAuthExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
