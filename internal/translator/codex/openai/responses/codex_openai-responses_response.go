package responses

import (
	"bytes"
	"context"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).
func ConvertCodexResponseToOpenAIResponses(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) [][]byte {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
		rawJSON = injectCodexResponseRequestEcho(rawJSON, originalRequestRawJSON, requestRawJSON)
		out := make([]byte, 0, len(rawJSON)+len("data: "))
		out = append(out, []byte("data: ")...)
		out = append(out, rawJSON...)
		return [][]byte{out}
	}
	return [][]byte{injectCodexResponseRequestEcho(rawJSON, originalRequestRawJSON, requestRawJSON)}
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	rootResult := gjson.ParseBytes(rawJSON)
	if rootResult.Get("type").String() != "response.completed" {
		return []byte{}
	}
	responseResult := rootResult.Get("response")
	resp := []byte(responseResult.Raw)
	return injectCodexResponseTopLevelEcho(resp, originalRequestRawJSON, requestRawJSON)
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

type codexRequestEchoField struct {
	requestPath string
	targetPath  string
}

func injectCodexResponseRequestEcho(rawJSON, originalRequestRawJSON, requestRawJSON []byte) []byte {
	root := gjson.ParseBytes(rawJSON)
	if root.Get("type").String() != "response.completed" {
		return rawJSON
	}
	request := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	if len(request) == 0 {
		return rawJSON
	}
	return injectCodexRequestEchoFields(rawJSON, gjson.ParseBytes(request), []codexRequestEchoField{
		{requestPath: "previous_response_id", targetPath: "response.previous_response_id"},
		{requestPath: "prompt_cache_key", targetPath: "response.prompt_cache_key"},
	})
}

func injectCodexResponseTopLevelEcho(rawJSON, originalRequestRawJSON, requestRawJSON []byte) []byte {
	request := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	if len(request) == 0 {
		return rawJSON
	}
	return injectCodexRequestEchoFields(rawJSON, gjson.ParseBytes(request), []codexRequestEchoField{
		{requestPath: "previous_response_id", targetPath: "previous_response_id"},
		{requestPath: "prompt_cache_key", targetPath: "prompt_cache_key"},
	})
}

func injectCodexRequestEchoFields(rawJSON []byte, request gjson.Result, fields []codexRequestEchoField) []byte {
	updated := append([]byte(nil), rawJSON...)
	for _, field := range fields {
		if gjson.GetBytes(updated, field.targetPath).Exists() {
			continue
		}
		if value := request.Get(field.requestPath); value.Exists() {
			updated, _ = sjson.SetBytes(updated, field.targetPath, value.String())
		}
	}
	return updated
}
