package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAIWithTools_PreservesMultiToolTranscript(t *testing.T) {
	inputJSON := `{
		"model":"gpt-5.4",
		"stream":true,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Inspect the repository."}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_read","name":"read_file","input":{"path":"README.md"}},
				{"type":"tool_use","id":"toolu_glob","name":"glob","input":{"pattern":"*.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_read","content":[{"type":"text","text":"README contents"}]},
				{"type":"tool_result","tool_use_id":"toolu_glob","content":[{"type":"text","text":"main.go"}]}
			]}
		],
		"tools":[
			{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"name":"glob","description":"Find files","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}}
		],
		"tool_choice":{"type":"auto"}
	}`

	result := ConvertClaudeRequestToOpenAIWithTools("gpt-5.4", []byte(inputJSON), true)
	root := gjson.ParseBytes(result)

	if got := root.Get("parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls = %v, want true", got)
	}
	if got := root.Get("tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want %q", got, "auto")
	}
	if got := root.Get("tools.#").Int(); got != 2 {
		t.Fatalf("tools length = %d, want %d", got, 2)
	}
	if got := root.Get("messages.#").Int(); got != 4 {
		t.Fatalf("messages length = %d, want %d", got, 4)
	}
	if got := root.Get("messages.1.tool_calls.#").Int(); got != 2 {
		t.Fatalf("assistant tool_calls length = %d, want %d", got, 2)
	}
	if got := root.Get("messages.1.tool_calls.0.id").String(); got != "toolu_read" {
		t.Fatalf("tool_calls[0].id = %q, want %q", got, "toolu_read")
	}
	if got := root.Get("messages.1.tool_calls.1.function.name").String(); got != "glob" {
		t.Fatalf("tool_calls[1].function.name = %q, want %q", got, "glob")
	}
	if got := root.Get("messages.2.role").String(); got != "tool" {
		t.Fatalf("messages[2].role = %q, want %q", got, "tool")
	}
	if got := root.Get("messages.2.tool_call_id").String(); got != "toolu_read" {
		t.Fatalf("messages[2].tool_call_id = %q, want %q", got, "toolu_read")
	}
	if got := root.Get("messages.2.content").String(); got != "README contents" {
		t.Fatalf("messages[2].content = %q, want %q", got, "README contents")
	}
	if got := root.Get("messages.3.content").String(); got != "main.go" {
		t.Fatalf("messages[3].content = %q, want %q", got, "main.go")
	}
}
