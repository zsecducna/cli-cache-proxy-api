package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestCurrentUsageSnapshotMergesPersistedAndLiveUsingProviderSetFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = usage.ClosePersistentStore()
	t.Cleanup(func() {
		_ = usage.ClosePersistentStore()
	})

	cfg := &config.Config{
		UsageStatisticsEnabled: true,
		OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "BoHe", BaseURL: "https://example.com", Models: []config.OpenAICompatibilityModel{{Name: "gpt-4.1-mini"}}},
		},
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := usage.ConfigurePersistentStore(cfg, configPath); err != nil {
		t.Fatalf("ConfigurePersistentStore() error = %v", err)
	}
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store")
	}

	now := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	for _, event := range []usage.CacheStatisticsEvent{
		{
			Timestamp: now.Add(-2 * time.Minute),
			Provider:  "OpenRouter",
			Model:     "gpt-4.1",
			AuthID:    "persisted-openrouter",
			AuthIndex: "0",
			Tokens:    usage.TokenStats{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		},
		{
			Timestamp: now.Add(-90 * time.Second),
			Provider:  "claude",
			Model:     "claude-sonnet-4-6",
			AuthID:    "persisted-claude",
			AuthIndex: "1",
			Tokens:    usage.TokenStats{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		},
	} {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	live := usage.NewRequestStatistics()
	live.Record(context.Background(), coreusage.Record{
		Provider:    "BoHe",
		Model:       "gpt-4.1-mini",
		APIKey:      "live-bohe-key",
		RequestedAt: now.Add(-30 * time.Second),
		AuthID:      "live-bohe",
		AuthIndex:   "2",
		Detail:      coreusage.Detail{InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
	})
	live.Record(context.Background(), coreusage.Record{
		Provider:    "claude",
		Model:       "claude-opus-4-6",
		APIKey:      "live-claude-key",
		RequestedAt: now.Add(-20 * time.Second),
		AuthID:      "live-claude",
		AuthIndex:   "3",
		Detail:      coreusage.Detail{InputTokens: 50, OutputTokens: 10, TotalTokens: 60},
	})

	handler := NewHandlerWithoutConfigFilePath(cfg, nil)
	handler.SetUsageStatistics(live)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage?provider=openai-compatible", nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	snapshot := handler.currentUsageSnapshot(ctx)
	if snapshot.TotalRequests != 2 {
		t.Fatalf("total_requests = %d, want 2", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 16 {
		t.Fatalf("total_tokens = %d, want 16", snapshot.TotalTokens)
	}
	if snapshot.SuccessCount != 2 || snapshot.FailureCount != 0 {
		t.Fatalf("success/failure = %d/%d, want 2/0", snapshot.SuccessCount, snapshot.FailureCount)
	}
	if len(snapshot.APIs) != 2 {
		t.Fatalf("apis len = %d, want 2", len(snapshot.APIs))
	}
	if _, ok := snapshot.APIs["persisted-openrouter"]; !ok {
		t.Fatalf("missing persisted openrouter bucket: %+v", snapshot.APIs)
	}
	if _, ok := snapshot.APIs["live-bohe-key"]; !ok {
		t.Fatalf("missing live bohe bucket: %+v", snapshot.APIs)
	}
	if _, ok := snapshot.APIs["persisted-claude"]; ok {
		t.Fatalf("unexpected persisted claude bucket in %+v", snapshot.APIs)
	}
	if _, ok := snapshot.APIs["live-claude-key"]; ok {
		t.Fatalf("unexpected live claude bucket in %+v", snapshot.APIs)
	}

	modelRequests := int64(0)
	allowedProviders := map[string]struct{}{"openrouter": {}, "bohe": {}}
	for _, apiSnapshot := range snapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			modelRequests += modelSnapshot.TotalRequests
			for _, detail := range modelSnapshot.Details {
				if _, ok := allowedProviders[strings.ToLower(strings.TrimSpace(detail.Provider))]; !ok {
					t.Fatalf("unexpected provider %q in detail %+v", detail.Provider, detail)
				}
			}
		}
	}
	if modelRequests != snapshot.TotalRequests {
		t.Fatalf("summed model requests = %d, want %d", modelRequests, snapshot.TotalRequests)
	}
}
