package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// kiroObs builds a Kiro cache observability record with the given billed credits
// and context-usage fraction.
func kiroObs(credits, ctxUsage float64) helps.KiroCacheObservability {
	return helps.KiroCacheObservability{Credits: credits, ContextUsagePercent: ctxUsage}
}

// TestCacheStatisticsStoreSurfacesKiroCacheSignal verifies that Kiro requests,
// which carry no token-level cache counts (cached_tokens stays 0), still surface
// a truthful cache signal in the snapshot: aggregated credits, average context
// usage, and a derived cache-savings ratio computed from the per-model credit
// cost spread (a repeated, cache-eligible prompt bills materially fewer credits).
func TestCacheStatisticsStoreSurfacesKiroCacheSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	// Three cache-eligible (>2048 input) requests for the same Kiro model. The
	// first is a cold miss (high credits); the next two are warm cache hits on
	// the same prefix (much lower credits). cached_tokens stays 0 throughout, as
	// Kiro reports no token-level cache counts.
	events := []CacheStatisticsEvent{
		{
			Timestamp: now.Add(-3 * time.Hour),
			Provider:  "kiro",
			Model:     "claude-opus-4.8",
			Source:    "user@example.com",
			AuthID:    "auth-kiro",
			Tokens:    TokenStats{InputTokens: 50000, OutputTokens: 500, CachedTokens: 0, TotalTokens: 50500},
			KiroCache: kiroObs(3.0, 0.62),
		},
		{
			Timestamp: now.Add(-2 * time.Hour),
			Provider:  "kiro",
			Model:     "claude-opus-4.8",
			Source:    "user@example.com",
			AuthID:    "auth-kiro",
			Tokens:    TokenStats{InputTokens: 50000, OutputTokens: 500, CachedTokens: 0, TotalTokens: 50500},
			KiroCache: kiroObs(1.5, 0.64),
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Provider:  "kiro",
			Model:     "claude-opus-4.8",
			Source:    "user@example.com",
			AuthID:    "auth-kiro",
			Tokens:    TokenStats{InputTokens: 50000, OutputTokens: 500, CachedTokens: 0, TotalTokens: 50500},
			KiroCache: kiroObs(1.5, 0.66),
		},
	}
	for i, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent(%d) error = %v", i, err)
		}
	}

	snapshot, err := store.SnapshotByProvider(context.Background(), 10, 10, 14, "kiro")
	if err != nil {
		t.Fatalf("SnapshotByProvider() error = %v", err)
	}

	// Summary must aggregate the real Kiro cache signal.
	if got := snapshot.Summary.KiroCredits; got < 5.99 || got > 6.01 {
		t.Fatalf("summary.KiroCredits = %f, want ~6.0", got)
	}
	if got := snapshot.Summary.KiroContextUsagePercent; got < 0.60 || got > 0.68 {
		t.Fatalf("summary.KiroContextUsagePercent = %f, want ~0.64", got)
	}
	// Derived savings ratio: warm requests cost half the cold baseline, so the
	// fleet-wide savings versus the per-model cold baseline must be clearly > 0.
	if got := snapshot.Summary.KiroCacheSavingsRatio; got <= 0.0 || got > 1.0 {
		t.Fatalf("summary.KiroCacheSavingsRatio = %f, want in (0,1]", got)
	}

	// Per-model breakdown must carry the same signal for claude-opus-4.8.
	var opus *CacheStatisticsModelSummary
	for i := range snapshot.ByModel {
		if snapshot.ByModel[i].Model == "claude-opus-4.8" {
			opus = &snapshot.ByModel[i]
			break
		}
	}
	if opus == nil {
		t.Fatalf("ByModel missing claude-opus-4.8; got %+v", snapshot.ByModel)
	}
	if opus.KiroCredits < 5.99 || opus.KiroCredits > 6.01 {
		t.Fatalf("by_model KiroCredits = %f, want ~6.0", opus.KiroCredits)
	}
	if opus.KiroCacheSavingsRatio <= 0.0 || opus.KiroCacheSavingsRatio > 1.0 {
		t.Fatalf("by_model KiroCacheSavingsRatio = %f, want in (0,1]", opus.KiroCacheSavingsRatio)
	}
}

// TestCacheStatisticsStoreKiroSavingsNotInflatedAcrossModels guards against
// pooling per-request credit samples from different models under one global cold
// baseline. Credit-per-input differs structurally by model, so two models that
// each show NO caching (flat per-request cost) must yield an aggregate savings
// ratio of ~0 — not a phantom ratio manufactured from the inter-model price gap.
func TestCacheStatisticsStoreKiroSavingsNotInflatedAcrossModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache-statistics.sqlite")
	store, err := OpenCacheStatisticsStore(path)
	if err != nil {
		t.Fatalf("OpenCacheStatisticsStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC().Truncate(time.Second)
	// Two models, each billed a FLAT credit cost per identical input (no caching):
	// opus ~1.5 credits/50k input, haiku ~0.3 credits/50k input. Neither model
	// shows intra-model variation, so each model's savings must be 0, and so must
	// the aggregate. A naive global-max baseline would report ~80% phantom savings.
	events := []CacheStatisticsEvent{
		{Timestamp: now.Add(-4 * time.Hour), Provider: "kiro", Model: "claude-opus-4.8", Source: "u@x", AuthID: "a", Tokens: TokenStats{InputTokens: 50000, TotalTokens: 50500}, KiroCache: kiroObs(1.5, 0.6)},
		{Timestamp: now.Add(-3 * time.Hour), Provider: "kiro", Model: "claude-opus-4.8", Source: "u@x", AuthID: "a", Tokens: TokenStats{InputTokens: 50000, TotalTokens: 50500}, KiroCache: kiroObs(1.5, 0.6)},
		{Timestamp: now.Add(-2 * time.Hour), Provider: "kiro", Model: "claude-haiku-4.5", Source: "u@x", AuthID: "a", Tokens: TokenStats{InputTokens: 50000, TotalTokens: 50500}, KiroCache: kiroObs(0.3, 0.6)},
		{Timestamp: now.Add(-1 * time.Hour), Provider: "kiro", Model: "claude-haiku-4.5", Source: "u@x", AuthID: "a", Tokens: TokenStats{InputTokens: 50000, TotalTokens: 50500}, KiroCache: kiroObs(0.3, 0.6)},
	}
	for i, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent(%d) error = %v", i, err)
		}
	}

	snapshot, err := store.SnapshotByProvider(context.Background(), 10, 10, 14, "kiro")
	if err != nil {
		t.Fatalf("SnapshotByProvider() error = %v", err)
	}

	for _, m := range snapshot.ByModel {
		if m.KiroCacheSavingsRatio != 0 {
			t.Fatalf("model %s KiroCacheSavingsRatio = %f, want 0 (flat cost, no caching)", m.Model, m.KiroCacheSavingsRatio)
		}
	}
	if got := snapshot.Summary.KiroCacheSavingsRatio; got > 0.001 {
		t.Fatalf("aggregate KiroCacheSavingsRatio = %f, want ~0 (no model is caching; nonzero is phantom inter-model price spread)", got)
	}
}

