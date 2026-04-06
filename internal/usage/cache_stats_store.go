package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	_ "modernc.org/sqlite"
)

const (
	defaultCacheStatisticsRecentLimit = 50
	defaultCacheStatisticsModelLimit  = 10
	defaultCacheStatisticsDays        = 14
)

type CacheStatisticsEvent struct {
	Timestamp       time.Time
	Provider        string
	Model           string
	ReasoningEffort string
	Source          string
	AuthID          string
	AuthIndex       string
	LatencyMs       int64
	Failed          bool
	Tokens          TokenStats
	Cache           *helps.CodexCacheObservability
	AnthropicCache  helps.AnthropicCacheObservability
}

type CacheStatisticsSummary struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessRequests int64   `json:"success_requests"`
	FailedRequests  int64   `json:"failed_requests"`
	InputTokens     int64   `json:"input_tokens"`
	EffectiveInputTokens int64 `json:"effective_input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CacheRatio      float64 `json:"cache_ratio"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
}

type CacheStatisticsModelSummary struct {
	Model           string  `json:"model"`
	Requests        int64   `json:"requests"`
	FailedRequests  int64   `json:"failed_requests"`
	InputTokens     int64   `json:"input_tokens"`
	EffectiveInputTokens int64 `json:"effective_input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CacheRatio      float64 `json:"cache_ratio"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
}

type CacheStatisticsDaySummary struct {
	Day          string  `json:"day"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	EffectiveInputTokens int64 `json:"effective_input_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CacheRatio   float64 `json:"cache_ratio"`
}

type CacheStatisticsRequest struct {
	ID                                int64     `json:"id"`
	Timestamp                         time.Time `json:"timestamp"`
	Provider                          string    `json:"provider"`
	Model                             string    `json:"model"`
	ReasoningEffort                   string    `json:"reasoning_effort,omitempty"`
	Source                            string    `json:"source"`
	AuthID                            string    `json:"auth_id"`
	AuthIndex                         string    `json:"auth_index"`
	LatencyMs                         int64     `json:"latency_ms"`
	Failed                            bool      `json:"failed"`
	InputTokens                       int64     `json:"input_tokens"`
	EffectiveInputTokens              int64     `json:"effective_input_tokens"`
	OutputTokens                      int64     `json:"output_tokens"`
	ReasoningTokens                   int64     `json:"reasoning_tokens"`
	CachedTokens                      int64     `json:"cached_tokens"`
	TotalTokens                       int64     `json:"total_tokens"`
	PromptCacheKey                    string    `json:"prompt_cache_key,omitempty"`
	PreviousResponseID                string    `json:"previous_response_id,omitempty"`
	ResponseID                        string    `json:"response_id,omitempty"`
	PromptCacheRetention              string    `json:"prompt_cache_retention,omitempty"`
	AnthropicRewriteApplied           bool      `json:"anthropic_rewrite_applied"`
	AnthropicOverwroteClientLayout    bool      `json:"anthropic_overwrote_client_layout"`
	AnthropicMatchedAgenticLoop       bool      `json:"anthropic_matched_agentic_loop"`
	AnthropicCacheTTL                 string    `json:"anthropic_cache_ttl,omitempty"`
	AnthropicBreakpoints              string    `json:"anthropic_breakpoints,omitempty"`
	AnthropicCacheCreationInputTokens int64     `json:"anthropic_cache_creation_input_tokens"`
	AnthropicCacheReadInputTokens     int64     `json:"anthropic_cache_read_input_tokens"`
	CacheRatio                        float64   `json:"cache_ratio"`
}

type CacheStatisticsSnapshot struct {
	Enabled        bool                          `json:"enabled"`
	DBPath         string                        `json:"db_path,omitempty"`
	Summary        CacheStatisticsSummary        `json:"summary"`
	ByModel        []CacheStatisticsModelSummary `json:"by_model"`
	ByDay          []CacheStatisticsDaySummary   `json:"by_day"`
	RecentRequests []CacheStatisticsRequest      `json:"recent_requests"`
}

