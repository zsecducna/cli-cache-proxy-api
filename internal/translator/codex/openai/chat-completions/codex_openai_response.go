// Package openai provides response translation functionality for Codex to OpenAI API compatibility.
// This package handles the conversion of Codex API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexNonStreamToolCall struct {
	ID                string
	Name              string
	Arguments         string
	HasArgumentsDelta bool
}

var (
	dataTag = []byte("data:")
)

// ConvertCliToOpenAIParams holds parameters for response conversion.
type ConvertCliToOpenAIParams struct {
	ResponseID                string
	CreatedAt                 int64
	Model                     string
	FunctionCallIndex         int
	HasReceivedArgumentsDelta bool
	HasToolCallAnnounced      bool
	LastImageHashByItemID     map[string][32]byte
}

// ConvertCodexResponseToOpenAI translates a single chunk of a streaming response from the
// Codex API format to the OpenAI Chat Completions streaming format.
// It processes various Codex event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertCodexResponseToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertCliToOpenAIParams{
			Model:                     modelName,
			CreatedAt:                 0,
			ResponseID:                "",
			FunctionCallIndex:         -1,
			HasReceivedArgumentsDelta: false,
			HasToolCallAnnounced:      false,
			LastImageHashByItemID:     make(map[string][32]byte),
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	// Initialize the OpenAI SSE template.
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{},"finish_reason":null,"native_finish_reason":null}]}`)

	rootResult := gjson.ParseBytes(rawJSON)

	typeResult := rootResult.Get("type")
	dataType := typeResult.String()
	if dataType == "response.created" {
		(*param).(*ConvertCliToOpenAIParams).ResponseID = rootResult.Get("response.id").String()
		(*param).(*ConvertCliToOpenAIParams).CreatedAt = rootResult.Get("response.created_at").Int()
		(*param).(*ConvertCliToOpenAIParams).Model = rootResult.Get("response.model").String()
		if (*param).(*ConvertCliToOpenAIParams).LastImageHashByItemID == nil {
			(*param).(*ConvertCliToOpenAIParams).LastImageHashByItemID = make(map[string][32]byte)
		}
		return [][]byte{}
	}

	// Extract and set the model version.
	cachedModel := (*param).(*ConvertCliToOpenAIParams).Model
	if modelResult := gjson.GetBytes(rawJSON, "model"); modelResult.Exists() {
		template, _ = sjson.SetBytes(template, "model", modelResult.String())
	} else if cachedModel != "" {
		template, _ = sjson.SetBytes(template, "model", cachedModel)
	} else if modelName != "" {
		template, _ = sjson.SetBytes(template, "model", modelName)
	}

	template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertCliToOpenAIParams).CreatedAt)

	// Extract and set the response ID.
	template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertCliToOpenAIParams).ResponseID)

	// Extract and set usage metadata (token counts).
	if usageResult := gjson.GetBytes(rawJSON, "response.usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", inputTokensResult.Int())
		}
		if cachedTokensResult := usageResult.Get("input_tokens_details.cached_tokens"); cachedTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokensResult.Int())
		}
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	if dataType == "response.reasoning_summary_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", deltaResult.String())
		}
	} else if dataType == "response.reasoning_summary_text.done" {
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", "\n\n")
	} else if dataType == "response.output_text.delta" {
		if deltaResult := rootResult.Get("delta"); deltaResult.Exists() {
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetBytes(template, "choices.0.delta.content", deltaResult.String())
		}
	} else if dataType == "response.image_generation_call.partial_image" {
		itemID := rootResult.Get("item_id").String()
		b64 := rootResult.Get("partial_image_b64").String()
		if b64 == "" {
			return [][]byte{}
		}
		if itemID != "" {
			p := (*param).(*ConvertCliToOpenAIParams)
			if p.LastImageHashByItemID == nil {
				p.LastImageHashByItemID = make(map[string][32]byte)
			}
			hash := sha256.Sum256([]byte(b64))
			if last, ok := p.LastImageHashByItemID[itemID]; ok && last == hash {
				return [][]byte{}
			}
			p.LastImageHashByItemID[itemID] = hash
		}

		outputFormat := rootResult.Get("output_format").String()
		mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
		imageURL := "data:" + mimeType + ";base64," + b64

		imagesResult := gjson.GetBytes(template, "choices.0.delta.images")
		if !imagesResult.Exists() || !imagesResult.IsArray() {
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.images", []byte(`[]`))
		}
		imageIndex := len(gjson.GetBytes(template, "choices.0.delta.images").Array())
		imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		imagePayload, _ = sjson.SetBytes(imagePayload, "index", imageIndex)
		imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)

		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.images.-1", imagePayload)
	} else if dataType == "response.completed" {
		finishReason := "stop"
		if (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex != -1 {
			finishReason = "tool_calls"
		}
		template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
		template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", finishReason)
	} else if dataType == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() || itemResult.Get("type").String() != "function_call" {
			return [][]byte{}
		}

		// Increment index for this new function call item.
		(*param).(*ConvertCliToOpenAIParams).FunctionCallIndex++
		(*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta = false
		(*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced = true

		functionCallItemTemplate := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := itemResult.Get("name").String()
		rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
		if orig, ok := rev[name]; ok {
			name = orig
		}
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.name", name)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", "")

		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.function_call_arguments.delta" {
		(*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta = true

		deltaValue := rootResult.Get("delta").String()
		functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", deltaValue)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.function_call_arguments.done" {
		if (*param).(*ConvertCliToOpenAIParams).HasReceivedArgumentsDelta {
			// Arguments were already streamed via delta events; nothing to emit.
			return [][]byte{}
		}

		// Fallback: no delta events were received, emit the full arguments as a single chunk.
		fullArgs := rootResult.Get("arguments").String()
		functionCallItemTemplate := []byte(`{"index":0,"function":{"arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", fullArgs)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else if dataType == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		if !itemResult.Exists() {
			return [][]byte{}
		}
		itemType := itemResult.Get("type").String()
		if itemType == "image_generation_call" {
			itemID := itemResult.Get("id").String()
			b64 := itemResult.Get("result").String()
			if b64 == "" {
				return [][]byte{}
			}
			if itemID != "" {
				p := (*param).(*ConvertCliToOpenAIParams)
				if p.LastImageHashByItemID == nil {
					p.LastImageHashByItemID = make(map[string][32]byte)
				}
				hash := sha256.Sum256([]byte(b64))
				if last, ok := p.LastImageHashByItemID[itemID]; ok && last == hash {
					return [][]byte{}
				}
				p.LastImageHashByItemID[itemID] = hash
			}

			outputFormat := itemResult.Get("output_format").String()
			mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
			imageURL := "data:" + mimeType + ";base64," + b64

			imagesResult := gjson.GetBytes(template, "choices.0.delta.images")
			if !imagesResult.Exists() || !imagesResult.IsArray() {
				template, _ = sjson.SetRawBytes(template, "choices.0.delta.images", []byte(`[]`))
			}
			imageIndex := len(gjson.GetBytes(template, "choices.0.delta.images").Array())
			imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
			imagePayload, _ = sjson.SetBytes(imagePayload, "index", imageIndex)
			imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)

			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
			template, _ = sjson.SetRawBytes(template, "choices.0.delta.images.-1", imagePayload)
			return [][]byte{template}
		}
		if itemType != "function_call" {
			return [][]byte{}
		}

		if (*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced {
			// Tool call was already announced via output_item.added; skip emission.
			(*param).(*ConvertCliToOpenAIParams).HasToolCallAnnounced = false
			return [][]byte{}
		}

		// Fallback path: model skipped output_item.added, so emit complete tool call now.
		(*param).(*ConvertCliToOpenAIParams).FunctionCallIndex++

		functionCallItemTemplate := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "index", (*param).(*ConvertCliToOpenAIParams).FunctionCallIndex)

		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte(`[]`))
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "id", itemResult.Get("call_id").String())

		// Restore original tool name if it was shortened.
		name := itemResult.Get("name").String()
		rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
		if orig, ok := rev[name]; ok {
			name = orig
		}
		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.name", name)

		functionCallItemTemplate, _ = sjson.SetBytes(functionCallItemTemplate, "function.arguments", itemResult.Get("arguments").String())
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", functionCallItemTemplate)

	} else {
		return [][]byte{}
	}

	return [][]byte{template}
}

