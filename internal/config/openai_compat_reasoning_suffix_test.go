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
	if !cfg.OpenAICompatibility[0].AppendReasoningEffortToModel {
		t.Fatal("AppendReasoningEffortToModel = false, want true")
	}
}
