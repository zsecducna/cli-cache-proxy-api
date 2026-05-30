package helps

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/tidwall/gjson"
)

// buildKiroFrame constructs a valid AWS EventStream frame with a single ":event-type"
// header (string type) and the given JSON payload. CRC fields are left zero since the
// decoder does not validate them.
func buildKiroFrame(eventType string, payload []byte) []byte {
	name := ":event-type"
	var hdr bytes.Buffer
	hdr.WriteByte(byte(len(name)))
	hdr.WriteString(name)
	hdr.WriteByte(7) // 7 = string header type
	var valLen [2]byte
	binary.BigEndian.PutUint16(valLen[:], uint16(len(eventType)))
	hdr.Write(valLen[:])
	hdr.WriteString(eventType)
	headers := hdr.Bytes()

	total := 12 + len(headers) + len(payload) + 4
	frame := make([]byte, total)
	binary.BigEndian.PutUint32(frame[0:4], uint32(total))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	copy(frame[12:12+len(headers)], headers)
	copy(frame[12+len(headers):total-4], payload)
	return frame
}

// dataJSON strips the leading "data: " SSE marker and parses the remaining JSON.
func dataJSON(t *testing.T, line []byte) gjson.Result {
	t.Helper()
	if !bytes.HasPrefix(line, []byte("data: ")) {
		t.Fatalf("line missing 'data: ' prefix: %q", string(line))
	}
	return gjson.ParseBytes(bytes.TrimPrefix(line, []byte("data: ")))
}

// TestDecode_AssistantText decodes a single assistant text frame into a content delta.
func TestDecode_AssistantText(t *testing.T) {
	dec := NewKiroEventStreamDecoder("claude-sonnet-4.5", 0, nil)
	lines := dec.Decode(buildKiroFrame("assistantResponseEvent", []byte(`{"content":"Hello"}`)))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	chunk := dataJSON(t, lines[0])
	if got := chunk.Get("choices.0.delta.content").String(); got != "Hello" {
		t.Fatalf("delta.content = %q, want Hello", got)
	}
	if chunk.Get("object").String() != "chat.completion.chunk" {
		t.Fatalf("object = %q, want chat.completion.chunk", chunk.Get("object").String())
	}
}

// TestDecode_ReasoningEvent maps reasoning content to delta.reasoning_content.
func TestDecode_ReasoningEvent(t *testing.T) {
	dec := NewKiroEventStreamDecoder("glm-5", 0, nil)
	lines := dec.Decode(buildKiroFrame("reasoningContentEvent", []byte(`{"text":"thinking..."}`)))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if got := dataJSON(t, lines[0]).Get("choices.0.delta.reasoning_content").String(); got != "thinking..." {
		t.Fatalf("delta.reasoning_content = %q, want thinking...", got)
	}
}

// TestDecode_ToolUseAndFinish covers streamed tool calls (id+name first, then argument
// fragments) and the tool_calls finish reason on messageStop.
func TestDecode_ToolUseAndFinish(t *testing.T) {
	dec := NewKiroEventStreamDecoder("claude-sonnet-4.5", 0, nil)

	var lines [][]byte
	lines = append(lines, dec.Decode(buildKiroFrame("toolUseEvent", []byte(`{"toolUseId":"t1","name":"foo","input":"{\"a\":"}`)))...)
	lines = append(lines, dec.Decode(buildKiroFrame("toolUseEvent", []byte(`{"toolUseId":"t1","input":"1}"}`)))...)
	lines = append(lines, dec.Decode(buildKiroFrame("messageStopEvent", []byte(`{}`)))...)

	// First line: tool_calls start with id + name.
	start := dataJSON(t, lines[0]).Get("choices.0.delta.tool_calls.0")
	if start.Get("id").String() != "t1" || start.Get("function.name").String() != "foo" {
		t.Fatalf("tool start chunk wrong: %s", start.Raw)
	}
	// Argument fragments accumulate across chunks.
	var args string
	for _, line := range lines {
		args += dataJSON(t, line).Get("choices.0.delta.tool_calls.0.function.arguments").String()
	}
	if args != `{"a":1}` {
		t.Fatalf("accumulated arguments = %q, want %q", args, `{"a":1}`)
	}
	// Last line: finish_reason tool_calls.
	last := dataJSON(t, lines[len(lines)-1])
	if got := last.Get("choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", got)
	}
}

