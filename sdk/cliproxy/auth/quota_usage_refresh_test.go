package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestRefreshQuotaUsageOnceUpdatesOAuthProviders(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	exec := &quotaUsageRefreshExecutor{}
	for _, provider := range []string{"codex", "antigravity", "claude"} {
		manager.RegisterExecutor(&quotaUsageProviderExecutor{
			provider: provider,
			inner:    exec,
		})
	}

	for _, provider := range []string{"codex", "antigravity", "claude", "gemini"} {
		_, err := manager.Register(context.Background(), &Auth{
			ID:       provider + "-auth",
			Provider: provider,
			Metadata: map[string]any{"access_token": provider + "-token"},
		})
		if err != nil {
			t.Fatalf("Register(%s) error = %v", provider, err)
		}
	}

	manager.refreshQuotaUsageOnce(context.Background(), time.Now())

	for _, provider := range []string{"codex", "antigravity", "claude"} {
		auth, ok := manager.GetByID(provider + "-auth")
		if !ok {
			t.Fatalf("updated auth %q not found", provider)
		}
		if got := auth.Metadata["quota_usage_refreshed_by"]; got != provider {
			t.Fatalf("%s quota refresh marker = %v, want %s", provider, got, provider)
		}
	}
	gemini, ok := manager.GetByID("gemini-auth")
	if !ok {
		t.Fatal("gemini auth not found")
	}
	if _, refreshed := gemini.Metadata["quota_usage_refreshed_by"]; refreshed {
		t.Fatal("gemini auth should not be quota-refreshed by OAuth quota loop")
	}
	if got := exec.calls(); got != 3 {
		t.Fatalf("quota refresh calls = %d, want 3", got)
	}
}

func TestNextQuotaUsageRefreshDueUsesFiveMinuteInterval(t *testing.T) {
	now := time.Date(2026, 4, 30, 1, 0, 0, 0, time.UTC)
	auth := &Auth{Provider: "codex"}
	if !quotaUsageRefreshDue(auth, now) {
		t.Fatal("auth with no previous quota refresh should be due")
	}
	auth.Metadata = map[string]any{"quota_usage_refreshed_at": now.Add(-4 * time.Minute).Format(time.RFC3339)}
	if quotaUsageRefreshDue(auth, now) {
		t.Fatal("auth refreshed 4 minutes ago should not be due")
	}
	auth.Metadata["quota_usage_refreshed_at"] = now.Add(-5 * time.Minute).Format(time.RFC3339)
	if !quotaUsageRefreshDue(auth, now) {
		t.Fatal("auth refreshed 5 minutes ago should be due")
	}
}

type quotaUsageRefreshExecutor struct {
	mu    sync.Mutex
	count int
}

type quotaUsageProviderExecutor struct {
	provider string
	inner    *quotaUsageRefreshExecutor
}

func (e *quotaUsageProviderExecutor) Identifier() string { return e.provider }

func (e *quotaUsageProviderExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *quotaUsageProviderExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *quotaUsageProviderExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (e *quotaUsageProviderExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *quotaUsageProviderExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *quotaUsageProviderExecutor) RefreshQuotaUsage(ctx context.Context, auth *Auth) (*Auth, error) {
	return e.inner.RefreshQuotaUsage(ctx, auth)
}

func (e *quotaUsageRefreshExecutor) RefreshQuotaUsage(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.count++
	e.mu.Unlock()
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	updated.Metadata["quota_usage_refreshed_by"] = updated.Provider
	return updated, nil
}

func (e *quotaUsageRefreshExecutor) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}
