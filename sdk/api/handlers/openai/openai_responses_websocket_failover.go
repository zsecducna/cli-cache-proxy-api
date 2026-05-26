package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/tidwall/gjson"
)

const (
	// responsesWebsocketReplayMaxBytes caps the JSON size of a replay payload
	// sent to a replacement auth after websocket continuity is rebuilt locally.
	responsesWebsocketReplayMaxBytes = 256 << 10
	// responsesWebsocketReplayMaxItems caps the number of input items carried in
	// one replay payload before local snapshot compaction is required.
	responsesWebsocketReplayMaxItems = 96
	// responsesWebsocketReplayRecentItems is the initial concrete tail preserved
	// after older transcript items are collapsed into a synthetic snapshot.
	responsesWebsocketReplayRecentItems = 24
	// responsesWebsocketReplaySnapshotLegacyIDPrefix is the historical local
	// snapshot prefix used before Codex websocket upstream started validating
	// `message.id` prefixes strictly.
	responsesWebsocketReplaySnapshotLegacyIDPrefix = "proxy-snapshot-"
	// responsesWebsocketReplaySnapshotLegacyCompatIDPrefix matches the older
	// broken proxy rewrite that converted legacy snapshot IDs into
	// upstream-looking `msg-proxy-snapshot-*` values. Keep it only so current
	// releases can strip those stale IDs from in-flight sessions.
	responsesWebsocketReplaySnapshotLegacyCompatIDPrefix = "msg-proxy-snapshot-"
	// responsesWebsocketReplaySummaryPrefix marks synthetic proxy snapshot
	// summaries. These items are local context compaction helpers, not untouched
	// output items from OpenAI, so they must be replayed without an `id`.
	responsesWebsocketReplaySummaryPrefix = "Proxy conversation snapshot after auth rotation. Earlier context summary:"
	// responsesWebsocketReplaySummaryMaxChars bounds the human-readable snapshot
	// summary that replaces older transcript history during local compaction.
	responsesWebsocketReplaySummaryMaxChars = 2048

	// responsesWebsocketFailoverBaseDelay is the first retry pause applied before
	// replaying a failed websocket turn on a replacement auth.
	responsesWebsocketFailoverBaseDelay = 200 * time.Millisecond
	// responsesWebsocketFailoverMaxDelay bounds retry delay growth.
	responsesWebsocketFailoverMaxDelay = 1 * time.Second
	// responsesWebsocketFailoverMaxConcurrent limits concurrent in-process
	// failover retries so auth exhaustion storms do not all replay at once.
	responsesWebsocketFailoverMaxConcurrent = 16
)

var responsesWebsocketFailoverLimiter = make(chan struct{}, responsesWebsocketFailoverMaxConcurrent)

var responsesWebsocketFailoverAcquire = defaultResponsesWebsocketFailoverAcquire
var responsesWebsocketFailoverSleep = sleepResponsesWebsocketFailoverDelay
var responsesWebsocketFailoverJitter = jitterResponsesWebsocketFailoverDelay

// responsesWebsocketSessionState keeps the per-downstream websocket failover
// guards: denied auth IDs and retry-streak backoff state.
type responsesWebsocketSessionState struct {
	failedAuthIDs    map[string]struct{}
	failoverAttempts int
}

func newResponsesWebsocketSessionState() *responsesWebsocketSessionState {
	return &responsesWebsocketSessionState{failedAuthIDs: make(map[string]struct{})}
}

func (s *responsesWebsocketSessionState) MarkAuthFailed(authID string) {
	if s == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if s.failedAuthIDs == nil {
		s.failedAuthIDs = make(map[string]struct{})
	}
	s.failedAuthIDs[authID] = struct{}{}
}

func (s *responsesWebsocketSessionState) ExcludedAuthIDs() []string {
	if s == nil || len(s.failedAuthIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.failedAuthIDs))
	for authID := range s.failedAuthIDs {
		out = append(out, authID)
	}
	return out
}

func (s *responsesWebsocketSessionState) NextFailoverAttempt() int {
	if s == nil {
		return 1
	}
	s.failoverAttempts++
	if s.failoverAttempts <= 0 {
		s.failoverAttempts = 1
	}
	return s.failoverAttempts
}

func (s *responsesWebsocketSessionState) ResetFailoverAttempts() {
	if s == nil {
		return
	}
	s.failoverAttempts = 0
}

// waitResponsesWebsocketFailoverRetry applies the shared retry limiter plus a
// bounded backoff/jitter delay before the proxy replays a failed websocket turn.
func waitResponsesWebsocketFailoverRetry(ctx context.Context, attempt int) error {
	release, errAcquire := responsesWebsocketFailoverAcquire(ctx)
	if errAcquire != nil {
		return errAcquire
	}
	defer release()

	if attempt <= 0 {
		attempt = 1
	}
	delay := time.Duration(attempt) * responsesWebsocketFailoverBaseDelay
	if delay > responsesWebsocketFailoverMaxDelay {
		delay = responsesWebsocketFailoverMaxDelay
	}
	delay = responsesWebsocketFailoverJitter(delay)
	return responsesWebsocketFailoverSleep(ctx, delay)
}

func defaultResponsesWebsocketFailoverAcquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case responsesWebsocketFailoverLimiter <- struct{}{}:
		return func() { <-responsesWebsocketFailoverLimiter }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func sleepResponsesWebsocketFailoverDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func jitterResponsesWebsocketFailoverDelay(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	jitterMax := base / 2
	if jitterMax <= 0 {
		return base
	}
	return base + time.Duration(rand.Int64N(int64(jitterMax)+1))
}

