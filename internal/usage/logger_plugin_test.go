package usage

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
}

func TestRequestStatisticsRecordIncludesCodexCacheMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	helps.SetCodexCacheRequestObservability(ctx, []byte(`{"previous_response_id":"resp-prev","prompt_cache_retention":"24h"}`), "session-cache")
	helps.SetCodexCacheResponseObservability(ctx, []byte(`{"response":{"id":"resp-1","usage":{"input_tokens_details":{"cached_tokens":12}}}}`), "session-cache")

	stats := NewRequestStatistics()
	stats.Record(ctx, coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			CachedTokens: 12,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].Cache == nil {
		t.Fatal("cache metadata missing")
	}
	if details[0].Cache.PromptCacheKey != "session-cache" {
		t.Fatalf("prompt_cache_key = %q, want %q", details[0].Cache.PromptCacheKey, "session-cache")
	}
	if details[0].Cache.PreviousResponseID != "resp-prev" {
		t.Fatalf("previous_response_id = %q, want %q", details[0].Cache.PreviousResponseID, "resp-prev")
	}
	if details[0].Cache.ResponseID != "resp-1" {
		t.Fatalf("response_id = %q, want %q", details[0].Cache.ResponseID, "resp-1")
	}
	if details[0].Cache.PromptCacheRetention != "24h" {
		t.Fatalf("prompt_cache_retention = %q, want %q", details[0].Cache.PromptCacheRetention, "24h")
	}
	if details[0].Cache.CachedTokens != 12 {
		t.Fatalf("cache.cached_tokens = %d, want 12", details[0].Cache.CachedTokens)
	}
}

func TestRequestStatisticsRecordUsesCustomerIDBucket(t *testing.T) {
	stats := NewRequestStatistics()
	ts := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)

	stats.Record(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5.4",
		APIKey:      "shared-system-key",
		CustomerID:  "customer-a",
		RequestedAt: ts,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5.4",
		APIKey:      "shared-system-key",
		CustomerID:  "customer-b",
		RequestedAt: ts.Add(1 * time.Second),
		Detail: coreusage.Detail{
			InputTokens:  20,
			OutputTokens: 3,
			TotalTokens:  23,
		},
	})

	snapshot := stats.Snapshot()
	if len(snapshot.APIs) != 2 {
		t.Fatalf("apis len = %d, want 2", len(snapshot.APIs))
	}
	if _, ok := snapshot.APIs["shared-system-key"]; ok {
		t.Fatalf("unexpected shared api key bucket in %+v", snapshot.APIs)
	}
	for _, customerID := range []string{"customer-a", "customer-b"} {
		apiSnapshot, ok := snapshot.APIs[customerID]
		if !ok {
			t.Fatalf("missing bucket for %q in %+v", customerID, snapshot.APIs)
		}
		modelSnapshot, ok := apiSnapshot.Models["gpt-5.4"]
		if !ok || len(modelSnapshot.Details) != 1 {
			t.Fatalf("model snapshot for %q = %+v, want single detail", customerID, modelSnapshot)
		}
		if modelSnapshot.Details[0].CustomerID != customerID {
			t.Fatalf("detail customer_id = %q, want %q", modelSnapshot.Details[0].CustomerID, customerID)
		}
		if modelSnapshot.Details[0].Provider != "codex" {
			t.Fatalf("detail provider = %q, want %q", modelSnapshot.Details[0].Provider, "codex")
		}
	}
}

func TestLoggerPluginPersistsWithDetachedContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}

	cacheStatisticsStoreMu.Lock()
	previousStore := cacheStatisticsStore
	cacheStatisticsStore = store
	cacheStatisticsStoreMu.Unlock()
	t.Cleanup(func() {
		cacheStatisticsStoreMu.Lock()
		cacheStatisticsStore = previousStore
		cacheStatisticsStoreMu.Unlock()
		_ = store.Close()
	})

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	baseCtx := context.WithValue(context.Background(), "gin", ginCtx)
	helps.SetCodexCacheRequestObservability(baseCtx, []byte(`{"previous_response_id":"resp-prev","prompt_cache_retention":"24h"}`), "session-cache")
	helps.SetCodexCacheResponseObservability(baseCtx, []byte(`{"response":{"id":"resp-1","usage":{"input_tokens_details":{"cached_tokens":12}}}}`), "session-cache")
	ctx, cancel := context.WithCancel(baseCtx)
	cancel()

	plugin := NewLoggerPlugin()
	plugin.HandleUsage(ctx, coreusage.Record{
		Provider:        "codex",
		Model:           "gpt-5.4",
		ReasoningEffort: "high",
		RequestedAt:     time.Now().UTC(),
		Source:          "user@example.com",
		AuthID:          "auth-1",
		AuthIndex:       "0",
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 20,
			CachedTokens: 12,
			TotalTokens:  120,
		},
	})

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 1)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Summary.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", snapshot.Summary.TotalRequests)
	}
	if len(snapshot.RecentRequests) != 1 {
		t.Fatalf("recent requests len = %d, want 1", len(snapshot.RecentRequests))
	}
	if snapshot.RecentRequests[0].ResponseID != "resp-1" {
		t.Fatalf("response_id = %q, want resp-1", snapshot.RecentRequests[0].ResponseID)
	}
	if snapshot.RecentRequests[0].ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", snapshot.RecentRequests[0].ReasoningEffort)
	}
}

func TestLoggerPluginPersistsAnthropicCacheObservability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := OpenCacheStatisticsStore(filepath.Join(t.TempDir(), "cache-statistics.sqlite"))
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}

	cacheStatisticsStoreMu.Lock()
	previousStore := cacheStatisticsStore
	cacheStatisticsStore = store
	cacheStatisticsStoreMu.Unlock()
	t.Cleanup(func() {
		cacheStatisticsStoreMu.Lock()
		cacheStatisticsStore = previousStore
		cacheStatisticsStoreMu.Unlock()
		_ = store.Close()
	})

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	helps.SetAnthropicCacheObservability(ctx, []byte(`{"usage":{"cache_creation_input_tokens":31,"cache_read_input_tokens":22000}}`))

	plugin := NewLoggerPlugin()
	plugin.HandleUsage(ctx, coreusage.Record{
		Provider:    "claude",
		Model:       "claude-3-5-sonnet",
		RequestedAt: time.Now().UTC(),
		Source:      "user@example.com",
		AuthID:      "auth-1",
		AuthIndex:   "0",
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 20,
			CachedTokens: 22000,
			TotalTokens:  120,
		},
	})

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 1)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.RecentRequests) != 1 {
		t.Fatalf("recent requests len = %d, want 1", len(snapshot.RecentRequests))
	}
	if snapshot.RecentRequests[0].AnthropicCacheCreationInputTokens != 31 {
		t.Fatalf("anthropic_cache_creation_input_tokens = %d, want 31", snapshot.RecentRequests[0].AnthropicCacheCreationInputTokens)
	}
	if snapshot.RecentRequests[0].AnthropicCacheReadInputTokens != 22000 {
		t.Fatalf("anthropic_cache_read_input_tokens = %d, want 22000", snapshot.RecentRequests[0].AnthropicCacheReadInputTokens)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}
