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
	openAILongContextInputThreshold   = int64(272000)
)

type CacheStatisticsEvent struct {
	Timestamp       time.Time
	Provider        string
	Model           string
	ReasoningEffort string
	Source          string
	APIKey          string
	CustomerID      string
	CustomerEmail   string
	AuthID          string
	AuthIndex       string
	LatencyMs       int64
	Failed          bool
	Tokens          TokenStats
	Cache           *helps.CodexCacheObservability
	AnthropicCache  helps.AnthropicCacheObservability
	StreamID        string
}

type CacheStatisticsSummary struct {
	TotalRequests           int64                         `json:"total_requests"`
	SuccessRequests         int64                         `json:"success_requests"`
	FailedRequests          int64                         `json:"failed_requests"`
	SuccessPercentage       float64                       `json:"success_percentage"`
	InputTokens             int64                         `json:"input_tokens"`
	EffectiveInputTokens    int64                         `json:"effective_input_tokens"`
	LongContextInputTokens  int64                         `json:"long_context_input_tokens,omitempty"`
	LongContextCachedTokens int64                         `json:"long_context_cached_tokens,omitempty"`
	LongContextOutputTokens int64                         `json:"long_context_output_tokens,omitempty"`
	OutputTokens            int64                         `json:"output_tokens"`
	ReasoningTokens         int64                         `json:"reasoning_tokens"`
	CachedTokens            int64                         `json:"cached_tokens"`
	TotalTokens             int64                         `json:"total_tokens"`
	CacheRatio                      float64                       `json:"cache_ratio"`
	AvgLatencyMs                    float64                       `json:"avg_latency_ms"`
	AnthropicCacheWrite5mTokens     int64                         `json:"anthropic_cache_write_5m_tokens,omitempty"`
	AnthropicCacheWrite1hTokens     int64                         `json:"anthropic_cache_write_1h_tokens,omitempty"`
	GPT54                           CacheStatisticsModelBreakdown `json:"gpt_5_4"`
}

