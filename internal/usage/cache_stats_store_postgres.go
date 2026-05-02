package usage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

type PersistentStoreOptions struct {
	PostgresDSN       string
	PostgresSchema    string
	PostgresLocalPath string
	RequirePostgres   bool
}

type PostgresCacheStatisticsStoreConfig struct {
	DSN    string
	Schema string
}

var persistentStoreOptions PersistentStoreOptions

func SetPersistentStoreOptions(opts PersistentStoreOptions) {
	localPath := strings.TrimSpace(opts.PostgresLocalPath)
	if resolved, err := util.ResolveAuthDir(localPath); err == nil && resolved != "" {
		localPath = resolved
	}
	cacheStatisticsStoreMu.Lock()
	defer cacheStatisticsStoreMu.Unlock()
	persistentStoreOptions = PersistentStoreOptions{
		PostgresDSN:       strings.TrimSpace(opts.PostgresDSN),
		PostgresSchema:    strings.TrimSpace(opts.PostgresSchema),
		PostgresLocalPath: localPath,
		RequirePostgres:   opts.RequirePostgres,
	}
}

func currentPersistentStoreOptions() PersistentStoreOptions {
	cacheStatisticsStoreMu.RLock()
	defer cacheStatisticsStoreMu.RUnlock()
	return persistentStoreOptions
}

func OpenPostgresCacheStatisticsStore(ctx context.Context, cfg PostgresCacheStatisticsStoreConfig) (*CacheStatisticsStore, error) {
	cfg.DSN = strings.TrimSpace(cfg.DSN)
	cfg.Schema = strings.TrimSpace(cfg.Schema)
	if cfg.DSN == "" {
		return nil, fmt.Errorf("cache statistics store: postgres DSN is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: open postgres database: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cache statistics store: ping postgres database: %w", err)
	}
	store := &CacheStatisticsStore{
		path:       "postgres",
		db:         db,
		driver:     "postgres",
		schema:     cfg.Schema,
		backendKey: postgresCacheStatisticsBackendKey(cfg.DSN, cfg.Schema),
	}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func postgresCacheStatisticsBackendKey(dsn, schema string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(dsn)))
	return "postgres:" + hex.EncodeToString(sum[:]) + ":" + strings.TrimSpace(schema)
}

func (s *CacheStatisticsStore) isPostgres() bool {
	return s != nil && s.driver == "postgres"
}

func (s *CacheStatisticsStore) bind(index int) string {
	if s != nil && s.isPostgres() {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func (s *CacheStatisticsStore) fullTableName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if s == nil || !s.isPostgres() {
		return name
	}
	if strings.TrimSpace(s.schema) == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(s.schema) + "." + quoteIdentifier(name)
}

func quoteIdentifier(identifier string) string {
	replaced := strings.ReplaceAll(identifier, `"`, `""`)
	return `"` + replaced + `"`
}

func (s *CacheStatisticsStore) requestsTableName() string {
	return s.fullTableName("cache_statistics_requests")
}

func (s *CacheStatisticsStore) promptCacheTableName() string {
	return s.fullTableName("prompt_cache_response_index")
}

func (s *CacheStatisticsStore) sinceArg(since string) any {
	if s == nil || !s.isPostgres() {
		return since
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(since))
	if err == nil {
		return parsed.UTC()
	}
	parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(since))
	if err == nil {
		return parsed.UTC()
	}
	return time.Now().UTC().AddDate(0, 0, -defaultCacheStatisticsDays)
}

func (s *CacheStatisticsStore) timestampArg(ts time.Time) any {
	if s == nil || !s.isPostgres() {
		return ts.UTC().Format(time.RFC3339Nano)
	}
	return ts.UTC()
}

