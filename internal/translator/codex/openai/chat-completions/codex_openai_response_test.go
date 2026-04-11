package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAI_StreamSetsModelFromResponseCreated(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.created","response":{"id":"resp_123","created_at":1700000000,"model":"gpt-5.3-codex"}}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected no output for response.created, got %d chunks", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.GetBytes(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_FirstChunkUsesRequestModelName(t *testing.T) {
	ctx := context.Background()
	var param any

	modelName := "gpt-5.3-codex"

	out := ConvertCodexResponseToOpenAI(ctx, modelName, nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	gotModel := gjson.GetBytes(out[0], "model").String()
	if gotModel != modelName {
		t.Fatalf("expected model %q, got %q", modelName, gotModel)
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallChunkOmitsNullContentFields(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"websearch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gjson.GetBytes(out[0], "choices.0.delta.content").Exists() {
		t.Fatalf("expected content to be omitted, got %s", string(out[0]))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content to be omitted, got %s", string(out[0]))
	}
	if !gjson.GetBytes(out[0], "choices.0.delta.tool_calls").Exists() {
		t.Fatalf("expected tool_calls to exist, got %s", string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_ToolCallArgumentsDeltaOmitsNullContentFields(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"websearch"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected tool call announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.4", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"query\":\"OpenAI\"}"}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gjson.GetBytes(out[0], "choices.0.delta.content").Exists() {
		t.Fatalf("expected content to be omitted, got %s", string(out[0]))
	}
	if gjson.GetBytes(out[0], "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("expected reasoning_content to be omitted, got %s", string(out[0]))
	}
	if !gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").Exists() {
		t.Fatalf("expected tool call arguments delta to exist, got %s", string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAINonStream_ReconstructsTextFromSSETranscript(t *testing.T) {
	raw := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1700000000,\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello \"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"from cheapRouter.\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1700000000,\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":4,\"total_tokens\":9}}}\n\n")

	out := ConvertCodexResponseToOpenAINonStream(context.Background(), "", nil, nil, raw, nil)
	if len(out) == 0 {
		t.Fatal("expected non-empty response")
	}

	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "Hello from cheapRouter." {
		t.Fatalf("expected reconstructed content, got %q", got)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("expected finish_reason stop, got %q", got)
	}
}

func TestConvertCodexResponseToOpenAINonStream_ReconstructsReasoningFromSSETranscript(t *testing.T) {
	raw := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\",\"created_at\":1700000001,\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"First \"}\n\n" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"second\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"created_at\":1700000001,\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")

	out := ConvertCodexResponseToOpenAINonStream(context.Background(), "", nil, nil, raw, nil)
	if len(out) == 0 {
		t.Fatal("expected non-empty response")
	}

	if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "First second" {
		t.Fatalf("expected reconstructed reasoning, got %q", got)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content"); got.Exists() && got.Type != gjson.Null {
		t.Fatalf("expected content to stay null, got %s", got.Raw)
	}
}

func TestConvertCodexResponseToOpenAINonStream_ReconstructsToolCallsFromSSETranscript(t *testing.T) {
	raw := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_3\",\"created_at\":1700000002,\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_123\",\"name\":\"websearch\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"query\\\":\\\"OpenAI\\\"}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_123\",\"name\":\"websearch\",\"arguments\":\"{\\\"query\\\":\\\"OpenAI\\\"}\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_3\",\"created_at\":1700000002,\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":6,\"total_tokens\":14}}}\n\n")

	out := ConvertCodexResponseToOpenAINonStream(context.Background(), "", nil, nil, raw, nil)
	if len(out) == 0 {
		t.Fatal("expected non-empty response")
	}

	if got := gjson.GetBytes(out, "choices.0.message.content"); got.Exists() && got.Type != gjson.Null {
		t.Fatalf("expected content to stay null, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q", got)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String(); got != "{\"query\":\"OpenAI\"}" {
		t.Fatalf("expected reconstructed tool call arguments, got %q", got)
	}
}
