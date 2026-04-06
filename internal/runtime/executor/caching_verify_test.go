package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureCacheControl(t *testing.T) {
	large := cacheEligibleAnthropicText()

	// Test case 1: System prompt as string
	t.Run("String System Prompt", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{"model": "claude-3-5-sonnet", "system": %q, "messages": []}`, large))
		output := ensureCacheControl(input)

		res := gjson.GetBytes(output, "system.0.cache_control.type")
		if res.String() != "ephemeral" {
			t.Errorf("cache_control not found in system string. Output: %s", string(output))
		}
	})

	// Test case 2: System prompt as array
	t.Run("Array System Prompt", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{"model": "claude-3-5-sonnet", "system": [{"type": "text", "text": %q}, {"type": "text", "text": %q}], "messages": []}`,
			large,
			large,
		))
		output := ensureCacheControl(input)

		res0 := gjson.GetBytes(output, "system.0.cache_control")
		res1 := gjson.GetBytes(output, "system.1.cache_control.type")

		if res0.Exists() {
			t.Errorf("cache_control should NOT be on the first element")
		}
		if res1.String() != "ephemeral" {
			t.Errorf("cache_control not found on last system element. Output: %s", string(output))
		}
	})

	// Test case 3: Tools are cached
	t.Run("Tools Caching", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": %q, "input_schema": {"type": "object"}},
				{"name": "tool2", "description": %q, "input_schema": {"type": "object"}}
			],
			"system": %q,
			"messages": []
		}`,
			large,
			large,
			large,
		))
		output := ensureCacheControl(input)

		tool0Cache := gjson.GetBytes(output, "tools.0.cache_control")
		tool1Cache := gjson.GetBytes(output, "tools.1.cache_control.type")

		if tool0Cache.Exists() {
			t.Errorf("cache_control should NOT be on the first tool")
		}
		if tool1Cache.String() != "ephemeral" {
			t.Errorf("cache_control not found on last tool. Output: %s", string(output))
		}

		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("cache_control not found in system. Output: %s", string(output))
		}
	})

	// Test case 4: Tools and system are INDEPENDENT breakpoints
	t.Run("Independent Cache Breakpoints", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": %q, "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
			],
			"system": [{"type": "text", "text": %q}],
			"messages": []
		}`,
			large,
			large,
		))
		output := ensureCacheControl(input)

		tool0Cache := gjson.GetBytes(output, "tools.0.cache_control.type")
		if tool0Cache.String() != "ephemeral" {
			t.Errorf("existing cache_control was incorrectly removed")
		}

		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("system should have its own cache_control breakpoint (independent of tools)")
		}
	})

	// Test case 5: Only tools, no system
	t.Run("Only Tools No System", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"tools": [
				{"name": "tool1", "description": %q, "input_schema": {"type": "object"}}
			],
			"messages": [{"role": "user", "content": %q}]
		}`,
			large,
			large,
		))
		output := ensureCacheControl(input)

		toolCache := gjson.GetBytes(output, "tools.0.cache_control.type")
		if toolCache.String() != "ephemeral" {
			t.Errorf("cache_control not found on tool. Output: %s", string(output))
		}
	})

	// Test case 6: Many tools (Claude Code scenario)
	t.Run("Many Tools (Claude Code Scenario)", func(t *testing.T) {
		var toolsBuilder strings.Builder
		toolsBuilder.WriteByte('[')
		for i := range 50 {
			if i > 0 {
				toolsBuilder.WriteByte(',')
			}
			toolsBuilder.WriteString(fmt.Sprintf(`{"name": "tool%d", "description": %q, "input_schema": {"type": "object"}}`, i, large))
		}
		toolsBuilder.WriteByte(']')

		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"tools": %s,
			"system": [{"type": "text", "text": %q}],
			"messages": [{"role": "user", "content": "Hello"}]
		}`,
			toolsBuilder.String(),
			large,
		))

		output := ensureCacheControl(input)

		for i := range 49 {
			path := fmt.Sprintf("tools.%d.cache_control", i)
			if gjson.GetBytes(output, path).Exists() {
				t.Errorf("tool %d should NOT have cache_control", i)
			}
		}

		lastToolCache := gjson.GetBytes(output, "tools.49.cache_control.type")
		if lastToolCache.String() != "ephemeral" {
			t.Errorf("last tool (49) should have cache_control")
		}

		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("system should have cache_control")
		}
	})

	// Test case 7: Empty tools array
	t.Run("Empty Tools Array", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{"model": "claude-3-5-sonnet", "tools": [], "system": %q, "messages": []}`,
			large,
		))
		output := ensureCacheControl(input)

		systemCache := gjson.GetBytes(output, "system.0.cache_control.type")
		if systemCache.String() != "ephemeral" {
			t.Errorf("system should have cache_control even with empty tools array")
		}
	})

	// Test case 8: Messages caching for multi-turn (second-to-last user)
	t.Run("Messages Caching Second-To-Last User", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": %q},
				{"role": "assistant", "content": "Assistant reply"},
				{"role": "user", "content": %q},
				{"role": "assistant", "content": "Assistant reply 2"},
				{"role": "user", "content": "Third user"}
			]
		}`,
			large,
			large,
		))
		output := ensureCacheControl(input)

		cacheType := gjson.GetBytes(output, "messages.2.content.0.cache_control.type")
		if cacheType.String() != "ephemeral" {
			t.Errorf("cache_control not found on second-to-last user turn. Output: %s", string(output))
		}

		lastUserCache := gjson.GetBytes(output, "messages.4.content.0.cache_control")
		if lastUserCache.Exists() {
			t.Errorf("last user turn should NOT have cache_control")
		}
	})

	// Test case 9: Existing message cache_control should be rewritten to the policy breakpoint
	t.Run("Messages Rewrite Existing Cache Control", func(t *testing.T) {
		input := []byte(fmt.Sprintf(`{
			"model": "claude-3-5-sonnet",
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": %q}]},
				{"role": "assistant", "content": [{"type": "text", "text": "Assistant reply", "cache_control": {"type": "ephemeral"}}]},
				{"role": "user", "content": [{"type": "text", "text": %q}]}
			]
		}`,
			large,
			large,
		))
		output := ensureCacheControl(input)

		userCache := gjson.GetBytes(output, "messages.0.content.0.cache_control")
		if !userCache.Exists() {
			t.Errorf("cache_control should be rewritten onto the second-to-last user turn")
		}
		if userCache.Get("type").String() != "ephemeral" {
			t.Errorf("messages policy cache_control.type = %q, want ephemeral", userCache.Get("type").String())
		}
		if userCache.Get("ttl").String() != "5m" {
			t.Errorf("messages policy cache_control.ttl = %q, want 5m", userCache.Get("ttl").String())
		}

		existingCache := gjson.GetBytes(output, "messages.1.content.0.cache_control")
		if existingCache.Exists() {
			t.Errorf("existing cache_control should be removed. Output: %s", string(output))
		}
	})
}

func TestCacheControlOrder(t *testing.T) {
	large := cacheEligibleAnthropicText()
	input := []byte(fmt.Sprintf(`{
		"model": "claude-sonnet-4",
		"tools": [
			{"name": "Read", "description": %q, "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}},
			{"name": "Write", "description": %q, "input_schema": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}}}
		],
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": %q}
		],
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`,
		large,
		large,
		large,
	))

	output := ensureCacheControl(input)

	if gjson.GetBytes(output, "tools.1.cache_control.type").String() != "ephemeral" {
		t.Error("last tool should have cache_control")
	}
	if gjson.GetBytes(output, "tools.0.cache_control").Exists() {
		t.Error("first tool should NOT have cache_control")
	}
	if gjson.GetBytes(output, "system.1.cache_control.type").String() != "ephemeral" {
		t.Error("last system element should have cache_control")
	}
	if gjson.GetBytes(output, "system.0.cache_control").Exists() {
		t.Error("first system element should NOT have cache_control")
	}
}
