package claude

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

type compatPendingToolTurn struct {
	ids []string
}

// ValidateClaudeViaOpenAIRequest enforces the stricter Phase 1 validator contract
// used by the Claude-via-GPT routing path. It keeps a narrower reason/stage surface
// than the broader compatibility helpers in compatibility.go.
func ValidateClaudeViaOpenAIRequest(raw []byte, supportsResponses, supportsChat, supportsTools, supportsStreaming bool) *CompatibilityError {
	root, err := validateClaudeViaOpenAISyntax(raw)
	if err != nil {
		return err
	}
	if !supportsStreaming {
		return &CompatibilityError{
			Class:   CompatibilityClassStreamingNotSupported,
			Reason:  "streaming_not_supported",
			Stage:   "backend",
			Message: "Claude-via-GPT routing rejected: selected backend does not support streaming.",
		}
	}
	if requestUsesTooling(root) {
		if !supportsTools {
			return &CompatibilityError{
				Class:   CompatibilityClassSurfaceNotSupported,
				Reason:  "tools_not_supported",
				Stage:   "backend",
				Message: "Claude-via-GPT routing rejected: selected backend does not support tools.",
			}
		}
		if supportsResponses {
			return nil
		}
		if supportsChat {
			return &CompatibilityError{
				Class:   CompatibilityClassSurfaceNotSupported,
				Reason:  "surface_not_supported",
				Stage:   "backend",
				Message: "Claude-via-GPT routing rejected: Chat Completions fallback only supports the text-only safe subset.",
			}
		}
		return &CompatibilityError{
			Class:   CompatibilityClassSurfaceNotSupported,
			Reason:  "surface_not_supported",
			Stage:   "backend",
			Message: "Claude-via-GPT routing rejected: selected backend does not expose a supported execution surface.",
		}
	}
	if supportsResponses || supportsChat {
		return nil
	}
	return &CompatibilityError{
		Class:   CompatibilityClassSurfaceNotSupported,
		Reason:  "surface_not_supported",
		Stage:   "backend",
		Message: "Claude-via-GPT routing rejected: selected backend does not expose a supported execution surface.",
	}
}

// FitsClaudeViaOpenAIChatSafeSubset reports whether the request is eligible for
// the text-only Chat Completions fallback.
func FitsClaudeViaOpenAIChatSafeSubset(raw []byte) bool {
	root, err := validateClaudeViaOpenAISyntax(raw)
	if err != nil {
		return false
	}
	if requestUsesTooling(root) {
		return false
	}
	return true
}

func validateClaudeViaOpenAISyntax(raw []byte) (gjson.Result, *CompatibilityError) {
	if !json.Valid(raw) {
		return gjson.Result{}, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"invalid_json",
			"syntax",
			"Claude-via-GPT routing rejected: request body is not valid JSON.",
		)
	}

	root := gjson.ParseBytes(raw)
	stream := root.Get("stream")
	if !stream.Exists() || !stream.Bool() {
		return gjson.Result{}, newCompatValidationError(
			CompatibilityClassStreamingNotSupported,
			"non_stream_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: non-stream requests are not supported in Phase 1.",
		)
	}
	if root.Get("thinking").Exists() || root.Get("output_config.effort").Exists() {
		return gjson.Result{}, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"thinking_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: explicit Claude thinking controls are not supported.",
		)
	}
	if err := validateCompatSystemSyntax(root.Get("system")); err != nil {
		return gjson.Result{}, err
	}
	if err := validateCompatToolDefinitions(root.Get("tools")); err != nil {
		return gjson.Result{}, err
	}

	messages := root.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return gjson.Result{}, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"messages_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: messages must be an array.",
		)
	}

	var pending *compatPendingToolTurn

	for _, message := range messages.Array() {
		role := strings.TrimSpace(message.Get("role").String())
		switch role {
		case "assistant":
			if pending != nil {
				return gjson.Result{}, newCompatValidationError(
					CompatibilityClassIncompatibleRequest,
					"tool_result_partner_missing",
					"syntax",
					"Claude-via-GPT routing rejected: assistant tool turns must be followed immediately by a matching user tool_result turn.",
				)
			}
			ids, hasToolUse, err := validateCompatAssistantMessage(message.Get("content"))
			if err != nil {
				return gjson.Result{}, err
			}
			if hasToolUse {
				pending = &compatPendingToolTurn{ids: ids}
			}
		case "user":
			nextPending, err := validateCompatUserMessage(message.Get("content"), pending)
			if err != nil {
				return gjson.Result{}, err
			}
			pending = nextPending
		default:
			return gjson.Result{}, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"messages_not_supported",
				"syntax",
				"Claude-via-GPT routing rejected: unsupported message role.",
			)
		}
	}

	if pending != nil {
		return gjson.Result{}, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"tool_result_partner_missing",
			"syntax",
			"Claude-via-GPT routing rejected: assistant tool turns must be followed immediately by a matching user tool_result turn.",
		)
	}

	return root, nil
}

