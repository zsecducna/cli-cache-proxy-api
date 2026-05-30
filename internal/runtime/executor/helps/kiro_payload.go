package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// This file converts an OpenAI Chat Completions request body into the AWS
// CodeWhisperer "conversationState" request body used by Kiro. It is pure and
// network-free so it can be unit tested in isolation.

// kiroPayload is the top-level CodeWhisperer generate request body.
type kiroPayload struct {
	ConversationState kiroConversationState `json:"conversationState"`
	ProfileArn        string                `json:"profileArn,omitempty"`
	InferenceConfig   *kiroInferenceConfig  `json:"inferenceConfig,omitempty"`
}

// kiroConversationState holds the current message plus alternating history.
type kiroConversationState struct {
	ChatTriggerType string             `json:"chatTriggerType"`
	ConversationID  string             `json:"conversationId"`
	CurrentMessage  kiroCurrentMessage `json:"currentMessage"`
	History         []kiroHistoryEntry `json:"history,omitempty"`
}

// kiroCurrentMessage wraps the trailing user message.
type kiroCurrentMessage struct {
	UserInputMessage kiroUserInputMessage `json:"userInputMessage"`
}

// kiroHistoryEntry is exactly one of userInputMessage / assistantResponseMessage.
type kiroHistoryEntry struct {
	UserInputMessage         *kiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *kiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// kiroUserInputMessage is a user (or remapped system/tool) message.
type kiroUserInputMessage struct {
	Content                 string                       `json:"content"`
	ModelID                 string                       `json:"modelId"`
	Origin                  string                       `json:"origin"`
	Images                  []kiroImage                  `json:"images,omitempty"`
	UserInputMessageContext *kiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// kiroAssistantResponseMessage is a prior assistant turn (text + tool calls).
type kiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []kiroToolUse `json:"toolUses,omitempty"`
}

// kiroUserInputMessageContext carries tool results and tool specs.
type kiroUserInputMessageContext struct {
	ToolResults []kiroToolResult `json:"toolResults,omitempty"`
	Tools       []kiroTool       `json:"tools,omitempty"`
}

// kiroImage is an inline image attachment.
type kiroImage struct {
	Format string          `json:"format"`
	Source kiroImageSource `json:"source"`
}

type kiroImageSource struct {
	Bytes string `json:"bytes"`
}

// kiroToolResult is the result of a previously requested tool call.
type kiroToolResult struct {
	ToolUseID string                  `json:"toolUseId"`
	Status    string                  `json:"status"`
	Content   []kiroToolResultContent `json:"content"`
}

type kiroToolResultContent struct {
	Text string `json:"text"`
}

// kiroToolUse is an assistant-issued tool call.
type kiroToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// kiroTool is a function/tool specification advertised to the model.
type kiroTool struct {
	ToolSpecification kiroToolSpec `json:"toolSpecification"`
}

type kiroToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema kiroInputSchema `json:"inputSchema"`
}

type kiroInputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// kiroInferenceConfig carries optional sampling parameters.
type kiroInferenceConfig struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

// kiroTurn is a normalized, role-collapsed intermediate message.
type kiroTurn struct {
	role        string // "user" or "assistant"
	text        []string
	images      []kiroImage
	toolUses    []kiroToolUse
	toolResults []kiroToolResult
}

// BuildKiroPayload converts an OpenAI Chat Completions body into a Kiro generate body.
//   - upstreamModel is the CodeWhisperer model id (synthetic suffixes already stripped).
//   - profileArn, when non-empty, is included at the top level.
//   - agentic injects the chunked-write system prompt prefix.
//   - thinkingEnabled injects the <thinking_mode> prefix with the given budget.
func BuildKiroPayload(openaiBody []byte, upstreamModel, profileArn string, agentic, thinkingEnabled bool, thinkingBudget int) ([]byte, error) {
	root := gjson.ParseBytes(openaiBody)
	turns := parseOpenAITurns(root.Get("messages"), upstreamModel)
	tools := parseOpenAITools(root.Get("tools"))
	return assembleKiroPayload(turns, tools, upstreamModel, profileArn, agentic, thinkingEnabled, thinkingBudget, buildInferenceConfig(root))
}