type CacheStatisticsBreakdownBucket struct {
	RequestCount int64 `json:"request_count"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type CacheStatisticsModelBreakdown struct {
	Standard    CacheStatisticsBreakdownBucket `json:"standard"`
	LongContext CacheStatisticsBreakdownBucket `json:"long_context"`
}

type CacheStatisticsModelSummary struct {
	Model                   string  `json:"model"`
	Requests                int64   `json:"requests"`
	FailedRequests          int64   `json:"failed_requests"`
	InputTokens             int64   `json:"input_tokens"`
	EffectiveInputTokens    int64   `json:"effective_input_tokens"`
	LongContextInputTokens  int64   `json:"long_context_input_tokens,omitempty"`
	LongContextCachedTokens int64   `json:"long_context_cached_tokens,omitempty"`
	LongContextOutputTokens int64   `json:"long_context_output_tokens,omitempty"`
	OutputTokens                    int64   `json:"output_tokens"`
	ReasoningTokens                 int64   `json:"reasoning_tokens"`
	CachedTokens                    int64   `json:"cached_tokens"`
	TotalTokens                     int64   `json:"total_tokens"`
	CacheRatio                      float64 `json:"cache_ratio"`
	AvgLatencyMs                    float64 `json:"avg_latency_ms"`
	AnthropicCacheWrite5mTokens     int64   `json:"anthropic_cache_write_5m_tokens,omitempty"`
	AnthropicCacheWrite1hTokens     int64   `json:"anthropic_cache_write_1h_tokens,omitempty"`
}

type CacheStatisticsDaySummary struct {
	Day                  string  `json:"day"`
	Requests             int64   `json:"requests"`
	InputTokens          int64   `json:"input_tokens"`
	EffectiveInputTokens int64   `json:"effective_input_tokens"`
	CachedTokens         int64   `json:"cached_tokens"`
	TotalTokens          int64   `json:"total_tokens"`
	CacheRatio           float64 `json:"cache_ratio"`
}

type CacheStatisticsRequest struct {
	ID                                int64     `json:"id"`
	Timestamp                         time.Time `json:"timestamp"`
	Provider                          string    `json:"provider"`
	Model                             string    `json:"model"`
	ReasoningEffort                   string    `json:"reasoning_effort,omitempty"`
	Source                            string    `json:"source"`
	APIKey                            string    `json:"api_key,omitempty"`
	CustomerID                        string    `json:"customer_id,omitempty"`
	CustomerEmail                     string    `json:"customer_email,omitempty"`
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
	Enabled        bool                                 `json:"enabled"`
	DBPath         string                               `json:"db_path,omitempty"`
	Summary        CacheStatisticsSummary               `json:"summary"`
	ByModel        []CacheStatisticsModelSummary        `json:"by_model"`
	ByDay          []CacheStatisticsDaySummary          `json:"by_day"`
	TrendByModel   map[string]CacheStatisticsModelTrend `json:"trend_by_model"`
	RecentRequests []CacheStatisticsRequest             `json:"recent_requests"`
}

type CacheStatisticsModelTrend struct {
	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

func (snapshot CacheStatisticsSnapshot) Redacted() CacheStatisticsSnapshot {
	snapshot.DBPath = ""
	for i := range snapshot.RecentRequests {
		snapshot.RecentRequests[i].Source = ""
		snapshot.RecentRequests[i].APIKey = ""
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
	path       string
	db         *sql.DB
	driver     string
	schema     string
	backendKey string
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
	opts := currentPersistentStoreOptions()
	cacheStatisticsStoreMu.RLock()
	existing := cacheStatisticsStore
	cacheStatisticsStoreMu.RUnlock()

	if strings.TrimSpace(opts.PostgresDSN) != "" {
		backendKey := postgresCacheStatisticsBackendKey(opts.PostgresDSN, opts.PostgresSchema)
		if existing != nil && existing.backendKey == backendKey {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		store, err := OpenPostgresCacheStatisticsStore(ctx, PostgresCacheStatisticsStoreConfig{
			DSN:    opts.PostgresDSN,
			Schema: opts.PostgresSchema,
		})
		cancel()
		if err != nil {
			return err
		}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		for _, legacyPath := range resolveLegacyCacheStatisticsPaths(cfg, configFilePath, opts) {
			if err := store.ImportSQLiteFile(ctx, legacyPath); err != nil {
				cancel()
				_ = store.Close()
				return err
			}
		}
		cancel()

		cacheStatisticsStoreMu.Lock()
		old := cacheStatisticsStore
		cacheStatisticsStore = store
		cacheStatisticsStoreMu.Unlock()

		if old != nil {
			_ = old.Close()
		}
		return nil
	}

	if opts.RequirePostgres {
		return fmt.Errorf("cache statistics store: PGSTORE_DSN is required when usage statistics are enabled")
	}
	path, err := resolveCacheStatisticsDBPath(cfg, configFilePath)
	if err != nil {
		return err
	}
	if absPath, errAbs := filepath.Abs(path); errAbs == nil {
		path = absPath
	}
	if existing != nil && existing.backendKey == "sqlite:"+path {
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
	if absPath, errAbs := filepath.Abs(path); errAbs == nil {
		path = absPath
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
	store := &CacheStatisticsStore{path: path, db: db, driver: "sqlite", backendKey: "sqlite:" + path}
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
	if s.isPostgres() {
		return s.initPostgresSchema()
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS cache_statistics_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_key TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    api_key TEXT NOT NULL DEFAULT '',
    customer_id TEXT NOT NULL DEFAULT '',
    customer_email TEXT NOT NULL DEFAULT '',
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
    anthropic_cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
    messages_stream_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_requested_at ON cache_statistics_requests(requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_model ON cache_statistics_requests(model);
`)
	if err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "event_key", "ALTER TABLE cache_statistics_requests ADD COLUMN event_key TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "reasoning_effort", "ALTER TABLE cache_statistics_requests ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "api_key", "ALTER TABLE cache_statistics_requests ADD COLUMN api_key TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_statistics_api_key ON cache_statistics_requests(api_key)`); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "customer_id", "ALTER TABLE cache_statistics_requests ADD COLUMN customer_id TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_statistics_customer_id ON cache_statistics_requests(customer_id)`); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "customer_email", "ALTER TABLE cache_statistics_requests ADD COLUMN customer_email TEXT NOT NULL DEFAULT ''"); err != nil {
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
	if err := ensureCacheStatisticsColumn(s.db, "cache_statistics_requests", "messages_stream_id", "ALTER TABLE cache_statistics_requests ADD COLUMN messages_stream_id TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_statistics_stream_id ON cache_statistics_requests(messages_stream_id)`); err != nil {
		return fmt.Errorf("cache statistics store: init schema: %w", err)
	}
	if err := s.backfillCacheStatisticsEventKeys(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_cache_statistics_event_key ON cache_statistics_requests(event_key)`); err != nil {
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
	eventKey := buildCacheStatisticsEventKey(event)
	if s.isPostgres() {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (
    event_key, requested_at, provider, model, reasoning_effort, source, api_key, customer_id, customer_email, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens,
    messages_stream_id
) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
ON CONFLICT (event_key) DO NOTHING`,
			s.requestsTableName(),
			s.bind(1), s.bind(2), s.bind(3), s.bind(4), s.bind(5), s.bind(6), s.bind(7), s.bind(8), s.bind(9), s.bind(10), s.bind(11),
			s.bind(12), s.bind(13), s.bind(14), s.bind(15), s.bind(16), s.bind(17), s.bind(18), s.bind(19), s.bind(20), s.bind(21), s.bind(22),
			s.bind(23), s.bind(24), s.bind(25), s.bind(26), s.bind(27), s.bind(28), s.bind(29), s.bind(30)),
			eventKey,
			s.timestampArg(timestamp),
			strings.TrimSpace(event.Provider),
			strings.TrimSpace(event.Model),
			strings.TrimSpace(event.ReasoningEffort),
			strings.TrimSpace(event.Source),
			strings.TrimSpace(event.APIKey),
			strings.TrimSpace(event.CustomerID),
			strings.TrimSpace(event.CustomerEmail),
			strings.TrimSpace(event.AuthID),
			strings.TrimSpace(event.AuthIndex),
			normaliseNonNegative(event.LatencyMs),
			event.Failed,
			normaliseNonNegative(tokens.InputTokens),
			normaliseNonNegative(tokens.OutputTokens),
			normaliseNonNegative(tokens.ReasoningTokens),
			normaliseNonNegative(tokens.CachedTokens),
			normaliseNonNegative(tokens.TotalTokens),
			cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PromptCacheKey }),
			cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PreviousResponseID }),
			cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.ResponseID }),
			cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PromptCacheRetention }),
			event.AnthropicCache.RewriteApplied,
			event.AnthropicCache.OverwroteClientLayout,
			event.AnthropicCache.MatchedAgenticCodingLoop,
			strings.TrimSpace(event.AnthropicCache.TTL),
			anthropicBreakpointSummary(event.AnthropicCache),
			anthropicCacheCreationTokens(event.AnthropicCache),
			anthropicCacheReadTokens(event.AnthropicCache),
			strings.TrimSpace(event.StreamID),
		)
		if err != nil {
			return fmt.Errorf("cache statistics store: insert event: %w", err)
		}
		s.estimateAntigravityCacheCreation(ctx, event)
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO cache_statistics_requests (
    event_key, requested_at, provider, model, reasoning_effort, source, api_key, customer_id, customer_email, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens,
    messages_stream_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventKey,
		timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(event.Provider),
		strings.TrimSpace(event.Model),
		strings.TrimSpace(event.ReasoningEffort),
		strings.TrimSpace(event.Source),
		strings.TrimSpace(event.APIKey),
		strings.TrimSpace(event.CustomerID),
		strings.TrimSpace(event.CustomerEmail),
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
		strings.TrimSpace(event.StreamID),
	)
	if err != nil {
		return fmt.Errorf("cache statistics store: insert event: %w", err)
	}
	s.estimateAntigravityCacheCreation(ctx, event)
	return nil
}

