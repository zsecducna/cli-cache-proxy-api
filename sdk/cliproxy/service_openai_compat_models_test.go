package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAICompatConfigModels_UsesStaticThinkingAndMarksUserDefined(t *testing.T) {
	models := buildOpenAICompatConfigModels("openai", []config.OpenAICompatibilityModel{{
		Name: "gpt-5.4",
	}})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}

	model := models[0]
	if !model.UserDefined {
		t.Fatal("UserDefined = false, want true")
	}
	if model.Type != "openai-compatibility" {
		t.Fatalf("Type = %q, want %q", model.Type, "openai-compatibility")
	}
	if model.Thinking == nil {
		t.Fatal("Thinking = nil, want inherited static thinking metadata")
	}

	hasXHigh := false
	for _, level := range model.Thinking.Levels {
		if level == "xhigh" {
			hasXHigh = true
			break
		}
	}
	if !hasXHigh {
		t.Fatalf("Thinking.Levels = %#v, want to include xhigh", model.Thinking.Levels)
	}
}

func TestBuildOpenAICompatConfigModels_DefaultsUnknownModelsToXHighMetadata(t *testing.T) {
	models := buildOpenAICompatConfigModels("openai", []config.OpenAICompatibilityModel{{
		Name: "custom-upstream-model",
	}})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].Thinking == nil {
		t.Fatal("Thinking = nil, want default metadata")
	}

	want := []string{"low", "medium", "high", "xhigh"}
	if got := models[0].Thinking.Levels; len(got) != len(want) {
		t.Fatalf("Thinking.Levels length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i, level := range want {
		if got := models[0].Thinking.Levels[i]; got != level {
			t.Fatalf("Thinking.Levels[%d] = %q, want %q (%#v)", i, got, level, models[0].Thinking.Levels)
		}
	}
}

func TestOpenAICompatConfigModels_ApplyThinkingNormalizesResponsesAliasXHigh(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-" + t.Name()
	reg.RegisterClient(clientID, "openai", buildOpenAICompatConfigModels("openai", []config.OpenAICompatibilityModel{{
		Name: "gpt-5.4",
	}}))
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	body := []byte(`{"model":"gpt-5.4","input":"Return ok exactly.","reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinking(body, "gpt-5.4", "openai-response", "openai-response", "openai")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}

	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want %q, body=%s", got, "xhigh", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed after normalization, body=%s", string(out))
	}
}
