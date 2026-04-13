package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/claude"
	responsefmt "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

const (
	openAICompatClaudeViaGPTRouteClass = "claude_via_openai_compat"
	openAICompatSurfaceResponses       = "responses"
	openAICompatSurfaceChatCompletions = "chat_completions"
)

type openAICompatExecutionPlan struct {
	routeClass       string
	selectedProvider string
	selectedSurface  string
	targetFormat     sdktranslator.Format
	endpoint         string
	originalPayload  []byte
	translated       []byte
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	plan, err := e.buildExecutionPlan(req, opts, auth, false)
	if err != nil {
		return resp, err
	}

	url := strings.TrimSuffix(baseURL, "/") + plan.endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(plan.translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "codex-tui/0.118.0")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:              url,
		Method:           http.MethodPost,
		Headers:          httpReq.Header.Clone(),
		Body:             plan.translated,
		Provider:         e.Identifier(),
		AuthID:           authID,
		AuthLabel:        authLabel,
		AuthType:         authType,
		AuthValue:        authValue,
		RouteClass:       plan.routeClass,
		SelectedProvider: plan.selectedProvider,
		SelectedSurface:  plan.selectedSurface,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
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
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// Translate response back to source format when needed
	out := e.translateResponseNonStream(ctx, plan, from, req.Model, body)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	plan, err := e.buildExecutionPlan(req, opts, auth, true)
	if err != nil {
		return nil, err
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	if plan.targetFormat == sdktranslator.FormatOpenAI {
		plan.translated, _ = sjson.SetBytes(plan.translated, "stream_options.include_usage", true)
	}

	url := strings.TrimSuffix(baseURL, "/") + plan.endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(plan.translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "codex-tui/0.118.0")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:              url,
		Method:           http.MethodPost,
		Headers:          httpReq.Header.Clone(),
		Body:             plan.translated,
		Provider:         e.Identifier(),
		AuthID:           authID,
		AuthLabel:        authLabel,
		AuthType:         authType,
		AuthValue:        authValue,
		RouteClass:       plan.routeClass,
		SelectedProvider: plan.selectedProvider,
		SelectedSurface:  plan.selectedSurface,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}

			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}

			// OpenAI-compatible streams are SSE: lines typically prefixed with "data: ".
			// Pass through translator; it yields one or more chunks for the target schema.
			chunks := e.translateResponseStream(ctx, plan, from, req.Model, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := e.translateResponseStream(ctx, plan, from, req.Model, []byte("data: [DONE]"), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *OpenAICompatExecutor) buildExecutionPlan(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, auth *cliproxyauth.Auth, stream bool) (openAICompatExecutionPlan, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}

	plan := openAICompatExecutionPlan{
		routeClass:       requestRouteClassFromMetadata(opts.Metadata),
		selectedProvider: e.selectedProvider(auth),
		targetFormat:     sdktranslator.FormatOpenAI,
		endpoint:         "/chat/completions",
		originalPayload:  originalPayload,
	}

	if opts.Alt == "responses/compact" {
		originalTranslated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAIResponse, baseModel, originalPayload, stream)
		translated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAIResponse, baseModel, req.Payload, stream)
		translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, sdktranslator.FormatOpenAIResponse.String(), "", translated, originalTranslated, requestedModel)
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
		translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), sdktranslator.FormatOpenAIResponse.String(), e.Identifier())
		if err != nil {
			return openAICompatExecutionPlan{}, err
		}
		translated = e.appendReasoningEffortToModelSuffix(translated, originalPayload, opts.Metadata, auth, sdktranslator.FormatOpenAIResponse, req.Model)
		plan.targetFormat = sdktranslator.FormatOpenAIResponse
		plan.endpoint = "/responses/compact"
		plan.translated = translated
		return plan, nil
	}

	if plan.routeClass == openAICompatClaudeViaGPTRouteClass && from == sdktranslator.FormatClaude {
		return e.buildClaudeViaGPTExecutionPlan(baseModel, from, req, requestedModel, originalPayload, opts.Metadata, auth, stream, plan)
	}

	// When the inbound request is already in OpenAI Responses format, forward
	// to the upstream's /responses endpoint directly instead of converting to
	// chat completions.
	if from == sdktranslator.FormatOpenAIResponse {
		originalTranslated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAIResponse, baseModel, originalPayload, stream)
		translated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAIResponse, baseModel, req.Payload, stream)
		translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, sdktranslator.FormatOpenAIResponse.String(), "", translated, originalTranslated, requestedModel)
		translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), sdktranslator.FormatOpenAIResponse.String(), e.Identifier())
		if err != nil {
			return openAICompatExecutionPlan{}, err
		}
		translated = e.appendReasoningEffortToModelSuffix(translated, originalPayload, opts.Metadata, auth, sdktranslator.FormatOpenAIResponse, req.Model)
		plan.selectedSurface = openAICompatSurfaceResponses
		plan.targetFormat = sdktranslator.FormatOpenAIResponse
		plan.endpoint = "/responses"
		plan.translated = translated
		return plan, nil
	}

	originalTranslated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAI, baseModel, originalPayload, stream)
	translated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAI, baseModel, req.Payload, stream)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, sdktranslator.FormatOpenAI.String(), "", translated, originalTranslated, requestedModel)
	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), sdktranslator.FormatOpenAI.String(), e.Identifier())
	if err != nil {
		return openAICompatExecutionPlan{}, err
	}
	translated = e.appendReasoningEffortToModelSuffix(translated, originalPayload, opts.Metadata, auth, sdktranslator.FormatOpenAI, req.Model)
	plan.translated = translated
	return plan, nil
}

