package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeStore struct {
	auths []*cliproxyauth.Auth
	saved []*cliproxyauth.Auth
}

func (f *fakeStore) List(context.Context) ([]*cliproxyauth.Auth, error) {
	return f.auths, nil
}

func (f *fakeStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	f.saved = append(f.saved, auth.Clone())
	return auth.ID, nil
}

func (f *fakeStore) Delete(context.Context, string) error {
	return nil
}

type fakeRefreshExecutor struct {
	updated map[string]*cliproxyauth.Auth
	errs    map[string]error
}

func (f fakeRefreshExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if err := f.errs[auth.ID]; err != nil {
		return nil, err
	}
	if updated := f.updated[auth.ID]; updated != nil {
		return updated.Clone(), nil
	}
	return auth.Clone(), nil
}

// fakeJWT builds a syntactically valid JWT payload with the supplied exp claim for parser-based tests.
func fakeJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(map[string]any{"exp": exp, "iat": exp - 3600})
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".signature"
}

// newCodexAuth creates a minimal auth record for cron refresh tests.
func newCodexAuth(id string, expiry time.Time, refreshToken string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  fakeJWT(expiry.Unix()),
			"refresh_token": refreshToken,
		},
	}
}

// TestAccessTokenExpiry verifies the command decodes JWT exp from access_token before falling back to metadata timestamps.
func TestAccessTokenExpiry(t *testing.T) {
	expiry := time.Date(2026, 5, 24, 17, 0, 0, 0, time.UTC)
	auth := newCodexAuth("codex-1", expiry, "refresh-1")

	got, ok := accessTokenExpiry(auth)
	if !ok {
		t.Fatal("accessTokenExpiry() reported no expiry")
	}
	if !got.Equal(expiry) {
		t.Fatalf("accessTokenExpiry() = %v, want %v", got, expiry)
	}
}

// TestExpiresWithinWindow locks the cron trigger rule to the next 48 hours by default.
func TestExpiresWithinWindow(t *testing.T) {
	now := time.Date(2026, 5, 24, 9, 30, 0, 0, time.UTC)
	window := 48 * time.Hour

	if !expiresWithinWindow(now, now.Add(47*time.Hour), window) {
		t.Fatal("expected token expiring within 48 hours to be due")
	}
	if !expiresWithinWindow(now, now.Add(-1*time.Hour), window) {
		t.Fatal("expected already expired token to be due")
	}
	if expiresWithinWindow(now, now.Add(49*time.Hour), window) {
		t.Fatal("expected token expiring after 48 hours to be skipped")
	}
}

// TestRefreshDueCodexAuths verifies the cron command refreshes and saves only due Codex auths with refresh tokens.
func TestRefreshDueCodexAuths(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, 5, 24, 9, 0, 0, 0, location)

	due := newCodexAuth("due", time.Date(2026, 5, 24, 18, 0, 0, 0, location), "refresh-due")
	future := newCodexAuth("future", time.Date(2026, 5, 26, 18, 0, 0, 0, location), "refresh-future")
	noRefresh := newCodexAuth("no-refresh", time.Date(2026, 5, 24, 18, 0, 0, 0, location), "")
	other := &cliproxyauth.Auth{ID: "gemini", Provider: "gemini", Metadata: map[string]any{"access_token": fakeJWT(now.Add(2 * time.Hour).Unix())}}

	updatedDue := due.Clone()
	updatedDue.Metadata["access_token"] = fakeJWT(now.Add(10 * 24 * time.Hour).Unix())
	updatedDue.Metadata["refresh_token"] = "refresh-new"
	updatedDue.Metadata["last_refresh"] = now.Format(time.RFC3339)

	store := &fakeStore{auths: []*cliproxyauth.Auth{due, future, noRefresh, other}}
	exec := fakeRefreshExecutor{
		updated: map[string]*cliproxyauth.Auth{"due": updatedDue},
		errs:    map[string]error{},
	}

	stats, err := refreshDueCodexAuths(context.Background(), store, exec, now, false, refreshOptions{})
	if err != nil {
		t.Fatalf("refreshDueCodexAuths() error = %v", err)
	}
	if stats.Scanned != 4 {
		t.Fatalf("Scanned = %d, want 4", stats.Scanned)
	}
	if stats.Due != 2 {
		t.Fatalf("Due = %d, want 2", stats.Due)
	}
	if stats.Refreshed != 1 {
		t.Fatalf("Refreshed = %d, want 1", stats.Refreshed)
	}
	if stats.SkippedFutureExpiry != 1 {
		t.Fatalf("SkippedFutureExpiry = %d, want 1", stats.SkippedFutureExpiry)
	}
	if stats.SkippedNoRefresh != 1 {
		t.Fatalf("SkippedNoRefresh = %d, want 1", stats.SkippedNoRefresh)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved auth count = %d, want 1", len(store.saved))
	}
	if got := store.saved[0].Metadata["refresh_token"]; got != "refresh-new" {
		t.Fatalf("saved refresh_token = %v, want refresh-new", got)
	}
}

