package responses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// LiftOpenAIChatCompletionsRequestToOpenAIResponses converts an OpenAI Chat Completions
// request into an OpenAI Responses request without registering it as a default translator path.
// It is intentionally scoped to the Phase 1 Claude-via-GPT supported subset:
// text-only turns plus tool calls / tool results that preserve ordering.
func LiftOpenAIChatCompletionsRequestToOpenAIResponses(modelName string, inputRawJSON []byte) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	if v := root.Get("max_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, "max_output_tokens", v.Int())
	}
	if v := root.Get("parallel_tool_calls"); v.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", v.Bool())
	}
	if v := root.Get("stream"); v.Exists() {
		out, _ = sjson.SetBytes(out, "stream", v.Bool())
	}
	if v := root.Get("temperature"); v.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", v.Float())
	}
	if v := root.Get("top_p"); v.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", v.Float())
	}
	if v := root.Get("user"); v.Exists() {
		out, _ = sjson.SetBytes(out, "user", v.Value())
	}
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning.effort", effort)
		}
	}
	if v := root.Get("tool_choice"); v.Exists() {
		out, _ = sjson.SetBytes(out, "tool_choice", v.Value())
	}

	var instructions []string
	appendInput := func(raw []byte) {
		out, _ = sjson.SetRawBytes(out, "input.-1", raw)
	}

	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			role := strings.TrimSpace(message.Get("role").String())
			if role == "" {
				return true
			}

			if role == "system" {
				if text := collectChatCompletionsMessageText(message.Get("content")); text != "" {
					instructions = append(instructions, text)
				}
				return true
			}

			if role == "tool" {
				output := collectToolMessageOutput(message.Get("content"))
				item := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
				item, _ = sjson.SetBytes(item, "call_id", message.Get("tool_call_id").String())
				item, _ = sjson.SetBytes(item, "output", output)
				appendInput(item)
				return true
			}

			if textParts := buildResponsesMessageContent(role, message.Get("content")); len(textParts) > 0 {
				msg := []byte(`{"type":"message","role":"","content":[]}`)
				msg, _ = sjson.SetBytes(msg, "role", role)
				for _, part := range textParts {
					msg, _ = sjson.SetRawBytes(msg, "content.-1", part)
				}
				appendInput(msg)
			}

			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					item := []byte(`{"type":"function_call","call_id":"","name":"","arguments":""}`)
					item, _ = sjson.SetBytes(item, "call_id", toolCall.Get("id").String())
					item, _ = sjson.SetBytes(item, "name", toolCall.Get("function.name").String())
					item, _ = sjson.SetBytes(item, "arguments", toolCall.Get("function.arguments").String())
					appendInput(item)
					return true
				})
			}

			return true
		})
	}

	if len(instructions) > 0 {
		out, _ = sjson.SetBytes(out, "instructions", strings.Join(instructions, "\n\n"))
	}

	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			function := tool.Get("function")
			if !function.Exists() {
				return true
			}

			respTool := []byte(`{"type":"function","name":"","description":"","parameters":{}}`)
			respTool, _ = sjson.SetBytes(respTool, "name", function.Get("name").String())
			respTool, _ = sjson.SetBytes(respTool, "description", function.Get("description").String())
			if parameters := function.Get("parameters"); parameters.Exists() {
				respTool, _ = sjson.SetRawBytes(respTool, "parameters", []byte(parameters.Raw))
			}
			out, _ = sjson.SetRawBytes(out, "tools.-1", respTool)
			return true
		})
	}

	return out
}

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions converts OpenAI responses format to OpenAI chat completions format.
// It transforms the OpenAI responses API format (with instructions and input array) into the standard
// OpenAI chat completions format (with messages array and system content).
//
// The conversion handles:
// 1. Model name and streaming configuration
// 2. Instructions to system message conversion
// 3. Input array to messages array transformation
// 4. Tool definitions and tool choice conversion
// 5. Function calls and function results handling
// 6. Generation parameters mapping (max_tokens, reasoning, etc.)
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data in OpenAI responses format
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI chat completions format
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI chat completions template with default values
	out := []byte(`{"model":"","messages":[],"stream":false}`)

	root := gjson.ParseBytes(rawJSON)

	// Set model name
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Set stream configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Map generation parameters from responses format to chat completions format
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
	}

	// Convert instructions to system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = sjson.SetBytes(systemMessage, "content", instructions.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", systemMessage)
	}

	// Convert input array to messages
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			itemType := item.Get("type").String()
			if itemType == "" && item.Get("role").String() != "" {
				itemType = "message"
			}

			switch itemType {
			case "message", "":
				// Handle regular message conversion
				role := item.Get("role").String()
				if role == "developer" {
					role = "user"
				}
				message := []byte(`{"role":"","content":[]}`)
				message, _ = sjson.SetBytes(message, "role", role)

				if content := item.Get("content"); content.Exists() && content.IsArray() {
					var messageContent string
					var toolCalls []interface{}

					content.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						switch contentType {
						case "input_text", "output_text":
							text := contentItem.Get("text").String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", text)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
							contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						}
						return true
					})

					if messageContent != "" {
						message, _ = sjson.SetBytes(message, "content", messageContent)
					}

					if len(toolCalls) > 0 {
						message, _ = sjson.SetBytes(message, "tool_calls", toolCalls)
					}
				} else if content.Type == gjson.String {
					message, _ = sjson.SetBytes(message, "content", content.String())
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", message)

			case "function_call":
				// Handle function call conversion to assistant message with tool_calls
				assistantMessage := []byte(`{"role":"assistant","tool_calls":[]}`)

				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)

				if callId := item.Get("call_id"); callId.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "id", callId.String())
				}

				if name := item.Get("name"); name.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.name", name.String())
				}

				if arguments := item.Get("arguments"); arguments.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments.String())
				}

				assistantMessage, _ = sjson.SetRawBytes(assistantMessage, "tool_calls.0", toolCall)
				out, _ = sjson.SetRawBytes(out, "messages.-1", assistantMessage)

			case "function_call_output":
				// Handle function call output conversion to tool message
				toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)

				if callId := item.Get("call_id"); callId.Exists() {
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callId.String())
				}

				if output := item.Get("output"); output.Exists() {
					toolMessage, _ = sjson.SetBytes(toolMessage, "content", output.String())
				}

				out, _ = sjson.SetRawBytes(out, "messages.-1", toolMessage)
			}

			return true
		})
	} else if input.Type == gjson.String {
		msg := []byte(`{}`)
		msg, _ = sjson.SetBytes(msg, "role", "user")
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}

	// Convert tools from responses format to chat completions format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var chatCompletionsTools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			// Built-in tools (e.g. {"type":"web_search"}) are already compatible with the Chat Completions schema.
			// Only function tools need structural conversion because Chat Completions nests details under "function".
			toolType := tool.Get("type").String()
			if toolType != "" && toolType != "function" && tool.IsObject() {
				// Almost all providers lack built-in tools, so we just ignore them.
				// chatCompletionsTools = append(chatCompletionsTools, tool.Value())
				return true
			}

			chatTool := []byte(`{"type":"function","function":{}}`)

			// Convert tool structure from responses format to chat completions format
			function := []byte(`{"name":"","description":"","parameters":{}}`)

			if name := tool.Get("name"); name.Exists() {
				function, _ = sjson.SetBytes(function, "name", name.String())
			}

			if description := tool.Get("description"); description.Exists() {
				function, _ = sjson.SetBytes(function, "description", description.String())
			}

			if parameters := tool.Get("parameters"); parameters.Exists() {
				function, _ = sjson.SetRawBytes(function, "parameters", []byte(parameters.Raw))
			}

			chatTool, _ = sjson.SetRawBytes(chatTool, "function", function)
			chatCompletionsTools = append(chatCompletionsTools, gjson.ParseBytes(chatTool).Value())

			return true
		})

		if len(chatCompletionsTools) > 0 {
			out, _ = sjson.SetBytes(out, "tools", chatCompletionsTools)
		}
	}

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
		}
	}

	// Convert tool_choice if present
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetBytes(out, "tool_choice", toolChoice.String())
	}

	return out
}

