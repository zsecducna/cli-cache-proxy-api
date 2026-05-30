package helps

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// eventName extracts the SSE "event:" name from a framed Anthropic event line.
func eventName(line []byte) string {
	s := string(line)
	if !strings.HasPrefix(s, "event: ") {
		return ""
	}
	return strings.SplitN(strings.TrimPrefix(s, "event: "), "\n", 2)[0]
}

// TestAssembleKiroClaudeResponse_DirectToolUse verifies the non-stream Kiro->Claude
// assembler emits thinking+text+tool_use blocks, restores shortened tool names, preserves
// tool input, and sets stop_reason/usage.
func TestAssembleKiroClaudeResponse_DirectToolUse(t *testing.T) {
	restore := map[string]string{"short_tool": "really_long_original_tool_name"}
	var full []byte
	full = append(full, buildKiroFrame("reasoningContentEvent", []byte(`{"text":"reasoning"}`))...)
	full = append(full, buildKiroFrame("assistantResponseEvent", []byte(`{"content":"Let me check."}`))...)
	full = append(full, buildKiroFrame("toolUseEvent", []byte(`{"toolUseId":"t1","name":"short_tool","input":{"city":"Paris"}}`))...)

	root := gjson.ParseBytes(AssembleKiroClaudeResponse("claude-opus-4.8", full, 7, restore))

	if root.Get("type").String() != "message" || root.Get("role").String() != "assistant" {
		t.Fatalf("bad envelope: %s", root.Raw)
	}
	if root.Get("stop_reason").String() != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", root.Get("stop_reason").String())
	}
	var types []string
	root.Get("content").ForEach(func(_, c gjson.Result) bool {
		types = append(types, c.Get("type").String())
		return true
	})
	if strings.Join(types, ",") != "thinking,text,tool_use" {
		t.Fatalf("content block types = %v, want [thinking text tool_use]", types)
	}
	if name := root.Get(`content.#(type=="tool_use").name`).String(); name != "really_long_original_tool_name" {
		t.Fatalf("tool name = %q, want restored original", name)
	}
	if city := root.Get(`content.#(type=="tool_use").input.city`).String(); city != "Paris" {
		t.Fatalf("tool input.city = %q, want Paris", city)
	}
	if root.Get("usage.input_tokens").Int() != 7 {
		t.Fatalf("usage.input_tokens = %d, want 7", root.Get("usage.input_tokens").Int())
	}
}

// TestKiroClaudeStreamEncoder_Sequence verifies the streaming encoder emits a valid
// Anthropic event order and restores shortened tool names on content_block_start.
func TestKiroClaudeStreamEncoder_Sequence(t *testing.T) {
	enc := NewKiroClaudeStreamEncoder("claude-opus-4.8", 3, map[string]string{"short_tool": "orig_name"})
	var events []string
	var toolName string
	collect := func(lines [][]byte) {
		for _, l := range lines {
			name := eventName(l)
			events = append(events, name)
			if name == "content_block_start" {
				if cb := dataJSON(t, sseData(l)).Get("content_block"); cb.Get("type").String() == "tool_use" {
					toolName = cb.Get("name").String()
				}
			}
		}
	}
	collect(enc.Encode(buildKiroFrame("assistantResponseEvent", []byte(`{"content":"Hi"}`))))
	collect(enc.Encode(buildKiroFrame("toolUseEvent", []byte(`{"toolUseId":"t1","name":"short_tool","input":{"a":1}}`))))
	collect(enc.Finish())

	if len(events) == 0 || events[0] != "message_start" {
		t.Fatalf("first event = %q, want message_start (seq=%v)", firstOr(events), events)
	}
	if events[len(events)-1] != "message_stop" {
		t.Fatalf("last event = %q, want message_stop (seq=%v)", events[len(events)-1], events)
	}
	if !contains(events, "message_delta") || !contains(events, "content_block_stop") {
		t.Fatalf("missing terminal/block events: %v", events)
	}
	if toolName != "orig_name" {
		t.Fatalf("streamed tool name = %q, want restored orig_name", toolName)
	}
}

// sseData returns the line with everything before the "data: " marker removed, so the
// shared dataJSON helper can parse it.
func sseData(line []byte) []byte {
	if i := bytes.Index(line, []byte("data: ")); i >= 0 {
		return line[i:]
	}
	return line
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func firstOr(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}
