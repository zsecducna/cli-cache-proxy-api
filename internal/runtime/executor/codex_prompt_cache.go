package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexPromptCacheDefaultTTL = time.Hour
	codexPromptCache24hTTL     = 24 * time.Hour
)

type codexPromptCacheSelection struct {
	Body []byte
	Key  string
	TTL  time.Duration
	Hdrs http.Header
}

func prepareCodexPromptCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte, sessionHeaderName string, allowPromptCacheRetention bool, stripPreviousResponseID bool) codexPromptCacheSelection {
	body := bytesClone(rawJSON)
	model := codexPromptCacheModel(body, req.Model)
	body = normalizeCodexPromptCacheRetention(body, model, allowPromptCacheRetention)
	selection := codexPromptCacheSelection{
		Body: body,
		TTL:  codexPromptCacheTTL(body),
		Hdrs: http.Header{},
	}

	if key := firstCodexPromptCacheValue(selection.Body, req.Payload, "prompt_cache_key"); key != "" {
		selection.Key = key
	} else if previousResponseID := firstCodexPromptCacheValue(selection.Body, req.Payload, "previous_response_id"); previousResponseID != "" {
		if key, ok := usage.LookupPromptCacheKeyByResponseID(previousResponseID); ok {
			selection.Key = key
		} else if key, ok := helps.GetCodexResponsePromptCacheKey(previousResponseID); ok {
			selection.Key = key
		}
	}

	if stripPreviousResponseID {
		selection.Body = stripCodexPreviousResponseID(selection.Body)
	}

	if selection.Key == "" && from == "claude" {
		if userID := strings.TrimSpace(gjson.GetBytes(req.Payload, "metadata.user_id").String()); userID != "" {
			key := fmt.Sprintf("%s-%s", req.Model, userID)
			if cache, ok := helps.GetCodexCache(key); ok {
				selection.Key = cache.ID
				selection.TTL = time.Until(cache.Expire)
			} else {
				selection.Key = uuid.New().String()
				helps.SetCodexCache(key, helps.CodexCache{ID: selection.Key, Expire: time.Now().Add(selection.TTL)})
			}
		}
	}

	if selection.Key == "" {
		if hint := codexPromptCacheSessionHint(ctx, selection.Body, req.Payload); hint != "" {
			selection.Key = codexDerivedPromptCacheKey("session", hint)
		} else if prefixHint := codexPromptCacheStablePrefixHint(selection.Body, req.Payload); prefixHint != "" {
			selection.Key = codexDerivedPromptCacheKey("prefix", prefixHint)
		} else if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			selection.Key = codexDerivedPromptCacheKey("api-key", apiKey)
		}
	}

	if selection.Key != "" {
		selection.Body, _ = sjson.SetBytes(selection.Body, "prompt_cache_key", selection.Key)
		if sessionHeaderName != "" {
			selection.Hdrs.Set(sessionHeaderName, selection.Key)
		}
	}
	helps.SetCodexCacheRequestObservability(ctx, selection.Body, selection.Key)

	return selection
}

func publishCodexUsageAndRecordPromptCache(ctx context.Context, reporter *helps.UsageReporter, responsePayload []byte, selection codexPromptCacheSelection) {
	if detail, ok := helps.ParseCodexUsage(responsePayload); ok {
		reporter.Publish(ctx, detail)
	}
	recordCodexPromptCacheResponse(ctx, responsePayload, selection.Key, selection.TTL)
}

func recordCodexPromptCacheResponse(ctx context.Context, responsePayload []byte, promptCacheKey string, ttl time.Duration) {
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	helps.SetCodexCacheResponseObservability(ctx, responsePayload, promptCacheKey)
	if promptCacheKey == "" {
		return
	}
	responseID := strings.TrimSpace(gjson.GetBytes(responsePayload, "response.id").String())
	if responseID == "" {
		return
	}
	helps.SetCodexResponsePromptCacheKey(responseID, promptCacheKey, ttl)
	usage.RememberPromptCacheKeyForResponse(responseID, promptCacheKey, ttl)
}

func codexPromptCacheTTL(rawJSON []byte) time.Duration {
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_retention").String()), "24h") {
		return codexPromptCache24hTTL
	}
	return codexPromptCacheDefaultTTL
}

func codexPromptCacheModel(primary []byte, fallback string) string {
	if model := strings.TrimSpace(gjson.GetBytes(primary, "model").String()); model != "" {
		return model
	}
	return strings.TrimSpace(fallback)
}

