package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type BackendSurface string

const (
	BackendSurfaceResponses       BackendSurface = "responses"
	BackendSurfaceChatCompletions BackendSurface = "chat_completions"
)

const (
	CompatibilityClassIncompatibleRequest   = "claude_via_gpt_incompatible_request"
	CompatibilityClassBackendNotAvailable   = "claude_via_gpt_backend_not_available"
	CompatibilityClassModelNotSupported     = "claude_via_gpt_model_not_supported"
	CompatibilityClassSurfaceNotSupported   = "claude_via_gpt_surface_not_supported"
	CompatibilityClassStreamingNotSupported = "claude_via_gpt_streaming_not_supported"
	CompatibilityClassTranslationFailed     = "claude_via_gpt_translation_failed"
)

type BackendCapabilities struct {
	SupportsOpenAIResponses bool
	SupportsChatCompletions bool
	SupportsTools           bool
	SupportsStreaming       bool
}

func requestWantsStream(root gjson.Result) bool {
	stream := root.Get("stream")
	return stream.Exists() && stream.Bool()
}

type CompatibilityError struct {
	Class   string
	Reason  string
	Stage   string
	Message string
	Surface BackendSurface
}

func (e *CompatibilityError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newCompatibilityError(class, reason, stage string, surface BackendSurface, format string, args ...any) *CompatibilityError {
	return &CompatibilityError{
		Class:   class,
		Reason:  reason,
		Stage:   stage,
		Surface: surface,
		Message: fmt.Sprintf(format, args...),
	}
}

func ValidateClaudeRequestSyntax(raw []byte) error {
	if !json.Valid(raw) {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"invalid_json",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: request body is not valid JSON.",
		)
	}

	root := gjson.ParseBytes(raw)
	if root.Get("thinking").Exists() || root.Get("output_config.effort").Exists() {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"thinking_not_supported",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: explicit Claude thinking controls are not supported.",
		)
	}

	if err := validateSystemBlocks(root.Get("system")); err != nil {
		return err
	}
	if err := validateToolDefinitions(root.Get("tools")); err != nil {
		return err
	}

	messages := root.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"messages_must_be_array",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: messages must be an array.",
		)
	}

	prevAssistantToolUseIDs := map[string]struct{}{}
	prevAssistantHadToolUse := false
	var syntaxErr error
	messages.ForEach(func(_, message gjson.Result) bool {
		role := strings.TrimSpace(message.Get("role").String())
		content := message.Get("content")

		switch role {
		case "assistant":
			ids, hadToolUse, err := validateAssistantMessageSyntax(content)
			if err != nil {
				syntaxErr = err
				return false
			}
			prevAssistantToolUseIDs = ids
			prevAssistantHadToolUse = hadToolUse
		case "user":
			err := validateUserMessageSyntax(content, prevAssistantToolUseIDs, prevAssistantHadToolUse)
			if err != nil {
				syntaxErr = err
				return false
			}
			prevAssistantToolUseIDs = map[string]struct{}{}
			prevAssistantHadToolUse = false
		default:
			syntaxErr = newCompatibilityError(
				CompatibilityClassIncompatibleRequest,
				"invalid_message_role",
				"syntax-pass",
				"",
				"Claude-via-GPT routing rejected: unsupported message role %q.",
				role,
			)
			return false
		}
		return true
	})
	if syntaxErr != nil {
		return syntaxErr
	}
	return nil
}

