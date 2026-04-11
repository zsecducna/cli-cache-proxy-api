package responses

import (
	"bytes"
	"context"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexResponsesNonStreamToolCall struct {
	ID                string
	Name              string
	Arguments         string
	HasArgumentsDelta bool
}

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).
func ConvertCodexResponseToOpenAIResponses(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) [][]byte {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
		rawJSON = injectCodexResponseRequestEcho(rawJSON, originalRequestRawJSON, requestRawJSON)
		out := make([]byte, 0, len(rawJSON)+len("data: "))
		out = append(out, []byte("data: ")...)
		out = append(out, rawJSON...)
		return [][]byte{out}
	}
	return [][]byte{injectCodexResponseRequestEcho(rawJSON, originalRequestRawJSON, requestRawJSON)}
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	responseResult, createdResult, synthesizedOutput, ok := collectCodexResponsesNonStreamResponse(originalRequestRawJSON, rawJSON)
	if !ok {
		return []byte{}
	}
	resp := []byte(responseResult.Raw)
	if !gjson.GetBytes(resp, "output").Exists() || len(gjson.GetBytes(resp, "output").Array()) == 0 {
		if len(synthesizedOutput) > 0 {
			resp, _ = sjson.SetRawBytes(resp, "output", synthesizedOutput)
		}
	}
	if !gjson.GetBytes(resp, "id").Exists() {
		if v := createdResult.Get("id"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "id", v.String())
		}
	}
	if !gjson.GetBytes(resp, "created_at").Exists() {
		if v := createdResult.Get("created_at"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "created_at", v.Int())
		}
	}
	if !gjson.GetBytes(resp, "model").Exists() {
		if v := createdResult.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		}
	}
	return injectCodexResponseTopLevelEcho(resp, originalRequestRawJSON, requestRawJSON)
}

func collectCodexResponsesNonStreamResponse(originalRequestRawJSON, rawJSON []byte) (gjson.Result, gjson.Result, []byte, bool) {
	rootResult := gjson.ParseBytes(rawJSON)
	if rootResult.Get("type").String() == "response.completed" {
		responseResult := rootResult.Get("response")
		return responseResult, gjson.Result{}, nil, responseResult.Exists()
	}

	rev := buildReverseMapFromOriginalOpenAI(originalRequestRawJSON)
	var createdResult gjson.Result
	var contentBuilder bytes.Buffer
	var reasoningBuilder bytes.Buffer
	var toolCalls []codexResponsesNonStreamToolCall
	lines := bytes.Split(rawJSON, []byte{'\n'})
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}

		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		event := gjson.ParseBytes(payload)
		switch event.Get("type").String() {
		case "response.created":
			createdResult = event.Get("response")
		case "response.output_text.delta":
			contentBuilder.WriteString(event.Get("delta").String())
		case "response.reasoning_summary_text.delta":
			reasoningBuilder.WriteString(event.Get("delta").String())
		case "response.reasoning_summary_text.done":
			if reasoningBuilder.Len() == 0 {
				reasoningBuilder.WriteString(event.Get("text").String())
			}
		case "response.output_item.added":
			itemResult := event.Get("item")
			if itemResult.Get("type").String() != "function_call" {
				continue
			}
			toolCalls = append(toolCalls, codexResponsesNonStreamToolCall{
				ID:   itemResult.Get("call_id").String(),
				Name: restoreCodexResponsesToolName(rev, itemResult.Get("name").String()),
			})
		case "response.function_call_arguments.delta":
			toolCall := ensureLastCodexResponsesToolCall(&toolCalls)
			toolCall.Arguments += event.Get("delta").String()
			toolCall.HasArgumentsDelta = true
		case "response.function_call_arguments.done":
			toolCall := ensureLastCodexResponsesToolCall(&toolCalls)
			if !toolCall.HasArgumentsDelta {
				toolCall.Arguments = event.Get("arguments").String()
			}
		case "response.output_item.done":
			itemResult := event.Get("item")
			if itemResult.Get("type").String() != "function_call" {
				continue
			}
			toolCall := ensureLastCodexResponsesToolCall(&toolCalls)
			if toolCall.ID == "" {
				toolCall.ID = itemResult.Get("call_id").String()
			}
			if toolCall.Name == "" {
				toolCall.Name = restoreCodexResponsesToolName(rev, itemResult.Get("name").String())
			}
			if toolCall.Arguments == "" {
				toolCall.Arguments = itemResult.Get("arguments").String()
			}
		case "response.completed":
			responseResult := event.Get("response")
			if !responseResult.Exists() {
				return gjson.Result{}, gjson.Result{}, nil, false
			}
			return responseResult, createdResult, synthesizeCodexResponsesOutput(contentBuilder.String(), reasoningBuilder.String(), toolCalls), true
		}
	}

	return gjson.Result{}, gjson.Result{}, nil, false
}

