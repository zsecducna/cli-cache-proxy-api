package usage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
)

func TestCacheStatisticsStoreSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	events := []CacheStatisticsEvent{
		{
			Timestamp: now.AddDate(0, 0, -30),
			Provider:  "codex",
			Model:     "old-model",
			Source:    "old@example.com",
			AuthID:    "auth-old",
			AuthIndex: "9",
			LatencyMs: 2000,
			Tokens:    TokenStats{InputTokens: 2000, OutputTokens: 200, CachedTokens: 1500, TotalTokens: 2200},
			Cache:     &helps.CodexCacheObservability{PromptCacheKey: "cache-old", ResponseID: "resp-old"},
		},
		{
			Timestamp:       now.Add(-2 * time.Hour),
			Provider:        "codex",
			Model:           "gpt-5.4",
			ReasoningEffort: "medium",
			Source:          "user@example.com",
			AuthID:          "auth-1",
			AuthIndex:       "0",
			LatencyMs:       1200,
			Tokens:          TokenStats{InputTokens: 1000, OutputTokens: 100, CachedTokens: 0, TotalTokens: 1100},
			Cache:           &helps.CodexCacheObservability{PromptCacheKey: "cache-1", ResponseID: "resp-1"},
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			Source:    "user@example.com",
			AuthID:    "auth-1",
			AuthIndex: "0",
			LatencyMs: 900,
			Tokens:    TokenStats{InputTokens: 1000, OutputTokens: 80, CachedTokens: 960, TotalTokens: 1080},
			Cache:     &helps.CodexCacheObservability{PromptCacheKey: "cache-1", PreviousResponseID: "resp-1", ResponseID: "resp-2"},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !snapshot.Enabled {
		t.Fatal("snapshot.Enabled = false, want true")
	}
	if snapshot.Summary.TotalRequests != 2 {
		t.Fatalf("total_requests = %d, want 2", snapshot.Summary.TotalRequests)
	}
	if snapshot.Summary.CachedTokens != 960 {
		t.Fatalf("cached_tokens = %d, want 960", snapshot.Summary.CachedTokens)
	}
	if len(snapshot.ByModel) != 1 {
		t.Fatalf("len(ByModel) = %d, want 1", len(snapshot.ByModel))
	}
	if got := snapshot.ByModel[0].CacheRatio; got <= 0.4 {
		t.Fatalf("cache ratio = %f, want > 0.4", got)
	}
	if len(snapshot.RecentRequests) != 2 {
		t.Fatalf("len(RecentRequests) = %d, want 2", len(snapshot.RecentRequests))
	}
	if snapshot.RecentRequests[0].ResponseID != "resp-2" {
		t.Fatalf("recent response_id = %q, want resp-2", snapshot.RecentRequests[0].ResponseID)
	}
	if snapshot.RecentRequests[1].ReasoningEffort != "medium" {
		t.Fatalf("recent reasoning_effort = %q, want medium", snapshot.RecentRequests[1].ReasoningEffort)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("database permissions = %o, want 600", got)
		}
	}
}

func TestCacheStatisticsStoreMigratesExistingDatabaseWithoutReasoningEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`
CREATE TABLE cache_statistics_requests (
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
INSERT INTO cache_statistics_requests (
    requested_at, provider, model, source, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention
) VALUES (
    ?, 'codex', 'gpt-5.4', 'user@example.com', 'auth-1', '0', 1000, 0,
    100, 20, 10, 30, 130,
    'cache-key', 'prev-id', 'resp-id', '24h'
);`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed legacy schema error = %v", err)
	}
	_ = db.Close()

	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.RecentRequests) != 1 {
		t.Fatalf("len(RecentRequests) = %d, want 1", len(snapshot.RecentRequests))
	}
	if snapshot.RecentRequests[0].ReasoningEffort != "" {
		t.Fatalf("legacy reasoning_effort = %q, want empty string", snapshot.RecentRequests[0].ReasoningEffort)
	}
}