func (snapshot CacheStatisticsSnapshot) Redacted() CacheStatisticsSnapshot {
	snapshot.DBPath = ""
	for i := range snapshot.RecentRequests {
		snapshot.RecentRequests[i].Source = ""
		snapshot.RecentRequests[i].AuthID = ""
		snapshot.RecentRequests[i].AuthIndex = ""
		snapshot.RecentRequests[i].PromptCacheKey = ""
		snapshot.RecentRequests[i].PreviousResponseID = ""
		snapshot.RecentRequests[i].ResponseID = ""
		snapshot.RecentRequests[i].PromptCacheRetention = ""
	}
	return snapshot
}

type CacheStatisticsStore struct {
	path string
	db   *sql.DB
}

var (
	cacheStatisticsStoreMu sync.RWMutex
	cacheStatisticsStore   *CacheStatisticsStore
)

func GetCacheStatisticsStore() *CacheStatisticsStore {
	cacheStatisticsStoreMu.RLock()
	defer cacheStatisticsStoreMu.RUnlock()
	return cacheStatisticsStore
}

func ClosePersistentStore() error {
	cacheStatisticsStoreMu.Lock()
	defer cacheStatisticsStoreMu.Unlock()
	if cacheStatisticsStore == nil {
		return nil
	}
	err := cacheStatisticsStore.Close()
	cacheStatisticsStore = nil
	return err
}

func ConfigurePersistentStore(cfg *config.Config, configFilePath string) error {
	enabled := cfg != nil && cfg.UsageStatisticsEnabled
	if !enabled {
		return ClosePersistentStore()
	}
	path, err := resolveCacheStatisticsDBPath(cfg, configFilePath)
	if err != nil {
		return err
	}

	cacheStatisticsStoreMu.RLock()
	existing := cacheStatisticsStore
	cacheStatisticsStoreMu.RUnlock()
	if existing != nil && existing.path == path {
		return nil
	}

	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		return err
	}

	cacheStatisticsStoreMu.Lock()
	old := cacheStatisticsStore
	cacheStatisticsStore = store
	cacheStatisticsStoreMu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return nil
}

func OpenCacheStatisticsStore(path string) (*CacheStatisticsStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("cache statistics store: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cache statistics store: create directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: create database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("cache statistics store: close database file: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
	} {
		if _, errExec := db.Exec(stmt); errExec != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cache statistics store: configure database: %w", errExec)
		}
	}
	store := &CacheStatisticsStore{path: path, db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cache statistics store: secure database file: %w", err)
	}
	return store, nil
}

func (s *CacheStatisticsStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *CacheStatisticsStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *CacheStatisticsStore) initSchema() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("cache statistics store: not initialized")
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS cache_statistics_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    requested_at TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    auth_id TEXT NOT NULL,
    auth_index TEXT NOT NULL,
    latency_ms INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL,
    cached_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    prompt_cache_key TEXT NOT NULL,
    previous_response_id TEXT NOT NULL,
    response_id TEXT NOT NULL,
    prompt_cache_retention TEXT NOT NULL,
    anthropic_rewrite_applied INTEGER NOT NULL DEFAULT 0,
    anthropic_overwrote_client_layout INTEGER NOT NULL DEFAULT 0,
    anthropic_matched_agentic_loop INTEGER NOT NULL DEFAULT 0,
    anthropic_cache_ttl TEXT NOT NULL DEFAULT '',
    anthropic_breakpoints TEXT NOT NULL DEFAULT '',
    anthropic_cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    anthropic_cache_read_input_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_requested_at ON cache_statistics_requests(requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_model ON cache_statistics_requests(model);
`)
	if err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "reasoning_effort", "ALTER TABLE cache_statistics_requests ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_rewrite_applied", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_rewrite_applied INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_overwrote_client_layout", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_overwrote_client_layout INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_matched_agentic_loop", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_matched_agentic_loop INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_cache_ttl", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_cache_ttl TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_breakpoints", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_breakpoints TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_cache_creation_input_tokens", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "anthropic_cache_read_input_tokens", "ALTER TABLE cache_statistics_requests ADD COLUMN anthropic_cache_read_input_tokens INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := s.initPromptCacheIndex(); err != nil {
		return err
	}
	return nil
}

func (s *CacheStatisticsStore) InsertEvent(ctx context.Context, event CacheStatisticsEvent) error {
	if s == nil || s.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tokens := normaliseTokenStats(event.Tokens)
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	cache := event.Cache
	_, err := s.db.ExecContext(ctx, `
INSERT INTO cache_statistics_requests (
    requested_at, provider, model, reasoning_effort, source, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(event.Provider),
		strings.TrimSpace(event.Model),
		strings.TrimSpace(event.ReasoningEffort),
		strings.TrimSpace(event.Source),
		strings.TrimSpace(event.AuthID),
		strings.TrimSpace(event.AuthIndex),
		normaliseNonNegative(event.LatencyMs),
		boolToInt(event.Failed),
		normaliseNonNegative(tokens.InputTokens),
		normaliseNonNegative(tokens.OutputTokens),
		normaliseNonNegative(tokens.ReasoningTokens),
		normaliseNonNegative(tokens.CachedTokens),
		normaliseNonNegative(tokens.TotalTokens),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PromptCacheKey }),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PreviousResponseID }),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.ResponseID }),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PromptCacheRetention }),
		boolToInt(event.AnthropicCache.RewriteApplied),
		boolToInt(event.AnthropicCache.OverwroteClientLayout),
		boolToInt(event.AnthropicCache.MatchedAgenticCodingLoop),
		strings.TrimSpace(event.AnthropicCache.TTL),
		anthropicBreakpointSummary(event.AnthropicCache),
		anthropicCacheCreationTokens(event.AnthropicCache),
		anthropicCacheReadTokens(event.AnthropicCache),
	)
	if err != nil {
		return fmt.Errorf("cache statistics store: insert event: %w", err)
	}
	return nil
}

