package executor

import (
	"context"
	"testing"

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

// TestRefresh_NoRefreshTokenIsNoop verifies Refresh returns the auth unchanged and makes
// no network call when there is no refresh token.
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
