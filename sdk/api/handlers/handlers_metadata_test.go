package handlers

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataIncludesExecutionSessionWithoutIdempotencyKey(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-1")

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("ExecutionSessionMetadataKey = %v, want %q", got, "session-1")
	}
	if _, ok := meta[coreexecutor.IdempotencyKeyMetadataKey]; ok {
		t.Fatalf("unexpected idempotency key in metadata: %v", meta[coreexecutor.IdempotencyKeyMetadataKey])
	}
}

func TestWithUsageReasoningEffort_ClaudeViaOpenAICompatPreservesPayload(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	ctx, payload := withUsageReasoningEffort(context.Background(), raw, sdktranslator.FormatClaude, "gpt-5.5", RequestRouteClaudeViaOpenAICompat)
	if got := helps.UsageReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("UsageReasoningEffortFromContext() = %q, want %q", got, "medium")
	}
	if !bytes.Equal(payload, raw) {
		t.Fatalf("Claude-via-GPT payload should remain unchanged, got %s", string(payload))
	}
	if gjson.GetBytes(payload, "output_config.effort").Exists() {
		t.Fatalf("Claude-via-GPT payload should not gain output_config.effort, got %s", string(payload))
	}
}
