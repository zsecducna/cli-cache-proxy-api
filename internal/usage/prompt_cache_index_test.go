package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPromptCacheResponseIndexRoundTrip(t *testing.T) {
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.UpsertPromptCacheKeyByResponseID(context.Background(), "resp-1", "session-cache", 2*time.Hour); err != nil {
		t.Fatalf("UpsertPromptCacheKeyByResponseID() error = %v", err)
	}

	got, ok, err := store.LookupPromptCacheKeyByResponseID(context.Background(), "resp-1")
	if err != nil {
		t.Fatalf("LookupPromptCacheKeyByResponseID() error = %v", err)
	}
	if !ok {
		t.Fatal("LookupPromptCacheKeyByResponseID() ok = false, want true")
	}
	if got != "session-cache" {
		t.Fatalf("LookupPromptCacheKeyByResponseID() = %q, want %q", got, "session-cache")
	}
}

func TestPromptCacheResponseIndexDeletesExpiredEntries(t *testing.T) {
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	_, err = store.db.Exec(`
INSERT INTO prompt_cache_response_index (response_id, prompt_cache_key, expires_at, updated_at)
VALUES (?, ?, ?, ?)`, "resp-expired", "stale-cache", now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed expired prompt cache entry error = %v", err)
	}

	got, ok, err := store.LookupPromptCacheKeyByResponseID(context.Background(), "resp-expired")
	if err != nil {
		t.Fatalf("LookupPromptCacheKeyByResponseID() error = %v", err)
	}
	if ok {
		t.Fatalf("LookupPromptCacheKeyByResponseID() ok = true, want false (got %q)", got)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM prompt_cache_response_index WHERE response_id = ?`, "resp-expired").Scan(&count); err != nil {
		t.Fatalf("count expired entry error = %v", err)
	}
	if count != 0 {
		t.Fatalf("expired entry count = %d, want 0", count)
	}
}

// TestPromptCacheLookupDoesNotGloballyDeleteExpiredEntries protects lookup latency from write amplification.
func TestPromptCacheLookupDoesNotGloballyDeleteExpiredEntries(t *testing.T) {
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	_, err = store.db.Exec(`
INSERT INTO prompt_cache_response_index (response_id, prompt_cache_key, expires_at, updated_at)
VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		"resp-valid", "session-cache", now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		"resp-unrelated-expired", "stale-cache", now.Add(-time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed prompt cache entries error = %v", err)
	}

	got, ok, err := store.LookupPromptCacheKeyByResponseID(context.Background(), "resp-valid")
	if err != nil {
		t.Fatalf("LookupPromptCacheKeyByResponseID() error = %v", err)
	}
	if !ok || got != "session-cache" {
		t.Fatalf("LookupPromptCacheKeyByResponseID() = (%q, %t), want (%q, true)", got, ok, "session-cache")
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM prompt_cache_response_index WHERE response_id = ?`, "resp-unrelated-expired").Scan(&count); err != nil {
		t.Fatalf("count unrelated expired entry error = %v", err)
	}
	if count != 1 {
		t.Fatalf("unrelated expired entry count = %d, want 1", count)
	}
}

// TestCacheStatisticsStoreIgnoresUseAfterClose protects stale pointers held across configuration reload.
func TestCacheStatisticsStoreIgnoresUseAfterClose(t *testing.T) {
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	err = store.InsertEvent(context.Background(), CacheStatisticsEvent{
		Timestamp: time.Now().UTC(),
		Provider:  "codex",
		Model:     "gpt-5.5",
	})
	if err != nil {
		t.Fatalf("InsertEvent() after Close error = %v", err)
	}
}

// TestPromptCacheConcurrentUpsertsAreSafe exercises cleanup bookkeeping under concurrent writers.
func TestPromptCacheConcurrentUpsertsAreSafe(t *testing.T) {
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	done := make(chan error, 16)
	for i := 0; i < cap(done); i++ {
		i := i
		go func() {
			responseID := "resp-concurrent-" + string(rune('a'+i))
			done <- store.UpsertPromptCacheKeyByResponseID(context.Background(), responseID, "session-cache", time.Hour)
		}()
	}
	for i := 0; i < cap(done); i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent upsert error = %v", err)
		}
	}
}
