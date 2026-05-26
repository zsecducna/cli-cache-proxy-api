package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	wsRequestTypeCreate  = "response.create"
	wsRequestTypeAppend  = "response.append"
	wsEventTypeError     = "error"
	wsEventTypeCompleted = "response.completed"
	wsDoneMarker         = "[DONE]"
	wsTurnStateHeader    = "x-codex-turn-state"
	wsTimelineBodyKey    = "WEBSOCKET_TIMELINE_OVERRIDE"
	maxTimelineLogSize   = 4 << 20 // 4 MiB cap to prevent unbounded growth on long-lived connections
)

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type websocketTimelineAppender interface {
	Append(eventType string, payload []byte, timestamp time.Time)
}

type websocketTimelineLog struct {
	enabled bool
	source  *requestlogging.FileBodySource
	builder *strings.Builder

	currentPart       io.WriteCloser
	currentPartHasLog bool
}

func newWebsocketTimelineLog(enabled bool, source *requestlogging.FileBodySource) *websocketTimelineLog {
	if !enabled {
		return &websocketTimelineLog{}
	}
	if source == nil {
		return newInMemoryWebsocketTimelineLog()
	}
	return &websocketTimelineLog{
		enabled: true,
		source:  source,
	}
}

func newInMemoryWebsocketTimelineLog() *websocketTimelineLog {
	return &websocketTimelineLog{
		enabled: true,
		builder: &strings.Builder{},
	}
}

func websocketTimelineSourceFromContext(c *gin.Context) *requestlogging.FileBodySource {
	if c == nil {
		return nil
	}
	value, exists := c.Get(requestlogging.WebsocketTimelineSourceContextKey)
	if !exists {
		return nil
	}
	source, ok := value.(*requestlogging.FileBodySource)
	if !ok {
		return nil
	}
	return source
}

func (l *websocketTimelineLog) BeginRequest() {
	if l == nil || !l.enabled || l.source == nil {
		return
	}
	l.closeCurrentPart()
	part, errCreate := l.source.CreatePart("request")
	if errCreate != nil {
		log.WithError(errCreate).Warn("failed to create websocket request detail log")
		return
	}
	l.currentPart = part
	l.currentPartHasLog = false
}

func (l *websocketTimelineLog) Append(eventType string, payload []byte, timestamp time.Time) {
	if l == nil || !l.enabled {
		return
	}
	data := formatWebsocketTimelineEvent(eventType, payload, timestamp)
	if len(data) == 0 {
		return
	}
	if l.source != nil {
		if l.currentPart == nil {
			l.BeginRequest()
		}
		if l.currentPart == nil {
			return
		}
		if errWrite := writeWebsocketTimelinePart(l.currentPart, data, l.currentPartHasLog); errWrite != nil {
			log.WithError(errWrite).Warn("failed to write websocket request detail log")
			return
		}
		l.currentPartHasLog = true
		return
	}
	if l.builder != nil {
		writeWebsocketTimelineBuilder(l.builder, data)
	}
}

func (l *websocketTimelineLog) SetContext(c *gin.Context) {
	if l == nil || !l.enabled {
		return
	}
	l.closeCurrentPart()
	if l.source != nil {
		if l.source.HasPayload() {
			c.Set(requestlogging.WebsocketTimelineSourceContextKey, l.source)
			return
		}
		if errCleanup := l.source.Cleanup(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up empty websocket timeline log parts")
		}
	}
	if l.builder != nil {
		setWebsocketTimelineBody(c, l.builder.String())
	}
}

func (l *websocketTimelineLog) String() string {
	if l == nil || !l.enabled {
		return ""
	}
	l.closeCurrentPart()
	if l.source != nil {
		data, errRead := l.source.Bytes()
		if errRead != nil {
			return ""
		}
		return string(data)
	}
	if l.builder == nil {
		return ""
	}
	return l.builder.String()
}

func (l *websocketTimelineLog) closeCurrentPart() {
	if l == nil || l.currentPart == nil {
		return
	}
	if errClose := l.currentPart.Close(); errClose != nil {
		log.WithError(errClose).Warn("failed to close websocket request detail log")
	}
	l.currentPart = nil
	l.currentPartHasLog = false
}

func writeWebsocketTimelinePart(w io.Writer, data []byte, prependNewline bool) error {
	if w == nil || len(data) == 0 {
		return nil
	}
	if prependNewline {
		if _, errWrite := io.WriteString(w, "\n"); errWrite != nil {
			return errWrite
		}
	}
	_, errWrite := w.Write(data)
	return errWrite
}

func writeWebsocketTimelineBuilder(builder *strings.Builder, data []byte) {
	if builder == nil || len(data) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.Write(data)
}

