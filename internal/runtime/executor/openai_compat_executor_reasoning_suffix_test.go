package executor

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorBuildExecutionPlan_AppendsReasoningEffortSuffixByDefault(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "openrouter",
			BaseURL: "https://openrouter.ai/api/v1",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name": "openrouter",
		},
	}

	plan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() error = %v", err)
	}

	if got := gjson.GetBytes(plan.translated, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "high")
	}
	if got := gjson.GetBytes(plan.translated, "model").String(); got != "gpt-5.4-high" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4-high")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_AppendsReasoningEffortSuffixAtHundredPercent(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                                "openrouter",
			BaseURL:                             "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel:        boolPtr(true),
			AppendReasoningEffortToModelPercent: intPtr(100),
		}},
	})
	auth := testOpenAICompatAuth()

	plan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"full-sample"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() error = %v", err)
	}

	if got := gjson.GetBytes(plan.translated, "model").String(); got != "gpt-5.4-high" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4-high")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_PreservesModelWhenToggleDisabled(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                         "openrouter",
			BaseURL:                      "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel: boolPtr(false),
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name": "openrouter",
		},
	}

	plan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() error = %v", err)
	}

	if got := gjson.GetBytes(plan.translated, "model").String(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_PreservesModelAtZeroPercent(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                                "openrouter",
			BaseURL:                             "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel:        boolPtr(true),
			AppendReasoningEffortToModelPercent: intPtr(0),
		}},
	})
	auth := testOpenAICompatAuth()

	plan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"zero-sample"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() error = %v", err)
	}

	if got := gjson.GetBytes(plan.translated, "model").String(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4")
	}
	if got := gjson.GetBytes(plan.translated, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "high")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_UsesDeterministicSamplingPercentage(t *testing.T) {
	const percent = 50
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                                "openrouter",
			BaseURL:                             "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel:        boolPtr(true),
			AppendReasoningEffortToModelPercent: intPtr(percent),
		}},
	})
	auth := testOpenAICompatAuth()

	sampledPayload := findOpenAICompatPayloadForBucket(t, percent, true)
	notSampledPayload := findOpenAICompatPayloadForBucket(t, percent, false)

	sampledPlan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: sampledPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() sampled error = %v", err)
	}
	if got := gjson.GetBytes(sampledPlan.translated, "model").String(); got != "gpt-5.4-high" {
		t.Fatalf("sampled model = %q, want %q", got, "gpt-5.4-high")
	}

	notSampledPlan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: notSampledPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() not sampled error = %v", err)
	}
	if got := gjson.GetBytes(notSampledPlan.translated, "model").String(); got != "gpt-5.4" {
		t.Fatalf("not sampled model = %q, want %q", got, "gpt-5.4")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_PrefersMetadataKeyForSampling(t *testing.T) {
	const percent = 50
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                                "openrouter",
			BaseURL:                             "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel:        boolPtr(true),
			AppendReasoningEffortToModelPercent: intPtr(percent),
		}},
	})
	auth := testOpenAICompatAuth()
	payload := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"same-payload"}]}`)

	sampledKey := findOpenAICompatSamplingKeyForBucket(t, percent, true)
	notSampledKey := findOpenAICompatSamplingKeyForBucket(t, percent, false)

	sampledPlan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			cliproxyexecutor.IdempotencyKeyMetadataKey: sampledKey,
		},
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() sampled metadata error = %v", err)
	}
	if got := gjson.GetBytes(sampledPlan.translated, "model").String(); got != "gpt-5.4-high" {
		t.Fatalf("sampled metadata model = %q, want %q", got, "gpt-5.4-high")
	}

	notSampledPlan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			cliproxyexecutor.IdempotencyKeyMetadataKey: notSampledKey,
		},
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() not sampled metadata error = %v", err)
	}
	if got := gjson.GetBytes(notSampledPlan.translated, "model").String(); got != "gpt-5.4" {
		t.Fatalf("not sampled metadata model = %q, want %q", got, "gpt-5.4")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_AppendsResponsesReasoningSuffixOnlyWhenPresent(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                         "openrouter",
			BaseURL:                      "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel: boolPtr(true),
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name": "openrouter",
		},
	}

	withReasoning, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() with reasoning error = %v", err)
	}
	if got := gjson.GetBytes(withReasoning.translated, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want %q", got, "high")
	}
	if got := gjson.GetBytes(withReasoning.translated, "model").String(); got != "gpt-5.4-high" {
		t.Fatalf("model with reasoning = %q, want %q", got, "gpt-5.4-high")
	}

	withoutReasoning, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, auth, false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() without reasoning error = %v", err)
	}
	if got := gjson.GetBytes(withoutReasoning.translated, "model").String(); got != "gpt-5.4" {
		t.Fatalf("model without reasoning = %q, want %q", got, "gpt-5.4")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_NormalizesResponsesReasoningEffortAlias(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                         "openrouter",
			BaseURL:                      "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel: boolPtr(true),
		}},
	})

	plan, err := executor.buildExecutionPlan(cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"Return ok exactly.","reasoning_effort":"high"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, testOpenAICompatAuth(), false)
	if err != nil {
		t.Fatalf("buildExecutionPlan() error = %v", err)
	}

	if got := plan.endpoint; got != "/responses" {
		t.Fatalf("endpoint = %q, want %q", got, "/responses")
	}
	if got := gjson.GetBytes(plan.translated, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want %q, body=%s", got, "high", string(plan.translated))
	}
	if gjson.GetBytes(plan.translated, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed after normalization, body=%s", string(plan.translated))
	}
	if got := gjson.GetBytes(plan.translated, "model").String(); got != "gpt-5.4-high" {
		t.Fatalf("model = %q, want %q, body=%s", got, "gpt-5.4-high", string(plan.translated))
	}
}

func findOpenAICompatPayloadForBucket(t *testing.T, percent int, sampled bool) []byte {
	t.Helper()

	for i := 0; i < 2048; i++ {
		payload := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"sample-%d"}]}`, i))
		bucket := openAICompatSamplingBucket("openrouter", "gpt-5.4(high)", "", payload, nil)
		if sampled && bucket < percent {
			return payload
		}
		if !sampled && bucket >= percent {
			return payload
		}
	}

	t.Fatalf("failed to find payload for sampled=%t percent=%d", sampled, percent)
	return nil
}

func findOpenAICompatSamplingKeyForBucket(t *testing.T, percent int, sampled bool) string {
	t.Helper()

	for i := 0; i < 2048; i++ {
		key := fmt.Sprintf("idempotency-%d", i)
		bucket := openAICompatSamplingBucket("openrouter", "gpt-5.4(high)", key, nil, nil)
		if sampled && bucket < percent {
			return key
		}
		if !sampled && bucket >= percent {
			return key
		}
	}

	t.Fatalf("failed to find sampling key for sampled=%t percent=%d", sampled, percent)
	return ""
}

func testOpenAICompatAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name": "openrouter",
		},
	}
}

func intPtr(value int) *int {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
