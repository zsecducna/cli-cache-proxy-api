package responses

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	if v := gjson.GetBytes(rawJSON, "service_tier"); v.Exists() {
		if v.String() != "priority" {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
		}
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	rawJSON = normalizeCodexInputItems(rawJSON)
	rawJSON = normalizeCodexBuiltinTools(rawJSON)

	return rawJSON
}

func normalizeCodexInputItems(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	result := rawJSON
	inputArray := inputResult.Array()
	var idMap map[string]string
	var replacements [][]byte
	changed := false
	for i := 0; i < len(inputArray); i++ {
		item := inputArray[i]
		var itemRaw []byte
		itemChanged := false
		if item.Get("role").String() == "system" {
			var ok bool
			itemRaw, ok = setCodexItemString(itemRaw, item, "role", "developer")
			itemChanged = itemChanged || ok
		}

		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID != "" {
			if idMap == nil {
				idMap = make(map[string]string)
			}
			normalized := idMap[callID]
			if normalized == "" {
				normalized = shortenCodexCallID(callID)
				idMap[callID] = normalized
			}
			if normalized != callID {
				var ok bool
				itemRaw, ok = setCodexItemString(itemRaw, item, "call_id", normalized)
				itemChanged = itemChanged || ok
			}
		}
		if itemChanged {
			if replacements == nil {
				replacements = make([][]byte, len(inputArray))
			}
			replacements[i] = itemRaw
			changed = true
		}
	}
	if !changed {
		return rawJSON
	}
	inputRaw := buildCodexJSONArray(inputArray, replacements)
	if updated, err := sjson.SetRawBytes(result, "input", inputRaw); err == nil {
		return updated
	}
	return rawJSON
}

func shortenCodexCallID(callID string) string {
	callID = strings.TrimSpace(callID)
	if len(callID) <= 64 {
		return callID
	}
	sum := sha256.Sum256([]byte(callID))
	return "call_" + hex.EncodeToString(sum[:])[:40]
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// normalizeCodexBuiltinTools rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := codexArrayFieldPath("tools", i, "type")
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type")

	toolChoiceTools := gjson.GetBytes(result, "tool_choice.tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := codexArrayFieldPath("tool_choice.tools", i, "type")
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	return result
}

func codexArrayFieldPath(arrayPath string, index int, field string) string {
	return arrayPath + "." + strconv.Itoa(index) + "." + field
}

func setCodexItemString(current []byte, item gjson.Result, field string, value string) ([]byte, bool) {
	if current == nil {
		current = []byte(item.Raw)
	}
	updated, err := sjson.SetBytes(current, field, value)
	if err != nil {
		return current, false
	}
	return updated, true
}

func buildCodexJSONArray(items []gjson.Result, replacements [][]byte) []byte {
	totalLen := 2
	if len(items) > 1 {
		totalLen += len(items) - 1
	}
	for i, item := range items {
		if i < len(replacements) && len(replacements[i]) > 0 {
			totalLen += len(replacements[i])
			continue
		}
		totalLen += len(item.Raw)
	}

	out := make([]byte, 0, totalLen)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		if i < len(replacements) && len(replacements[i]) > 0 {
			out = append(out, replacements[i]...)
			continue
		}
		out = append(out, item.Raw...)
	}
	out = append(out, ']')
	return out
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string) []byte {
	currentType := gjson.GetBytes(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}
