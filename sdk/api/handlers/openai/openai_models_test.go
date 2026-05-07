package openai

import "testing"

func TestMergeOpenAIModelListsAddsLocalClaudeModels(t *testing.T) {
	upstream := []map[string]any{
		{"id": "gpt-5.5", "object": "model", "owned_by": "openai"},
	}
	local := []map[string]any{
		{"id": "gpt-5.5", "object": "model", "owned_by": "openai"},
		{"id": "claude-sonnet-4-6", "object": "model", "owned_by": "anthropic"},
	}

	merged := mergeOpenAIModelLists(upstream, local)

	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	if got := merged[0]["id"]; got != "gpt-5.5" {
		t.Fatalf("merged[0].id = %v, want gpt-5.5", got)
	}
	if got := merged[1]["id"]; got != "claude-sonnet-4-6" {
		t.Fatalf("merged[1].id = %v, want claude-sonnet-4-6", got)
	}
}
