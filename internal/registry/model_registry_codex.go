package registry

import "strings"

const (
	codexDefaultShellType                  = "shell_command"
	codexDefaultVisibility                 = "list"
	codexDefaultPriority                   = 99
	codexDefaultBaseInstructions           = "You are Codex, a coding agent based on GPT-5. You and the user share the same workspace and collaborate to achieve the user's goals."
	codexDefaultReasoningSummary           = "auto"
	codexDefaultTruncationMode             = "bytes"
	codexDefaultTruncationLimit            = 10000
	codexEffectiveContextWindowPercent     = 95
	codexFastTier                          = "fast"
	codexReasoningEffortParam              = "reasoning.effort"
	codexReasoningSummaryParam             = "reasoning.summary"
	codexVerbosityParam                    = "verbosity"
	codexTextVerbosityParam                = "text.verbosity"
	codexSupportsParallelToolCallsParam    = "parallel_tool_calls"
	codexSupportsParallelToolCallsAltParam = "parallel-tool-calls"
	codexSupportsSearchToolParam           = "web_search"
	codexSupportsSearchToolAltParam        = "search"
)

var codexDefaultReasoningLevels = []string{"low", "medium", "high", "xhigh"}

func convertCodexModelToMap(model *ModelInfo) map[string]any {
	if model == nil {
		return nil
	}

	result := map[string]any{
		"id":                           model.ID,
		"slug":                         model.ID,
		"display_name":                 codexDisplayName(model),
		"shell_type":                   codexDefaultShellType,
		"visibility":                   codexDefaultVisibility,
		"supported_in_api":             true,
		"priority":                     codexDefaultPriority,
		"base_instructions":            codexDefaultBaseInstructions,
		"supports_reasoning_summaries": codexSupportsReasoningSummaries(model),
		"default_reasoning_summary":    codexDefaultReasoningSummary,
		"support_verbosity":            codexSupportsVerbosity(model),
		"truncation_policy": map[string]any{
			"mode":  codexDefaultTruncationMode,
			"limit": codexDefaultTruncationLimit,
		},
		"supports_parallel_tool_calls":     codexSupportsParallelToolCalls(model),
		"effective_context_window_percent": codexEffectiveContextWindowPercent,
		"experimental_supported_tools":     []string{},
		"supported_reasoning_levels":       codexReasoningEffortPresets(codexSupportedReasoningLevels(model)),
		"supports_search_tool":             codexSupportsSearchTool(model),
	}

	if model.Description != "" {
		result["description"] = model.Description
	}
	if contextWindow := codexContextWindow(model); contextWindow > 0 {
		result["context_length"] = contextWindow
		result["context_window"] = contextWindow
	}
	if model.MaxCompletionTokens > 0 {
		result["max_completion_tokens"] = model.MaxCompletionTokens
	}
	if tiers := codexAdditionalSpeedTiers(model); len(tiers) > 0 {
		result["additional_speed_tiers"] = tiers
	}
	if levels := codexSupportedReasoningLevels(model); len(levels) > 0 {
		if defaultLevel := codexDefaultReasoningLevel(levels); defaultLevel != "" {
			result["default_reasoning_level"] = defaultLevel
		}
	}
	if inputModalities := codexInputModalities(model); len(inputModalities) > 0 {
		result["input_modalities"] = inputModalities
	}

	return result
}

func codexDisplayName(model *ModelInfo) string {
	if model == nil {
		return ""
	}
	if display := strings.TrimSpace(model.DisplayName); display != "" {
		return display
	}
	return strings.TrimSpace(model.ID)
}

func codexContextWindow(model *ModelInfo) int {
	if model == nil {
		return 0
	}
	if model.ContextLength > 0 {
		return model.ContextLength
	}
	if model.InputTokenLimit > 0 {
		return model.InputTokenLimit
	}
	return 0
}

func codexAdditionalSpeedTiers(model *ModelInfo) []string {
	if model == nil {
		return nil
	}
	joined := strings.ToLower(strings.Join([]string{model.ID, model.DisplayName, model.Version}, " "))
	if strings.Contains(joined, "spark") {
		return []string{codexFastTier}
	}
	return nil
}