func normalizeCodexPromptCacheRetention(rawJSON []byte, model string, allowPromptCacheRetention bool) []byte {
	if len(rawJSON) == 0 {
		return rawJSON
	}
	if !allowPromptCacheRetention {
		updated, err := sjson.DeleteBytes(rawJSON, "prompt_cache_retention")
		if err != nil {
			return rawJSON
		}
		return updated
	}
	retention := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_retention").String())
	if !supportsCodexExtendedPromptCacheRetention(model) {
		if retention == "" || strings.EqualFold(retention, "in_memory") {
			return rawJSON
		}
		updated, err := sjson.DeleteBytes(rawJSON, "prompt_cache_retention")
		if err != nil {
			return rawJSON
		}
		return updated
	}
	if retention == "24h" {
		return rawJSON
	}
	updated, err := sjson.SetBytes(rawJSON, "prompt_cache_retention", "24h")
	if err != nil {
		return rawJSON
	}
	return updated
}

func supportsCodexExtendedPromptCacheRetention(model string) bool {
	return registry.SupportsCodexExtendedPromptCacheRetention(model)
}

func stripCodexPreviousResponseID(rawJSON []byte) []byte {
	if len(rawJSON) == 0 || !gjson.GetBytes(rawJSON, "previous_response_id").Exists() {
		return rawJSON
	}
	updated, err := sjson.DeleteBytes(rawJSON, "previous_response_id")
	if err != nil {
		return rawJSON
	}
	return updated
}

func codexUpstreamSupportsPromptCacheRetention(upstreamURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(upstreamURL))
	return lower != "" && !strings.Contains(lower, "/backend-api/codex/")
}

func codexPromptCacheSessionHint(ctx context.Context, primary, secondary []byte) string {
	for _, path := range []string{
		"conversation",
		"conversation_id",
		"session_id",
		"metadata.conversation",
		"metadata.conversation_id",
		"metadata.session",
		"metadata.session_id",
		"metadata.thread_id",
		"metadata.chat_id",
		"metadata.client_session_id",
		"metadata.workspace_id",
		"metadata.project_id",
		"metadata.user_id",
		"metadata.account_id",
		"metadata.organization_id",
		"metadata.org_id",
		"metadata.repository",
		"metadata.repo",
	} {
		if value := firstCodexPromptCacheValue(primary, secondary, path); value != "" {
			return path + ":" + value
		}
	}

	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		for _, header := range []string{"Conversation_id", "Session_id"} {
			if value := strings.TrimSpace(ginCtx.Request.Header.Get(header)); value != "" {
				return header + ":" + value
			}
		}
	}

	return ""
}

func firstCodexPromptCacheValue(primary, secondary []byte, path string) string {
	if value := strings.TrimSpace(gjson.GetBytes(primary, path).String()); value != "" {
		return value
	}
	return strings.TrimSpace(gjson.GetBytes(secondary, path).String())
}

func codexPromptCacheStablePrefixHint(primary, secondary []byte) string {
	parts := make([]string, 0, 8)
	for _, path := range []string{"instructions", "system", "tools", "response_format", "text.format", "tool_choice"} {
		if raw := firstCodexPromptCacheRaw(primary, secondary, path); raw != "" {
			parts = append(parts, path+":"+raw)
		}
	}
	if raw := leadingRoleMessagesRaw(primary, secondary, "messages"); raw != "" {
		parts = append(parts, "messages:"+raw)
	}
	if raw := leadingRoleMessagesRaw(primary, secondary, "input"); raw != "" {
		parts = append(parts, "input:"+raw)
	}
	return strings.Join(parts, "\n")
}

func firstCodexPromptCacheRaw(primary, secondary []byte, path string) string {
	if raw := codexPromptCacheRaw(primary, path); raw != "" {
		return raw
	}
	return codexPromptCacheRaw(secondary, path)
}

func codexPromptCacheRaw(payload []byte, path string) string {
	if len(payload) == 0 {
		return ""
	}
	result := gjson.GetBytes(payload, path)
	if !result.Exists() {
		return ""
	}
	return strings.TrimSpace(result.Raw)
}

func leadingRoleMessagesRaw(primary, secondary []byte, path string) string {
	if raw := leadingRoleMessagesRawFromPayload(primary, path); raw != "" {
		return raw
	}
	return leadingRoleMessagesRawFromPayload(secondary, path)
}

func leadingRoleMessagesRawFromPayload(payload []byte, path string) string {
	if len(payload) == 0 {
		return ""
	}
	items := gjson.GetBytes(payload, path)
	if !items.Exists() || !items.IsArray() {
		return ""
	}
	blocks := make([]string, 0, 4)
	items.ForEach(func(_, item gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role == "system" || role == "developer" {
			blocks = append(blocks, strings.TrimSpace(item.Raw))
			return true
		}
		return false
	})
	if len(blocks) == 0 {
		return ""
	}
	return "[" + strings.Join(blocks, ",") + "]"
}

func codexDerivedPromptCacheKey(scope, value string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+scope+":"+value)).String()
}

func bytesClone(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	return append([]byte(nil), payload...)
}