func validateCompatSystemSyntax(system gjson.Result) *CompatibilityError {
	if !system.Exists() || system.Type == gjson.String {
		return nil
	}
	if !system.IsArray() {
		return newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"content_type_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: system must be plain text or text blocks only.",
		)
	}
	for _, block := range system.Array() {
		if block.Get("type").String() != "text" {
			return newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"content_type_not_supported",
				"syntax",
				"Claude-via-GPT routing rejected: non-text content blocks are not supported.",
			)
		}
	}
	return nil
}

func validateCompatToolDefinitions(tools gjson.Result) *CompatibilityError {
	if !tools.Exists() {
		return nil
	}
	if !tools.IsArray() {
		return newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"tool_schema_not_object",
			"syntax",
			"Claude-via-GPT routing rejected: tools must be an array of object-schema declarations.",
		)
	}
	for _, tool := range tools.Array() {
		if !tool.Get("input_schema").IsObject() {
			return newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_schema_not_object",
				"syntax",
				"Claude-via-GPT routing rejected: tool input_schema must be a JSON object.",
			)
		}
	}
	return nil
}

func validateCompatAssistantMessage(content gjson.Result) ([]string, bool, *CompatibilityError) {
	if content.Type == gjson.String {
		return nil, false, nil
	}
	if !content.IsArray() {
		return nil, false, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"content_type_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: assistant content must be text or structured blocks.",
		)
	}

	parts := content.Array()
	if len(parts) == 0 {
		return nil, false, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"empty_content_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: empty content arrays are not supported.",
		)
	}

	var ids []string
	seenIDs := make(map[string]struct{}, len(parts))
	hasText := false
	hasToolUse := false
	for _, part := range parts {
		switch part.Get("type").String() {
		case "text":
			hasText = true
		case "tool_use":
			hasToolUse = true
			id := strings.TrimSpace(part.Get("id").String())
			if id == "" {
				return nil, false, newCompatValidationError(
					CompatibilityClassIncompatibleRequest,
					"duplicate_tool_use_id",
					"syntax",
					"Claude-via-GPT routing rejected: tool_use ids must be non-empty and unique.",
				)
			}
			if _, exists := seenIDs[id]; exists {
				return nil, false, newCompatValidationError(
					CompatibilityClassIncompatibleRequest,
					"duplicate_tool_use_id",
					"syntax",
					"Claude-via-GPT routing rejected: tool_use ids must be unique within an assistant turn.",
				)
			}
			if !part.Get("input").IsObject() {
				return nil, false, newCompatValidationError(
					CompatibilityClassIncompatibleRequest,
					"tool_input_not_object",
					"syntax",
					"Claude-via-GPT routing rejected: tool_use input must be a JSON object.",
				)
			}
			seenIDs[id] = struct{}{}
			ids = append(ids, id)
		default:
			return nil, false, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"content_type_not_supported",
				"syntax",
				"Claude-via-GPT routing rejected: non-text content blocks are not supported.",
			)
		}
	}

	if hasText && hasToolUse {
		return nil, false, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"mixed_assistant_content_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: assistant messages must not mix text and tool_use blocks.",
		)
	}

	return ids, hasToolUse, nil
}