func ValidateClaudeRequestForSurface(raw []byte, caps BackendCapabilities, surface BackendSurface) ([]byte, error) {
	if err := ValidateClaudeRequestSyntax(raw); err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(raw)
	if requestWantsStream(root) && !caps.SupportsStreaming {
		return nil, newCompatibilityError(
			CompatibilityClassStreamingNotSupported,
			"backend_streaming_required",
			"backend-pass",
			surface,
			"Claude-via-GPT routing rejected: selected backend surface does not support streaming.",
		)
	}

	usesTools := requestUsesTools(root)

	switch surface {
	case BackendSurfaceResponses:
		if !caps.SupportsOpenAIResponses {
			return nil, newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"responses_not_supported",
				"backend-pass",
				surface,
				"Claude-via-GPT routing rejected: selected backend does not support OpenAI Responses.",
			)
		}
		if usesTools && !caps.SupportsTools {
			return nil, newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"tools_not_supported",
				"backend-pass",
				surface,
				"Claude-via-GPT routing rejected: selected backend does not support tools for Responses execution.",
			)
		}
		if err := validateResponsesTranscriptInvariant(root.Get("messages")); err != nil {
			return nil, err
		}
		return canonicalizeToolResultObjects(raw)
	case BackendSurfaceChatCompletions:
		if !caps.SupportsChatCompletions {
			return nil, newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"chat_completions_not_supported",
				"backend-pass",
				surface,
				"Claude-via-GPT routing rejected: selected backend does not support Chat Completions.",
			)
		}
		if usesTools {
			return nil, newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"chat_completions_requires_text_only",
				"backend-pass",
				surface,
				"Claude-via-GPT routing rejected: Chat Completions fallback only supports text-only requests.",
			)
		}
		return raw, nil
	default:
		return nil, newCompatibilityError(
			CompatibilityClassSurfaceNotSupported,
			"unknown_surface",
			"backend-pass",
			surface,
			"Claude-via-GPT routing rejected: unknown backend surface %q.",
			surface,
		)
	}
}

func validateSystemBlocks(system gjson.Result) error {
	if !system.Exists() {
		return nil
	}
	if system.Type == gjson.String {
		return nil
	}
	if !system.IsArray() {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"system_must_be_text",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: system must be a string or array of text blocks.",
		)
	}
	var validationErr error
	system.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			validationErr = newCompatibilityError(
				CompatibilityClassIncompatibleRequest,
				fmt.Sprintf("unsupported_system_block_%s", block.Get("type").String()),
				"syntax-pass",
				"",
				"Claude-via-GPT routing rejected: system uses unsupported content block %q.",
				block.Get("type").String(),
			)
			return false
		}
		return true
	})
	return validationErr
}

func validateToolDefinitions(tools gjson.Result) error {
	if !tools.Exists() {
		return nil
	}
	if !tools.IsArray() {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"tools_must_be_array",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: tools must be an array.",
		)
	}
	var validationErr error
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			validationErr = newCompatibilityError(
				CompatibilityClassIncompatibleRequest,
				"tool_name_required",
				"syntax-pass",
				"",
				"Claude-via-GPT routing rejected: tool names must be non-empty strings.",
			)
			return false
		}
		if description := tool.Get("description"); description.Exists() && description.Type != gjson.String {
			validationErr = newCompatibilityError(
				CompatibilityClassIncompatibleRequest,
				"tool_description_must_be_string",
				"syntax-pass",
				"",
				"Claude-via-GPT routing rejected: tool description for %q must be a string.",
				name,
			)
			return false
		}
		inputSchema := tool.Get("input_schema")
		if !inputSchema.Exists() || !inputSchema.IsObject() {
			validationErr = newCompatibilityError(
				CompatibilityClassIncompatibleRequest,
				"tool_schema_must_be_object",
				"syntax-pass",
				"",
				"Claude-via-GPT routing rejected: tool %q input_schema must be a JSON object.",
				name,
			)
			return false
		}
		return true
	})
	return validationErr
}