func (s *CacheStatisticsStore) estimateAntigravityCacheCreation(ctx context.Context, event CacheStatisticsEvent) {
	if s == nil || s.db == nil {
		return
	}
	if !strings.Contains(strings.ToLower(event.Model), "claude") {
		return
	}
	if strings.ToLower(strings.TrimSpace(event.Provider)) != "antigravity" {
		return
	}
	streamID := strings.TrimSpace(event.StreamID)
	if streamID == "" {
		return
	}
	currentCacheRead := anthropicCacheReadTokens(event.AnthropicCache)
	if currentCacheRead <= 0 {
		return
	}
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	windowStart := timestamp.Add(-1 * time.Hour)

	var prevID int64
	var prevCacheRead int64
	if s.isPostgres() {
		err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, COALESCE(anthropic_cache_read_input_tokens, 0)
FROM %s
WHERE messages_stream_id = %s
  AND LOWER(model) LIKE '%%claude%%'
  AND provider = 'antigravity'
  AND requested_at < %s
  AND requested_at > %s
  AND anthropic_cache_creation_input_tokens = 0
ORDER BY requested_at DESC
LIMIT 1`, s.requestsTableName(), s.bind(1), s.bind(2), s.bind(3)),
			streamID, timestamp, windowStart,
		).Scan(&prevID, &prevCacheRead)
		if err != nil {
			return
		}
		estimated := currentCacheRead - prevCacheRead
		if estimated <= 0 {
			return
		}
		_, _ = s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET anthropic_cache_creation_input_tokens = %s WHERE id = %s`,
			s.requestsTableName(), s.bind(1), s.bind(2)), estimated, prevID)
	} else {
		err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, COALESCE(anthropic_cache_read_input_tokens, 0)
FROM %s
WHERE messages_stream_id = ?
  AND LOWER(model) LIKE '%%claude%%'
  AND provider = 'antigravity'
  AND requested_at < ?
  AND requested_at > ?
  AND anthropic_cache_creation_input_tokens = 0
ORDER BY requested_at DESC
LIMIT 1`, s.requestsTableName()),
			streamID,
			timestamp.UTC().Format(time.RFC3339Nano),
			windowStart.UTC().Format(time.RFC3339Nano),
		).Scan(&prevID, &prevCacheRead)
		if err != nil {
			return
		}
		estimated := currentCacheRead - prevCacheRead
		if estimated <= 0 {
			return
		}
		_, _ = s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET anthropic_cache_creation_input_tokens = ? WHERE id = ?`,
			s.requestsTableName()), estimated, prevID)
	}
}

func (s *CacheStatisticsStore) Snapshot(ctx context.Context, recentLimit, modelLimit, days int) (CacheStatisticsSnapshot, error) {
	return s.SnapshotByProvider(ctx, recentLimit, modelLimit, days, "")
}

func (s *CacheStatisticsStore) SnapshotByProviders(ctx context.Context, recentLimit, modelLimit, days int, providers []string) (CacheStatisticsSnapshot, error) {
	if days <= 0 {
		days = defaultCacheStatisticsDays
	}
	return s.snapshotSinceProviders(ctx, recentLimit, modelLimit, snapshotSince(days), providers)
}

func (s *CacheStatisticsStore) SnapshotByProvider(ctx context.Context, recentLimit, modelLimit, days int, provider string) (CacheStatisticsSnapshot, error) {
	return s.SnapshotByProviders(ctx, recentLimit, modelLimit, days, cacheStatisticsProvidersForFilter(provider))
}

func (s *CacheStatisticsStore) SnapshotSince(ctx context.Context, recentLimit, modelLimit int, since time.Time) (CacheStatisticsSnapshot, error) {
	return s.SnapshotSinceByProvider(ctx, recentLimit, modelLimit, since, "")
}

func (s *CacheStatisticsStore) SnapshotSinceByProviders(ctx context.Context, recentLimit, modelLimit int, since time.Time, providers []string) (CacheStatisticsSnapshot, error) {
	if since.IsZero() {
		return s.SnapshotByProviders(ctx, recentLimit, modelLimit, defaultCacheStatisticsDays, providers)
	}
	return s.snapshotSinceProviders(ctx, recentLimit, modelLimit, since.UTC().Format(time.RFC3339Nano), providers)
}

func (s *CacheStatisticsStore) SnapshotSinceByProvider(ctx context.Context, recentLimit, modelLimit int, since time.Time, provider string) (CacheStatisticsSnapshot, error) {
	return s.SnapshotSinceByProviders(ctx, recentLimit, modelLimit, since, cacheStatisticsProvidersForFilter(provider))
}

