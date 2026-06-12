package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// KiroExecutor is a stateless executor for Amazon Kiro (AWS CodeWhisperer). For Claude
// (Anthropic) clients it translates directly between the Anthropic Messages format and the
// CodeWhisperer conversationState/EventStream in both directions, preserving tools and
// tool_use/tool_result without any OpenAI intermediate. Other client formats (OpenAI,
// Gemini) pivot through the OpenAI schema. All Kiro-specific logic stays out of
// internal/translator and lives in internal/runtime/executor/helps.
type KiroExecutor struct {
	cfg *config.Config
}

// NewKiroExecutor creates a new Kiro executor.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor { return &KiroExecutor{cfg: cfg} }

// Identifier returns the executor identifier.
func (e *KiroExecutor) Identifier() string { return "kiro" }

// ShouldPrepareRequestAuth reports whether the access token must be refreshed before
// the request is sent. Kiro access tokens are short-lived and AWS SSO OIDC rotates them
// on every refresh, immediately invalidating the previous one. The background refresh
// loop alone is insufficient: a request that picked an auth clone before the loop rotated
// the token would send the now-dead token and get a 403 "bearer token ... is invalid".
// Returning true here routes the auth through Manager.prepareRequestAuth, which refreshes
// under a per-auth lock against the latest in-memory state and persists once, closing that
// rotation race. Refresh is only possible (and only worthwhile) when a refresh token exists
// and the access token is missing or within the refresh lead of expiry.
func (e *KiroExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	creds := kiroCreds(auth)
	if strings.TrimSpace(creds.refreshToken) == "" {
		return false // Cannot refresh without a refresh token; send what we have.
	}
	if strings.TrimSpace(creds.accessToken) == "" {
		return true // No usable token at all; must mint one before the request.
	}
	return kiroAccessTokenExpiringSoon(auth)
}

// PrepareRequestAuth refreshes the Kiro access token just before the request when
// ShouldPrepareRequestAuth signaled it is missing or near expiry. It delegates to the
// shared Refresh path so token rotation, profileArn resolution, and metadata updates stay
// in one place. Returning the same auth unchanged when no refresh is needed lets the
// caller proceed with the existing token.
func (e *KiroExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	refreshed, err := e.Refresh(ctx, auth)
	if err != nil {
		// ShouldPrepareRequestAuth fires up to 5 minutes BEFORE expiry, so the current
		// token is often still valid right now. A transient refresh failure (network blip,
		// AWS OIDC 5xx) must not fail the request when we still hold a usable token: fall
		// back to it and let the next attempt or the proactive loop retry the refresh.
		// Only surface the error when there is no usable token left to send (missing or
		// already expired).
		if kiroAccessTokenStillUsable(auth) {
			log.Warnf("kiro executor: request-time refresh failed, falling back to still-valid token: %v", err)
			return auth, nil
		}
		return nil, err
	}
	return refreshed, nil
}

// kiroAccessTokenStillUsable reports whether auth currently holds an access token that can
// still be sent upstream: present, and — when a parseable expiry exists — not yet past it.
// Distinct from kiroAccessTokenExpiringSoon (which fires within the pre-expiry lead): a
// token can be "expiring soon" yet "still usable" in the lead window.
func kiroAccessTokenStillUsable(auth *cliproxyauth.Auth) bool {
	if strings.TrimSpace(kiroCreds(auth).accessToken) == "" {
		return false
	}
	expiry, ok := auth.ExpirationTime()
	if !ok || expiry.IsZero() {
		return true // Present token with no parseable expiry: assume usable.
	}
	return time.Now().Before(expiry)
}

// kiroAccessTokenExpiringSoon reports whether the access token expires within the Kiro
// refresh lead (5 minutes, matching sdk/auth.kiroRefreshLead and the conductor's proactive
// scheduler). A missing or unparseable expiry is treated as "not expiring" here because the
// missing-token case is already handled by ShouldPrepareRequestAuth; we only want the
// time-based trigger to fire on a genuine, parseable near-expiry timestamp.
func kiroAccessTokenExpiringSoon(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	expiry, ok := auth.ExpirationTime()
	if !ok || expiry.IsZero() {
		return false
	}
	return time.Until(expiry) <= kiroRequestRefreshLead
}

// kiroRequestRefreshLead is the pre-expiry window in which a request-time refresh is
// triggered. It matches the proactive loop's lead (sdk/auth.kiroRefreshLead) so the
// request path and the background loop agree on when a token is "near expiry".
const kiroRequestRefreshLead = 5 * time.Minute

