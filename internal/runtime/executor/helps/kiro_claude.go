package helps

import (
	"strings"

	"github.com/google/uuid"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This file translates the Kiro (CodeWhisperer) binary EventStream DIRECTLY into the
// Anthropic Messages format — both a streaming SSE encoder and a non-stream assembler —
// without pivoting through the OpenAI schema. It reuses the frame parser, tool-input
// helper, and token estimator from kiro_eventstream.go.

// KiroClaudeStreamEncoder incrementally turns Kiro EventStream bytes into Anthropic
// Messages SSE events (message_start, content_block_*, message_delta, message_stop).
type KiroClaudeStreamEncoder struct {
	buf             []byte
	model           string
	msgID           string
	promptTokens    int
	completionChars int
	nameRestore     map[string]string

	started   bool   // message_start emitted
	finished  bool   // terminal events emitted
	curKind   string // currently open block: "", "text", "thinking", "tool"
	curIndex  int    // index of the currently open block
	nextIndex int    // next content block index to assign
	curToolID string // toolUseId of the currently open tool block
	sawTool   bool
}

// NewKiroClaudeStreamEncoder creates an encoder stamped with the given model. promptTokens
// is the estimated input count; nameRestore (may be nil) maps shortened tool names back.
func NewKiroClaudeStreamEncoder(model string, promptTokens int, nameRestore map[string]string) *KiroClaudeStreamEncoder {
	return &KiroClaudeStreamEncoder{
		model:        model,
		msgID:        "msg_" + uuid.NewString(),
		promptTokens: promptTokens,
		nameRestore:  nameRestore,
	}
}

// Encode appends p, parses complete frames, and returns the resulting Anthropic SSE lines.
func (e *KiroClaudeStreamEncoder) Encode(p []byte) [][]byte {
	e.buf = append(e.buf, p...)
	frames, consumed := parseKiroFrames(e.buf)
	if consumed > 0 {
		e.buf = e.buf[consumed:]
	}
	var out [][]byte
	for i := range frames {
		out = append(out, e.convert(frames[i])...)
	}
	return out
}

// Finish emits the terminal content_block_stop/message_delta/message_stop events (once).
func (e *KiroClaudeStreamEncoder) Finish() [][]byte {
	if e.finished {
		return nil
	}
	e.finished = true
	var out [][]byte
	e.ensureStarted(&out)
	e.closeBlock(&out)
	stop := "end_turn"
	if e.sawTool {
		stop = "tool_use"
	}
	md := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
	md, _ = sjson.SetBytes(md, "delta.stop_reason", stop)
	md, _ = sjson.SetBytes(md, "usage.input_tokens", e.promptTokens)
	md, _ = sjson.SetBytes(md, "usage.output_tokens", estimateTokensFromChars(e.completionChars))
	out = append(out, sseEvent("message_delta", md))
	out = append(out, sseEvent("message_stop", []byte(`{"type":"message_stop"}`)))
	return out
}

// UsageDetail returns the estimated usage for reporting (CodeWhisperer returns no counts).
func (e *KiroClaudeStreamEncoder) UsageDetail() usage.Detail {
	completion := estimateTokensFromChars(e.completionChars)
	return usage.Detail{InputTokens: int64(e.promptTokens), OutputTokens: int64(completion), TotalTokens: int64(e.promptTokens + completion)}
}

// convert maps a single Kiro frame to zero or more Anthropic SSE lines.
func (e *KiroClaudeStreamEncoder) convert(f kiroFrame) [][]byte {
	root := gjson.ParseBytes(f.payload)
	var out [][]byte
	switch f.eventType {
	case "assistantResponseEvent", "codeEvent":
		if c := root.Get("content").String(); c != "" {
			e.ensureStarted(&out)
			e.ensureSimpleBlock("text", &out)
			e.completionChars += len(c)
			d := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
			d, _ = sjson.SetBytes(d, "index", e.curIndex)
			d, _ = sjson.SetBytes(d, "delta.text", c)
			out = append(out, sseEvent("content_block_delta", d))
		}
	case "reasoningContentEvent":
		text := root.Get("text").String()
		if text == "" {
			text = root.Get("content").String()
		}
		if text != "" {
			e.ensureStarted(&out)
			e.ensureSimpleBlock("thinking", &out)
			e.completionChars += len(text)
			d := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			d, _ = sjson.SetBytes(d, "index", e.curIndex)
			d, _ = sjson.SetBytes(d, "delta.thinking", text)
			out = append(out, sseEvent("content_block_delta", d))
		}
	case "toolUseEvent":
		e.ensureStarted(&out)
		if id := root.Get("toolUseId").String(); id != "" && id != e.curToolID {
			e.closeBlock(&out)
			name := root.Get("name").String()
			if orig, ok := e.nameRestore[name]; ok {
				name = orig
			}
			e.curIndex = e.nextIndex
			e.nextIndex++
			e.curKind = "tool"
			e.curToolID = id
			e.sawTool = true
			s := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
			s, _ = sjson.SetBytes(s, "index", e.curIndex)
			s, _ = sjson.SetBytes(s, "content_block.id", util.SanitizeClaudeToolID(id))
			s, _ = sjson.SetBytes(s, "content_block.name", name)
			out = append(out, sseEvent("content_block_start", s))
		}
		if frag := toolInputFragment(root.Get("input")); frag != "" && e.curKind == "tool" {
			e.completionChars += len(frag)
			d := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
			d, _ = sjson.SetBytes(d, "index", e.curIndex)
			d, _ = sjson.SetBytes(d, "delta.partial_json", frag)
			out = append(out, sseEvent("content_block_delta", d))
		}
	case "messageStopEvent":
		out = append(out, e.Finish()...)
	}
	return out
}

// ensureStarted emits message_start exactly once, before any content block.
func (e *KiroClaudeStreamEncoder) ensureStarted(out *[][]byte) {
	if e.started {
		return
	}
	e.started = true
	m := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	m, _ = sjson.SetBytes(m, "message.id", e.msgID)
	m, _ = sjson.SetBytes(m, "message.model", e.model)
	m, _ = sjson.SetBytes(m, "message.usage.input_tokens", e.promptTokens)
	*out = append(*out, sseEvent("message_start", m))
}

// ensureSimpleBlock opens a text/thinking block, closing any other open block first.
func (e *KiroClaudeStreamEncoder) ensureSimpleBlock(kind string, out *[][]byte) {
	if e.curKind == kind {
		return
	}
	e.closeBlock(out)
	e.curIndex = e.nextIndex
	e.nextIndex++
	e.curKind = kind
	s := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	if kind == "thinking" {
		s = []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	}
	s, _ = sjson.SetBytes(s, "index", e.curIndex)
	*out = append(*out, sseEvent("content_block_start", s))
}

// closeBlock emits content_block_stop for the currently open block, if any.
func (e *KiroClaudeStreamEncoder) closeBlock(out *[][]byte) {
	if e.curKind == "" {
		return
	}
	s := []byte(`{"type":"content_block_stop","index":0}`)
	s, _ = sjson.SetBytes(s, "index", e.curIndex)
	*out = append(*out, sseEvent("content_block_stop", s))
	e.curKind = ""
	e.curToolID = ""
}

// sseEvent frames an Anthropic event using the shared SSE writer (event:/data: + \n\n).
func sseEvent(event string, payload []byte) []byte {
	return translatorcommon.AppendSSEEventBytes(nil, event, payload, 2)
}

// AssembleKiroClaudeResponse parses a full Kiro EventStream body directly into a single
// non-streaming Anthropic Messages response (thinking + text + tool_use blocks, stop
// reason, and estimated usage). nameRestore (may be nil) restores shortened tool names.
func AssembleKiroClaudeResponse(model string, full []byte, promptTokens int, nameRestore map[string]string) []byte {
	frames, _ := parseKiroFrames(full)
	restore := func(n string) string {
		if orig, ok := nameRestore[n]; ok {
			return orig
		}
		return n
	}

	var text, reasoning strings.Builder
	type toolAcc struct {
		id   string
		name string
		args strings.Builder
	}
	var tools []*toolAcc
	byID := make(map[string]*toolAcc)

	for i := range frames {
		root := gjson.ParseBytes(frames[i].payload)
		switch frames[i].eventType {
		case "assistantResponseEvent", "codeEvent":
			text.WriteString(root.Get("content").String())
		case "reasoningContentEvent":
			if t := root.Get("text").String(); t != "" {
				reasoning.WriteString(t)
			} else {
				reasoning.WriteString(root.Get("content").String())
			}
		case "toolUseEvent":
			id := root.Get("toolUseId").String()
			acc, ok := byID[id]
			if !ok {
				acc = &toolAcc{id: id, name: restore(root.Get("name").String())}
				byID[id] = acc
				tools = append(tools, acc)
			} else if acc.name == "" {
				acc.name = restore(root.Get("name").String())
			}
			acc.args.WriteString(toolInputFragment(root.Get("input")))
		}
	}

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", "msg_"+uuid.NewString())
	out, _ = sjson.SetBytes(out, "model", model)
	// Emit thinking, then text, then tool_use blocks (mirrors the streaming block order).
	if reasoning.Len() > 0 {
		b := []byte(`{"type":"thinking","thinking":""}`)
		b, _ = sjson.SetBytes(b, "thinking", reasoning.String())
		out, _ = sjson.SetRawBytes(out, "content.-1", b)
	}
	if text.Len() > 0 {
		b := []byte(`{"type":"text","text":""}`)
		b, _ = sjson.SetBytes(b, "text", text.String())
		out, _ = sjson.SetRawBytes(out, "content.-1", b)
	}
	for _, acc := range tools {
		b := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
		b, _ = sjson.SetBytes(b, "id", util.SanitizeClaudeToolID(acc.id))
		b, _ = sjson.SetBytes(b, "name", acc.name)
		if args := acc.args.String(); args != "" && gjson.Valid(args) && gjson.Parse(args).IsObject() {
			b, _ = sjson.SetRawBytes(b, "input", []byte(args))
		}
		out, _ = sjson.SetRawBytes(out, "content.-1", b)
	}
	if len(tools) > 0 {
		out, _ = sjson.SetBytes(out, "stop_reason", "tool_use")
	}

	completionChars := text.Len() + reasoning.Len()
	for _, acc := range tools {
		completionChars += acc.args.Len()
	}
	out, _ = sjson.SetBytes(out, "usage.input_tokens", promptTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", estimateTokensFromChars(completionChars))
	return out
}
