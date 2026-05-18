// Package iflow implements thinking configuration for iFlow models.
package iflow

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier applies thinking configuration for iFlow models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates an iFlow thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("iflow", NewApplier())
}

// Apply applies iFlow thinking controls.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if isMiniMaxModel(modelInfo) {
		return applyMiniMaxThinking(body, config), nil
	}
	return applyGLMThinking(body, config), nil
}

func isMiniMaxModel(modelInfo *registry.ModelInfo) bool {
	if modelInfo == nil {
		return false
	}
	id := strings.ToLower(strings.TrimSpace(modelInfo.ID))
	return strings.Contains(id, "minimax")
}

func applyMiniMaxThinking(body []byte, config thinking.ThinkingConfig) []byte {
	result, _ := sjson.SetBytes(body, "reasoning_split", config.Mode != thinking.ModeNone)
	return result
}

func applyGLMThinking(body []byte, config thinking.ThinkingConfig) []byte {
	enabled := config.Mode != thinking.ModeNone
	result, _ := sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", enabled)
	result, _ = sjson.SetBytes(result, "chat_template_kwargs.clear_thinking", !enabled)
	return result
}