// kiroCredentials holds the per-auth credentials read from metadata/attributes.
type kiroCredentials struct {
	accessToken  string
	refreshToken string
	profileArn   string
	clientID     string
	clientSecret string
	region       string
	authMethod   string
	// tokenEndpoint and scopes are set for the external IdP (enterprise SSO) method and
	// select the IdP-token-endpoint refresh path (OAuth2 refresh_token grant).
	tokenEndpoint string
	scopes        string
}

// kiroCreds extracts Kiro credentials from auth.Metadata first then auth.Attributes.
func kiroCreds(a *cliproxyauth.Auth) kiroCredentials {
	creds := kiroCredentials{}
	if a == nil {
		return creds
	}
	get := func(key string) string {
		if a.Metadata != nil {
			if v, ok := a.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		if a.Attributes != nil {
			if v := strings.TrimSpace(a.Attributes[key]); v != "" {
				return v
			}
		}
		return ""
	}
	creds.accessToken = get("access_token")
	creds.refreshToken = get("refresh_token")
	creds.profileArn = get("profile_arn")
	creds.clientID = get("client_id")
	creds.clientSecret = get("client_secret")
	creds.region = get("region")
	creds.authMethod = get("auth_method")
	creds.tokenEndpoint = get("token_endpoint")
	creds.scopes = get("scopes")
	return creds
}

// regionForCreds resolves the AWS region for the generate endpoint, preferring the
// region embedded in the profile ARN, then the stored region, then the default.
func regionForCreds(creds kiroCredentials) string {
	if r := kiroauth.RegionFromProfileArn(creds.profileArn); r != "" {
		return r
	}
	if strings.TrimSpace(creds.region) != "" {
		return creds.region
	}
	return kiroauth.DefaultRegion
}

// applyKiroHeaders sets the headers CodeWhisperer requires, including the Kiro-IDE-style
// User-Agent (CodeWhisperer rejects requests that do not look like the Kiro IDE).
func applyKiroHeaders(r *http.Request, creds kiroCredentials) {
	machineID := kiroauth.BuildMachineID(creds.clientID, creds.refreshToken, creds.profileArn, creds.accessToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/vnd.amazon.eventstream")
	r.Header.Set("Authorization", "Bearer "+creds.accessToken)
	r.Header.Set("X-Amz-Target", kiroauth.GenerateTarget)
	r.Header.Set("amz-sdk-invocation-id", uuid.NewString())
	r.Header.Set("amz-sdk-request", "attempt=1; max=1")
	r.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	r.Header.Set("x-amzn-codewhisperer-optout", "true")
	r.Header.Set("User-Agent", kiroauth.BuildUserAgent(machineID))
	r.Header.Set("x-amz-user-agent", kiroauth.BuildXAmzUserAgent(machineID))
	// External IdP (enterprise SSO) tokens must declare their type or CodeWhisperer rejects
	// the generate call; the Kiro IDE adds the same header for external_idp credentials.
	if creds.authMethod == "external_idp" {
		r.Header.Set("TokenType", "EXTERNAL_IDP")
	}
}

// PrepareRequest injects the bearer token and custom headers for generic HTTP paths.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	if token := kiroCreds(auth).accessToken; strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects credentials and executes the request with no post-connect timeout.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// kiroRequest holds the artifacts produced while preparing an upstream call.
type kiroRequest struct {
	url          string
	kiroBody     []byte
	openaiBody   []byte
	from         sdktranslator.Format
	to           sdktranslator.Format
	creds        kiroCredentials
	nameRestore  map[string]string
	promptTokens int
}

// prepareWithAuth builds the CodeWhisperer payload and resolves the endpoint URL. For
// Claude-format clients it translates the Anthropic request directly into the Kiro payload
// (preserving tools/tool_use/tool_result); other client formats pivot through OpenAI.
func (e *KiroExecutor) prepareWithAuth(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (kiroRequest, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	// Decode model variant suffixes (upstream id, agentic mode, thinking variant).
	upstreamModel, agentic, thinkingSuffix := kiroauth.ResolveKiroModel(baseModel)

	creds := kiroCreds(auth)
	// The Kiro runtime generate endpoint requires a profileArn (true for every method,
	// including external_idp — confirmed by the upstream 400 "profileArn is required").
	// Logins resolve and persist it (via ListAvailableProfiles or an operator-supplied
	// override); as a fallback (older credentials, or login-time resolution failure) resolve
	// it here. This writes only the per-request auth clone, so it does not persist across
	// requests — treat it as best-effort recovery, not a cache.
	if creds.profileArn == "" {
		arn, freshToken := e.resolveKiroProfileArn(ctx, auth, creds)
		if arn != "" {
			creds.profileArn = arn
		}
		// Use the refreshed token (if any) for the generate request so it isn't sent with
		// an expired token.
		if freshToken != "" {
			creds.accessToken = freshToken
		}
	}
	// Stop locally when the credential still has no profileArn: forwarding the request
	// only produces an opaque upstream 400 and leaves the operator with no repair hint.
	if errProfile := kiroauth.RequireProfileArn(creds.profileArn, "kiro request"); errProfile != nil {
		return kiroRequest{}, errProfile
	}

	var openaiBody, kiroBody []byte
	var nameRestore map[string]string
	var promptTokens int
	var err error

	if from == sdktranslator.FromString("claude") {
		// Claude clients target real Claude models, so build the Kiro payload DIRECTLY from
		// the Anthropic body: the chat-safe Claude->OpenAI translation drops tool declarations
		// and tool_use/tool_result turns, which silently breaks Claude Code's agent loop.
		// openaiBody is still produced (chat-safe) purely as context for translating the
		// decoded response back to Claude.
		claudeBody := bytes.Clone(req.Payload)
		thinkingEnabled := thinkingSuffix
		budget := kiroauth.DefaultThinkingBudget
		if gjson.GetBytes(claudeBody, "thinking.type").String() == "enabled" {
			thinkingEnabled = true
			if b := gjson.GetBytes(claudeBody, "thinking.budget_tokens"); b.Exists() && b.Int() > 0 {
				budget = int(b.Int())
			}
		}
		if kiroBody, err = helps.BuildKiroPayloadFromClaude(claudeBody, upstreamModel, creds.profileArn, agentic, thinkingEnabled, budget); err != nil {
			return kiroRequest{}, err
		}
		nameRestore = helps.KiroToolNameRestoreMap(claudeBody, true)
		promptTokens = helps.EstimateClaudePromptTokens(claudeBody)
	} else {
		// Non-Claude clients (OpenAI/Gemini) pivot through the OpenAI schema.
		originalPayloadSource := req.Payload
		if len(opts.OriginalRequest) > 0 {
			originalPayloadSource = opts.OriginalRequest
		}
		originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(originalPayloadSource), stream)
		openaiBody = sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)

		// Apply the canonical thinking pipeline; the Kiro applier writes a thinking object
		// the builder reads to decide whether to inject the <thinking_mode> prefix.
		if openaiBody, err = thinking.ApplyThinking(openaiBody, req.Model, from.String(), "kiro", e.Identifier()); err != nil {
			return kiroRequest{}, err
		}
		requestedModel := helps.PayloadRequestedModel(opts, req.Model)
		requestPath := helps.PayloadRequestPath(opts)
		openaiBody = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", openaiBody, originalTranslated, requestedModel, requestPath)

		thinkingEnabled := thinkingSuffix
		budget := kiroauth.DefaultThinkingBudget
		if gjson.GetBytes(openaiBody, "thinking.type").String() == "enabled" {
			thinkingEnabled = true
			if b := gjson.GetBytes(openaiBody, "thinking.budget_tokens"); b.Exists() && b.Int() > 0 {
				budget = int(b.Int())
			}
		}
		if kiroBody, err = helps.BuildKiroPayload(openaiBody, upstreamModel, creds.profileArn, agentic, thinkingEnabled, budget); err != nil {
			return kiroRequest{}, err
		}
		nameRestore = helps.KiroToolNameRestoreMap(openaiBody, false)
		promptTokens = helps.EstimateOpenAIPromptTokens(openaiBody)
	}

	return kiroRequest{
		url:          kiroauth.GenerateEndpoint(regionForCreds(creds)),
		kiroBody:     kiroBody,
		openaiBody:   openaiBody,
		from:         from,
		to:           to,
		creds:        creds,
		nameRestore:  nameRestore,
		promptTokens: promptTokens,
	}, nil
}

// resolveKiroProfileArn resolves the credential's CodeWhisperer profileArn (required by the
// runtime generate endpoint). CodeWhisperer returns an EMPTY profile list for an expired
// access token, so on an empty/failed result it refreshes the token once and retries; the
// refreshed access token (if any) is returned so the caller can use it for the generate
// request too. The arn is written to auth.Metadata, but since the executor receives a
// per-request clone this does NOT persist across requests. Returns (arn, freshAccessToken);
// either may be empty.
func (e *KiroExecutor) resolveKiroProfileArn(ctx context.Context, auth *cliproxyauth.Auth, creds kiroCredentials) (string, string) {
	proxyURL := ""
	if auth != nil {
		proxyURL = auth.ProxyURL
	}
	svc := kiroauth.NewKiroAuthWithProxy(e.cfg, proxyURL)
	region := regionForCreds(creds)
	extIdp := creds.authMethod == "external_idp"
	freshToken := ""
	arn, err := svc.ListAvailableProfiles(ctx, creds.accessToken, region, extIdp)
	if err != nil || arn == "" {
		// Empty list usually means the access token is expired; refresh once and retry.
		td, errRefresh := svc.RefreshToken(ctx, kiroauth.RefreshParams{
			RefreshToken:  creds.refreshToken,
			ClientID:      creds.clientID,
			ClientSecret:  creds.clientSecret,
			Region:        region,
			TokenEndpoint: creds.tokenEndpoint,
			Scopes:        creds.scopes,
		})
		if errRefresh != nil {
			log.Warnf("kiro executor: profile resolution refresh failed: %v", errRefresh)
			return "", ""
		}
		freshToken = td.AccessToken
		if arn, err = svc.ListAvailableProfiles(ctx, freshToken, region, extIdp); err != nil || arn == "" {
			log.Warnf("kiro executor: profile resolution failed after refresh: %v", err)
			return "", freshToken
		}
	}
	if auth != nil {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["profile_arn"] = arn
	}
	return arn, freshToken
}

// kiroRateLimitCooldownFloor is the minimum cooldown applied to a Kiro auth file
// after a 429. Kiro's "SERVICE_REQUEST_RATE_EXCEEDED" body carries no retry hint,
// so without a floor the conductor's quota backoff would start at 1s and the
// fill-first selector would ping-pong across files at 1s granularity. A 5s floor
// gives the rate-limited credential time to recover before it is reselected.
const kiroRateLimitCooldownFloor = 5 * time.Second

// kiroRateLimitCooldownMax caps the cooldown derived from an upstream hint. A
// malformed or far-future Retry-After HTTP-date would otherwise suspend the
// credential for the full bogus duration (MarkResult stores now+retryAfter with
// no ceiling). Five minutes is well beyond any legitimate rate-limit window.
const kiroRateLimitCooldownMax = 5 * time.Minute

// kiroRetryAfter derives the cooldown to attach to a Kiro 429 error. It prefers an
// explicit upstream hint (Retry-After / Retry-After-Ms headers, or a body retryDelay),
// then enforces kiroRateLimitCooldownFloor so a hint-less rate-limit still yields a
// calm cooldown. It returns nil for any non-429 status so other failures fall through
// to the conductor's default per-status handling unchanged.
func kiroRetryAfter(status int, header http.Header, body []byte) *time.Duration {
	if status != http.StatusTooManyRequests {
		return nil
	}

	// Largest of any explicit upstream hint wins; default 0 means "no hint".
	var hint time.Duration

	// Retry-After header: integer seconds or an HTTP-date.
	if header != nil {
		if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
			if secs, errConv := strconv.Atoi(raw); errConv == nil && secs > 0 {
				hint = time.Duration(secs) * time.Second
			} else if when, errParse := http.ParseTime(raw); errParse == nil {
				if d := time.Until(when); d > hint {
					hint = d
				}
			}
		}
		// Retry-After-Ms header: integer milliseconds (Anthropic-style).
		if raw := strings.TrimSpace(header.Get("Retry-After-Ms")); raw != "" {
			if ms, errConv := strconv.Atoi(raw); errConv == nil && ms > 0 {
				if d := time.Duration(ms) * time.Millisecond; d > hint {
					hint = d
				}
			}
		}
	}

	// Body retryDelay (e.g. "0.847655010s" or "30s"), if the upstream ever sends one.
	if len(body) > 0 {
		if raw := strings.TrimSpace(gjson.GetBytes(body, "retryDelay").String()); raw != "" {
			if d, errDur := time.ParseDuration(raw); errDur == nil && d > hint {
				hint = d
			}
		}
	}

	// Enforce the floor: a hint shorter than the floor (or no hint at all) is raised.
	if hint < kiroRateLimitCooldownFloor {
		hint = kiroRateLimitCooldownFloor
	}
	// Cap a bogus/far-future upstream hint so it cannot suspend the credential indefinitely.
	if hint > kiroRateLimitCooldownMax {
		hint = kiroRateLimitCooldownMax
	}
	return &hint
}