func (s *CacheStatisticsStore) initPostgresSchema() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("cache statistics store: not initialized")
	}
	if schema := strings.TrimSpace(s.schema); schema != "" {
		if _, err := s.db.Exec(`CREATE SCHEMA IF NOT EXISTS ` + quoteIdentifier(schema)); err != nil {
			return fmt.Errorf("cache statistics store: create postgres schema: %w", err)
		}
	}
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS ` + s.requestsTableName() + ` (
    id BIGSERIAL PRIMARY KEY,
    event_key TEXT NOT NULL UNIQUE,
    requested_at TIMESTAMPTZ NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    api_key TEXT NOT NULL DEFAULT '',
    customer_id TEXT NOT NULL DEFAULT '',
    customer_email TEXT NOT NULL DEFAULT '',
    auth_id TEXT NOT NULL DEFAULT '',
    auth_index TEXT NOT NULL DEFAULT '',
    latency_ms BIGINT NOT NULL DEFAULT 0,
    failed BOOLEAN NOT NULL DEFAULT FALSE,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens BIGINT NOT NULL DEFAULT 0,
    cached_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    prompt_cache_key TEXT NOT NULL DEFAULT '',
    previous_response_id TEXT NOT NULL DEFAULT '',
    response_id TEXT NOT NULL DEFAULT '',
    prompt_cache_retention TEXT NOT NULL DEFAULT '',
    anthropic_rewrite_applied BOOLEAN NOT NULL DEFAULT FALSE,
    anthropic_overwrote_client_layout BOOLEAN NOT NULL DEFAULT FALSE,
    anthropic_matched_agentic_loop BOOLEAN NOT NULL DEFAULT FALSE,
    anthropic_cache_ttl TEXT NOT NULL DEFAULT '',
    anthropic_breakpoints TEXT NOT NULL DEFAULT '',
    anthropic_cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
    anthropic_cache_read_input_tokens BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_requested_at ON ` + s.requestsTableName() + ` (requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_provider ON ` + s.requestsTableName() + ` (provider);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_model ON ` + s.requestsTableName() + ` (model);
CREATE INDEX IF NOT EXISTS idx_cache_statistics_api_key ON ` + s.requestsTableName() + ` (api_key);
`); err != nil {
		return fmt.Errorf("cache statistics store: init postgres request schema: %w", err)
	}
	if err := ensurePostgresColumn(s.db, s.requestsTableName(), "customer_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init postgres request schema: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_statistics_customer_id ON ` + s.requestsTableName() + ` (customer_id)`); err != nil {
		return fmt.Errorf("cache statistics store: init postgres customer index: %w", err)
	}
	if err := ensurePostgresColumn(s.db, s.requestsTableName(), "customer_email", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init postgres request schema: %w", err)
	}
	if err := ensurePostgresColumn(s.db, s.requestsTableName(), "messages_stream_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("cache statistics store: init postgres request schema: %w", err)
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_cache_statistics_stream_id ON ` + s.requestsTableName() + ` (messages_stream_id)`); err != nil {
		return fmt.Errorf("cache statistics store: init postgres stream_id index: %w", err)
	}
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS ` + s.promptCacheTableName() + ` (
    response_id TEXT PRIMARY KEY,
    prompt_cache_key TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_prompt_cache_response_index_expires_at ON ` + s.promptCacheTableName() + ` (expires_at);
`); err != nil {
		return fmt.Errorf("cache statistics store: init postgres prompt-cache schema: %w", err)
	}
	return nil
}

