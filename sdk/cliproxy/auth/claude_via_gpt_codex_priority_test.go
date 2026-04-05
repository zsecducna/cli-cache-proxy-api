package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type orderedProviderTestExecutor struct {
	id    string
	calls []string
}

func (e *orderedProviderTestExecutor) Identifier() string { return e.id }
func (e *orderedProviderTestExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls = append(e.calls, auth.Provider+"|"+req.Model)
	return cliproxyexecutor.Response{Payload: []byte(auth.Provider + "|" + req.Model)}, nil
}
func (e *orderedProviderTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Code: "unsupported", Message: "not used"}
}
func (e *orderedProviderTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *orderedProviderTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "unsupported", Message: "not used"}
}
func (e *orderedProviderTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "unsupported", Message: "not used"}
}

func TestManager_ClaudeViaGPTRoutePrefersCodexBeforeOpenAICompat(t *testing.T) {
	m := NewManager(nil, nil, nil)
	codexExec := &orderedProviderTestExecutor{id: "codex"}
	openAICompatExec := &orderedProviderTestExecutor{id: "openai-compatibility"}
	m.RegisterExecutor(codexExec)
	m.RegisterExecutor(openAICompatExec)

	codexAuth := &Auth{ID: "codex-auth", Provider: "codex", Status: StatusActive}
	compatAuth := &Auth{
		ID:       "compat-auth",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Attributes: map[string]string{
			"compat_name":  "compat",
			"provider_key": "compat",
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
	reg.RegisterClient(compatAuth.ID, "openai-compatibility", []*registry.ModelInfo{{ID: "gpt-5.4", Type: "openai-compatibility"}})
	t.Cleanup(func() {
		reg.UnregisterClient(codexAuth.ID)
		reg.UnregisterClient(compatAuth.ID)
	})

	resp, err := m.Execute(context.Background(), []string{"codex", "openai-compatibility"}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"messages":[]}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestRouteMetadataKey: "claude_via_openai_compat",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := string(resp.Payload); got != "codex|gpt-5.4" {
		t.Fatalf("payload = %q, want %q", got, "codex|gpt-5.4")
	}
	if len(openAICompatExec.calls) != 0 {
		t.Fatalf("unexpected openai-compatibility execution: %v", openAICompatExec.calls)
	}
}