// Execute performs a non-streaming generate request and assembles a single response.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prep, err := e.prepareWithAuth(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}

	httpResp, err := e.dispatch(ctx, auth, prep)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b), retryAfter: kiroRetryAfter(httpResp.StatusCode, httpResp.Header, b)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	// Record Kiro's credit/context-usage cache signal (Kiro reports no token-level cache
	// counts; reduced credits on a repeated prompt is the observable cache hit).
	helps.SetKiroCacheObservability(ctx, helps.ParseKiroCacheObservability(data))

	promptTokens := prep.promptTokens
	if prep.from == sdktranslator.FromString("claude") {
		// Direct Kiro EventStream -> Anthropic Messages (no OpenAI intermediate).
		claudeResp := helps.AssembleKiroClaudeResponse(req.Model, data, promptTokens, prep.nameRestore)
		reporter.Publish(ctx, helps.ParseOpenAIUsage(claudeResp))
		resp = cliproxyexecutor.Response{Payload: claudeResp, Headers: httpResp.Header.Clone()}
		return resp, nil
	}

	// Non-Claude clients: decode the binary EventStream into one OpenAI response, then translate.
	openaiResp := helps.AssembleKiroOpenAIResponse(req.Model, data, promptTokens, prep.nameRestore)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(openaiResp))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, prep.to, prep.from, req.Model, opts.OriginalRequest, prep.openaiBody, openaiResp, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming generate request, decoding EventStream frames into
