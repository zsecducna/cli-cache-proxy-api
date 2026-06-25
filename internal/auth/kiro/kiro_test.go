package kiro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestListProfilesEndpoint_RegionHost verifies every region (including us-east-1 and the
// empty default) resolves to the q.{region}.amazonaws.com Amazon Q Developer host — the EU
// host the legacy codewhisperer.* alias never provided.
func TestListProfilesEndpoint_RegionHost(t *testing.T) {
	if got := ListProfilesEndpoint("us-east-1"); got != "https://q.us-east-1.amazonaws.com/" {
		t.Fatalf("us-east-1 endpoint = %q, want q.us-east-1 host", got)
	}
	if got := ListProfilesEndpoint(""); got != "https://q.us-east-1.amazonaws.com/" {
		t.Fatalf("empty-region endpoint = %q, want default q.us-east-1 host", got)
	}
	if got := ListProfilesEndpoint("eu-central-1"); got != "https://q.eu-central-1.amazonaws.com/" {
		t.Fatalf("eu-central-1 endpoint = %q, want q.eu-central-1 host", got)
	}
	if got := ListProfilesEndpoint("ap-south-1"); got != "https://q.ap-south-1.amazonaws.com/" {
		t.Fatalf("ap-south-1 endpoint = %q, want q.ap-south-1 host", got)
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

// TestResolveProfileArn_RefreshRetry verifies login-time profile resolution retries
// with a refreshed OIDC access token when the initial device-code token returns no profiles.
func TestResolveProfileArn_RefreshRetry(t *testing.T) {
	var profileLookupCount int
	var profileLookupAuths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/profiles":
			profileLookupCount++
			profileLookupAuths = append(profileLookupAuths, r.Header.Get("Authorization"))
			if profileLookupCount == 1 {
				_, _ = w.Write([]byte(`{"profiles":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:1:profile/p"}]}`))
		case "/token":
			_, _ = w.Write([]byte(`{"accessToken":"refreshed-access","refreshToken":"refreshed-refresh","expiresIn":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewKiroAuth(&config.Config{})
	svc.listProfilesURLFn = func(string) string { return srv.URL + "/profiles" }
	svc.tokenURLFn = func(string) string { return srv.URL + "/token" }

	resolvedTokenData, arn, err := svc.ResolveProfileArn(context.Background(), &KiroTokenData{
		AccessToken:  "initial-access",
		RefreshToken: "initial-refresh",
	}, RefreshParams{
		RefreshToken: "initial-refresh",
		ClientID:     "cid",
		ClientSecret: "secret",
		Region:       "us-east-1",
	})
	if err != nil {
		t.Fatalf("ResolveProfileArn() error = %v", err)
	}
	if arn != "arn:aws:codewhisperer:us-east-1:1:profile/p" {
		t.Fatalf("ResolveProfileArn() arn = %q, want resolved profile ARN", arn)
	}
	if resolvedTokenData == nil || resolvedTokenData.AccessToken != "refreshed-access" || resolvedTokenData.RefreshToken != "refreshed-refresh" {
		t.Fatalf("ResolveProfileArn() tokenData = %+v, want refreshed token data", resolvedTokenData)
	}
	if profileLookupCount != 2 {
		t.Fatalf("profile lookup count = %d, want 2", profileLookupCount)
	}
	if len(profileLookupAuths) != 2 || profileLookupAuths[0] != "Bearer initial-access" || profileLookupAuths[1] != "Bearer refreshed-access" {
		t.Fatalf("profile lookup auths = %#v, want initial then refreshed bearer tokens", profileLookupAuths)
	}
}

// TestCachedProfileArnForStartURL verifies the IDC fallback can reuse an already
// resolved profileArn from another Kiro auth in the configured auth directory.
func TestCachedProfileArnForStartURL(t *testing.T) {
	authDir := t.TempDir()
	payload := map[string]any{
		"type":        "kiro",
		"start_url":   "https://d-90660ceab3.awsapps.com/start",
		"region":      "us-east-1",
		"profile_arn": "arn:aws:codewhisperer:us-east-1:1:profile/p",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err = os.WriteFile(filepath.Join(authDir, "kiro-good.json"), raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	svc := NewKiroAuth(&config.Config{AuthDir: authDir})
	got := svc.CachedProfileArnForStartURL("https://d-90660ceab3.awsapps.com/start", "us-east-1")
	if got != "arn:aws:codewhisperer:us-east-1:1:profile/p" {
		t.Fatalf("CachedProfileArnForStartURL() = %q, want cached profile ARN", got)
	}
}

// TestListAvailableProfiles_UsesKiroClientHeaders verifies the profile-resolution call
// is shaped like a Kiro client request instead of a generic bearer-token JSON POST.
func TestListAvailableProfiles_UsesKiroClientHeaders(t *testing.T) {
	var gotContentType string
	var gotAccept string
	var gotAuth string
	var gotTarget string
	var gotSDKRequest string
	var gotAgentMode string
	var gotOptOut string
	var gotInvocationID string
	var gotUserAgent string
	var gotXAmzUserAgent string
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotAuth = r.Header.Get("Authorization")
		gotTarget = r.Header.Get("X-Amz-Target")
		gotSDKRequest = r.Header.Get("amz-sdk-request")
		gotAgentMode = r.Header.Get("x-amzn-kiro-agent-mode")
		gotOptOut = r.Header.Get("x-amzn-codewhisperer-optout")
		gotInvocationID = r.Header.Get("amz-sdk-invocation-id")
		gotUserAgent = r.Header.Get("User-Agent")
		gotXAmzUserAgent = r.Header.Get("x-amz-user-agent")
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:1:profile/p"}]}`))
	}))
	defer srv.Close()

	svc := NewKiroAuth(&config.Config{})
	svc.listProfilesURLFn = func(string) string { return srv.URL }

	arn, err := svc.ListAvailableProfiles(context.Background(), "access-token", "us-east-1", false)
	if err != nil {
		t.Fatalf("ListAvailableProfiles() error = %v", err)
	}
	if arn == "" {
		t.Fatal("ListAvailableProfiles() arn = empty, want parsed profile ARN")
	}
	if gotContentType != "application/x-amz-json-1.0" {
		t.Fatalf("Content-Type = %q, want application/x-amz-json-1.0", gotContentType)
	}
	if gotAccept != "application/x-amz-json-1.0" {
		t.Fatalf("Accept = %q, want application/x-amz-json-1.0", gotAccept)
	}
	if gotAuth != "Bearer access-token" {
		t.Fatalf("Authorization = %q, want bearer access token", gotAuth)
	}
	if gotTarget != ListProfilesTarget {
		t.Fatalf("X-Amz-Target = %q, want %q", gotTarget, ListProfilesTarget)
	}
	if gotSDKRequest != "attempt=1; max=1" {
		t.Fatalf("amz-sdk-request = %q, want attempt=1; max=1", gotSDKRequest)
	}
	if gotAgentMode != "vibe" {
		t.Fatalf("x-amzn-kiro-agent-mode = %q, want vibe", gotAgentMode)
	}
	if gotOptOut != "true" {
		t.Fatalf("x-amzn-codewhisperer-optout = %q, want true", gotOptOut)
	}
	if gotInvocationID == "" {
		t.Fatal("amz-sdk-invocation-id = empty, want non-empty request id")
	}
	if !strings.Contains(gotUserAgent, "KiroIDE-") {
		t.Fatalf("User-Agent = %q, want KiroIDE marker", gotUserAgent)
	}
	if !strings.Contains(gotXAmzUserAgent, "KiroIDE-") {
		t.Fatalf("x-amz-user-agent = %q, want KiroIDE marker", gotXAmzUserAgent)
	}
	if gotBody != "{}" {
		t.Fatalf("body = %q, want {}", gotBody)
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
