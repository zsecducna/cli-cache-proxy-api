package helps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIUsageResponsesNestedUsage(t *testing.T) {
	data := []byte(`{"response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIStreamUsageResponses(t *testing.T) {
	line := []byte(`data: {"type":"response.completed","response":{"id":"resp_1"},"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("ParseOpenAIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIStreamUsageResponsesNestedUsage(t *testing.T) {
	line := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("ParseOpenAIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIStreamUsageIgnoresEmptyUsageChunks(t *testing.T) {
	tests := []struct {
		name string
		line []byte
	}{
		{
			name: "null usage",
			line: []byte(`data: {"id":"chunk-1","usage":null}`),
		},
		{
			name: "empty usage object",
			line: []byte(`data: {"id":"chunk-1","usage":{}}`),
		},
		{
			name: "zero chat completions usage",
			line: []byte(`data: {"id":"chunk-1","usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`),
		},
		{
			name: "zero responses usage",
			line: []byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := ParseOpenAIStreamUsage(tt.line); ok {
				t.Fatalf("expected empty OpenAI-compatible usage chunk to be ignored")
			}
		})
	}
}

func TestParseClaudeUsage_DoesNotTreatCacheCreationAsCacheRead(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3,"output_tokens":7,"cache_creation_input_tokens":103562}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 3 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 3)
	}
	if detail.OutputTokens != 7 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 7)
	}
	if detail.CachedTokens != 0 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 0)
	}
}