// OpenAI SSE lines and translating each into the client's streaming format.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prep, err := e.prepareWithAuth(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}

	httpResp, err := e.dispatch(ctx, auth, prep)
	if err != nil {
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b), retryAfter: kiroRetryAfter(httpResp.StatusCode, httpResp.Header, b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("kiro executor: close response body error: %v", errClose)
			}
		}()

		promptTokens := prep.promptTokens
		isClaude := prep.from == sdktranslator.FromString("claude")
		decoder := helps.NewKiroEventStreamDecoder(req.Model, promptTokens, prep.nameRestore)
		claudeEnc := helps.NewKiroClaudeStreamEncoder(req.Model, promptTokens, prep.nameRestore)
		var param any
		// send forwards an already client-format SSE line (used by the direct Claude path).
		send := func(line []byte) bool {
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			return helps.SendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: line})
		}
		// emit translates a decoded OpenAI SSE line into client chunks (non-Claude clients).
		emit := func(line []byte) bool {
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, prep.to, prep.from, req.Model, opts.OriginalRequest, prep.openaiBody, bytes.Clone(line), &param)
			for i := range chunks {
				if !helps.SendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					return false
				}
			}
			return true
		}
		// process converts a batch of raw upstream bytes into client chunks.
		process := func(b []byte) bool {
			if isClaude {
				for _, line := range claudeEnc.Encode(b) {
					if !send(line) {
						return false
					}
				}
				return true
			}
			for _, line := range decoder.Decode(b) {
				if !emit(line) {
					return false
				}
			}
			return true
		}

		// Read raw binary frames. No timeout is applied after the connection is open.
		readBuf := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(readBuf)
			if n > 0 {
				if !process(readBuf[:n]) {
					return
				}
			}
			if errRead != nil {
				if errRead == io.EOF {
					break
				}
				helps.RecordAPIResponseError(ctx, e.cfg, errRead)
				reporter.PublishFailure(ctx)
				helps.SendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: errRead})
				return
			}
		}

		if isClaude {
			// Emit terminal Anthropic events directly; Claude streams end on message_stop (no [DONE]).
			for _, line := range claudeEnc.Finish() {
				if !send(line) {
					return
				}
			}
			// Record the Kiro cache signal BEFORE publishing usage: the usage record is handed
			// to a separate dispatcher goroutine that reads this gin-context value, so it must be
			// set first or the persisted row would race to 0/0 (mirrors the claude executor order).
			helps.SetKiroCacheObservability(ctx, claudeEnc.CacheObservability())
			reporter.Publish(ctx, claudeEnc.UsageDetail())
		} else {
			// Record the Kiro cache signal before the finish loop: that loop's usage chunk
			// triggers reporter.Publish (via emit), which the dispatcher goroutine consumes and
			// reads this value from, so it must be set first to avoid a race to 0/0.
			helps.SetKiroCacheObservability(ctx, decoder.CacheObservability())
			// Emit terminal finish/usage chunks, then the SSE [DONE] marker.
			for _, line := range decoder.Finish() {
				if !emit(line) {
					return
				}
			}
			doneChunks := sdktranslator.TranslateStream(ctx, prep.to, prep.from, req.Model, opts.OriginalRequest, prep.openaiBody, []byte("data: [DONE]"), &param)
			for i := range doneChunks {
				if !helps.SendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}) {
					return
				}
			}
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// dispatch builds the HTTP request with Kiro headers, records it, and sends it with no
// post-connection timeout.
func (e *KiroExecutor) dispatch(ctx context.Context, auth *cliproxyauth.Auth, prep kiroRequest) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, prep.url, bytes.NewReader(prep.kiroBody))
	if err != nil {
		return nil, err
	}
	applyKiroHeaders(httpReq, prep.creds)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       prep.url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      prep.kiroBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpResp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	return httpResp, nil
}

