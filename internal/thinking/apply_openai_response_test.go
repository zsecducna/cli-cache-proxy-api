package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_OpenAIResponseAliasNormalizesTopLevelReasoningEffort(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-openai-response-alias-" + t.Name()
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:       "gpt-5.4",
		Type:     "codex",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	body := []byte(`{"model":"gpt-5.4","input":"Return ok exactly.","reasoning_effort":"high"}`)
	out, err := thinking.ApplyThinking(body, "gpt-5.4", "openai-response", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}

	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want %q, body=%s", got, "high", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed after normalization, body=%s", string(out))
	}
}
