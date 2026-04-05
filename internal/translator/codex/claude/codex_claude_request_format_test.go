package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodex_MapsOutputConfigFormatJSONSchema(t *testing.T) {
	inputJSON := `{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],
		"output_config":{
			"effort":"medium",
			"format":{
				"type":"json_schema",
				"name":"session_title",
				"strict":true,
				"schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}
			}
		}
	}`

	result := ConvertClaudeRequestToCodex("gpt-5.4", []byte(inputJSON), true)
	root := gjson.ParseBytes(result)

	if got := root.Get("text.format.type").String(); got != "json_schema" {
		t.Fatalf("text.format.type = %q, want %q, body=%s", got, "json_schema", result)
	}
	if got := root.Get("text.format.name").String(); got != "session_title" {
		t.Fatalf("text.format.name = %q, want %q, body=%s", got, "session_title", result)
	}
	if !root.Get("text.format.strict").Bool() {
		t.Fatalf("text.format.strict = false, want true, body=%s", result)
	}
	if got := root.Get("text.format.schema.properties.title.type").String(); got != "string" {
		t.Fatalf("text.format.schema.properties.title.type = %q, want %q, body=%s", got, "string", result)
	}
	if got := root.Get("reasoning.effort").String(); got != "medium" {
		t.Fatalf("reasoning.effort = %q, want %q, body=%s", got, "medium", result)
	}
}