func (s *CacheStatisticsStore) snapshotSince(ctx context.Context, recentLimit, modelLimit int, since string, provider string) (CacheStatisticsSnapshot, error) {
	return s.snapshotSinceProviders(ctx, recentLimit, modelLimit, since, cacheStatisticsProvidersForFilter(provider))
}

func (s *CacheStatisticsStore) snapshotSinceProviders(ctx context.Context, recentLimit, modelLimit int, since string, providers []string) (CacheStatisticsSnapshot, error) {
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

	summary, err := s.querySummary(ctx, since, providers)
	if err != nil {
		return result, err
	}
	byModel, err := s.queryModelSummaries(ctx, modelLimit, since, providers)
	if err != nil {
		return result, err
	}
	summaryLongContext, modelLongContext, err := s.queryLongContextPricingUsage(ctx, since, providers)
	if err != nil {
		return result, err
	}
	gpt54LongContext, err := s.queryExactModelLongContextUsage(ctx, since, providers, "gpt-5.4")
	if err != nil {
		return result, err
	}
	summary.SuccessPercentage = cacheStatisticsPercentage(summary.SuccessRequests, summary.TotalRequests)
	summary.LongContextInputTokens = summaryLongContext.InputTokens
	summary.LongContextCachedTokens = summaryLongContext.CachedTokens
	summary.LongContextOutputTokens = summaryLongContext.OutputTokens
	for i := range byModel {
		item := &byModel[i]
		modelName := strings.TrimSpace(item.Model)
		if modelName == "" {
			modelName = "unknown"
		}
		usage := modelLongContext[modelName]
		item.LongContextInputTokens = usage.InputTokens
		item.LongContextCachedTokens = usage.CachedTokens
		item.LongContextOutputTokens = usage.OutputTokens
	}
	summary.GPT54 = cacheStatisticsGPT54Breakdown(byModel, gpt54LongContext)
	byDay, err := s.queryDaySummaries(ctx, since, providers)
	if err != nil {
		return result, err
	}
	trendByModel, err := s.queryModelTrends(ctx, since, providers)
	if err != nil {
		return result, err
	}
	recent, err := s.queryRecentRequests(ctx, recentLimit, since, providers)
	if err != nil {
		return result, err
	}
	result.Summary = summary
	result.ByModel = byModel
	result.ByDay = byDay
	result.TrendByModel = trendByModel
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

func (s *CacheStatisticsStore) StatisticsSnapshotByProviders(ctx context.Context, providers []string) (StatisticsSnapshot, error) {
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

	query := fmt.Sprintf(`
SELECT
    requested_at,
    provider,
    model,
    api_key,
    customer_id,
    customer_email,
    source,
    auth_id,
    auth_index,
    latency_ms,
    CASE WHEN failed THEN 1 ELSE 0 END,
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
FROM %s
WHERE 1 = 1`, s.requestsTableName())
	args := []any{}
	query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
	query += `
ORDER BY requested_at ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("cache statistics store: usage snapshot query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			requestedAt                  any
			provider                     string
			model                        string
			apiKey                       string
			customerID                   string
			customerEmail                string
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
			&apiKey,
			&customerID,
			&customerEmail,
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

		timestamp, err := scanCacheStatisticsTime(requestedAt)
		if err != nil {
			continue
		}
		model = strings.TrimSpace(model)
		if model == "" {
			model = "unknown"
		}
		bucketKey := statisticsBucketKey(customerID, apiKey, authID, authIndex, provider)
		tokens := normaliseTokenStats(TokenStats{
			InputTokens:     inputTokens,
			OutputTokens:    outputTokens,
			ReasoningTokens: reasoningTokens,
			CachedTokens:    cachedTokens,
			TotalTokens:     totalTokens,
		})
		detail := RequestDetail{
			Timestamp:     timestamp,
			Provider:      strings.TrimSpace(provider),
			CustomerID:    strings.TrimSpace(customerID),
			CustomerEmail: strings.TrimSpace(customerEmail),
			LatencyMs:     latencyMs,
			Source:        source,
			AuthIndex:     authIndex,
			Tokens:        tokens,
			Failed:        failedInt != 0,
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

		apiSnapshot := result.APIs[bucketKey]
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
		result.APIs[bucketKey] = apiSnapshot
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("cache statistics store: usage snapshot iterate: %w", err)
	}
	return result, nil
}

func (s *CacheStatisticsStore) StatisticsSnapshotByProvider(ctx context.Context, provider string) (StatisticsSnapshot, error) {
	return s.StatisticsSnapshotByProviders(ctx, cacheStatisticsProvidersForFilter(provider))
}

func (s *CacheStatisticsStore) querySummary(ctx context.Context, since string, providers []string) (CacheStatisticsSummary, error) {
	var summary CacheStatisticsSummary
	query := fmt.Sprintf(`
SELECT
    COUNT(*),
    COALESCE(SUM(CASE WHEN NOT failed THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(CASE
        WHEN LOWER(provider) = 'claude' OR LOWER(model) LIKE '%%claude%%' THEN input_tokens + anthropic_cache_creation_input_tokens +
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
    COALESCE(AVG(latency_ms), 0),
    COALESCE(SUM(CASE WHEN anthropic_cache_ttl IN ('', '5m') AND anthropic_cache_creation_input_tokens > 0 THEN anthropic_cache_creation_input_tokens ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN anthropic_cache_ttl = '1h' THEN anthropic_cache_creation_input_tokens ELSE 0 END), 0)
FROM %s
WHERE requested_at >= %s`, s.requestsTableName(), s.bind(1))
	args := []any{s.sinceArg(since)}
	query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
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
		&summary.AnthropicCacheWrite5mTokens,
		&summary.AnthropicCacheWrite1hTokens,
	)
	if err != nil {
		return summary, fmt.Errorf("cache statistics store: query summary: %w", err)
	}
	summary.CacheRatio = ratio(summary.CachedTokens, cacheStatisticsRatioDenominator(summary.EffectiveInputTokens, summary.InputTokens))
	return summary, nil
}

type cacheStatisticsLongContextUsage struct {
	RequestCount int64
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
}

type cacheStatisticsLongContextRequest struct {
	ID                 int64
	Model              string
	InputTokens        int64
	CachedTokens       int64
	OutputTokens       int64
	PromptCacheKey     string
	PreviousResponseID string
	ResponseID         string
}

func (s *CacheStatisticsStore) queryModelSummaries(ctx context.Context, limit int, since string, providers []string) ([]CacheStatisticsModelSummary, error) {
	query := fmt.Sprintf(`
SELECT
    model,
    COUNT(*),
    COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(CASE
        WHEN LOWER(provider) = 'claude' OR LOWER(model) LIKE '%%claude%%' THEN input_tokens + anthropic_cache_creation_input_tokens +
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
    COALESCE(AVG(latency_ms), 0),
    COALESCE(SUM(CASE WHEN anthropic_cache_ttl IN ('', '5m') AND anthropic_cache_creation_input_tokens > 0 THEN anthropic_cache_creation_input_tokens ELSE 0 END), 0),
    COALESCE(SUM(CASE WHEN anthropic_cache_ttl = '1h' THEN anthropic_cache_creation_input_tokens ELSE 0 END), 0)
FROM %s
WHERE requested_at >= %s`, s.requestsTableName(), s.bind(1))
	args := []any{s.sinceArg(since)}
	query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
	query += `
GROUP BY model
ORDER BY COUNT(*) DESC, model ASC
LIMIT ` + s.bind(len(args)+1)
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
			&item.AnthropicCacheWrite5mTokens,
			&item.AnthropicCacheWrite1hTokens,
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

func (s *CacheStatisticsStore) queryLongContextPricingUsage(ctx context.Context, since string, providers []string) (cacheStatisticsLongContextUsage, map[string]cacheStatisticsLongContextUsage, error) {
	return s.queryLongContextUsage(ctx, since, providers, supportsOpenAILongContextPricing, nil)
}

func (s *CacheStatisticsStore) queryExactModelLongContextUsage(ctx context.Context, since string, providers []string, model string) (cacheStatisticsLongContextUsage, error) {
	modelMatcher := cacheStatisticsExactModelMatcher(model)
	if modelMatcher == nil {
		return cacheStatisticsLongContextUsage{}, nil
	}
	summary, _, err := s.queryLongContextUsage(ctx, since, providers, modelMatcher, modelMatcher)
	return summary, err
}

func (s *CacheStatisticsStore) queryLongContextUsage(
	ctx context.Context,
	since string,
	providers []string,
	qualifies func(string) bool,
	include func(string) bool,
) (cacheStatisticsLongContextUsage, map[string]cacheStatisticsLongContextUsage, error) {
	summary := cacheStatisticsLongContextUsage{}
	byModel := make(map[string]cacheStatisticsLongContextUsage)
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.db == nil {
		return summary, byModel, nil
	}

	query := fmt.Sprintf(`
SELECT
    id,
    model,
    input_tokens,
    cached_tokens,
    output_tokens,
    prompt_cache_key,
    previous_response_id,
    response_id
FROM %s
WHERE requested_at >= %s`, s.requestsTableName(), s.bind(1))
	args := []any{s.sinceArg(since)}
	query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
	query += `
ORDER BY requested_at ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return summary, nil, fmt.Errorf("cache statistics store: query long-context pricing usage: %w", err)
	}
	defer rows.Close()

	responseToSession := make(map[string]string)
	sessionRequests := make(map[string][]cacheStatisticsLongContextRequest)
	sessionLongContext := make(map[string]bool)

	for rows.Next() {
		var item cacheStatisticsLongContextRequest
		if err := rows.Scan(
			&item.ID,
			&item.Model,
			&item.InputTokens,
			&item.CachedTokens,
			&item.OutputTokens,
			&item.PromptCacheKey,
			&item.PreviousResponseID,
			&item.ResponseID,
		); err != nil {
			return summary, nil, fmt.Errorf("cache statistics store: scan long-context pricing usage: %w", err)
		}

		item.Model = strings.TrimSpace(item.Model)
		if item.Model == "" {
			item.Model = "unknown"
		}

		sessionKey := resolveLongContextPricingSessionKey(item, responseToSession)
		sessionRequests[sessionKey] = append(sessionRequests[sessionKey], item)
		if qualifies != nil && qualifies(item.Model) && item.InputTokens > openAILongContextInputThreshold {
			sessionLongContext[sessionKey] = true
		}
		if responseID := strings.TrimSpace(item.ResponseID); responseID != "" {
			responseToSession[responseID] = sessionKey
		}
	}
	if err := rows.Err(); err != nil {
		return summary, nil, fmt.Errorf("cache statistics store: iterate long-context pricing usage: %w", err)
	}

	for sessionKey, items := range sessionRequests {
		if !sessionLongContext[sessionKey] {
			continue
		}
		for i := range items {
			item := items[i]
			if include != nil && !include(item.Model) {
				continue
			}
			itemInputTokens := normaliseNonNegative(item.InputTokens)
			itemCachedTokens := normaliseNonNegative(item.CachedTokens)
			itemOutputTokens := normaliseNonNegative(item.OutputTokens)

			summary.RequestCount++
			summary.InputTokens += itemInputTokens
			summary.CachedTokens += itemCachedTokens
			summary.OutputTokens += itemOutputTokens

			modelUsage := byModel[item.Model]
			modelUsage.RequestCount++
			modelUsage.InputTokens += itemInputTokens
			modelUsage.CachedTokens += itemCachedTokens
			modelUsage.OutputTokens += itemOutputTokens
			byModel[item.Model] = modelUsage
		}
	}

	return summary, byModel, nil
}

func resolveLongContextPricingSessionKey(item cacheStatisticsLongContextRequest, responseToSession map[string]string) string {
	if promptCacheKey := strings.TrimSpace(item.PromptCacheKey); promptCacheKey != "" {
		return "prompt-cache:" + promptCacheKey
	}
	if previousResponseID := strings.TrimSpace(item.PreviousResponseID); previousResponseID != "" {
		if sessionKey := strings.TrimSpace(responseToSession[previousResponseID]); sessionKey != "" {
			return sessionKey
		}
		return "response-chain:" + previousResponseID
	}
	if responseID := strings.TrimSpace(item.ResponseID); responseID != "" {
		if sessionKey := strings.TrimSpace(responseToSession[responseID]); sessionKey != "" {
			return sessionKey
		}
		return "response-chain:" + responseID
	}
	return fmt.Sprintf("request:%d", item.ID)
}

func supportsOpenAILongContextPricing(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "gpt-5.4" || model == "gpt-5.4-pro"
}

func cacheStatisticsExactModelMatcher(model string) func(string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	return func(candidate string) bool {
		return strings.ToLower(strings.TrimSpace(candidate)) == model
	}
}

func cacheStatisticsPercentage(numerator, denominator int64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return (float64(numerator) / float64(denominator)) * 100
}

func cacheStatisticsGPT54Breakdown(models []CacheStatisticsModelSummary, longContext cacheStatisticsLongContextUsage) CacheStatisticsModelBreakdown {
	var total CacheStatisticsModelSummary
	for i := range models {
		if strings.EqualFold(strings.TrimSpace(models[i].Model), "gpt-5.4") {
			total = models[i]
			break
		}
	}

	return CacheStatisticsModelBreakdown{
		Standard: CacheStatisticsBreakdownBucket{
			RequestCount: normaliseNonNegative(total.Requests - longContext.RequestCount),
			InputTokens:  normaliseNonNegative(total.InputTokens - longContext.InputTokens),
			OutputTokens: normaliseNonNegative(total.OutputTokens - longContext.OutputTokens),
		},
		LongContext: CacheStatisticsBreakdownBucket{
			RequestCount: normaliseNonNegative(longContext.RequestCount),
			InputTokens:  normaliseNonNegative(longContext.InputTokens),
			OutputTokens: normaliseNonNegative(longContext.OutputTokens),
		},
	}
}

func (s *CacheStatisticsStore) queryDaySummaries(ctx context.Context, since string, providers []string) ([]CacheStatisticsDaySummary, error) {
	dayExpr := "substr(requested_at, 1, 10)"
	if s.isPostgres() {
		dayExpr = "to_char(requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
	}
	query := fmt.Sprintf(`
SELECT
    %s AS day,
    COUNT(*),
    COALESCE(SUM(input_tokens), 0),
    COALESCE(SUM(CASE
        WHEN LOWER(provider) = 'claude' OR LOWER(model) LIKE '%%claude%%' THEN input_tokens + anthropic_cache_creation_input_tokens +
            CASE
                WHEN anthropic_cache_read_input_tokens > 0 THEN anthropic_cache_read_input_tokens
                ELSE cached_tokens
            END
        ELSE input_tokens
    END), 0),
    COALESCE(SUM(cached_tokens), 0),
    COALESCE(SUM(total_tokens), 0)
FROM %s
WHERE requested_at >= %s`, dayExpr, s.requestsTableName(), s.bind(1))
	args := []any{s.sinceArg(since)}
	query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
	query += `
GROUP BY day
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

func (s *CacheStatisticsStore) queryModelTrends(ctx context.Context, since string, providers []string) (map[string]CacheStatisticsModelTrend, error) {
	result := make(map[string]CacheStatisticsModelTrend)
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.db == nil {
		return result, nil
	}

	dayExpr := "substr(requested_at, 1, 10)"
	hourExpr := "strftime('%Y-%m-%dT%H:00:00Z', requested_at)"
	if s.isPostgres() {
		dayExpr = "to_char(requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
		hourExpr = "to_char(date_trunc('hour', requested_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD\"T\"HH24:00:00\"Z\"')"
	}

	loadBuckets := func(bucketExpr string, assign func(*CacheStatisticsModelTrend, string, int64, int64)) error {
		query := fmt.Sprintf(`
SELECT
    model,
    %s AS bucket,
    COUNT(*),
    COALESCE(SUM(total_tokens), 0)
FROM %s
WHERE requested_at >= %s`, bucketExpr, s.requestsTableName(), s.bind(1))
		args := []any{s.sinceArg(since)}
		query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
		query += `
GROUP BY model, bucket
ORDER BY model ASC, bucket ASC`
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				model       string
				bucket      string
				requests    int64
				totalTokens int64
			)
			if err := rows.Scan(&model, &bucket, &requests, &totalTokens); err != nil {
				return err
			}
			model = strings.TrimSpace(model)
			if model == "" {
				model = "unknown"
			}
			trend := result[model]
			if trend.RequestsByDay == nil {
				trend.RequestsByDay = make(map[string]int64)
			}
			if trend.RequestsByHour == nil {
				trend.RequestsByHour = make(map[string]int64)
			}
			if trend.TokensByDay == nil {
				trend.TokensByDay = make(map[string]int64)
			}
			if trend.TokensByHour == nil {
				trend.TokensByHour = make(map[string]int64)
			}
			assign(&trend, bucket, requests, totalTokens)
			result[model] = trend
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return nil
	}

	if err := loadBuckets(dayExpr, func(trend *CacheStatisticsModelTrend, bucket string, requests int64, totalTokens int64) {
		trend.RequestsByDay[bucket] = requests
		trend.TokensByDay[bucket] = totalTokens
	}); err != nil {
		return nil, fmt.Errorf("cache statistics store: query model day trends: %w", err)
	}
	if err := loadBuckets(hourExpr, func(trend *CacheStatisticsModelTrend, bucket string, requests int64, totalTokens int64) {
		trend.RequestsByHour[bucket] = requests
		trend.TokensByHour[bucket] = totalTokens
	}); err != nil {
		return nil, fmt.Errorf("cache statistics store: query model hour trends: %w", err)
	}

	return result, nil
}

func (s *CacheStatisticsStore) queryRecentRequests(ctx context.Context, limit int, since string, providers []string) ([]CacheStatisticsRequest, error) {
	query := fmt.Sprintf(`
SELECT
    id, requested_at, provider, model, reasoning_effort, source, api_key, customer_id, customer_email, auth_id, auth_index, latency_ms, CASE WHEN failed THEN 1 ELSE 0 END,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens
FROM %s
WHERE requested_at >= %s`, s.requestsTableName(), s.bind(1))
	args := []any{s.sinceArg(since)}
	query, args = appendCacheStatisticsProvidersFilter(query, args, providers)
	query += `
ORDER BY requested_at DESC, id DESC
LIMIT ` + s.bind(len(args)+1)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: query recent requests: %w", err)
	}
	defer rows.Close()
	result := make([]CacheStatisticsRequest, 0)
	for rows.Next() {
		var item CacheStatisticsRequest
		var requestedAt any
		var failedInt int
		if err := rows.Scan(
			&item.ID,
			&requestedAt,
			&item.Provider,
			&item.Model,
			&item.ReasoningEffort,
			&item.Source,
			&item.APIKey,
			&item.CustomerID,
			&item.CustomerEmail,
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
		if ts, errParse := scanCacheStatisticsTime(requestedAt); errParse == nil {
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
	return appendCacheStatisticsProvidersFilter(query, args, cacheStatisticsProvidersForFilter(provider))
}

func appendCacheStatisticsProvidersFilter(query string, args []any, providers []string) (string, []any) {
	providers = uniqueProviderNames(providers)
	if len(providers) == 0 {
		return query, args
	}
	placeholders := make([]string, 0, len(providers))
	usePostgresBinds := strings.Contains(query, "$1")
	for _, item := range providers {
		if usePostgresBinds {
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)+1))
		} else {
			placeholders = append(placeholders, "?")
		}
		args = append(args, item)
	}
	query += " AND LOWER(provider) IN (" + strings.Join(placeholders, ",") + ")"
	return query, args
}

func cacheStatisticsProvidersForFilter(provider string) []string {
	return ProviderNamesForFilter(provider, nil)
}

func normalizeProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func uniqueProviderNames(names ...[]string) []string {
	total := 0
	for _, group := range names {
		total += len(group)
	}
	if total == 0 {
		return nil
	}
	result := make([]string, 0, total)
	seen := make(map[string]struct{}, total)
	for _, group := range names {
		for _, name := range group {
			normalized := normalizeProviderName(name)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			result = append(result, normalized)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func ProviderNamesForFilter(provider string, openAICompatProviders []string) []string {
	switch normalizeProviderName(provider) {
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
		return uniqueProviderNames([]string{"openai-compatibility", "openrouter"}, openAICompatProviders)
	default:
		return uniqueProviderNames([]string{provider})
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

type cacheStatisticsEventKeyBackfillRow struct {
	ID                                int64
	EventKey                          string
	RequestedAt                       any
	Provider                          string
	Model                             string
	ReasoningEffort                   string
	Source                            string
	APIKey                            string
	CustomerID                        string
	CustomerEmail                     string
	AuthID                            string
	AuthIndex                         string
	LatencyMs                         int64
	Failed                            int
	InputTokens                       int64
	OutputTokens                      int64
	ReasoningTokens                   int64
	CachedTokens                      int64
	TotalTokens                       int64
	PromptCacheKey                    string
	PreviousResponseID                string
	ResponseID                        string
	PromptCacheRetention              string
	AnthropicRewriteApplied           int
	AnthropicOverwroteClientLayout    int
	AnthropicMatchedAgenticLoop       int
	AnthropicCacheTTL                 string
	AnthropicBreakpoints              string
	AnthropicCacheCreationInputTokens int64
	AnthropicCacheReadInputTokens     int64
}

func (r cacheStatisticsEventKeyBackfillRow) toEvent() (CacheStatisticsEvent, error) {
	timestamp, err := scanCacheStatisticsTime(r.RequestedAt)
	if err != nil {
		return CacheStatisticsEvent{}, err
	}
	event := CacheStatisticsEvent{
		Timestamp:       timestamp,
		Provider:        r.Provider,
		Model:           r.Model,
		ReasoningEffort: r.ReasoningEffort,
		Source:          r.Source,
		APIKey:          r.APIKey,
		CustomerID:      r.CustomerID,
		CustomerEmail:   r.CustomerEmail,
		AuthID:          r.AuthID,
		AuthIndex:       r.AuthIndex,
		LatencyMs:       r.LatencyMs,
		Failed:          r.Failed != 0,
		Tokens: TokenStats{
			InputTokens:     r.InputTokens,
			OutputTokens:    r.OutputTokens,
			ReasoningTokens: r.ReasoningTokens,
			CachedTokens:    r.CachedTokens,
			TotalTokens:     r.TotalTokens,
		},
	}
	if r.PromptCacheKey != "" || r.PreviousResponseID != "" || r.ResponseID != "" || r.PromptCacheRetention != "" {
		event.Cache = &helps.CodexCacheObservability{
			PromptCacheKey:       r.PromptCacheKey,
			PreviousResponseID:   r.PreviousResponseID,
			ResponseID:           r.ResponseID,
			PromptCacheRetention: r.PromptCacheRetention,
		}
	}
	if r.AnthropicRewriteApplied != 0 || r.AnthropicOverwroteClientLayout != 0 || r.AnthropicMatchedAgenticLoop != 0 || r.AnthropicCacheTTL != "" || r.AnthropicBreakpoints != "" || r.AnthropicCacheCreationInputTokens != 0 || r.AnthropicCacheReadInputTokens != 0 {
		event.AnthropicCache = helps.AnthropicCacheObservability{
			RewriteApplied:           r.AnthropicRewriteApplied != 0,
			OverwroteClientLayout:    r.AnthropicOverwroteClientLayout != 0,
			MatchedAgenticCodingLoop: r.AnthropicMatchedAgenticLoop != 0,
			TTL:                      r.AnthropicCacheTTL,
			CacheCreationInputTokens: r.AnthropicCacheCreationInputTokens,
			CacheReadInputTokens:     r.AnthropicCacheReadInputTokens,
		}
	}
	return event, nil
}

func (s *CacheStatisticsStore) backfillCacheStatisticsEventKeys() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("cache statistics store: not initialized")
	}
	rows, err := s.db.Query(`
SELECT
    id,
    event_key,
    requested_at,
    provider,
    model,
    reasoning_effort,
    source,
    api_key,
    customer_id,
    customer_email,
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
    anthropic_rewrite_applied,
    anthropic_overwrote_client_layout,
    anthropic_matched_agentic_loop,
    anthropic_cache_ttl,
    anthropic_breakpoints,
    anthropic_cache_creation_input_tokens,
    anthropic_cache_read_input_tokens
FROM cache_statistics_requests
ORDER BY id ASC`)
	if err != nil {
		return fmt.Errorf("cache statistics store: backfill event keys query: %w", err)
	}
	defer rows.Close()

	entries := make([]cacheStatisticsEventKeyBackfillRow, 0, 128)
	for rows.Next() {
		var entry cacheStatisticsEventKeyBackfillRow
		if err := rows.Scan(
			&entry.ID,
			&entry.EventKey,
			&entry.RequestedAt,
			&entry.Provider,
			&entry.Model,
			&entry.ReasoningEffort,
			&entry.Source,
			&entry.APIKey,
			&entry.CustomerID,
			&entry.CustomerEmail,
			&entry.AuthID,
			&entry.AuthIndex,
			&entry.LatencyMs,
			&entry.Failed,
			&entry.InputTokens,
			&entry.OutputTokens,
			&entry.ReasoningTokens,
			&entry.CachedTokens,
			&entry.TotalTokens,
			&entry.PromptCacheKey,
			&entry.PreviousResponseID,
			&entry.ResponseID,
			&entry.PromptCacheRetention,
			&entry.AnthropicRewriteApplied,
			&entry.AnthropicOverwroteClientLayout,
			&entry.AnthropicMatchedAgenticLoop,
			&entry.AnthropicCacheTTL,
			&entry.AnthropicBreakpoints,
			&entry.AnthropicCacheCreationInputTokens,
			&entry.AnthropicCacheReadInputTokens,
		); err != nil {
			return fmt.Errorf("cache statistics store: backfill event keys scan: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cache statistics store: backfill event keys iterate: %w", err)
	}

	seen := make(map[string]struct{}, len(entries))
	updates := make([]struct {
		id       int64
		eventKey string
	}, 0, len(entries))
	for _, entry := range entries {
		event, err := entry.toEvent()
		if err != nil {
			return fmt.Errorf("cache statistics store: backfill event keys build event: %w", err)
		}
		canonical := buildCacheStatisticsEventKey(event)
		current := strings.TrimSpace(entry.EventKey)
		nextKey := canonical
		if nextKey == "" || hasSeenEventKey(seen, nextKey) {
			nextKey = fmt.Sprintf("%s|%d", canonical, entry.ID)
		}
		if nextKey == "" {
			nextKey = fmt.Sprintf("%d", entry.ID)
		}
		for hasSeenEventKey(seen, nextKey) {
			nextKey = fmt.Sprintf("%s|%d", canonical, entry.ID)
		}
		seen[nextKey] = struct{}{}
		if nextKey != current {
			updates = append(updates, struct {
				id       int64
				eventKey string
			}{id: entry.ID, eventKey: nextKey})
		}
	}

	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("cache statistics store: backfill event keys begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, update := range updates {
		if _, err := tx.Exec(`UPDATE cache_statistics_requests SET event_key = ? WHERE id = ?`, update.eventKey, update.id); err != nil {
			return fmt.Errorf("cache statistics store: backfill event keys update: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cache statistics store: backfill event keys commit: %w", err)
	}
	return nil
}

func hasSeenEventKey(seen map[string]struct{}, key string) bool {
	_, ok := seen[key]
	return ok
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

func scanCacheStatisticsTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		return parseCacheStatisticsTime(typed)
	case []byte:
		return parseCacheStatisticsTime(string(typed))
	default:
		return time.Time{}, fmt.Errorf("unsupported time value type %T", value)
	}
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