// BuildKiroPayloadFromClaude converts an Anthropic Messages request body directly into a
// Kiro generate body. Unlike BuildKiroPayload (which receives an OpenAI body), this path
// preserves tool declarations, assistant tool_use calls, tool_result outputs, images, and
// system text that the lossy chat-safe Claude->OpenAI translation would otherwise drop.
func BuildKiroPayloadFromClaude(claudeBody []byte, upstreamModel, profileArn string, agentic, thinkingEnabled bool, thinkingBudget int) ([]byte, error) {
	root := gjson.ParseBytes(claudeBody)
	turns := parseClaudeTurns(root)
	tools := parseClaudeTools(root.Get("tools"))
	return assembleKiroPayload(turns, tools, upstreamModel, profileArn, agentic, thinkingEnabled, thinkingBudget, buildInferenceConfig(root))
}

// assembleKiroPayload turns normalized turns + tool specs into the final CodeWhisperer
// conversationState body shared by the OpenAI and Anthropic request builders.
func assembleKiroPayload(turns []kiroTurn, tools []kiroTool, upstreamModel, profileArn string, agentic, thinkingEnabled bool, thinkingBudget int, infer *kiroInferenceConfig) ([]byte, error) {
	// 1. Collapse consecutive same-role turns to satisfy Kiro's strict alternation.
	turns = collapseTurns(turns)

	// 2. The trailing user turn becomes the current message; the rest is history.
	var current kiroTurn
	history := turns
	if n := len(turns); n > 0 && turns[n-1].role == "user" {
		current = turns[n-1]
		history = turns[:n-1]
	} else {
		current = kiroTurn{role: "user"}
	}

	// 3. Assemble the current user message with prefixes prepended in fixed order.
	currentMsg := kiroUserInputMessage{
		Content: buildCurrentContent(current, agentic, thinkingEnabled, thinkingBudget),
		ModelID: upstreamModel,
		Origin:  "AI_EDITOR",
		Images:  current.images,
	}
	ctx := &kiroUserInputMessageContext{ToolResults: current.toolResults, Tools: tools}
	if len(ctx.ToolResults) > 0 || len(ctx.Tools) > 0 {
		currentMsg.UserInputMessageContext = ctx
	}

	payload := kiroPayload{
		ConversationState: kiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.NewString(),
			CurrentMessage:  kiroCurrentMessage{UserInputMessage: currentMsg},
			History:         buildHistory(history, upstreamModel),
		},
		ProfileArn:      strings.TrimSpace(profileArn),
		InferenceConfig: infer,
	}

	return json.Marshal(payload)
}

// parseClaudeTurns converts an Anthropic Messages request (top-level system + messages)
// into normalized turns, preserving text, images, assistant tool_use calls, and user
// tool_result outputs.
func parseClaudeTurns(root gjson.Result) []kiroTurn {
	var turns []kiroTurn
	// Anthropic carries the system prompt at the top level; Kiro has no system role, so
	// fold it into a leading user turn (Claude Code's attribution block is stripped).
	if sys := strings.Join(extractClaudeSystemText(root.Get("system")), "\n\n"); strings.TrimSpace(sys) != "" {
		turns = append(turns, kiroTurn{role: "user", text: []string{sys}})
	}
	messages := root.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return turns
	}
	for _, msg := range messages.Array() {
		turn := kiroTurn{role: "user"}
		if strings.TrimSpace(msg.Get("role").String()) == "assistant" {
			turn.role = "assistant"
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			if t := content.String(); t != "" {
				turn.text = append(turn.text, t)
			}
			turns = append(turns, turn)
			continue
		}
		content.ForEach(func(_, part gjson.Result) bool {
			switch strings.TrimSpace(part.Get("type").String()) {
			case "text":
				if t := part.Get("text").String(); t != "" {
					turn.text = append(turn.text, t)
				}
			case "image":
				if img, ok := parseClaudeImage(part.Get("source")); ok {
					turn.images = append(turn.images, img)
				}
			case "tool_use":
				turn.toolUses = append(turn.toolUses, kiroToolUse{
					ToolUseID: part.Get("id").String(),
					Name:      shortenKiroToolName(part.Get("name").String()),
					Input:     rawObjectOrEmpty(part.Get("input")),
				})
			case "tool_result":
				status := "success"
				if part.Get("is_error").Bool() {
					status = "error"
				}
				turn.toolResults = append(turn.toolResults, kiroToolResult{
					ToolUseID: part.Get("tool_use_id").String(),
					Status:    status,
					Content:   []kiroToolResultContent{{Text: collectClaudeToolResultText(part.Get("content"))}},
				})
			}
			return true
		})
		turns = append(turns, turn)
	}
	return turns
}

