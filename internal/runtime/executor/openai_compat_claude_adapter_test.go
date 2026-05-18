package executor

import (
	"context"
	"strings"
	"testing"

	responsefmt "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
)

type anthropicSSEEvent struct {
	name string
	data gjson.Result
}

func parseAnthropicSSEEvents(t *testing.T, payload []byte) []anthropicSSEEvent {
	t.Helper()

	var events []anthropicSSEEvent
	for _, block := range strings.Split(strings.TrimSpace(string(payload)), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		lines := strings.Split(block, "\n")
		var eventName string
		var dataLine string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}

		if eventName == "" || dataLine == "" {
			t.Fatalf("unexpected SSE block: %q", block)
		}
		if !gjson.Valid(dataLine) {
			t.Fatalf("invalid SSE data JSON: %q", dataLine)
		}
		events = append(events, anthropicSSEEvent{name: eventName, data: gjson.Parse(dataLine)})
	}

	return events
}

func findAnthropicMessageDeltaStopReason(t *testing.T, events []anthropicSSEEvent) string {
	t.Helper()
	for _, event := range events {
		if event.name == "message_delta" {
			return event.data.Get("delta.stop_reason").String()
		}
	}
	t.Fatalf("did not find message_delta in %v events", len(events))
	return ""
}

func TestAdaptOpenAIResponsesStreamChunkToClaude_MapsTerminalStopReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stopReason string
		want       string
	}{
		{name: "stop becomes end_turn", stopReason: "stop", want: "end_turn"},
		{name: "length becomes max_tokens", stopReason: "length", want: "max_tokens"},
		{name: "tool_calls becomes tool_use", stopReason: "tool_calls", want: "tool_use"},
		{name: "function_call becomes tool_use", stopReason: "function_call", want: "tool_use"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			request := []byte(`{"model":"gpt-5.4"}`)
			var param any

			_ = adaptOpenAIResponsesStreamChunkToClaude(ctx, "gpt-5.4", request, request, []byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`), &param)
			chunks := adaptOpenAIResponsesStreamChunkToClaude(ctx, "gpt-5.4", request, request, []byte(`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.4","stop_reason":"`+tt.stopReason+`","usage":{"input_tokens":2,"output_tokens":3}}}`), &param)

			if len(chunks) == 0 {
				t.Fatal("expected translated chunks")
			}

			var got string
			for _, chunk := range chunks {
				events := parseAnthropicSSEEvents(t, chunk)
				for _, event := range events {
					if event.name == "message_delta" {
						got = event.data.Get("delta.stop_reason").String()
					}
				}
			}

			if got != tt.want {
				t.Fatalf("stop_reason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAdaptOpenAIChatFallbackToClaude_PreservesMaxTokensStopReason(t *testing.T) {
	ctx := context.Background()
	request := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)

	var responsesParam any
	var claudeParam any
	var translatedEvents []anthropicSSEEvent

	feed := func(chunk []byte) {
		lifted := responsefmt.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "gpt-5.4", request, request, chunk, &responsesParam)
		for _, liftedChunk := range lifted {
			adapted := adaptOpenAIResponsesStreamChunkToClaude(ctx, "gpt-5.4", request, request, liftedChunk, &claudeParam)
			for _, adaptedChunk := range adapted {
				translatedEvents = append(translatedEvents, parseAnthropicSSEEvents(t, adaptedChunk)...)
			}
		}
	}

	feed([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`))
	feed([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1773896263,"model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`))
	feed([]byte(`data: [DONE]`))

	if got := findAnthropicMessageDeltaStopReason(t, translatedEvents); got != "max_tokens" {
		t.Fatalf("message_delta stop_reason = %q, want %q", got, "max_tokens")
	}
}

func TestAdaptOpenAIResponsesNonStreamToClaude_WrapsResponsesObject(t *testing.T) {
	ctx := context.Background()
	request := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	raw := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}`)

	out := adaptOpenAIResponsesNonStreamToClaude(ctx, "gpt-5.4", request, request, raw, nil)
	if got := gjson.GetBytes(out, "type").String(); got != "message" {
		t.Fatalf("type = %q, want %q; body=%s", got, "message", string(out))
	}
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "hello" {
		t.Fatalf("content.0.text = %q, want %q; body=%s", got, "hello", string(out))
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want %q; body=%s", got, "end_turn", string(out))
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 5 {
		t.Fatalf("usage.input_tokens = %d, want 5; body=%s", got, string(out))
	}
}