func buildCacheStatisticsEventKey(event CacheStatisticsEvent) string {
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	tokens := normaliseTokenStats(event.Tokens)
	cache := event.Cache
	parts := []string{
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
		fmt.Sprintf("%d", normaliseNonNegative(event.LatencyMs)),
		fmt.Sprintf("%t", event.Failed),
		fmt.Sprintf("%d", normaliseNonNegative(tokens.InputTokens)),
		fmt.Sprintf("%d", normaliseNonNegative(tokens.OutputTokens)),
		fmt.Sprintf("%d", normaliseNonNegative(tokens.ReasoningTokens)),
		fmt.Sprintf("%d", normaliseNonNegative(tokens.CachedTokens)),
		fmt.Sprintf("%d", normaliseNonNegative(tokens.TotalTokens)),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PromptCacheKey }),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PreviousResponseID }),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.ResponseID }),
		cacheString(cache, func(v *helps.CodexCacheObservability) string { return v.PromptCacheRetention }),
		fmt.Sprintf("%t", event.AnthropicCache.RewriteApplied),
		fmt.Sprintf("%t", event.AnthropicCache.OverwroteClientLayout),
		fmt.Sprintf("%t", event.AnthropicCache.MatchedAgenticCodingLoop),
		strings.TrimSpace(event.AnthropicCache.TTL),
		anthropicBreakpointSummary(event.AnthropicCache),
		fmt.Sprintf("%d", anthropicCacheCreationTokens(event.AnthropicCache)),
		fmt.Sprintf("%d", anthropicCacheReadTokens(event.AnthropicCache)),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func resolveLegacyCacheStatisticsPaths(cfg *config.Config, configFilePath string, opts PersistentStoreOptions) []string {
	paths := make([]string, 0, 2)
	seen := make(map[string]struct{})
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if path, err := resolveCacheStatisticsDBPath(cfg, configFilePath); err == nil {
		add(path)
	}
	if localRoot := strings.TrimSpace(opts.PostgresLocalPath); localRoot != "" {
		add(filepath.Join(localRoot, "stats", "cache-statistics.sqlite"))
	}
	return paths
}

func (s *CacheStatisticsStore) ImportSQLiteFile(ctx context.Context, path string) error {
	if s == nil || s.db == nil || !s.isPostgres() {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cache statistics store: inspect legacy sqlite database: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("cache statistics store: open legacy sqlite database: %w", err)
	}
	defer func() { _ = legacyDB.Close() }()
	if err := s.importSQLiteRequestRows(ctx, legacyDB); err != nil {
		return err
	}
	if err := s.importSQLitePromptCacheRows(ctx, legacyDB); err != nil {
		return err
	}
	return nil
}