func (s *CacheStatisticsStore) Snapshot(ctx context.Context, recentLimit, modelLimit, days int) (CacheStatisticsSnapshot, error) {
	return s.SnapshotByProvider(ctx, recentLimit, modelLimit, days, "")
}

func (s *CacheStatisticsStore) SnapshotByProvider(ctx context.Context, recentLimit, modelLimit, days int, provider string) (CacheStatisticsSnapshot, error) {
	if days <= 0 {
		days = defaultCacheStatisticsDays
	}
	return s.snapshotSince(ctx, recentLimit, modelLimit, snapshotSince(days), provider)
}

func (s *CacheStatisticsStore) SnapshotSince(ctx context.Context, recentLimit, modelLimit int, since time.Time) (CacheStatisticsSnapshot, error) {
	return s.SnapshotSinceByProvider(ctx, recentLimit, modelLimit, since, "")
}

func (s *CacheStatisticsStore) SnapshotSinceByProvider(ctx context.Context, recentLimit, modelLimit int, since time.Time, provider string) (CacheStatisticsSnapshot, error) {
	if since.IsZero() {
		return s.SnapshotByProvider(ctx, recentLimit, modelLimit, defaultCacheStatisticsDays, provider)
	}
	return s.snapshotSince(ctx, recentLimit, modelLimit, since.UTC().Format(time.RFC3339Nano), provider)
}

func (s *CacheStatisticsStore) snapshotSince(ctx context.Context, recentLimit, modelLimit int, since string, provider string) (CacheStatisticsSnapshot, error) {
	result := CacheStatisticsSnapshot{Enabled: s != nil && s.db != nil}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.db == nil {
		return result, nil
	}
	if recentLimit <= 0 {
		recentLimit = defaultCacheStatisticsRecentLimit
	}
	if modelLimit <= 0 {
		modelLimit = defaultCacheStatisticsModelLimit
	}
	result.DBPath = s.path

	summary, err := s.querySummary(ctx, since, provider)
	if err != nil {
		return result, err
	}
	byModel, err := s.queryModelSummaries(ctx, modelLimit, since, provider)
	if err != nil {
		return result, err
	}
	byDay, err := s.queryDaySummaries(ctx, since, provider)
	if err != nil {
		return result, err
	}
	recent, err := s.queryRecentRequests(ctx, recentLimit, since, provider)
	if err != nil {
		return result, err
	}
	result.Summary = summary
	result.ByModel = byModel
	result.ByDay = byDay
	result.RecentRequests = recent
	return result, nil
}