// ResponsesWebsocket handles websocket requests for /v1/responses.
// It accepts `response.create` and `response.append` requests and streams
// response events back as JSON websocket text messages.
func (h *OpenAIResponsesAPIHandler) ResponsesWebsocket(c *gin.Context) {
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, websocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	passthroughSessionID := uuid.NewString()
	downstreamSessionKey := websocketDownstreamSessionKey(c.Request)
	retainResponsesWebsocketToolCaches(downstreamSessionKey)
	clientIP := websocketClientAddress(c)
	log.Infof("responses websocket: client connected id=%s remote=%s", passthroughSessionID, clientIP)

	requestLogEnabled := h != nil && h.Cfg != nil && h.Cfg.RequestLog && h.Cfg.Streaming.WebsocketTimelineLog
	wsTimelineLog := newWebsocketTimelineLog(requestLogEnabled, websocketTimelineSourceFromContext(c))

	// Register the optional Codex upstream-disconnect hook without mirroring it
	// into an automatic downstream close. Recoverable auth rotation must keep
	// the client websocket alive long enough to replay the failed turn.
	if h != nil && h.AuthManager != nil {
		if exec, ok := h.AuthManager.Executor("codex"); ok && exec != nil {
			type upstreamDisconnectSubscriber interface {
				UpstreamDisconnectChan(sessionID string) <-chan error
			}
			if subscriber, ok := exec.(upstreamDisconnectSubscriber); ok && subscriber != nil {
				_ = subscriber.UpstreamDisconnectChan(passthroughSessionID)
			}
		}
	}

	var wsTerminateErr error
	defer func() {
		releaseResponsesWebsocketToolCaches(downstreamSessionKey)
		if wsTerminateErr != nil {
			appendWebsocketTimelineDisconnect(wsTimelineLog, wsTerminateErr, time.Now())
		} else {
			log.Infof("responses websocket: session closing id=%s", passthroughSessionID)
		}
		if h != nil && h.AuthManager != nil {
			h.AuthManager.CloseExecutionSession(passthroughSessionID)
			log.Infof("responses websocket: upstream execution session closed id=%s", passthroughSessionID)
		}
		wsTimelineLog.SetContext(c)
		if errClose := conn.Close(); errClose != nil {
			log.Warnf("responses websocket: close connection error: %v", errClose)
		}
	}()

	var lastRequest []byte
	lastResponseOutput := []byte("[]")
	pinnedAuthID := ""
	sessionState := newResponsesWebsocketSessionState()
	sessionAuthByID := func(authID string) (*coreauth.Auth, bool) {
		if h == nil || h.AuthManager == nil {
			return nil, false
		}
		if auth, ok := h.AuthManager.GetExecutionSessionAuthByID(passthroughSessionID, authID); ok {
			return auth, true
		}
		return h.AuthManager.GetByID(authID)
	}
	forceTranscriptReplayNextRequest := false
	var retryPayload []byte
	suppressNextReplayableError := false

	for {
		var msgType int
		var payload []byte
		if len(retryPayload) > 0 {
			msgType = websocket.TextMessage
			payload = bytes.Clone(retryPayload)
			retryPayload = nil
		} else {
			var errReadMessage error
			msgType, payload, errReadMessage = conn.ReadMessage()
			if errReadMessage != nil {
				wsTerminateErr = errReadMessage
				if websocket.IsCloseError(errReadMessage, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					log.Infof("responses websocket: client disconnected id=%s error=%v", passthroughSessionID, errReadMessage)
				}
				return
			}
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				continue
			}
			if suppressNextReplayableError {
				suppressNextReplayableError = false
			}
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}

		wsTimelineLog.BeginRequest()
		wsTimelineLog.Append("request", payload, time.Now())

		for {
			allowIncrementalInputWithPreviousResponseID := false
			if pinnedAuthID != "" {
				if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && pinnedAuth != nil {
					allowIncrementalInputWithPreviousResponseID = websocketUpstreamSupportsIncrementalInput(pinnedAuth.Attributes, pinnedAuth.Metadata)
				}
			} else {
				requestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
				if requestModelName == "" {
					requestModelName = strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				}
				allowIncrementalInputWithPreviousResponseID = h.websocketUpstreamSupportsIncrementalInputForModel(requestModelName)
			}
			if forceTranscriptReplayNextRequest {
				allowIncrementalInputWithPreviousResponseID = false
			}

			allowCompactionReplayBypass := false
			if pinnedAuthID != "" {
				if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && pinnedAuth != nil {
					allowCompactionReplayBypass = responsesWebsocketAuthSupportsCompactionReplay(pinnedAuth)
				}
			} else {
				requestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
				if requestModelName == "" {
					requestModelName = strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				}
				allowCompactionReplayBypass = h.websocketUpstreamSupportsCompactionReplayForModel(requestModelName)
			}

			var requestJSON []byte
			var updatedLastRequest []byte
			var errMsg *interfaces.ErrorMessage
			requestJSON, updatedLastRequest, errMsg = normalizeResponsesWebsocketRequestWithMode(
				payload,
				lastRequest,
				lastResponseOutput,
				allowIncrementalInputWithPreviousResponseID,
				allowCompactionReplayBypass,
			)
			if errMsg != nil {
				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
				markAPIResponseTimestamp(c)
				errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					passthroughSessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
				if errWrite != nil {
					log.Warnf(
						"responses websocket: downstream_out write failed id=%s event=%s error=%v",
						passthroughSessionID,
						websocketPayloadEventType(errorPayload),
						errWrite,
					)
					return
				}
				break
			}
			if shouldHandleResponsesWebsocketPrewarmLocally(payload, lastRequest, allowIncrementalInputWithPreviousResponseID) {
				if updated, errDelete := sjson.DeleteBytes(requestJSON, "generate"); errDelete == nil {
					requestJSON = updated
				}
				if updated, errDelete := sjson.DeleteBytes(updatedLastRequest, "generate"); errDelete == nil {
					updatedLastRequest = updated
				}
				lastRequest = updatedLastRequest
				lastResponseOutput = []byte("[]")
				if errWrite := writeResponsesWebsocketSyntheticPrewarm(c, conn, requestJSON, wsTimelineLog, passthroughSessionID); errWrite != nil {
					wsTerminateErr = errWrite
					return
				}
				break
			}

			requestJSON = repairResponsesWebsocketToolCalls(downstreamSessionKey, requestJSON)
			updatedLastRequest = bytes.Clone(requestJSON)
			previousLastRequest := bytes.Clone(lastRequest)
			previousLastResponseOutput := bytes.Clone(lastResponseOutput)
			forcedTranscriptReplay := forceTranscriptReplayNextRequest
			lastRequest = updatedLastRequest
			if forcedTranscriptReplay {
				forceTranscriptReplayNextRequest = false
			}

			modelName := gjson.GetBytes(requestJSON, "model").String()
			cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
			cliCtx = cliproxyexecutor.WithDownstreamWebsocket(cliCtx)
			cliCtx = handlers.WithExecutionSessionID(cliCtx, passthroughSessionID)
			if excludedAuthIDs := sessionState.ExcludedAuthIDs(); len(excludedAuthIDs) > 0 {
				cliCtx = handlers.WithExcludedAuthIDs(cliCtx, excludedAuthIDs)
			}
			currentTurnAuthID := strings.TrimSpace(pinnedAuthID)
			if pinnedAuthID != "" {
				cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
			} else {
				cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
					authID = strings.TrimSpace(authID)
					if authID == "" || h == nil || h.AuthManager == nil {
						return
					}
					selectedAuth, ok := sessionAuthByID(authID)
					if !ok || selectedAuth == nil {
						return
					}
					currentTurnAuthID = authID
					if websocketUpstreamSupportsIncrementalInput(selectedAuth.Attributes, selectedAuth.Metadata) {
						pinnedAuthID = authID
					}
				})
			}
			dataChan, _, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, requestJSON, "")

			suppressForwardError := suppressNextReplayableError
			completedOutput, forwardErrMsg, errForward := h.forwardResponsesWebsocket(c, conn, cliCancel, dataChan, errChan, wsTimelineLog, passthroughSessionID, suppressForwardError)
			if errForward != nil {
				wsTerminateErr = errForward
				log.Warnf("responses websocket: forward failed id=%s error=%v", passthroughSessionID, errForward)
				return
			}
			if shouldReleaseResponsesWebsocketPinnedAuth(forwardErrMsg) {
				sessionState.MarkAuthFailed(currentTurnAuthID)
				if suppressForwardError {
					suppressNextReplayableError = false
					lastRequest = previousLastRequest
					lastResponseOutput = previousLastResponseOutput
					h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), forwardErrMsg)
					markAPIResponseTimestamp(c)
					errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, forwardErrMsg)
					log.Infof(
						"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
						passthroughSessionID,
						websocket.TextMessage,
						websocketPayloadEventType(errorPayload),
						websocketPayloadPreview(errorPayload),
					)
					if errWrite != nil {
						log.Warnf(
							"responses websocket: downstream_out write failed id=%s event=%s error=%v",
							passthroughSessionID,
							websocketPayloadEventType(errorPayload),
							errWrite,
						)
						return
					}
					break
				}

				// Rewind the local transcript snapshot and retry the same client
				// turn immediately. With incremental mode disabled on the retry,
				// request normalization expands into a full transcript replay so
				// the replacement auth receives complete context instead of the
				// old previous_response_id chain bound to the exhausted account.
				if errWait := waitResponsesWebsocketFailoverRetry(c.Request.Context(), sessionState.NextFailoverAttempt()); errWait != nil {
					wsTerminateErr = errWait
					return
				}
				pinnedAuthID = ""
				forceTranscriptReplayNextRequest = true
				suppressNextReplayableError = true
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				retryPayload = bytes.Clone(payload)
				break
			}
			if suppressForwardError && isReplayableResponsesWebsocketUpstreamError(forwardErrMsg) {
				suppressNextReplayableError = false
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), forwardErrMsg)
				markAPIResponseTimestamp(c)
				errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, forwardErrMsg)
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					passthroughSessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
				if errWrite != nil {
					log.Warnf(
						"responses websocket: downstream_out write failed id=%s event=%s error=%v",
						passthroughSessionID,
						websocketPayloadEventType(errorPayload),
						errWrite,
					)
					return
				}
				break
			}
			if !suppressForwardError && isReplayableResponsesWebsocketUpstreamError(forwardErrMsg) {
				pinnedAuthID = ""
				forceTranscriptReplayNextRequest = true
				suppressNextReplayableError = true
				lastRequest = previousLastRequest
				lastResponseOutput = previousLastResponseOutput
				retryPayload = bytes.Clone(payload)
				break
			}
			suppressNextReplayableError = false
			sessionState.ResetFailoverAttempts()
			lastResponseOutput = completedOutput
			break
		}
	}
}

