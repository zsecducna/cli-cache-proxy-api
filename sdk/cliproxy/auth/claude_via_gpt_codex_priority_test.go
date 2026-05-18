package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type recordingProviderTestExecutor struct {
	id    string
	calls []string
}

func (e *recordingProviderTestExecutor) Identifier() string { return e.id }
func (e *recordingProviderTestExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls = append(e.calls, auth.Provider+"|"+req.Model)
	return cliproxyexecutor.Response{Payload: []byte(auth.Provider + "|" + req.Model)}, nil
}
func (e *recordingProviderTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Code: "unsupported", Message: "not used"}
}
func (e *recordingProviderTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *recordingProviderTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "unsupported", Message: "not used"}
}
func (e *recordingProviderTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "unsupported", Message: "not used"}
}

func TestManager_ClaudeViaGPTRouteUsesMixedProviderRoundRobin(t *testing.T) {
	m := NewManager(nil, nil, nil)
	codexExec := &recordingProviderTestExecutor{id: "codex"}
	openAICompatExec := &recordingProviderTestExecutor{id: "llmgate"}
	m.RegisterExecutor(codexExec)
	m.RegisterExecutor(openAICompatExec)

	codexAuth := &Auth{ID: "codex-auth", Provider: "codex", Status: StatusActive}
	compatAuth := &Auth{
		ID:       "compat-auth",
		Provider: "llmgate",
		Status:   StatusActive,
		Attributes: map[string]string{
			"compat_name":  "llmgate",
			"provider_key": "llmgate",
		},
	}
	if _, err := m.Register(context.Background(), codexAuth); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}
	if _, err := m.Register(context.Background(), compatAuth); err != nil {
		t.Fatalf("register compat auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(codexAuth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4", Type: "codex"}})
	reg.RegisterClient(compatAuth.ID, "llmgate", []*registry.ModelInfo{{ID: "gpt-5.4", Type: "openai-compatibility"}})
	t.Cleanup(func() {
		reg.UnregisterClient(codexAuth.ID)
		reg.UnregisterClient(compatAuth.ID)
	})

	routeOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"messages":[]}`),
	}

	first, err := m.Execute(context.Background(), []string{"codex", "openai-compatibility"}, req, routeOpts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	second, err := m.Execute(context.Background(), []string{"codex", "openai-compatibility"}, req, routeOpts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if got := string(first.Payload); got != "codex|gpt-5.4" {
		t.Fatalf("first payload = %q, want %q", got, "codex|gpt-5.4")
	}
	if got := string(second.Payload); got != "llmgate|gpt-5.4" {
		t.Fatalf("second payload = %q, want %q", got, "llmgate|gpt-5.4")
	}
	if len(codexExec.calls) != 1 {
		t.Fatalf("codex calls = %d, want %d (%v)", len(codexExec.calls), 1, codexExec.calls)
	}
	if len(openAICompatExec.calls) != 1 {
		t.Fatalf("llmgate calls = %d, want %d (%v)", len(openAICompatExec.calls), 1, openAICompatExec.calls)
	}
}

func TestManager_ClaudeViaGPTRouteIncludesConcreteCompatProviderKey(t *testing.T) {
	m := NewManager(nil, nil, nil)
	codexExec := &recordingProviderTestExecutor{id: "codex"}
	llmgateExec := &recordingProviderTestExecutor{id: "llmgate"}
	m.RegisterExecutor(codexExec)
	m.RegisterExecutor(llmgateExec)

	codexAuth := &Auth{ID: "codex-auth-llmgate-test", Provider: "codex", Status: StatusActive}
	llmgateAuth := &Auth{
		ID:       "llmgate-auth",
		Provider: "llmgate",
		Status:   StatusActive,
		Attributes: map[string]string{
			"compat_name":  "llmgate",
			"provider_key": "llmgate",
		},
	}
	if _, err := m.Register(context.Background(), codexAuth); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}
	if _, err := m.Register(context.Background(), llmgateAuth); err != nil {
		t.Fatalf("register llmgate auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(codexAuth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4", Type: "codex"}})
	reg.RegisterClient(llmgateAuth.ID, "llmgate", []*registry.ModelInfo{{ID: "gpt-5.4", Type: "openai-compatibility"}})
	t.Cleanup(func() {
		reg.UnregisterClient(codexAuth.ID)
		reg.UnregisterClient(llmgateAuth.ID)
	})

	routeOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"messages":[]}`),
	}

	first, err := m.Execute(context.Background(), []string{"codex", "openai-compatibility"}, req, routeOpts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	second, err := m.Execute(context.Background(), []string{"codex", "openai-compatibility"}, req, routeOpts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if got := string(first.Payload); got != "codex|gpt-5.4" {
		t.Fatalf("first payload = %q, want %q", got, "codex|gpt-5.4")
	}
	// Regression target: concrete compat provider keys (for example llmgate)
	// must be eligible when route providers include generic openai-compatibility.
	if got := string(second.Payload); got != "llmgate|gpt-5.4" {
		t.Fatalf("second payload = %q, want %q", got, "llmgate|gpt-5.4")
	}
	if len(codexExec.calls) != 1 {
		t.Fatalf("codex calls = %d, want %d (%v)", len(codexExec.calls), 1, codexExec.calls)
	}
	if len(llmgateExec.calls) != 1 {
		t.Fatalf("llmgate calls = %d, want %d (%v)", len(llmgateExec.calls), 1, llmgateExec.calls)
	}
}
