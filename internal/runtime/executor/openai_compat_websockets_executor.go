package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// OpenAICompatWebsocketsExecutor executes OpenAI-compatible Responses API requests
// using a WebSocket upstream transport. Each request opens a fresh connection (no pooling).
// Non-streaming and non-/responses requests fall back to HTTP via OpenAICompatExecutor.
type OpenAICompatWebsocketsExecutor struct {
	*OpenAICompatExecutor
}

func NewOpenAICompatWebsocketsExecutor(provider string, cfg *config.Config) *OpenAICompatWebsocketsExecutor {
	return &OpenAICompatWebsocketsExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor(provider, cfg),
	}
}

func (e *OpenAICompatWebsocketsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
}

func (e *OpenAICompatWebsocketsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat

	plan, errPlan := e.buildExecutionPlan(req, opts, auth, true)
	if errPlan != nil {
		return nil, errPlan
	}

	// WS upstream only works with the Responses API surface.
	if plan.endpoint != "/responses" {
		return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
	}

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return nil, err
	}

	wsBody := buildCodexWebsocketRequestBody(plan.translated)
	wsHeaders := openAICompatWebsocketHeaders(apiKey, auth)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      wsBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	dialer := newProxyAwareWebsocketDialer(e.cfg, auth)
	dialer.HandshakeTimeout = codexResponsesWebsocketHandshakeTO
	conn, respHS, errDial := dialer.DialContext(ctx, wsURL, wsHeaders)
	if conn != nil {
		conn.EnableWriteCompression(false)
	}
	if errDial != nil {
		bodyErr := websocketHandshakeBody(respHS)
		if respHS != nil {
			helps.RecordAPIWebsocketUpgradeRejection(ctx, e.cfg, websocketUpgradeRequestLog(wsReqLog), respHS.StatusCode, respHS.Header.Clone(), bodyErr)
		}
		if respHS != nil && respHS.StatusCode == http.StatusUpgradeRequired {
			return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
		}
		if respHS != nil && respHS.StatusCode > 0 {
			return nil, statusErr{code: respHS.StatusCode, msg: string(bodyErr)}
		}
		helps.RecordAPIWebsocketError(ctx, e.cfg, "dial", errDial)
		return nil, errDial
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)

	var upstreamHeaders http.Header
	if respHS != nil {
		upstreamHeaders = respHS.Header.Clone()
	}

	if errSend := conn.WriteMessage(websocket.TextMessage, wsBody); errSend != nil {
		helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
		_ = conn.Close()
		return nil, errSend
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("openai compat websockets executor: close websocket error: %v", errClose)
			}
		}()

		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		var param any
		for {
			if ctx.Err() != nil {
				_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
				return
			}

			_ = conn.SetReadDeadline(time.Now().Add(codexResponsesWebsocketIdleTimeout))
			msgType, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read", errRead)
				reporter.PublishFailure(ctx)
				_ = send(cliproxyexecutor.StreamChunk{Err: errRead})
				return
			}

			if msgType != websocket.TextMessage {
				if msgType == websocket.BinaryMessage {
					errBin := fmt.Errorf("openai compat websockets executor: unexpected binary message")
					helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", errBin)
					reporter.PublishFailure(ctx)
					_ = send(cliproxyexecutor.StreamChunk{Err: errBin})
					return
				}
				continue
			}

			payload = bytes.TrimSpace(payload)
			if len(payload) == 0 {
				continue
			}

			if wsErr, ok := parseCodexWebsocketError(payload); ok {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
				reporter.PublishFailure(ctx)
				_ = send(cliproxyexecutor.StreamChunk{Err: wsErr})
				return
			}

			payload = normalizeCodexWebsocketCompletion(payload)
			eventType := gjson.GetBytes(payload, "type").String()

			helps.AppendAPIWebsocketResponse(ctx, e.cfg, payload)

			line := encodeCodexWebsocketAsSSE(payload)
			chunks := e.translateResponseStream(ctx, plan, from, req.Model, line, &param)
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					return
				}
			}

			if eventType == "response.completed" || eventType == "response.done" {
				reporter.EnsurePublished(ctx)
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: upstreamHeaders, Chunks: out}, nil
}

func openAICompatWebsocketHeaders(apiKey string, auth *cliproxyauth.Auth) http.Header {
	headers := http.Header{}
	if strings.TrimSpace(apiKey) != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	headers.Set("OpenAI-Beta", codexResponsesWebsocketBetaHeaderValue)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(&http.Request{Header: headers}, attrs)
	return headers
}

// OpenAICompatAutoExecutor routes OpenAI-compatible requests to the WebSocket upstream
// when ws_upstream is set to true on the auth entry, falling back to HTTP on failures.
// When ws_upstream is not set (the default), all requests go through HTTP.
type OpenAICompatAutoExecutor struct {
	httpExec *OpenAICompatExecutor
	wsExec   *OpenAICompatWebsocketsExecutor
}

func NewOpenAICompatAutoExecutor(provider string, cfg *config.Config) *OpenAICompatAutoExecutor {
	return &OpenAICompatAutoExecutor{
		httpExec: NewOpenAICompatExecutor(provider, cfg),
		wsExec:   NewOpenAICompatWebsocketsExecutor(provider, cfg),
	}
}

func (e *OpenAICompatAutoExecutor) Identifier() string {
	if e == nil || e.httpExec == nil {
		return ""
	}
	return e.httpExec.Identifier()
}

func (e *OpenAICompatAutoExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if e == nil || e.httpExec == nil {
		return nil
	}
	return e.httpExec.PrepareRequest(req, auth)
}

func (e *OpenAICompatAutoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("openai compat auto executor: http executor is nil")
	}
	return e.httpExec.HttpRequest(ctx, auth, req)
}

func (e *OpenAICompatAutoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.httpExec == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat auto executor: http executor is nil")
	}
	return e.httpExec.Execute(ctx, auth, req, opts)
}

func (e *OpenAICompatAutoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return nil, fmt.Errorf("openai compat auto executor: executor is nil")
	}
	// Claude /v1/messages translated onto GPT/OpenAI-compatible providers must
	// default to the HTTP SSE path. That path lets OpenAICompatExecutor choose
	// the Responses surface and translate the upstream SSE stream back to
	// Anthropic SSE without depending on the unstable websocket upstream.
	if isClaudeViaOpenAICompatRoute(opts) {
		return e.httpExec.ExecuteStream(ctx, auth, req, opts)
	}
	if codexPreferWSUpstream(auth) {
		result, err := e.wsExec.ExecuteStream(ctx, auth, req, opts)
		if err == nil || !isWSUpstreamFallbackEligible(err) {
			return result, err
		}
		log.Warnf("openai compat ws_upstream: websocket stream failed, falling back to HTTP: %v", err)
		return e.httpExec.ExecuteStream(ctx, auth, req, opts)
	}
	return e.httpExec.ExecuteStream(ctx, auth, req, opts)
}

func (e *OpenAICompatAutoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("openai compat auto executor: http executor is nil")
	}
	return e.httpExec.Refresh(ctx, auth)
}

func (e *OpenAICompatAutoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.httpExec == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat auto executor: http executor is nil")
	}
	return e.httpExec.CountTokens(ctx, auth, req, opts)
}