func websocketClientAddress(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.ClientIP())
}

func websocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}

	// Keep the same sticky turn-state across reconnects when provided by the client.
	turnState := strings.TrimSpace(req.Header.Get(wsTurnStateHeader))
	if turnState != "" {
		headers.Set(wsTurnStateHeader, turnState)
	}
	return headers
}

func normalizeResponsesWebsocketRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	return normalizeResponsesWebsocketRequestWithMode(rawJSON, lastRequest, lastResponseOutput, true, true)
}

func normalizeResponsesWebsocketRequestWithMode(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case wsRequestTypeCreate:
		// log.Infof("responses websocket: response.create request")
		if len(lastRequest) == 0 {
			return normalizeResponseCreateRequest(rawJSON)
		}
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
	case wsRequestTypeAppend:
		// log.Infof("responses websocket: response.append request")
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
	default:
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("unsupported websocket request type: %s", requestType),
		}
	}
}

func normalizeResponseCreateRequest(rawJSON []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if !gjson.GetBytes(normalized, "input").Exists() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte("[]"))
	}
	normalized = defaultStoreForWebsocketPreviousResponseID(normalized)

	modelName := strings.TrimSpace(gjson.GetBytes(normalized, "model").String())
	if modelName == "" {
		return nil, nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("missing model in response.create request"),
		}
	}
	return normalized, bytes.Clone(normalized), nil
}

func normalizeResponseSubsequentRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	if len(lastRequest) == 0 {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request received before response.create"),
		}
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request requires field: input"),
		}
	}
	if nextInput.Type != gjson.String && !(nextInput.Type == gjson.JSON && nextInput.IsArray()) {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request requires string or array field: input"),
		}
	}

	// Compaction can cause clients to replace local websocket history with a new
	// compact transcript on the next `response.create`. When the input already
	// contains historical model output items, treating it as an incremental append
	// duplicates stale turn-state and can leave late orphaned function_call items.
	if shouldReplaceWebsocketTranscript(rawJSON, nextInput) {
		normalized, errReplayBudget := normalizeResponseTranscriptReplacement(rawJSON, lastRequest)
		if errReplayBudget != nil {
			return nil, lastRequest, errReplayBudget
		}
		return normalized, bytes.Clone(normalized), nil
	}

	// Websocket v2 mode uses response.create with previous_response_id + incremental input.
	// Do not expand it into a full input transcript; upstream expects the incremental payload.
	if allowIncrementalInputWithPreviousResponseID {
		if prev := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()); prev != "" {
			normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
			if errDelete != nil {
				normalized = bytes.Clone(rawJSON)
			}
			if !gjson.GetBytes(normalized, "model").Exists() {
				modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				if modelName != "" {
					normalized, _ = sjson.SetBytes(normalized, "model", modelName)
				}
			}
			if !gjson.GetBytes(normalized, "instructions").Exists() {
				instructions := gjson.GetBytes(lastRequest, "instructions")
				if instructions.Exists() {
					normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
				}
			}
			normalized, _ = sjson.SetBytes(normalized, "stream", true)
			normalized = defaultStoreForWebsocketPreviousResponseID(normalized)
			return normalized, bytes.Clone(normalized), nil
		}
	}

	// When the client sends a compact replay for a downstream that can consume it
	// directly, the input already carries the canonical history. In that case,
	// skip merging with stale lastRequest/lastResponseOutput to avoid breaking
	// function_call / function_call_output pairings.
	// See: https://github.com/router-for-me/CLIProxyAPI/issues/2207
	var mergedInput string
	if allowCompactionReplayBypass && inputContainsFullTranscript(nextInput) {
		log.Infof("responses websocket: full transcript detected, skipping stale merge (input items=%d)", len(nextInput.Array()))
		mergedInput = normalizeResponsesInputArrayRaw(nextInput)
	} else {
		appendInputRaw := normalizeResponsesInputArrayRaw(nextInput)
		if inputContainsFullTranscript(nextInput) {
			appendInputRaw = inputWithoutCompactionItems(nextInput)
		}

		existingInput := gjson.GetBytes(lastRequest, "input")
		var errMerge error
		mergedInput, errMerge = mergeJSONArrayRaw(normalizeResponsesInputArrayRaw(existingInput), normalizeJSONArrayRaw(lastResponseOutput))
		if errMerge != nil {
			return nil, lastRequest, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("invalid previous response output: %w", errMerge),
			}
		}

		mergedInput, errMerge = mergeJSONArrayRaw(mergedInput, appendInputRaw)
		if errMerge != nil {
			return nil, lastRequest, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("invalid request input: %w", errMerge),
			}
		}
	}
	dedupedInput, errDedupeFunctionCalls := dedupeFunctionCallsByCallID(mergedInput)
	if errDedupeFunctionCalls == nil {
		mergedInput = dedupedInput
	}
	sanitizedInput, errSanitize := removeOrphanedToolOutputs(mergedInput)
	if errSanitize == nil {
		mergedInput = sanitizedInput
	}
	budgetedInput, errReplayBudget := enforceResponsesWebsocketReplayBudget(mergedInput)
	if errReplayBudget != nil {
		return nil, lastRequest, errReplayBudget
	}
	mergedInput = budgetedInput

	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	var errSet error
	normalized, errSet = sjson.SetRawBytes(normalized, "input", []byte(mergedInput))
	if errSet != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("failed to merge websocket input: %w", errSet),
		}
	}
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	return normalized, bytes.Clone(normalized), nil
}

func shouldReplaceWebsocketTranscript(rawJSON []byte, nextInput gjson.Result) bool {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	if requestType != wsRequestTypeCreate && requestType != wsRequestTypeAppend {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != "" {
		return false
	}
	if !nextInput.Exists() || !nextInput.IsArray() {
		return false
	}

	for _, item := range nextInput.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			return true
		case "message":
			role := strings.TrimSpace(item.Get("role").String())
			if role == "assistant" {
				return true
			}
		}
	}

	return false
}

func normalizeResponseTranscriptReplacement(rawJSON []byte, lastRequest []byte) ([]byte, *interfaces.ErrorMessage) {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if inputResult := gjson.GetBytes(normalized, "input"); inputResult.Exists() && inputResult.IsArray() {
		if sanitized, err := removeOrphanedToolOutputs(inputResult.Raw); err == nil {
			budgeted, errBudget := enforceResponsesWebsocketReplayBudget(sanitized)
			if errBudget != nil {
				return nil, errBudget
			}
			normalized, _ = sjson.SetRawBytes(normalized, "input", []byte(budgeted))
		}
	}
	return bytes.Clone(normalized), nil
}

func dedupeFunctionCallsByCallID(rawArray string) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	seenCallIDs := make(map[string]struct{}, len(items))
	filtered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if isResponsesToolCallType(itemType) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID != "" {
				if _, ok := seenCallIDs[callID]; ok {
					continue
				}
				seenCallIDs[callID] = struct{}{}
			}
		}
		filtered = append(filtered, item)
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

