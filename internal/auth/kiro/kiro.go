package kiro

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// defaultPollInterval is the default interval between token polls.
	defaultPollInterval = 5 * time.Second
	// maxPollDuration bounds how long we wait for the user to authorize.
	maxPollDuration = 15 * time.Minute
	// credentialTimeout bounds individual credential-phase HTTP calls. Timeouts are
	// only permitted during credential acquisition, never on the generate stream.
	credentialTimeout = 30 * time.Second
	// socialAuthBase is the Cognito-backed social login base (Google/GitHub) used for
	// refreshing imported social refresh tokens.
	socialAuthBase = "https://prod.us-east-1.auth.desktop.kiro.dev"
)

// KiroAuth performs the AWS SSO OIDC credential flows for Kiro.
type KiroAuth struct {
	cfg      *config.Config
	proxyURL string
	// tokenURLFn and socialRefreshURL are overridable in tests; they default to the real
	// AWS SSO OIDC token endpoint and the social refresh endpoint respectively.
	tokenURLFn       func(region string) string
	socialRefreshURL string
}

// NewKiroAuth creates a new KiroAuth service instance.
func NewKiroAuth(cfg *config.Config) *KiroAuth {
	return &KiroAuth{cfg: cfg, tokenURLFn: OIDCTokenURL, socialRefreshURL: socialAuthBase + "/refreshToken"}
}

// NewKiroAuthWithProxy creates a KiroAuth that overrides the configured proxy URL.
func NewKiroAuthWithProxy(cfg *config.Config, proxyURL string) *KiroAuth {
	return &KiroAuth{cfg: cfg, proxyURL: strings.TrimSpace(proxyURL), tokenURLFn: OIDCTokenURL, socialRefreshURL: socialAuthBase + "/refreshToken"}
}

// httpClient builds a proxy-aware HTTP client with a credential-phase timeout.
func (k *KiroAuth) httpClient() *http.Client {
	client := &http.Client{Timeout: credentialTimeout}
	var sdkCfg config.SDKConfig
	effectiveProxy := k.proxyURL
	if k.cfg != nil {
		sdkCfg = k.cfg.SDKConfig
		if effectiveProxy == "" {
			effectiveProxy = strings.TrimSpace(k.cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxy
	return util.SetProxy(&sdkCfg, client)
}

// postJSON issues a POST with a JSON body and decodes the JSON response into out.
// It returns the HTTP status code and the raw body for callers that need to branch
// on pending states.
func (k *KiroAuth) postJSON(ctx context.Context, url string, payload any, out any) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("kiro: failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("kiro: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := k.httpClient().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("kiro: request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kiro: close response body error: %v", errClose)
		}
	}()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("kiro: failed to read response: %w", err)
	}
	// Decode best-effort; callers inspect status/body for pending/error states.
	if out != nil && len(respBody) > 0 {
		_ = json.Unmarshal(respBody, out)
	}
	return resp.StatusCode, respBody, nil
}

// tokenURL resolves the OIDC token endpoint, honoring a test override when present.
func (k *KiroAuth) tokenURL(region string) string {
	if k.tokenURLFn != nil {
		return k.tokenURLFn(region)
	}
	return OIDCTokenURL(region)
}

// socialRefreshEndpoint resolves the social refresh endpoint, honoring a test override.
func (k *KiroAuth) socialRefreshEndpoint() string {
	if strings.TrimSpace(k.socialRefreshURL) != "" {
		return k.socialRefreshURL
	}
	return socialAuthBase + "/refreshToken"
}

// RegisterClient registers a public OAuth client with AWS SSO OIDC and returns the
// client credentials used by the device authorization and token endpoints.
func (k *KiroAuth) RegisterClient(ctx context.Context, region string) (clientID string, clientSecret string, err error) {
	payload := map[string]any{
		"clientName": OAuthClientName,
		"clientType": "public",
		"scopes":     OAuthScopes,
		"grantTypes": []string{DeviceCodeGrantType, RefreshGrantType},
		"issuerUrl":  IssuerURL,
	}
	var out struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	status, respBody, err := k.postJSON(ctx, OIDCRegisterURL(region), payload, &out)
	if err != nil {
		return "", "", err
	}
	if status < 200 || status >= 300 || out.ClientID == "" || out.ClientSecret == "" {
		return "", "", fmt.Errorf("kiro: client registration failed (status %d): %s", status, string(respBody))
	}
	return out.ClientID, out.ClientSecret, nil
}