// extractClaudeSystemText returns the system prompt text parts (string or block array),
// dropping Claude Code's synthetic billing-attribution block.
func extractClaudeSystemText(system gjson.Result) []string {
	if !system.Exists() {
		return nil
	}
	var parts []string
	if system.Type == gjson.String {
		parts = append(parts, system.String())
	} else if system.IsArray() {
		system.ForEach(func(_, p gjson.Result) bool {
			if p.Get("type").String() == "text" {
				if t := p.Get("text").String(); t != "" {
					parts = append(parts, t)
				}
			}
			return true
		})
	}
	filtered := make([]string, 0, len(parts))
	for _, t := range parts {
		if util.IsClaudeCodeAttributionSystemText(t) {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// parseClaudeImage converts an Anthropic base64 image source into a Kiro image.
func parseClaudeImage(source gjson.Result) (kiroImage, bool) {
	data := strings.TrimSpace(source.Get("data").String())
	if data == "" {
		return kiroImage{}, false
	}
	format := "png"
	if mt := source.Get("media_type").String(); mt != "" {
		if slash := strings.LastIndex(mt, "/"); slash != -1 && slash+1 < len(mt) {
			format = mt[slash+1:]
		}
	}
	return kiroImage{Format: format, Source: kiroImageSource{Bytes: data}}, true
}

// rawObjectOrEmpty returns the raw JSON of an object input, defaulting to "{}".
func rawObjectOrEmpty(input gjson.Result) json.RawMessage {
	if input.Exists() && input.IsObject() {
		return json.RawMessage(input.Raw)
	}
	return json.RawMessage(`{}`)
}

// collectClaudeToolResultText flattens an Anthropic tool_result content (string, block
// array, or object) into a single text string.
func collectClaudeToolResultText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return content.Raw
	}
	var parts []string
	content.ForEach(func(_, part gjson.Result) bool {
		switch {
		case part.Type == gjson.String:
			parts = append(parts, part.String())
		case part.Get("type").String() == "text":
			parts = append(parts, part.Get("text").String())
		default:
			parts = append(parts, part.Raw)
		}
		return true
	})
	return strings.Join(parts, "\n\n")
}

// kiroMaxToolNameLen is CodeWhisperer's hard limit on tool names; longer names are
// rejected upstream with a misleading "Invalid tool use format" error.
const kiroMaxToolNameLen = 64

// shortenKiroToolName deterministically shortens a tool name that exceeds Kiro's 64-char
// limit to "<prefix>_<12-hex-sha256>" (exactly 64 chars). Deterministic so the shortened
// name is identical across tool declarations and assistant tool_use history within a
// request, and reversible via KiroToolNameRestoreMap for the response.
func shortenKiroToolName(name string) string {
	if len(name) <= kiroMaxToolNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:])[:12]
	return name[:kiroMaxToolNameLen-len(hash)-1] + "_" + hash
}

// KiroToolNameRestoreMap returns a shortened->original map for every declared tool whose
// name was shortened, so the executor can restore the original name on tool calls in the
// response. isClaude selects the Anthropic (tools[].name) vs OpenAI (tools[].function.name)
// declaration shape.
func KiroToolNameRestoreMap(body []byte, isClaude bool) map[string]string {
	m := make(map[string]string)
	gjson.GetBytes(body, "tools").ForEach(func(_, t gjson.Result) bool {
		name := t.Get("name").String()
		if !isClaude {
			name = t.Get("function.name").String()
		}
		if name == "" {
			return true
		}
		if short := shortenKiroToolName(name); short != name {
			m[short] = name
		}
		return true
	})
	return m
}

// EstimateClaudePromptTokens approximates prompt tokens directly from an Anthropic
// Messages body by summing system + message text/tool content lengths (~4 chars per
// token). Used because CodeWhisperer returns no token usage, and so the Claude path needs
// no OpenAI body at all.
func EstimateClaudePromptTokens(claudeBody []byte) int {
	root := gjson.ParseBytes(claudeBody)
	chars := len(strings.Join(extractClaudeSystemText(root.Get("system")), "\n"))
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.Type == gjson.String {
			chars += len(content.String())
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			switch part.Get("type").String() {
			case "text":
				chars += len(part.Get("text").String())
			case "tool_use":
				chars += len(part.Get("input").Raw)
			case "tool_result":
				chars += len(collectClaudeToolResultText(part.Get("content")))
			}
			return true
		})
		return true
	})
	return estimateTokensFromChars(chars)
}

// parseClaudeTools converts Anthropic tool declarations into Kiro tool specs.
func parseClaudeTools(tools gjson.Result) []kiroTool {
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}
	var specs []kiroTool
	for _, t := range tools.Array() {
		name := t.Get("name").String()
		if name == "" {
			continue
		}
		specs = append(specs, kiroTool{ToolSpecification: kiroToolSpec{
			Name:        shortenKiroToolName(name),
			Description: t.Get("description").String(),
			InputSchema: kiroInputSchema{JSON: normalizeToolSchema(t.Get("input_schema"))},
		}})
	}
	return specs
}

