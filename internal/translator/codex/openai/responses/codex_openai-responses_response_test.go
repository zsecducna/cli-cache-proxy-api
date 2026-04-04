package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponsesEchoesCacheMetadata(t *testing.T) {
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp-prev","prompt_cache_key":"session-cache"}`)
	raw := []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)

	out := ConvertCodexResponseToOpenAIResponses(context.Background(), "gpt-5.4", request, request, raw, nil)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	payload := gjson.ParseBytes(out[0][6:])
	if got := payload.Get("response.previous_response_id").String(); got != "resp-prev" {
		t.Fatalf("response.previous_response_id = %q, want %q", got, "resp-prev")
	}
	if got := payload.Get("response.prompt_cache_key").String(); got != "session-cache" {
		t.Fatalf("response.prompt_cache_key = %q, want %q", got, "session-cache")
	}
}

func TestConvertCodexResponseToOpenAIResponsesNonStreamEchoesCacheMetadata(t *testing.T) {
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp-prev","prompt_cache_key":"session-cache"}`)
	raw := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.4", request, request, raw, nil)
	payload := gjson.ParseBytes(out)
	if got := payload.Get("previous_response_id").String(); got != "resp-prev" {
		t.Fatalf("previous_response_id = %q, want %q", got, "resp-prev")
	}
	if got := payload.Get("prompt_cache_key").String(); got != "session-cache" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "session-cache")
	}
}