// removeOrphanedToolOutputs drops unpaired function_call_output / custom_tool_call_output
// items whose call_id has no matching function_call / custom_tool_call in the
// same input array. This happens when an auth switch invalidates the upstream
// session and a full transcript replay carries tool outputs whose originating
// tool calls lived in a now-unreachable previous_response_id.
func removeOrphanedToolOutputs(rawArray string) (string, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", nil
	}
	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", errUnmarshal
	}

	toolCallIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID != "" && isResponsesToolCallType(itemType) {
			toolCallIDs[callID] = struct{}{}
		}
	}

	filtered := make([]json.RawMessage, 0, len(items))
	removed := 0
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if isResponsesToolCallOutputType(itemType) {
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID != "" {
				if _, ok := toolCallIDs[callID]; !ok {
					removed++
					continue
				}
			}
		}
		filtered = append(filtered, item)
	}

	if removed > 0 {
		log.Infof("responses websocket: removed %d orphaned tool output(s) from transcript replay", removed)
	}

	out, errMarshal := json.Marshal(filtered)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

func websocketUpstreamSupportsIncrementalInput(attributes map[string]string, metadata map[string]any) bool {
	if len(attributes) > 0 {
		for _, key := range []string{"websockets", "websocket"} {
			if raw := strings.TrimSpace(attributes[key]); raw != "" {
				parsed, errParse := strconv.ParseBool(raw)
				if errParse == nil {
					return parsed
				}
			}
		}
	}
	if len(metadata) == 0 {
		return false
	}
	for _, key := range []string{"websockets", "websocket"} {
		raw, ok := metadata[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case bool:
			return value
		case string:
			parsed, errParse := strconv.ParseBool(strings.TrimSpace(value))
			if errParse == nil {
				return parsed
			}
		default:
		}
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsIncrementalInputForModel(modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	for _, auth := range auths {
		if websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata) {
			return true
		}
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsCompactionReplayForModel(modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	if len(auths) == 0 {
		return false
	}
	for _, auth := range auths {
		if !responsesWebsocketAuthSupportsCompactionReplay(auth) {
			return false
		}
	}
	return true
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketAvailableAuthsForModel(modelName string) ([]*coreauth.Auth, string) {
	if h == nil || h.AuthManager == nil {
		return nil, ""
	}
	resolvedModelName := responsesWebsocketResolvedModelName(modelName)
	providerSet, modelKey := responsesWebsocketProviderSetForModel(resolvedModelName)
	if len(providerSet) == 0 {
		return nil, modelKey
	}

	registryRef := registry.GetGlobalRegistry()
	now := time.Now()
	auths := h.AuthManager.List()
	available := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if !responsesWebsocketAuthMatchesModel(auth, providerSet, modelKey, registryRef, now) {
			continue
		}
		available = append(available, auth)
	}
	return available, modelKey
}

func responsesWebsocketResolvedModelName(modelName string) string {
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
		if initialSuffix.HasSuffix {
			return fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
		}
		return resolvedBase
	}
	return util.ResolveAutoModel(modelName)
}

func responsesWebsocketProviderSetForModel(resolvedModelName string) (map[string]struct{}, string) {
	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)
	providers := util.GetProviderName(baseModel)
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		providerSet[providerKey] = struct{}{}
	}
	modelKey := baseModel
	if modelKey == "" {
		modelKey = strings.TrimSpace(resolvedModelName)
	}
	return providerSet, modelKey
}

func responsesWebsocketAuthMatchesModel(auth *coreauth.Auth, providerSet map[string]struct{}, modelKey string, registryRef *registry.ModelRegistry, now time.Time) bool {
	if auth == nil {
		return false
	}
	providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
	if _, ok := providerSet[providerKey]; !ok {
		return false
	}
	modelKeys := responsesWebsocketAuthModelSupportKeys(auth, modelKey)
	if len(modelKeys) > 0 && registryRef != nil {
		supported := false
		for _, key := range modelKeys {
			if registryRef.ClientSupportsModel(auth.ID, key) {
				supported = true
				break
			}
		}
		if !supported {
			return false
		}
	}
	return responsesWebsocketAuthAvailableForModel(auth, responsesWebsocketAuthAvailabilityModelKey(auth, modelKey), now)
}

func responsesWebsocketAuthModelSupportKeys(auth *coreauth.Auth, modelKey string) []string {
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return nil
	}
	keys := []string{modelKey}
	if stripped := responsesWebsocketAuthPrefixedModelTarget(auth, modelKey); stripped != "" && !strings.EqualFold(stripped, modelKey) {
		keys = append(keys, stripped)
	}
	return keys
}

func responsesWebsocketAuthAvailabilityModelKey(auth *coreauth.Auth, modelKey string) string {
	if stripped := responsesWebsocketAuthPrefixedModelTarget(auth, modelKey); stripped != "" {
		return stripped
	}
	return strings.TrimSpace(modelKey)
}

func responsesWebsocketAuthPrefixedModelTarget(auth *coreauth.Auth, modelKey string) string {
	if auth == nil {
		return ""
	}
	prefix := strings.TrimSpace(auth.Prefix)
	modelKey = strings.TrimSpace(modelKey)
	if prefix == "" || modelKey == "" {
		return ""
	}
	needle := prefix + "/"
	if !strings.HasPrefix(modelKey, needle) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(modelKey, needle))
}

func responsesWebsocketAuthSupportsCompactionReplay(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
}

func responsesWebsocketAuthAvailableForModel(auth *coreauth.Auth, modelName string, now time.Time) bool {
	if auth == nil {
		return false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if modelName != "" && len(auth.ModelStates) > 0 {
		state, ok := auth.ModelStates[modelName]
		if (!ok || state == nil) && modelName != "" {
			baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
			if baseModel != "" && baseModel != modelName {
				state, ok = auth.ModelStates[baseModel]
			}
		}
		if ok && state != nil {
			if state.Status == coreauth.StatusDisabled {
				return false
			}
			if state.Unavailable && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
				return false
			}
			return true
		}
	}
	if auth.Unavailable && !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
		return false
	}
	return true
}

