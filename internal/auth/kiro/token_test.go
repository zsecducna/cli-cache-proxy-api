package kiro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSaveTokenToFile_RoundTripFlattensMetadata verifies the storage serializes its
// fields (with type=kiro) and flattens injected metadata.
func TestSaveTokenToFile_RoundTripFlattensMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiro-1.json")

	ts := &KiroTokenStorage{
		AccessToken:  "access-123",
		RefreshToken: "aorAAAAAGrefresh",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		ProfileArn:   "arn:aws:codewhisperer:us-west-2:123:profile/abc",
		Region:       "us-west-2",
		AuthMethod:   "import",
	}
	ts.SetMetadata(map[string]any{"email": "user@example.com"})

	if err := ts.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back error = %v", err)
	}
	var parsed map[string]any
	if err = json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	if parsed["type"] != "kiro" {
		t.Fatalf("type = %v, want kiro", parsed["type"])
	}
	if parsed["access_token"] != "access-123" {
		t.Fatalf("access_token = %v, want access-123", parsed["access_token"])
	}
	if parsed["email"] != "user@example.com" {
		t.Fatalf("flattened metadata email = %v, want user@example.com", parsed["email"])
	}
}

// TestIsExpired covers the expiry threshold and unset/invalid cases.
func TestIsExpired(t *testing.T) {
	cases := []struct {
		name    string
		expired string
		want    bool
	}{
		{"unset", "", false},
		{"future", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), false},
		{"within threshold", time.Now().Add(time.Minute).UTC().Format(time.RFC3339), true},
		{"past", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), true},
		{"unparseable", "not-a-time", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := &KiroTokenStorage{Expired: tc.expired}
			if got := ts.IsExpired(); got != tc.want {
				t.Fatalf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNeedsRefresh requires both a refresh token and an expired access token.
func TestNeedsRefresh(t *testing.T) {
	expiredPast := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if (&KiroTokenStorage{Expired: expiredPast}).NeedsRefresh() {
		t.Fatal("NeedsRefresh() = true without refresh token, want false")
	}
	if !(&KiroTokenStorage{RefreshToken: "r", Expired: expiredPast}).NeedsRefresh() {
		t.Fatal("NeedsRefresh() = false with refresh token and expiry, want true")
	}
}

// TestRequireProfileArn verifies Kiro rejects credentials that never resolved the
// runtime-mandatory profile ARN.
func TestRequireProfileArn(t *testing.T) {
	if err := RequireProfileArn("arn:aws:codewhisperer:us-east-1:1:profile/p", "kiro test"); err != nil {
		t.Fatalf("RequireProfileArn() unexpected error = %v", err)
	}
	if err := RequireProfileArn("", "kiro test"); err == nil {
		t.Fatal("RequireProfileArn() error = nil, want missing-profile failure")
	}
}