// ConvertCodexResponseToOpenAINonStream converts a non-streaming Codex response to a non-streaming OpenAI response.
// This function processes the complete Codex response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	responseResult, createdResult, contentText, reasoningText, toolCalls, ok := collectCodexOpenAINonStreamResponse(originalRequestRawJSON, rawJSON)
	if !ok {
		return []byte{}
	}

	unixTimestamp := time.Now().Unix()

	template := []byte(`{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)

	// Extract and set the model version.
	if modelResult := responseResult.Get("model"); modelResult.Exists() {
		template, _ = sjson.SetBytes(template, "model", modelResult.String())
	} else if modelResult := createdResult.Get("model"); modelResult.Exists() {
		template, _ = sjson.SetBytes(template, "model", modelResult.String())
	}

	// Extract and set the creation timestamp.
	if createdAtResult := responseResult.Get("created_at"); createdAtResult.Exists() {
		template, _ = sjson.SetBytes(template, "created", createdAtResult.Int())
	} else if createdAtResult := createdResult.Get("created_at"); createdAtResult.Exists() {
		template, _ = sjson.SetBytes(template, "created", createdAtResult.Int())
	} else {
		template, _ = sjson.SetBytes(template, "created", unixTimestamp)
	}

	// Extract and set the response ID.
	if idResult := responseResult.Get("id"); idResult.Exists() {
		template, _ = sjson.SetBytes(template, "id", idResult.String())
	} else if idResult := createdResult.Get("id"); idResult.Exists() {
		template, _ = sjson.SetBytes(template, "id", idResult.String())
	}

	// Extract and set usage metadata (token counts).
	if usageResult := responseResult.Get("usage"); usageResult.Exists() {
		if outputTokensResult := usageResult.Get("output_tokens"); outputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", outputTokensResult.Int())
		}
		if totalTokensResult := usageResult.Get("total_tokens"); totalTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokensResult.Int())
		}
		if inputTokensResult := usageResult.Get("input_tokens"); inputTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", inputTokensResult.Int())
		}
		if cachedTokensResult := usageResult.Get("input_tokens_details.cached_tokens"); cachedTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokensResult.Int())
		}
		if reasoningTokensResult := usageResult.Get("output_tokens_details.reasoning_tokens"); reasoningTokensResult.Exists() {
			template, _ = sjson.SetBytes(template, "usage.completion_tokens_details.reasoning_tokens", reasoningTokensResult.Int())
		}
	}

	// Set content and reasoning content if found.
	if contentText != "" {
		template, _ = sjson.SetBytes(template, "choices.0.message.content", contentText)
		template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
	}

	if reasoningText != "" {
		template, _ = sjson.SetBytes(template, "choices.0.message.reasoning_content", reasoningText)
		template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
	}

	var images [][]byte
	outputResult := responseResult.Get("output")
	if outputResult.IsArray() {
		for _, outputItem := range outputResult.Array() {
			if outputItem.Get("type").String() != "image_generation_call" {
				continue
			}
			b64 := outputItem.Get("result").String()
			if b64 == "" {
				continue
			}
			outputFormat := outputItem.Get("output_format").String()
			mimeType := mimeTypeFromCodexOutputFormat(outputFormat)
			imageURL := "data:" + mimeType + ";base64," + b64

			imagePayload := []byte(`{"type":"image_url","image_url":{"url":""}}`)
			imagePayload, _ = sjson.SetBytes(imagePayload, "index", len(images))
			imagePayload, _ = sjson.SetBytes(imagePayload, "image_url.url", imageURL)
			images = append(images, imagePayload)
		}
	}

	// Add tool calls if any.
	if len(toolCalls) > 0 {
		template, _ = sjson.SetRawBytes(template, "choices.0.message.tool_calls", []byte(`[]`))
		for _, toolCall := range toolCalls {
			functionCallTemplate := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
			if toolCall.ID != "" {
				functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "id", toolCall.ID)
			}
			if toolCall.Name != "" {
				functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.name", toolCall.Name)
			}
			functionCallTemplate, _ = sjson.SetBytes(functionCallTemplate, "function.arguments", toolCall.Arguments)
			template, _ = sjson.SetRawBytes(template, "choices.0.message.tool_calls.-1", functionCallTemplate)
		}
		template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
	}

	if len(images) > 0 {
		template, _ = sjson.SetRawBytes(template, "choices.0.message.images", []byte(`[]`))
		for _, image := range images {
			template, _ = sjson.SetRawBytes(template, "choices.0.message.images.-1", image)
		}
		template, _ = sjson.SetBytes(template, "choices.0.message.role", "assistant")
	}

	// Extract and set the finish reason based on status
	if statusResult := responseResult.Get("status"); statusResult.Exists() {
		status := statusResult.String()
		if status == "completed" {
			finishReason := "stop"
			if len(toolCalls) > 0 {
				finishReason = "tool_calls"
			}
			template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finishReason)
			template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", finishReason)
		}
	}

	return template
}

func collectCodexOpenAINonStreamResponse(originalRequestRawJSON, rawJSON []byte) (gjson.Result, gjson.Result, string, string, []codexNonStreamToolCall, bool) {
	rootResult := gjson.ParseBytes(rawJSON)
	if rootResult.Get("type").String() == "response.completed" {
		responseResult := rootResult.Get("response")
		if !responseResult.Exists() {
			return gjson.Result{}, gjson.Result{}, "", "", nil, false
		}
		contentText, reasoningText, toolCalls := extractCodexOpenAINonStreamOutput(originalRequestRawJSON, responseResult.Get("output"))
		return responseResult, gjson.Result{}, contentText, reasoningText, toolCalls, true
	}

	rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
	var createdResult gjson.Result
	var responseResult gjson.Result
	var contentBuilder bytes.Buffer
	var reasoningBuilder bytes.Buffer
	var toolCalls []codexNonStreamToolCall

	lines := bytes.Split(rawJSON, []byte{'\n'})
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}

		payload := bytes.TrimSpace(line[len(dataTag):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		event := gjson.ParseBytes(payload)
		switch event.Get("type").String() {
		case "response.created":
			createdResult = event.Get("response")
		case "response.output_text.delta":
			contentBuilder.WriteString(event.Get("delta").String())
		case "response.reasoning_summary_text.delta":
			reasoningBuilder.WriteString(event.Get("delta").String())
		case "response.reasoning_summary_text.done":
			if reasoningBuilder.Len() == 0 {
				reasoningBuilder.WriteString(event.Get("text").String())
			}
		case "response.output_item.added":
			itemResult := event.Get("item")
			if itemResult.Get("type").String() != "function_call" {
				continue
			}
			toolCalls = append(toolCalls, codexNonStreamToolCall{
				ID:   itemResult.Get("call_id").String(),
				Name: restoreCodexOpenAIToolName(rev, itemResult.Get("name").String()),
			})
		case "response.function_call_arguments.delta":
			toolCall := ensureLastCodexNonStreamToolCall(&toolCalls)
			toolCall.Arguments += event.Get("delta").String()
			toolCall.HasArgumentsDelta = true
		case "response.function_call_arguments.done":
			toolCall := ensureLastCodexNonStreamToolCall(&toolCalls)
			if !toolCall.HasArgumentsDelta {
				toolCall.Arguments = event.Get("arguments").String()
			}
		case "response.output_item.done":
			itemResult := event.Get("item")
			if itemResult.Get("type").String() != "function_call" {
				continue
			}
			toolCall := ensureLastCodexNonStreamToolCall(&toolCalls)
			if toolCall.ID == "" {
				toolCall.ID = itemResult.Get("call_id").String()
			}
			if toolCall.Name == "" {
				toolCall.Name = restoreCodexOpenAIToolName(rev, itemResult.Get("name").String())
			}
			if toolCall.Arguments == "" {
				toolCall.Arguments = itemResult.Get("arguments").String()
			}
		case "response.completed":
			responseResult = event.Get("response")
		}
	}

	if !responseResult.Exists() {
		return gjson.Result{}, gjson.Result{}, "", "", nil, false
	}

	contentText, reasoningText, completedToolCalls := extractCodexOpenAINonStreamOutput(originalRequestRawJSON, responseResult.Get("output"))
	if contentText == "" {
		contentText = contentBuilder.String()
	}
	if reasoningText == "" {
		reasoningText = reasoningBuilder.String()
	}
	if len(completedToolCalls) > 0 {
		toolCalls = completedToolCalls
	}

	return responseResult, createdResult, contentText, reasoningText, toolCalls, true
}

func extractCodexOpenAINonStreamOutput(originalRequestRawJSON []byte, outputResult gjson.Result) (string, string, []codexNonStreamToolCall) {
	if !outputResult.IsArray() {
		return "", "", nil
	}

	rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
	outputArray := outputResult.Array()
	var contentBuilder bytes.Buffer
	var reasoningBuilder bytes.Buffer
	var toolCalls []codexNonStreamToolCall

	for _, outputItem := range outputArray {
		switch outputItem.Get("type").String() {
		case "reasoning":
			if summaryResult := outputItem.Get("summary"); summaryResult.IsArray() {
				for _, summaryItem := range summaryResult.Array() {
					if summaryItem.Get("type").String() == "summary_text" {
						reasoningBuilder.WriteString(summaryItem.Get("text").String())
					}
				}
			}
		case "message":
			if contentResult := outputItem.Get("content"); contentResult.IsArray() {
				for _, contentItem := range contentResult.Array() {
					if contentItem.Get("type").String() == "output_text" {
						contentBuilder.WriteString(contentItem.Get("text").String())
					}
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, codexNonStreamToolCall{
				ID:        outputItem.Get("call_id").String(),
				Name:      restoreCodexOpenAIToolName(rev, outputItem.Get("name").String()),
				Arguments: outputItem.Get("arguments").String(),
			})
		}
	}

	return contentBuilder.String(), reasoningBuilder.String(), toolCalls
}

func ensureLastCodexNonStreamToolCall(toolCalls *[]codexNonStreamToolCall) *codexNonStreamToolCall {
	if len(*toolCalls) == 0 {
		*toolCalls = append(*toolCalls, codexNonStreamToolCall{})
	}
	return &(*toolCalls)[len(*toolCalls)-1]
}

func restoreCodexOpenAIToolName(rev map[string]string, name string) string {
	if orig, ok := rev[name]; ok {
		return orig
	}
	return name
}

// buildReverseMapFromOriginalOpenAI builds a map of shortened tool name -> original tool name
// from the original OpenAI-style request JSON using the same shortening logic.
func buildReverseMapFromOriginalOpenAI(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	rev := map[string]string{}
	if tools.IsArray() && len(tools.Array()) > 0 {
		var names []string
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			if t.Get("type").String() != "function" {
				continue
			}
			fn := t.Get("function")
			if !fn.Exists() {
				continue
			}
			if v := fn.Get("name"); v.Exists() {
				names = append(names, v.String())
			}
		}
		if len(names) > 0 {
			m := buildShortNameMap(names)
			for orig, short := range m {
				rev[short] = orig
			}
		}
	}
	return rev
}

func mimeTypeFromCodexOutputFormat(outputFormat string) string {
	if outputFormat == "" {
		return "image/png"
	}
	if strings.Contains(outputFormat, "/") {
		return outputFormat
	}
	switch strings.ToLower(outputFormat) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}