// TestRefreshDueCodexAuthsDryRun ensures cron preview mode never mutates persistence while still reporting due auths.
func TestRefreshDueCodexAuthsDryRun(t *testing.T) {
	now := time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC)
	due := newCodexAuth("due", now.Add(6*time.Hour), "refresh-due")
	store := &fakeStore{auths: []*cliproxyauth.Auth{due}}

	stats, err := refreshDueCodexAuths(context.Background(), store, fakeRefreshExecutor{}, now, true, refreshOptions{})
	if err != nil {
		t.Fatalf("refreshDueCodexAuths() error = %v", err)
	}
	if stats.Due != 1 || stats.Refreshed != 0 {
		t.Fatalf("dry run stats = %+v, want due=1 refreshed=0", stats)
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved auth count = %d, want 0", len(store.saved))
	}
	if len(stats.RefreshedIDs) != 1 || stats.RefreshedIDs[0] != "due" {
		t.Fatalf("dry run refreshed IDs = %v, want [due]", stats.RefreshedIDs)
	}
}

// TestLoadFileResourcesRequiresConfig keeps the command failure mode explicit when cron runs outside a configured workspace.
func TestLoadFileResourcesRequiresConfig(t *testing.T) {
	_, err := loadFileResources(t.TempDir(), "")
	if err == nil {
		t.Fatal("expected loadFileResources() to fail without config.yaml")
	}
	if !strings.Contains(err.Error(), "load config failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMetadataString trims values because auth metadata is sourced from loosely normalized JSON files.
func TestMetadataString(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"refresh_token": "  abc  "}}
	if got := metadataString(auth, "refresh_token"); got != "abc" {
		t.Fatalf("metadataString() = %q, want abc", got)
	}
}

// TestFakeJWT ensures the helper emits a three-part token suitable for ParseJWTToken-based tests.
func TestFakeJWT(t *testing.T) {
	token := fakeJWT(1234567890)
	if parts := strings.Count(token, "."); parts != 2 {
		t.Fatalf("fakeJWT() produced %q with %d dots, want 2", token, parts)
	}
	if !strings.Contains(token, ".") {
		t.Fatalf("fakeJWT() = %q, want jwt-like token", token)
	}
	if _, ok := accessTokenExpiry(&cliproxyauth.Auth{Metadata: map[string]any{"access_token": token}}); !ok {
		t.Fatalf("fakeJWT() token should parse via accessTokenExpiry()")
	}
}

// TestRefreshDueCodexAuthsRefreshFailurePreservesAccounting locks the failed counter for transient refresh errors.
func TestRefreshDueCodexAuthsRefreshFailurePreservesAccounting(t *testing.T) {
	now := time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC)
	due := newCodexAuth("due", now.Add(2*time.Hour), "refresh-due")
	store := &fakeStore{auths: []*cliproxyauth.Auth{due}}

	stats, err := refreshDueCodexAuths(
		context.Background(),
		store,
		fakeRefreshExecutor{errs: map[string]error{"due": fmt.Errorf("boom")}},
		now,
		false,
		refreshOptions{},
	)
	if err != nil {
		t.Fatalf("refreshDueCodexAuths() error = %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", stats.Failed)
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved auth count = %d, want 0", len(store.saved))
	}
}

// TestRefreshDueCodexAuthsForceAndFilter lets operators target one auth for live verification without waiting for the date window.
func TestRefreshDueCodexAuthsForceAndFilter(t *testing.T) {
	now := time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC)
	futureA := newCodexAuth("future-a", now.Add(48*time.Hour), "refresh-a")
	futureB := newCodexAuth("future-b", now.Add(72*time.Hour), "refresh-b")
	store := &fakeStore{auths: []*cliproxyauth.Auth{futureA, futureB}}

	updated := futureB.Clone()
	updated.Metadata["access_token"] = fakeJWT(now.Add(10 * 24 * time.Hour).Unix())
	exec := fakeRefreshExecutor{updated: map[string]*cliproxyauth.Auth{"future-b": updated}}

	stats, err := refreshDueCodexAuths(
		context.Background(),
		store,
		exec,
		now,
		false,
		refreshOptions{
			AuthIDs:       map[string]struct{}{"future-b": {}},
			Force:         true,
			RefreshWindow: 48 * time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("refreshDueCodexAuths() error = %v", err)
	}
	if stats.Due != 1 {
		t.Fatalf("Due = %d, want 1", stats.Due)
	}
	if stats.Refreshed != 1 {
		t.Fatalf("Refreshed = %d, want 1", stats.Refreshed)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved auth count = %d, want 1", len(store.saved))
	}
	if store.saved[0].ID != "future-b" {
		t.Fatalf("saved auth ID = %s, want future-b", store.saved[0].ID)
	}
}
