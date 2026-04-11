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

func TestConvertCodexResponseToOpenAIResponsesNonStream_UnwrapsBufferedSSETranscript(t *testing.T) {
	request := []byte(`{"model":"gpt-5.4","previous_response_id":"resp-prev","prompt_cache_key":"session-cache"}`)
	raw := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello from cheapRouter.\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[]}}\n\n")

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.4", request, request, raw, nil)
	payload := gjson.ParseBytes(out)
	if got := payload.Get("id").String(); got != "resp-1" {
		t.Fatalf("id = %q, want %q", got, "resp-1")
	}
	if got := payload.Get("output.0.content.0.text").String(); got != "Hello from cheapRouter." {
		t.Fatalf("output text = %q, want %q", got, "Hello from cheapRouter.")
	}
	if got := payload.Get("previous_response_id").String(); got != "resp-prev" {
		t.Fatalf("previous_response_id = %q, want %q", got, "resp-prev")
	}
	if got := payload.Get("prompt_cache_key").String(); got != "session-cache" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "session-cache")
	}
}

func TestConvertCodexResponseToOpenAIResponsesNonStream_ReconstructsToolCallsFromBufferedSSETranscript(t *testing.T) {
	request := []byte(`{"model":"gpt-5.4"}`)
	raw := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-2\",\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_123\",\"name\":\"lookup\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"topic\\\":\\\"cheaprouter\\\"}\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[]}}\n\n")

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.4", request, request, raw, nil)
	payload := gjson.ParseBytes(out)
	if got := payload.Get("output.0.type").String(); got != "function_call" {
		t.Fatalf("output.0.type = %q, want function_call", got)
	}
	if got := payload.Get("output.0.arguments").String(); got != "{\"topic\":\"cheaprouter\"}" {
		t.Fatalf("output.0.arguments = %q, want %q", got, "{\"topic\":\"cheaprouter\"}")
	}
}
