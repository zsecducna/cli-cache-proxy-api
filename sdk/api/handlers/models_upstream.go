package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	chatGPTModelsURL               = "https://chatgpt.com/backend-api/models"
	codexModelsURL                 = "https://chatgpt.com/backend-api/codex/models"
	defaultCodexModelsClientVerion = "0.118.0"
)

var codexClientVersionPattern = regexp.MustCompile(`(?i)\bcodex(?:[_-](?:cli[_-]?rs|tui))?/([0-9]+(?:\.[0-9]+){1,2})`)

type upstreamChatGPTModelsPayload struct {
	Models []struct {
		Slug string `json:"slug"`
	} `json:"models"`
}

var upstreamChatGPTModelAliases = map[string]string{
	"gpt-5-5-thinking": "gpt-5.5",
}

func (h *BaseAPIHandler) FetchCodexModelsUpstream(ctx context.Context, userAgent string) ([]byte, error) {
	clientVersion := codexClientVersion(userAgent)
	reqURL, err := url.Parse(codexModelsURL)
	if err != nil {
		return nil, err
	}
	query := reqURL.Query()
	query.Set("client_version", clientVersion)
	reqURL.RawQuery = query.Encode()
	body, _, err := h.fetchCodexUpstreamJSON(ctx, reqURL.String(), userAgent)
	return body, err
}

func (h *BaseAPIHandler) FetchChatGPTModelsUpstream(ctx context.Context, userAgent string) ([]byte, error) {
	body, _, err := h.fetchCodexUpstreamJSON(ctx, chatGPTModelsURL, userAgent)
	return body, err
}

func (h *BaseAPIHandler) fetchCodexUpstreamJSON(ctx context.Context, rawURL, userAgent string) ([]byte, http.Header, error) {
	auth, executor, err := h.selectCodexModelsAuth()
	if err != nil {
		return nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestBody, responseHeaders, status, err := h.doCodexUpstreamJSONRequest(ctx, executor, auth, rawURL, userAgent)
	if err == nil {
		return requestBody, responseHeaders, nil
	}
	if !shouldRetryCodexUpstreamModelsAfterRefresh(status, requestBody) {
		return nil, responseHeaders, err
	}

	refreshed, errRefresh := executor.Refresh(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, responseHeaders, err
	}
	if refreshed == nil {
		return nil, responseHeaders, err
	}
	if h != nil && h.AuthManager != nil {
		if updated, errUpdate := h.AuthManager.Update(ctx, refreshed); errUpdate == nil && updated != nil {
			refreshed = updated
		}
	}
	requestBody, responseHeaders, status, err = h.doCodexUpstreamJSONRequest(ctx, executor, refreshed, rawURL, userAgent)
	if err != nil {
		return nil, responseHeaders, err
	}
	return requestBody, responseHeaders, nil
}

func (h *BaseAPIHandler) doCodexUpstreamJSONRequest(ctx context.Context, executor coreauth.ProviderExecutor, auth *coreauth.Auth, rawURL, userAgent string) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", strings.TrimSpace(userAgent))
	}

	resp, err := executor.HttpRequest(ctx, auth, req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.Header.Clone(), resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.Header.Clone(), resp.StatusCode, fmt.Errorf("upstream models request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, resp.Header.Clone(), resp.StatusCode, nil
}

func (h *BaseAPIHandler) selectCodexModelsAuth() (*coreauth.Auth, coreauth.ProviderExecutor, error) {
	if h == nil || h.AuthManager == nil {
		return nil, nil, fmt.Errorf("codex models upstream fetch unavailable: auth manager is nil")
	}
	executor, ok := h.AuthManager.Executor("codex")
	if !ok || executor == nil {
		return nil, nil, fmt.Errorf("codex models upstream fetch unavailable: codex executor is not registered")
	}
	auths := h.AuthManager.List()
	now := time.Now()
	sort.SliceStable(auths, func(i, j int) bool {
		if auths[i] == nil || auths[j] == nil {
			return auths[j] != nil
		}
		if auths[i].Status == coreauth.StatusActive && auths[j].Status != coreauth.StatusActive {
			return true
		}
		if auths[j].Status == coreauth.StatusActive && auths[i].Status != coreauth.StatusActive {
			return false
		}
		if !auths[i].UpdatedAt.Equal(auths[j].UpdatedAt) {
			return auths[i].UpdatedAt.After(auths[j].UpdatedAt)
		}
		if !auths[i].CreatedAt.Equal(auths[j].CreatedAt) {
			return auths[i].CreatedAt.After(auths[j].CreatedAt)
		}
		return auths[i].ID < auths[j].ID
	})
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if !coreauth.AuthSelectableForModel(auth, "", now) {
			continue
		}
		return auth, executor, nil
	}
	return nil, nil, fmt.Errorf("codex models upstream fetch unavailable: no active codex auth found")
}

func codexClientVersion(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	if match := codexClientVersionPattern.FindStringSubmatch(userAgent); len(match) == 2 {
		return match[1]
	}
	return defaultCodexModelsClientVerion
}

func shouldRetryCodexUpstreamModelsAfterRefresh(status int, body []byte) bool {
	if status != http.StatusUnauthorized {
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(trimmed, &payload) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Error.Code), "token_expired")
}

func MapUpstreamChatGPTModelsToOpenAIList(body []byte) ([]map[string]any, error) {
	var payload upstreamChatGPTModelsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	models := make([]map[string]any, 0, len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Models))
	for _, model := range payload.Models {
		slug := normalizeUpstreamChatGPTModelSlug(model.Slug)
		if slug == "" {
			continue
		}
		if _, exists := seen[slug]; exists {
			continue
		}
		seen[slug] = struct{}{}
		models = append(models, map[string]any{
			"id":       slug,
			"object":   "model",
			"owned_by": "openai",
		})
	}
	return models, nil
}

func normalizeUpstreamChatGPTModelSlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ""
	}
	if alias, ok := upstreamChatGPTModelAliases[strings.ToLower(slug)]; ok {
		return alias
	}
	return slug
}
