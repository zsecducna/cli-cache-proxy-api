package kiro

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestBuildMachineID_DeterministicHex verifies the machine ID is a stable 64-char hex digest.
func TestBuildMachineID_DeterministicHex(t *testing.T) {
	a := BuildMachineID("client", "refresh", "arn", "access")
	b := BuildMachineID("client", "refresh", "arn", "access")
	if a != b {
		t.Fatalf("BuildMachineID not deterministic: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("BuildMachineID length = %d, want 64", len(a))
	}
	if c := BuildMachineID("other"); c == a {
		t.Fatal("BuildMachineID should differ for different seeds")
	}
}

// TestResolveKiroModel covers the synthetic agentic/thinking suffix decoding.
func TestResolveKiroModel(t *testing.T) {
	cases := []struct {
		in           string
		wantModel    string
		wantAgentic  bool
		wantThinking bool
	}{
		{"claude-sonnet-4.5", "claude-sonnet-4.5", false, false},
		{"claude-sonnet-4.5-agentic", "claude-sonnet-4.5", true, false},
		{"claude-sonnet-4.5-thinking", "claude-sonnet-4.5", false, true},
		{"claude-sonnet-4.5-agentic-thinking", "claude-sonnet-4.5", true, true},
	}
	for _, tc := range cases {
		model, agentic, thinking := ResolveKiroModel(tc.in)
		if model != tc.wantModel || agentic != tc.wantAgentic || thinking != tc.wantThinking {
			t.Fatalf("ResolveKiroModel(%q) = (%q,%v,%v), want (%q,%v,%v)", tc.in, model, agentic, thinking, tc.wantModel, tc.wantAgentic, tc.wantThinking)
		}
	}
}

// TestRegionFromProfileArn parses the region segment and tolerates malformed input.
func TestRegionFromProfileArn(t *testing.T) {
	if got := RegionFromProfileArn("arn:aws:codewhisperer:eu-west-1:123:profile/x"); got != "eu-west-1" {
		t.Fatalf("region = %q, want eu-west-1", got)
	}
	if got := RegionFromProfileArn(""); got != "" {
		t.Fatalf("region = %q, want empty", got)
	}
}

// TestClampThinkingBudget bounds and defaults the budget.
func TestClampThinkingBudget(t *testing.T) {
	if got := ClampThinkingBudget(0); got != DefaultThinkingBudget {
		t.Fatalf("ClampThinkingBudget(0) = %d, want %d", got, DefaultThinkingBudget)
	}
	if got := ClampThinkingBudget(99999); got != MaxThinkingBudget {
		t.Fatalf("ClampThinkingBudget(99999) = %d, want %d", got, MaxThinkingBudget)
	}
	if got := ClampThinkingBudget(8192); got != 8192 {
		t.Fatalf("ClampThinkingBudget(8192) = %d, want 8192", got)
	}
}

// TestBuildThinkingPrefix embeds the clamped budget in the prefix.
func TestBuildThinkingPrefix(t *testing.T) {
	prefix := BuildThinkingPrefix(8192)
	if !strings.Contains(prefix, "<thinking_mode>enabled</thinking_mode>") || !strings.Contains(prefix, "<max_thinking_length>8192</max_thinking_length>") {
		t.Fatalf("unexpected thinking prefix: %q", prefix)
	}
}

// TestRefreshToken_OIDCBranch verifies that supplying client credentials routes the
// refresh to the AWS SSO OIDC token endpoint.
func TestRefreshToken_OIDCBranch(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte(`{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}`))
	}))
	defer srv.Close()

	svc := NewKiroAuth(&config.Config{})
	svc.tokenURLFn = func(string) string { return srv.URL + "/token" }

	td, err := svc.RefreshToken(context.Background(), RefreshParams{
		RefreshToken: "r", ClientID: "cid", ClientSecret: "secret", Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("RefreshToken() error = %v", err)
	}
	if hitPath != "/token" {
		t.Fatalf("expected OIDC /token endpoint, hit %q", hitPath)
	}
	if td.AccessToken != "new-access" || td.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected token data: %+v", td)
	}
}

// TestRefreshToken_SocialBranch verifies that omitting client credentials routes the
// refresh to the social endpoint and captures the returned profile ARN.
func TestRefreshToken_SocialBranch(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte(`{"accessToken":"sa","refreshToken":"sr","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/p","expiresIn":3600}`))
	}))
	defer srv.Close()

	svc := NewKiroAuth(&config.Config{})
	svc.socialRefreshURL = srv.URL + "/social"

	td, err := svc.RefreshToken(context.Background(), RefreshParams{RefreshToken: "aorAAAAAGtoken"})
	if err != nil {
		t.Fatalf("RefreshToken() error = %v", err)
	}
	if hitPath != "/social" {
		t.Fatalf("expected social endpoint, hit %q", hitPath)
	}
	if td.ProfileArn == "" {
		t.Fatalf("expected profile ARN from social refresh, got empty")
	}
}

// TestValidateImportToken_RejectsBadPrefix ensures invalid tokens fail before any network call.
func TestValidateImportToken_RejectsBadPrefix(t *testing.T) {
	svc := NewKiroAuth(&config.Config{})
	if _, err := svc.ValidateImportToken(context.Background(), "not-a-kiro-token"); err == nil {
		t.Fatal("ValidateImportToken() expected error for bad prefix, got nil")
	}
}

// TestExtractEmailFromJWT decodes the email claim from a JWT payload segment.
func TestExtractEmailFromJWT(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"dev@example.com"}`))
	token := "header." + payload + ".sig"
	if got := ExtractEmailFromJWT(token); got != "dev@example.com" {
		t.Fatalf("ExtractEmailFromJWT() = %q, want dev@example.com", got)
	}
	if got := ExtractEmailFromJWT("not-a-jwt"); got != "" {
		t.Fatalf("ExtractEmailFromJWT(invalid) = %q, want empty", got)
	}
}
