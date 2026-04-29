package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_OpenAICompatibilityAppendReasoningEffortToModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    append-reasoning-effort-to-model: true
    append-reasoning-effort-to-model-percent: 25
    models:
      - name: gpt-5.4
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("OpenAICompatibility length = %d, want 1", len(cfg.OpenAICompatibility))
	}
	if !cfg.OpenAICompatibility[0].AppendReasoningEffortToModelEnabled() {
		t.Fatal("AppendReasoningEffortToModelEnabled() = false, want true")
	}
	if cfg.OpenAICompatibility[0].AppendReasoningEffortToModelPercent == nil || *cfg.OpenAICompatibility[0].AppendReasoningEffortToModelPercent != 25 {
		t.Fatalf("AppendReasoningEffortToModelPercent = %v, want 25", cfg.OpenAICompatibility[0].AppendReasoningEffortToModelPercent)
	}
}

func TestLoadConfigOptional_OpenAICompatibilityAppendReasoningEffortDefaultsTrue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    models:
      - name: gpt-5.4
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("OpenAICompatibility length = %d, want 1", len(cfg.OpenAICompatibility))
	}
	if !cfg.OpenAICompatibility[0].AppendReasoningEffortToModelEnabled() {
		t.Fatal("AppendReasoningEffortToModelEnabled() = false, want true")
	}
}

func TestLoadConfigOptional_OpenAICompatibilityAppendReasoningEffortAllowsExplicitFalse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    append-reasoning-effort-to-model: false
    models:
      - name: gpt-5.4
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("OpenAICompatibility length = %d, want 1", len(cfg.OpenAICompatibility))
	}
	if cfg.OpenAICompatibility[0].AppendReasoningEffortToModelEnabled() {
		t.Fatal("AppendReasoningEffortToModelEnabled() = true, want false")
	}
}

func TestSanitizeOpenAICompatibilityClampsAppendReasoningEffortPercent(t *testing.T) {
	cfg := &Config{
		OpenAICompatibility: []OpenAICompatibility{
			{
				Name:                                "openrouter",
				BaseURL:                             "https://openrouter.ai/api/v1",
				AppendReasoningEffortToModelPercent: intPtr(150),
			},
			{
				Name:                                "openrouter-low",
				BaseURL:                             "https://openrouter.ai/api/v1",
				AppendReasoningEffortToModelPercent: intPtr(-5),
			},
		},
	}

	cfg.SanitizeOpenAICompatibility()

	if got := cfg.OpenAICompatibility[0].AppendReasoningEffortToModelPercent; got == nil || *got != 100 {
		t.Fatalf("first percent = %v, want 100", got)
	}
	if got := cfg.OpenAICompatibility[1].AppendReasoningEffortToModelPercent; got == nil || *got != 0 {
		t.Fatalf("second percent = %v, want 0", got)
	}
}

func intPtr(value int) *int {
	return &value
}
