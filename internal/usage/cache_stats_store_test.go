package usage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
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

func TestCacheStatisticsStoreEstimatesAntigravityClaudeCacheCreationOnCurrentRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	reads := []int64{0, 100, 150, 130}
	for i, read := range reads {
		err := store.InsertEvent(context.Background(), CacheStatisticsEvent{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Provider:  "antigravity",
			Model:     "claude-sonnet-4-6",
			AuthID:    "auth-1",
			AuthIndex: "0",
			Tokens: TokenStats{
				InputTokens:  10,
				OutputTokens: 2,
				TotalTokens:  12,
			},
			AnthropicCache: helps.AnthropicCacheObservability{
				TTL:                  "1h",
				CacheReadInputTokens: read,
			},
			StreamID: "stream-1",
		})
		if err != nil {
			t.Fatalf("InsertEvent(%d) error = %v", i, err)
		}
	}

	rows, err := store.db.Query(`SELECT anthropic_cache_read_input_tokens, anthropic_cache_creation_input_tokens FROM cache_statistics_requests ORDER BY requested_at ASC`)
	if err != nil {
		t.Fatalf("query rows error = %v", err)
	}
	defer rows.Close()

	wantWrites := []int64{0, 100, 50, 0}
	i := 0
	for rows.Next() {
		var gotRead, gotWrite int64
		if err := rows.Scan(&gotRead, &gotWrite); err != nil {
			t.Fatalf("scan row %d error = %v", i, err)
		}
		if i >= len(reads) {
			t.Fatalf("unexpected extra row %d", i)
		}
		if gotRead != reads[i] {
			t.Fatalf("row %d cache read = %d, want %d", i, gotRead, reads[i])
		}
		if gotWrite != wantWrites[i] {
			t.Fatalf("row %d cache write = %d, want %d", i, gotWrite, wantWrites[i])
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows error = %v", err)
	}
	if i != len(reads) {
		t.Fatalf("row count = %d, want %d", i, len(reads))
	}
}

func TestCacheStatisticsStoreCountsAnthropicCacheWritesAsOneHour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	ttls := []string{"", "5m", "1h"}
	for i, ttl := range ttls {
		err := store.InsertEvent(context.Background(), CacheStatisticsEvent{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Provider:  "claude",
			Model:     "claude-sonnet-4-6",
			AuthID:    "auth-1",
			AuthIndex: "0",
			Tokens: TokenStats{
				InputTokens:  10,
				OutputTokens: 2,
				TotalTokens:  12,
			},
			AnthropicCache: helps.AnthropicCacheObservability{
				TTL:                      ttl,
				CacheCreationInputTokens: 10,
			},
		})
		if err != nil {
			t.Fatalf("InsertEvent(%d) error = %v", i, err)
		}
	}

	snapshot, err := store.Snapshot(context.Background(), 10, 10, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Summary.AnthropicCacheWrite5mTokens != 0 {
		t.Fatalf("summary 5m cache write = %d, want 0", snapshot.Summary.AnthropicCacheWrite5mTokens)
	}
	if snapshot.Summary.AnthropicCacheWrite1hTokens != 30 {
		t.Fatalf("summary 1h cache write = %d, want 30", snapshot.Summary.AnthropicCacheWrite1hTokens)
	}
	if len(snapshot.ByModel) != 1 {
		t.Fatalf("len(ByModel) = %d, want 1", len(snapshot.ByModel))
	}
	if snapshot.ByModel[0].AnthropicCacheWrite5mTokens != 0 {
		t.Fatalf("model 5m cache write = %d, want 0", snapshot.ByModel[0].AnthropicCacheWrite5mTokens)
	}
	if snapshot.ByModel[0].AnthropicCacheWrite1hTokens != 30 {
		t.Fatalf("model 1h cache write = %d, want 30", snapshot.ByModel[0].AnthropicCacheWrite1hTokens)
	}
}