// StartDeviceAuthorization requests a device + user code pair for the given start URL.
func (k *KiroAuth) StartDeviceAuthorization(ctx context.Context, clientID, clientSecret, startURL, region string) (*DeviceCodeResponse, error) {
	if strings.TrimSpace(startURL) == "" {
		startURL = BuilderIDStartURL
	}
	payload := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     startURL,
	}
	var out DeviceCodeResponse
	status, respBody, err := k.postJSON(ctx, OIDCDeviceAuthURL(region), payload, &out)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || out.DeviceCode == "" {
		return nil, fmt.Errorf("kiro: device authorization failed (status %d): %s", status, string(respBody))
	}
	return &out, nil
}

// PollForToken polls the token endpoint until the user authorizes, the device code
// expires, or the context is cancelled.
func (k *KiroAuth) PollForToken(ctx context.Context, clientID, clientSecret, deviceCode, region string, deviceCodeResp *DeviceCodeResponse) (*KiroTokenData, error) {
	interval := defaultPollInterval
	deadline := time.Now().Add(maxPollDuration)
	if deviceCodeResp != nil {
		if deviceCodeResp.Interval > 0 {
			interval = time.Duration(deviceCodeResp.Interval) * time.Second
		}
		if deviceCodeResp.ExpiresIn > 0 {
			if codeDeadline := time.Now().Add(time.Duration(deviceCodeResp.ExpiresIn) * time.Second); codeDeadline.Before(deadline) {
				deadline = codeDeadline
			}
		}
	}
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("kiro: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("kiro: device code expired")
			}
			token, pending, errPoll := k.exchangeDeviceCode(ctx, clientID, clientSecret, deviceCode, region)
			if token != nil {
				return token, nil
			}
			if !pending {
				return nil, errPoll
			}
			// Pending (authorization_pending / slow_down): keep polling.
		}
	}
}

// exchangeDeviceCode performs one token poll. It returns (token, pending, error):
// pending is true when the caller should keep polling.
func (k *KiroAuth) exchangeDeviceCode(ctx context.Context, clientID, clientSecret, deviceCode, region string) (*KiroTokenData, bool, error) {
	payload := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    DeviceCodeGrantType,
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Error        string `json:"error"`
	}
	status, respBody, err := k.postJSON(ctx, OIDCTokenURL(region), payload, &out)
	if err != nil {
		return nil, false, err
	}
	// AWS reports pending states either via a JSON "error" field or an HTTP error.
	if out.Error == "authorization_pending" || out.Error == "slow_down" {
		return nil, true, nil
	}
	if status < 200 || status >= 300 {
		// Treat unknown 4xx without a recognized error as pending only when no error code
		// is present; otherwise surface it so the user is not stuck forever.
		if out.Error == "" && status == http.StatusBadRequest {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("kiro: token poll failed (status %d): %s", status, string(respBody))
	}
	if out.AccessToken == "" {
		return nil, false, fmt.Errorf("kiro: empty access token in token response")
	}
	return k.toTokenData(out.AccessToken, out.RefreshToken, out.ExpiresIn, ""), false, nil
}

// RefreshParams carries the inputs required to refresh a Kiro token. When ClientID and
// ClientSecret are present the AWS SSO OIDC branch is used; otherwise the social branch.
type RefreshParams struct {
	RefreshToken string
	ClientID     string
	ClientSecret string
	Region       string
}

// RefreshToken exchanges a refresh token for a fresh access token. It selects the AWS
// OIDC branch when client credentials are present, falling back to the social branch.
func (k *KiroAuth) RefreshToken(ctx context.Context, params RefreshParams) (*KiroTokenData, error) {
	if strings.TrimSpace(params.RefreshToken) == "" {
		return nil, fmt.Errorf("kiro: refresh token is empty")
	}
	if strings.TrimSpace(params.ClientID) != "" && strings.TrimSpace(params.ClientSecret) != "" {
		return k.refreshViaOIDC(ctx, params)
	}
	return k.refreshViaSocial(ctx, params.RefreshToken)
}

// refreshViaOIDC refreshes through the AWS SSO OIDC token endpoint.
func (k *KiroAuth) refreshViaOIDC(ctx context.Context, params RefreshParams) (*KiroTokenData, error) {
	payload := map[string]any{
		"clientId":     params.ClientID,
		"clientSecret": params.ClientSecret,
		"refreshToken": params.RefreshToken,
		"grantType":    RefreshGrantType,
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
	}
	status, respBody, err := k.postJSON(ctx, k.tokenURL(params.Region), payload, &out)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || out.AccessToken == "" {
		return nil, fmt.Errorf("kiro: refresh failed (status %d): %s", status, string(respBody))
	}
	return k.toTokenData(out.AccessToken, out.RefreshToken, out.ExpiresIn, ""), nil
}

