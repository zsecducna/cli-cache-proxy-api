package registry

import "testing"

func TestGetAvailableModelsCodexReturnsRichMetadata(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{
		ID:                       "gpt-5.3-codex-spark",
		OwnedBy:                  "openai",
		Type:                     "codex",
		DisplayName:              "GPT 5.3 Codex Spark",
		Description:              "Fast Codex lane",
		ContextLength:            256000,
		MaxCompletionTokens:      32000,
		SupportedParameters:      []string{"reasoning.summary", "parallel_tool_calls"},
		SupportedInputModalities: []string{"TEXT", "IMAGE"},
		Thinking: &ThinkingSupport{
			Levels: []string{"minimal", "medium", "max"},
		},
	}})

	models := r.GetAvailableModels("codex")
	if len(models) != 1 {
		t.Fatalf("expected one Codex model, got %d", len(models))
	}

	model := models[0]
	if got := model["slug"]; got != "gpt-5.3-codex-spark" {
		t.Fatalf("slug = %v, want %q", got, "gpt-5.3-codex-spark")
	}
	if got := model["display_name"]; got != "GPT 5.3 Codex Spark" {
		t.Fatalf("display_name = %v, want %q", got, "GPT 5.3 Codex Spark")
	}
	if got := model["description"]; got != "Fast Codex lane" {
		t.Fatalf("description = %v, want %q", got, "Fast Codex lane")
	}
	if got := model["default_reasoning_level"]; got != "medium" {
		t.Fatalf("default_reasoning_level = %v, want %q", got, "medium")
	}
	if got := model["shell_type"]; got != codexDefaultShellType {
		t.Fatalf("shell_type = %v, want %q", got, codexDefaultShellType)
	}
	if got := model["visibility"]; got != codexDefaultVisibility {
		t.Fatalf("visibility = %v, want %q", got, codexDefaultVisibility)
	}
	if got := model["supported_in_api"]; got != true {
		t.Fatalf("supported_in_api = %v, want true", got)
	}
	if got := model["supports_reasoning_summaries"]; got != true {
		t.Fatalf("supports_reasoning_summaries = %v, want true", got)
	}
	if got := model["supports_parallel_tool_calls"]; got != true {
		t.Fatalf("supports_parallel_tool_calls = %v, want true", got)
	}
	if got := model["default_reasoning_summary"]; got != codexDefaultReasoningSummary {
		t.Fatalf("default_reasoning_summary = %v, want %q", got, codexDefaultReasoningSummary)
	}
	if got := model["support_verbosity"]; got != false {
		t.Fatalf("support_verbosity = %v, want false", got)
	}
	if got := model["context_window"]; got != 256000 {
		t.Fatalf("context_window = %v, want %d", got, 256000)
	}
	if got := model["base_instructions"]; got != codexDefaultBaseInstructions {
		t.Fatalf("base_instructions = %v, want Codex default base instructions", got)
	}
	if got := model["additional_speed_tiers"]; !stringSliceContains(got, codexFastTier) {
		t.Fatalf("additional_speed_tiers = %#v, want %q", got, codexFastTier)
	}
	if got := model["input_modalities"]; !stringSliceEqual(got, []string{"text", "image"}) {
		t.Fatalf("input_modalities = %#v, want [text image]", got)
	}
	if got := model["experimental_supported_tools"]; !stringSliceEqual(got, []string{}) {
		t.Fatalf("experimental_supported_tools = %#v, want empty []string", got)
	}

	policy, ok := model["truncation_policy"].(map[string]any)
	if !ok {
		t.Fatalf("truncation_policy type = %T, want map[string]any", model["truncation_policy"])
	}
	if got := policy["mode"]; got != codexDefaultTruncationMode {
		t.Fatalf("truncation_policy.mode = %v, want %q", got, codexDefaultTruncationMode)
	}
	if got := policy["limit"]; got != codexDefaultTruncationLimit {
		t.Fatalf("truncation_policy.limit = %v, want %d", got, codexDefaultTruncationLimit)
	}

	presets := codexPresetSlice(t, model["supported_reasoning_levels"])
	if len(presets) != 3 {
		t.Fatalf("supported_reasoning_levels len = %d, want 3", len(presets))
	}
	if got := presets[0]["effort"]; got != "minimal" {
		t.Fatalf("supported_reasoning_levels[0].effort = %v, want %q", got, "minimal")
	}
	if got := presets[2]["effort"]; got != "xhigh" {
		t.Fatalf("supported_reasoning_levels[2].effort = %v, want %q", got, "xhigh")
	}
}

func TestGetAvailableModelsCodexFallsBackToOpenAIDefaultReasoningLevels(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{
		ID:          "custom-gpt-5.4",
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "Custom GPT 5.4",
	}})

	models := r.GetAvailableModels("codex")
	if len(models) != 1 {
		t.Fatalf("expected one Codex model, got %d", len(models))
	}

	presets := codexPresetSlice(t, models[0]["supported_reasoning_levels"])
	wantEfforts := []string{"low", "medium", "high", "xhigh"}
	if len(presets) != len(wantEfforts) {
		t.Fatalf("supported_reasoning_levels len = %d, want %d", len(presets), len(wantEfforts))
	}
	for i, want := range wantEfforts {
		if got := presets[i]["effort"]; got != want {
			t.Fatalf("supported_reasoning_levels[%d].effort = %v, want %q", i, got, want)
		}
	}
	if got := models[0]["default_reasoning_level"]; got != "medium" {
		t.Fatalf("default_reasoning_level = %v, want %q", got, "medium")
	}
	if got := models[0]["input_modalities"]; !stringSliceEqual(got, []string{"text", "image"}) {
		t.Fatalf("input_modalities = %#v, want [text image] when modalities are unspecified", got)
	}
}

func TestGetAvailableModelsCodexReturnsClonedNestedSnapshots(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{
		ID:                  "gpt-5.4",
		OwnedBy:             "openai",
		Type:                "codex",
		DisplayName:         "GPT 5.4",
		SupportedParameters: []string{"reasoning.summary"},
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}})

	first := r.GetAvailableModels("codex")
	if len(first) != 1 {
		t.Fatalf("expected one Codex model, got %d", len(first))
	}
	firstPresets := codexPresetSlice(t, first[0]["supported_reasoning_levels"])
	firstPresets[0]["effort"] = "mutated"
	first[0]["truncation_policy"].(map[string]any)["mode"] = "mutated"

	second := r.GetAvailableModels("codex")
	secondPresets := codexPresetSlice(t, second[0]["supported_reasoning_levels"])
	if got := secondPresets[0]["effort"]; got != "low" {
		t.Fatalf("supported_reasoning_levels clone mutated to %v", got)
	}
	if got := second[0]["truncation_policy"].(map[string]any)["mode"]; got != codexDefaultTruncationMode {
		t.Fatalf("truncation_policy clone mutated to %v", got)
	}
}

func codexPresetSlice(t *testing.T, value any) []map[string]any {
	t.Helper()

	presets, ok := value.([]map[string]any)
	if !ok {
		t.Fatalf("supported_reasoning_levels type = %T, want []map[string]any", value)
	}
	return presets
}

func stringSliceContains(value any, want string) bool {
	values, ok := value.([]string)
	if !ok {
		return false
	}
	for _, entry := range values {
		if entry == want {
			return true
		}
	}
	return false
}

func stringSliceEqual(value any, want []string) bool {
	values, ok := value.([]string)
	if !ok || len(values) != len(want) {
		return false
	}
	for i := range want {
		if values[i] != want[i] {
			return false
		}
	}
	return true
}