// CountTokens returns a best-effort token estimate; Kiro exposes no count endpoint.
func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	openaiBody := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	// Heuristic estimate: roughly four characters per token across message content.
	chars := 0
	gjson.GetBytes(openaiBody, "messages").ForEach(func(_, msg gjson.Result) bool {
		chars += len(msg.Get("content").String())
		return true
	})
	count := int64(chars / 4)
	out := sdktranslator.TranslateTokenCount(ctx, to, from, count, openaiBody)
	return cliproxyexecutor.Response{Payload: out}, nil
}

// Refresh refreshes the Kiro access token using the stored refresh token.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("kiro executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}
	creds := kiroCreds(auth)
	if strings.TrimSpace(creds.refreshToken) == "" {
		return auth, nil // Nothing to refresh.
	}
	// Fail fast rather than mis-route an external IdP refresh token to the Cognito social
	// endpoint: RefreshToken selects the IdP branch by token_endpoint, so an external_idp
	// credential without one would silently hit the wrong endpoint and fail confusingly.
	if creds.authMethod == "external_idp" && strings.TrimSpace(creds.tokenEndpoint) == "" {
		return nil, fmt.Errorf("kiro executor: external_idp credential is missing token_endpoint; re-run Kiro SSO login")
	}

	svc := kiroauth.NewKiroAuthWithProxy(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshToken(ctx, kiroauth.RefreshParams{
		RefreshToken:  creds.refreshToken,
		ClientID:      creds.clientID,
		ClientSecret:  creds.clientSecret,
		Region:        regionForCreds(creds),
		TokenEndpoint: creds.tokenEndpoint,
		Scopes:        creds.scopes,
	})
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.ProfileArn != "" {
		auth.Metadata["profile_arn"] = td.ProfileArn
	} else if strings.TrimSpace(creds.profileArn) == "" {
		// The OIDC refresh does not return a profileArn; resolve it now with the fresh
		// token (the runtime generate endpoint requires it) and persist it so it survives
		// restarts and avoids per-request resolution.
		if arn, errArn := svc.ListAvailableProfiles(ctx, td.AccessToken, regionForCreds(creds), creds.authMethod == "external_idp"); errArn == nil && arn != "" {
			auth.Metadata["profile_arn"] = arn
		}
	}
	// Always persist the freshly minted token, including its new expiry. The token
	// refresh itself SUCCEEDED above; AWS SSO OIDC has already rotated and invalidated the
	// previous access token, so the old one in metadata is dead. Discarding this result on a
	// profileArn-resolution miss (RequireProfileArn returning an error) would leave the dead
	// token in place and guarantee a 403 "bearer token ... is invalid" on the next request —
	// exactly the failure this path is meant to prevent. A missing profileArn is separately
	// recoverable at request time (resolveKiroProfileArn), so we keep the token and only warn.
	if td.ExpiresAt > 0 {
		auth.Metadata["expired"] = time.Unix(td.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	auth.Metadata["type"] = "kiro"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	if errProfile := kiroauth.RequireProfileArn(strings.TrimSpace(metaStringValue(auth.Metadata, "profile_arn")), "kiro refresh"); errProfile != nil {
		log.Warnf("kiro executor: refreshed token but profileArn unresolved (will retry at request time): %v", errProfile)
	}
	return auth, nil
}