// TestDecode_MultiFrameSplit verifies frames split across reads buffer correctly.
func TestDecode_MultiFrameSplit(t *testing.T) {
	frame1 := buildKiroFrame("assistantResponseEvent", []byte(`{"content":"AB"}`))
	frame2 := buildKiroFrame("assistantResponseEvent", []byte(`{"content":"CD"}`))
	combined := append(append([]byte{}, frame1...), frame2...)
	split := len(frame1) + 3 // partway into frame2

	dec := NewKiroEventStreamDecoder("auto", 0, nil)
	first := dec.Decode(combined[:split])
	if len(first) != 1 || dataJSON(t, first[0]).Get("choices.0.delta.content").String() != "AB" {
		t.Fatalf("first decode should yield only AB, got %d lines", len(first))
	}
	second := dec.Decode(combined[split:])
	if len(second) != 1 || dataJSON(t, second[0]).Get("choices.0.delta.content").String() != "CD" {
		t.Fatalf("second decode should yield CD, got %d lines", len(second))
	}
}

// TestDecode_FinishThenUsage verifies the terminal usage chunk is emitted after finish,
// with estimated tokens (CodeWhisperer returns no token counts).
func TestDecode_FinishThenUsage(t *testing.T) {
	dec := NewKiroEventStreamDecoder("claude-sonnet-4.5", 5, nil)
	dec.Decode(buildKiroFrame("assistantResponseEvent", []byte(`{"content":"hello world"}`)))
	dec.Decode(buildKiroFrame("messageStopEvent", []byte(`{}`)))

	finish := dec.Finish()
	if len(finish) == 0 {
		t.Fatal("Finish() returned no lines")
	}
	usage := dataJSON(t, finish[len(finish)-1])
	if usage.Get("usage.prompt_tokens").Int() != 5 {
		t.Fatalf("prompt_tokens = %d, want 5 (estimate passed in)", usage.Get("usage.prompt_tokens").Int())
	}
	if usage.Get("usage.completion_tokens").Int() <= 0 {
		t.Fatalf("completion_tokens should be > 0 (estimated from output), got %d", usage.Get("usage.completion_tokens").Int())
	}
	if usage.Get("usage.total_tokens").Int() != usage.Get("usage.prompt_tokens").Int()+usage.Get("usage.completion_tokens").Int() {
		t.Fatalf("total_tokens mismatch: %s", usage.Raw)
	}
}

// TestAssembleKiroOpenAIResponse builds a single non-streaming response from frames.
func TestAssembleKiroOpenAIResponse(t *testing.T) {
	var full []byte
	full = append(full, buildKiroFrame("assistantResponseEvent", []byte(`{"content":"Hello "}`))...)
	full = append(full, buildKiroFrame("assistantResponseEvent", []byte(`{"content":"world"}`))...)

	out := AssembleKiroOpenAIResponse("claude-sonnet-4.5", full, 9, nil)
	root := gjson.ParseBytes(out)
	if got := root.Get("choices.0.message.content").String(); got != "Hello world" {
		t.Fatalf("assembled content = %q, want 'Hello world'", got)
	}
	if root.Get("object").String() != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", root.Get("object").String())
	}
	if root.Get("usage.prompt_tokens").Int() != 9 {
		t.Fatalf("prompt_tokens = %d, want 9 (estimate passed in)", root.Get("usage.prompt_tokens").Int())
	}
	if root.Get("usage.completion_tokens").Int() <= 0 {
		t.Fatalf("completion_tokens should be > 0 (estimated), got %d", root.Get("usage.completion_tokens").Int())
	}
}

// TestAssembleKiroOpenAIResponse_ToolCalls assembles tool calls into the message.
func TestAssembleKiroOpenAIResponse_ToolCalls(t *testing.T) {
	var full []byte
	full = append(full, buildKiroFrame("toolUseEvent", []byte(`{"toolUseId":"t1","name":"foo","input":"{\"a\":1}"}`))...)
	full = append(full, buildKiroFrame("messageStopEvent", []byte(`{}`))...)

	out := AssembleKiroOpenAIResponse("claude-sonnet-4.5", full, 0, nil)
	root := gjson.ParseBytes(out)
	tc := root.Get("choices.0.message.tool_calls.0")
	if tc.Get("function.name").String() != "foo" || tc.Get("function.arguments").String() != `{"a":1}` {
		t.Fatalf("assembled tool call wrong: %s", tc.Raw)
	}
	if root.Get("choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", root.Get("choices.0.finish_reason").String())
	}
}
