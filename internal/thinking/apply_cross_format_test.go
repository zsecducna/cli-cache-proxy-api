package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/iflow"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_ClaudeSourceConfigCrossFormat(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	openAIClientID := "test-cross-format-openai-" + t.Name()
	iflowClientID := "test-cross-format-iflow-" + t.Name()

	reg.RegisterClient(openAIClientID, "openai", []*registry.ModelInfo{{
		ID:       "cross-level-model",
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}},
	}})
	reg.RegisterClient(iflowClientID, "iflow", []*registry.ModelInfo{
		{
			ID:       "glm-cross-model",
			Type:     "iflow",
			Thinking: &registry.ThinkingSupport{Levels: []string{"none", "auto", "minimal", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:       "minimax-cross-model",
			Type:     "iflow",
			Thinking: &registry.ThinkingSupport{Levels: []string{"none", "auto", "minimal", "low", "medium", "high", "xhigh"}},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(openAIClientID)
		reg.UnregisterClient(iflowClientID)
	})

	tests := []struct {
		name        string
		model       string
		body        []byte
		toFormat    string
		providerKey string
		wantField   string
		wantValue   string
		wantField2  string
		wantValue2  string
	}{
		{
			name:        "adaptive effort to openai",
			model:       "cross-level-model",
			body:        []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`),
			toFormat:    "openai",
			providerKey: "openai",
			wantField:   "reasoning_effort",
			wantValue:   "high",
		},
		{
			name:        "adaptive max to openai xhigh",
			model:       "cross-level-model",
			body:        []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`),
			toFormat:    "openai",
			providerKey: "openai",
			wantField:   "reasoning_effort",
			wantValue:   "xhigh",
		},
		{
			name:        "enabled budget to glm",
			model:       "glm-cross-model",
			body:        []byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`),
			toFormat:    "iflow",
			providerKey: "iflow",
			wantField:   "chat_template_kwargs.enable_thinking",
			wantValue:   "true",
			wantField2:  "chat_template_kwargs.clear_thinking",
			wantValue2:  "false",
		},
		{
			name:        "adaptive effort to minimax",
			model:       "minimax-cross-model",
			body:        []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`),
			toFormat:    "iflow",
			providerKey: "iflow",
			wantField:   "reasoning_split",
			wantValue:   "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking(tt.body, tt.model, "claude", tt.toFormat, tt.providerKey)
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v", err)
			}

			if got := gjson.GetBytes(out, tt.wantField).String(); got != tt.wantValue {
				t.Fatalf("%s = %q, want %q, body=%s", tt.wantField, got, tt.wantValue, string(out))
			}
			if tt.wantField2 != "" {
				if got := gjson.GetBytes(out, tt.wantField2).String(); got != tt.wantValue2 {
					t.Fatalf("%s = %q, want %q, body=%s", tt.wantField2, got, tt.wantValue2, string(out))
				}
			}
			if gjson.GetBytes(out, "thinking").Exists() {
				t.Fatalf("thinking should be stripped after apply, body=%s", string(out))
			}
			if gjson.GetBytes(out, "output_config.effort").Exists() {
				t.Fatalf("output_config.effort should be stripped after apply, body=%s", string(out))
			}
		})
	}
}
