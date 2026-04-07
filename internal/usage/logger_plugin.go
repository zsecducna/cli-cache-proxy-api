// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)
	store := GetCacheStatisticsStore()
	if store == nil {
		return
	}
	event := buildCacheStatisticsEvent(ctx, record)
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.InsertEvent(persistCtx, event); err != nil {
		log.WithError(err).Warn("failed to persist cache statistics event")
	}
}

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp, latency, and token usage for a single request.
type RequestDetail struct {
	Timestamp      time.Time                          `json:"timestamp"`
	Provider       string                             `json:"provider,omitempty"`
	CustomerID     string                             `json:"customer_id,omitempty"`
	LatencyMs      int64                              `json:"latency_ms"`
	Source         string                             `json:"source"`
	AuthIndex      string                             `json:"auth_index"`
	Tokens         TokenStats                         `json:"tokens"`
	Cache          *helps.CodexCacheObservability     `json:"cache,omitempty"`
	AnthropicCache *helps.AnthropicCacheObservability `json:"anthropic_cache,omitempty"`
	Failed         bool                               `json:"failed"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

func (s StatisticsSnapshot) Redacted() StatisticsSnapshot {
	result := StatisticsSnapshot{
		TotalRequests:  s.TotalRequests,
		SuccessCount:   s.SuccessCount,
		FailureCount:   s.FailureCount,
		TotalTokens:    s.TotalTokens,
		APIs:           make(map[string]APISnapshot, len(s.APIs)),
		RequestsByDay:  cloneStringInt64Map(s.RequestsByDay),
		RequestsByHour: cloneStringInt64Map(s.RequestsByHour),
		TokensByDay:    cloneStringInt64Map(s.TokensByDay),
		TokensByHour:   cloneStringInt64Map(s.TokensByHour),
	}
	for apiKey, apiSnapshot := range s.APIs {
		maskedKey := redactStatisticsAPIKey(apiKey)
		redactedSnapshot := APISnapshot{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(apiSnapshot.Models)),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			redactedModel := ModelSnapshot{
				TotalRequests: modelSnapshot.TotalRequests,
				TotalTokens:   modelSnapshot.TotalTokens,
				Details:       make([]RequestDetail, 0, len(modelSnapshot.Details)),
			}
			for _, detail := range modelSnapshot.Details {
				redactedModel.Details = append(redactedModel.Details, redactRequestDetail(detail))
			}
			redactedSnapshot.Models[modelName] = redactedModel
		}
		existing, ok := result.APIs[maskedKey]
		if !ok {
			result.APIs[maskedKey] = redactedSnapshot
			continue
		}
		result.APIs[maskedKey] = mergeRedactedAPISnapshot(existing, redactedSnapshot)
	}
	return result
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	statsKey, modelName, detail := prepareRequestDetail(ctx, record)
	totalTokens := detail.Tokens.TotalTokens
	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	}
	s.updateAPIStats(stats, modelName, detail)

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       requestDetails,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := formatHour(hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := formatHour(hour)
		result.TokensByHour[key] = v
	}

	return result
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved and duplicate request details are skipped.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		stats, ok := s.apis[apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				s.recordImported(apiName, modelName, stats, detail)
				result.Added++
			}
		}
	}

	return result
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(stats, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)
	cachePromptKey, cachePreviousResponseID, cacheResponseID, cacheRetention := "", "", "", ""
	if detail.Cache != nil {
		cachePromptKey = detail.Cache.PromptCacheKey
		cachePreviousResponseID = detail.Cache.PreviousResponseID
		cacheResponseID = detail.Cache.ResponseID
		cacheRetention = detail.Cache.PromptCacheRetention
	}
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d|%s|%s|%s|%s",
		apiName,
		modelName,
		timestamp,
		detail.Provider,
		detail.CustomerID,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
		cachePromptKey,
		cachePreviousResponseID,
		cacheResponseID,
		cacheRetention,
	)
}

func prepareRequestDetail(ctx context.Context, record coreusage.Record) (string, string, RequestDetail) {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := RequestDetail{
		Timestamp:      timestamp,
		Provider:       strings.TrimSpace(record.Provider),
		CustomerID:     strings.TrimSpace(record.CustomerID),
		LatencyMs:      normaliseLatency(record.Latency),
		Source:         record.Source,
		AuthIndex:      record.AuthIndex,
		Tokens:         normaliseDetail(record.Detail),
		Cache:          resolveCodexCacheMetadata(ctx),
		AnthropicCache: resolveAnthropicCacheMetadata(ctx),
	}
	statsKey := statisticsBucketKey(detail.CustomerID, record.APIKey, record.AuthID, record.AuthIndex, resolveAPIIdentifier(ctx, record))
	detail.Failed = record.Failed
	if !detail.Failed {
		detail.Failed = !resolveSuccess(ctx)
	}
	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	return statsKey, modelName, detail
}

func buildCacheStatisticsEvent(ctx context.Context, record coreusage.Record) CacheStatisticsEvent {
	_, modelName, detail := prepareRequestDetail(ctx, record)
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	return CacheStatisticsEvent{
		Timestamp:       detail.Timestamp,
		Provider:        provider,
		Model:           modelName,
		ReasoningEffort: strings.TrimSpace(record.ReasoningEffort),
		Source:          detail.Source,
		APIKey:          strings.TrimSpace(record.APIKey),
		CustomerID:      detail.CustomerID,
		AuthID:          record.AuthID,
		AuthIndex:       detail.AuthIndex,
		LatencyMs:       detail.LatencyMs,
		Failed:          detail.Failed,
		Tokens:          detail.Tokens,
		Cache:           detail.Cache,
		AnthropicCache:  valueOrZeroAnthropic(detail.AnthropicCache),
	}
}

func statisticsBucketKey(customerID, apiKey, authID, authIndex, fallback string) string {
	for _, candidate := range []string{customerID, apiKey, authID, authIndex, fallback} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" {
			return trimmed
		}
	}
	return "unknown"
}

func redactStatisticsAPIKey(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "redacted"
	}
	return util.HideAPIKey(apiKey)
}

func redactRequestDetail(detail RequestDetail) RequestDetail {
	detail.Source = ""
	detail.AuthIndex = ""
	detail.Cache = nil
	return detail
}

// FilterStatisticsSnapshotByProvider keeps only request details that belong to the
// requested provider group and recomputes aggregates from the retained details.
func FilterStatisticsSnapshotByProvider(snapshot StatisticsSnapshot, provider string) StatisticsSnapshot {
	providers := cacheStatisticsProvidersForFilter(provider)
	if len(providers) == 0 {
		return snapshot
	}
	allowed := make(map[string]struct{}, len(providers))
	for _, item := range providers {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		allowed[item] = struct{}{}
	}
	filtered := NewRequestStatistics()
	for apiName, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if !statisticsDetailMatchesProvider(detail, allowed) {
					continue
				}
				stats, ok := filtered.apis[apiName]
				if !ok {
					stats = &apiStats{Models: make(map[string]*modelStats)}
					filtered.apis[apiName] = stats
				}
				filtered.recordImported(apiName, modelName, stats, detail)
			}
		}
	}
	return filtered.Snapshot()
}

// MergeStatisticsSnapshots combines multiple snapshots into one deduplicated view.
func MergeStatisticsSnapshots(snapshots ...StatisticsSnapshot) StatisticsSnapshot {
	merged := NewRequestStatistics()
	for _, snapshot := range snapshots {
		merged.MergeSnapshot(snapshot)
	}
	return merged.Snapshot()
}

func statisticsDetailMatchesProvider(detail RequestDetail, allowed map[string]struct{}) bool {
	provider := strings.ToLower(strings.TrimSpace(detail.Provider))
	if provider == "" {
		return false
	}
	_, ok := allowed[provider]
	return ok
}

func mergeRedactedAPISnapshot(left, right APISnapshot) APISnapshot {
	merged := APISnapshot{
		TotalRequests: left.TotalRequests + right.TotalRequests,
		TotalTokens:   left.TotalTokens + right.TotalTokens,
		Models:        make(map[string]ModelSnapshot, len(left.Models)+len(right.Models)),
	}
	for modelName, snapshot := range left.Models {
		merged.Models[modelName] = snapshot
	}
	for modelName, snapshot := range right.Models {
		existing, ok := merged.Models[modelName]
		if !ok {
			merged.Models[modelName] = snapshot
			continue
		}
		existing.TotalRequests += snapshot.TotalRequests
		existing.TotalTokens += snapshot.TotalTokens
		existing.Details = append(existing.Details, snapshot.Details...)
		merged.Models[modelName] = existing
	}
	return merged
}

func cloneStringInt64Map(source map[string]int64) map[string]int64 {
	if len(source) == 0 {
		return map[string]int64{}
	}
	cloned := make(map[string]int64, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			path := ginCtx.FullPath()
			if path == "" && ginCtx.Request != nil {
				path = ginCtx.Request.URL.Path
			}
			method := ""
			if ginCtx.Request != nil {
				method = ginCtx.Request.Method
			}
			if path != "" {
				if method != "" {
					return method + " " + path
				}
				return path
			}
		}
	}
	if record.Provider != "" {
		return record.Provider
	}
	return "unknown"
}

func resolveSuccess(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return true
	}
	status := ginCtx.Writer.Status()
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

const httpStatusBadRequest = 400

func normaliseDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:     detail.InputTokens,
		OutputTokens:    detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens,
		CachedTokens:    detail.CachedTokens,
		TotalTokens:     detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
	}
	return tokens
}

func normaliseTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return tokens
}

func normaliseLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func resolveCodexCacheMetadata(ctx context.Context) *helps.CodexCacheObservability {
	obs, ok := helps.GetCodexCacheObservability(ctx)
	if !ok {
		return nil
	}
	return &obs
}

func resolveAnthropicCacheMetadata(ctx context.Context) *helps.AnthropicCacheObservability {
	obs, ok := helps.GetAnthropicCacheObservability(ctx)
	if !ok {
		return nil
	}
	return &obs
}

func valueOrZeroAnthropic(obs *helps.AnthropicCacheObservability) helps.AnthropicCacheObservability {
	if obs == nil {
		return helps.AnthropicCacheObservability{}
	}
	return *obs
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