func snapshotSince(days int) string {
	if days <= 0 {
		days = defaultCacheStatisticsDays
	}
	since := time.Now().UTC().AddDate(0, 0, -(days - 1))
	since = since.Truncate(24 * time.Hour)
	return since.Format(time.RFC3339Nano)
}

func (s *CacheStatisticsStore) StatisticsSnapshot(ctx context.Context) (StatisticsSnapshot, error) {
	return s.StatisticsSnapshotByProvider(ctx, "")
}

func (s *CacheStatisticsStore) StatisticsSnapshotByProvider(ctx context.Context, provider string) (StatisticsSnapshot, error) {
	result := StatisticsSnapshot{
		APIs:           make(map[string]APISnapshot),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.db == nil {
		return result, nil
	}

	query := `
SELECT
    requested_at,
    provider,
    model,
    source,
    auth_id,
    auth_index,
    latency_ms,
    failed,
    input_tokens,
    output_tokens,
    reasoning_tokens,
    cached_tokens,
    total_tokens,
    prompt_cache_key,
    previous_response_id,
    response_id,
    prompt_cache_retention,
    anthropic_cache_creation_input_tokens,
    anthropic_cache_read_input_tokens
FROM cache_statistics_requests
WHERE 1 = 1`
	args := []any{}
	query, args = appendCacheStatisticsProviderFilter(query, args, provider)
	query += `
ORDER BY requested_at ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("cache statistics store: usage snapshot query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			requestedAt                  string
			provider                     string
			model                        string
			source                       string
			authID                       string
			authIndex                    string
			latencyMs                    int64
			failedInt                    int
			inputTokens                  int64
			outputTokens                 int64
			reasoningTokens              int64
			cachedTokens                 int64
			totalTokens                  int64
			promptCacheKey               string
			previousResponseID           string
			responseID                   string
			promptCacheRetention         string
			anthropicCacheCreationTokens int64
			anthropicCacheReadTokens     int64
		)
		if err := rows.Scan(
			&requestedAt,
			&provider,
			&model,
			&source,
			&authID,
			&authIndex,
			&latencyMs,
			&failedInt,
			&inputTokens,
			&outputTokens,
			&reasoningTokens,
			&cachedTokens,
			&totalTokens,
			&promptCacheKey,
			&previousResponseID,
			&responseID,
			&promptCacheRetention,
			&anthropicCacheCreationTokens,
			&anthropicCacheReadTokens,
		); err != nil {
			return result, fmt.Errorf("cache statistics store: usage snapshot scan: %w", err)
		}

		timestamp, err := time.Parse(time.RFC3339Nano, requestedAt)
		if err != nil {
			continue
		}
		model = strings.TrimSpace(model)
		if model == "" {
			model = "unknown"
		}
		apiKey := strings.TrimSpace(authID)
		if apiKey == "" {
			apiKey = strings.TrimSpace(authIndex)
		}
		if apiKey == "" {
			apiKey = strings.TrimSpace(provider)
		}
		if apiKey == "" {
			apiKey = "persisted"
		}
		tokens := normaliseTokenStats(TokenStats{
			InputTokens:     inputTokens,
			OutputTokens:    outputTokens,
			ReasoningTokens: reasoningTokens,
			CachedTokens:    cachedTokens,
			TotalTokens:     totalTokens,
		})
		detail := RequestDetail{
			Timestamp: timestamp,
			LatencyMs: latencyMs,
			Source:    source,
			AuthIndex: authIndex,
			Tokens:    tokens,
			Failed:    failedInt != 0,
		}
		if promptCacheKey != "" || previousResponseID != "" || responseID != "" || promptCacheRetention != "" {
			detail.Cache = &helps.CodexCacheObservability{
				PromptCacheKey:       promptCacheKey,
				PreviousResponseID:   previousResponseID,
				ResponseID:           responseID,
				PromptCacheRetention: promptCacheRetention,
			}
		}
		if anthropicCacheCreationTokens != 0 || anthropicCacheReadTokens != 0 {
			detail.AnthropicCache = &helps.AnthropicCacheObservability{
				CacheCreationInputTokens: anthropicCacheCreationTokens,
				CacheReadInputTokens:     anthropicCacheReadTokens,
			}
		}

		result.TotalRequests++
		if detail.Failed {
			result.FailureCount++
		} else {
			result.SuccessCount++
		}
		result.TotalTokens += tokens.TotalTokens
		dayKey := timestamp.Format("2006-01-02")
		hourKey := formatHour(timestamp.Hour())
		result.RequestsByDay[dayKey]++
		result.RequestsByHour[hourKey]++
		result.TokensByDay[dayKey] += tokens.TotalTokens
		result.TokensByHour[hourKey] += tokens.TotalTokens

		apiSnapshot := result.APIs[apiKey]
		if apiSnapshot.Models == nil {
			apiSnapshot.Models = make(map[string]ModelSnapshot)
		}
		apiSnapshot.TotalRequests++
		apiSnapshot.TotalTokens += tokens.TotalTokens
		modelSnapshot := apiSnapshot.Models[model]
		modelSnapshot.TotalRequests++
		modelSnapshot.TotalTokens += tokens.TotalTokens
		modelSnapshot.Details = append(modelSnapshot.Details, detail)
		apiSnapshot.Models[model] = modelSnapshot
		result.APIs[apiKey] = apiSnapshot
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("cache statistics store: usage snapshot iterate: %w", err)
	}
	return result, nil
}

func (s *CacheStatisticsStore) querySummary(ctx context.Context, since string, provider string) (CacheStatisticsSummary, error) {
	var summary CacheStatisticsSummary
	query := `
SELECT
    COUNT(*),
    COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(CASE
        WHEN LOWER(provider) = 'claude' THEN input_tokens + anthropic_cache_creation_input_tokens +
            CASE
                WHEN anthropic_cache_read_input_tokens > 0 THEN anthropic_cache_read_input_tokens
                ELSE cached_tokens
            END
        ELSE input_tokens
    END), 0),
    COALESCE(SUM(output_tokens), 0),
    COALESCE(SUM(reasoning_tokens), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0),
    COALESCE(AVG(latency_ms), 0)
FROM cache_statistics_requests
WHERE requested_at >= ?`
	args := []any{since}
	query, args = appendCacheStatisticsProviderFilter(query, args, provider)
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.TotalRequests,
		&summary.SuccessRequests,
		&summary.FailedRequests,
		&summary.InputTokens,
		&summary.EffectiveInputTokens,
		&summary.OutputTokens,
		&summary.ReasoningTokens,
		&summary.CachedTokens,
		&summary.TotalTokens,
		&summary.AvgLatencyMs,
	)
	if err != nil {
		return summary, fmt.Errorf("cache statistics store: query summary: %w", err)
	}
	summary.CacheRatio = ratio(summary.CachedTokens, cacheStatisticsRatioDenominator(summary.EffectiveInputTokens, summary.InputTokens))
	return summary, nil
}

func (s *CacheStatisticsStore) queryModelSummaries(ctx context.Context, limit int, since string, provider string) ([]CacheStatisticsModelSummary, error) {
	query := `
SELECT
    model,
    COUNT(*),
    COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(CASE
        WHEN LOWER(provider) = 'claude' THEN input_tokens + anthropic_cache_creation_input_tokens +
            CASE
                WHEN anthropic_cache_read_input_tokens > 0 THEN anthropic_cache_read_input_tokens
                ELSE cached_tokens
            END
        ELSE input_tokens
    END), 0),
    COALESCE(SUM(output_tokens), 0),
    COALESCE(SUM(reasoning_tokens), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0),
    COALESCE(AVG(latency_ms), 0)