func TestCacheStatisticsStoreSnapshotTracksOpenAILongContextSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	events := []CacheStatisticsEvent{
		{
			Timestamp: now.Add(-3 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-1",
			AuthIndex: "0",
			Tokens: TokenStats{
				InputTokens:  300000,
				OutputTokens: 2000,
				CachedTokens: 120000,
				TotalTokens:  302000,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-long", ResponseID: "resp-1"},
		},
		{
			Timestamp: now.Add(-2 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-1",
			AuthIndex: "0",
			Tokens: TokenStats{
				InputTokens:  80000,
				OutputTokens: 800,
				CachedTokens: 79000,
				TotalTokens:  80800,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-long", PreviousResponseID: "resp-1", ResponseID: "resp-2"},
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4-mini",
			AuthID:    "auth-2",
			AuthIndex: "1",
			Tokens: TokenStats{
				InputTokens:  300500,
				OutputTokens: 50,
				CachedTokens: 100,
				TotalTokens:  300550,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-short", ResponseID: "resp-3"},
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

	if snapshot.Summary.LongContextInputTokens != 380000 {
		t.Fatalf("summary long_context_input_tokens = %d, want 380000", snapshot.Summary.LongContextInputTokens)
	}
	if snapshot.Summary.LongContextCachedTokens != 199000 {
		t.Fatalf("summary long_context_cached_tokens = %d, want 199000", snapshot.Summary.LongContextCachedTokens)
	}
	if snapshot.Summary.LongContextOutputTokens != 2800 {
		t.Fatalf("summary long_context_output_tokens = %d, want 2800", snapshot.Summary.LongContextOutputTokens)
	}

	if len(snapshot.ByModel) != 2 {
		t.Fatalf("len(ByModel) = %d, want 2", len(snapshot.ByModel))
	}

	models := make(map[string]CacheStatisticsModelSummary, len(snapshot.ByModel))
	for _, item := range snapshot.ByModel {
		models[item.Model] = item
	}
	gpt54, ok := models["gpt-5.4"]
	if !ok {
		t.Fatalf("missing gpt-5.4 summary in %+v", snapshot.ByModel)
	}
	if gpt54.LongContextInputTokens != 380000 {
		t.Fatalf("gpt-5.4 long_context_input_tokens = %d, want 380000", gpt54.LongContextInputTokens)
	}
	if gpt54.LongContextCachedTokens != 199000 {
		t.Fatalf("gpt-5.4 long_context_cached_tokens = %d, want 199000", gpt54.LongContextCachedTokens)
	}
	if gpt54.LongContextOutputTokens != 2800 {
		t.Fatalf("gpt-5.4 long_context_output_tokens = %d, want 2800", gpt54.LongContextOutputTokens)
	}

	gpt54Mini, ok := models["gpt-5.4-mini"]
	if !ok {
		t.Fatalf("missing gpt-5.4-mini summary in %+v", snapshot.ByModel)
	}
	if gpt54Mini.LongContextInputTokens != 0 || gpt54Mini.LongContextCachedTokens != 0 || gpt54Mini.LongContextOutputTokens != 0 {
		t.Fatalf("gpt-5.4-mini long-context fields = %+v, want zeros", gpt54Mini)
	}

	if snapshot.Summary.SuccessPercentage != 100 {
		t.Fatalf("summary success_percentage = %v, want 100", snapshot.Summary.SuccessPercentage)
	}
	if snapshot.Summary.GPT54.Standard.RequestCount != 0 {
		t.Fatalf("summary gpt_5_4.standard.request_count = %d, want 0", snapshot.Summary.GPT54.Standard.RequestCount)
	}
	if snapshot.Summary.GPT54.Standard.InputTokens != 0 {
		t.Fatalf("summary gpt_5_4.standard.input_tokens = %d, want 0", snapshot.Summary.GPT54.Standard.InputTokens)
	}
	if snapshot.Summary.GPT54.Standard.OutputTokens != 0 {
		t.Fatalf("summary gpt_5_4.standard.output_tokens = %d, want 0", snapshot.Summary.GPT54.Standard.OutputTokens)
	}
	if snapshot.Summary.GPT54.LongContext.RequestCount != 2 {
		t.Fatalf("summary gpt_5_4.long_context.request_count = %d, want 2", snapshot.Summary.GPT54.LongContext.RequestCount)
	}
	if snapshot.Summary.GPT54.LongContext.InputTokens != 380000 {
		t.Fatalf("summary gpt_5_4.long_context.input_tokens = %d, want 380000", snapshot.Summary.GPT54.LongContext.InputTokens)
	}
	if snapshot.Summary.GPT54.LongContext.OutputTokens != 2800 {
		t.Fatalf("summary gpt_5_4.long_context.output_tokens = %d, want 2800", snapshot.Summary.GPT54.LongContext.OutputTokens)
	}
}

func TestCacheStatisticsStoreSnapshotGPT54ThresholdAndSessionFollowUpCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	events := []CacheStatisticsEvent{
		{
			Timestamp: now.Add(-4 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-std",
			AuthIndex: "0",
			Tokens: TokenStats{
				InputTokens:  272000,
				OutputTokens: 10,
				TotalTokens:  272010,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-standard", ResponseID: "resp-standard"},
		},
		{
			Timestamp: now.Add(-3 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-long",
			AuthIndex: "1",
			Tokens: TokenStats{
				InputTokens:  272001,
				OutputTokens: 20,
				TotalTokens:  272021,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-chain", ResponseID: "resp-long-1"},
		},
		{
			Timestamp: now.Add(-2 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-long",
			AuthIndex: "1",
			Tokens: TokenStats{
				InputTokens:  500,
				OutputTokens: 30,
				TotalTokens:  530,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-chain", PreviousResponseID: "resp-long-1", ResponseID: "resp-long-2"},
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4-mini",
			AuthID:    "auth-mini",
			AuthIndex: "2",
			Failed:    true,
			Tokens: TokenStats{
				InputTokens:  100,
				OutputTokens: 5,
				TotalTokens:  105,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-mini", ResponseID: "resp-mini"},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	snapshot, err := store.Snapshot(context.Background(), 20, 20, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	if snapshot.Summary.LongContextInputTokens != 272501 {
		t.Fatalf("summary long_context_input_tokens = %d, want 272501", snapshot.Summary.LongContextInputTokens)
	}
	if snapshot.Summary.LongContextOutputTokens != 50 {
		t.Fatalf("summary long_context_output_tokens = %d, want 50", snapshot.Summary.LongContextOutputTokens)
	}

	if snapshot.Summary.SuccessPercentage != 75 {
		t.Fatalf("summary success_percentage = %v, want 75", snapshot.Summary.SuccessPercentage)
	}
	if snapshot.Summary.GPT54.Standard.RequestCount != 1 {
		t.Fatalf("summary gpt_5_4.standard.request_count = %d, want 1", snapshot.Summary.GPT54.Standard.RequestCount)
	}
	if snapshot.Summary.GPT54.Standard.InputTokens != 272000 {
		t.Fatalf("summary gpt_5_4.standard.input_tokens = %d, want 272000", snapshot.Summary.GPT54.Standard.InputTokens)
	}
	if snapshot.Summary.GPT54.Standard.OutputTokens != 10 {
		t.Fatalf("summary gpt_5_4.standard.output_tokens = %d, want 10", snapshot.Summary.GPT54.Standard.OutputTokens)
	}
	if snapshot.Summary.GPT54.LongContext.RequestCount != 2 {
		t.Fatalf("summary gpt_5_4.long_context.request_count = %d, want 2", snapshot.Summary.GPT54.LongContext.RequestCount)
	}
	if snapshot.Summary.GPT54.LongContext.InputTokens != 272501 {
		t.Fatalf("summary gpt_5_4.long_context.input_tokens = %d, want 272501", snapshot.Summary.GPT54.LongContext.InputTokens)
	}
	if snapshot.Summary.GPT54.LongContext.OutputTokens != 50 {
		t.Fatalf("summary gpt_5_4.long_context.output_tokens = %d, want 50", snapshot.Summary.GPT54.LongContext.OutputTokens)
	}
}

func TestCacheStatisticsStoreSnapshotGPT54BreakdownIgnoresGPT54ProThresholdCrossing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	events := []CacheStatisticsEvent{
		{
			Timestamp: now.Add(-4 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4-pro",
			AuthID:    "auth-pro",
			AuthIndex: "0",
			Tokens: TokenStats{
				InputTokens:  300000,
				OutputTokens: 1000,
				TotalTokens:  301000,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-mixed", ResponseID: "resp-pro-1"},
		},
		{
			Timestamp: now.Add(-3 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-gpt54",
			AuthIndex: "1",
			Tokens: TokenStats{
				InputTokens:  1000,
				OutputTokens: 100,
				TotalTokens:  1100,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-mixed", PreviousResponseID: "resp-pro-1", ResponseID: "resp-gpt54-1"},
		},
		{
			Timestamp: now.Add(-2 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			AuthID:    "auth-gpt54",
			AuthIndex: "1",
			Tokens: TokenStats{
				InputTokens:  2000,
				OutputTokens: 200,
				TotalTokens:  2200,
			},
			Cache: &helps.CodexCacheObservability{PromptCacheKey: "session-standard", ResponseID: "resp-gpt54-2"},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	snapshot, err := store.Snapshot(context.Background(), 20, 20, 14)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	if snapshot.Summary.GPT54.Standard.RequestCount != 2 {
		t.Fatalf("summary gpt_5_4.standard.request_count = %d, want 2", snapshot.Summary.GPT54.Standard.RequestCount)
	}
	if snapshot.Summary.GPT54.Standard.InputTokens != 3000 {
		t.Fatalf("summary gpt_5_4.standard.input_tokens = %d, want 3000", snapshot.Summary.GPT54.Standard.InputTokens)
	}
	if snapshot.Summary.GPT54.Standard.OutputTokens != 300 {
		t.Fatalf("summary gpt_5_4.standard.output_tokens = %d, want 300", snapshot.Summary.GPT54.Standard.OutputTokens)
	}
	if snapshot.Summary.GPT54.LongContext.RequestCount != 0 {
		t.Fatalf("summary gpt_5_4.long_context.request_count = %d, want 0", snapshot.Summary.GPT54.LongContext.RequestCount)
	}
	if snapshot.Summary.GPT54.LongContext.InputTokens != 0 {
		t.Fatalf("summary gpt_5_4.long_context.input_tokens = %d, want 0", snapshot.Summary.GPT54.LongContext.InputTokens)
	}
	if snapshot.Summary.GPT54.LongContext.OutputTokens != 0 {
		t.Fatalf("summary gpt_5_4.long_context.output_tokens = %d, want 0", snapshot.Summary.GPT54.LongContext.OutputTokens)
	}
}

func TestCacheStatisticsStoreSnapshotIncludesModelTrends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	events := []CacheStatisticsEvent{
		{
			Timestamp: time.Date(2026, 4, 7, 10, 15, 0, 0, time.UTC),
			Provider:  "codex",
			Model:     "gpt-5.4",
			Tokens:    TokenStats{InputTokens: 100, OutputTokens: 10, TotalTokens: 110},
		},
		{
			Timestamp: time.Date(2026, 4, 7, 11, 20, 0, 0, time.UTC),
			Provider:  "codex",
			Model:     "gpt-5.4-mini",
			Tokens:    TokenStats{InputTokens: 50, OutputTokens: 5, TotalTokens: 55},
		},
		{
			Timestamp: time.Date(2026, 4, 7, 11, 45, 0, 0, time.UTC),
			Provider:  "codex",
			Model:     "gpt-5.4",
			Tokens:    TokenStats{InputTokens: 200, OutputTokens: 20, TotalTokens: 220},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	snapshot, err := store.SnapshotSinceByProvider(context.Background(), 10, 10, time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC), "codex")
	if err != nil {
		t.Fatalf("SnapshotSinceByProvider() error = %v", err)
	}

	if len(snapshot.TrendByModel) != 2 {
		t.Fatalf("trend_by_model len = %d, want 2", len(snapshot.TrendByModel))
	}
	modelA, ok := snapshot.TrendByModel["gpt-5.4"]
	if !ok {
		t.Fatalf("missing model trend for gpt-5.4 in %+v", snapshot.TrendByModel)
	}
	if modelA.RequestsByDay["2026-04-07"] != 2 {
		t.Fatalf("gpt-5.4 requests_by_day = %d, want 2", modelA.RequestsByDay["2026-04-07"])
	}
	if modelA.RequestsByHour["2026-04-07T10:00:00Z"] != 1 || modelA.RequestsByHour["2026-04-07T11:00:00Z"] != 1 {
		t.Fatalf("gpt-5.4 requests_by_hour = %+v, want one request in 10:00 and 11:00 UTC buckets", modelA.RequestsByHour)
	}
	if modelA.TokensByHour["2026-04-07T10:00:00Z"] != 110 || modelA.TokensByHour["2026-04-07T11:00:00Z"] != 220 {
		t.Fatalf("gpt-5.4 tokens_by_hour = %+v, want 110 and 220", modelA.TokensByHour)
	}
	modelB, ok := snapshot.TrendByModel["gpt-5.4-mini"]
	if !ok {
		t.Fatalf("missing model trend for gpt-5.4-mini in %+v", snapshot.TrendByModel)
	}
	if modelB.RequestsByHour["2026-04-07T11:00:00Z"] != 1 {
		t.Fatalf("gpt-5.4-mini requests_by_hour = %+v, want one request in 11:00 UTC bucket", modelB.RequestsByHour)
	}
	if modelB.TokensByDay["2026-04-07"] != 55 {
		t.Fatalf("gpt-5.4-mini tokens_by_day = %d, want 55", modelB.TokensByDay["2026-04-07"])
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

func TestCacheStatisticsStoreBackfillsDuplicateEventKeysAndPreservesDedupe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	seedTime := time.Date(2026, 4, 8, 15, 4, 5, 0, time.UTC)
	_, err = db.Exec(`
CREATE TABLE cache_statistics_requests (
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
    anthropic_cache_read_input_tokens INTEGER NOT NULL DEFAULT 0
);
INSERT INTO cache_statistics_requests (
    event_key, requested_at, provider, model, reasoning_effort, source, api_key, customer_id, customer_email, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens
) VALUES (
    '', ?, 'codex', 'gpt-5.4', '', 'user@example.com', '', '', '', 'auth-1', '0', 1000, 0,
    100, 20, 10, 30, 130,
    'cache-key', 'prev-id', 'resp-id', '24h',
    0, 0, 0, '', '', 0, 0
), (
    'legacy-duplicate', ?, 'codex', 'gpt-5.4', '', 'user@example.com', '', '', '', 'auth-1', '0', 1000, 0,
    100, 20, 10, 30, 130,
    'cache-key', 'prev-id', 'resp-id', '24h',
    0, 0, 0, '', '', 0, 0
);`, seedTime.Format(time.RFC3339Nano), seedTime.Add(1*time.Second).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed legacy schema error = %v", err)
	}
	_ = db.Close()

	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	var totalRows, distinctEventKeys, emptyEventKeys int
	if err := store.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT event_key), SUM(CASE WHEN event_key = '' THEN 1 ELSE 0 END) FROM cache_statistics_requests`).Scan(&totalRows, &distinctEventKeys, &emptyEventKeys); err != nil {
		t.Fatalf("query backfilled keys error = %v", err)
	}
	if totalRows != 2 {
		t.Fatalf("total rows = %d, want 2", totalRows)
	}
	if distinctEventKeys != 2 {
		t.Fatalf("distinct event_key count = %d, want 2", distinctEventKeys)
	}
	if emptyEventKeys != 0 {
		t.Fatalf("empty event_key rows = %d, want 0", emptyEventKeys)
	}

	expectedKey := buildCacheStatisticsEventKey(CacheStatisticsEvent{
		Timestamp: seedTime,
		Provider:  "codex",
		Model:     "gpt-5.4",
		Source:    "user@example.com",
		AuthID:    "auth-1",
		AuthIndex: "0",
		LatencyMs: 1000,
		Tokens: TokenStats{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 10,
			CachedTokens:    30,
			TotalTokens:     130,
		},
		Cache: &helps.CodexCacheObservability{
			PromptCacheKey:       "cache-key",
			PreviousResponseID:   "prev-id",
			ResponseID:           "resp-id",
			PromptCacheRetention: "24h",
		},
	})
	var matchingRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cache_statistics_requests WHERE event_key = ?`, expectedKey).Scan(&matchingRows); err != nil {
		t.Fatalf("query canonical event key error = %v", err)
	}
	if matchingRows != 1 {
		t.Fatalf("canonical event_key rows = %d, want 1", matchingRows)
	}

	if err := store.InsertEvent(context.Background(), CacheStatisticsEvent{
		Timestamp: seedTime,
		Provider:  "codex",
		Model:     "gpt-5.4",
		Source:    "user@example.com",
		AuthID:    "auth-1",
		AuthIndex: "0",
		LatencyMs: 1000,
		Tokens: TokenStats{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 10,
			CachedTokens:    30,
			TotalTokens:     130,
		},
		Cache: &helps.CodexCacheObservability{
			PromptCacheKey:       "cache-key",
			PreviousResponseID:   "prev-id",
			ResponseID:           "resp-id",
			PromptCacheRetention: "24h",
		},
	}); err != nil {
		t.Fatalf("InsertEvent() error = %v", err)
	}

	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cache_statistics_requests`).Scan(&totalRows); err != nil {
		t.Fatalf("query total rows after dedupe error = %v", err)
	}
	if totalRows != 2 {
		t.Fatalf("total rows after dedupe = %d, want 2", totalRows)
	}
}

func TestCacheStatisticsStoreCanonicalizesLegacyEventKeyForFutureDedupe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	seedTime := time.Date(2026, 4, 8, 15, 4, 5, 0, time.UTC)
	_, err = db.Exec(`
CREATE TABLE cache_statistics_requests (
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
    anthropic_cache_read_input_tokens INTEGER NOT NULL DEFAULT 0
);
INSERT INTO cache_statistics_requests (
    event_key, requested_at, provider, model, reasoning_effort, source, api_key, customer_id, customer_email, auth_id, auth_index, latency_ms, failed,
    input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
    prompt_cache_key, previous_response_id, response_id, prompt_cache_retention,
    anthropic_rewrite_applied, anthropic_overwrote_client_layout, anthropic_matched_agentic_loop, anthropic_cache_ttl, anthropic_breakpoints,
    anthropic_cache_creation_input_tokens, anthropic_cache_read_input_tokens
) VALUES (
    'legacy-placeholder', ?, 'codex', 'gpt-5.4', '', 'user@example.com', '', '', '', 'auth-1', '0', 1000, 0,
    100, 20, 10, 30, 130,
    'cache-key', 'prev-id', 'resp-id', '24h',
    0, 0, 0, '', '', 0, 0
);`, seedTime.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed legacy schema error = %v", err)
	}
	_ = db.Close()

	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	expectedKey := buildCacheStatisticsEventKey(CacheStatisticsEvent{
		Timestamp: seedTime,
		Provider:  "codex",
		Model:     "gpt-5.4",
		Source:    "user@example.com",
		AuthID:    "auth-1",
		AuthIndex: "0",
		LatencyMs: 1000,
		Tokens: TokenStats{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 10,
			CachedTokens:    30,
			TotalTokens:     130,
		},
		Cache: &helps.CodexCacheObservability{
			PromptCacheKey:       "cache-key",
			PreviousResponseID:   "prev-id",
			ResponseID:           "resp-id",
			PromptCacheRetention: "24h",
		},
	})

	var storedKey string
	if err := store.db.QueryRow(`SELECT event_key FROM cache_statistics_requests`).Scan(&storedKey); err != nil {
		t.Fatalf("query stored event_key error = %v", err)
	}
	if storedKey != expectedKey {
		t.Fatalf("stored event_key = %q, want canonical %q", storedKey, expectedKey)
	}

	if err := store.InsertEvent(context.Background(), CacheStatisticsEvent{
		Timestamp: seedTime,
		Provider:  "codex",
		Model:     "gpt-5.4",
		Source:    "user@example.com",
		AuthID:    "auth-1",
		AuthIndex: "0",
		LatencyMs: 1000,
		Tokens: TokenStats{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 10,
			CachedTokens:    30,
			TotalTokens:     130,
		},
		Cache: &helps.CodexCacheObservability{
			PromptCacheKey:       "cache-key",
			PreviousResponseID:   "prev-id",
			ResponseID:           "resp-id",
			PromptCacheRetention: "24h",
		},
	}); err != nil {
		t.Fatalf("InsertEvent() error = %v", err)
	}

	var totalRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cache_statistics_requests`).Scan(&totalRows); err != nil {
		t.Fatalf("query total rows after dedupe error = %v", err)
	}
	if totalRows != 1 {
		t.Fatalf("total rows after dedupe = %d, want 1", totalRows)
	}
}

func TestCacheStatisticsStoreSeparatesSharedAPIKeyCustomers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	baseTimestamp := time.Now().UTC().Add(-time.Hour)
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
	first.CustomerEmail = "customer-a@example.com"
	second := baseEvent
	second.CustomerID = "customer-b"
	second.CustomerEmail = "customer-b@example.com"

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
		switch request.CustomerID {
		case "customer-a":
			if request.CustomerEmail != "customer-a@example.com" {
				t.Fatalf("recent customer_email = %q, want %q", request.CustomerEmail, "customer-a@example.com")
			}
		case "customer-b":
			if request.CustomerEmail != "customer-b@example.com" {
				t.Fatalf("recent customer_email = %q, want %q", request.CustomerEmail, "customer-b@example.com")
			}
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
		wantEmail := customerID + "@example.com"
		if modelSnapshot.Details[0].CustomerEmail != wantEmail {
			t.Fatalf("detail customer_email = %q, want %q", modelSnapshot.Details[0].CustomerEmail, wantEmail)
		}
	}
}

func TestCacheStatisticsStoreProviderSetFiltersAreCaseInsensitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	for _, event := range []CacheStatisticsEvent{
		{
			Timestamp: now.Add(-2 * time.Minute),
			Provider:  "OpenRouter",
			Model:     "gpt-4.1",
			AuthID:    "persisted-openrouter",
			AuthIndex: "0",
			Tokens:    TokenStats{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		},
		{
			Timestamp: now.Add(-1 * time.Minute),
			Provider:  "BoHe",
			Model:     "gpt-4.1-mini",
			AuthID:    "persisted-bohe",
			AuthIndex: "1",
			Tokens:    TokenStats{InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
		},
		{
			Timestamp: now.Add(-30 * time.Second),
			Provider:  "claude",
			Model:     "claude-opus-4-6",
			AuthID:    "persisted-claude",
			AuthIndex: "2",
			Tokens:    TokenStats{InputTokens: 50, OutputTokens: 10, TotalTokens: 60},
		},
	} {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	providers := ProviderNamesForFilter("openai-compatible", []string{"BoHe"})
	cacheSnapshot, err := store.SnapshotByProviders(context.Background(), 10, 10, 14, providers)
	if err != nil {
		t.Fatalf("SnapshotByProviders() error = %v", err)
	}
	if cacheSnapshot.Summary.TotalRequests != 2 {
		t.Fatalf("summary total_requests = %d, want 2", cacheSnapshot.Summary.TotalRequests)
	}
	if len(cacheSnapshot.ByModel) != 2 {
		t.Fatalf("by_model len = %d, want 2", len(cacheSnapshot.ByModel))
	}
	if len(cacheSnapshot.RecentRequests) != 2 {
		t.Fatalf("recent requests len = %d, want 2", len(cacheSnapshot.RecentRequests))
	}
	for _, item := range cacheSnapshot.RecentRequests {
		switch item.Provider {
		case "OpenRouter", "BoHe":
		default:
			t.Fatalf("unexpected provider %q in recent requests %+v", item.Provider, cacheSnapshot.RecentRequests)
		}
	}

	usageSnapshot, err := store.StatisticsSnapshotByProviders(context.Background(), providers)
	if err != nil {
		t.Fatalf("StatisticsSnapshotByProviders() error = %v", err)
	}
	if usageSnapshot.TotalRequests != 2 {
		t.Fatalf("usage total_requests = %d, want 2", usageSnapshot.TotalRequests)
	}
	if len(usageSnapshot.APIs) != 2 {
		t.Fatalf("usage apis len = %d, want 2", len(usageSnapshot.APIs))
	}
	for bucket, apiSnapshot := range usageSnapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				switch detail.Provider {
				case "OpenRouter", "BoHe":
				default:
					t.Fatalf("unexpected provider %q in bucket %q detail %+v", detail.Provider, bucket, detail)
				}
			}
		}
	}
}