func (s *CacheStatisticsStore) importSQLiteRequestRows(ctx context.Context, legacyDB *sql.DB) error {
	exists, err := sqliteTableExists(legacyDB, "cache_statistics_requests")
	if err != nil || !exists {
		return err
	}
	columns, err := sqliteColumnSet(legacyDB, "cache_statistics_requests")
	if err != nil {
		return err
	}
	selects := []string{
		sqliteSelectExpr(columns, "requested_at", "''"),
		sqliteSelectExpr(columns, "provider", "''"),
		sqliteSelectExpr(columns, "model", "''"),
		sqliteSelectExpr(columns, "reasoning_effort", "''"),
		sqliteSelectExpr(columns, "source", "''"),
		sqliteSelectExpr(columns, "api_key", "''"),
		sqliteSelectExpr(columns, "customer_id", "''"),
		sqliteSelectExpr(columns, "customer_email", "''"),
		sqliteSelectExpr(columns, "auth_id", "''"),
		sqliteSelectExpr(columns, "auth_index", "''"),
		sqliteSelectExpr(columns, "latency_ms", "0"),
		sqliteSelectExpr(columns, "failed", "0"),
		sqliteSelectExpr(columns, "input_tokens", "0"),
		sqliteSelectExpr(columns, "output_tokens", "0"),
		sqliteSelectExpr(columns, "reasoning_tokens", "0"),
		sqliteSelectExpr(columns, "cached_tokens", "0"),
		sqliteSelectExpr(columns, "total_tokens", "0"),
		sqliteSelectExpr(columns, "prompt_cache_key", "''"),
		sqliteSelectExpr(columns, "previous_response_id", "''"),
		sqliteSelectExpr(columns, "response_id", "''"),
		sqliteSelectExpr(columns, "prompt_cache_retention", "''"),
		sqliteSelectExpr(columns, "anthropic_rewrite_applied", "0"),
		sqliteSelectExpr(columns, "anthropic_overwrote_client_layout", "0"),
		sqliteSelectExpr(columns, "anthropic_matched_agentic_loop", "0"),
		sqliteSelectExpr(columns, "anthropic_cache_ttl", "''"),
		sqliteSelectExpr(columns, "anthropic_breakpoints", "''"),
		sqliteSelectExpr(columns, "anthropic_cache_creation_input_tokens", "0"),
		sqliteSelectExpr(columns, "anthropic_cache_read_input_tokens", "0"),
	}
	query := `SELECT ` + strings.Join(selects, ", ") + ` FROM cache_statistics_requests ORDER BY requested_at ASC, id ASC`
	rows, err := legacyDB.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("cache statistics store: query legacy sqlite requests: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			requestedAt                  string
			provider                     string
			model                        string
			reasoningEffort              string
			source                       string
			apiKey                       string
			customerID                   string
			customerEmail                string
			authID                       string
			authIndex                    string
			latencyMs                    int64
			failed                       int
			inputTokens                  int64
			outputTokens                 int64
			reasoningTokens              int64
			cachedTokens                 int64
			totalTokens                  int64
			promptCacheKey               string
			previousResponseID           string
			responseID                   string
			promptCacheRetention         string
			anthropicRewriteApplied      int
			anthropicOverwroteLayout     int
			anthropicMatchedAgenticLoop  int
			anthropicCacheTTL            string
			anthropicBreakpoints         string
			anthropicCacheCreationTokens int64
			anthropicCacheReadTokens     int64
		)
		if err := rows.Scan(
			&requestedAt,
			&provider,
			&model,
			&reasoningEffort,
			&source,
			&apiKey,
			&customerID,
			&customerEmail,
			&authID,
			&authIndex,
			&latencyMs,
			&failed,
			&inputTokens,
			&outputTokens,
			&reasoningTokens,
			&cachedTokens,
			&totalTokens,
			&promptCacheKey,
			&previousResponseID,
			&responseID,
			&promptCacheRetention,
			&anthropicRewriteApplied,
			&anthropicOverwroteLayout,
			&anthropicMatchedAgenticLoop,
			&anthropicCacheTTL,
			&anthropicBreakpoints,
			&anthropicCacheCreationTokens,
			&anthropicCacheReadTokens,
		); err != nil {
			return fmt.Errorf("cache statistics store: scan legacy sqlite request: %w", err)
		}
		timestamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(requestedAt))
		if err != nil {
			timestamp, err = time.Parse(time.RFC3339, strings.TrimSpace(requestedAt))
			if err != nil {
				continue
			}
		}
		event := CacheStatisticsEvent{
			Timestamp:       timestamp,
			Provider:        provider,
			Model:           model,
			ReasoningEffort: reasoningEffort,
			Source:          source,
			APIKey:          apiKey,
			CustomerID:      customerID,
			CustomerEmail:   customerEmail,
			AuthID:          authID,
			AuthIndex:       authIndex,
			LatencyMs:       latencyMs,
			Failed:          failed != 0,
			Tokens: TokenStats{
				InputTokens:     inputTokens,
				OutputTokens:    outputTokens,
				ReasoningTokens: reasoningTokens,
				CachedTokens:    cachedTokens,
				TotalTokens:     totalTokens,
			},
			AnthropicCache: helps.AnthropicCacheObservability{
				RewriteApplied:           anthropicRewriteApplied != 0,
				OverwroteClientLayout:    anthropicOverwroteLayout != 0,
				MatchedAgenticCodingLoop: anthropicMatchedAgenticLoop != 0,
				TTL:                      anthropicCacheTTL,
				CacheCreationInputTokens: anthropicCacheCreationTokens,
				CacheReadInputTokens:     anthropicCacheReadTokens,
			},
		}
		if promptCacheKey != "" || previousResponseID != "" || responseID != "" || promptCacheRetention != "" {
			event.Cache = &helps.CodexCacheObservability{
				PromptCacheKey:       promptCacheKey,
				PreviousResponseID:   previousResponseID,
				ResponseID:           responseID,
				PromptCacheRetention: promptCacheRetention,
			}
		}
		applyAnthropicBreakpoints(&event.AnthropicCache, anthropicBreakpoints)
		if err := s.InsertEvent(ctx, event); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cache statistics store: iterate legacy sqlite requests: %w", err)
	}
	return nil
}