FROM cache_statistics_requests
WHERE requested_at >= ?`
	args := []any{since}
	query, args = appendCacheStatisticsProviderFilter(query, args, provider)
	query += `
GROUP BY model
ORDER BY COUNT(*) DESC, model ASC
LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: query model summaries: %w", err)
	}
	defer rows.Close()
	result := make([]CacheStatisticsModelSummary, 0)
	for rows.Next() {
		var item CacheStatisticsModelSummary
		if err := rows.Scan(
			&item.Model,
			&item.Requests,
			&item.FailedRequests,
			&item.InputTokens,
			&item.EffectiveInputTokens,
			&item.OutputTokens,
			&item.ReasoningTokens,
			&item.CachedTokens,
			&item.TotalTokens,
			&item.AvgLatencyMs,
		); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan model summary: %w", err)
		}
		item.CacheRatio = ratio(item.CachedTokens, cacheStatisticsRatioDenominator(item.EffectiveInputTokens, item.InputTokens))
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate model summaries: %w", err)
	}
	return result, nil
}

func (s *CacheStatisticsStore) queryDaySummaries(ctx context.Context, since string, provider string) ([]CacheStatisticsDaySummary, error) {
	query := `
SELECT
    substr(requested_at, 1, 10) AS day,
    COUNT(*),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(CASE
        WHEN LOWER(provider) = 'claude' THEN input_tokens + anthropic_cache_creation_input_tokens +
            CASE
                WHEN anthropic_cache_read_input_tokens > 0 THEN anthropic_cache_read_input_tokens
                ELSE cached_tokens
            END
        ELSE input_tokens
    END), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0)
FROM cache_statistics_requests
WHERE requested_at >= ?`
	args := []any{since}
	query, args = appendCacheStatisticsProviderFilter(query, args, provider)
	query += `
GROUP BY substr(requested_at, 1, 10)
ORDER BY day ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: query day summaries: %w", err)
	}
	defer rows.Close()
	result := make([]CacheStatisticsDaySummary, 0)
	for rows.Next() {
		var item CacheStatisticsDaySummary
		if err := rows.Scan(&item.Day, &item.Requests, &item.InputTokens, &item.EffectiveInputTokens, &item.CachedTokens, &item.TotalTokens); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan day summary: %w", err)
		}
		item.CacheRatio = ratio(item.CachedTokens, cacheStatisticsRatioDenominator(item.EffectiveInputTokens, item.InputTokens))
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate day summaries: %w", err)
	}
	return result, nil
}

func (s *CacheStatisticsStore) queryRecentRequests(ctx context.Context, limit int, since string, provider string) ([]CacheStatisticsRequest, error) {
	query := `
SELECT
    id, requested_at, provider, model, reasoning_effort, source, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens
FROM cache_statistics_requests
WHERE requested_at >= ?`
	args := []any{since}
	query, args = appendCacheStatisticsProviderFilter(query, args, provider)
	query += `
ORDER BY requested_at DESC, id DESC
LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: query recent requests: %w", err)
	}
	defer rows.Close()
	result := make([]CacheStatisticsRequest, 0)
	for rows.Next() {
		var item CacheStatisticsRequest
		var requestedAt string
		var failedInt int
		if err := rows.Scan(
			&item.ID,
			&requestedAt,
			&item.Provider,
			&item.Model,
			&item.ReasoningEffort,
			&item.Source,
			&item.AuthID,
			&item.AuthIndex,
			&item.LatencyMs,
			&failedInt,
			&item.InputTokens,
			&item.OutputTokens,
			&item.ReasoningTokens,
			&item.CachedTokens,
			&item.TotalTokens,
			&item.PromptCacheKey,
			&item.PreviousResponseID,
			&item.ResponseID,
			&item.PromptCacheRetention,
			&item.AnthropicRewriteApplied,
			&item.AnthropicOverwroteClientLayout,
			&item.AnthropicMatchedAgenticLoop,
			&item.AnthropicCacheTTL,
			&item.AnthropicBreakpoints,
			&item.AnthropicCacheCreationInputTokens,
			&item.AnthropicCacheReadInputTokens,
		); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan recent request: %w", err)
		}
		item.Failed = failedInt != 0
		if ts, errParse := time.Parse(time.RFC3339Nano, requestedAt); errParse == nil {
			item.Timestamp = ts
		}
		item.EffectiveInputTokens = cacheStatisticsEffectiveInputTokens(item.Provider, item.InputTokens, item.CachedTokens, item.AnthropicCacheCreationInputTokens, item.AnthropicCacheReadInputTokens)
		item.CacheRatio = ratio(item.CachedTokens, cacheStatisticsRatioDenominator(item.EffectiveInputTokens, item.InputTokens))
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate recent requests: %w", err)
	}
	return result, nil
}

