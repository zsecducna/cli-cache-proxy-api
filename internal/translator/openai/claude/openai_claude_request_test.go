package claude

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAI_ChatSafeSystemScenarios(t *testing.T) {
	tests := []struct {
		name             string
		inputJSON        string
		wantMessageCount int
		wantSystemTexts  []string
		wantLastRole     string
		wantLastText     string
	}{
		{
			name: "system string becomes system text message",
			inputJSON: `{
				"model": "gpt-5.4",
				"stream": true,
				"system": "Be concise.",
				"messages": [{"role":"user","content":[{"type":"text","text":"Hello"}]}]
			}`,
			wantMessageCount: 2,
			wantSystemTexts:  []string{"Be concise."},
			wantLastRole:     "user",
			wantLastText:     "Hello",
		},
		{
			name: "system text array preserves block order",
			inputJSON: `{
				"model": "gpt-5.4",
				"stream": true,
				"system": [
					{"type":"text","text":"Rule 1"},
					{"type":"text","text":"Rule 2"}
				],
				"messages": [{"role":"assistant","content":[{"type":"text","text":"Understood"}]}]
			}`,
			wantMessageCount: 2,
			wantSystemTexts:  []string{"Rule 1", "Rule 2"},
			wantLastRole:     "assistant",
			wantLastText:     "Understood",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToOpenAI("gpt-5.4", []byte(tt.inputJSON), true)
			parsed := gjson.ParseBytes(result)
			messages := parsed.Get("messages").Array()
			if len(messages) != tt.wantMessageCount {
				t.Fatalf("expected %d messages, got %d: %s", tt.wantMessageCount, len(messages), parsed.Get("messages").Raw)
			}
			for idx, wantText := range tt.wantSystemTexts {
				path := fmt.Sprintf("content.%d.text", idx)
				if got := messages[0].Get(path).String(); got != wantText {
					t.Fatalf("expected system text[%d] %q, got %q", idx, wantText, got)
				}
			}
			if got := messages[len(messages)-1].Get("role").String(); got != tt.wantLastRole {
				t.Fatalf("expected last role %q, got %q", tt.wantLastRole, got)
			}
			if got := messages[len(messages)-1].Get("content.0.text").String(); got != tt.wantLastText {
				t.Fatalf("expected last text %q, got %q", tt.wantLastText, got)
			}
		})
	}
}

func TestConvertClaudeRequestToOpenAI_ChatSafeUsesTopPWhenTemperatureMissing(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"stream": true,
		"top_p": 0.42,
		"messages": [{"role":"user","content":[{"type":"text","text":"Hello"}]}]
	}`

	result := ConvertClaudeRequestToOpenAI("gpt-5.4", []byte(inputJSON), true)
	parsed := gjson.ParseBytes(result)

	if parsed.Get("temperature").Exists() {
		t.Fatalf("did not expect temperature when only top_p was provided: %s", result)
	}
	if got := parsed.Get("top_p").Float(); got != 0.42 {
		t.Fatalf("expected top_p 0.42, got %v", got)
	}
}