// enforceResponsesWebsocketReplayBudget compactly snapshots older transcript
// items when a full replay grows too large. If even the compacted replay cannot
// fit, the websocket request is rejected with an explicit retry-too-large error.
func enforceResponsesWebsocketReplayBudget(rawArray string) (string, *interfaces.ErrorMessage) {
	if responsesWebsocketReplayWithinBudget(rawArray) {
		return rawArray, nil
	}
	compacted, compactedOK := compactResponsesWebsocketReplay(rawArray)
	if compactedOK && responsesWebsocketReplayWithinBudget(compacted) {
		return compacted, nil
	}
	return "", &interfaces.ErrorMessage{
		StatusCode: http.StatusRequestEntityTooLarge,
		Error: fmt.Errorf(
			"session_replay_too_large: replay exceeds %d bytes or %d items after local compaction",
			responsesWebsocketReplayMaxBytes,
			responsesWebsocketReplayMaxItems,
		),
	}
}

func responsesWebsocketReplayWithinBudget(rawArray string) bool {
	if len(rawArray) > responsesWebsocketReplayMaxBytes {
		return false
	}
	result := gjson.Parse(rawArray)
	if !result.IsArray() {
		return true
	}
	return len(result.Array()) <= responsesWebsocketReplayMaxItems
}

func compactResponsesWebsocketReplay(rawArray string) (string, bool) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return "[]", true
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return "", false
	}
	if len(items) <= 1 {
		return "", false
	}

	keepCount := responsesWebsocketReplayRecentItems
	if keepCount >= len(items) {
		keepCount = len(items) - 1
	}
	if keepCount < 1 {
		return "", false
	}

	for keepCount >= 1 {
		cut := len(items) - keepCount
		if cut <= 0 {
			break
		}
		snapshotItem, okSnapshot := buildResponsesWebsocketReplaySnapshotItem(items[:cut])
		if !okSnapshot {
			return "", false
		}
		compactedItems := make([]json.RawMessage, 0, keepCount+1)
		compactedItems = append(compactedItems, snapshotItem)
		compactedItems = append(compactedItems, items[cut:]...)
		rawCompacted, errMarshal := json.Marshal(compactedItems)
		if errMarshal != nil {
			return "", false
		}

		compacted := string(rawCompacted)
		if deduped, errDedupe := dedupeFunctionCallsByCallID(compacted); errDedupe == nil {
			compacted = deduped
		}
		if sanitized, errSanitize := removeOrphanedToolOutputs(compacted); errSanitize == nil {
			compacted = sanitized
		}
		if responsesWebsocketReplayWithinBudget(compacted) {
			return compacted, true
		}

		if keepCount == 1 {
			break
		}
		keepCount /= 2
	}
	return "", false
}

// buildResponsesWebsocketReplaySnapshotItem collapses older replay history into
// one synthetic assistant message. Official Responses examples show client
// input messages without `id`, while model output items carry server-generated
// IDs like `msg_...`, so the proxy must not invent an upstream `message.id`
// for its own local snapshot summary.
func buildResponsesWebsocketReplaySnapshotItem(items []json.RawMessage) (json.RawMessage, bool) {
	if len(items) == 0 {
		return nil, false
	}
	summaryText := summarizeResponsesWebsocketReplayItems(items)
	if summaryText == "" {
		return nil, false
	}
	item := map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []map[string]string{
			{
				"type": "output_text",
				"text": summaryText,
			},
		},
	}
	raw, errMarshal := json.Marshal(item)
	if errMarshal != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

func summarizeResponsesWebsocketReplayItems(items []json.RawMessage) string {
	if len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(responsesWebsocketReplaySummaryPrefix)
	for i := 0; i < len(items); i++ {
		entry := summarizeResponsesWebsocketReplayItem(items[i])
		if entry == "" {
			continue
		}
		if builder.Len()+len(entry)+3 > responsesWebsocketReplaySummaryMaxChars {
			builder.WriteString(" ...")
			break
		}
		builder.WriteString("\n- ")
		builder.WriteString(entry)
	}
	return builder.String()
}

func summarizeResponsesWebsocketReplayItem(item json.RawMessage) string {
	itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
	switch itemType {
	case "message":
		role := strings.TrimSpace(gjson.GetBytes(item, "role").String())
		return role + " message: " + truncateResponsesWebsocketReplayText(extractResponsesWebsocketReplayText(item))
	case "function_call", "custom_tool_call":
		name := strings.TrimSpace(gjson.GetBytes(item, "name").String())
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		return fmt.Sprintf("%s %s call_id=%s", itemType, name, callID)
	case "function_call_output", "custom_tool_call_output":
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		return fmt.Sprintf("%s call_id=%s output=%s", itemType, callID, truncateResponsesWebsocketReplayText(gjson.GetBytes(item, "output").String()))
	default:
		if itemType == "" {
			return ""
		}
		return itemType + ": " + truncateResponsesWebsocketReplayText(string(item))
	}
}

func extractResponsesWebsocketReplayText(item json.RawMessage) string {
	content := gjson.GetBytes(item, "content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		parts := make([]string, 0, len(content.Array()))
		for _, entry := range content.Array() {
			text := strings.TrimSpace(entry.Get("text").String())
			if text == "" {
				text = strings.TrimSpace(entry.Get("content").String())
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func truncateResponsesWebsocketReplayText(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " "))
	if len(text) <= 160 {
		return text
	}
	return text[:157] + "..."
}