func shouldHandleResponsesWebsocketPrewarmLocally(rawJSON []byte, lastRequest []byte, allowIncrementalInputWithPreviousResponseID bool) bool {
	if allowIncrementalInputWithPreviousResponseID || len(lastRequest) != 0 {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String()) != wsRequestTypeCreate {
		return false
	}
	generateResult := gjson.GetBytes(rawJSON, "generate")
	return generateResult.Exists() && !generateResult.Bool()
}

func writeResponsesWebsocketSyntheticPrewarm(
	c *gin.Context,
	conn *websocket.Conn,
	requestJSON []byte,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
) error {
	payloads, errPayloads := syntheticResponsesWebsocketPrewarmPayloads(requestJSON)
	if errPayloads != nil {
		return errPayloads
	}
	for i := 0; i < len(payloads); i++ {
		markAPIResponseTimestamp(c)
		// log.Infof(
		// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
		// 	sessionID,
		// 	websocket.TextMessage,
		// 	websocketPayloadEventType(payloads[i]),
		// 	websocketPayloadPreview(payloads[i]),
		// )
		if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
			log.Warnf(
				"responses websocket: downstream_out write failed id=%s event=%s error=%v",
				sessionID,
				websocketPayloadEventType(payloads[i]),
				errWrite,
			)
			return errWrite
		}
	}
	return nil
}

func syntheticResponsesWebsocketPrewarmPayloads(requestJSON []byte) ([][]byte, error) {
	responseID := "resp_prewarm_" + uuid.NewString()
	createdAt := time.Now().Unix()
	modelName := strings.TrimSpace(gjson.GetBytes(requestJSON, "model").String())

	createdPayload := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	var errSet error
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		createdPayload, errSet = sjson.SetBytes(createdPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	completedPayload := []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		completedPayload, errSet = sjson.SetBytes(completedPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	return [][]byte{createdPayload, completedPayload}, nil
}

func mergeJSONArrayRaw(existingRaw, appendRaw string) (string, error) {
	existingRaw = strings.TrimSpace(existingRaw)
	appendRaw = strings.TrimSpace(appendRaw)
	if existingRaw == "" {
		existingRaw = "[]"
	}
	if appendRaw == "" {
		appendRaw = "[]"
	}

	var existing []json.RawMessage
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return "", err
	}
	var appendItems []json.RawMessage
	if err := json.Unmarshal([]byte(appendRaw), &appendItems); err != nil {
		return "", err
	}

	merged := append(existing, appendItems...)
	out, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func normalizeResponsesInputArrayRaw(input gjson.Result) string {
	if !input.Exists() {
		return "[]"
	}
	if input.Type == gjson.JSON && input.IsArray() {
		return input.Raw
	}
	if input.Type == gjson.String {
		item := map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]string{
				{
					"type": "input_text",
					"text": input.String(),
				},
			},
		}
		raw, err := json.Marshal([]map[string]any{item})
		if err == nil {
			return string(raw)
		}
	}
	return "[]"
}

// inputContainsFullTranscript returns true when the input array carries compact
// replay markers that indicate the client already sent the full conversation
// transcript. Merging that input with stale lastRequest/lastResponseOutput
// would duplicate or break function_call/function_call_output pairings, so the
// caller should use the input as-is.
//
// Assistant messages alone are not enough to classify the payload as a replay:
// incremental websocket requests may legitimately append assistant items.
func inputContainsFullTranscript(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			return true
		}
	}
	return false
}

func inputWithoutCompactionItems(input gjson.Result) string {
	if !input.IsArray() {
		return normalizeJSONArrayRaw([]byte(input.Raw))
	}
	filtered := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			continue
		}
		filtered = append(filtered, item.Raw)
	}
	return "[" + strings.Join(filtered, ",") + "]"
}

func normalizeJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := gjson.Parse(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocket(
	c *gin.Context,
	conn *websocket.Conn,
	cancel handlers.APIHandlerCancelFunc,
	data <-chan []byte,
	errs <-chan *interfaces.ErrorMessage,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
	suppressReplayableError bool,
) ([]byte, *interfaces.ErrorMessage, error) {
	completed := false
	completedOutput := []byte("[]")
	streamedOutputItemsByIndex := make(map[int64][]byte)
	var streamedOutputItemsFallback [][]byte
	downstreamSessionKey := ""
	if c != nil && c.Request != nil {
		downstreamSessionKey = websocketDownstreamSessionKey(c.Request)
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return completedOutput, nil, c.Request.Context().Err()
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errMsg != nil {
				if isRecoverableResponsesWebsocketUpstreamError(errMsg) {
					cancel(errMsg.Error)
					return completedOutput, errMsg, nil
				}
				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
				markAPIResponseTimestamp(c)
				errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					sessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
				if errWrite != nil {
					// log.Warnf(
					// 	"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					// 	sessionID,
					// 	websocketPayloadEventType(errorPayload),
					// 	errWrite,
					// )
					cancel(errMsg.Error)
					return completedOutput, errMsg, errWrite
				}
			}
			if errMsg != nil {
				cancel(errMsg.Error)
			} else {
				cancel(nil)
			}
			return completedOutput, errMsg, nil
		case chunk, ok := <-data:
			if !ok {
				if !completed {
					errMsg := &interfaces.ErrorMessage{
						StatusCode: http.StatusRequestTimeout,
						Error:      fmt.Errorf("stream closed before response.completed"),
					}
					if suppressReplayableError && isReplayableResponsesWebsocketUpstreamError(errMsg) {
						cancel(errMsg.Error)
						return completedOutput, errMsg, nil
					}
					h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
					markAPIResponseTimestamp(c)
					errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
					log.Infof(
						"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
						sessionID,
						websocket.TextMessage,
						websocketPayloadEventType(errorPayload),
						websocketPayloadPreview(errorPayload),
					)
					if errWrite != nil {
						log.Warnf(
							"responses websocket: downstream_out write failed id=%s event=%s error=%v",
							sessionID,
							websocketPayloadEventType(errorPayload),
							errWrite,
						)
						cancel(errMsg.Error)
						return completedOutput, errMsg, errWrite
					}
					cancel(errMsg.Error)
					return completedOutput, errMsg, nil
				}
				cancel(nil)
				return completedOutput, nil, nil
			}

			payloads := websocketJSONPayloadsFromChunk(chunk)
			for i := range payloads {
				recordResponsesWebsocketToolCallsFromPayload(downstreamSessionKey, payloads[i])
				eventType := gjson.GetBytes(payloads[i], "type").String()
				if eventType == "response.output_item.done" {
					collectResponsesWebsocketOutputItemDone(payloads[i], streamedOutputItemsByIndex, &streamedOutputItemsFallback)
				}
				if eventType == wsEventTypeCompleted {
					completed = true
					completedOutput = responseCompletedOutputFromPayload(payloads[i])
					if responsesWebsocketOutputIsEmpty(completedOutput) {
						completedOutput = responsesWebsocketStreamedOutputItems(streamedOutputItemsByIndex, streamedOutputItemsFallback)
					}
				}
				markAPIResponseTimestamp(c)
				// log.Infof(
				// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
				// 	sessionID,
				// 	websocket.TextMessage,
				// 	websocketPayloadEventType(payloads[i]),
				// 	websocketPayloadPreview(payloads[i]),
				// )
				if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
					log.Warnf(
						"responses websocket: downstream_out write failed id=%s event=%s error=%v",
						sessionID,
						websocketPayloadEventType(payloads[i]),
						errWrite,
					)
					cancel(errWrite)
					return completedOutput, nil, errWrite
				}
			}
		}
	}
}

func collectResponsesWebsocketOutputItemDone(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := gjson.GetBytes(payload, "item")
	if !itemResult.Exists() || itemResult.Type != gjson.JSON {
		return
	}
	outputIndexResult := gjson.GetBytes(payload, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = bytes.Clone([]byte(itemResult.Raw))
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, bytes.Clone([]byte(itemResult.Raw)))
}

func responsesWebsocketStreamedOutputItems(outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	if len(outputItemsByIndex) == 0 && len(outputItemsFallback) == 0 {
		return []byte("[]")
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([][]byte, 0, len(outputItemsByIndex)+len(outputItemsFallback))
	for _, idx := range indexes {
		items = append(items, outputItemsByIndex[idx])
	}
	items = append(items, outputItemsFallback...)

	var buf bytes.Buffer
	totalLen := 2
	for _, item := range items {
		totalLen += len(item)
	}
	if len(items) > 1 {
		totalLen += len(items) - 1
	}
	buf.Grow(totalLen)
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func responsesWebsocketOutputIsEmpty(output []byte) bool {
	result := gjson.ParseBytes(output)
	return !result.IsArray() || len(result.Array()) == 0
}

// isRecoverableResponsesWebsocketUpstreamError identifies upstream failures
// that can be repaired inside the same downstream websocket session. The
// handler retries these by releasing the current auth pin and replaying the
// same client turn.
func isRecoverableResponsesWebsocketUpstreamError(errMsg *interfaces.ErrorMessage) bool {
	return shouldReleaseResponsesWebsocketPinnedAuth(errMsg) || isReplayableResponsesWebsocketUpstreamError(errMsg)
}

func shouldReleaseResponsesWebsocketPinnedAuth(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return false
	}
	status := errMsg.StatusCode
	if status <= 0 && errMsg.Error != nil {
		if se, ok := errMsg.Error.(interface{ StatusCode() int }); ok && se != nil {
			status = se.StatusCode()
		}
	}
	errText := ""
	if errMsg.Error != nil {
		errText = strings.ToLower(strings.TrimSpace(errMsg.Error.Error()))
	}
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	case http.StatusServiceUnavailable:
		return strings.Contains(errText, "auth_unavailable") || strings.Contains(errText, "auth_not_found")
	default:
		return strings.Contains(errText, "auth_unavailable") ||
			strings.Contains(errText, "auth_not_found") ||
			strings.Contains(errText, "model_cooldown")
	}
}

func isReplayableResponsesWebsocketUpstreamError(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return false
	}
	status := errMsg.StatusCode
	if status <= 0 && errMsg.Error != nil {
		if se, ok := errMsg.Error.(interface{ StatusCode() int }); ok && se != nil {
			status = se.StatusCode()
		}
	}
	if status != http.StatusBadRequest {
		return false
	}
	errText := ""
	if errMsg.Error != nil {
		errText = strings.ToLower(errMsg.Error.Error())
	}
	if strings.Contains(errText, "previous_response_not_found") || strings.Contains(errText, "previous response with id") {
		return true
	}
	if !(strings.Contains(errText, "no tool call found") || strings.Contains(errText, "no tool output found")) {
		return false
	}
	return strings.Contains(errText, "call_id") || strings.Contains(errText, "function call") || strings.Contains(errText, "tool call")
}

func responseCompletedOutputFromPayload(payload []byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() {
		return bytes.Clone([]byte(output.Raw))
	}
	return []byte("[]")
}