func appendCacheStatisticsProviderFilter(query string, args []any, provider string) (string, []any) {
	providers := cacheStatisticsProvidersForFilter(provider)
	if len(providers) == 0 {
		return query, args
	}
	placeholders := make([]string, 0, len(providers))
	for _, item := range providers {
		placeholders = append(placeholders, "?")
		args = append(args, item)
	}
	query += " AND provider IN (" + strings.Join(placeholders, ",") + ")"
	return query, args
}

func cacheStatisticsProvidersForFilter(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return nil
	case "gemini":
		return []string{"gemini", "gemini-cli", "aistudio", "antigravity"}
	case "codex":
		return []string{"codex"}
	case "claude":
		return []string{"claude"}
	case "vertex":
		return []string{"vertex"}
	case "ampcode":
		return []string{"ampcode"}
	case "openai-compatible", "openai_compatible", "openai compatible providers":
		return []string{"openai-compatibility", "openrouter"}
	default:
		return []string{strings.TrimSpace(provider)}
	}
}

func ensureCacheStatisticsColumn(db *sql.DB, tableName, columnName, alterSQL string) error {
	if db == nil {
		return fmt.Errorf("cache statistics store: database is nil")
	}
	var count int
	row := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tableName, columnName)
	if err := row.Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := db.Exec(alterSQL)
	return err
}