func (s *CacheStatisticsStore) importSQLitePromptCacheRows(ctx context.Context, legacyDB *sql.DB) error {
	exists, err := sqliteTableExists(legacyDB, "prompt_cache_response_index")
	if err != nil || !exists {
		return err
	}
	columns, err := sqliteColumnSet(legacyDB, "prompt_cache_response_index")
	if err != nil {
		return err
	}
	query := `SELECT ` + strings.Join([]string{
		sqliteSelectExpr(columns, "response_id", "''"),
		sqliteSelectExpr(columns, "prompt_cache_key", "''"),
		sqliteSelectExpr(columns, "expires_at", "''"),
		sqliteSelectExpr(columns, "updated_at", "''"),
	}, ", ") + ` FROM prompt_cache_response_index`
	rows, err := legacyDB.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("cache statistics store: query legacy sqlite prompt cache rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var responseID, promptCacheKey, expiresAtRaw, updatedAtRaw string
		if err := rows.Scan(&responseID, &promptCacheKey, &expiresAtRaw, &updatedAtRaw); err != nil {
			return fmt.Errorf("cache statistics store: scan legacy sqlite prompt cache row: %w", err)
		}
		expiresAt, err := parseCacheStatisticsTime(expiresAtRaw)
		if err != nil {
			continue
		}
		updatedAt, err := parseCacheStatisticsTime(updatedAtRaw)
		if err != nil {
			updatedAt = time.Now().UTC()
		}
		if err := s.upsertPromptCacheKeyByResponseID(ctx, responseID, promptCacheKey, expiresAt, updatedAt); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cache statistics store: iterate legacy sqlite prompt cache rows: %w", err)
	}
	return nil
}

func sqliteTableExists(db *sql.DB, tableName string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tableName).Scan(&count); err != nil {
		return false, fmt.Errorf("cache statistics store: inspect legacy sqlite table %s: %w", tableName, err)
	}
	return count > 0, nil
}

func sqliteColumnSet(db *sql.DB, tableName string) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, tableName)
	if err != nil {
		return nil, fmt.Errorf("cache statistics store: inspect legacy sqlite columns for %s: %w", tableName, err)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("cache statistics store: scan legacy sqlite column for %s: %w", tableName, err)
		}
		columns[strings.TrimSpace(name)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cache statistics store: iterate legacy sqlite columns for %s: %w", tableName, err)
	}
	return columns, nil
}

func sqliteSelectExpr(columns map[string]struct{}, columnName, fallback string) string {
	if _, ok := columns[columnName]; ok {
		return quoteIdentifier(columnName)
	}
	return fallback + ` AS ` + quoteIdentifier(columnName)
}

func parseCacheStatisticsTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", raw)
}

func applyAnthropicBreakpoints(obs *helps.AnthropicCacheObservability, raw string) {
	if obs == nil {
		return
	}
	for _, part := range strings.Split(strings.TrimSpace(raw), ",") {
		switch strings.TrimSpace(strings.ToLower(part)) {
		case "tools":
			obs.ToolsBreakpoint = true
		case "system":
			obs.SystemBreakpoint = true
		case "messages":
			obs.MessagesBreakpoint = true
		}
	}
}

func ensurePostgresColumn(db *sql.DB, tableName, columnName, definition string) error {
	if db == nil {
		return fmt.Errorf("cache statistics store: database is nil")
	}
	_, err := db.Exec(`ALTER TABLE ` + tableName + ` ADD COLUMN IF NOT EXISTS ` + quoteIdentifier(columnName) + ` ` + definition)
	return err
}
