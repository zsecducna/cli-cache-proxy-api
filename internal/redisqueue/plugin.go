package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var registerUsagePluginOnce sync.Once

func init() {
	RegisterUsagePlugin()
}

// RegisterUsagePlugin attaches the Redis queue usage sink to the global usage
// manager. init calls this for compatibility; server startup also calls it to
// make the side-effect dependency explicit.
func RegisterUsagePlugin() {
	registerUsagePluginOnce.Do(registerUsagePlugin)
}

func registerUsagePlugin() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !UsageStatisticsEnabled() {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	aliasName := strings.TrimSpace(record.Alias)
	if aliasName == "" {
		aliasName = modelName
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}
	apiKey := strings.TrimSpace(record.APIKey)
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))
	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}

	tokens := tokenStats{
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheReadTokens:     record.Detail.CacheReadTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	fail := resolveFail(ctx, record, failed)

	detail := internalusage.RequestDetail{
		Timestamp:     timestamp,
		CustomerID:    strings.TrimSpace(record.CustomerID),
		CustomerEmail: strings.TrimSpace(record.CustomerEmail),
		LatencyMs:     record.Latency.Milliseconds(),
		Source:        record.Source,
		AuthIndex:     record.AuthIndex,
		Tokens: internalusage.TokenStats{
			InputTokens:     tokens.InputTokens,
			OutputTokens:    tokens.OutputTokens,
			ReasoningTokens: tokens.ReasoningTokens,
			CachedTokens:    tokens.CachedTokens,
			TotalTokens:     tokens.TotalTokens,
		},
		Failed: failed,
	}

	payload, err := json.Marshal(queuedUsageDetail{
		RequestDetail:   detail,
		Provider:        provider,
		Model:           modelName,
		Alias:           aliasName,
		ReasoningEffort: reasoningEffort,
		Endpoint:        resolveEndpoint(ctx),
		AuthType:        authType,
		APIKey:          apiKey,
		RequestID:       requestID,
		Fail:            fail,
		ResponseHeaders: record.ResponseHeaders,
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type queuedUsageDetail struct {
	internalusage.RequestDetail
	Provider        string      `json:"provider"`
	Model           string      `json:"model"`
	Alias           string      `json:"alias"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	Endpoint        string      `json:"endpoint"`
	AuthType        string      `json:"auth_type"`
	APIKey          string      `json:"api_key"`
	RequestID       string      `json:"request_id"`
	Fail            failDetail  `json:"fail"`
	ResponseHeaders http.Header `json:"response_headers,omitempty"`
}

type tokenStats struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type failDetail struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func resolveFail(ctx context.Context, record coreusage.Record, failed bool) failDetail {
	fail := failDetail{
		StatusCode: record.Fail.StatusCode,
		Body:       strings.TrimSpace(record.Fail.Body),
	}
	if !failed {
		return failDetail{StatusCode: 200}
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = internallogging.GetResponseStatus(ctx)
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = 500
	}
	return fail
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

func resolveEndpoint(ctx context.Context) string {
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}

const httpStatusBadRequest = 400