func websocketJSONPayloadsFromChunk(chunk []byte) [][]byte {
	payloads := make([][]byte, 0, 2)
	lines := bytes.Split(chunk, []byte("\n"))
	for i := range lines {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(line[len("data:"):])
		}
		if len(line) == 0 || bytes.Equal(line, []byte(wsDoneMarker)) {
			continue
		}
		if json.Valid(line) {
			payloads = append(payloads, bytes.Clone(line))
		}
	}

	if len(payloads) > 0 {
		return payloads
	}

	trimmed := bytes.TrimSpace(chunk)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte(wsDoneMarker)) && json.Valid(trimmed) {
		payloads = append(payloads, bytes.Clone(trimmed))
	}
	return payloads
}

func writeResponsesWebsocketError(conn *websocket.Conn, wsTimelineLog websocketTimelineAppender, errMsg *interfaces.ErrorMessage) ([]byte, error) {
	status := http.StatusInternalServerError
	errText := http.StatusText(status)
	if errMsg != nil {
		if errMsg.StatusCode > 0 {
			status = errMsg.StatusCode
			errText = http.StatusText(status)
		}
		if errMsg.Error != nil && strings.TrimSpace(errMsg.Error.Error()) != "" {
			errText = errMsg.Error.Error()
		}
	}

	body := handlers.BuildErrorResponseBody(status, errText)
	payload := []byte(`{}`)
	var errSet error
	payload, errSet = sjson.SetBytes(payload, "type", wsEventTypeError)
	if errSet != nil {
		return nil, errSet
	}
	payload, errSet = sjson.SetBytes(payload, "status", status)
	if errSet != nil {
		return nil, errSet
	}

	if errMsg != nil && errMsg.Addon != nil {
		headers := []byte(`{}`)
		hasHeaders := false
		for key, values := range errMsg.Addon {
			if len(values) == 0 {
				continue
			}
			headerPath := strings.ReplaceAll(strings.ReplaceAll(key, `\\`, `\\\\`), ".", `\\.`)
			headers, errSet = sjson.SetBytes(headers, headerPath, values[0])
			if errSet != nil {
				return nil, errSet
			}
			hasHeaders = true
		}
		if hasHeaders {
			payload, errSet = sjson.SetRawBytes(payload, "headers", headers)
			if errSet != nil {
				return nil, errSet
			}
		}
	}

	if len(body) > 0 && json.Valid(body) {
		errorNode := gjson.GetBytes(body, "error")
		if errorNode.Exists() {
			payload, errSet = sjson.SetRawBytes(payload, "error", []byte(errorNode.Raw))
		} else {
			payload, errSet = sjson.SetRawBytes(payload, "error", body)
		}
		if errSet != nil {
			return nil, errSet
		}
	}

	if !gjson.GetBytes(payload, "error").Exists() {
		payload, errSet = sjson.SetBytes(payload, "error.type", "server_error")
		if errSet != nil {
			return nil, errSet
		}
		payload, errSet = sjson.SetBytes(payload, "error.message", errText)
		if errSet != nil {
			return nil, errSet
		}
	}

	return payload, writeResponsesWebsocketPayload(conn, wsTimelineLog, payload, time.Now())
}

func appendWebsocketEvent(builder *strings.Builder, eventType string, payload []byte) {
	if builder == nil {
		return
	}
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString("websocket.")
	builder.WriteString(eventType)
	builder.WriteString("\n")
	builder.Write(trimmedPayload)
	builder.WriteString("\n")
}

func websocketPayloadEventType(payload []byte) string {
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if eventType == "" {
		return "-"
	}
	return eventType
}

func websocketPayloadPreview(payload []byte) string {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return "<empty>"
	}
	previewText := strings.ReplaceAll(string(trimmedPayload), "\n", "\\n")
	previewText = strings.ReplaceAll(previewText, "\r", "\\r")
	return previewText
}

func setWebsocketTimelineBody(c *gin.Context, body string) {
	setWebsocketBody(c, wsTimelineBodyKey, body)
}

func setWebsocketBody(c *gin.Context, key string, body string) {
	if c == nil {
		return
	}
	trimmedBody := strings.TrimSpace(body)
	if trimmedBody == "" {
		return
	}
	c.Set(key, []byte(trimmedBody))
}

func writeResponsesWebsocketPayload(conn *websocket.Conn, wsTimelineLog websocketTimelineAppender, payload []byte, timestamp time.Time) error {
	if wsTimelineLog != nil {
		wsTimelineLog.Append("response", payload, timestamp)
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func appendWebsocketTimelineDisconnect(timeline websocketTimelineAppender, err error, timestamp time.Time) {
	if err == nil {
		return
	}
	if timeline != nil {
		timeline.Append("disconnect", []byte(err.Error()), timestamp)
	}
}

func appendWebsocketTimelineEvent(builder *strings.Builder, eventType string, payload []byte, timestamp time.Time) {
	if builder == nil {
		return
	}
	if builder.Len() > maxTimelineLogSize {
		if builder.Len() <= maxTimelineLogSize+256 {
			builder.WriteString("\n...truncated...\n")
		}
		return
	}
	writeWebsocketTimelineBuilder(builder, formatWebsocketTimelineEvent(eventType, payload, timestamp))
}

func formatWebsocketTimelineEvent(eventType string, payload []byte, timestamp time.Time) []byte {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return nil
	}
	var builder strings.Builder
	builder.WriteString("Timestamp: ")
	builder.WriteString(timestamp.Format(time.RFC3339Nano))
	builder.WriteString("\n")
	builder.WriteString("Event: websocket.")
	builder.WriteString(eventType)
	builder.WriteString("\n")
	builder.Write(trimmedPayload)
	builder.WriteString("\n")
	return []byte(builder.String())
}

func markAPIResponseTimestamp(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); exists {
		return
	}
	c.Set("API_RESPONSE_TIMESTAMP", time.Now())
}
