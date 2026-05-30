// Package kiro implements thinking configuration for Amazon Kiro (AWS CodeWhisperer)
// models. Kiro is budget-based: the applier records the decision as a canonical thinking
// object ({"type":"enabled","budget_tokens":N} or {"type":"disabled"}) in the OpenAI body.
// The Kiro executor reads that object to decide whether to inject the <thinking_mode>
// content prefix and with what budget, so no reasoning_effort field is emitted here.
package kiro

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// defaultBudget is used for auto/unspecified thinking budgets.
	defaultBudget = 16000
	// minBudget and maxBudget bound the emitted budget.
	minBudget = 1
	maxBudget = 32000
)

// Applier implements thinking.ProviderApplier for Kiro models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Kiro thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("kiro", NewApplier())
}

// Apply records the thinking decision as a canonical thinking object on the body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	// Unknown/user-defined models still get the config applied so the upstream can react.
	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		return body, nil
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeNone:
		// A clamped fallback level means the model cannot fully disable thinking.
		if config.Level != "" && config.Level != thinking.LevelNone {
			return enableThinking(body, levelBudget(config.Level))
		}
		return disableThinking(body)
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		return enableThinking(body, levelBudget(config.Level))
	case thinking.ModeBudget:
		if config.Budget <= 0 {
			return body, nil
		}
		return enableThinking(body, config.Budget)
	case thinking.ModeAuto:
		return enableThinking(body, defaultBudget)
	default:
		return body, nil
	}
}

// levelBudget converts a discrete effort level to a token budget, defaulting when the
// level is unknown.
func levelBudget(level thinking.ThinkingLevel) int {
	if budget, ok := thinking.ConvertLevelToBudget(string(level)); ok {
		return budget
	}
	return defaultBudget
}

// enableThinking writes {"thinking":{"type":"enabled","budget_tokens":N}} and removes any
// reasoning_effort field so only the canonical object remains.
func enableThinking(body []byte, budget int) ([]byte, error) {
	if budget < minBudget {
		budget = minBudget
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	result, err := sjson.DeleteBytes(body, "reasoning_effort")
	if err != nil {
		return body, fmt.Errorf("kiro thinking: failed to clear reasoning_effort: %w", err)
	}
	result, err = sjson.SetBytes(result, "thinking.type", "enabled")
	if err != nil {
		return body, fmt.Errorf("kiro thinking: failed to set thinking.type: %w", err)
	}
	result, err = sjson.SetBytes(result, "thinking.budget_tokens", budget)
	if err != nil {
		return body, fmt.Errorf("kiro thinking: failed to set thinking.budget_tokens: %w", err)
	}
	return result, nil
}

// disableThinking writes {"thinking":{"type":"disabled"}} and removes reasoning_effort.
func disableThinking(body []byte) ([]byte, error) {
	result, err := sjson.DeleteBytes(body, "reasoning_effort")
	if err != nil {
		return body, fmt.Errorf("kiro thinking: failed to clear reasoning_effort: %w", err)
	}
	result, err = sjson.SetBytes(result, "thinking.type", "disabled")
	if err != nil {
		return body, fmt.Errorf("kiro thinking: failed to set thinking.type: %w", err)
	}
	result, err = sjson.DeleteBytes(result, "thinking.budget_tokens")
	if err != nil {
		return body, fmt.Errorf("kiro thinking: failed to clear thinking.budget_tokens: %w", err)
	}
	return result, nil
}
