package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type quotaUsageRoundTripper func(*http.Request) (*http.Response, error)

func (f quotaUsageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodexRefreshQuotaUsageCallsWhamUsage(t *testing.T) {
	exec := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "codex-token",
			"account_id":   "acct-1",
		},
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", quotaUsageRoundTripper(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != codexQuotaUsageURL {
			t.Fatalf("url = %s, want %s", got, codexQuotaUsageURL)
		}
		if got := req.Method; got != http.MethodGet {
			t.Fatalf("method = %s, want GET", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer codex-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-1" {
			t.Fatalf("Chatgpt-Account-Id = %q", got)
		}
		return quotaUsageJSONResponse(`{"limit":100,"used":25}`), nil
	}))

	updated, err := exec.RefreshQuotaUsage(ctx, auth)
	if err != nil {
		t.Fatalf("RefreshQuotaUsage error = %v", err)
	}
	if got := updated.Metadata["quota_usage_provider"]; got != "codex" {
		t.Fatalf("quota_usage_provider = %v, want codex", got)
	}
	usage, ok := updated.Metadata["quota_usage"].(map[string]any)
	if !ok {
		t.Fatalf("quota_usage = %#v, want object", updated.Metadata["quota_usage"])
	}
	if got := usage["limit"]; got != float64(100) {
		t.Fatalf("quota_usage.limit = %v, want 100", got)
	}
}

func TestClaudeRefreshQuotaUsageCallsOAuthUsage(t *testing.T) {
	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "claude-token"},
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", quotaUsageRoundTripper(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != claudeQuotaUsageURL {
			t.Fatalf("url = %s, want %s", got, claudeQuotaUsageURL)
		}
		if got := req.Method; got != http.MethodPost {
			t.Fatalf("method = %s, want POST", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer claude-token" {
			t.Fatalf("authorization = %q", got)
		}
		return quotaUsageJSONResponse(`{"usage":{"input_tokens":10}}`), nil
	}))

	updated, err := exec.RefreshQuotaUsage(ctx, auth)
	if err != nil {
		t.Fatalf("RefreshQuotaUsage error = %v", err)
	}
	if got := updated.Metadata["quota_usage_provider"]; got != "claude" {
		t.Fatalf("quota_usage_provider = %v, want claude", got)
	}
}

func TestAntigravityRefreshQuotaUsageCallsLoadCodeAssist(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "ag-auth",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "ag-token"},
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", quotaUsageRoundTripper(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "fetchAvailableModels") {
			return quotaUsageJSONResponse(`{"models":{}}`), nil
		}
		if got := req.URL.String(); got != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("url = %s, want loadCodeAssist", got)
		}
		if got := req.Method; got != http.MethodPost {
			t.Fatalf("method = %s, want POST", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer ag-token" {
			t.Fatalf("authorization = %q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !strings.Contains(string(body), `"ide_name":"antigravity"`) {
			t.Fatalf("loadCodeAssist body = %s", string(body))
		}
		return quotaUsageJSONResponse(`{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"25000","minimumCreditAmountForUsage":"50"}]}}`), nil
	}))

	updated, err := exec.RefreshQuotaUsage(ctx, auth)
	if err != nil {
		t.Fatalf("RefreshQuotaUsage error = %v", err)
	}
	if got := updated.Metadata["quota_usage_provider"]; got != "antigravity" {
		t.Fatalf("quota_usage_provider = %v, want antigravity", got)
	}
	if got := updated.Metadata["antigravity_paid_tier_id"]; got != "tier-1" {
		t.Fatalf("antigravity_paid_tier_id = %v, want tier-1", got)
	}
	if got := updated.Metadata["antigravity_credits_available"]; got != true {
		t.Fatalf("antigravity_credits_available = %v, want true", got)
	}
}

func quotaUsageJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
