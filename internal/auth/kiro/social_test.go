package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestGenerateSocialPKCE verifies the verifier/challenge pair satisfies RFC 7636 S256:
// the challenge must equal base64url(sha256(verifier)) and state must be non-empty.
func TestGenerateSocialPKCE(t *testing.T) {
	pkce, err := GenerateSocialPKCE()
	if err != nil {
		t.Fatalf("GenerateSocialPKCE error: %v", err)
	}
	if pkce.Verifier == "" || pkce.Challenge == "" || pkce.State == "" {
		t.Fatalf("empty PKCE field(s): %+v", pkce)
	}
	// Verifier must be URL-safe base64 (decodable) and within the RFC 7636 43..128 range.
	if l := len(pkce.Verifier); l < 43 || l > 128 {
		t.Fatalf("verifier length = %d, want 43..128", l)
	}
	if _, err = base64.RawURLEncoding.DecodeString(pkce.Verifier); err != nil {
		t.Fatalf("verifier is not valid base64url: %v", err)
	}
	sum := sha256.Sum256([]byte(pkce.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if pkce.Challenge != want {
		t.Fatalf("challenge mismatch:\n got %q\nwant %q", pkce.Challenge, want)
	}
	if pkce.State == pkce.Verifier {
		t.Fatal("state must be independent of verifier")
	}
}

// TestSocialSignInURL verifies the sign-in URL carries the PKCE challenge, S256 method,
// the fixed loopback redirect URI, and the Kiro IDE client tag.
func TestSocialSignInURL(t *testing.T) {
	pkce := &SocialPKCE{Verifier: "v", Challenge: "chal", State: "st8"}
	raw := SocialSignInURL(pkce)
	if !strings.HasPrefix(raw, SocialSignInBaseURL+"?") {
		t.Fatalf("sign-in URL base = %q, want prefix %q", raw, SocialSignInBaseURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("sign-in URL does not parse: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"state":                 "st8",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
		"redirect_uri":          SocialRedirectURI,
		"redirect_from":         SocialRedirectFrom,
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Fatalf("param %q = %q, want %q", k, got, want)
		}
	}
}

// TestExchangeSocialCode_Success confirms the exchange POSTs {code, code_verifier,
// redirect_uri} and maps the camelCase response onto KiroTokenData with a future expiry.
func TestExchangeSocialCode_Success(t *testing.T) {
	srv := newJSONServer(t, func(body map[string]any) (int, string) {
		if body["code"] != "the-code" {
			t.Errorf("code = %v, want the-code", body["code"])
		}
		if body["code_verifier"] != "the-verifier" {
			t.Errorf("code_verifier = %v, want the-verifier", body["code_verifier"])
		}
		if body["redirect_uri"] != SocialRedirectURI {
			t.Errorf("redirect_uri = %v, want %s", body["redirect_uri"], SocialRedirectURI)
		}
		return http.StatusOK, `{"accessToken":"acc","refreshToken":"ref","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/x","expiresIn":3600}`
	})
	defer srv.Close()

	k := NewKiroAuth(nil)
	k.socialTokenURL = srv.URL

	td, err := k.ExchangeSocialCode(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("ExchangeSocialCode error: %v", err)
	}
	if td.AccessToken != "acc" || td.RefreshToken != "ref" {
		t.Fatalf("token mismatch: %+v", td)
	}
	if td.ProfileArn != "arn:aws:codewhisperer:us-east-1:1:profile/x" {
		t.Fatalf("profileArn = %q", td.ProfileArn)
	}
	if td.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("ExpiresAt = %d, want a future unix timestamp", td.ExpiresAt)
	}
}

// TestExchangeSocialCode_Error verifies a non-2xx response and an empty access token are
// both surfaced as errors rather than persisted.
func TestExchangeSocialCode_Error(t *testing.T) {
	// Non-2xx.
	srv := newJSONServer(t, func(map[string]any) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	})
	k := NewKiroAuth(nil)
	k.socialTokenURL = srv.URL
	if _, err := k.ExchangeSocialCode(context.Background(), "c", "v"); err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	srv.Close()

	// 2xx but no access token.
	srv2 := newJSONServer(t, func(map[string]any) (int, string) {
		return http.StatusOK, `{"refreshToken":"r"}`
	})
	defer srv2.Close()
	k.socialTokenURL = srv2.URL
	if _, err := k.ExchangeSocialCode(context.Background(), "c", "v"); err == nil {
		t.Fatal("expected error when access token is empty")
	}

	// Empty code is rejected before any request.
	if _, err := k.ExchangeSocialCode(context.Background(), "  ", "v"); err == nil {
		t.Fatal("expected error for empty code")
	}
}

// TestStartSocialCallbackListener_StateGate is the security regression test for the
// loopback callback: a wrong-state or non-GET request must NOT resolve the one-shot
// (otherwise a local process could DoS the real login on the fixed port), while the
// state-matched redirect delivers the code.
func TestStartSocialCallbackListener_StateGate(t *testing.T) {
	resultCh, cleanup, err := StartSocialCallbackListener("good-state")
	if err != nil {
		// The port is fixed (3128); skip rather than fail if the environment cannot bind it.
		t.Skipf("cannot bind loopback callback port (skipping): %v", err)
	}
	defer cleanup()

	base := "http://127.0.0.1:" + SocialRedirectPort

	// Wrong state -> must be ignored (no delivery).
	mustGet(t, base+"/?code=evil&state=wrong-state")
	// Non-GET -> must be ignored (405, no delivery).
	mustReq(t, http.MethodPost, base+"/?code=evil&state=good-state")
	select {
	case r := <-resultCh:
		t.Fatalf("listener resolved on a forged callback: %+v", r)
	case <-time.After(150 * time.Millisecond):
		// Expected: neither forged request resolved the attempt.
	}

	// Correct state -> delivers the code.
	mustGet(t, base+"/?code=real-code&state=good-state")
	select {
	case r := <-resultCh:
		if r.Err != nil {
			t.Fatalf("unexpected error on valid callback: %v", r.Err)
		}
		if r.Code != "real-code" {
			t.Fatalf("code = %q, want real-code", r.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not resolve on the valid callback")
	}
}

// --- helpers ---

// newJSONServer starts an httptest server that decodes the JSON request body, passes it
// to fn, and writes the (status, body) fn returns.
func newJSONServer(t *testing.T, fn func(map[string]any) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		status, resp := fn(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, resp)
	}))
}

func mustGet(t *testing.T, rawURL string) { mustReq(t, http.MethodGet, rawURL) }

// mustReq issues a request and drains/closes the body; transport errors fail the test,
// but any HTTP status is acceptable (the callback returns 204/405 for ignored hits).
func mustReq(t *testing.T, method, rawURL string) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
