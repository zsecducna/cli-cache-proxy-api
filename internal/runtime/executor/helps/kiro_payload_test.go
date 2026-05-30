package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestBuildKiroPayload_CollapseAndHistory verifies system+user collapsing, the trailing
// user message becoming currentMessage, and history alternation.
func TestBuildKiroPayload_CollapseAndHistory(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"user","content":"bye"}
	]}`)
	out, err := BuildKiroPayload(body, "claude-sonnet-4.5", "", false, false, 0)
	if err != nil {
		t.Fatalf("BuildKiroPayload() error = %v", err)
	}
	root := gjson.ParseBytes(out)

	content := root.Get("conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "bye") || !strings.Contains(content, "[Context: Current time is") {
		t.Fatalf("current content missing bye/context prefix: %q", content)
	}
	if model := root.Get("conversationState.currentMessage.userInputMessage.modelId").String(); model != "claude-sonnet-4.5" {
		t.Fatalf("modelId = %q, want claude-sonnet-4.5", model)
	}
	history := root.Get("conversationState.history").Array()
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	if got := history[0].Get("userInputMessage.content").String(); got != "sys\n\nhi" {
		t.Fatalf("history[0] user content = %q, want %q", got, "sys\n\nhi")
	}
	if got := history[1].Get("assistantResponseMessage.content").String(); got != "hello" {
		t.Fatalf("history[1] assistant content = %q, want hello", got)
	}
}

// TestBuildKiroPayload_ToolsNormalized verifies tool specs always carry a well-formed schema.
func TestBuildKiroPayload_ToolsNormalized(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"foo","description":"d","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}},
		{"type":"function","function":{"name":"bar"}}
	]}`)
	out, err := BuildKiroPayload(body, "glm-5", "", false, false, 0)
	if err != nil {
		t.Fatalf("BuildKiroPayload() error = %v", err)
	}
	tools := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if len(tools.Array()) != 2 {
		t.Fatalf("tools length = %d, want 2", len(tools.Array()))
	}
	foo := tools.Array()[0].Get("toolSpecification")
	if foo.Get("name").String() != "foo" {
		t.Fatalf("tool[0] name = %q, want foo", foo.Get("name").String())
	}
	if !foo.Get("inputSchema.json.required").Exists() || foo.Get("inputSchema.json.type").String() != "object" {
		t.Fatalf("tool[0] schema not normalized: %s", foo.Raw)
	}
	// bar had no parameters: must still get a default object schema.
	bar := tools.Array()[1].Get("toolSpecification.inputSchema.json")
	if bar.Get("type").String() != "object" || !bar.Get("required").Exists() || !bar.Get("properties").Exists() {
		t.Fatalf("tool[1] default schema missing fields: %s", bar.Raw)
	}
}

// TestBuildKiroPayload_ToolCallsAndResults verifies assistant tool calls map to toolUses
// and tool messages map to the trailing message's toolResults.
func TestBuildKiroPayload_ToolCallsAndResults(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"run"},
		{"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"foo","arguments":"{\"a\":1}"}}]},
		{"role":"tool","tool_call_id":"t1","content":"result"}
	]}`)
	out, err := BuildKiroPayload(body, "claude-sonnet-4.5", "", false, false, 0)
	if err != nil {
		t.Fatalf("BuildKiroPayload() error = %v", err)
	}
	root := gjson.ParseBytes(out)

	tr := root.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0")
	if tr.Get("toolUseId").String() != "t1" || tr.Get("content.0.text").String() != "result" {
		t.Fatalf("toolResults not mapped: %s", tr.Raw)
	}
	tu := root.Get("conversationState.history.1.assistantResponseMessage.toolUses.0")
	if tu.Get("name").String() != "foo" || tu.Get("toolUseId").String() != "t1" {
		t.Fatalf("toolUses not mapped: %s", tu.Raw)
	}
	if tu.Get("input.a").Int() != 1 {
		t.Fatalf("toolUses input not parsed to object: %s", tu.Raw)
	}
}

// TestBuildKiroPayload_ThinkingAndAgenticPrefixes verifies the content prefixes are injected
// in order when enabled.
func TestBuildKiroPayload_ThinkingAndAgenticPrefixes(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"do it"}]}`)
	out, err := BuildKiroPayload(body, "claude-sonnet-4.5", "", true, true, 8192)
	if err != nil {
		t.Fatalf("BuildKiroPayload() error = %v", err)
	}
	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	for _, want := range []string{"<thinking_mode>enabled</thinking_mode>", "<max_thinking_length>8192</max_thinking_length>", "[Context: Current time is", "CHUNKED WRITE PROTOCOL", "do it"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q: %q", want, content)
		}
	}
	// thinking prefix must precede the context prefix.
	if strings.Index(content, "<thinking_mode>") > strings.Index(content, "[Context:") {
		t.Fatal("thinking prefix should precede context prefix")
	}
}