// refreshViaSocial refreshes through the Cognito-backed social endpoint, which also
// returns the CodeWhisperer profile ARN.
func (k *KiroAuth) refreshViaSocial(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	payload := map[string]any{"refreshToken": refreshToken}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    int64  `json:"expiresIn"`
	}
	status, respBody, err := k.postJSON(ctx, k.socialRefreshEndpoint(), payload, &out)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || out.AccessToken == "" {
		return nil, fmt.Errorf("kiro: social refresh failed (status %d): %s", status, string(respBody))
	}
	td := k.toTokenData(out.AccessToken, out.RefreshToken, out.ExpiresIn, out.ProfileArn)
	return td, nil
}

// toTokenData normalizes a token response, defaulting the refresh token and computing
// the absolute expiry from the relative expiresIn.
func (k *KiroAuth) toTokenData(accessToken, refreshToken string, expiresIn int64, profileArn string) *KiroTokenData {
	var expiresAt int64
	if expiresIn > 0 {
		expiresAt = time.Now().Unix() + expiresIn
	}
	return &KiroTokenData{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		ProfileArn:   profileArn,
	}
}

// ValidateImportToken validates an imported refresh token by checking its prefix and
// performing a single social refresh. It returns the freshly minted token data.
func (k *KiroAuth) ValidateImportToken(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if !strings.HasPrefix(refreshToken, ImportTokenPrefix) {
		return nil, fmt.Errorf("kiro: invalid refresh token (must start with %q)", ImportTokenPrefix)
	}
	return k.refreshViaSocial(ctx, refreshToken)
}

// AutoDetectToken scans ~/.aws/sso/cache for a cached Kiro refresh token, preferring
// kiro-auth-token.json. It returns the first refresh token with the expected prefix.
func (k *KiroAuth) AutoDetectToken() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("kiro: cannot resolve home dir: %w", err)
	}
	cacheDir := filepath.Join(home, ".aws", "sso", "cache")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return "", fmt.Errorf("kiro: cannot read sso cache: %w", err)
	}
	// Order candidates so the dedicated kiro file is inspected first.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	readToken := func(name string) string {
		data, errRead := os.ReadFile(filepath.Join(cacheDir, name))
		if errRead != nil {
			return ""
		}
		var parsed struct {
			RefreshToken string `json:"refreshToken"`
		}
		if errParse := json.Unmarshal(data, &parsed); errParse != nil {
			return ""
		}
		if strings.HasPrefix(strings.TrimSpace(parsed.RefreshToken), ImportTokenPrefix) {
			return strings.TrimSpace(parsed.RefreshToken)
		}
		return ""
	}
	if token := readToken("kiro-auth-token.json"); token != "" {
		return token, nil
	}
	for _, name := range names {
		if name == "kiro-auth-token.json" {
			continue
		}
		if token := readToken(name); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("kiro: no cached refresh token found in %s", cacheDir)
}

// ExtractEmailFromJWT decodes the JWT payload of an access token (best-effort) and
// returns the embedded email claim, or "" when unavailable.
func ExtractEmailFromJWT(accessToken string) string {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some tokens use standard (padded) base64; retry leniently.
		if payload, err = base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return ""
		}
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Email)
}

// ListAvailableProfiles fetches the CodeWhisperer profiles for this credential and
// returns the first profile's ARN. Mirrors the Kiro / Amazon Q Developer CLI call:
// POST https://codewhisperer.{region}.amazonaws.com/ with X-Amz-Target
// AmazonCodeWhispererService.ListAvailableProfiles (aws-json-1.0). The runtime generate
// endpoint requires this profileArn.
func (k *KiroAuth) ListAvailableProfiles(ctx context.Context, accessToken, region string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("kiro: access token is empty")
	}
	machineID := BuildMachineID(accessToken)
	body, err := json.Marshal(map[string]any{})
	if err != nil {
		return "", fmt.Errorf("kiro: failed to marshal list-profiles request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ListProfilesEndpoint(region), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("kiro: failed to create list-profiles request: %w", err)
	}
	// aws-json-1.0 protocol: operation is routed by X-Amz-Target, not the path.
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Amz-Target", ListProfilesTarget)
	req.Header.Set("User-Agent", BuildUserAgent(machineID))
	req.Header.Set("x-amz-user-agent", BuildXAmzUserAgent(machineID))

	resp, err := k.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("kiro: list-profiles request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kiro list-profiles: close body error: %v", errClose)
		}
	}()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("kiro: failed to read list-profiles response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("kiro: list-profiles failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if err = json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("kiro: failed to parse list-profiles response: %w", err)
	}
	for _, p := range parsed.Profiles {
		if strings.TrimSpace(p.Arn) != "" {
			return p.Arn, nil
		}
	}
	return "", fmt.Errorf("kiro: no profiles available")
}