func synthesizeCodexResponsesOutput(contentText, reasoningText string, toolCalls []codexResponsesNonStreamToolCall) []byte {
	output := []byte(`[]`)

	if reasoningText != "" {
		reasoningItem := []byte(`{"type":"reasoning","summary":[{"type":"summary_text","text":""}]}`)
		reasoningItem, _ = sjson.SetBytes(reasoningItem, "summary.0.text", reasoningText)
		output, _ = sjson.SetRawBytes(output, "-1", reasoningItem)
	}

	if contentText != "" {
		messageItem := []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"","annotations":[],"logprobs":[]}],"status":"completed"}`)
		messageItem, _ = sjson.SetBytes(messageItem, "content.0.text", contentText)
		output, _ = sjson.SetRawBytes(output, "-1", messageItem)
	}

	for _, toolCall := range toolCalls {
		functionCallItem := []byte(`{"type":"function_call","call_id":"","name":"","arguments":"","status":"completed"}`)
		if toolCall.ID != "" {
			functionCallItem, _ = sjson.SetBytes(functionCallItem, "call_id", toolCall.ID)
		}
		if toolCall.Name != "" {
			functionCallItem, _ = sjson.SetBytes(functionCallItem, "name", toolCall.Name)
		}
		functionCallItem, _ = sjson.SetBytes(functionCallItem, "arguments", toolCall.Arguments)
		output, _ = sjson.SetRawBytes(output, "-1", functionCallItem)
	}

	if len(gjson.ParseBytes(output).Array()) == 0 {
		return nil
	}
	return output
}

func ensureLastCodexResponsesToolCall(toolCalls *[]codexResponsesNonStreamToolCall) *codexResponsesNonStreamToolCall {
	if len(*toolCalls) == 0 {
		*toolCalls = append(*toolCalls, codexResponsesNonStreamToolCall{})
	}
	return &(*toolCalls)[len(*toolCalls)-1]
}

func restoreCodexResponsesToolName(rev map[string]string, name string) string {
	if orig, ok := rev[name]; ok {
		return orig
	}
	return name
}

func buildReverseMapFromOriginalOpenAI(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	rev := map[string]string{}
	if tools.IsArray() && len(tools.Array()) > 0 {
		var names []string
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			if t.Get("type").String() != "function" {
				continue
			}
			fn := t.Get("function")
			if !fn.Exists() {
				continue
			}
			if v := fn.Get("name"); v.Exists() {
				names = append(names, v.String())
			}
		}
		if len(names) > 0 {
			m := buildShortNameMap(names)
			for orig, short := range m {
				rev[short] = orig
			}
		}
	}
	return rev
}

func buildShortNameMap(names []string) map[string]string {
	if len(names) == 0 {
		return map[string]string{}
	}

	counts := map[string]int{}
	used := map[string]struct{}{}
	out := map[string]string{}

	for _, n := range names {
		short := n
		if len(short) > 64 {
			short = short[:64]
		}
		short = normalizeToolShortName(short)
		if short == "" {
			short = "tool"
		}
		base := short
		counts[base]++
		if _, exists := used[short]; exists || counts[base] > 1 {
			suffix := counts[base]
			for {
				candidate := trimToolShortName(base, 64-len(intToString(suffix))-1) + "_" + intToString(suffix)
				if _, exists := used[candidate]; !exists {
					short = candidate
					break
				}
				suffix++
			}
		}
		used[short] = struct{}{}
		out[n] = short
	}
	return out
}

func normalizeToolShortName(name string) string {
	buf := make([]byte, 0, len(name))
	lastUnderscore := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			if c >= 'A' && c <= 'Z' {
				c = c - 'A' + 'a'
			}
			buf = append(buf, c)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			buf = append(buf, '_')
			lastUnderscore = true
		}
	}
	for len(buf) > 0 && buf[0] == '_' {
		buf = buf[1:]
	}
	for len(buf) > 0 && buf[len(buf)-1] == '_' {
		buf = buf[:len(buf)-1]
	}
	return string(buf)
}

func trimToolShortName(name string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(name) <= max {
		return name
	}
	return name[:max]
}

func intToString(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

type codexRequestEchoField struct {
	requestPath string
	targetPath  string
}

func injectCodexResponseRequestEcho(rawJSON, originalRequestRawJSON, requestRawJSON []byte) []byte {
	root := gjson.ParseBytes(rawJSON)
	if root.Get("type").String() != "response.completed" {
		return rawJSON
	}
	request := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	if len(request) == 0 {
		return rawJSON
	}
	return injectCodexRequestEchoFields(rawJSON, gjson.ParseBytes(request), []codexRequestEchoField{
		{requestPath: "previous_response_id", targetPath: "response.previous_response_id"},
		{requestPath: "prompt_cache_key", targetPath: "response.prompt_cache_key"},
	})
}

func injectCodexResponseTopLevelEcho(rawJSON, originalRequestRawJSON, requestRawJSON []byte) []byte {
	request := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	if len(request) == 0 {
		return rawJSON
	}
	return injectCodexRequestEchoFields(rawJSON, gjson.ParseBytes(request), []codexRequestEchoField{
		{requestPath: "previous_response_id", targetPath: "previous_response_id"},
		{requestPath: "prompt_cache_key", targetPath: "prompt_cache_key"},
	})
}

func injectCodexRequestEchoFields(rawJSON []byte, request gjson.Result, fields []codexRequestEchoField) []byte {
	updated := append([]byte(nil), rawJSON...)
	for _, field := range fields {
		if gjson.GetBytes(updated, field.targetPath).Exists() {
			continue
		}
		if value := request.Get(field.requestPath); value.Exists() {
			updated, _ = sjson.SetBytes(updated, field.targetPath, value.String())
		}
	}
	return updated
}
