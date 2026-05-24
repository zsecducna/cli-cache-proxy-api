package handlers

import "testing"

// TestClassifyRequestRoute_CoversPrefixedGPTModels locks the route classifier
// directly so future handler refactors cannot silently drop Claude-via-GPT
// routing for prefixed GPT models.
func TestClassifyRequestRoute_CoversPrefixedGPTModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   string
		model     string
		wantRoute RequestRoute
	}{
		{
			name:      "plain gpt model on claude handler uses Claude-via-GPT route",
			handler:   "claude",
			model:     "gpt-5.4",
			wantRoute: RequestRouteClaudeViaOpenAICompat,
		},
		{
			name:      "prefixed gpt model on claude handler uses Claude-via-GPT route",
			handler:   "claude",
			model:     "codex/gpt-5.4",
			wantRoute: RequestRouteClaudeViaOpenAICompat,
		},
		{
			name:      "trimmed prefixed gpt model on claude handler uses Claude-via-GPT route",
			handler:   " claude ",
			model:     " codex/gpt-5.4 ",
			wantRoute: RequestRouteClaudeViaOpenAICompat,
		},
		{
			name:      "prefixed claude model stays on default route",
			handler:   "claude",
			model:     "teamA/claude-sonnet-4-6",
			wantRoute: RequestRouteDefault,
		},
		{
			name:      "gpt model on non-claude handler stays on default route",
			handler:   "openai",
			model:     "codex/gpt-5.4",
			wantRoute: RequestRouteDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifyRequestRoute(tt.handler, tt.model)
			if got.Route != tt.wantRoute {
				t.Fatalf("route = %q, want %q", got.Route, tt.wantRoute)
			}
		})
	}
}
