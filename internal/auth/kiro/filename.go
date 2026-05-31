package kiro

import (
	"fmt"
	"net/url"
	"strings"
)

// DirectoryIDFromStartURL extracts the IAM Identity Center directory identifier
// from an IDC start URL. AWS issues start URLs of the form
// https://d-XXXXXXXXXX.awsapps.com/start (or a custom alias in place of the
// d- id); the leading host label is that identifier and forms the "IDC" segment
// of the saved credential filename. It returns "" when no host label can be
// resolved, in which case callers fall back to a username-only filename.
func DirectoryIDFromStartURL(startURL string) string {
	s := strings.TrimSpace(startURL)
	if s == "" {
		return ""
	}
	// Tolerate scheme-less inputs (e.g. "d-90660ceab3.awsapps.com/start") by
	// defaulting to https so url.Parse populates the host rather than the path.
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	// The directory id / alias is the first dot-separated label of the host.
	label, _, _ := strings.Cut(host, ".")
	return label
}

// SanitizeFileComponent exposes the filename-component sanitizer so login entry
// points can reject an IDC username that collapses to empty (e.g. a non-Latin or
// symbol-only label), which would otherwise pass a raw empty-string check yet
// break the kiro-<username>-<directoryID>.json naming contract by falling back
// to a timestamp filename.
func SanitizeFileComponent(s string) string { return sanitizeFileComponent(s) }

// sanitizeFileComponent reduces an arbitrary string to a filesystem-safe
// filename component. It keeps alphanumerics plus '.', '_' and '-', collapses
// every run of other characters into a single '-', and trims leading/trailing
// dashes. This prevents a user-supplied username from injecting path separators
// or other unsafe characters into the persisted auth filename.
func sanitizeFileComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		safe := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-'
		if safe {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		// Collapse any run of unsafe characters into a single separator.
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// CredentialFileName builds the filename used to persist a Kiro credential.
//
// For the IDC (IAM Identity Center) method the goal format is
// kiro-<username>-<directoryID>.json, where username is operator-supplied (the
// IDC access token is an opaque AWS blob and carries no derivable identity) and
// directoryID is parsed from the start URL host. When the directory id cannot be
// resolved it degrades to kiro-<username>.json.
//
// For other methods it prefers an explicit username, then the JWT email (Builder
// ID tokens are JWTs), and finally a millisecond timestamp so a filename is
// always produced. The username is sanitized; the email is used verbatim to
// match the existing per-provider naming convention.
func CredentialFileName(method, username, startURL, email string, ts int64) string {
	username = sanitizeFileComponent(username)
	if username != "" {
		if method == "idc" {
			// Sanitize the directory id symmetrically with the username so a custom
			// alias host can never inject unexpected characters into the filename.
			if dirID := sanitizeFileComponent(DirectoryIDFromStartURL(startURL)); dirID != "" {
				return fmt.Sprintf("kiro-%s-%s.json", username, dirID)
			}
		}
		return fmt.Sprintf("kiro-%s.json", username)
	}
	if email = strings.TrimSpace(email); email != "" {
		return fmt.Sprintf("kiro-%s.json", email)
	}
	return fmt.Sprintf("kiro-%d.json", ts)
}
