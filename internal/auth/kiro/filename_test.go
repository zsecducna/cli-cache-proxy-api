package kiro

import "testing"

// TestDirectoryIDFromStartURL verifies that the IAM Identity Center directory
// identifier is extracted from the leading host label of the start URL. This is
// the "IDC" segment of the credential filename (e.g. kiro-<username>-<dirID>.json).
func TestDirectoryIDFromStartURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Canonical AWS-issued directory id form (https://d-XXXXXXXXXX.awsapps.com/start).
		{"d-form", "https://d-90660ceab3.awsapps.com/start", "d-90660ceab3"},
		// Custom directory alias instead of a d- id.
		{"alias-form", "https://mycompany.awsapps.com/start", "mycompany"},
		// Scheme-less input must still resolve (we default the scheme to https).
		{"scheme-less", "d-90660ceab3.awsapps.com/start", "d-90660ceab3"},
		// Trailing whitespace is tolerated.
		{"trimmed", "  https://d-90660ceab3.awsapps.com/start  ", "d-90660ceab3"},
		// Empty / unparseable inputs yield an empty id (callers fall back).
		{"empty", "", ""},
		{"no-host", "https:///start", ""},
	}
	for _, tc := range cases {
		if got := DirectoryIDFromStartURL(tc.in); got != tc.want {
			t.Fatalf("%s: DirectoryIDFromStartURL(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestSanitizeFileComponent ensures filename components are reduced to a
// filesystem-safe character set so a username can never inject path separators
// or other unsafe characters into the saved auth filename.
func TestSanitizeFileComponent(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"wqh9niny3", "wqh9niny3"},
		{"d-90660ceab3", "d-90660ceab3"},
		{"user.name_1", "user.name_1"},
		{"a/b\\c", "a-b-c"},
		{"  spaced  ", "spaced"},
		{"weird@#$%name", "weird-name"},
		{"--leading--", "leading"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := sanitizeFileComponent(tc.in); got != tc.want {
			t.Fatalf("sanitizeFileComponent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCredentialFileName covers the filename produced for each login method.
// The IDC scheme is the goal format: kiro-<username>-<directoryID>.json.
func TestCredentialFileName(t *testing.T) {
	const ts int64 = 1780222460239

	cases := []struct {
		name     string
		method   string
		username string
		startURL string
		email    string
		want     string
	}{
		// Goal example: IDC with username + directory id -> deterministic name.
		{
			name:     "idc-goal-example",
			method:   "idc",
			username: "wqh9niny3",
			startURL: "https://d-90660ceab3.awsapps.com/start",
			want:     "kiro-wqh9niny3-d-90660ceab3.json",
		},
		// IDC username is sanitized before use.
		{
			name:     "idc-sanitized-username",
			method:   "idc",
			username: "John Doe",
			startURL: "https://d-90660ceab3.awsapps.com/start",
			want:     "kiro-John-Doe-d-90660ceab3.json",
		},
		// IDC with username but an unparseable start URL falls back to username-only.
		{
			name:     "idc-no-dir",
			method:   "idc",
			username: "wqh9niny3",
			startURL: "",
			want:     "kiro-wqh9niny3.json",
		},
		// A custom alias host is sanitized symmetrically with the username.
		{
			name:     "idc-sanitized-alias",
			method:   "idc",
			username: "alice",
			startURL: "https://my_company!.awsapps.com/start",
			want:     "kiro-alice-my_company.json",
		},
		// Builder ID with a username uses the username directly.
		{
			name:     "builder-id-username",
			method:   "builder-id",
			username: "alice",
			want:     "kiro-alice.json",
		},
		// Builder ID without a username but with a JWT email uses the email.
		{
			name:   "builder-id-email",
			method: "builder-id",
			email:  "dev@example.com",
			want:   "kiro-dev@example.com.json",
		},
		// No identifying information at all -> timestamp fallback (legacy behavior).
		{
			name:   "timestamp-fallback",
			method: "builder-id",
			want:   "kiro-1780222460239.json",
		},
	}
	for _, tc := range cases {
		got := CredentialFileName(tc.method, tc.username, tc.startURL, tc.email, ts)
		if got != tc.want {
			t.Fatalf("%s: CredentialFileName(%q,%q,%q,%q) = %q, want %q",
				tc.name, tc.method, tc.username, tc.startURL, tc.email, got, tc.want)
		}
	}
}
