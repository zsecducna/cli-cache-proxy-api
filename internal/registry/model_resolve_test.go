package registry

import "testing"

// TestResolveModelID verifies separator-insensitive (dash<->dot) resolution while keeping
// exact registrations authoritative, so standard Claude Code ids like "claude-opus-4-8"
// route to a registered dotted id like "claude-opus-4.8".
func TestResolveModelID(t *testing.T) {
	r := newTestModelRegistry()
	// Kiro advertises dotted minor versions; register them as a live client would.
	r.RegisterClient("kiro-1", "kiro", []*ModelInfo{
		{ID: "claude-opus-4.8"},
		{ID: "claude-sonnet-4.6"},
	})
	// A verbatim dash-form id registered directly must still resolve to itself.
	r.RegisterClient("claude-1", "claude", []*ModelInfo{
		{ID: "claude-opus-4-6"},
	})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"dash request resolves to dotted registration", "claude-opus-4-8", "claude-opus-4.8"},
		{"dotted request exact match", "claude-opus-4.8", "claude-opus-4.8"},
		{"dash sonnet resolves to dotted", "claude-sonnet-4-6", "claude-sonnet-4.6"},
		{"exact dash-form registration preserved", "claude-opus-4-6", "claude-opus-4-6"},
		{"unknown model returns empty", "gpt-5.4", ""},
		{"empty input returns empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.ResolveModelID(tt.input)
			if got != tt.expected {
				t.Errorf("ResolveModelID(%q) = %q, expected %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestResolveModelIDIgnoresUnavailableProviders ensures a registration with no active
// provider does not satisfy a normalized lookup (mirrors GetModelProviders semantics).
func TestResolveModelIDIgnoresUnavailableProviders(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("kiro-1", "kiro", []*ModelInfo{{ID: "claude-opus-4.8"}})
	// Drop the only provider so the model is registered but unavailable.
	r.UnregisterClient("kiro-1")

	if got := r.ResolveModelID("claude-opus-4-8"); got != "" {
		t.Errorf("ResolveModelID with no available provider = %q, expected empty", got)
	}
}
