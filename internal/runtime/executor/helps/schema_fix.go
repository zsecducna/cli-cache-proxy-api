package helps

import (
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// EnsureSchemaAdditionalProperties checks common JSON schema locations in
// the request body and recursively injects "additionalProperties": false
// into every object-typed schema node that lacks it. This satisfies the
// upstream OpenAI/Codex requirement without requiring clients to set it.
//
// Paths checked:
//   - text.format.schema          (Responses API)
//   - response_format.json_schema.schema (Chat Completions API)
func EnsureSchemaAdditionalProperties(body []byte) []byte {
	paths := []string{
		"text.format.schema",
		"response_format.json_schema.schema",
	}
	for _, path := range paths {
		schema := gjson.GetBytes(body, path)
		if !schema.Exists() || !schema.IsObject() {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(schema.Raw), &m); err != nil {
			continue
		}
		injectAdditionalProperties(m)
		fixed, err := json.Marshal(m)
		if err != nil {
			continue
		}
		body, _ = sjson.SetRawBytes(body, path, fixed)
	}
	return body
}

func injectAdditionalProperties(schema map[string]interface{}) {
	typ, _ := schema["type"].(string)

	if typ == "object" {
		if _, has := schema["additionalProperties"]; !has {
			schema["additionalProperties"] = false
		}
	}

	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for _, v := range props {
			if sub, ok := v.(map[string]interface{}); ok {
				injectAdditionalProperties(sub)
			}
		}
	}

	if items, ok := schema["items"].(map[string]interface{}); ok {
		injectAdditionalProperties(items)
	}

	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if arr, ok := schema[key].([]interface{}); ok {
			for _, item := range arr {
				if sub, ok := item.(map[string]interface{}); ok {
					injectAdditionalProperties(sub)
				}
			}
		}
	}

	if defs, ok := schema["$defs"].(map[string]interface{}); ok {
		for _, v := range defs {
			if sub, ok := v.(map[string]interface{}); ok {
				injectAdditionalProperties(sub)
			}
		}
	}
	if defs, ok := schema["definitions"].(map[string]interface{}); ok {
		for _, v := range defs {
			if sub, ok := v.(map[string]interface{}); ok {
				injectAdditionalProperties(sub)
			}
		}
	}
}