// buildCurrentContent prepends thinking/context/agentic prefixes to the user text.
func buildCurrentContent(turn kiroTurn, agentic, thinkingEnabled bool, thinkingBudget int) string {
	parts := make([]string, 0, 4)
	if thinkingEnabled {
		parts = append(parts, kiroauth.BuildThinkingPrefix(thinkingBudget))
	}
	parts = append(parts, kiroauth.BuildContextPrefix(time.Now()))
	if agentic {
		parts = append(parts, kiroauth.AgenticSystemPrompt)
	}
	if userText := strings.Join(turn.text, "\n\n"); strings.TrimSpace(userText) != "" {
		parts = append(parts, userText)
	}
	return strings.Join(parts, "\n\n")
}

// parseOpenAITurns converts OpenAI messages into normalized turns (system/tool -> user).
func parseOpenAITurns(messages gjson.Result, _ string) []kiroTurn {
	if !messages.Exists() || !messages.IsArray() {
		return nil
	}
	var turns []kiroTurn
	for _, msg := range messages.Array() {
		role := strings.TrimSpace(msg.Get("role").String())
		turn := kiroTurn{}
		switch role {
		case "assistant":
			turn.role = "assistant"
			if text, _ := extractContentAndImages(msg.Get("content")); text != "" {
				turn.text = append(turn.text, text)
			}
			turn.toolUses = parseAssistantToolUses(msg.Get("tool_calls"))
		case "tool":
			// Tool results are remapped onto a user turn's context.
			turn.role = "user"
			turn.toolResults = []kiroToolResult{parseToolResult(msg)}
		default: // user, system, or unknown -> user
			turn.role = "user"
			text, images := extractContentAndImages(msg.Get("content"))
			if text != "" {
				turn.text = append(turn.text, text)
			}
			turn.images = images
		}
		turns = append(turns, turn)
	}
	return turns
}

// collapseTurns merges consecutive turns sharing the same role to satisfy Kiro's
// strict user/assistant alternation requirement.
func collapseTurns(turns []kiroTurn) []kiroTurn {
	if len(turns) == 0 {
		return nil
	}
	merged := make([]kiroTurn, 0, len(turns))
	for _, turn := range turns {
		if n := len(merged); n > 0 && merged[n-1].role == turn.role {
			merged[n-1].text = append(merged[n-1].text, turn.text...)
			merged[n-1].images = append(merged[n-1].images, turn.images...)
			merged[n-1].toolUses = append(merged[n-1].toolUses, turn.toolUses...)
			merged[n-1].toolResults = append(merged[n-1].toolResults, turn.toolResults...)
			continue
		}
		merged = append(merged, turn)
	}
	return merged
}

// buildHistory renders prior turns into alternating history entries.
func buildHistory(turns []kiroTurn, modelID string) []kiroHistoryEntry {
	if len(turns) == 0 {
		return nil
	}
	entries := make([]kiroHistoryEntry, 0, len(turns))
	for _, turn := range turns {
		if turn.role == "assistant" {
			entries = append(entries, kiroHistoryEntry{AssistantResponseMessage: &kiroAssistantResponseMessage{
				Content:  strings.Join(turn.text, "\n\n"),
				ToolUses: turn.toolUses,
			}})
			continue
		}
		msg := &kiroUserInputMessage{
			Content: strings.Join(turn.text, "\n\n"),
			ModelID: modelID,
			Origin:  "AI_EDITOR",
			Images:  turn.images,
		}
		if len(turn.toolResults) > 0 {
			msg.UserInputMessageContext = &kiroUserInputMessageContext{ToolResults: turn.toolResults}
		}
		entries = append(entries, kiroHistoryEntry{UserInputMessage: msg})
	}
	return entries
}

// extractContentAndImages pulls text and inline images from an OpenAI content node,
// which may be a plain string or an array of typed parts.
func extractContentAndImages(content gjson.Result) (string, []kiroImage) {
	if !content.Exists() {
		return "", nil
	}
	if content.Type == gjson.String {
		return content.String(), nil
	}
	if !content.IsArray() {
		return "", nil
	}
	var texts []string
	var images []kiroImage
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "text":
			if t := part.Get("text").String(); t != "" {
				texts = append(texts, t)
			}
		case "image_url":
			if img, ok := parseImageURL(part.Get("image_url.url").String()); ok {
				images = append(images, img)
			}
		}
	}
	return strings.Join(texts, "\n"), images
}

