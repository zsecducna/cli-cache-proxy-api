package responses

import (
	"testing"

	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	"github.com/tidwall/gjson"
)

func TestLiftOpenAIChatCompletionsRequestToOpenAIResponses_TextOnly(t *testing.T) {
	claudeRequest := []byte(`{
		"model":"gpt-5.4",
		"system":"Be terse.",
		"max_tokens":256,
		"temperature":0.2,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Explain this change."}]}
		]
	}`)

	chatRequest := openaiclaude.ConvertClaudeRequestToOpenAIWithTools("gpt-5.4", claudeRequest, true)
	lifted := LiftOpenAIChatCompletionsRequestToOpenAIResponses("gpt-5.4", chatRequest)
	root := gjson.ParseBytes(lifted)

	if got := root.Get("model").String(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4")
	}
	if got := root.Get("instructions").String(); got != "Be terse." {
		t.Fatalf("instructions = %q, want %q", got, "Be terse.")
	}
	if got := root.Get("max_output_tokens").Int(); got != 256 {
		t.Fatalf("max_output_tokens = %d, want %d", got, 256)
	}
	if got := root.Get("temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want %v", got, 0.2)
	}
	if got := root.Get("input.#").Int(); got != 1 {
		t.Fatalf("input length = %d, want %d", got, 1)
	}
	if got := root.Get("input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q", got, "message")
	}
	if got := root.Get("input.0.role").String(); got != "user" {
		t.Fatalf("input[0].role = %q, want %q", got, "user")
	}
	if got := root.Get("input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("input[0].content[0].type = %q, want %q", got, "input_text")
	}
	if got := root.Get("input.0.content.0.text").String(); got != "Explain this change." {
		t.Fatalf("input[0].content[0].text = %q, want %q", got, "Explain this change.")
	}
}

func TestLiftOpenAIChatCompletionsRequestToOpenAIResponses_MultiTool(t *testing.T) {
	claudeRequest := []byte(`{
		"model":"gpt-5.4",
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
	}`)

	chatRequest := openaiclaude.ConvertClaudeRequestToOpenAIWithTools("gpt-5.4", claudeRequest, true)
	lifted := LiftOpenAIChatCompletionsRequestToOpenAIResponses("gpt-5.4", chatRequest)
	root := gjson.ParseBytes(lifted)

	if got := root.Get("input.#").Int(); got != 5 {
		t.Fatalf("input length = %d, want %d", got, 5)
	}

	if got := root.Get("input.0.type").String(); got != "message" {
		t.Fatalf("input[0].type = %q, want %q", got, "message")
	}
	if got := root.Get("input.0.content.0.text").String(); got != "Inspect the repository." {
		t.Fatalf("input[0].content[0].text = %q, want %q", got, "Inspect the repository.")
	}

	if got := root.Get("input.1.type").String(); got != "function_call" {
		t.Fatalf("input[1].type = %q, want %q", got, "function_call")
	}
	if got := root.Get("input.1.call_id").String(); got != "toolu_read" {
		t.Fatalf("input[1].call_id = %q, want %q", got, "toolu_read")
	}
	if got := root.Get("input.1.name").String(); got != "read_file" {
		t.Fatalf("input[1].name = %q, want %q", got, "read_file")
	}
	if got := gjson.Get(root.Get("input.1.arguments").String(), "path").String(); got != "README.md" {
		t.Fatalf("input[1].arguments.path = %q, want %q", got, "README.md")
	}

	if got := root.Get("input.2.type").String(); got != "function_call" {
		t.Fatalf("input[2].type = %q, want %q", got, "function_call")
	}
	if got := root.Get("input.2.call_id").String(); got != "toolu_glob" {
		t.Fatalf("input[2].call_id = %q, want %q", got, "toolu_glob")
	}
	if got := root.Get("input.2.name").String(); got != "glob" {
		t.Fatalf("input[2].name = %q, want %q", got, "glob")
	}
	if got := gjson.Get(root.Get("input.2.arguments").String(), "pattern").String(); got != "*.go" {
		t.Fatalf("input[2].arguments.pattern = %q, want %q", got, "*.go")
	}

	if got := root.Get("input.3.type").String(); got != "function_call_output" {
		t.Fatalf("input[3].type = %q, want %q", got, "function_call_output")
	}
	if got := root.Get("input.3.call_id").String(); got != "toolu_read" {
		t.Fatalf("input[3].call_id = %q, want %q", got, "toolu_read")
	}
	if got := root.Get("input.3.output").String(); got != "README contents" {
		t.Fatalf("input[3].output = %q, want %q", got, "README contents")
	}

	if got := root.Get("input.4.type").String(); got != "function_call_output" {
		t.Fatalf("input[4].type = %q, want %q", got, "function_call_output")
	}
	if got := root.Get("input.4.call_id").String(); got != "toolu_glob" {
		t.Fatalf("input[4].call_id = %q, want %q", got, "toolu_glob")
	}
	if got := root.Get("input.4.output").String(); got != "main.go" {
		t.Fatalf("input[4].output = %q, want %q", got, "main.go")
	}

	if got := root.Get("tools.#").Int(); got != 2 {
		t.Fatalf("tools length = %d, want %d", got, 2)
	}
	if got := root.Get("tools.0.name").String(); got != "read_file" {
		t.Fatalf("tools[0].name = %q, want %q", got, "read_file")
	}
	if got := root.Get("tools.1.name").String(); got != "glob" {
		t.Fatalf("tools[1].name = %q, want %q", got, "glob")
	}
	if got := root.Get("tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want %q", got, "auto")
	}
}
