package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestWithUsageReasoningEffort(t *testing.T) {
	baseCtx := context.Background()
	ctx, normalized := withUsageReasoningEffort(baseCtx, []byte(`{"reasoning":{"effort":"medium"}}`), sdktranslator.FromString("openai-response"), "gpt-5.4", RequestRouteDefault)

	if got := helps.UsageReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("UsageReasoningEffortFromContext() = %q, want %q", got, "medium")
	}
	if got := gjson.GetBytes(normalized, "reasoning.effort").String(); got != "medium" {
		t.Fatalf("normalized reasoning.effort = %q, want %q", got, "medium")
	}
}

func TestWithUsageReasoningEffortInjectsDefaultMedium(t *testing.T) {
	ctx, normalized := withUsageReasoningEffort(context.Background(), []byte(`{"stream":true}`), sdktranslator.FromString("openai"), "gpt-5.4", RequestRouteDefault)

	if got := helps.UsageReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("UsageReasoningEffortFromContext() = %q, want %q", got, "medium")
	}
	if got := gjson.GetBytes(normalized, "reasoning_effort").String(); got != "medium" {
		t.Fatalf("normalized reasoning_effort = %q, want %q", got, "medium")
	}
}

func TestWithUsageReasoningEffortUsesModelSuffixWhenRequestOmitsEffort(t *testing.T) {
	ctx, normalized := withUsageReasoningEffort(context.Background(), []byte(`{"messages":[]}`), sdktranslator.FromString("openai"), "gpt-5.4(high)", RequestRouteDefault)

	if got := helps.UsageReasoningEffortFromContext(ctx); got != "high" {
		t.Fatalf("UsageReasoningEffortFromContext() = %q, want %q", got, "high")
	}
	if gjson.GetBytes(normalized, "reasoning_effort").Exists() {
		t.Fatalf("expected model suffix to avoid payload injection, body=%s", string(normalized))
	}
}
