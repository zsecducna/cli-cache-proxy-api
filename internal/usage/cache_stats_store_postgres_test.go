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

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPostgresCacheStatisticsImportsSQLiteAndPersistsLoggerEvents(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLI_PROXY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLI_PROXY_TEST_POSTGRES_DSN is not set")
	}

	schema := fmt.Sprintf("cache_stats_it_%d", time.Now().UTC().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := OpenPostgresCacheStatisticsStore(ctx, PostgresCacheStatisticsStoreConfig{
		DSN:    dsn,
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("OpenPostgresCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	pgdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open(pgx) error = %v", err)
	}
	defer func() { _ = pgdb.Close() }()
	t.Cleanup(func() {
		_, _ = pgdb.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdentifier(schema)+` CASCADE`)
	})

	legacyPath := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	legacyStore, err := OpenCacheStatisticsStore(legacyPath)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}

	legacyEvent := CacheStatisticsEvent{
		Timestamp:       time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		Provider:        "codex",
		Model:           "gpt-5.4",
		ReasoningEffort: "medium",
		Source:          "legacy@example.com",
		APIKey:          "legacy-key-123",
		AuthID:          "auth-legacy",
		AuthIndex:       "0",
		LatencyMs:       1234,
		Tokens: TokenStats{
			InputTokens:  100,
			OutputTokens: 25,
			TotalTokens:  125,
		},
	}
	if err := legacyStore.InsertEvent(context.Background(), legacyEvent); err != nil {
		t.Fatalf("legacyStore.InsertEvent() error = %v", err)
	}
	if err := legacyStore.UpsertPromptCacheKeyByResponseID(context.Background(), "legacy-response", "legacy-cache", time.Hour); err != nil {
		t.Fatalf("legacyStore.UpsertPromptCacheKeyByResponseID() error = %v", err)
	}
	_ = legacyStore.Close()

	if err := store.ImportSQLiteFile(context.Background(), legacyPath); err != nil {
		t.Fatalf("ImportSQLiteFile() error = %v", err)
	}

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() after import error = %v", err)
	}
	if snapshot.Summary.TotalRequests != 1 {
		t.Fatalf("imported total_requests = %d, want 1", snapshot.Summary.TotalRequests)
	}
	if len(snapshot.RecentRequests) != 1 {
		t.Fatalf("imported recent requests = %d, want 1", len(snapshot.RecentRequests))
	}
	if snapshot.RecentRequests[0].APIKey != "legacy-key-123" {
		t.Fatalf("imported api_key = %q, want persisted raw api key", snapshot.RecentRequests[0].APIKey)
	}
	if key, ok, err := store.LookupPromptCacheKeyByResponseID(context.Background(), "legacy-response"); err != nil {
		t.Fatalf("LookupPromptCacheKeyByResponseID() after import error = %v", err)
	} else if !ok || key != "legacy-cache" {
		t.Fatalf("imported prompt cache key = (%q, %t), want (%q, true)", key, ok, "legacy-cache")
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

	plugin := NewLoggerPlugin()
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider:        "codex",
		Model:           "gpt-5.4-mini",
		APIKey:          "live-key-456",
		ReasoningEffort: "high",
		RequestedAt:     time.Now().UTC().Truncate(time.Second),
		Source:          "live@example.com",
		AuthID:          "auth-live",
		AuthIndex:       "1",
		Detail: coreusage.Detail{
			InputTokens:  55,
			OutputTokens: 11,
			TotalTokens:  66,
		},
	})

	var count int
	row := pgdb.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM `+quoteIdentifier(schema)+`.cache_statistics_requests
WHERE api_key = $1`, "live-key-456")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query persisted live api key count error = %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted live api key count = %d, want 1", count)
	}

	postSnapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() after logger write error = %v", err)
	}
	if postSnapshot.Summary.TotalRequests != 2 {
		t.Fatalf("post-write total_requests = %d, want 2", postSnapshot.Summary.TotalRequests)
	}
	foundLiveKey := false
	for _, item := range postSnapshot.RecentRequests {
		if item.APIKey == "live-key-456" {
			foundLiveKey = true
			break
		}
	}
	if !foundLiveKey {
		t.Fatal("recent requests missing persisted live api key")
	}
}
