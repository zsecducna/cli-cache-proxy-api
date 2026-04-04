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
	Timestamp time.Time
	Provider  string
	Model     string
	Source    string
	AuthID    string
	AuthIndex string
	LatencyMs int64
	Failed    bool
	Tokens    TokenStats
	Cache     *helps.CodexCacheObservability
}

type CacheStatisticsSummary struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessRequests int64   `json:"success_requests"`
	FailedRequests  int64   `json:"failed_requests"`
	InputTokens     int64   `json:"input_tokens"`
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
	CachedTokens int64   `json:"cached_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CacheRatio   float64 `json:"cache_ratio"`
}

type CacheStatisticsRequest struct {
	ID                   int64     `json:"id"`
	Timestamp            time.Time `json:"timestamp"`
	Provider             string    `json:"provider"`
	Model                string    `json:"model"`
	Source               string    `json:"source"`
	AuthID               string    `json:"auth_id"`
	AuthIndex            string    `json:"auth_index"`
	LatencyMs            int64     `json:"latency_ms"`
	Failed               bool      `json:"failed"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
	ReasoningTokens      int64     `json:"reasoning_tokens"`
	CachedTokens         int64     `json:"cached_tokens"`
	TotalTokens          int64     `json:"total_tokens"`
	PromptCacheKey       string    `json:"prompt_cache_key,omitempty"`
	PreviousResponseID   string    `json:"previous_response_id,omitempty"`
	ResponseID           string    `json:"response_id,omitempty"`
	PromptCacheRetention string    `json:"prompt_cache_retention,omitempty"`
	CacheRatio           float64   `json:"cache_ratio"`
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
    prompt_cache_retention TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_requested_at ON cache_statistics_requests(requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_model ON cache_statistics_requests(model);
`)
	if err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
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
    requested_at, provider, model, source, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(event.Provider),
		strings.TrimSpace(event.Model),
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
	)
	if err != nil {
		return fmt.Errorf("cache statistics store: insert event: %w", err)
	}
	return nil
}

func (s *CacheStatisticsStore) Snapshot(ctx context.Context, recentLimit, modelLimit, days int) (CacheStatisticsSnapshot, error) {
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
	if days <= 0 {
		days = defaultCacheStatisticsDays
	}
	since := snapshotSince(days)
	result.DBPath = s.path

	summary, err := s.querySummary(ctx, since)
	if err != nil {
		return result, err
	}
	byModel, err := s.queryModelSummaries(ctx, modelLimit, since)
	if err != nil {
		return result, err
	}
	byDay, err := s.queryDaySummaries(ctx, since)
	if err != nil {
		return result, err
	}
	recent, err := s.queryRecentRequests(ctx, recentLimit, since)
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

func (s *CacheStatisticsStore) querySummary(ctx context.Context, since string) (CacheStatisticsSummary, error) {
	var summary CacheStatisticsSummary
	err := s.db.QueryRowContext(ctx, `
SELECT
    COUNT(*),
    COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(output_tokens), 0),
    COALESCE(SUM(reasoning_tokens), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0),
    COALESCE(AVG(latency_ms), 0)
FROM cache_statistics_requests
WHERE requested_at >= ?`, since).Scan(
		&summary.TotalRequests,
		&summary.SuccessRequests,
		&summary.FailedRequests,
		&summary.InputTokens,
		&summary.OutputTokens,
		&summary.ReasoningTokens,
		&summary.CachedTokens,
		&summary.TotalTokens,
		&summary.AvgLatencyMs,
	)
	if err != nil {
		return summary, fmt.Errorf("cache statistics store: query summary: %w", err)
	}
	summary.CacheRatio = ratio(summary.CachedTokens, summary.InputTokens)
	return summary, nil
}

func (s *CacheStatisticsStore) queryModelSummaries(ctx context.Context, limit int, since string) ([]CacheStatisticsModelSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    model,
    COUNT(*),
    COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(output_tokens), 0),
    COALESCE(SUM(reasoning_tokens), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0),
    COALESCE(AVG(latency_ms), 0)
FROM cache_statistics_requests
WHERE requested_at >= ?
GROUP BY model
ORDER BY COUNT(*) DESC, model ASC
LIMIT ?`, since, limit)
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
			&item.OutputTokens,
			&item.ReasoningTokens,
			&item.CachedTokens,
			&item.TotalTokens,
			&item.AvgLatencyMs,
		); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan model summary: %w", err)
		}
		item.CacheRatio = ratio(item.CachedTokens, item.InputTokens)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate model summaries: %w", err)
	}
	return result, nil
}

func (s *CacheStatisticsStore) queryDaySummaries(ctx context.Context, since string) ([]CacheStatisticsDaySummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    substr(requested_at, 1, 10) AS day,
    COUNT(*),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0)
FROM cache_statistics_requests
WHERE requested_at >= ?
GROUP BY substr(requested_at, 1, 10)
ORDER BY day ASC`, since)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: query day summaries: %w", err)
	}
	defer rows.Close()
	result := make([]CacheStatisticsDaySummary, 0)
	for rows.Next() {
		var item CacheStatisticsDaySummary
		if err := rows.Scan(&item.Day, &item.Requests, &item.InputTokens, &item.CachedTokens, &item.TotalTokens); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan day summary: %w", err)
		}
		item.CacheRatio = ratio(item.CachedTokens, item.InputTokens)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate day summaries: %w", err)
	}
	return result, nil
}

func (s *CacheStatisticsStore) queryRecentRequests(ctx context.Context, limit int, since string) ([]CacheStatisticsRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    id, requested_at, provider, model, source, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention
FROM cache_statistics_requests
WHERE requested_at >= ?
ORDER BY requested_at DESC, id DESC
LIMIT ?`, since, limit)
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
		); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan recent request: %w", err)
		}
		item.Failed = failedInt != 0
		if ts, errParse := time.Parse(time.RFC3339Nano, requestedAt); errParse == nil {
			item.Timestamp = ts
		}
		item.CacheRatio = ratio(item.CachedTokens, item.InputTokens)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate recent requests: %w", err)
	}
	return result, nil
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

func cacheString(cache *helps.CodexCacheObservability, extract func(*helps.CodexCacheObservability) string) string {
	if cache == nil {
		return ""
	}
	return strings.TrimSpace(extract(cache))
}