func codexSupportedReasoningLevels(model *ModelInfo) []string {
	if model == nil {
		return nil
	}

	levels := make([]string, 0, len(codexDefaultReasoningLevels))
	seen := make(map[string]struct{}, len(codexDefaultReasoningLevels))

	appendLevel := func(level string) {
		normalized, ok := normalizeCodexReasoningLevel(level)
		if !ok {
			return
		}
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		levels = append(levels, normalized)
	}

	if model.Thinking != nil {
		for _, level := range model.Thinking.Levels {
			appendLevel(level)
		}
	}
	if len(levels) > 0 {
		return levels
	}

	if codexUsesDefaultReasoningLevels(model) {
		for _, level := range codexDefaultReasoningLevels {
			appendLevel(level)
		}
	}

	return levels
}

func codexUsesDefaultReasoningLevels(model *ModelInfo) bool {
	if model == nil {
		return false
	}
	if codexHasSupportedParameter(model, codexReasoningEffortParam) {
		return true
	}

	ownedBy := strings.ToLower(strings.TrimSpace(model.OwnedBy))
	modelType := strings.ToLower(strings.TrimSpace(model.Type))
	return ownedBy == "openai" || modelType == "openai" || modelType == "codex"
}

func normalizeCodexReasoningLevel(level string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none":
		return "none", true
	case "minimal":
		return "minimal", true
	case "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	case "xhigh", "x-high", "x_high", "max":
		return "xhigh", true
	default:
		return "", false
	}
}

func codexReasoningEffortPresets(levels []string) []map[string]any {
	if len(levels) == 0 {
		return []map[string]any{}
	}
	presets := make([]map[string]any, 0, len(levels))
	for _, level := range levels {
		presets = append(presets, map[string]any{
			"effort":      level,
			"description": codexReasoningDescription(level),
		})
	}
	return presets
}

func codexReasoningDescription(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none":
		return "Disable explicit reasoning."
	case "minimal":
		return "Fastest replies with minimal extra reasoning."
	case "low":
		return "Fast replies with light reasoning."
	case "medium":
		return "Balanced speed and reasoning depth."
	case "high":
		return "Deeper reasoning for harder tasks."
	case "xhigh":
		return "Deepest reasoning for the hardest tasks."
	default:
		return "Reasoning effort preset."
	}
}

func codexDefaultReasoningLevel(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	for _, preferred := range []string{"medium", "low", "high", "minimal", "xhigh", "none"} {
		for _, level := range levels {
			if level == preferred {
				return level
			}
		}
	}
	return levels[0]
}

func codexSupportsReasoningSummaries(model *ModelInfo) bool {
	return codexHasSupportedParameter(model, codexReasoningSummaryParam)
}

func codexSupportsVerbosity(model *ModelInfo) bool {
	return codexHasSupportedParameter(model, codexVerbosityParam) || codexHasSupportedParameter(model, codexTextVerbosityParam)
}

func codexSupportsParallelToolCalls(model *ModelInfo) bool {
	return codexHasSupportedParameter(model, codexSupportsParallelToolCallsParam) || codexHasSupportedParameter(model, codexSupportsParallelToolCallsAltParam)
}

func codexSupportsSearchTool(model *ModelInfo) bool {
	return codexHasSupportedParameter(model, codexSupportsSearchToolParam) || codexHasSupportedParameter(model, codexSupportsSearchToolAltParam)
}

func codexHasSupportedParameter(model *ModelInfo, want string) bool {
	if model == nil {
		return false
	}
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return false
	}
	for _, param := range model.SupportedParameters {
		if strings.ToLower(strings.TrimSpace(param)) == want {
			return true
		}
	}
	return false
}

func codexInputModalities(model *ModelInfo) []string {
	if model == nil || len(model.SupportedInputModalities) == 0 {
		return []string{"text", "image"}
	}

	modalities := make([]string, 0, len(model.SupportedInputModalities))
	seen := make(map[string]struct{}, len(model.SupportedInputModalities))
	for _, modality := range model.SupportedInputModalities {
		value := strings.ToLower(strings.TrimSpace(modality))
		switch value {
		case "text", "image":
		default:
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		modalities = append(modalities, value)
	}
	if len(modalities) == 0 {
		return []string{"text", "image"}
	}
	return modalities
}