// parseImageURL converts a data URI (or bare base64) into a Kiro image, stripping the
// "data:...;base64," prefix when present.
func parseImageURL(url string) (kiroImage, bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return kiroImage{}, false
	}
	format := "png"
	data := url
	if strings.HasPrefix(url, "data:") {
		if idx := strings.Index(url, ";base64,"); idx != -1 {
			mediaType := url[len("data:"):idx]
			if slash := strings.LastIndex(mediaType, "/"); slash != -1 && slash+1 < len(mediaType) {
				format = mediaType[slash+1:]
			}
			data = url[idx+len(";base64,"):]
		} else {
			return kiroImage{}, false
		}
	}
	if data == "" {
		return kiroImage{}, false
	}
	return kiroImage{Format: format, Source: kiroImageSource{Bytes: data}}, true
}

// parseAssistantToolUses converts OpenAI assistant tool_calls into Kiro toolUses.
func parseAssistantToolUses(toolCalls gjson.Result) []kiroToolUse {
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return nil
	}
	var uses []kiroToolUse
	for _, tc := range toolCalls.Array() {
		name := tc.Get("function.name").String()
		if name == "" {
			continue
		}
		uses = append(uses, kiroToolUse{
			ToolUseID: tc.Get("id").String(),
			Name:      shortenKiroToolName(name),
			Input:     argumentsToObject(tc.Get("function.arguments").String()),
		})
	}
	return uses
}

// parseToolResult converts an OpenAI tool message into a Kiro tool result.
func parseToolResult(msg gjson.Result) kiroToolResult {
	toolUseID := strings.TrimSpace(msg.Get("tool_call_id").String())
	if toolUseID == "" {
		toolUseID = strings.TrimSpace(msg.Get("call_id").String())
	}
	text, _ := extractContentAndImages(msg.Get("content"))
	return kiroToolResult{
		ToolUseID: toolUseID,
		Status:    "success",
		Content:   []kiroToolResultContent{{Text: text}},
	}
}

// parseOpenAITools converts OpenAI function tool definitions into Kiro tool specs.
func parseOpenAITools(tools gjson.Result) []kiroTool {
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}
	var specs []kiroTool
	for _, t := range tools.Array() {
		fn := t.Get("function")
		if !fn.Exists() {
			continue
		}
		name := fn.Get("name").String()
		if name == "" {
			continue
		}
		specs = append(specs, kiroTool{ToolSpecification: kiroToolSpec{
			Name:        shortenKiroToolName(name),
			Description: fn.Get("description").String(),
			InputSchema: kiroInputSchema{JSON: normalizeToolSchema(fn.Get("parameters"))},
		}})
	}
	return specs
}

// normalizeToolSchema ensures the JSON schema is a well-formed object schema with
// "type", "properties", and "required" always present (Kiro rejects partial schemas).
func normalizeToolSchema(parameters gjson.Result) json.RawMessage {
	base := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
	if parameters.Exists() && parameters.IsObject() {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(parameters.Raw), &parsed); err == nil {
			if _, ok := parsed["type"]; !ok {
				parsed["type"] = "object"
			}
			if _, ok := parsed["properties"]; !ok {
				parsed["properties"] = map[string]any{}
			}
			if _, ok := parsed["required"]; !ok {
				parsed["required"] = []any{}
			}
			base = parsed
		}
	}
	out, err := json.Marshal(base)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
	}
	return out
}

// argumentsToObject parses a tool-call argument string into a JSON object, defaulting
// to "{}" when the string is empty or not valid JSON.
func argumentsToObject(args string) json.RawMessage {
	args = strings.TrimSpace(args)
	if args == "" || !gjson.Valid(args) {
		return json.RawMessage(`{}`)
	}
	if !gjson.Parse(args).IsObject() {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(args)
}

// buildInferenceConfig maps OpenAI sampling parameters to Kiro's inferenceConfig.
func buildInferenceConfig(root gjson.Result) *kiroInferenceConfig {
	cfg := &kiroInferenceConfig{MaxTokens: 32000}
	if mt := root.Get("max_tokens"); mt.Exists() && mt.Int() > 0 {
		cfg.MaxTokens = int(mt.Int())
	} else if mt = root.Get("max_completion_tokens"); mt.Exists() && mt.Int() > 0 {
		cfg.MaxTokens = int(mt.Int())
	}
	if t := root.Get("temperature"); t.Exists() && t.Type == gjson.Number {
		v := t.Float()
		cfg.Temperature = &v
	}
	if p := root.Get("top_p"); p.Exists() && p.Type == gjson.Number {
		v := p.Float()
		cfg.TopP = &v
	}
	return cfg
}
