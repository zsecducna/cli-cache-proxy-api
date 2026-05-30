package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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

// kiroCredentials holds the per-auth credentials read from metadata/attributes.
type kiroCredentials struct {
	accessToken  string
	refreshToken string
	profileArn   string
	clientID     string
	clientSecret string
	region       string
	authMethod   string
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
	// The Kiro runtime generate endpoint requires a profileArn. Logins now resolve and
	// persist it, but as a fallback (older credentials, or login-time resolution failure)
	// resolve it here. This writes only the per-request auth clone, so it does not persist
	// across requests — treat it as best-effort recovery, not a cache.
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
	freshToken := ""
	arn, err := svc.ListAvailableProfiles(ctx, creds.accessToken, region)
	if err != nil || arn == "" {
		// Empty list usually means the access token is expired; refresh once and retry.
		td, errRefresh := svc.RefreshToken(ctx, kiroauth.RefreshParams{
			RefreshToken: creds.refreshToken,
			ClientID:     creds.clientID,
			ClientSecret: creds.clientSecret,
			Region:       region,
		})
		if errRefresh != nil {
			log.Warnf("kiro executor: profile resolution refresh failed: %v", errRefresh)
			return "", ""
		}
		freshToken = td.AccessToken
		if arn, err = svc.ListAvailableProfiles(ctx, freshToken, region); err != nil || arn == "" {
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
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

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
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
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
			reporter.Publish(ctx, claudeEnc.UsageDetail())
		} else {
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

	svc := kiroauth.NewKiroAuthWithProxy(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshToken(ctx, kiroauth.RefreshParams{
		RefreshToken: creds.refreshToken,
		ClientID:     creds.clientID,
		ClientSecret: creds.clientSecret,
		Region:       regionForCreds(creds),
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
		if arn, errArn := svc.ListAvailableProfiles(ctx, td.AccessToken, regionForCreds(creds)); errArn == nil && arn != "" {
			auth.Metadata["profile_arn"] = arn
		}
	}
	if td.ExpiresAt > 0 {
		auth.Metadata["expired"] = time.Unix(td.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	auth.Metadata["type"] = "kiro"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	return auth, nil
}
