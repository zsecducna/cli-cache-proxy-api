package kiro

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// social.go implements the Kiro browser sign-in flow — the same flow the Kiro IDE uses.
// It starts as a PKCE authorization-code flow against the Kiro-hosted portal
// (app.kiro.dev/signin), which then branches by account type:
//
//   - Social (Google/GitHub): the portal authenticates via its Cognito backend and
//     redirects the authorization code straight back to the loopback redirect. The code is
//     exchanged at the Kiro social token endpoint (ExchangeSocialCode).
//
//   - Enterprise / external IdP (e.g. an Azure AD tenant): the portal detects the email
//     belongs to an external IdP and redirects to /signin/callback with the IdP descriptor
//     (issuer_url, client_id, scopes) instead of a code. The proxy then drives a SECOND
//     OIDC authorization-code+PKCE flow directly against that IdP (loopback redirect), and
//     exchanges the code at the IdP token endpoint (ExchangeExternalIdpCode). The resulting
//     access token is an IdP-issued token scoped for CodeWhisperer; it is used as the
//     runtime bearer and refreshed against the IdP token endpoint.
//
// A single transient loopback listener on the fixed redirect port handles every leg via
// browser redirects, so both the WebUI and the CLI complete the flow without the proxy
// ever needing to "open" the intermediate URLs itself (it 302-redirects the browser).

// --- PKCE ---------------------------------------------------------------------------

// SocialPKCE holds a PKCE verifier/challenge pair plus the anti-CSRF state token used
// for one social sign-in attempt.
type SocialPKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// GenerateSocialPKCE creates a PKCE verifier, its S256 challenge, and a random state
// token for one Kiro sign-in attempt (RFC 7636).
func GenerateSocialPKCE() (*SocialPKCE, error) {
	verifier, err := randomURLSafe(96)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to generate code verifier: %w", err)
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to generate state: %w", err)
	}
	return &SocialPKCE{Verifier: verifier, Challenge: pkceChallenge(verifier), State: state}, nil
}

// pkceChallenge returns the S256 challenge (base64url, no padding) for a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomURLSafe returns n cryptographically random bytes encoded as unpadded base64url.
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SocialSignInURL builds the Kiro hosted sign-in URL for the given PKCE codes. The
// redirect URI and client tag are fixed values the portal validates.
func SocialSignInURL(pkce *SocialPKCE) string {
	q := url.Values{}
	q.Set("state", pkce.State)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("redirect_uri", SocialRedirectURI)
	q.Set("redirect_from", SocialRedirectFrom)
	return SocialSignInBaseURL + "?" + q.Encode()
}

// --- Social (Cognito) token exchange ------------------------------------------------

// socialTokenEndpoint resolves the Cognito-backed social token-exchange endpoint,
// honoring a test override when present.
func (k *KiroAuth) socialTokenEndpoint() string {
	if strings.TrimSpace(k.socialTokenURL) != "" {
		return k.socialTokenURL
	}
	return socialAuthBase + "/oauth/token"
}

// ExchangeSocialCode exchanges an authorization code (with its PKCE verifier) for Kiro
// tokens at the social token endpoint. Request body matches the Kiro IDE client exactly —
// {code, code_verifier, redirect_uri} — and the response mirrors the social refresh
// response (camelCase accessToken/refreshToken/profileArn/expiresIn).
func (k *KiroAuth) ExchangeSocialCode(ctx context.Context, code, codeVerifier string) (*KiroTokenData, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("kiro: social exchange requires an authorization code")
	}
	payload := map[string]any{
		"code":          code,
		"code_verifier": codeVerifier,
		"redirect_uri":  SocialRedirectURI,
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    int64  `json:"expiresIn"`
	}
	status, respBody, err := k.postJSON(ctx, k.socialTokenEndpoint(), payload, &out)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 || out.AccessToken == "" {
		return nil, fmt.Errorf("kiro: social token exchange failed (status %d): %s", status, string(respBody))
	}
	return k.toTokenData(out.AccessToken, out.RefreshToken, out.ExpiresIn, out.ProfileArn), nil
}

// --- External IdP (e.g. Azure AD) OIDC ----------------------------------------------

