package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// invalidGrantRefreshTestExecutor always fails refresh with the AWS SSO OIDC
// "invalid_grant" 400 response that Kiro emits when a refresh token has been
// consumed (rotated by a sibling process) or revoked.
type invalidGrantRefreshTestExecutor struct {
	schedulerProviderTestExecutor
	// usedRefreshTokens records the refresh_token value seen on each Refresh call
	// so a test can assert which credential was actually presented upstream.
	usedRefreshTokens []string
}

func (e *invalidGrantRefreshTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	rt, _ := auth.Metadata["refresh_token"].(string)
	e.usedRefreshTokens = append(e.usedRefreshTokens, rt)
	return nil, errors.New(`kiro: refresh failed (status 400): {"error":"invalid_grant","error_description":"Resource not found"}`)
}

// rotatingRefreshTestExecutor succeeds only when presented with the freshly
// rotated refresh token that a sibling process persisted to the shared store;
// any other (stale) token fails with invalid_grant. This mirrors the real
// single-use rotating IDC refresh token contract.
type rotatingRefreshTestExecutor struct {
	schedulerProviderTestExecutor
	validRefreshToken string
}

func (e *rotatingRefreshTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	rt, _ := auth.Metadata["refresh_token"].(string)
	if rt != e.validRefreshToken {
		return nil, errors.New(`kiro: refresh failed (status 400): {"error":"invalid_grant","error_description":"Resource not found"}`)
	}
	refreshed := auth.Clone()
	refreshed.Metadata["access_token"] = "fresh-access-token"
	refreshed.Metadata["refresh_token"] = e.validRefreshToken
	return refreshed, nil
}

// staticRefreshStore is a minimal Store whose List returns a fixed snapshot,
// letting a test simulate a sibling process having rotated the refresh token in
// the shared (DB-backed) store after the in-memory copy was cloned.
type staticRefreshStore struct {
	auths []*Auth
}

func (s *staticRefreshStore) List(context.Context) ([]*Auth, error) {
	out := make([]*Auth, 0, len(s.auths))
	for _, a := range s.auths {
		out = append(out, a.Clone())
	}
	return out, nil
}

func (s *staticRefreshStore) Save(context.Context, *Auth) (string, error) { return "", nil }
func (s *staticRefreshStore) Delete(context.Context, string) error        { return nil }

// TestManager_RefreshInvalidGrantStopsRetryWhenTokenUnchanged verifies that an
// invalid_grant (HTTP 400) refresh failure is treated as a hard unauthorized
// failure when the store holds the same refresh token we just tried (the token
// is genuinely revoked, not rotated). Previously this 400 escaped the 401-only
// unauthorized classifier and the scheduler retried the dead token every 5
// minutes forever.
func TestManager_RefreshInvalidGrantStopsRetryWhenTokenUnchanged(t *testing.T) {
	ctx := context.Background()
	exec := &invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "kiro"},
	}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "kiro-invalid-grant",
		Provider: "kiro",
		Metadata: map[string]any{
			"type":          "kiro",
			"auth_method":   "idc",
			"refresh_token": "stale-token",
			"client_id":     "cid",
			"client_secret": "secret",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected invalid_grant refresh failure to be recorded")
	}
	if got := updated.LastError.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("LastError.StatusCode() = %d, want %d", got, http.StatusUnauthorized)
	}
	now := time.Now()
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected invalid_grant auth to stop refresh attempts")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected invalid_grant auth to be removed from the auto-refresh schedule")
	}
}

// TestManager_RefreshInvalidGrantRecoversFromRotatedStoreToken verifies that
// when a sibling process has rotated the refresh token in the shared store, an
// invalid_grant failure on the stale in-memory token triggers a reload from the
// store and a successful retry with the rotated token — the auth recovers
// instead of being marked unauthorized.
func TestManager_RefreshInvalidGrantRecoversFromRotatedStoreToken(t *testing.T) {
	ctx := context.Background()
	exec := &rotatingRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "kiro"},
		validRefreshToken:             "rotated-token",
	}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	// The store (DB authority) already holds the rotated token a sibling wrote.
	stored := &Auth{
		ID:       "kiro-rotated",
		Provider: "kiro",
		Metadata: map[string]any{
			"type":          "kiro",
			"auth_method":   "idc",
			"refresh_token": "rotated-token",
			"client_id":     "cid",
			"client_secret": "secret",
		},
	}
	manager.SetStore(&staticRefreshStore{auths: []*Auth{stored}})

	// The in-memory copy still has the stale token that was already consumed.
	auth := stored.Clone()
	auth.Metadata["refresh_token"] = "stale-token"
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError != nil {
		t.Fatalf("expected recovery, got LastError = %+v", updated.LastError)
	}
	if got, _ := updated.Metadata["access_token"].(string); got != "fresh-access-token" {
		t.Fatalf("access_token = %q, want fresh-access-token (recovered via rotated store token)", got)
	}
	if updated.Metadata["refresh_token"] != "rotated-token" {
		t.Fatalf("refresh_token = %v, want rotated-token persisted after recovery", updated.Metadata["refresh_token"])
	}
}

// TestManager_RefreshInvalidGrantDoesNotDisableOnTransientContention verifies
// that when the store holds a DIFFERENT refresh token (a sibling rotated it) but
// the retry with that token also fails — e.g. the sibling rotated again
// mid-flight — the auth is NOT permanently escalated to unauthorized. A token
// that is being actively rotated may still be valid, so the manager must fall
// back to a timed failure backoff and stay on the refresh schedule.
func TestManager_RefreshInvalidGrantDoesNotDisableOnTransientContention(t *testing.T) {
	ctx := context.Background()
	// This executor fails invalid_grant for every token, simulating a token that
	// keeps getting rotated out from under us by a sibling process.
	exec := &invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "kiro"},
	}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	// The store holds a token different from the in-memory one (sibling rotated).
	stored := &Auth{
		ID:       "kiro-contention",
		Provider: "kiro",
		Metadata: map[string]any{
			"type":          "kiro",
			"auth_method":   "idc",
			"refresh_token": "store-token",
			"client_id":     "cid",
			"client_secret": "secret",
		},
	}
	manager.SetStore(&staticRefreshStore{auths: []*Auth{stored}})

	auth := stored.Clone()
	auth.Metadata["refresh_token"] = "in-memory-token"
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	// Must NOT be escalated to unauthorized: the differing store token means the
	// credential might still be valid, so this is a transient failure, not a dead
	// token. It must remain schedulable on a backoff.
	if updated.LastError != nil && updated.LastError.Code == "unauthorized" {
		t.Fatal("expected transient contention to avoid unauthorized escalation")
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected a non-zero failure backoff for transient contention")
	}
	now := time.Now()
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); !shouldSchedule {
		t.Fatal("expected transiently-failing auth to remain on the auto-refresh schedule")
	}
	// The store token differs from the in-memory token, so the recovery retry must
	// have been attempted (two Refresh calls: original + retry).
	if len(exec.usedRefreshTokens) != 2 {
		t.Fatalf("Refresh attempts = %d, want 2 (original + rotated-token retry)", len(exec.usedRefreshTokens))
	}
}
