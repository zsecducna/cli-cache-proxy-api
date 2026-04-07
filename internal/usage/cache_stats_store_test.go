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

func TestCacheStatisticsStoreUsesSingleSQLiteConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	stats := store.db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("max open connections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestCacheStatisticsStoreSnapshotIncludesAnthropicEffectiveInputTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	event := CacheStatisticsEvent{
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		Tokens: TokenStats{
			InputTokens:  3,
			OutputTokens: 101,
			CachedTokens: 164451,
			TotalTokens:  104,
		},
		AnthropicCache: helps.AnthropicCacheObservability{
			CacheCreationInputTokens: 1235,
			CacheReadInputTokens:     164451,
		},
	}
	if err := store.InsertEvent(context.Background(), event); err != nil {
		t.Fatalf("InsertEvent() error = %v", err)
	}

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Summary.EffectiveInputTokens != 165689 {
		t.Fatalf("summary effective_input_tokens = %d, want 165689", snapshot.Summary.EffectiveInputTokens)
	}
	if len(snapshot.RecentRequests) != 1 {
		t.Fatalf("len(RecentRequests) = %d, want 1", len(snapshot.RecentRequests))
	}
	if snapshot.RecentRequests[0].EffectiveInputTokens != 165689 {
		t.Fatalf("recent effective_input_tokens = %d, want 165689", snapshot.RecentRequests[0].EffectiveInputTokens)
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

func TestCacheStatisticsStoreSeparatesSharedAPIKeyCustomers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	baseTimestamp := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	baseEvent := CacheStatisticsEvent{
		Timestamp: baseTimestamp,
		Provider:  "codex",
		Model:     "gpt-5.4",
		APIKey:    "shared-system-key",
		AuthID:    "shared-auth",
		AuthIndex: "0",
		LatencyMs: 900,
		Tokens: TokenStats{
			InputTokens:  100,
			OutputTokens: 25,
			TotalTokens:  125,
		},
	}
	first := baseEvent
	first.CustomerID = "customer-a"
	second := baseEvent
	second.CustomerID = "customer-b"

	if err := store.InsertEvent(context.Background(), first); err != nil {
		t.Fatalf("InsertEvent(first) error = %v", err)
	}
	if err := store.InsertEvent(context.Background(), second); err != nil {
		t.Fatalf("InsertEvent(second) error = %v", err)
	}

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Summary.TotalRequests != 2 {
		t.Fatalf("total_requests = %d, want 2", snapshot.Summary.TotalRequests)
	}
	if len(snapshot.RecentRequests) != 2 {
		t.Fatalf("recent requests len = %d, want 2", len(snapshot.RecentRequests))
	}
	seenRecent := map[string]bool{}
	for _, request := range snapshot.RecentRequests {
		seenRecent[request.CustomerID] = true
		if request.APIKey != "shared-system-key" {
			t.Fatalf("recent api_key = %q, want %q", request.APIKey, "shared-system-key")
		}
	}
	for _, customerID := range []string{"customer-a", "customer-b"} {
		if !seenRecent[customerID] {
			t.Fatalf("recent requests missing customer_id %q: %+v", customerID, snapshot.RecentRequests)
		}
	}

	usageSnapshot, err := store.StatisticsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("StatisticsSnapshot() error = %v", err)
	}
	if len(usageSnapshot.APIs) != 2 {
		t.Fatalf("usage apis len = %d, want 2", len(usageSnapshot.APIs))
	}
	if _, ok := usageSnapshot.APIs["shared-system-key"]; ok {
		t.Fatalf("unexpected shared api bucket in %+v", usageSnapshot.APIs)
	}
	for _, customerID := range []string{"customer-a", "customer-b"} {
		apiSnapshot, ok := usageSnapshot.APIs[customerID]
		if !ok {
			t.Fatalf("missing usage bucket for %q in %+v", customerID, usageSnapshot.APIs)
		}
		modelSnapshot, ok := apiSnapshot.Models["gpt-5.4"]
		if !ok || len(modelSnapshot.Details) != 1 {
			t.Fatalf("model snapshot for %q = %+v, want one detail", customerID, modelSnapshot)
		}
		if modelSnapshot.Details[0].CustomerID != customerID {
			t.Fatalf("detail customer_id = %q, want %q", modelSnapshot.Details[0].CustomerID, customerID)
		}
	}
}
