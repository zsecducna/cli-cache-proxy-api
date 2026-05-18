// Package claude provides request translation functionality for Anthropic to OpenAI API.
// For Phase 1 Claude-via-GPT routing, this translator is intentionally limited to the
// Chat-Completions-safe subset: text-only messages plus basic generation controls.
// Tool use, tool results, and Claude-specific thinking controls are validated elsewhere
// and must not rely on this translator.
package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToOpenAI parses and transforms an Anthropic API request into an
// OpenAI Chat Completions request restricted to the Phase 1 chat-safe subset.
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","messages":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.IsArray() {
		var stops []string
		stopSequences.ForEach(func(_, value gjson.Result) bool {
			stops = append(stops, value.String())
			return true
		})
		if len(stops) == 1 {
			out, _ = sjson.SetBytes(out, "stop", stops[0])
		} else if len(stops) > 1 {
			out, _ = sjson.SetBytes(out, "stop", stops)
		}
	}
	out, _ = sjson.SetBytes(out, "stream", stream)
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.SetBytes(out, "user", user.String())
	}
	out = preserveClaudeThinkingMetadata(out, root)

	messagesJSON := []byte(`[]`)
	// Claude Code injects an attribution-only system block that must not become OpenAI-visible prompt text.
	messagesJSON = appendTextMessage(messagesJSON, "system", extractClaudeSystemTextParts(root.Get("system")))
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			if role != "user" && role != "assistant" {
				return true
			}
			parts := extractClaudeTextParts(message.Get("content"))
			messagesJSON = appendTextMessage(messagesJSON, role, parts)
			return true
		})
	}

	if parsed := gjson.ParseBytes(messagesJSON); parsed.IsArray() && len(parsed.Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "messages", messagesJSON)
	}
	return out
}

func preserveClaudeThinkingMetadata(out []byte, root gjson.Result) []byte {
	if thinking := root.Get("thinking"); thinking.Exists() && thinking.IsObject() {
		out, _ = sjson.SetRawBytes(out, "thinking", []byte(thinking.Raw))
	}
	if effort := root.Get("output_config.effort"); effort.Exists() && effort.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "output_config.effort", effort.String())
	}
	return out
}

