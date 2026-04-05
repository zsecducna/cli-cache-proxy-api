package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestValidateClaudeRequestSyntax_AcceptsSupportedSubset(t *testing.T) {
	request := []byte(`{
		"model": "gpt-5.4",
		"stream": true,
		"max_tokens": 512,
		"system": [{"type":"text","text":"You are helpful."}],
		"tools": [{
			"name": "lookup_weather",
			"description": "Look up weather",
			"input_schema": {
				"type": "object",
				"properties": {"city": {"type": "string"}},
				"required": ["city"]
			}
		}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Weather in Hanoi?"}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"lookup_weather","input":{"city":"Hanoi"}},
				{"type":"tool_use","id":"call_2","name":"lookup_weather","input":{"city":"Saigon"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":{"b":2,"a":1}},
				{"type":"tool_result","tool_use_id":"call_2","content":"hot"}
			]}
		]
	}`)

	if err := ValidateClaudeRequestSyntax(request); err != nil {
		t.Fatalf("expected syntax validation to accept supported subset, got %v", err)
	}
}

func TestValidateClaudeRequestSyntax_RejectsUnsupportedCases(t *testing.T) {
	tests := []struct {
		name       string
		request    string
		wantClass  string
		wantReason string
	}{
		{
			name:       "non-stream request",
			request:    `{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantClass:  CompatibilityClassStreamingNotSupported,
			wantReason: "streaming_required",
		},
		{
			name:       "assistant mixes text and tool_use",
			request:    `{"model":"gpt-5.4","stream":true,"messages":[{"role":"assistant","content":[{"type":"text","text":"pre"},{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]}]}`,
			wantClass:  CompatibilityClassIncompatibleRequest,
			wantReason: "assistant_mixed_text_and_tool_use",
		},
		{
			name:       "tool schema must be object",
			request:    `{"model":"gpt-5.4","stream":true,"tools":[{"name":"lookup","input_schema":[]}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantClass:  CompatibilityClassIncompatibleRequest,
			wantReason: "tool_schema_must_be_object",
		},
		{
			name:       "thinking controls rejected",
			request:    `{"model":"gpt-5.4","stream":true,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantClass:  CompatibilityClassIncompatibleRequest,
			wantReason: "thinking_not_supported",
		},
		{
			name:       "image content rejected",
			request:    `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/image.png"}}]}]}`,
			wantClass:  CompatibilityClassIncompatibleRequest,
			wantReason: "unsupported_content_block_image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClaudeRequestSyntax([]byte(tt.request))
			if err == nil {
				t.Fatalf("expected syntax validation error")
			}
			compatErr, ok := err.(*CompatibilityError)
			if !ok {
				t.Fatalf("expected CompatibilityError, got %T", err)
			}
			if compatErr.Class != tt.wantClass {
				t.Fatalf("expected class %q, got %q", tt.wantClass, compatErr.Class)
			}
			if compatErr.Reason != tt.wantReason {
				t.Fatalf("expected reason %q, got %q", tt.wantReason, compatErr.Reason)
			}
		})
	}
}

func TestValidateClaudeRequestForSurface_ResponsesCanonicalizesToolResultJSON(t *testing.T) {
	request := []byte(`{
		"model": "gpt-5.4",
		"stream": true,
		"tools": [{
			"name": "lookup_weather",
			"input_schema": {"type":"object","properties":{"city":{"type":"string"}}}
		}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Weather in Hanoi?"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup_weather","input":{"city":"Hanoi"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":{"z":1,"a":{"d":4,"c":3}}}]}
		]
	}`)

	normalized, err := ValidateClaudeRequestForSurface(request, BackendCapabilities{
		SupportsOpenAIResponses: true,
		SupportsTools:           true,
		SupportsStreaming:       true,
	}, BackendSurfaceResponses)
	if err != nil {
		t.Fatalf("expected responses backend-pass validation to accept transcript, got %v", err)
	}

	got := gjson.GetBytes(normalized, "messages.2.content.0.content").Raw
	want := `{"a":{"c":3,"d":4},"z":1}`
	if got != want {
		t.Fatalf("expected canonical tool_result JSON %s, got %s", want, got)
	}
}

func TestValidateClaudeRequestForSurface_RejectsTranscriptInvariantViolations(t *testing.T) {
	tests := []struct {
		name       string
		request    string
		surface    BackendSurface
		caps       BackendCapabilities
		wantClass  string
		wantReason string
	}{
		{
			name: "responses tool results must match assistant order",
			request: `{
				"model":"gpt-5.4",
				"stream":true,
				"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
				"messages":[
					{"role":"assistant","content":[
						{"type":"tool_use","id":"call_1","name":"lookup","input":{}},
						{"type":"tool_use","id":"call_2","name":"lookup","input":{}}
					]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"call_2","content":"second"},
						{"type":"tool_result","tool_use_id":"call_1","content":"first"}
					]}
				]
			}`,
			surface:    BackendSurfaceResponses,
			caps:       BackendCapabilities{SupportsOpenAIResponses: true, SupportsTools: true, SupportsStreaming: true},
			wantClass:  CompatibilityClassSurfaceNotSupported,
			wantReason: "tool_result_order_mismatch",
		},
		{
			name: "responses tool result turn cannot mix text",
			request: `{
				"model":"gpt-5.4",
				"stream":true,
				"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},
					{"role":"user","content":[
						{"type":"text","text":"before"},
						{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
					]}
				]
			}`,
			surface:    BackendSurfaceResponses,
			caps:       BackendCapabilities{SupportsOpenAIResponses: true, SupportsTools: true, SupportsStreaming: true},
			wantClass:  CompatibilityClassSurfaceNotSupported,
			wantReason: "tool_result_turn_must_not_mix_text",
		},
		{
			name: "chat completions fallback is text only",
			request: `{
				"model":"gpt-5.4",
				"stream":true,
				"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
				"messages":[
					{"role":"user","content":[{"type":"text","text":"hello"}]},
					{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]}
				]
			}`,
			surface:    BackendSurfaceChatCompletions,
			caps:       BackendCapabilities{SupportsChatCompletions: true, SupportsStreaming: true},
			wantClass:  CompatibilityClassSurfaceNotSupported,
			wantReason: "chat_completions_requires_text_only",
		},
		{
			name:       "backend without streaming rejected",
			request:    `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			surface:    BackendSurfaceResponses,
			caps:       BackendCapabilities{SupportsOpenAIResponses: true},
			wantClass:  CompatibilityClassStreamingNotSupported,
			wantReason: "backend_streaming_required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateClaudeRequestForSurface([]byte(tt.request), tt.caps, tt.surface)
			if err == nil {
				t.Fatalf("expected backend-pass validation error")
			}
			compatErr, ok := err.(*CompatibilityError)
			if !ok {
				t.Fatalf("expected CompatibilityError, got %T", err)
			}
			if compatErr.Class != tt.wantClass {
				t.Fatalf("expected class %q, got %q", tt.wantClass, compatErr.Class)
			}
			if compatErr.Reason != tt.wantReason {
				t.Fatalf("expected reason %q, got %q", tt.wantReason, compatErr.Reason)
			}
		})
	}
}
