package helps

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
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

func TestNewUsageReporterIncludesReasoningEffortFromContext(t *testing.T) {
	ctx := WithUsageReasoningEffort(context.Background(), "xhigh")
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want %q", record.ReasoningEffort, "xhigh")
	}
}
