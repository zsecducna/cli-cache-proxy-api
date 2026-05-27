package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const tokenInvalidatedTestErrorBody = `{"error":{"message":"Your authentication token has been invalidated. Please try signing in again.","type":"invalid_request_error","code":"token_invalidated","param":null},"status":401}`

// tokenInvalidatedDeleteStore records delete calls so tests can prove purge behavior
// without depending on a concrete backing store implementation.
type tokenInvalidatedDeleteStore struct {
	saveCount   atomic.Int32
	deleteCount atomic.Int32

	mu         sync.Mutex
	deletedIDs []string
	deleteErr  error
}

// List satisfies the auth.Store interface for these focused manager tests.
func (s *tokenInvalidatedDeleteStore) List(context.Context) ([]*Auth, error) { return nil, nil }

// Save satisfies the auth.Store interface and lets tests assert purge paths do
// not accidentally persist stale auth state.
func (s *tokenInvalidatedDeleteStore) Save(_ context.Context, _ *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

// Delete records the deleted auth identifier so tests can verify the exact auth
// was purged through the store abstraction.
func (s *tokenInvalidatedDeleteStore) Delete(_ context.Context, id string) error {
	s.deleteCount.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

// deletedSnapshot returns a stable copy of deleted auth IDs for assertions.
func (s *tokenInvalidatedDeleteStore) deletedSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.deletedIDs))
	copy(out, s.deletedIDs)
	return out
}

// tokenInvalidatedTestExecutor injects exact upstream failures into the manager
// so tests can validate purge behavior on both execute and quota-refresh paths.
type tokenInvalidatedTestExecutor struct {
	provider   string
	executeErr error
	refreshErr error
}

// Identifier returns the provider key registered with the manager.
func (e *tokenInvalidatedTestExecutor) Identifier() string { return e.provider }

// Execute returns the configured request error so manager retry/purge behavior
// can be tested without involving real upstream traffic.
func (e *tokenInvalidatedTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.executeErr
}

// ExecuteStream is unused in these tests.
func (e *tokenInvalidatedTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

// Refresh is unused in these tests.
func (e *tokenInvalidatedTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

// CountTokens is unused in these tests.
func (e *tokenInvalidatedTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

// HttpRequest is unused in these tests.
func (e *tokenInvalidatedTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// RefreshQuotaUsage returns the configured quota-refresh error so tests can
// validate purge behavior on the background refresh path.
func (e *tokenInvalidatedTestExecutor) RefreshQuotaUsage(_ context.Context, _ *Auth) (*Auth, error) {
	return nil, e.refreshErr
}

// registerTokenInvalidatedTestAuth adds one Codex auth to the manager without
// persisting setup noise so each test can focus on the purge side effect.
func registerTokenInvalidatedTestAuth(t *testing.T, manager *Manager, authID string) {
	t.Helper()
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
}

// TestManagerRefreshQuotaUsageAuth_TokenInvalidatedPurgesAuth proves the exact
// upstream token_invalidated refresh failure removes the auth through the store.
func TestManagerRefreshQuotaUsageAuth_TokenInvalidatedPurgesAuth(t *testing.T) {
	store := &tokenInvalidatedDeleteStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&tokenInvalidatedTestExecutor{
		provider:   "codex",
		refreshErr: &Error{HTTPStatus: http.StatusUnauthorized, Message: tokenInvalidatedTestErrorBody},
	})

	registerTokenInvalidatedTestAuth(t, manager, "codex-refresh-token-invalidated")
	auth, ok := manager.GetByID("codex-refresh-token-invalidated")
	if !ok || auth == nil {
		t.Fatal("expected registered auth before refresh")
	}

	manager.refreshQuotaUsageAuth(context.Background(), auth, time.Now().UTC())

	if _, ok = manager.GetByID("codex-refresh-token-invalidated"); ok {
		t.Fatal("expected auth to be purged after token_invalidated quota refresh failure")
	}
	if got := store.deleteCount.Load(); got != 1 {
		t.Fatalf("delete count = %d, want 1", got)
	}
	if got := store.deletedSnapshot(); len(got) != 1 || got[0] != "codex-refresh-token-invalidated" {
		t.Fatalf("deleted ids = %#v, want [%q]", got, "codex-refresh-token-invalidated")
	}
}

// TestManagerExecute_TokenInvalidatedPurgesAuth proves normal request execution
// also purges the auth when the upstream body reports the exact token_invalidated code.
func TestManagerExecute_TokenInvalidatedPurgesAuth(t *testing.T) {
	const model = "gpt-token-invalidated-purge-test"

	store := &tokenInvalidatedDeleteStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&tokenInvalidatedTestExecutor{
		provider:   "codex",
		executeErr: &Error{HTTPStatus: http.StatusUnauthorized, Message: tokenInvalidatedTestErrorBody},
	})

	registerTokenInvalidatedTestAuth(t, manager, "codex-execute-token-invalidated")
	registry.GetGlobalRegistry().RegisterClient("codex-execute-token-invalidated", "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-execute-token-invalidated") })

	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error for token_invalidated auth")
	}

	if _, ok := manager.GetByID("codex-execute-token-invalidated"); ok {
		t.Fatal("expected auth to be purged after token_invalidated execute failure")
	}
	if got := store.deleteCount.Load(); got != 1 {
		t.Fatalf("delete count = %d, want 1", got)
	}
	if got := store.deletedSnapshot(); len(got) != 1 || got[0] != "codex-execute-token-invalidated" {
		t.Fatalf("deleted ids = %#v, want [%q]", got, "codex-execute-token-invalidated")
	}
}

// TestManagerExecute_TokenInvalidatedDeleteFailureLeavesAuthPresent proves the
// manager does not half-purge runtime state when the backing-store delete fails.
func TestManagerExecute_TokenInvalidatedDeleteFailureLeavesAuthPresent(t *testing.T) {
	const model = "gpt-token-invalidated-delete-failure-test"

	store := &tokenInvalidatedDeleteStore{deleteErr: context.DeadlineExceeded}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&tokenInvalidatedTestExecutor{
		provider:   "codex",
		executeErr: &Error{HTTPStatus: http.StatusUnauthorized, Message: tokenInvalidatedTestErrorBody},
	})

	registerTokenInvalidatedTestAuth(t, manager, "codex-execute-token-invalidated-delete-failure")
	registry.GetGlobalRegistry().RegisterClient("codex-execute-token-invalidated-delete-failure", "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("codex-execute-token-invalidated-delete-failure")
	})

	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error for token_invalidated auth")
	}

	if _, ok := manager.GetByID("codex-execute-token-invalidated-delete-failure"); !ok {
		t.Fatal("expected auth to remain when backing-store delete fails")
	}
	if got := store.deleteCount.Load(); got != 1 {
		t.Fatalf("delete count = %d, want 1", got)
	}
}
