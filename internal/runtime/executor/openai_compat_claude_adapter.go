package executor

import (
	"bytes"
	"context"

	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	responsefmt "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// adaptOpenAIResponsesStreamChunkToClaude reuses the Codex Responses-to-Claude translator
// because OpenAI Responses streaming chunks and Codex response.* events share the same shape
// needed by the Claude-compatible adapter.
func adaptOpenAIResponsesStreamChunkToClaude(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	rawJSON = normalizeOpenAIResponsesStreamChunk(rawJSON)
	return codexclaude.ConvertCodexResponseToClaude(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

func adaptOpenAIResponsesNonStreamToClaude(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	rawJSON = normalizeOpenAIResponsesNonStreamForClaude(rawJSON)
	return codexclaude.ConvertCodexResponseToClaudeNonStream(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

type openAICompatChatFallbackParams struct {
	responsesParam any
	claudeParam    any
}

func adaptOpenAIChatCompletionsStreamChunkToClaude(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	state := ensureOpenAICompatChatFallbackParams(param)
	lifted := responsefmt.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, &state.responsesParam)
	out := make([][]byte, 0, len(lifted))
	for _, chunk := range lifted {
		out = append(out, adaptOpenAIResponsesStreamChunkToClaude(ctx, model, originalRequestRawJSON, requestRawJSON, chunk, &state.claudeParam)...)
	}
	return out
}

func adaptOpenAIChatCompletionsNonStreamToClaude(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	lifted := responsefmt.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, nil)
	return adaptOpenAIResponsesNonStreamToClaude(ctx, model, originalRequestRawJSON, requestRawJSON, lifted, nil)
}

func ensureOpenAICompatChatFallbackParams(param *any) *openAICompatChatFallbackParams {
	if param == nil {
		return &openAICompatChatFallbackParams{}
	}
	if existing, ok := (*param).(*openAICompatChatFallbackParams); ok && existing != nil {
		return existing
	}
	state := &openAICompatChatFallbackParams{}
	*param = state
	return state
}

func normalizeOpenAIResponsesStreamChunk(rawJSON []byte) []byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return trimmed
	}

	for _, line := range bytes.Split(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			return line
		}
	}

	return trimmed
}

func normalizeOpenAIResponsesNonStreamForClaude(rawJSON []byte) []byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if !gjson.ValidBytes(trimmed) {
		return trimmed
	}

	root := gjson.ParseBytes(trimmed)
	if root.Get("type").String() == "response.completed" {
		return trimmed
	}
	if root.Get("object").String() != "response" && !root.Get("output").Exists() {
		return trimmed
	}

	wrapped := []byte(`{"type":"response.completed","response":{}}`)
	wrapped, _ = sjson.SetRawBytes(wrapped, "response", trimmed)
	return wrapped
}