func collectChatCompletionsMessageText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}

	var parts []string
	content.ForEach(func(_, item gjson.Result) bool {
		text := strings.TrimSpace(item.Get("text").String())
		if text != "" {
			parts = append(parts, text)
		}
		return true
	})
	return strings.Join(parts, "\n\n")
}

func buildResponsesMessageContent(role string, content gjson.Result) [][]byte {
	partType := "input_text"
	if role == "assistant" {
		partType = "output_text"
	}

	var parts [][]byte
	appendText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		part := []byte(`{"type":"","text":""}`)
		part, _ = sjson.SetBytes(part, "type", partType)
		part, _ = sjson.SetBytes(part, "text", text)
		parts = append(parts, part)
	}

	if !content.Exists() {
		return parts
	}
	if content.Type == gjson.String {
		appendText(content.String())
		return parts
	}
	if !content.IsArray() {
		return parts
	}

	content.ForEach(func(_, item gjson.Result) bool {
		switch {
		case item.Type == gjson.String:
			appendText(item.String())
		case item.Get("type").String() == "text":
			appendText(item.Get("text").String())
		}
		return true
	})
	return parts
}

func collectToolMessageOutput(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		content.ForEach(func(_, item gjson.Result) bool {
			switch {
			case item.Type == gjson.String:
				parts = append(parts, item.String())
			case item.Get("type").String() == "text":
				parts = append(parts, item.Get("text").String())
			default:
				parts = append(parts, item.Raw)
			}
			return true
		})
		return strings.Join(parts, "\n\n")
	}
	if content.IsObject() {
		if text := content.Get("text"); text.Exists() && text.Type == gjson.String {
			return text.String()
		}
		return content.Raw
	}
	return content.String()
}
