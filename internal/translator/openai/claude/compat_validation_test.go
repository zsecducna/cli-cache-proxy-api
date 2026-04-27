package claude

import "testing"

func TestValidateClaudeViaOpenAIRequest_AcceptsSupportedSubset(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "text only with supported scalar settings",
			raw: `{
				"model":"gpt-5.4-custom",
				"stream":true,
				"system":[{"type":"text","text":"be precise"}],
				"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
				"max_tokens":64,
				"temperature":0.2,
				"top_p":0.9,
				"stop_sequences":["DONE"]
			}`,
		},
		{
			name: "non-stream text request",
			raw:  `{"model":"gpt-5.4-custom","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		},
		{
			name: "single tool turn transcript",
			raw: `{
				"model":"gpt-5.4-custom",
				"stream":true,
				"tools":[{"name":"lookup","description":"lookup","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}}],
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"lookup","input":{"id":"123"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":{"ok":true,"value":"x"}}]}
				]
			}`,
		},
		{
			name: "multi tool transcript",
			raw: `{
				"model":"gpt-5.4-custom",
				"stream":true,
				"tools":[{"name":"a","input_schema":{"type":"object"}},{"name":"b","input_schema":{"type":"object"}}],
				"messages":[
					{"role":"assistant","content":[
						{"type":"tool_use","id":"tool_1","name":"a","input":{"x":1}},
						{"type":"tool_use","id":"tool_2","name":"b","input":{"y":2}}
					]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"tool_1","content":{"x":1,"a":2}},
						{"type":"tool_result","tool_use_id":"tool_2","content":{"y":2,"b":3}}
					]}
				]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateClaudeViaOpenAIRequest([]byte(tt.raw), true, true, true, true); err != nil {
				t.Fatalf("ValidateClaudeViaOpenAIRequest() error = %v", err)
			}
		})
	}
}

func TestValidateClaudeViaOpenAIRequest_RejectsUnsupportedSyntax(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantReason string
	}{
		{
			name:       "thinking not supported",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantReason: "thinking_not_supported",
		},
		{
			name:       "output effort not supported",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"output_config":{"effort":"high"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantReason: "thinking_not_supported",
		},
		{
			name:       "non text content block",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
			wantReason: "content_type_not_supported",
		},
		{
			name:       "assistant mixes text and tool use",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"tool_1","name":"lookup","input":{"id":"1"}}]}]}`,
			wantReason: "mixed_assistant_content_not_supported",
		},
		{
			name:       "empty content array",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"user","content":[]}]}`,
			wantReason: "empty_content_not_supported",
		},
		{
			name:       "duplicate tool use ids",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"a","input":{}},{"type":"tool_use","id":"tool_1","name":"b","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"ok"},{"type":"tool_result","tool_use_id":"tool_1","content":"ok"}]}]}`,
			wantReason: "duplicate_tool_use_id",
		},
		{
			name:       "duplicate tool result ids",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"a","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"ok"},{"type":"tool_result","tool_use_id":"tool_1","content":"ok"}]}]}`,
			wantReason: "duplicate_tool_result_id",
		},
		{
			name:       "missing tool result partner",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"a","input":{}}]},{"role":"user","content":[{"type":"text","text":"later"}]}]}`,
			wantReason: "tool_result_partner_missing",
		},
		{
			name:       "reordered tool results",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"a","input":{}},{"type":"tool_use","id":"tool_2","name":"b","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_2","content":"ok"},{"type":"tool_result","tool_use_id":"tool_1","content":"ok"}]}]}`,
			wantReason: "tool_result_order_mismatch",
		},
		{
			name:       "tool schema must be object",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"tools":[{"name":"lookup","input_schema":"bad"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantReason: "tool_schema_not_object",
		},
		{
			name:       "tool input must be object",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"lookup","input":"bad"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"ok"}]}]}`,
			wantReason: "tool_input_not_object",
		},
		{
			name:       "tool result payload must be text or object",
			raw:        `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":[1,2,3]}]}]}`,
			wantReason: "tool_result_content_not_supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClaudeViaOpenAIRequest([]byte(tt.raw), true, true, true, true)
			if err == nil {
				t.Fatal("ValidateClaudeViaOpenAIRequest() error = nil, want non-nil")
			}
			if err.Reason != tt.wantReason {
				t.Fatalf("error reason = %q, want %q", err.Reason, tt.wantReason)
			}
			if err.Stage != "syntax" {
				t.Fatalf("error stage = %q, want %q", err.Stage, "syntax")
			}
		})
	}
}