func validateCompatUserMessage(content gjson.Result, pending *compatPendingToolTurn) (*compatPendingToolTurn, *CompatibilityError) {
	if pending == nil {
		if err := validateCompatTextOnlyUserMessage(content); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if content.Type == gjson.String {
		return nil, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"tool_result_partner_missing",
			"syntax",
			"Claude-via-GPT routing rejected: tool_result turns must immediately follow assistant tool_use turns.",
		)
	}
	if !content.IsArray() {
		return nil, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"tool_result_partner_missing",
			"syntax",
			"Claude-via-GPT routing rejected: tool_result turns must immediately follow assistant tool_use turns.",
		)
	}

	parts := content.Array()
	if len(parts) == 0 {
		return nil, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"empty_content_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: empty content arrays are not supported.",
		)
	}
	seen := make(map[string]struct{}, len(parts))
	for idx, part := range parts {
		if part.Get("type").String() != "tool_result" {
			return nil, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_result_partner_missing",
				"syntax",
				"Claude-via-GPT routing rejected: tool_result turns must contain only tool_result blocks.",
			)
		}
		id := strings.TrimSpace(part.Get("tool_use_id").String())
		if id == "" {
			return nil, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_result_partner_missing",
				"syntax",
				"Claude-via-GPT routing rejected: tool_result blocks must reference a preceding tool_use id.",
			)
		}
		if _, exists := seen[id]; exists {
			return nil, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"duplicate_tool_result_id",
				"syntax",
				"Claude-via-GPT routing rejected: tool_result ids must be unique within a user turn.",
			)
		}
		seen[id] = struct{}{}
		if idx >= len(pending.ids) {
			return nil, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_result_partner_missing",
				"syntax",
				"Claude-via-GPT routing rejected: every tool_use requires one matching tool_result in the next user turn.",
			)
		}
		if id != pending.ids[idx] {
			return nil, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_result_order_mismatch",
				"syntax",
				"Claude-via-GPT routing rejected: tool_result order must match the preceding assistant tool_use order.",
			)
		}

		resultContent := part.Get("content")
		if resultContent.Type != gjson.String && !resultContent.IsObject() {
			return nil, newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_result_content_not_supported",
				"syntax",
				"Claude-via-GPT routing rejected: tool_result content must be plain text or a JSON object.",
			)
		}
	}
	if len(seen) != len(pending.ids) {
		return nil, newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"tool_result_partner_missing",
			"syntax",
			"Claude-via-GPT routing rejected: every tool_use requires one matching tool_result in the next user turn.",
		)
	}

	return nil, nil
}

func validateCompatTextOnlyUserMessage(content gjson.Result) *CompatibilityError {
	if content.Type == gjson.String {
		return nil
	}
	if !content.IsArray() {
		return newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"content_type_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: user content must be text or structured blocks.",
		)
	}

	parts := content.Array()
	if len(parts) == 0 {
		return newCompatValidationError(
			CompatibilityClassIncompatibleRequest,
			"empty_content_not_supported",
			"syntax",
			"Claude-via-GPT routing rejected: empty content arrays are not supported.",
		)
	}
	for _, part := range parts {
		switch part.Get("type").String() {
		case "text":
			continue
		case "tool_result":
			return newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"tool_result_partner_missing",
				"syntax",
				"Claude-via-GPT routing rejected: tool_result blocks require an immediately preceding assistant tool_use turn.",
			)
		default:
			return newCompatValidationError(
				CompatibilityClassIncompatibleRequest,
				"content_type_not_supported",
				"syntax",
				"Claude-via-GPT routing rejected: non-text content blocks are not supported.",
			)
		}
	}
	return nil
}

func requestUsesTooling(root gjson.Result) bool {
	if root.Get("tools").Exists() || root.Get("tool_choice").Exists() {
		return true
	}
	messages := root.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}
	for _, message := range messages.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "tool_use", "tool_result":
				return true
			}
		}
	}
	return false
}

func canonicalizeToolResultContent(raw string) (string, bool) {
	canonical, err := canonicalizeCompatValue(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return canonical, true
}

func canonicalizeCompatValue(raw string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return "", errors.New("tool result content is not a JSON object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func newCompatValidationError(class, reason, stage, message string) *CompatibilityError {
	return &CompatibilityError{
		Class:   class,
		Reason:  reason,
		Stage:   stage,
		Message: message,
	}
}