func (e *OpenAICompatExecutor) buildClaudeViaGPTExecutionPlan(baseModel string, from sdktranslator.Format, req cliproxyexecutor.Request, requestedModel string, originalPayload []byte, metadata map[string]any, auth *cliproxyauth.Auth, stream bool, plan openAICompatExecutionPlan) (openAICompatExecutionPlan, error) {
	caps := e.claudeViaGPTCapabilities(auth)

	responsePlan, responseCompatErr, responseErr := e.buildClaudeViaGPTSurfacePlan(baseModel, from, req, requestedModel, originalPayload, metadata, auth, stream, caps, openaiclaude.BackendSurfaceResponses, plan)
	if responseErr != nil {
		return openAICompatExecutionPlan{}, responseErr
	}
	if responseCompatErr == nil {
		return responsePlan, nil
	}

	chatPlan, chatCompatErr, chatErr := e.buildClaudeViaGPTSurfacePlan(baseModel, from, req, requestedModel, originalPayload, metadata, auth, stream, caps, openaiclaude.BackendSurfaceChatCompletions, plan)
	if chatErr != nil {
		return openAICompatExecutionPlan{}, chatErr
	}
	if chatCompatErr == nil {
		return chatPlan, nil
	}

	if chatCompatErr != nil {
		return openAICompatExecutionPlan{}, compatibilityStatusErr(chatCompatErr)
	}
	return openAICompatExecutionPlan{}, compatibilityStatusErr(responseCompatErr)
}

func (e *OpenAICompatExecutor) buildClaudeViaGPTSurfacePlan(baseModel string, from sdktranslator.Format, req cliproxyexecutor.Request, requestedModel string, originalPayload []byte, metadata map[string]any, auth *cliproxyauth.Auth, stream bool, caps openaiclaude.BackendCapabilities, surface openaiclaude.BackendSurface, plan openAICompatExecutionPlan) (openAICompatExecutionPlan, *openaiclaude.CompatibilityError, error) {
	validatedOriginal, compatErr := openaiclaude.ValidateClaudeRequestForSurface(originalPayload, caps, surface)
	if compatErr != nil {
		if typed, ok := compatErr.(*openaiclaude.CompatibilityError); ok {
			return openAICompatExecutionPlan{}, typed, nil
		}
		return openAICompatExecutionPlan{}, nil, compatErr
	}

	currentPayload := req.Payload
	if len(currentPayload) == 0 {
		currentPayload = originalPayload
	}
	validatedPayload, compatErr := openaiclaude.ValidateClaudeRequestForSurface(currentPayload, caps, surface)
	if compatErr != nil {
		if typed, ok := compatErr.(*openaiclaude.CompatibilityError); ok {
			return openAICompatExecutionPlan{}, typed, nil
		}
		return openAICompatExecutionPlan{}, nil, compatErr
	}

	next := plan
	next.originalPayload = validatedOriginal

	switch surface {
	case openaiclaude.BackendSurfaceResponses:
		chatOriginal := openaiclaude.ConvertClaudeRequestToOpenAIWithTools(baseModel, validatedOriginal, stream)
		chatPayload := openaiclaude.ConvertClaudeRequestToOpenAIWithTools(baseModel, validatedPayload, stream)
		originalTranslated := responsefmt.LiftOpenAIChatCompletionsRequestToOpenAIResponses(baseModel, chatOriginal)
		translated := responsefmt.LiftOpenAIChatCompletionsRequestToOpenAIResponses(baseModel, chatPayload)
		translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, sdktranslator.FormatOpenAIResponse.String(), "", translated, originalTranslated, requestedModel)
		translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), sdktranslator.FormatOpenAIResponse.String(), e.Identifier())
		if err != nil {
			return openAICompatExecutionPlan{}, nil, err
		}
		translated = e.appendReasoningEffortToModelSuffix(translated, validatedOriginal, metadata, auth, sdktranslator.FormatOpenAIResponse, req.Model)
		next.selectedSurface = openAICompatSurfaceResponses
		next.targetFormat = sdktranslator.FormatOpenAIResponse
		next.endpoint = "/responses"
		next.translated = translated
		return next, nil, nil
	case openaiclaude.BackendSurfaceChatCompletions:
		originalTranslated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAI, baseModel, validatedOriginal, stream)
		translated := sdktranslator.TranslateRequest(from, sdktranslator.FormatOpenAI, baseModel, validatedPayload, stream)
		translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, sdktranslator.FormatOpenAI.String(), "", translated, originalTranslated, requestedModel)
		translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), sdktranslator.FormatOpenAI.String(), e.Identifier())
		if err != nil {
			return openAICompatExecutionPlan{}, nil, err
		}
		translated = e.appendReasoningEffortToModelSuffix(translated, validatedOriginal, metadata, auth, sdktranslator.FormatOpenAI, req.Model)
		next.selectedSurface = openAICompatSurfaceChatCompletions
		next.targetFormat = sdktranslator.FormatOpenAI
		next.endpoint = "/chat/completions"
		next.translated = translated
		return next, nil, nil
	default:
		return openAICompatExecutionPlan{}, &openaiclaude.CompatibilityError{
			Class:   openaiclaude.CompatibilityClassSurfaceNotSupported,
			Reason:  "unknown_surface",
			Stage:   "executor",
			Surface: surface,
			Message: fmt.Sprintf("Claude-via-GPT routing rejected: unknown backend surface %q.", surface),
		}, nil
	}
}

