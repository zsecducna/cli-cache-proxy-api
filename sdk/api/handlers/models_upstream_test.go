package handlers

import "testing"

func TestMapUpstreamChatGPTModelsToOpenAIList_MapsGPT55ThinkingToGPT55(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5-5-thinking"},{"slug":"gpt-5.4"}]}`)

	models, err := MapUpstreamChatGPTModelsToOpenAIList(body)
	if err != nil {
		t.Fatalf("MapUpstreamChatGPTModelsToOpenAIList() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if got := models[0]["id"]; got != "gpt-5.5" {
		t.Fatalf("models[0].id = %v, want %q", got, "gpt-5.5")
	}
	if got := models[1]["id"]; got != "gpt-5.4" {
		t.Fatalf("models[1].id = %v, want %q", got, "gpt-5.4")
	}
}

func TestMapUpstreamChatGPTModelsToOpenAIList_DeDuplicatesMappedGPT55(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-5-5-thinking"},{"slug":"gpt-5.5"}]}`)

	models, err := MapUpstreamChatGPTModelsToOpenAIList(body)
	if err != nil {
		t.Fatalf("MapUpstreamChatGPTModelsToOpenAIList() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if got := models[0]["id"]; got != "gpt-5.5" {
		t.Fatalf("models[0].id = %v, want %q", got, "gpt-5.5")
	}
}