func resolveCacheStatisticsDBPath(cfg *config.Config, configFilePath string) (string, error) {
	if base := util.WritablePath(); base != "" {
		return filepath.Join(base, "stats", "cache-statistics.sqlite"), nil
	}
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath != "" {
		base := filepath.Dir(configFilePath)
		if info, err := os.Stat(configFilePath); err == nil && info.IsDir() {
			base = configFilePath
		}
		return filepath.Join(base, "stats", "cache-statistics.sqlite"), nil
	}
	if cfg != nil && strings.TrimSpace(cfg.AuthDir) != "" {
		authDir, err := util.ResolveAuthDir(cfg.AuthDir)
		if err != nil {
			return "", err
		}
		if authDir != "" {
			return filepath.Join(authDir, "stats", "cache-statistics.sqlite"), nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join("stats", "cache-statistics.sqlite"), nil
	}
	return filepath.Join(wd, "stats", "cache-statistics.sqlite"), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ratio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func normaliseNonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func cacheStatisticsEffectiveInputTokens(provider string, inputTokens, cachedTokens, anthropicCacheCreationTokens, anthropicCacheReadTokens int64) int64 {
	inputTokens = normaliseNonNegative(inputTokens)
	if strings.EqualFold(strings.TrimSpace(provider), "claude") {
		cacheReadTokens := normaliseNonNegative(anthropicCacheReadTokens)
		if cacheReadTokens == 0 {
			cacheReadTokens = normaliseNonNegative(cachedTokens)
		}
		return inputTokens + normaliseNonNegative(anthropicCacheCreationTokens) + cacheReadTokens
	}
	return inputTokens
}

func cacheStatisticsRatioDenominator(effectiveInputTokens, inputTokens int64) int64 {
	if effectiveInputTokens > 0 {
		return effectiveInputTokens
	}
	return inputTokens
}

func cacheString(cache *helps.CodexCacheObservability, extract func(*helps.CodexCacheObservability) string) string {
	if cache == nil {
		return ""
	}
	return strings.TrimSpace(extract(cache))
}

func anthropicBreakpointSummary(obs helps.AnthropicCacheObservability) string {
	parts := make([]string, 0, 3)
	if obs.ToolsBreakpoint {
		parts = append(parts, "tools")
	}
	if obs.SystemBreakpoint {
		parts = append(parts, "system")
	}
	if obs.MessagesBreakpoint {
		parts = append(parts, "messages")
	}
	return strings.Join(parts, ",")
}

func anthropicCacheCreationTokens(obs helps.AnthropicCacheObservability) int64 {
	return normaliseNonNegative(obs.CacheCreationInputTokens)
}

func anthropicCacheReadTokens(obs helps.AnthropicCacheObservability) int64 {
	return normaliseNonNegative(obs.CacheReadInputTokens)
}