func (e *OpenAICompatExecutor) translateResponseNonStream(ctx context.Context, plan openAICompatExecutionPlan, from sdktranslator.Format, model string, body []byte) []byte {
	if plan.routeClass == openAICompatClaudeViaGPTRouteClass && from == sdktranslator.FormatClaude {
		switch plan.selectedSurface {
		case openAICompatSurfaceResponses:
			return adaptOpenAIResponsesNonStreamToClaude(ctx, model, plan.originalPayload, plan.translated, body, nil)
		case openAICompatSurfaceChatCompletions:
			return adaptOpenAIChatCompletionsNonStreamToClaude(ctx, model, plan.originalPayload, plan.translated, body, nil)
		}
	}
	var param any
	return sdktranslator.TranslateNonStream(ctx, plan.targetFormat, from, model, plan.originalPayload, plan.translated, body, &param)
}

func (e *OpenAICompatExecutor) translateResponseStream(ctx context.Context, plan openAICompatExecutionPlan, from sdktranslator.Format, model string, line []byte, param *any) [][]byte {
	if plan.routeClass == openAICompatClaudeViaGPTRouteClass && from == sdktranslator.FormatClaude {
		switch plan.selectedSurface {
		case openAICompatSurfaceResponses:
			return adaptOpenAIResponsesStreamChunkToClaude(ctx, model, plan.originalPayload, plan.translated, line, param)
		case openAICompatSurfaceChatCompletions:
			return adaptOpenAIChatCompletionsStreamChunkToClaude(ctx, model, plan.originalPayload, plan.translated, line, param)
		}
	}
	return sdktranslator.TranslateStream(ctx, plan.targetFormat, from, model, plan.originalPayload, plan.translated, line, param)
}

func requestRouteClassFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.RequestRouteMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func (e *OpenAICompatExecutor) selectedProvider(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if provider := strings.TrimSpace(auth.Attributes["provider_key"]); provider != "" {
			return provider
		}
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			return compatName
		}
	}
	if trimmed := strings.TrimSpace(e.Identifier()); trimmed != "" {
		return trimmed
	}
	if auth != nil {
		return strings.TrimSpace(auth.Provider)
	}
	return ""
}

func (e *OpenAICompatExecutor) claudeViaGPTCapabilities(auth *cliproxyauth.Auth) openaiclaude.BackendCapabilities {
	caps := openaiclaude.BackendCapabilities{
		SupportsChatCompletions: true,
		SupportsTools:           true,
		SupportsStreaming:       true,
	}
	if auth == nil || auth.Attributes == nil {
		return caps
	}
	if value, ok := parseOptionalBoolAttr(auth.Attributes, "supports_openai_responses"); ok {
		caps.SupportsOpenAIResponses = value
	}
	if value, ok := parseOptionalBoolAttr(auth.Attributes, "supports_chat_completions"); ok {
		caps.SupportsChatCompletions = value
	}
	if value, ok := parseOptionalBoolAttr(auth.Attributes, "supports_tools"); ok {
		caps.SupportsTools = value
	}
	if value, ok := parseOptionalBoolAttr(auth.Attributes, "supports_streaming"); ok {
		caps.SupportsStreaming = value
	}
	return caps
}

