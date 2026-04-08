package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorBuildExecutionPlan_AppendsReasoningEffortSuffixWhenEnabled(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                         "openrouter",
			BaseURL:                      "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel: true,
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

func TestOpenAICompatExecutorBuildExecutionPlan_PreservesModelWhenToggleDisabled(t *testing.T) {
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

	if got := gjson.GetBytes(plan.translated, "model").String(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4")
	}
}

func TestOpenAICompatExecutorBuildExecutionPlan_AppendsResponsesReasoningSuffixOnlyWhenPresent(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                         "openrouter",
			BaseURL:                      "https://openrouter.ai/api/v1",
			AppendReasoningEffortToModel: true,
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