// TestBuildKiroPayload_ImagesAndInferenceConfig verifies image data-URI stripping and
// sampling parameter mapping.
func TestBuildKiroPayload_ImagesAndInferenceConfig(t *testing.T) {
	body := []byte(`{"max_tokens":1000,"temperature":0.5,"top_p":0.9,"messages":[
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}
	]}`)
	out, err := BuildKiroPayload(body, "claude-sonnet-4.5", "arn:aws:codewhisperer:us-east-1:1:profile/p", false, false, 0)
	if err != nil {
		t.Fatalf("BuildKiroPayload() error = %v", err)
	}
	root := gjson.ParseBytes(out)
	img := root.Get("conversationState.currentMessage.userInputMessage.images.0")
	if img.Get("format").String() != "png" || img.Get("source.bytes").String() != "QUJD" {
		t.Fatalf("image not parsed: %s", img.Raw)
	}
	if root.Get("inferenceConfig.maxTokens").Int() != 1000 || root.Get("inferenceConfig.temperature").Float() != 0.5 || root.Get("inferenceConfig.topP").Float() != 0.9 {
		t.Fatalf("inferenceConfig not mapped: %s", root.Get("inferenceConfig").Raw)
	}
	if root.Get("profileArn").String() != "arn:aws:codewhisperer:us-east-1:1:profile/p" {
		t.Fatalf("profileArn not set: %s", root.Get("profileArn").String())
	}
}

// TestKiroToolNameShortening verifies that tool names exceeding Kiro's 64-char limit are
// shortened deterministically and identically across the tool declaration and the assistant
// tool_use history, and that the restore map recovers the original name.
func TestKiroToolNameShortening(t *testing.T) {
	longName := "mcp__" + strings.Repeat("x", 65) // 70 chars, exceeds the 64-char limit
	short := shortenKiroToolName(longName)
	if len(short) != 64 {
		t.Fatalf("shortened length = %d, want 64", len(short))
	}
	if short != shortenKiroToolName(longName) {
		t.Fatalf("shortenKiroToolName is not deterministic")
	}
	if got := shortenKiroToolName("Glob"); got != "Glob" {
		t.Fatalf("short name should be unchanged, got %q", got)
	}

	body := []byte(`{
		"tools":[{"name":"` + longName + `","description":"d","input_schema":{"type":"object","properties":{}}}],
		"messages":[
			{"role":"user","content":"go"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"` + longName + `","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}
		]
	}`)
	out, err := BuildKiroPayloadFromClaude(body, "claude-opus-4.8", "", false, false, 0)
	if err != nil {
		t.Fatalf("BuildKiroPayloadFromClaude() error = %v", err)
	}
	root := gjson.ParseBytes(out)

	// The declared tool spec name must be the shortened name (<=64 chars).
	specName := root.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.name").String()
	if specName != short {
		t.Fatalf("tool spec name = %q, want %q", specName, short)
	}
	// The assistant tool_use name in history must match the shortened spec name.
	useName := root.Get("conversationState.history.1.assistantResponseMessage.toolUses.0.name").String()
	if useName != short {
		t.Fatalf("history toolUse name = %q, want %q", useName, short)
	}
	// The restore map must recover the original name from the shortened alias.
	if got := KiroToolNameRestoreMap(body, true)[short]; got != longName {
		t.Fatalf("restore map[%q] = %q, want %q", short, got, longName)
	}
}