// allowedExternalIdpIssuerSuffixes restricts which IdP issuer/endpoint hosts the enterprise
// leg will discover and redirect to. The issuer arrives in an attacker-influenceable portal
// callback query, so it is constrained to known enterprise IdP hosts (Microsoft Entra /
// Azure AD — the supported provider). This is the primary control against SSRF, open-redirect,
// and forced-authorization abuse via a forged /signin/callback. The leading dot anchors each
// suffix to a real subdomain boundary so "evil-microsoftonline.com" cannot match. Extend this
// list to onboard additional enterprise IdPs.
var allowedExternalIdpIssuerSuffixes = []string{
	".microsoftonline.com",
	".microsoftonline.us",
	".microsoftonline.cn",
}

// validateExternalIdpEndpoint verifies rawURL is an https URL whose host is a non-IP,
// allow-listed enterprise IdP host. It is the single gate applied to the issuer (before
// discovery) and to BOTH discovered endpoints (the authorize URL the browser is 302'd to,
// and the token endpoint the code is exchanged at).
func validateExternalIdpEndpoint(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("kiro: invalid external IdP URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("kiro: external IdP URL must be https")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("kiro: external IdP URL has no host")
	}
	// Reject IP-literal hosts outright; only named, allow-listed IdP hosts are permitted.
	if net.ParseIP(host) != nil {
		return fmt.Errorf("kiro: external IdP host must not be an IP literal")
	}
	for _, suffix := range allowedExternalIdpIssuerSuffixes {
		if strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("kiro: external IdP host %q is not allow-listed", host)
}

// OIDCDiscover fetches the OpenID Connect discovery document for issuerURL and returns its
// authorization and token endpoints. issuerURL is the IdP issuer reported by the Kiro portal
// (e.g. https://login.microsoftonline.com/<tenant>/v2.0). The issuer and BOTH discovered
// endpoints are validated against the IdP host allow-list; redirects are NOT followed (so a
// discovery host cannot bounce the fetch to an internal target); and no response body is
// echoed into errors (so an internal response cannot be exfiltrated through the error).
func (k *KiroAuth) OIDCDiscover(ctx context.Context, issuerURL string) (authEndpoint, tokenEndpoint string, err error) {
	if err = validateExternalIdpEndpoint(issuerURL); err != nil {
		return "", "", err
	}
	docURL := strings.TrimRight(strings.TrimSpace(issuerURL), "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("kiro: failed to build OIDC discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Do not follow redirects: the allow-listed issuer host must answer directly, so a 3xx
	// (which could point at an internal/link-local target) is treated as a failure.
	client := k.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("kiro: OIDC discovery request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("kiro OIDC discovery: close body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("kiro: failed to read OIDC discovery response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Deliberately omit the body to avoid exfiltrating an internal response via the error.
		return "", "", fmt.Errorf("kiro: OIDC discovery failed (status %d)", resp.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err = json.Unmarshal(body, &doc); err != nil {
		return "", "", fmt.Errorf("kiro: failed to parse OIDC discovery document: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return "", "", fmt.Errorf("kiro: OIDC discovery document missing authorization_endpoint or token_endpoint")
	}
	// Both endpoints must themselves be https + allow-listed: the browser is 302'd to the
	// authorize endpoint and the authorization code is POSTed to the token endpoint, so a
	// tampered discovery document must not be able to point either at an arbitrary host.
	if err = validateExternalIdpEndpoint(doc.AuthorizationEndpoint); err != nil {
		return "", "", fmt.Errorf("kiro: discovered authorization_endpoint rejected: %w", err)
	}
	if err = validateExternalIdpEndpoint(doc.TokenEndpoint); err != nil {
		return "", "", fmt.Errorf("kiro: discovered token_endpoint rejected: %w", err)
	}
	return doc.AuthorizationEndpoint, doc.TokenEndpoint, nil
}

// ExternalIdpAuthorizeURL builds the IdP authorization-code+PKCE URL the browser is
// redirected to for the enterprise leg. scopes is passed through verbatim from the portal
// (already a space-separated list, e.g. "api://<id>/codewhisperer:conversations …").
func ExternalIdpAuthorizeURL(authEndpoint, clientID, redirectURI, scopes, challenge, state, loginHint string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("response_mode", "query")
	q.Set("state", state)
	if strings.TrimSpace(loginHint) != "" {
		q.Set("login_hint", loginHint)
	}
	return authEndpoint + "?" + q.Encode()
}

// ExchangeExternalIdpCode exchanges an IdP authorization code (with its PKCE verifier) for
// IdP tokens at the discovered token endpoint. Standard OAuth2 authorization_code grant for
// a public client (PKCE, no client secret); request is form-encoded and the response is
// snake_case (access_token/refresh_token/expires_in).
func (k *KiroAuth) ExchangeExternalIdpCode(ctx context.Context, tokenEndpoint, clientID, code, codeVerifier, redirectURI, scopes string) (*KiroTokenData, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("kiro: external IdP exchange requires an authorization code")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)
	if strings.TrimSpace(scopes) != "" {
		form.Set("scope", scopes)
	}
	return k.postExternalIdpToken(ctx, tokenEndpoint, form)
}

// refreshViaExternalIdp refreshes an IdP-issued token through the IdP token endpoint using
// the OAuth2 refresh_token grant (public client, no secret). offline_access in the original
// scopes is what makes the refresh token available.
func (k *KiroAuth) refreshViaExternalIdp(ctx context.Context, tokenEndpoint, clientID, refreshToken, scopes string) (*KiroTokenData, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if strings.TrimSpace(scopes) != "" {
		form.Set("scope", scopes)
	}
	return k.postExternalIdpToken(ctx, tokenEndpoint, form)
}

// postExternalIdpToken performs a form-encoded POST to an IdP token endpoint and maps the
// snake_case OAuth2 token response onto KiroTokenData. The IdP issues no profileArn (it is
// resolved separately via ListAvailableProfiles using the access token).
func (k *KiroAuth) postExternalIdpToken(ctx context.Context, tokenEndpoint string, form url.Values) (*KiroTokenData, error) {
	if strings.TrimSpace(tokenEndpoint) == "" {
		return nil, fmt.Errorf("kiro: external IdP token endpoint is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to build external IdP token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := k.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro: external IdP token request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("kiro external IdP token: close body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to read external IdP token response: %w", err)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.AccessToken == "" {
		if out.Error != "" {
			return nil, fmt.Errorf("kiro: external IdP token exchange failed (status %d): %s: %s", resp.StatusCode, out.Error, out.ErrorDesc)
		}
		return nil, fmt.Errorf("kiro: external IdP token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}
	return k.toTokenData(out.AccessToken, out.RefreshToken, out.ExpiresIn, ""), nil
}

// --- Loopback callback listener (state machine across the legs) ---------------------

// KiroLoginKind discriminates the outcome of the loopback flow.
type KiroLoginKind int

const (
	// KiroLoginSocial is a Cognito authorization code to exchange via ExchangeSocialCode.
	KiroLoginSocial KiroLoginKind = iota
	// KiroLoginExternalIdp is an IdP authorization code to exchange via ExchangeExternalIdpCode,
	// with the discovered token endpoint / client / scopes needed to do so.
	KiroLoginExternalIdp
)

// KiroLoginResult is delivered once the loopback listener captures a final authorization
// code (or an error). For the external IdP kind it also carries the leg-2 context required
// to complete the token exchange.
type KiroLoginResult struct {
	Kind KiroLoginKind
	Code string
	Err  error

	// External IdP (enterprise) leg-2 context.
	TokenEndpoint string
	IssuerURL     string
	ClientID      string
	Scopes        string
	RedirectURI   string
	CodeVerifier  string
}

// leg2Context is the per-attempt state captured when the enterprise descriptor arrives at
// /signin/callback and consumed when the IdP redirects the code back to /oauth/callback.
type leg2Context struct {
	state         string
	verifier      string
	tokenEndpoint string
	issuerURL     string
	clientID      string
	scopes        string
	redirectURI   string
}

// StartKiroLoginListener binds transient loopback HTTP listeners on the fixed redirect port
// (IPv4 127.0.0.1 and, best-effort, IPv6 ::1 — a browser resolving "localhost" may use
// either) and returns a channel that yields the final KiroLoginResult. The returned cleanup
// func MUST be called to release the port(s).
//
// It is a method on KiroAuth so the enterprise leg can use the proxy-aware HTTP client for
// OIDC discovery. portalState is the state echoed back on the SOCIAL redirect (the enterprise
// descriptor carries a portal-generated state instead, so it is not matched there; the
// security-critical match happens on the leg-2 IdP state which the proxy itself generates).
//
// Paths handled (all GET):
//   - /signin/callback?login_option=external_idp&issuer_url=…&client_id=…&scopes=… (no code):
//     enterprise descriptor → OIDC-discover the issuer, generate leg-2 PKCE, and 302-redirect
//     the browser to the IdP authorization URL (loopback redirect to /oauth/callback).
//   - /oauth/callback?code=…&state=<leg2 state>: the IdP authorization code → deliver
//     KiroLoginExternalIdp with the leg-2 context.
//   - any other path with ?code=… (and matching portalState): the social authorization code
//     → deliver KiroLoginSocial.
func (k *KiroAuth) StartKiroLoginListener(portalState string) (<-chan KiroLoginResult, func(), error) {
	ln4, err := net.Listen("tcp", "127.0.0.1:"+SocialRedirectPort)
	if err != nil {
		return nil, nil, fmt.Errorf("kiro: cannot bind loopback 127.0.0.1:%s for the SSO callback (is the port already in use?): %w", SocialRedirectPort, err)
	}

	resultCh := make(chan KiroLoginResult, 1)
	var once sync.Once
	deliver := func(r KiroLoginResult) { once.Do(func() { resultCh <- r }) }

	var mu sync.Mutex
	var leg2 *leg2Context // set when the enterprise descriptor arrives; read on /oauth/callback

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		// Only browser GET redirects are expected; reject other methods to shrink the
		// local attack surface.
		if req.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := req.URL.Query()

		// --- Enterprise leg-1: external IdP descriptor (no code) ---
		// The portal redirects here after detecting the email belongs to an external IdP.
		// Gate on path != /oauth/callback so a forged /oauth/callback?issuer_url=… cannot be
		// routed here and reset an in-flight leg-2.
		if req.URL.Path != OAuthCallbackPath &&
			(strings.EqualFold(strings.TrimSpace(q.Get("login_option")), "external_idp") || strings.TrimSpace(q.Get("issuer_url")) != "") {
			// Single-shot: once a leg-2 is in flight, ignore further descriptors so a stray or
			// forged local request cannot reset/hijack the active enterprise login.
			mu.Lock()
			alreadyStarted := leg2 != nil
			mu.Unlock()
			if alreadyStarted {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			issuerURL := strings.TrimSpace(q.Get("issuer_url"))
			clientID := strings.TrimSpace(q.Get("client_id"))
			scopes := strings.TrimSpace(q.Get("scopes"))
			loginHint := strings.TrimSpace(q.Get("login_hint"))
			if clientID == "" {
				writeSocialCallbackPage(w, false)
				deliver(KiroLoginResult{Err: fmt.Errorf("kiro: invalid external IdP descriptor (missing client_id)")})
				return
			}
			// OIDCDiscover validates the issuer + both discovered endpoints against the IdP
			// host allow-list (https only, no IP literals, no redirect-follow), so the issuer
			// here is not trusted blindly.
			authEndpoint, tokenEndpoint, errDisc := k.OIDCDiscover(req.Context(), issuerURL)
			if errDisc != nil {
				writeSocialCallbackPage(w, false)
				deliver(KiroLoginResult{Err: errDisc})
				return
			}
			verifier, errV := randomURLSafe(96)
			state2, errS := randomURLSafe(32)
			if errV != nil || errS != nil {
				writeSocialCallbackPage(w, false)
				deliver(KiroLoginResult{Err: fmt.Errorf("kiro: failed to generate leg-2 PKCE")})
				return
			}
			redirectURI := SocialRedirectURI + OAuthCallbackPath
			mu.Lock()
			// Re-check under the lock to resolve a race between concurrent descriptors: only
			// the first sets leg2 and is redirected; a loser returns 204 without redirecting.
			if leg2 != nil {
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			leg2 = &leg2Context{
				state:         state2,
				verifier:      verifier,
				tokenEndpoint: tokenEndpoint,
				issuerURL:     issuerURL,
				clientID:      clientID,
				scopes:        scopes,
				redirectURI:   redirectURI,
			}
			mu.Unlock()
			authURL := ExternalIdpAuthorizeURL(authEndpoint, clientID, redirectURI, scopes, pkceChallenge(verifier), state2, loginHint)
			// Redirect the SAME browser tab on to the IdP login page.
			http.Redirect(w, req, authURL, http.StatusFound)
			return
		}

		// --- Enterprise leg-2: IdP authorization code at /oauth/callback ---
		if req.URL.Path == OAuthCallbackPath {
			mu.Lock()
			ctx2 := leg2
			mu.Unlock()
			code := strings.TrimSpace(q.Get("code"))
			state := strings.TrimSpace(q.Get("state"))
			errParam := strings.TrimSpace(q.Get("error"))
			// Ignore callbacks that don't match the in-flight leg-2 state (anti-CSRF / stray
			// hits) without consuming the one-shot.
			if ctx2 == nil || state == "" || state != ctx2.state {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if errParam != "" {
				desc := strings.TrimSpace(q.Get("error_description"))
				writeSocialCallbackPage(w, false)
				deliver(KiroLoginResult{Err: fmt.Errorf("kiro: external IdP authorization error: %s %s", errParam, desc)})
				return
			}
			if code == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeSocialCallbackPage(w, true)
			deliver(KiroLoginResult{
				Kind:          KiroLoginExternalIdp,
				Code:          code,
				TokenEndpoint: ctx2.tokenEndpoint,
				IssuerURL:     ctx2.issuerURL,
				ClientID:      ctx2.clientID,
				Scopes:        ctx2.scopes,
				RedirectURI:   ctx2.redirectURI,
				CodeVerifier:  ctx2.verifier,
			})
			return
		}

		// --- Social leg-1: Cognito authorization code ---
		code := strings.TrimSpace(q.Get("code"))
		errParam := strings.TrimSpace(q.Get("error"))
		state := strings.TrimSpace(q.Get("state"))
		// Ignore stray hits with neither a code nor an error, and any callback whose state
		// does not match — WITHOUT consuming the one-shot.
		if code == "" && errParam == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if portalState == "" || state != portalState {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if errParam != "" {
			desc := strings.TrimSpace(q.Get("error_description"))
			writeSocialCallbackPage(w, false)
			deliver(KiroLoginResult{Err: fmt.Errorf("kiro: SSO authorization error: %s %s", errParam, desc)})
			return
		}
		writeSocialCallbackPage(w, true)
		deliver(KiroLoginResult{Kind: KiroLoginSocial, Code: code})
	})

	// ReadHeaderTimeout bounds a stalled local client (slowloris-style); also satisfies G112.
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	serve := func(l net.Listener) {
		go func() {
			if errServe := srv.Serve(l); errServe != nil && errServe != http.ErrServerClosed {
				log.Debugf("kiro callback listener stopped: %v", errServe)
			}
		}()
	}
	serve(ln4)
	if ln6, err6 := net.Listen("tcp", "[::1]:"+SocialRedirectPort); err6 == nil {
		serve(ln6)
	} else {
		log.Debugf("kiro callback: IPv6 loopback bind skipped: %v", err6)
	}

	cleanup := func() {
		ctxShutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctxShutdown)
	}
	return resultCh, cleanup, nil
}

// writeSocialCallbackPage renders a minimal HTML page shown in the browser after the final
// redirect, telling the user whether to return to the app or that something went wrong.
func writeSocialCallbackPage(w http.ResponseWriter, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	msg := "Kiro sign-in complete. You can close this tab and return to the app."
	if !ok {
		msg = "Kiro sign-in failed. Return to the app and try again."
	}
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>Kiro Sign-In</title></head><body style=\"font-family:sans-serif;padding:2rem\"><p>%s</p></body></html>", msg)
}
