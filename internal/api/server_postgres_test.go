package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestManagementCacheStatisticsUsesPostgresBackfillAndLiveWrites(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CLI_PROXY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("CLI_PROXY_TEST_POSTGRES_DSN is not set")
	}

	t.Setenv("MANAGEMENT_PASSWORD", "test-secret")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	managementPath := filepath.Join(tmpDir, "management.html")
	if err := os.WriteFile(managementPath, []byte("<!doctype html><html><body><div id=\"root\"></div></body></html>"), 0o644); err != nil {
		t.Fatalf("failed to write management fixture: %v", err)
	}
	t.Setenv("MANAGEMENT_STATIC_PATH", managementPath)

	legacyPath := filepath.Join(tmpDir, "stats", "cache-statistics.sqlite")
	legacyStore, err := usage.OpenCacheStatisticsStore(legacyPath)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	if err := legacyStore.InsertEvent(context.Background(), usage.CacheStatisticsEvent{
		Timestamp:       time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		Provider:        "codex",
		Model:           "gpt-5.4",
		ReasoningEffort: "medium",
		Source:          "legacy@example.com",
		APIKey:          "legacy-key-123",
		AuthID:          "auth-legacy",
		AuthIndex:       "0",
		LatencyMs:       900,
		Tokens: usage.TokenStats{
			InputTokens:  100,
			OutputTokens: 10,
			TotalTokens:  110,
		},
	}); err != nil {
		t.Fatalf("legacyStore.InsertEvent() error = %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("legacyStore.Close() error = %v", err)
	}

	schema := fmt.Sprintf("mgmt_stats_it_%d", time.Now().UTC().UnixNano())
	usage.SetPersistentStoreOptions(usage.PersistentStoreOptions{
		PostgresDSN:       dsn,
		PostgresSchema:    schema,
		PostgresLocalPath: tmpDir,
		RequirePostgres:   true,
	})
	t.Cleanup(func() {
		usage.SetPersistentStoreOptions(usage.PersistentStoreOptions{})
		_ = usage.ClosePersistentStore()
	})

	pgdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open(pgx) error = %v", err)
	}
	defer func() { _ = pgdb.Close() }()
	t.Cleanup(func() {
		_, _ = pgdb.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quotePgIdentifier(schema)+` CASCADE`)
	})

	cfg := &proxyconfig.Config{
		SDKConfig:              sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: true,
	}
	server := NewServer(cfg, auth.NewManager(nil, nil, nil), sdkaccess.NewManager(), filepath.Join(tmpDir, "config.yaml"))

	decode := func(path string) usage.CacheStatisticsSnapshot {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-secret")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", path, rr.Code, http.StatusOK, rr.Body.String())
		}
		var payload struct {
			CacheStatistics usage.CacheStatisticsSnapshot `json:"cache_statistics"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode %s response: %v", path, err)
		}
		return payload.CacheStatistics
	}

	imported := decode("/v0/management/cache-statistics?days=14")
	if imported.Summary.TotalRequests != 1 {
		t.Fatalf("imported total_requests = %d, want 1", imported.Summary.TotalRequests)
	}
	if len(imported.RecentRequests) != 1 {
		t.Fatalf("imported recent requests = %d, want 1", len(imported.RecentRequests))
	}
	if imported.RecentRequests[0].APIKey != "" {
		t.Fatalf("management cache statistics should redact api keys, got %q", imported.RecentRequests[0].APIKey)
	}

	usage.NewLoggerPlugin().HandleUsage(context.Background(), coreusage.Record{
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

	afterWrite := decode("/v0/management/cache-statistics?days=14")
	if afterWrite.Summary.TotalRequests != 2 {
		t.Fatalf("post-write total_requests = %d, want 2", afterWrite.Summary.TotalRequests)
	}
	if len(afterWrite.RecentRequests) != 2 {
		t.Fatalf("post-write recent requests = %d, want 2", len(afterWrite.RecentRequests))
	}

	var count int
	row := pgdb.QueryRowContext(context.Background(), `
SELECT COUNT(*)
FROM `+quotePgIdentifier(schema)+`.cache_statistics_requests
WHERE api_key = $1`, "live-key-456")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query persisted api key count error = %v", err)
	}
	if count != 1 {
		t.Fatalf("persisted api key count = %d, want 1", count)
	}
}

func quotePgIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
