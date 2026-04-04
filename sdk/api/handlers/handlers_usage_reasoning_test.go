package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestWithUsageReasoningEffort(t *testing.T) {
	baseCtx := context.Background()
	ctx := withUsageReasoningEffort(baseCtx, []byte(`{"reasoning":{"effort":"medium"}}`), sdktranslator.FromString("openai-response"))

	if got := helps.UsageReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("UsageReasoningEffortFromContext() = %q, want %q", got, "medium")
	}
}

func TestWithUsageReasoningEffortAllowsEmptyValue(t *testing.T) {
	ctx := withUsageReasoningEffort(context.Background(), []byte(`{"stream":true}`), sdktranslator.FromString("openai"))

	if got := helps.UsageReasoningEffortFromContext(ctx); got != "" {
		t.Fatalf("UsageReasoningEffortFromContext() = %q, want empty string", got)
	}
}
