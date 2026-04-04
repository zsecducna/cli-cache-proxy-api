package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
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

func prepareCodexPromptCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte, sessionHeaderName string) codexPromptCacheSelection {
	selection := codexPromptCacheSelection{
		Body: bytesClone(rawJSON),
		TTL:  codexPromptCacheTTL(rawJSON),
		Hdrs: http.Header{},
	}

	if key := firstCodexPromptCacheValue(selection.Body, req.Payload, "prompt_cache_key"); key != "" {
		selection.Key = key
	} else if previousResponseID := firstCodexPromptCacheValue(selection.Body, req.Payload, "previous_response_id"); previousResponseID != "" {
		if key, ok := helps.GetCodexResponsePromptCacheKey(previousResponseID); ok {
			selection.Key = key
		}
	}

	if selection.Key == "" {
		switch from {
		case "claude":
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
		default:
			if hint := codexPromptCacheSessionHint(ctx, selection.Body, req.Payload); hint != "" {
				selection.Key = codexDerivedPromptCacheKey("session", hint)
			} else if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
				selection.Key = codexDerivedPromptCacheKey("api-key", apiKey)
			}
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
}

func codexPromptCacheTTL(rawJSON []byte) time.Duration {
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_retention").String()), "24h") {
		return codexPromptCache24hTTL
	}
	return codexPromptCacheDefaultTTL
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

func codexDerivedPromptCacheKey(scope, value string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+scope+":"+value)).String()
}

func bytesClone(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	return append([]byte(nil), payload...)
}