func TestValidateClaudeViaOpenAIRequest_BackendPass(t *testing.T) {
	const toolRequest = `{
		"model":"gpt-5.4-custom",
		"stream":true,
		"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"lookup","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":{"ok":true}}]}
		]
	}`

	tests := []struct {
		name              string
		raw               string
		supportsResponses bool
		supportsChat      bool
		supportsTools     bool
		supportsStreaming bool
		wantReason        string
		wantStage         string
	}{
		{
			name:              "streaming support required",
			raw:               `{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			supportsResponses: true,
			supportsChat:      true,
			supportsTools:     true,
			supportsStreaming: false,
			wantReason:        "streaming_not_supported",
			wantStage:         "backend",
		},
		{
			name:              "tools require tool support",
			raw:               toolRequest,
			supportsResponses: true,
			supportsChat:      true,
			supportsTools:     false,
			supportsStreaming: true,
			wantReason:        "tools_not_supported",
			wantStage:         "backend",
		},
		{
			name:              "chat only rejects non safe subset",
			raw:               toolRequest,
			supportsResponses: false,
			supportsChat:      true,
			supportsTools:     true,
			supportsStreaming: true,
			wantReason:        "surface_not_supported",
			wantStage:         "backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClaudeViaOpenAIRequest([]byte(tt.raw), tt.supportsResponses, tt.supportsChat, tt.supportsTools, tt.supportsStreaming)
			if err == nil {
				t.Fatal("ValidateClaudeViaOpenAIRequest() error = nil, want non-nil")
			}
			if err.Reason != tt.wantReason {
				t.Fatalf("error reason = %q, want %q", err.Reason, tt.wantReason)
			}
			if err.Stage != tt.wantStage {
				t.Fatalf("error stage = %q, want %q", err.Stage, tt.wantStage)
			}
		})
	}
}

func TestValidateClaudeViaOpenAIRequest_NonStreamDoesNotRequireStreamingSupport(t *testing.T) {
	err := ValidateClaudeViaOpenAIRequest([]byte(`{"model":"gpt-5.4-custom","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`), true, true, true, false)
	if err != nil {
		t.Fatalf("ValidateClaudeViaOpenAIRequest() error = %v", err)
	}
}

func TestFitsClaudeViaOpenAIChatSafeSubset(t *testing.T) {
	if !FitsClaudeViaOpenAIChatSafeSubset([]byte(`{"model":"gpt-5.4-custom","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":32,"temperature":0.2,"stop_sequences":["DONE"]}`)) {
		t.Fatal("FitsClaudeViaOpenAIChatSafeSubset() = false, want true")
	}
	if FitsClaudeViaOpenAIChatSafeSubset([]byte(`{"model":"gpt-5.4-custom","stream":true,"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"lookup","input":{}}]}]}`)) {
		t.Fatal("FitsClaudeViaOpenAIChatSafeSubset() = true, want false for tool-bearing request")
	}
}

func TestCanonicalizeToolResultObject_StableKeyOrdering(t *testing.T) {
	got, ok := canonicalizeToolResultContent(`{"z":1,"a":{"d":4,"b":2},"m":[{"y":2,"x":1}]}`)
	if !ok {
		t.Fatal("canonicalizeToolResultContent() ok = false, want true")
	}
	want := `{"a":{"b":2,"d":4},"m":[{"x":1,"y":2}],"z":1}`
	if got != want {
		t.Fatalf("canonicalizeToolResultContent() = %q, want %q", got, want)
	}
}
