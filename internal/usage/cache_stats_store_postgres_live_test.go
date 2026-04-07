package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestLiveConfiguredPostgresImportAndWrite(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLI_PROXY_LIVE_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLI_PROXY_LIVE_POSTGRES_DSN is not set")
	}

	schema := strings.TrimSpace(os.Getenv("CLI_PROXY_LIVE_POSTGRES_SCHEMA"))
	if schema == "" {
		schema = "public"
	}

	localPath := strings.TrimSpace(os.Getenv("CLI_PROXY_LIVE_LOCAL_PATH"))
	if localPath == "" {
		localPath = "~/.cli-cache-proxy"
	}
	resolvedLocalPath, err := util.ResolveAuthDir(localPath)
	if err != nil {
		t.Fatalf("ResolveAuthDir(%q) error = %v", localPath, err)
	}
	legacyPath := filepath.Join(resolvedLocalPath, "stats", "cache-statistics.sqlite")
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy sqlite path %q error = %v", legacyPath, err)
	}

	legacyDB, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	defer func() { _ = legacyDB.Close() }()

	var legacyCount int64
	if err := legacyDB.QueryRow(`SELECT COUNT(*) FROM cache_statistics_requests`).Scan(&legacyCount); err != nil {
		t.Fatalf("count legacy sqlite requests error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	store, err := OpenPostgresCacheStatisticsStore(ctx, PostgresCacheStatisticsStoreConfig{
		DSN:    dsn,
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("OpenPostgresCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	var preImportCount int64
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+store.requestsTableName()).Scan(&preImportCount); err != nil {
		t.Fatalf("count postgres requests before import error = %v", err)
	}

	if err := store.ImportSQLiteFile(ctx, legacyPath); err != nil {
		t.Fatalf("ImportSQLiteFile() error = %v", err)
	}

	var postImportCount int64
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+store.requestsTableName()).Scan(&postImportCount); err != nil {
		t.Fatalf("count postgres requests after import error = %v", err)
	}
	if postImportCount < preImportCount {
		t.Fatalf("postgres request count decreased after import: before=%d after=%d", preImportCount, postImportCount)
	}
	if postImportCount == 0 && legacyCount > 0 {
		t.Fatalf("postgres request count is zero after import, legacy sqlite count = %d", legacyCount)
	}

	cacheStatisticsStoreMu.Lock()
	previousStore := cacheStatisticsStore
	cacheStatisticsStore = store
	cacheStatisticsStoreMu.Unlock()
	t.Cleanup(func() {
		cacheStatisticsStoreMu.Lock()
		cacheStatisticsStore = previousStore
		cacheStatisticsStoreMu.Unlock()
	})

	liveAPIKey := fmt.Sprintf("live-align-check-%d", time.Now().UTC().UnixNano())
	NewLoggerPlugin().HandleUsage(context.Background(), coreusage.Record{
		Provider:        "codex",
		Model:           "gpt-5.4-mini",
		APIKey:          liveAPIKey,
		ReasoningEffort: "high",
		RequestedAt:     time.Now().UTC().Truncate(time.Second),
		Source:          "live-validation@example.com",
		AuthID:          "live-validation-auth",
		AuthIndex:       "live-validation-index",
		Detail: coreusage.Detail{
			InputTokens:  21,
			OutputTokens: 8,
			TotalTokens:  29,
		},
	})

	var liveCount int
	row := store.db.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM `+store.requestsTableName()+`
WHERE api_key = $1`, liveAPIKey)
	if err := row.Scan(&liveCount); err != nil {
		t.Fatalf("count live persisted api key error = %v", err)
	}
	if liveCount != 1 {
		t.Fatalf("live persisted api key count = %d, want 1", liveCount)
	}
}
