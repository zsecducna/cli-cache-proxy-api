package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// refreshThresholdSeconds is how long before expiry a token is treated as expired (5 minutes).
const refreshThresholdSeconds = 300

// KiroTokenStorage stores OAuth2 token information for Kiro (AWS CodeWhisperer) authentication.
// It is persisted to auths/kiro-*.json and read back by the store, which derives the
// provider from the "type" field.
type KiroTokenStorage struct {
	// AccessToken is the bearer token used for CodeWhisperer requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is exchanged for new access tokens.
	RefreshToken string `json:"refresh_token"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
	// ProfileArn optionally identifies the CodeWhisperer profile and carries the region.
	ProfileArn string `json:"profile_arn,omitempty"`
	// ClientID and ClientSecret are the registered AWS SSO OIDC client credentials
	// (present for device-code logins, absent for social logins).
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	// Region is the AWS region used for the OIDC endpoints.
	Region string `json:"region,omitempty"`
	// AuthMethod records how the credential was obtained: builder-id|idc|import|social.
	AuthMethod string `json:"auth_method,omitempty"`
	// StartURL is the IAM Identity Center start URL (IDC method only).
	StartURL string `json:"start_url,omitempty"`
	// Username is the operator-supplied account label used to name the auth file
	// (kiro-<username>-<directoryID>.json). Required for the IDC method because the
	// IDC access token is an opaque AWS blob that carries no derivable identity.
	Username string `json:"username,omitempty"`
	// Email is the best-effort account email parsed from the access token JWT.
	Email string `json:"email,omitempty"`
	// TokenEndpoint, IssuerURL and Scopes are set for the external IdP (enterprise SSO)
	// method. The credential is an IdP-issued OAuth token (e.g. from an Azure AD tenant)
	// refreshed against TokenEndpoint using the IdP ClientID and Scopes (refresh_token grant).
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	IssuerURL     string `json:"issuer_url,omitempty"`
	Scopes        string `json:"scopes,omitempty"`
	// Type indicates the authentication provider type, always "kiro" for this storage.
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks. It is flattened
	// during serialization and therefore not exported directly to JSON.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *KiroTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// KiroTokenData holds the raw OAuth token response from AWS SSO OIDC.
// Note AWS returns camelCase fields (accessToken/refreshToken/expiresIn).
type KiroTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"accessToken"`
	// RefreshToken is the OAuth2 refresh token.
	RefreshToken string `json:"refreshToken"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expiresAt"`
	// ProfileArn optionally carries the CodeWhisperer profile ARN (social refresh returns it).
	ProfileArn string `json:"profileArn,omitempty"`
}

// KiroAuthBundle bundles the data produced by a successful login flow.
type KiroAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *KiroTokenData
	// ClientID and ClientSecret are the registered OIDC client credentials (device flows).
	ClientID     string
	ClientSecret string
	// Region is the AWS region used during login.
	Region string
	// AuthMethod records the login method used.
	AuthMethod string
	// StartURL is the IDC start URL when applicable.
	StartURL string
	// Username is the operator-supplied account label (IDC method) used to name
	// the persisted auth file.
	Username string
	// ProfileArn is the resolved CodeWhisperer profile ARN when available.
	ProfileArn string
	// Email is the best-effort account email.
	Email string
	// TokenEndpoint, IssuerURL and Scopes are set for the external IdP method and carry the
	// IdP token endpoint, issuer, and granted scopes needed to refresh the credential.
	TokenEndpoint string
	IssuerURL     string
	Scopes        string
}

// DeviceCodeResponse represents the AWS SSO OIDC device authorization response.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code used when polling for the token.
	DeviceCode string `json:"deviceCode"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"userCode"`
	// VerificationURI is the URL where the user should enter the code.
	VerificationURI string `json:"verificationUri,omitempty"`
	// VerificationURIComplete is the URL with the code pre-filled.
	VerificationURIComplete string `json:"verificationUriComplete"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expiresIn"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
}

// SaveTokenToFile serializes the Kiro token storage to a JSON file, flattening Metadata.
func (ts *KiroTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "kiro"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	// 0600: the file holds the refresh token and OIDC client secret, so restrict it to the
	// owner (os.Create would use 0666 & umask, typically 0644).
	f, err := os.OpenFile(authFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	// Merge metadata using the shared helper so injected hook values are flattened in.
	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired reports whether the access token has expired (within the refresh threshold).
func (ts *KiroTokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false // No expiry set, assume valid.
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true // Has an expiry string but it cannot be parsed.
	}
	return time.Now().Add(time.Duration(refreshThresholdSeconds) * time.Second).After(t)
}

// NeedsRefresh reports whether the token should be refreshed.
func (ts *KiroTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false // Cannot refresh without a refresh token.
	}
	return ts.IsExpired()
}
