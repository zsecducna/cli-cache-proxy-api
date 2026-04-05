package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAI_ChatSafeTextOnly(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"stream": true,
		"max_tokens": 256,
		"system": [{"type":"text","text":"You are helpful."}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Hello"}]},
			{"role":"assistant","content":[{"type":"text","text":"Hi there"}]}
		],
		"stop_sequences": ["DONE"]
	}`

	result := ConvertClaudeRequestToOpenAI("gpt-5.4", []byte(inputJSON), true)
	parsed := gjson.ParseBytes(result)

	if got := parsed.Get("model").String(); got != "gpt-5.4" {
		t.Fatalf("expected model %q, got %q", "gpt-5.4", got)
	}
	if got := parsed.Get("stream").Bool(); !got {
		t.Fatalf("expected stream=true")
	}
	if got := parsed.Get("max_tokens").Int(); got != 256 {
		t.Fatalf("expected max_tokens 256, got %d", got)
	}
	if got := parsed.Get("stop").String(); got != "DONE" {
		t.Fatalf("expected stop %q, got %q", "DONE", got)
	}

	messages := parsed.Get("messages").Array()
	if len(messages) != 3 {
		t.Fatalf("expected 3 chat-safe messages, got %d: %s", len(messages), parsed.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "system" {
		t.Fatalf("expected first role %q, got %q", "system", got)
	}
	if got := messages[1].Get("role").String(); got != "user" {
		t.Fatalf("expected second role %q, got %q", "user", got)
	}
	if got := messages[2].Get("role").String(); got != "assistant" {
		t.Fatalf("expected third role %q, got %q", "assistant", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("expected user text %q, got %q", "Hello", got)
	}
	if got := messages[2].Get("content.0.text").String(); got != "Hi there" {
		t.Fatalf("expected assistant text %q, got %q", "Hi there", got)
	}
}

func TestConvertClaudeRequestToOpenAI_ChatSafeOmitsToolingAndReasoningFields(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"stream": true,
		"thinking": {"type":"enabled","budget_tokens":1024},
		"tools": [{"name":"lookup","input_schema":{"type":"object"}}],
		"tool_choice": {"type":"tool","name":"lookup"},
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"internal"},
				{"type":"text","text":"Visible"},
				{"type":"tool_use","id":"call_1","name":"lookup","input":{"city":"Hanoi"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("gpt-5.4", []byte(inputJSON), true)
	parsed := gjson.ParseBytes(result)

	if parsed.Get("reasoning_effort").Exists() {
		t.Fatalf("did not expect reasoning_effort in chat-safe translator output: %s", result)
	}
	if parsed.Get("tools").Exists() {
		t.Fatalf("did not expect tools in chat-safe translator output: %s", result)
	}
	if parsed.Get("tool_choice").Exists() {
		t.Fatalf("did not expect tool_choice in chat-safe translator output: %s", result)
	}
	if parsed.Get(`messages.#(role=="tool")`).Exists() {
		t.Fatalf("did not expect tool messages in chat-safe translator output: %s", result)
	}
	if parsed.Get(`messages.#(tool_calls)`).Exists() {
		t.Fatalf("did not expect tool_calls in chat-safe translator output: %s", result)
	}
	if parsed.Get(`messages.#(reasoning_content)`).Exists() {
		t.Fatalf("did not expect reasoning_content in chat-safe translator output: %s", result)
	}

	messages := parsed.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("expected only visible text message to survive chat-safe translation, got %d: %s", len(messages), parsed.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "assistant" {
		t.Fatalf("expected surviving role %q, got %q", "assistant", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Visible" {
		t.Fatalf("expected surviving assistant text %q, got %q", "Visible", got)
	}
}

func TestConvertClaudeRequestToOpenAI_ChatSafePreservesClaudeThinkingMetadata(t *testing.T) {
	tests := []struct {
		name         string
		inputJSON    string
		wantThinking string
		wantBudget   int64
		wantEffort   string
	}{
		{
			name: "enabled budget preserved",
			inputJSON: `{
				"model": "gpt-5.4",
				"thinking": {"type":"enabled","budget_tokens":8192},
				"messages": [{"role":"user","content":[{"type":"text","text":"hi"}]}]
			}`,
			wantThinking: "enabled",
			wantBudget:   8192,
		},
		{
			name: "adaptive effort preserved",
			inputJSON: `{
				"model": "gpt-5.4",
				"thinking": {"type":"adaptive"},
				"output_config": {"effort":"high"},
				"messages": [{"role":"user","content":[{"type":"text","text":"hi"}]}]
			}`,
			wantThinking: "adaptive",
			wantEffort:   "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToOpenAI("gpt-5.4", []byte(tt.inputJSON), true)
			parsed := gjson.ParseBytes(result)

			if parsed.Get("reasoning_effort").Exists() {
				t.Fatalf("did not expect reasoning_effort in translator output: %s", result)
			}
			if got := parsed.Get("thinking.type").String(); got != tt.wantThinking {
				t.Fatalf("thinking.type = %q, want %q, body=%s", got, tt.wantThinking, result)
			}
			if tt.wantBudget > 0 {
				if got := parsed.Get("thinking.budget_tokens").Int(); got != tt.wantBudget {
					t.Fatalf("thinking.budget_tokens = %d, want %d, body=%s", got, tt.wantBudget, result)
				}
			}
			if tt.wantEffort != "" {
				if got := parsed.Get("output_config.effort").String(); got != tt.wantEffort {
					t.Fatalf("output_config.effort = %q, want %q, body=%s", got, tt.wantEffort, result)
				}
			}
		})
	}
}