func validateAssistantMessageSyntax(content gjson.Result) (map[string]struct{}, bool, error) {
	if content.Type == gjson.String {
		return map[string]struct{}{}, false, nil
	}
	if !content.IsArray() {
		return nil, false, newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"assistant_content_must_be_array",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: assistant content must be a text string or array.",
		)
	}
	if len(content.Array()) == 0 {
		return nil, false, newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"empty_content_array",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: content arrays must not be empty.",
		)
	}

	ids := make(map[string]struct{})
	hasText := false
	hasToolUse := false
	var validationErr error
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "text":
			hasText = true
		case "tool_use":
			hasToolUse = true
			id := strings.TrimSpace(part.Get("id").String())
			name := strings.TrimSpace(part.Get("name").String())
			if id == "" {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_use_id_required",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_use blocks require non-empty ids.",
				)
				return false
			}
			if name == "" {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_use_name_required",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_use blocks require non-empty names.",
				)
				return false
			}
			if _, exists := ids[id]; exists {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"duplicate_tool_use_id",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: assistant tool_use ids must be unique within a turn.",
				)
				return false
			}
			ids[id] = struct{}{}
			input := part.Get("input")
			if !input.Exists() || !input.IsObject() {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_use_input_must_be_object",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_use input must be a JSON object.",
				)
				return false
			}
		default:
			validationErr = unsupportedContentBlockError(part.Get("type").String())
			return false
		}
		return true
	})
	if validationErr != nil {
		return nil, false, validationErr
	}
	if hasText && hasToolUse {
		return nil, false, newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"assistant_mixed_text_and_tool_use",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: assistant messages must not mix text and tool_use blocks.",
		)
	}
	return ids, hasToolUse, nil
}

func validateUserMessageSyntax(content gjson.Result, prevAssistantToolUseIDs map[string]struct{}, prevAssistantHadToolUse bool) error {
	if content.Type == gjson.String {
		return nil
	}
	if !content.IsArray() {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"user_content_must_be_array",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: user content must be a text string or array.",
		)
	}
	if len(content.Array()) == 0 {
		return newCompatibilityError(
			CompatibilityClassIncompatibleRequest,
			"empty_content_array",
			"syntax-pass",
			"",
			"Claude-via-GPT routing rejected: content arrays must not be empty.",
		)
	}

	seenToolResultIDs := make(map[string]struct{})
	var validationErr error
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "text":
			return true
		case "tool_result":
			toolUseID := strings.TrimSpace(part.Get("tool_use_id").String())
			if toolUseID == "" {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_result_tool_use_id_required",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_result blocks require non-empty tool_use_id values.",
				)
				return false
			}
			if _, exists := seenToolResultIDs[toolUseID]; exists {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"duplicate_tool_result_for_tool_use",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: multiple tool_result blocks referenced tool_use_id %q in the same turn.",
					toolUseID,
				)
				return false
			}
			if !prevAssistantHadToolUse {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_result_missing_preceding_tool_use",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_result %q did not reference a tool_use from the immediately preceding assistant turn.",
					toolUseID,
				)
				return false
			}
			if _, exists := prevAssistantToolUseIDs[toolUseID]; !exists {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_result_missing_preceding_tool_use",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_result %q did not reference a tool_use from the immediately preceding assistant turn.",
					toolUseID,
				)
				return false
			}
			seenToolResultIDs[toolUseID] = struct{}{}
			resultContent := part.Get("content")
			if resultContent.Type != gjson.String && !resultContent.IsObject() {
				validationErr = newCompatibilityError(
					CompatibilityClassIncompatibleRequest,
					"tool_result_content_must_be_text_or_object",
					"syntax-pass",
					"",
					"Claude-via-GPT routing rejected: tool_result content must be plain text or a JSON object.",
				)
				return false
			}
			return true
		default:
			validationErr = unsupportedContentBlockError(part.Get("type").String())
			return false
		}
	})
	return validationErr
}

