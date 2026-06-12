package kiro

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// social.go implements the Kiro "social" sign-in flow — the same browser flow the Kiro
// IDE uses. It is a PKCE (RFC 7636) authorization-code flow against the Kiro-hosted
// portal, which federates Google, GitHub, and enterprise identity providers (e.g. an
// Azure AD tenant) into a single Kiro account. The portal redirects the authorization
// code to a fixed loopback URI; a transient listener captures it and the code is then
// exchanged for Kiro tokens at the Cognito-backed token endpoint.

// SocialPKCE holds a PKCE verifier/challenge pair plus the anti-CSRF state token used
// for one social sign-in attempt.
type SocialPKCE struct {
	// Verifier is the high-entropy secret retained by the proxy and replayed at the token
	// exchange to prove it initiated the authorization request.
	Verifier string
	// Challenge is the S256 hash of Verifier sent on the sign-in URL.
	Challenge string
	// State is an opaque random token echoed back on the redirect; it guards against
	// cross-session/CSRF code injection into the loopback listener.
	State string
}

// GenerateSocialPKCE creates a PKCE verifier, its S256 challenge, and a random state
// token for one Kiro social sign-in attempt.
func GenerateSocialPKCE() (*SocialPKCE, error) {
	// 96 random bytes -> 128 base64url chars, within the RFC 7636 43..128 verifier range.
	verifier, err := randomURLSafe(96)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to generate code verifier: %w", err)
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("kiro: failed to generate state: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return &SocialPKCE{Verifier: verifier, Challenge: challenge, State: state}, nil
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
// redirect URI and client tag are fixed values the portal validates, so they are not
// configurable here.
func SocialSignInURL(pkce *SocialPKCE) string {
	q := url.Values{}
	q.Set("state", pkce.State)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("redirect_uri", SocialRedirectURI)
	q.Set("redirect_from", SocialRedirectFrom)
	return SocialSignInBaseURL + "?" + q.Encode()
}

// socialTokenEndpoint resolves the Cognito-backed social token-exchange endpoint,
// honoring a test override when present.
func (k *KiroAuth) socialTokenEndpoint() string {
	if strings.TrimSpace(k.socialTokenURL) != "" {
		return k.socialTokenURL
	}
	return socialAuthBase + "/oauth/token"
}

// ExchangeSocialCode exchanges an authorization code (with its PKCE verifier) for Kiro
// tokens at the social token endpoint. The request body matches the Kiro IDE client
// exactly — {code, code_verifier, redirect_uri} — and the response mirrors the social
// refresh response (camelCase accessToken/refreshToken/profileArn/expiresIn).
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

// SocialCallbackResult is delivered once the loopback listener observes a redirect from
// the Kiro portal (or an authorization error).
type SocialCallbackResult struct {
	// Code is the authorization code to exchange (empty when Err is set).
	Code string
	// State is the state value echoed back on the redirect.
	State string
	// Err is non-nil when the portal returned an error or the state did not match.
	Err error
}

// StartSocialCallbackListener binds transient HTTP listeners on the fixed loopback
// redirect port and returns a channel that yields the authorization code once the Kiro
// portal redirects the browser to it. It binds IPv4 (127.0.0.1) and, best-effort, IPv6
// (::1): the portal's redirect URI uses the literal host "localhost", which a browser on
// a dual-stack host may resolve to either family, so binding only one would let the
// callback hit a closed socket and silently hang until the timeout. The returned cleanup
// func MUST be called by the caller to release the port(s).
//
// Binding the IPv4 loopback fails with a clear error when the port is already in use
// (e.g. another login in progress, or a local proxy such as Squid on 3128). The port is
// fixed because the Kiro portal only accepts SocialRedirectURI as the redirect target.
//
// expectedState guards against cross-session/CSRF code injection: the listener is on a
// fixed, predictable port, so any local process or web page could fire a request at it.
// A callback whose state does not match expectedState is ignored WITHOUT resolving the
// attempt — only a state-matched redirect (or the timeout) completes the login — so a
// forged or mistimed request can neither inject a code nor abort (DoS) the real flow.
func StartSocialCallbackListener(expectedState string) (<-chan SocialCallbackResult, func(), error) {
	// IPv4 loopback is required; without it the callback cannot be received at all.
	ln4, err := net.Listen("tcp", "127.0.0.1:"+SocialRedirectPort)
	if err != nil {
		return nil, nil, fmt.Errorf("kiro: cannot bind loopback 127.0.0.1:%s for the SSO callback (is the port already in use?): %w", SocialRedirectPort, err)
	}

	resultCh := make(chan SocialCallbackResult, 1)
	var once sync.Once
	deliver := func(r SocialCallbackResult) { once.Do(func() { resultCh <- r }) }

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		// Only a browser GET redirect is expected; reject other methods to shrink the
		// local attack surface.
		if req.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := req.URL.Query()
		code := strings.TrimSpace(q.Get("code"))
		errParam := strings.TrimSpace(q.Get("error"))
		state := strings.TrimSpace(q.Get("state"))

		// Ignore stray hits with neither a code nor an error (favicon, probes), and any
		// callback whose state does not match — WITHOUT consuming the one-shot, so a forged
		// or mistimed request cannot abort the legitimate login. Only a state-matched
		// redirect (or the caller's timeout) resolves the attempt.
		if code == "" && errParam == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if expectedState == "" || state != expectedState {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// State matches: this is the genuine redirect. Surface a portal error (e.g.
		// access_denied) or the authorization code.
		if errParam != "" {
			desc := strings.TrimSpace(q.Get("error_description"))
			writeSocialCallbackPage(w, false)
			deliver(SocialCallbackResult{Err: fmt.Errorf("kiro: SSO authorization error: %s %s", errParam, desc)})
			return
		}
		writeSocialCallbackPage(w, true)
		deliver(SocialCallbackResult{Code: code, State: state})
	})

	// ReadHeaderTimeout bounds a stalled local client that opens a connection but never
	// finishes sending the request header (slowloris-style); also satisfies gosec G112.
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// Serve on every bound loopback listener; Shutdown later closes all of them since the
	// Server tracks each listener passed to Serve.
	serve := func(l net.Listener) {
		go func() {
			if errServe := srv.Serve(l); errServe != nil && errServe != http.ErrServerClosed {
				log.Debugf("kiro social callback listener stopped: %v", errServe)
			}
		}()
	}
	serve(ln4)
	// Best-effort IPv6 loopback. Failure (no IPv6 stack, or already taken) is non-fatal.
	if ln6, err6 := net.Listen("tcp", "[::1]:"+SocialRedirectPort); err6 == nil {
		serve(ln6)
	} else {
		log.Debugf("kiro social callback: IPv6 loopback bind skipped: %v", err6)
	}

	cleanup := func() {
		ctxShutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctxShutdown)
	}
	return resultCh, cleanup, nil
}

// writeSocialCallbackPage renders a minimal HTML page shown in the user's browser after
// the redirect, telling them whether to return to the app or that something went wrong.
func writeSocialCallbackPage(w http.ResponseWriter, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	msg := "Kiro SSO sign-in complete. You can close this tab and return to the app."
	if !ok {
		msg = "Kiro SSO sign-in failed. Return to the app and try again."
	}
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>Kiro SSO</title></head><body style=\"font-family:sans-serif;padding:2rem\"><p>%s</p></body></html>", msg)
}
