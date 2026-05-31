package helps

import (
	"encoding/binary"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This file decodes the AWS EventStream (application/vnd.amazon.eventstream) binary
// frames returned by CodeWhisperer into OpenAI chat.completion.chunk SSE lines, plus a
// non-stream assembler. It is pure and network-free for unit testing.
//
// Frame layout (all integers big-endian):
//
//	[0:4]   total length (bytes of the whole frame)
//	[4:8]   headers length
//	[8:12]  prelude CRC
//	[12 : 12+headersLen]            headers
//	[12+headersLen : total-4]       payload (JSON)
//	[total-4 : total]               message CRC
//
// Each header is: 1-byte name length, name, 1-byte type, then a type-specific value.
// The ":event-type" header (type 7, string) names the event.

// maxKiroFrameSize bounds a single frame to guard against corrupt length prefixes.
const maxKiroFrameSize = 16 << 20 // 16MB

// kiroFrame is one decoded EventStream message.
type kiroFrame struct {
	eventType string
	payload   []byte
}

// parseKiroFrames extracts as many complete frames as possible from buf and returns
// them along with the number of bytes consumed. Incomplete trailing data is left for
// the next call. It never panics on malformed input.
func parseKiroFrames(buf []byte) (frames []kiroFrame, consumed int) {
	for {
		rest := buf[consumed:]
		if len(rest) < 12 {
			break // Not enough bytes for the prelude yet.
		}
		total := binary.BigEndian.Uint32(rest[0:4])
		if total < 16 || total > maxKiroFrameSize {
			break // Invalid/corrupt length; stop to avoid desync, keep leftover.
		}
		if uint32(len(rest)) < total {
			break // Frame split across reads; wait for more bytes.
		}
		headersLen := binary.BigEndian.Uint32(rest[4:8])
		// Header region must fit before the 4-byte message CRC.
		if 12+uint64(headersLen) > uint64(total)-4 {
			consumed += int(total) // Skip this malformed frame.
			continue
		}
		headers := rest[12 : 12+headersLen]
		payload := rest[12+headersLen : total-4]
		frames = append(frames, kiroFrame{
			eventType: parseKiroEventType(headers),
			payload:   append([]byte(nil), payload...),
		})
		consumed += int(total)
	}
	return frames, consumed
}

// parseKiroEventType walks the header block and returns the ":event-type" value.
// It understands all AWS header value types so it can skip past non-string headers.
func parseKiroEventType(headers []byte) string {
	i := 0
	for i < len(headers) {
		nameLen := int(headers[i])
		i++
		if i+nameLen > len(headers) {
			break
		}
		name := string(headers[i : i+nameLen])
		i += nameLen
		if i >= len(headers) {
			break
		}
		htype := headers[i]
		i++
		switch htype {
		case 6, 7: // bytes or string: 2-byte length + value
			if i+2 > len(headers) {
				return ""
			}
			vlen := int(binary.BigEndian.Uint16(headers[i : i+2]))
			i += 2
			if i+vlen > len(headers) {
				return ""
			}
			val := headers[i : i+vlen]
			i += vlen
			if name == ":event-type" {
				return string(val)
			}
		case 0, 1: // bool true / false: no value
		case 2: // byte
			i++
		case 3: // short
			i += 2
		case 4: // int
			i += 4
		case 5, 8: // long / timestamp
			i += 8
		case 9: // uuid
			i += 16
		default:
			return "" // Unknown header type; cannot safely continue.
		}
	}
	return ""
}

// accumulateKiroCacheFrame extracts Kiro's cache-relevant signals from a single frame into
// obs. Kiro reports no token-level cache counts; instead meteringEvent carries the credit
// cost of the request and contextUsageEvent carries the context-window usage fraction. A
// repeated cache-eligible prompt bills fewer credits, so a falling credit cost is the
// observable cache-hit signal. Non-cache frames are ignored. The last frame of each type
// wins (Kiro emits one terminal frame of each per request).
func accumulateKiroCacheFrame(eventType string, payload []byte, obs *KiroCacheObservability) {
	if obs == nil {
		return
	}
	switch eventType {
	case "meteringEvent":
		if v := gjson.GetBytes(payload, "usage"); v.Exists() {
			obs.Credits = v.Float()
		}
	case "contextUsageEvent":
		if v := gjson.GetBytes(payload, "contextUsagePercentage"); v.Exists() {
			obs.ContextUsagePercent = v.Float()
		}
	}
}

// ParseKiroCacheObservability scans a complete Kiro EventStream body and returns the credit
// cost and context-usage signal. Used by the non-streaming assembler path, which already has
// the full body in memory.
func ParseKiroCacheObservability(full []byte) KiroCacheObservability {
	frames, _ := parseKiroFrames(full)
	var obs KiroCacheObservability
	for i := range frames {
		accumulateKiroCacheFrame(frames[i].eventType, frames[i].payload, &obs)
	}
	return obs
}

// KiroEventStreamDecoder incrementally turns EventStream bytes into OpenAI SSE lines.
// Each returned line is "data: {chat.completion.chunk json}". The executor appends the
// terminal "data: [DONE]" itself.
type KiroEventStreamDecoder struct {
	buf           []byte
	model         string
	respID        string
	created       int64
	toolIdx       int
	toolSeen      map[string]int
	sawTool       bool
	finishEmitted bool
	// promptTokens is the estimated input token count; completionChars accumulates emitted
	// output text length. CodeWhisperer returns no token usage, so usage is estimated.
	promptTokens    int
	completionChars int
	// nameRestore maps shortened tool names back to their originals (tool names >64 chars
	// are shortened on the request side to satisfy Kiro's limit).
	nameRestore map[string]string
	// cache accumulates Kiro's credit/context-usage signals from metering/contextUsage
	// frames (Kiro reports no token-level cache counts).
	cache KiroCacheObservability
}

// NewKiroEventStreamDecoder creates a decoder that stamps chunks with the given model.
// promptTokens is the estimated input token count (CodeWhisperer returns no token usage).
// nameRestore (may be nil) restores tool names that were shortened for the upstream request.
func NewKiroEventStreamDecoder(model string, promptTokens int, nameRestore map[string]string) *KiroEventStreamDecoder {
	return &KiroEventStreamDecoder{
		model:        model,
		respID:       "chatcmpl-" + uuid.NewString(),
		created:      time.Now().Unix(),
		toolSeen:     make(map[string]int),
		promptTokens: promptTokens,
		nameRestore:  nameRestore,
	}
}

// Decode appends p to the internal buffer, parses any complete frames, and returns the
// resulting OpenAI SSE lines (possibly empty when frames are still incomplete).
func (d *KiroEventStreamDecoder) Decode(p []byte) [][]byte {
	d.buf = append(d.buf, p...)
	frames, consumed := parseKiroFrames(d.buf)
	if consumed > 0 {
		d.buf = d.buf[consumed:]
	}
	var lines [][]byte
	for i := range frames {
		lines = append(lines, d.convert(frames[i])...)
	}
	return lines
}

// Finish returns the terminal chunks (a finish chunk if not already emitted, then a
// usage chunk when token counts were seen). The executor sends "data: [DONE]" after.
func (d *KiroEventStreamDecoder) Finish() [][]byte {
	var lines [][]byte
	if !d.finishEmitted {
		lines = append(lines, d.finishChunks()...)
	}
	lines = append(lines, d.usageChunk())
	return lines
}

// CacheObservability returns the Kiro credit/context-usage signal accumulated from the
// stream's metering/contextUsage frames.
func (d *KiroEventStreamDecoder) CacheObservability() KiroCacheObservability {
	return d.cache
}

// convert maps a single decoded frame to zero or more OpenAI SSE lines.
func (d *KiroEventStreamDecoder) convert(f kiroFrame) [][]byte {
	root := gjson.ParseBytes(f.payload)
	switch f.eventType {
	case "assistantResponseEvent", "codeEvent":
		if c := root.Get("content").String(); c != "" {
			return [][]byte{d.deltaLine("content", c)}
		}
	case "reasoningContentEvent":
		text := root.Get("text").String()
		if text == "" {
			text = root.Get("content").String()
		}
		if text != "" {
			return [][]byte{d.deltaLine("reasoning_content", text)}
		}
	case "toolUseEvent":
		return d.toolChunks(root)
	case "messageStopEvent":
		return d.finishChunks()
	case "metricsEvent", "contextUsageEvent", "meteringEvent":
		// Bookkeeping only — CodeWhisperer reports credits/context%, not token counts.
		// Capture the credit/context-usage signal for cache statistics.
		accumulateKiroCacheFrame(f.eventType, f.payload, &d.cache)
	default:
		// Some assistant text frames omit an explicit event-type header.
		if c := root.Get("content").String(); c != "" {
			return [][]byte{d.deltaLine("content", c)}
		}
	}
	return nil
}

// baseChunk returns a fresh chat.completion.chunk skeleton stamped with id/model/created.
func (d *KiroEventStreamDecoder) baseChunk() []byte {
	b := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	b, _ = sjson.SetBytes(b, "id", d.respID)
	b, _ = sjson.SetBytes(b, "created", d.created)
	b, _ = sjson.SetBytes(b, "model", d.model)
	return b
}

// deltaLine builds an SSE line carrying a single delta field (content/reasoning_content).
func (d *KiroEventStreamDecoder) deltaLine(field, text string) []byte {
	d.completionChars += len(text)
	b := d.baseChunk()
	b, _ = sjson.SetBytes(b, "choices.0.delta."+field, text)
	return sseLine(b)
}

// toolChunks emits the streamed tool_calls deltas: the first sighting of a tool id emits
// id+name with empty arguments, then each input fragment is emitted as an arguments delta.
func (d *KiroEventStreamDecoder) toolChunks(root gjson.Result) [][]byte {
	toolUseID := root.Get("toolUseId").String()
	name := root.Get("name").String()
	if orig, ok := d.nameRestore[name]; ok {
		name = orig
	}
	d.sawTool = true

	var lines [][]byte
	idx, seen := d.toolSeen[toolUseID]
	if !seen {
		idx = d.toolIdx
		d.toolIdx++
		d.toolSeen[toolUseID] = idx
		start := d.baseChunk()
		tc := `[{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}]`
		start, _ = sjson.SetRawBytes(start, "choices.0.delta.tool_calls", []byte(tc))
		start, _ = sjson.SetBytes(start, "choices.0.delta.tool_calls.0.index", idx)
		start, _ = sjson.SetBytes(start, "choices.0.delta.tool_calls.0.id", toolUseID)
		start, _ = sjson.SetBytes(start, "choices.0.delta.tool_calls.0.function.name", name)
		lines = append(lines, sseLine(start))
	}

	if args := toolInputFragment(root.Get("input")); args != "" {
		d.completionChars += len(args)
		argChunk := d.baseChunk()
		tc := `[{"index":0,"function":{"arguments":""}}]`
		argChunk, _ = sjson.SetRawBytes(argChunk, "choices.0.delta.tool_calls", []byte(tc))
		argChunk, _ = sjson.SetBytes(argChunk, "choices.0.delta.tool_calls.0.index", idx)
		argChunk, _ = sjson.SetBytes(argChunk, "choices.0.delta.tool_calls.0.function.arguments", args)
		lines = append(lines, sseLine(argChunk))
	}
	return lines
}

// finishChunks emits the single finish chunk (idempotent across messageStop/Finish).
func (d *KiroEventStreamDecoder) finishChunks() [][]byte {
	if d.finishEmitted {
		return nil
	}
	d.finishEmitted = true
	reason := "stop"
	if d.sawTool {
		reason = "tool_calls"
	}
	b := d.baseChunk()
	b, _ = sjson.SetBytes(b, "choices.0.finish_reason", reason)
	return [][]byte{sseLine(b)}
}

// usageChunk emits the final OpenAI usage chunk. CodeWhisperer does not return token
// counts, so usage is estimated: prompt from the request, completion from emitted output
// (~4 chars per token, matching the proxy's internal approximation).
func (d *KiroEventStreamDecoder) usageChunk() []byte {
	completion := estimateTokensFromChars(d.completionChars)
	b := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	b, _ = sjson.SetBytes(b, "id", d.respID)
	b, _ = sjson.SetBytes(b, "created", d.created)
	b, _ = sjson.SetBytes(b, "model", d.model)
	b, _ = sjson.SetBytes(b, "usage.prompt_tokens", d.promptTokens)
	b, _ = sjson.SetBytes(b, "usage.completion_tokens", completion)
	b, _ = sjson.SetBytes(b, "usage.total_tokens", d.promptTokens+completion)
	return sseLine(b)
}

// AssembleKiroOpenAIResponse parses a full EventStream body into a single non-streaming
// OpenAI chat.completion response, accumulating text, reasoning, tool calls, and usage.
// nameRestore (may be nil) restores tool names shortened for the upstream request.
func AssembleKiroOpenAIResponse(model string, full []byte, promptTokens int, nameRestore map[string]string) []byte {
	frames, _ := parseKiroFrames(full)
	restore := func(n string) string {
		if orig, ok := nameRestore[n]; ok {
			return orig
		}
		return n
	}

	var content, reasoning strings.Builder
	type toolAcc struct {
		id   string
		name string
		args strings.Builder
	}
	var tools []*toolAcc
	toolByID := make(map[string]*toolAcc)

	for i := range frames {
		f := frames[i]
		root := gjson.ParseBytes(f.payload)
		switch f.eventType {
		case "assistantResponseEvent", "codeEvent":
			content.WriteString(root.Get("content").String())
		case "reasoningContentEvent":
			if t := root.Get("text").String(); t != "" {
				reasoning.WriteString(t)
			} else {
				reasoning.WriteString(root.Get("content").String())
			}
		case "toolUseEvent":
			id := root.Get("toolUseId").String()
			acc, ok := toolByID[id]
			if !ok {
				acc = &toolAcc{id: id, name: restore(root.Get("name").String())}
				toolByID[id] = acc
				tools = append(tools, acc)
			} else if acc.name == "" {
				acc.name = restore(root.Get("name").String())
			}
			acc.args.WriteString(toolInputFragment(root.Get("input")))
		case "metricsEvent", "contextUsageEvent", "meteringEvent":
			// Bookkeeping only — CodeWhisperer reports credits/context%, not token counts.
		default:
			content.WriteString(root.Get("content").String())
		}
	}

	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", "chatcmpl-"+uuid.NewString())
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "choices.0.message.content", content.String())
	if reasoning.Len() > 0 {
		out, _ = sjson.SetBytes(out, "choices.0.message.reasoning_content", reasoning.String())
	}
	if len(tools) > 0 {
		for _, acc := range tools {
			tc := `{"id":"","type":"function","function":{"name":"","arguments":""}}`
			tcBytes := []byte(tc)
			tcBytes, _ = sjson.SetBytes(tcBytes, "id", acc.id)
			tcBytes, _ = sjson.SetBytes(tcBytes, "function.name", acc.name)
			tcBytes, _ = sjson.SetBytes(tcBytes, "function.arguments", acc.args.String())
			out, _ = sjson.SetRawBytes(out, "choices.0.message.tool_calls.-1", tcBytes)
		}
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
	}
	// CodeWhisperer returns no token counts; estimate completion from emitted output.
	completionChars := content.Len() + reasoning.Len()
	for _, acc := range tools {
		completionChars += acc.args.Len()
	}
	completion := estimateTokensFromChars(completionChars)
	out, _ = sjson.SetBytes(out, "usage.prompt_tokens", promptTokens)
	out, _ = sjson.SetBytes(out, "usage.completion_tokens", completion)
	out, _ = sjson.SetBytes(out, "usage.total_tokens", promptTokens+completion)
	return out
}

// toolInputFragment renders a toolUseEvent input as an arguments-string fragment.
// String inputs (the common streamed case) are used verbatim; objects are kept raw.
func toolInputFragment(input gjson.Result) string {
	if !input.Exists() {
		return ""
	}
	if input.Type == gjson.String {
		return input.String()
	}
	return input.Raw
}

// estimateTokensFromChars approximates a token count from a character count (~4 chars
// per token), matching the proxy's internal token approximation. CodeWhisperer returns
// no token usage, so prompt/completion tokens are estimated this way.
func estimateTokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// EstimateOpenAIPromptTokens approximates prompt tokens from an OpenAI request body by
// summing message content lengths (~4 chars per token). Used because CodeWhisperer
// returns no token usage in its response.
func EstimateOpenAIPromptTokens(openaiBody []byte) int {
	chars := 0
	gjson.GetBytes(openaiBody, "messages").ForEach(func(_, msg gjson.Result) bool {
		chars += len(msg.Get("content").String())
		return true
	})
	return estimateTokensFromChars(chars)
}

// sseLine prefixes a JSON chunk with the SSE "data: " marker expected by the translators.
func sseLine(b []byte) []byte {
	return append([]byte("data: "), b...)
}