func validateResponsesTranscriptInvariant(messages gjson.Result) error {
	arr := messages.Array()
	for i := 0; i < len(arr); i++ {
		toolUseIDs := assistantToolUseOrder(arr[i])
		if len(toolUseIDs) == 0 {
			continue
		}
		if i+1 >= len(arr) || arr[i+1].Get("role").String() != "user" {
			return newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"tool_result_turn_missing",
				"backend-pass",
				BackendSurfaceResponses,
				"Claude-via-GPT routing rejected: Responses tool turns require an immediately following user tool_result turn.",
			)
		}
		userContent := arr[i+1].Get("content")
		if !userContent.IsArray() || len(userContent.Array()) == 0 {
			return newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"tool_result_turn_must_not_mix_text",
				"backend-pass",
				BackendSurfaceResponses,
				"Claude-via-GPT routing rejected: Responses tool_result turns must contain only tool_result blocks.",
			)
		}
		toolResultIDs := make([]string, 0, len(userContent.Array()))
		for _, part := range userContent.Array() {
			if part.Get("type").String() != "tool_result" {
				return newCompatibilityError(
					CompatibilityClassSurfaceNotSupported,
					"tool_result_turn_must_not_mix_text",
					"backend-pass",
					BackendSurfaceResponses,
					"Claude-via-GPT routing rejected: Responses tool_result turns must contain only tool_result blocks.",
				)
			}
			toolResultIDs = append(toolResultIDs, part.Get("tool_use_id").String())
		}
		if len(toolUseIDs) != len(toolResultIDs) {
			return newCompatibilityError(
				CompatibilityClassSurfaceNotSupported,
				"tool_result_count_mismatch",
				"backend-pass",
				BackendSurfaceResponses,
				"Claude-via-GPT routing rejected: every tool_use in a Responses turn must have exactly one matching tool_result.",
			)
		}
		for idx := range toolUseIDs {
			if toolUseIDs[idx] != toolResultIDs[idx] {
				return newCompatibilityError(
					CompatibilityClassSurfaceNotSupported,
					"tool_result_order_mismatch",
					"backend-pass",
					BackendSurfaceResponses,
					"Claude-via-GPT routing rejected: tool_result order must match the preceding assistant tool_use order.",
				)
			}
		}
	}
	return nil
}

func requestUsesTools(root gjson.Result) bool {
	if root.Get("tools").Exists() || root.Get("tool_choice").Exists() {
		return true
	}
	messages := root.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}
	usesTools := false
	messages.ForEach(func(_, message gjson.Result) bool {
		content := message.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				t := part.Get("type").String()
				if t == "tool_use" || t == "tool_result" {
					usesTools = true
					return false
				}
				return true
			})
		}
		return !usesTools
	})
	return usesTools
}

func assistantToolUseOrder(message gjson.Result) []string {
	if message.Get("role").String() != "assistant" {
		return nil
	}
	content := message.Get("content")
	if !content.IsArray() {
		return nil
	}
	ids := make([]string, 0)
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "tool_use" {
			ids = append(ids, part.Get("id").String())
		}
		return true
	})
	return ids
}

func canonicalizeToolResultObjects(raw []byte) ([]byte, error) {
	root := gjson.ParseBytes(raw)
	messages := root.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return raw, nil
	}
	updated := raw
	for mi, message := range messages.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for ci, part := range content.Array() {
			if part.Get("type").String() != "tool_result" {
				continue
			}
			resultContent := part.Get("content")
			if !resultContent.IsObject() {
				continue
			}
			canonical, err := canonicalizeJSONObject(resultContent.Raw)
			if err != nil {
				return nil, newCompatibilityError(
					CompatibilityClassTranslationFailed,
					"tool_result_canonicalization_failed",
					"backend-pass",
					BackendSurfaceResponses,
					"Claude-via-GPT routing rejected: could not canonicalize tool_result JSON content.",
				)
			}
			path := fmt.Sprintf("messages.%d.content.%d.content", mi, ci)
			updated, err = sjson.SetRawBytes(updated, path, []byte(canonical))
			if err != nil {
				return nil, newCompatibilityError(
					CompatibilityClassTranslationFailed,
					"tool_result_canonicalization_failed",
					"backend-pass",
					BackendSurfaceResponses,
					"Claude-via-GPT routing rejected: could not canonicalize tool_result JSON content.",
				)
			}
		}
	}
	return updated, nil
}

func canonicalizeJSONObject(raw string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return "", fmt.Errorf("tool result content is not an object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func unsupportedContentBlockError(blockType string) error {
	if strings.TrimSpace(blockType) == "" {
		blockType = "unknown"
	}
	return newCompatibilityError(
		CompatibilityClassIncompatibleRequest,
		fmt.Sprintf("unsupported_content_block_%s", blockType),
		"syntax-pass",
		"",
		"Claude-via-GPT routing rejected: request uses unsupported Claude content block %q.",
		blockType,
	)
}