func TestParseClaudeStreamUsage_DoesNotTreatCacheCreationAsCacheRead(t *testing.T) {
	line := []byte(`data: {"usage":{"input_tokens":3,"output_tokens":7,"cache_creation_input_tokens":103562}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("ParseClaudeStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 3 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 3)
	}
	if detail.OutputTokens != 7 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 7)
	}
	if detail.CachedTokens != 0 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 0)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestExtractReasoningEffortFromRequest(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   []byte
		want   string
	}{
		{
			name:   "codex reasoning effort",
			format: "codex",
			body:   []byte(`{"reasoning":{"effort":"medium"}}`),
			want:   "medium",
		},
		{
			name:   "openai chat reasoning effort",
			format: "openai",
			body:   []byte(`{"reasoning_effort":"high"}`),
			want:   "high",
		},
		{
			name:   "claude adaptive effort",
			format: "claude",
			body:   []byte(`{"output_config":{"effort":"low"}}`),
			want:   "low",
		},
		{
			name:   "missing effort",
			format: "gemini",
			body:   []byte(`{"generationConfig":{"temperature":0.1}}`),
			want:   "",
		},
		{
			name:   "invalid json",
			format: "codex",
			body:   []byte(`{"reasoning":`),
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractReasoningEffortFromRequest(tt.body, tt.format); got != tt.want {
				t.Fatalf("ExtractReasoningEffortFromRequest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeReasoningEffortRequest(t *testing.T) {
	tests := []struct {
		name        string
		format      string
		model       string
		body        []byte
		wantEffort  string
		wantPath    string
		wantPresent bool
	}{
		{
			name:        "inject openai medium by default",
			format:      "openai",
			model:       "gpt-5.4",
			body:        []byte(`{"messages":[]}`),
			wantEffort:  "medium",
			wantPath:    "reasoning_effort",
			wantPresent: true,
		},
		{
			name:        "inject codex medium by default",
			format:      "codex",
			model:       "gpt-5.4-codex",
			body:        []byte(`{"input":[]}`),
			wantEffort:  "medium",
			wantPath:    "reasoning.effort",
			wantPresent: true,
		},
		{
			name:        "inject claude medium by default",
			format:      "claude",
			model:       "claude-sonnet-4-6",
			body:        []byte(`{"messages":[]}`),
			wantEffort:  "medium",
			wantPath:    "output_config.effort",
			wantPresent: true,
		},
		{
			name:        "preserve configured effort from model suffix",
			format:      "openai",
			model:       "gpt-5.4(high)",
			body:        []byte(`{"messages":[]}`),
			wantEffort:  "high",
			wantPresent: false,
		},
		{
			name:        "preserve existing request effort",
			format:      "openai",
			model:       "gpt-5.4",
			body:        []byte(`{"reasoning_effort":"low"}`),
			wantEffort:  "low",
			wantPath:    "reasoning_effort",
			wantPresent: true,
		},
		{
			name:        "leave unknown formats unchanged while defaulting context effort",
			format:      "gemini",
			model:       "gemini-2.5-pro",
			body:        []byte(`{"contents":[]}`),
			wantEffort:  "medium",
			wantPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBody, gotEffort := NormalizeReasoningEffortRequest(tt.body, tt.format, tt.model)
			if gotEffort != tt.wantEffort {
				t.Fatalf("NormalizeReasoningEffortRequest() effort = %q, want %q", gotEffort, tt.wantEffort)
			}
			if tt.wantPresent {
				if got := gjson.GetBytes(gotBody, tt.wantPath).String(); got != tt.wantEffort {
					t.Fatalf("NormalizeReasoningEffortRequest() %s = %q, want %q, body=%s", tt.wantPath, got, tt.wantEffort, string(gotBody))
				}
				return
			}
			if !bytes.Equal(gotBody, tt.body) {
				t.Fatalf("NormalizeReasoningEffortRequest() unexpectedly changed body: got=%s want=%s", string(gotBody), string(tt.body))
			}
		})
	}
}

func TestNewUsageReporterIncludesReasoningEffortFromContext(t *testing.T) {
	ctx := WithUsageReasoningEffort(context.Background(), "xhigh")
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want %q", record.ReasoningEffort, "xhigh")
	}
}

func TestShouldPublishFailureSkipsContextCanceledError(t *testing.T) {
	if shouldPublishFailure(context.Background(), context.Canceled) {
		t.Fatal("expected context cancellation error to skip failed usage publication")
	}
}

func TestShouldPublishFailureSkipsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if shouldPublishFailure(ctx, nil) {
		t.Fatal("expected canceled context to skip failed usage publication")
	}
}

func TestShouldPublishFailureAllowsNonCanceledFailures(t *testing.T) {
	if !shouldPublishFailure(context.Background(), context.DeadlineExceeded) {
		t.Fatal("expected non-canceled failure to be published")
	}
}

func TestShouldPublishFailureSkipsCreditsExhaustedFailures(t *testing.T) {
	if shouldPublishFailure(context.Background(), fmt.Errorf(`{"error":{"code":"credits_exhausted","message":"customer credits exhausted"}}`)) {
		t.Fatal("expected credits_exhausted failure to skip failed usage publication")
	}
	if shouldPublishFailure(context.Background(), fmt.Errorf(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`)) {
		t.Fatal("expected quota_exhausted failure to skip failed usage publication")
	}
}

func TestShouldPublishFailureDoesNotSkipGenericRateLimits(t *testing.T) {
	if !shouldPublishFailure(context.Background(), fmt.Errorf(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`)) {
		t.Fatal("expected generic rate limit failure to be published")
	}
}

func TestShouldPublishFailureSkipsAbortHandler(t *testing.T) {
	if shouldPublishFailure(context.Background(), http.ErrAbortHandler) {
		t.Fatal("expected http.ErrAbortHandler to skip failed usage publication")
	}
}

func TestShouldPublishFailureSkipsWrappedAbortHandler(t *testing.T) {
	if shouldPublishFailure(context.Background(), fmt.Errorf("wrapped: %w", http.ErrAbortHandler)) {
		t.Fatal("expected wrapped http.ErrAbortHandler to skip failed usage publication")
	}
}

func TestUsageReporterBuildAdditionalModelRecordSkipsZeroTokens(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "codex",
		model:       "gpt-5.4",
		requestedAt: time.Now(),
	}

	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{}); ok {
		t.Fatalf("expected all-zero token usage to be skipped")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{InputTokens: 2}); !ok {
		t.Fatalf("expected non-zero input token usage to be recorded")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{CachedTokens: 2}); !ok {
		t.Fatalf("expected non-zero cached token usage to be recorded")
	}
}
