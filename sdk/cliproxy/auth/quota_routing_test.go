package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type quotaRoutingExecutor struct {
	provider string

	mu             sync.Mutex
	selected       []string
	refreshes      map[string]int
	refreshedQuota map[string]float64
}

func newQuotaRoutingExecutor(provider string) *quotaRoutingExecutor {
	return &quotaRoutingExecutor{
		provider:       provider,
		refreshes:      make(map[string]int),
		refreshedQuota: make(map[string]float64),
	}
}

func (e *quotaRoutingExecutor) Identifier() string { return e.provider }

func (e *quotaRoutingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.selected = append(e.selected, auth.ID)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *quotaRoutingExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.selected = append(e.selected, auth.ID)
	e.mu.Unlock()
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"ok":true}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *quotaRoutingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *quotaRoutingExecutor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *quotaRoutingExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *quotaRoutingExecutor) RefreshQuotaUsage(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshes[auth.ID]++
	quota, hasQuota := e.refreshedQuota[auth.ID]
	e.mu.Unlock()

	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	if hasQuota {
		updated.Metadata["quota"] = quotaMetadata(quota, quota)
	}
	return updated, nil
}

func (e *quotaRoutingExecutor) selectedIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.selected...)
}

func (e *quotaRoutingExecutor) refreshCount(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refreshes[authID]
}

func quotaMetadata(fiveHours, sevenDays float64) map[string]any {
	return map[string]any{
		"5hrs":  map[string]any{"remaining_percent": fiveHours},
		"7days": map[string]any{"remaining_percent": sevenDays},
	}
}

func TestManagerPickNext_RefreshesStaleCodexQuotaBeforeSelection(t *testing.T) {
	model := "gpt-stale-quota-refresh-test"
	registerSchedulerModels(t, "codex", model, "stale-rich-after-refresh", "fresh-before-refresh")

	now := time.Now().UTC()
	exec := newQuotaRoutingExecutor("codex")
	exec.refreshedQuota["stale-rich-after-refresh"] = 100

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = exec
	for _, auth := range []*Auth{
		{
			ID:       "stale-rich-after-refresh",
			Provider: "codex",
			Metadata: map[string]any{
				"quota":                    quotaMetadata(10, 10),
				"quota_usage_refreshed_at": now.Add(-16 * time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID:       "fresh-before-refresh",
			Provider: "codex",
			Metadata: map[string]any{
				"quota":                    quotaMetadata(80, 80),
				"quota_usage_refreshed_at": now.Format(time.RFC3339),
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}

	got, _, errPick := manager.pickNext(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickNext() auth = nil")
	}
	if got.ID != "stale-rich-after-refresh" {
		t.Fatalf("pickNext() auth.ID = %q, want stale auth after blocking quota refresh", got.ID)
	}
	if refreshes := exec.refreshCount("stale-rich-after-refresh"); refreshes != 1 {
		t.Fatalf("stale auth refresh count = %d, want 1", refreshes)
	}
}

func TestSchedulerPick_CodexRichestQuotaTieUsesRandomSelection(t *testing.T) {
	model := "gpt-quota-random-tie-test"
	registerSchedulerModels(t, "codex", model, "tie-a", "tie-b")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "tie-a", Provider: "codex", Metadata: map[string]any{"quota": quotaMetadata(100, 100)}},
		&Auth{ID: "tie-b", Provider: "codex", Metadata: map[string]any{"quota": quotaMetadata(100, 100)}},
	)

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		got, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", i+1, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", i+1)
		}
		seen[got.ID] = true
	}
	if !seen["tie-a"] || !seen["tie-b"] {
		t.Fatalf("random tie selection seen = %#v, want both richest auths", seen)
	}
}

func TestManagerExecute_RotatesSessionAfterTwentyTurnsAndRefreshesAuth(t *testing.T) {
	model := "gpt-session-turn-refresh-test"
	registerSchedulerModels(t, "codex", model, "session-rich", "session-next")

	exec := newQuotaRoutingExecutor("codex")
	manager := NewManager(nil, NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	}), nil)
	manager.executors["codex"] = exec
	now := time.Now().UTC()
	for _, auth := range []*Auth{
		{
			ID:       "session-rich",
			Provider: "codex",
			Metadata: map[string]any{
				"quota":                    quotaMetadata(90, 90),
				"quota_usage_refreshed_at": now.Format(time.RFC3339),
			},
		},
		{
			ID:       "session-next",
			Provider: "codex",
			Metadata: map[string]any{
				"quota":                    quotaMetadata(80, 80),
				"quota_usage_refreshed_at": now.Format(time.RFC3339),
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{"Session_id": []string{"turn-session"}},
	}
	req := cliproxyexecutor.Request{Model: model, Metadata: map[string]any{}}
	for i := 0; i < 20; i++ {
		if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
			t.Fatalf("Execute() turn %d error = %v", i+1, err)
		}
	}
	if refreshes := exec.refreshCount("session-rich"); refreshes != 1 {
		t.Fatalf("session-rich refresh count after 20 turns = %d, want 1", refreshes)
	}

	if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("Execute() turn 21 error = %v", err)
	}
	selected := exec.selectedIDs()
	if len(selected) != 21 {
		t.Fatalf("selected count = %d, want 21", len(selected))
	}
	for i := 0; i < 20; i++ {
		if selected[i] != "session-rich" {
			t.Fatalf("turn %d selected %q, want session-rich", i+1, selected[i])
		}
	}
	if selected[20] != "session-next" {
		t.Fatalf("turn 21 selected %q, want session-next", selected[20])
	}
}

func TestManagerCloseExecutionSession_RefreshesShortSessionAuth(t *testing.T) {
	model := "gpt-close-session-refresh-test"
	registerSchedulerModels(t, "codex", model, "short-session-auth")

	exec := newQuotaRoutingExecutor("codex")
	manager := NewManager(nil, NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	}), nil)
	manager.executors["codex"] = exec
	now := time.Now().UTC()
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "short-session-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"quota":                    quotaMetadata(90, 90),
			"quota_usage_refreshed_at": now.Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{"Session_id": []string{"short-session"}},
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "exec-short-session",
		},
	}
	req := cliproxyexecutor.Request{Model: model, Metadata: map[string]any{}}
	if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	manager.CloseExecutionSession("exec-short-session")

	if refreshes := exec.refreshCount("short-session-auth"); refreshes != 1 {
		t.Fatalf("short-session-auth refresh count on session close = %d, want 1", refreshes)
	}
}

func TestManagerExecuteStream_RefreshesShortSSESessionAuthOnStreamEnd(t *testing.T) {
	model := "gpt-stream-end-refresh-test"
	registerSchedulerModels(t, "codex", model, "stream-session-auth")

	exec := newQuotaRoutingExecutor("codex")
	manager := NewManager(nil, NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	}), nil)
	manager.executors["codex"] = exec
	now := time.Now().UTC()
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "stream-session-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"quota":                    quotaMetadata(90, 90),
			"quota_usage_refreshed_at": now.Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{"Session_id": []string{"stream-session"}},
	}
	req := cliproxyexecutor.Request{Model: model, Metadata: map[string]any{}}
	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range result.Chunks {
	}

	if refreshes := exec.refreshCount("stream-session-auth"); refreshes != 1 {
		t.Fatalf("stream-session-auth refresh count on stream end = %d, want 1", refreshes)
	}
}