func parseOptionalBoolAttr(attrs map[string]string, key string) (bool, bool) {
	if len(attrs) == 0 {
		return false, false
	}
	value, ok := attrs[key]
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func compatibilityStatusErr(err *openaiclaude.CompatibilityError) error {
	if err == nil {
		return nil
	}
	return statusErr{code: compatibilityStatusCode(err), msg: err.Message}
}

func compatibilityStatusCode(err *openaiclaude.CompatibilityError) int {
	if err == nil {
		return http.StatusBadRequest
	}
	switch err.Class {
	case openaiclaude.CompatibilityClassBackendNotAvailable:
		return http.StatusServiceUnavailable
	case openaiclaude.CompatibilityClassTranslationFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	_ = ctx
	return auth, nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

func (e *OpenAICompatExecutor) appendReasoningEffortToModelSuffix(payload []byte, originalPayload []byte, metadata map[string]any, auth *cliproxyauth.Auth, format sdktranslator.Format, requestedModel string) []byte {
	compat := e.resolveCompatConfig(auth)
	if compat == nil || !compat.AppendReasoningEffortToModel || len(payload) == 0 {
		return payload
	}
	if !shouldAppendReasoningEffortToModel(compat, requestedModel, originalPayload, payload, metadata) {
		return payload
	}
	effort := helps.ExtractReasoningEffortFromRequest(payload, format.String())
	derivedFromModel := false
	if effort == "" {
		effort = reasoningEffortSuffix(requestedModel)
		derivedFromModel = effort != ""
	}
	if effort == "" {
		return payload
	}
	if derivedFromModel {
		payload = ensureReasoningEffortInPayload(payload, format, effort)
	}
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		return payload
	}
	suffix := "-" + effort
	if strings.HasSuffix(model, suffix) {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model+suffix)
	return payload
}

func shouldAppendReasoningEffortToModel(compat *config.OpenAICompatibility, requestedModel string, originalPayload []byte, translatedPayload []byte, metadata map[string]any) bool {
	if compat == nil || !compat.AppendReasoningEffortToModel {
		return false
	}
	percent := 100
	if compat.AppendReasoningEffortToModelPercent != nil {
		percent = *compat.AppendReasoningEffortToModelPercent
	}
	switch {
	case percent <= 0:
		return false
	case percent >= 100:
		return true
	default:
		return openAICompatSamplingBucket(compat.Name, requestedModel, openAICompatSamplingKey(metadata), originalPayload, translatedPayload) < percent
	}
}

// Stable request bucketing keeps retries and equivalent replays on the same side
// of the sampling threshold for a given provider.
func openAICompatSamplingBucket(providerName, requestedModel, samplingKey string, originalPayload []byte, translatedPayload []byte) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(strings.TrimSpace(providerName)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strings.TrimSpace(requestedModel)))
	_, _ = hasher.Write([]byte{0})
	if trimmedKey := strings.TrimSpace(samplingKey); trimmedKey != "" {
		_, _ = hasher.Write([]byte(trimmedKey))
	} else if len(originalPayload) > 0 {
		_, _ = hasher.Write(originalPayload)
	} else {
		_, _ = hasher.Write(translatedPayload)
	}
	return int(hasher.Sum32() % 100)
}

func openAICompatSamplingKey(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range []string{cliproxyexecutor.IdempotencyKeyMetadataKey, cliproxyexecutor.RequestIDMetadataKey} {
		if value, ok := metadata[key]; ok {
			switch typed := value.(type) {
			case string:
				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					return trimmed
				}
			case fmt.Stringer:
				if trimmed := strings.TrimSpace(typed.String()); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func ensureReasoningEffortInPayload(payload []byte, format sdktranslator.Format, effort string) []byte {
	switch format {
	case sdktranslator.FormatOpenAIResponse:
		payload, _ = sjson.SetBytes(payload, "reasoning.effort", effort)
	default:
		payload, _ = sjson.SetBytes(payload, "reasoning_effort", effort)
	}
	return payload
}

func reasoningEffortSuffix(model string) string {
	suffix := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).RawSuffix))
	switch suffix {
	case "low", "medium", "high", "xhigh":
		return suffix
	default:
		return ""
	}
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
