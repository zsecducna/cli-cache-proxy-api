package kiro

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

// thinkingModel returns a model info with thinking support for tests.
func thinkingModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:       "claude-sonnet-4.5",
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
	}
}

// TestApply_ModeNone_DisablesThinking verifies ModeNone emits {"thinking":{"type":"disabled"}}.
func TestApply_ModeNone_DisablesThinking(t *testing.T) {
	out, err := NewApplier().Apply([]byte(`{"reasoning_effort":"high"}`), thinking.ThinkingConfig{Mode: thinking.ModeNone}, thinkingModel())
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be cleared, body=%s", string(out))
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("budget_tokens should be absent when disabled, body=%s", string(out))
	}
}

// TestApply_ModeBudget_EnablesWithBudget verifies an explicit budget is preserved.
func TestApply_ModeBudget_EnablesWithBudget(t *testing.T) {
	out, err := NewApplier().Apply([]byte(`{}`), thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 8192}, thinkingModel())
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking.type").String() != "enabled" {
		t.Fatalf("thinking.type want enabled, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != 8192 {
		t.Fatalf("budget_tokens = %d, want 8192", got)
	}
}

// TestApply_ModeLevel_EnablesWithDerivedBudget verifies a level maps to a bounded budget.
func TestApply_ModeLevel_EnablesWithDerivedBudget(t *testing.T) {
	out, err := NewApplier().Apply([]byte(`{}`), thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, thinkingModel())
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking.type").String() != "enabled" {
		t.Fatalf("thinking.type want enabled, body=%s", string(out))
	}
	budget := gjson.GetBytes(out, "thinking.budget_tokens").Int()
	if budget <= 0 || budget > 32000 {
		t.Fatalf("budget_tokens = %d, want within (0,32000]", budget)
	}
}

// TestApply_ModeAuto_UsesDefaultBudget verifies auto mode enables thinking with the default budget.
func TestApply_ModeAuto_UsesDefaultBudget(t *testing.T) {
	out, err := NewApplier().Apply([]byte(`{}`), thinking.ThinkingConfig{Mode: thinking.ModeAuto}, thinkingModel())
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != defaultBudget {
		t.Fatalf("budget_tokens = %d, want %d", got, defaultBudget)
	}
}