// ConvertClaudeRequestToOpenAIWithTools preserves Claude tool declarations and
// tool transcript turns for the Claude-via-GPT route while still emitting an
// OpenAI Chat Completions request shape.
func ConvertClaudeRequestToOpenAIWithTools(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","messages":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.IsArray() {
		var stops []string
		stopSequences.ForEach(func(_, value gjson.Result) bool {
			stops = append(stops, value.String())
			return true
		})
		if len(stops) == 1 {
			out, _ = sjson.SetBytes(out, "stop", stops[0])
		} else if len(stops) > 1 {
			out, _ = sjson.SetBytes(out, "stop", stops)
		}
	}
	out, _ = sjson.SetBytes(out, "stream", stream)
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.SetBytes(out, "user", user.String())
	}

	messagesJSON := []byte(`[]`)
	// Claude Code injects an attribution-only system block that must not become OpenAI-visible prompt text.
	messagesJSON = appendTextMessage(messagesJSON, "system", extractClaudeSystemTextParts(root.Get("system")))

	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			content := message.Get("content")
			switch role {
			case "user":
				if parts := extractClaudeTextParts(content); len(parts) > 0 {
					messagesJSON = appendTextMessage(messagesJSON, role, parts)
					return true
				}
				if content.IsArray() {
					content.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() != "tool_result" {
							return true
						}
						toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
						toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", part.Get("tool_use_id").String())
						toolMessage, _ = sjson.SetBytes(toolMessage, "content", collectClaudeToolResultOutput(part.Get("content")))
						messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", toolMessage)
						return true
					})
				}
			case "assistant":
				if parts := extractClaudeTextParts(content); len(parts) > 0 {
					messagesJSON = appendTextMessage(messagesJSON, role, parts)
				}
				if content.IsArray() {
					toolCallMessage := []byte(`{"role":"assistant","tool_calls":[]}`)
					toolCallCount := 0
					content.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() != "tool_use" {
							return true
						}
						toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":"{}"}}`)
						toolCall, _ = sjson.SetBytes(toolCall, "id", part.Get("id").String())
						toolCall, _ = sjson.SetBytes(toolCall, "function.name", part.Get("name").String())
						args := part.Get("input")
						if args.Exists() && args.IsObject() {
							toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", args.Raw)
						}
						toolCallMessage, _ = sjson.SetRawBytes(toolCallMessage, "tool_calls.-1", toolCall)
						toolCallCount++
						return true
					})
					if toolCallCount > 0 {
						messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", toolCallMessage)
						if toolCallCount > 1 {
							out, _ = sjson.SetBytes(out, "parallel_tool_calls", true)
						}
					}
				}
			}
			return true
		})
	}

	if parsed := gjson.ParseBytes(messagesJSON); parsed.IsArray() && len(parsed.Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "messages", messagesJSON)
	}

	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			openAITool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{}}}`)
			openAITool, _ = sjson.SetBytes(openAITool, "function.name", tool.Get("name").String())
			if description := tool.Get("description"); description.Exists() {
				openAITool, _ = sjson.SetBytes(openAITool, "function.description", description.String())
			}
			if schema := tool.Get("input_schema"); schema.Exists() && schema.IsObject() {
				openAITool, _ = sjson.SetRawBytes(openAITool, "function.parameters", []byte(schema.Raw))
			}
			out, _ = sjson.SetRawBytes(out, "tools.-1", openAITool)
			return true
		})
	}

	if toolChoice := translateClaudeToolChoice(root.Get("tool_choice")); len(toolChoice) > 0 {
		out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoice)
	}

	return out
}

func extractClaudeTextParts(content gjson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil
		}
		return []string{content.String()}
	}
	if !content.IsArray() {
		return nil
	}
	parts := make([]string, 0, len(content.Array()))
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			text := part.Get("text").String()
			if text != "" {
				parts = append(parts, text)
			}
		}
		return true
	})
	return parts
}

// extractClaudeSystemTextParts keeps real Claude system prompt text while dropping
// Claude Code's synthetic billing attribution block before OpenAI translation.
func extractClaudeSystemTextParts(content gjson.Result) []string {
	parts := extractClaudeTextParts(content)
	if len(parts) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(parts))
	for _, text := range parts {
		// The attribution helper owns prefix/whitespace rules so all Claude translators strip the same marker.
		if util.IsClaudeCodeAttributionSystemText(text) {
			continue
		}
		filtered = append(filtered, text)
	}
	return filtered
}

func appendTextMessage(messagesJSON []byte, role string, parts []string) []byte {
	if len(parts) == 0 {
		return messagesJSON
	}
	messageJSON := []byte(`{"role":"","content":[]}`)
	messageJSON, _ = sjson.SetBytes(messageJSON, "role", role)
	for _, text := range parts {
		contentPart := []byte(`{"type":"text","text":""}`)
		contentPart, _ = sjson.SetBytes(contentPart, "text", text)
		messageJSON, _ = sjson.SetRawBytes(messageJSON, "content.-1", contentPart)
	}
	messagesJSON, _ = sjson.SetRawBytes(messagesJSON, "-1", messageJSON)
	return messagesJSON
}

func collectClaudeToolResultOutput(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsObject() {
		return content.Raw
	}
	if !content.IsArray() {
		return content.Raw
	}
	parts := make([]string, 0, len(content.Array()))
	content.ForEach(func(_, part gjson.Result) bool {
		switch {
		case part.Type == gjson.String:
			parts = append(parts, part.String())
		case part.Get("type").String() == "text":
			parts = append(parts, part.Get("text").String())
		default:
			parts = append(parts, part.Raw)
		}
		return true
	})
	return strings.Join(parts, "\n\n")
}

func translateClaudeToolChoice(toolChoice gjson.Result) []byte {
	if !toolChoice.Exists() {
		return nil
	}
	if toolChoice.Type == gjson.String {
		return []byte(`"` + toolChoice.String() + `"`)
	}
	if !toolChoice.IsObject() {
		return nil
	}
	switch toolChoice.Get("type").String() {
	case "auto":
		return []byte(`"auto"`)
	case "none":
		return []byte(`"none"`)
	case "any", "required":
		return []byte(`"required"`)
	case "tool":
		name := toolChoice.Get("name").String()
		if name == "" {
			return nil
		}
		out := []byte(`{"type":"function","function":{"name":""}}`)
		out, _ = sjson.SetBytes(out, "function.name", name)
		return out
	default:
		return nil
	}
}
