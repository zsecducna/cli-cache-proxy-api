package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestKiroCreds_MetadataPrecedence verifies metadata values win over attributes and that
// all credential fields are read.
func TestKiroCreds_MetadataPrecedence(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token":  "meta-access",
			"refresh_token": "meta-refresh",
			"profile_arn":   "arn:aws:codewhisperer:us-west-2:1:profile/p",
			"client_id":     "cid",
			"client_secret": "secret",
			"region":        "us-west-2",
			"auth_method":   "idc",
		},
		Attributes: map[string]string{"access_token": "attr-access"},
	}
	creds := kiroCreds(auth)
	if creds.accessToken != "meta-access" {
		t.Fatalf("accessToken = %q, want meta-access (metadata precedence)", creds.accessToken)
	}
	if creds.refreshToken != "meta-refresh" || creds.clientID != "cid" || creds.clientSecret != "secret" || creds.authMethod != "idc" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
}

// TestKiroCreds_AttributesFallback verifies attributes are used when metadata is absent.
func TestKiroCreds_AttributesFallback(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"access_token": "attr-access"}}
	if got := kiroCreds(auth).accessToken; got != "attr-access" {
		t.Fatalf("accessToken = %q, want attr-access", got)
	}
}

// TestRegionForCreds covers profile-ARN region extraction and the fallback chain.
func TestRegionForCreds(t *testing.T) {
	if got := regionForCreds(kiroCredentials{profileArn: "arn:aws:codewhisperer:ap-south-1:1:profile/p"}); got != "ap-south-1" {
		t.Fatalf("region = %q, want ap-south-1 (from ARN)", got)
	}
	if got := regionForCreds(kiroCredentials{region: "eu-west-1"}); got != "eu-west-1" {
		t.Fatalf("region = %q, want eu-west-1 (from stored region)", got)
	}
	if got := regionForCreds(kiroCredentials{}); got != "us-east-1" {
		t.Fatalf("region = %q, want us-east-1 (default)", got)
	}
}

// TestKiroRetryAfter verifies the 429 cooldown derivation: non-429 yields no hint,
// 429 without an upstream hint is floored, and explicit hints above the floor win.
func TestKiroRetryAfter(t *testing.T) {
	// Non-429 must return nil so other statuses fall through to default handling.
	if got := kiroRetryAfter(http.StatusInternalServerError, nil, nil); got != nil {
		t.Fatalf("non-429 retryAfter = %v, want nil", got)
	}

	// 429 with no hint anywhere is raised to the floor.
	if got := kiroRetryAfter(http.StatusTooManyRequests, nil, []byte(`{"message":"Too many requests"}`)); got == nil || *got != kiroRateLimitCooldownFloor {
		t.Fatalf("hint-less 429 retryAfter = %v, want %v", got, kiroRateLimitCooldownFloor)
	}

	// Retry-After header in seconds, above the floor, wins.
	hdr := http.Header{}
	hdr.Set("Retry-After", "30")
	if got := kiroRetryAfter(http.StatusTooManyRequests, hdr, nil); got == nil || *got != 30*time.Second {
		t.Fatalf("Retry-After=30 retryAfter = %v, want 30s", got)
	}

	// A header hint below the floor is raised to the floor.
	hdrLow := http.Header{}
	hdrLow.Set("Retry-After", "1")
	if got := kiroRetryAfter(http.StatusTooManyRequests, hdrLow, nil); got == nil || *got != kiroRateLimitCooldownFloor {
		t.Fatalf("Retry-After=1 retryAfter = %v, want floor %v", got, kiroRateLimitCooldownFloor)
	}

	// Retry-After-Ms is honored when larger than the floor.
	hdrMs := http.Header{}
	hdrMs.Set("Retry-After-Ms", "12000")
	if got := kiroRetryAfter(http.StatusTooManyRequests, hdrMs, nil); got == nil || *got != 12*time.Second {
		t.Fatalf("Retry-After-Ms=12000 retryAfter = %v, want 12s", got)
	}

	// Body retryDelay (Google-style duration string) is honored when larger than the floor.
	if got := kiroRetryAfter(http.StatusTooManyRequests, nil, []byte(`{"retryDelay":"15s"}`)); got == nil || *got != 15*time.Second {
		t.Fatalf("body retryDelay=15s retryAfter = %v, want 15s", got)
	}

	// A far-future / bogus hint is clamped to the max so a credential cannot be suspended indefinitely.
	hdrHuge := http.Header{}
	hdrHuge.Set("Retry-After", "86400") // 24h
	if got := kiroRetryAfter(http.StatusTooManyRequests, hdrHuge, nil); got == nil || *got != kiroRateLimitCooldownMax {
		t.Fatalf("Retry-After=86400 retryAfter = %v, want clamp %v", got, kiroRateLimitCooldownMax)
	}
}
func TestRefresh_NoRefreshTokenIsNoop(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "a"}}
	got, err := e.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got != auth {
		t.Fatal("Refresh() should return the same auth when there is nothing to refresh")
	}
}

// TestShouldPrepareRequestAuth covers the request-time refresh gate that closes the
// token-rotation race. The proactive loop is not enough: a request that picked an auth
// clone before the loop rotated the token would send the now-dead token and get a 403
// "bearer token ... is invalid".
func TestShouldPrepareRequestAuth(t *testing.T) {
	e := NewKiroExecutor(&config.Config{})
	rfc := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	now := time.Now()

	cases := []struct {
		name string
		meta map[string]any
		want bool
	}{
		{
			name: "no refresh token: cannot refresh, send as-is",
			meta: map[string]any{"access_token": "a", "expired": rfc(now.Add(-time.Hour))},
			want: false,
		},
		{
			name: "missing access token: must mint before request",
			meta: map[string]any{"refresh_token": "r"},
			want: true,
		},
		{
			name: "already expired: refresh",
			meta: map[string]any{"access_token": "a", "refresh_token": "r", "expired": rfc(now.Add(-time.Minute))},
			want: true,
		},
		{
			name: "within 5m lead: refresh",
			meta: map[string]any{"access_token": "a", "refresh_token": "r", "expired": rfc(now.Add(2 * time.Minute))},
			want: true,
		},
		{
			name: "comfortably valid: do not refresh",
			meta: map[string]any{"access_token": "a", "refresh_token": "r", "expired": rfc(now.Add(time.Hour))},
			want: false,
		},
		{
			name: "valid token, no parseable expiry: do not refresh (loop owns it)",
			meta: map[string]any{"access_token": "a", "refresh_token": "r"},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Metadata: tc.meta}
			if got := e.ShouldPrepareRequestAuth(auth); got != tc.want {
				t.Fatalf("ShouldPrepareRequestAuth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestKiroExecutorImplementsRequestAuthPreparer guards the wiring: prepareRequestAuth in
// the conductor only refreshes at request time for executors implementing this interface.
// If the methods are renamed/removed, the rotation-race fix silently regresses.
func TestKiroExecutorImplementsRequestAuthPreparer(t *testing.T) {
	var _ cliproxyauth.RequestAuthPreparer = NewKiroExecutor(&config.Config{})
}
