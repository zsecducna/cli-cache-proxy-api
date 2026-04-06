package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func newManagementTestServer(t *testing.T) *Server {
	t.Helper()
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
	cfg := &proxyconfig.Config{
		SDKConfig:              sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: true,
	}
	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)
	t.Cleanup(func() { _ = usage.ClosePersistentStore() })
	return server
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
	}
	if resp.Status != "ok" {
		t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
	}
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestManagementControlPanelIncludesCacheStatisticsIntegration(t *testing.T) {
	server := newManagementTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d", rr.Code, http.StatusOK)
	}
	if cacheControl := rr.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("management page should disable browser caching, got Cache-Control=%q", cacheControl)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "cliproxy-cache-stats-overlay") {
		t.Fatalf("management page missing cache stats injection: %s", body)
	}
	if !strings.Contains(body, "window.__cliproxyManagementEnhancer") {
		t.Fatalf("management page missing management enhancer bootstrap: %s", body)
	}
	if !strings.Contains(body, "cliproxy-usage-auto-refresh-toggle") || !strings.Contains(body, "cliproxy-usage-auto-refresh-interval") {
		t.Fatalf("management page missing usage auto-refresh controls: %s", body)
	}
	if !strings.Contains(body, "Usage auto refresh") {
		t.Fatalf("management page missing usage refresh label copy: %s", body)
	}
	if !strings.Contains(body, "cliproxy-quota-auto-refresh-toggle") || !strings.Contains(body, "cliproxy-quota-auto-refresh-interval") || !strings.Contains(body, "cliproxy-quota-page-size") {
		t.Fatalf("management page missing quota controls: %s", body)
	}
	if !strings.Contains(body, "Quota auto refresh") || !strings.Contains(body, "Paged view size") {
		t.Fatalf("management page missing quota control copy: %s", body)
	}
	if !strings.Contains(body, "cliproxy-usage-service-health") || !strings.Contains(body, "cliproxy-request-events") {
		t.Fatalf("management page missing stable custom section markers: %s", body)
	}
	if !strings.Contains(body, "Service Health") || !strings.Contains(body, "Cached Tokens") || !strings.Contains(body, "Reasoning Effort") {
		t.Fatalf("management page missing usage/request-events labels: %s", body)
	}
	if !strings.Contains(body, "Cache Read") || !strings.Contains(body, "Cache Write") || !strings.Contains(body, "Total Input") || !strings.Contains(body, "anthropic_cache_read_input_tokens") || !strings.Contains(body, "anthropic_cache_creation_input_tokens") {
		t.Fatalf("management page missing anthropic cache accounting labels/fields: %s", body)
	}
	if strings.Contains(body, "cliproxy-cache-stats-inline-host") {
		t.Fatalf("management page should not include the removed inline host: %s", body)
	}
	if strings.Contains(body, "Prompt Cache Statistics") || strings.Contains(body, "Open Cache Statistics") || strings.Contains(body, "managementKey") {
		t.Fatalf("management page should not include the legacy overlay launcher or management key field: %s", body)
	}
	if strings.Contains(body, "requestBlock.innerHTML") || strings.Contains(body, "removeChild(node)") {
		t.Fatalf("management enhancer should not mutate React-owned DOM unsafely: %s", body)
	}
	if strings.Contains(body, "new MutationObserver(syncRouteSoon)") || strings.Contains(body, "if (!background) triggerUsageRefresh()") || strings.Contains(body, "if (!background) triggerQuotaRefreshAll()") {
		t.Fatalf("management enhancer should guard against self-triggered refresh loops: %s", body)
	}
	if !strings.Contains(body, "const DEFAULT_USAGE_INTERVAL = 5;") || !strings.Contains(body, "scheduleUsageRefreshRetry") || !strings.Contains(body, "lastUsageStatisticsFetchAt") || !strings.Contains(body, "lastUsageStatisticsRangeKey") {
		t.Fatalf("management enhancer should throttle cache statistics fetches to the configured refresh interval and refetch when the selected time range changes: %s", body)
	}
	if !strings.Contains(body, "button[aria-label=\"Time Range\"]") || !strings.Contains(body, "setEnhancerMarkup") || !strings.Contains(body, "node.dataset.cliproxySignature") {
		t.Fatalf("management enhancer should follow the live time-range control and avoid rewriting unchanged custom usage sections: %s", body)
	}
}

func TestCacheStatisticsPageRedirectsToManagement(t *testing.T) {
	server := newManagementTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/cache-statistics.html", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("unexpected status code: got %d want %d", rr.Code, http.StatusFound)
	}
	if location := rr.Header().Get("Location"); location != "/management.html#cache-statistics" {
		t.Fatalf("redirect location = %q, want %q", location, "/management.html#cache-statistics")
	}
}

func TestCacheStatisticsPageRequiresManagementRoutes(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/cache-statistics.html", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status code: got %d want %d", rr.Code, http.StatusNotFound)
	}
}

func TestManagementCacheStatisticsEndpoint(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	now := time.Now().UTC().Truncate(time.Second)
	events := []usage.CacheStatisticsEvent{
		{
			Timestamp: now.AddDate(0, 0, -30),
			Provider:  "codex",
			Model:     "old-model",
			Source:    "old@example.com",
			AuthID:    "auth-old",
			AuthIndex: "9",
			LatencyMs: 3000,
			Tokens:    usage.TokenStats{InputTokens: 2000, OutputTokens: 200, CachedTokens: 1500, TotalTokens: 2200},
			Cache:     &helps.CodexCacheObservability{PromptCacheKey: "old-cache", ResponseID: "resp-old"},
		},
		{
			Timestamp:       now.Add(-2 * time.Hour),
			Provider:        "codex",
			Model:           "gpt-5.4",
			ReasoningEffort: "medium",
			Source:          "recent-a@example.com",
			AuthID:          "auth-a",
			AuthIndex:       "1",
			LatencyMs:       1200,
			Tokens:          usage.TokenStats{InputTokens: 1000, OutputTokens: 100, CachedTokens: 900, TotalTokens: 1100},
			Cache:           &helps.CodexCacheObservability{PromptCacheKey: "cache-a", ResponseID: "resp-a"},
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4-mini",
			Source:    "recent-b@example.com",
			AuthID:    "auth-b",
			AuthIndex: "2",
			LatencyMs: 800,
			Tokens:    usage.TokenStats{InputTokens: 500, OutputTokens: 60, CachedTokens: 250, TotalTokens: 560},
			Cache:     &helps.CodexCacheObservability{PromptCacheKey: "cache-b", ResponseID: "resp-b"},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/cache-statistics?days=7&limit=1&model_limit=1", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var payload struct {
		CacheStatistics struct {
			DBPath  string `json:"db_path"`
			Summary struct {
				TotalRequests int64 `json:"total_requests"`
				CachedTokens  int64 `json:"cached_tokens"`
			} `json:"summary"`
			ByModel []struct {
				Model string `json:"model"`
			} `json:"by_model"`
			RecentRequests []struct {
				Model                string `json:"model"`
				ReasoningEffort      string `json:"reasoning_effort"`
				Source               string `json:"source"`
				AuthID               string `json:"auth_id"`
				AuthIndex            string `json:"auth_index"`
				PromptCacheKey       string `json:"prompt_cache_key"`
				PreviousResponseID   string `json:"previous_response_id"`
				ResponseID           string `json:"response_id"`
				PromptCacheRetention string `json:"prompt_cache_retention"`
			} `json:"recent_requests"`
		} `json:"cache_statistics"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.CacheStatistics.DBPath != "" {
		t.Fatalf("db_path = %q, want redacted", payload.CacheStatistics.DBPath)
	}
	if payload.CacheStatistics.Summary.TotalRequests != 2 {
		t.Fatalf("total_requests = %d, want 2", payload.CacheStatistics.Summary.TotalRequests)
	}
	if payload.CacheStatistics.Summary.CachedTokens != 1150 {
		t.Fatalf("cached_tokens = %d, want 1150", payload.CacheStatistics.Summary.CachedTokens)
	}
	if len(payload.CacheStatistics.ByModel) != 1 {
		t.Fatalf("by_model len = %d, want 1", len(payload.CacheStatistics.ByModel))
	}
	if payload.CacheStatistics.ByModel[0].Model != "gpt-5.4" {
		t.Fatalf("first model = %q, want gpt-5.4", payload.CacheStatistics.ByModel[0].Model)
	}
	if len(payload.CacheStatistics.RecentRequests) != 1 {
		t.Fatalf("recent requests len = %d, want 1", len(payload.CacheStatistics.RecentRequests))
	}
	recent := payload.CacheStatistics.RecentRequests[0]
	if recent.Model != "gpt-5.4-mini" {
		t.Fatalf("recent model = %q, want gpt-5.4-mini", recent.Model)
	}
	if recent.ReasoningEffort != "" {
		t.Fatalf("recent reasoning_effort = %q, want empty for historical row without value", recent.ReasoningEffort)
	}
	if payload.CacheStatistics.ByModel[0].Model != "gpt-5.4" {
		t.Fatalf("first model = %q, want gpt-5.4", payload.CacheStatistics.ByModel[0].Model)
	}
	if recent.Source != "" || recent.AuthID != "" || recent.AuthIndex != "" {
		t.Fatalf("sensitive auth fields should be redacted: %+v", recent)
	}
	if recent.PromptCacheKey != "" || recent.PreviousResponseID != "" || recent.ResponseID != "" || recent.PromptCacheRetention != "" {
		t.Fatalf("cache identifiers should be redacted: %+v", recent)
	}

	sinceReq := httptest.NewRequest(http.MethodGet, "/v0/management/cache-statistics?since="+url.QueryEscape(now.Add(-90*time.Minute).Format(time.RFC3339Nano))+"&limit=10&model_limit=10", nil)
	sinceReq.Header.Set("Authorization", "Bearer test-secret")
	sinceRR := httptest.NewRecorder()
	server.engine.ServeHTTP(sinceRR, sinceReq)
	if sinceRR.Code != http.StatusOK {
		t.Fatalf("unexpected status code for since filter: got %d want %d; body=%s", sinceRR.Code, http.StatusOK, sinceRR.Body.String())
	}
	var sincePayload struct {
		CacheStatistics usage.CacheStatisticsSnapshot `json:"cache_statistics"`
	}
	if err := json.Unmarshal(sinceRR.Body.Bytes(), &sincePayload); err != nil {
		t.Fatalf("failed to decode since response: %v", err)
	}
	if sincePayload.CacheStatistics.Summary.TotalRequests != 1 {
		t.Fatalf("since total_requests = %d, want 1", sincePayload.CacheStatistics.Summary.TotalRequests)
	}
	if len(sincePayload.CacheStatistics.RecentRequests) != 1 || sincePayload.CacheStatistics.RecentRequests[0].Model != "gpt-5.4-mini" {
		t.Fatalf("since recent requests = %+v, want only gpt-5.4-mini", sincePayload.CacheStatistics.RecentRequests)
	}
}

func TestManagementUsageEndpointUsesPersistedCacheStatistics(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	now := time.Now().UTC().Truncate(time.Second)
	events := []usage.CacheStatisticsEvent{
		{
			Timestamp:       now.Add(-2 * time.Hour),
			Provider:        "codex",
			Model:           "gpt-5.4",
			ReasoningEffort: "medium",
			Source:          "user-a@example.com",
			AuthID:          "codex-user-a.json",
			AuthIndex:       "idx-a",
			LatencyMs:       1200,
			Tokens:          usage.TokenStats{InputTokens: 1000, OutputTokens: 100, CachedTokens: 900, TotalTokens: 1100},
			Cache:           &helps.CodexCacheObservability{PromptCacheKey: "cache-a", ResponseID: "resp-a"},
		},
		{
			Timestamp:       now.Add(-1 * time.Hour),
			Provider:        "codex",
			Model:           "gpt-5.4-mini",
			ReasoningEffort: "high",
			Source:          "user-b@example.com",
			AuthID:          "codex-user-b.json",
			AuthIndex:       "idx-b",
			LatencyMs:       800,
			Failed:          true,
			Tokens:          usage.TokenStats{InputTokens: 500, OutputTokens: 60, CachedTokens: 250, TotalTokens: 560},
			Cache:           &helps.CodexCacheObservability{PromptCacheKey: "cache-b", ResponseID: "resp-b"},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var payload struct {
		Usage          usage.StatisticsSnapshot `json:"usage"`
		FailedRequests int64                    `json:"failed_requests"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.Usage.TotalRequests != 2 {
		t.Fatalf("total_requests = %d, want 2", payload.Usage.TotalRequests)
	}
	if payload.Usage.TotalTokens != 1660 {
		t.Fatalf("total_tokens = %d, want 1660", payload.Usage.TotalTokens)
	}
	if payload.Usage.SuccessCount != 1 || payload.Usage.FailureCount != 1 || payload.FailedRequests != 1 {
		t.Fatalf("success/failure counts = %+v failed_requests=%d, want 1/1/1", payload.Usage, payload.FailedRequests)
	}
	if len(payload.Usage.APIs) != 2 {
		t.Fatalf("apis len = %d, want 2", len(payload.Usage.APIs))
	}
	apiSnapshot, ok := payload.Usage.APIs["codex-user-a.json"]
	if !ok {
		t.Fatalf("missing persisted api bucket for auth id")
	}
	if apiSnapshot.TotalRequests != 1 || apiSnapshot.TotalTokens != 1100 {
		t.Fatalf("api snapshot = %+v, want 1 request and 1100 tokens", apiSnapshot)
	}
	modelSnapshot, ok := apiSnapshot.Models["gpt-5.4"]
	if !ok || modelSnapshot.TotalRequests != 1 || len(modelSnapshot.Details) != 1 {
		t.Fatalf("model snapshot = %+v, want gpt-5.4 with one detail", modelSnapshot)
	}
	dayKey := now.Format("2006-01-02")
	if payload.Usage.RequestsByDay[dayKey] != 2 {
		t.Fatalf("requests_by_day[%q] = %d, want 2", dayKey, payload.Usage.RequestsByDay[dayKey])
	}
}

func TestDefaultRequestLoggerFactory_EnablesLoggingWhenDebugTrue(t *testing.T) {
	cfg := &proxyconfig.Config{Debug: true}
	logger := defaultRequestLoggerFactory(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}
	if !fileLogger.IsEnabled() {
		t.Fatal("expected request logger to be enabled when debug is true")
	}
}

type toggleableRequestLogger struct {
	enabled bool
}

func (l *toggleableRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	return nil
}

func (l *toggleableRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (internallogging.StreamingLogWriter, error) {
	return &internallogging.NoOpStreamingLogWriter{}, nil
}

func (l *toggleableRequestLogger) IsEnabled() bool {
	return l.enabled
}

func (l *toggleableRequestLogger) SetEnabled(enabled bool) {
	l.enabled = enabled
}

func TestUpdateClients_TogglesRequestLoggerWhenOnlyDebugChanges(t *testing.T) {
	logger := &toggleableRequestLogger{}
	server := &Server{
		requestLogger: logger,
		handlers:      apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil),
	}
	server.oldConfigYaml = []byte("debug: false\nrequest-log: false\n")

	server.UpdateClients(&proxyconfig.Config{Debug: true})
	if !logger.enabled {
		t.Fatal("expected request logger to be enabled when debug becomes true")
	}

	server.oldConfigYaml = []byte("debug: true\nrequest-log: false\n")
	server.UpdateClients(&proxyconfig.Config{})
	if logger.enabled {
		t.Fatal("expected request logger to be disabled when debug and request-log are both false")
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}

func TestDefaultRequestLoggerFactory_KeepsWritableRepoLogsDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	repoLogsDir := filepath.Join(tmpDir, "logs")
	if errMkdirLogs := os.MkdirAll(repoLogsDir, 0o755); errMkdirLogs != nil {
		t.Fatalf("failed to create repo logs dir: %v", errMkdirLogs)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/messages",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"repo-logs",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	repoEntries, errReadRepoDir := os.ReadDir(repoLogsDir)
	if errReadRepoDir != nil {
		t.Fatalf("failed to read repo logs dir %s: %v", repoLogsDir, errReadRepoDir)
	}
	foundErrorLogInRepoDir := false
	for _, entry := range repoEntries {
		if strings.HasPrefix(entry.Name(), "error-v1-messages-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInRepoDir = true
			break
		}
	}
	if !foundErrorLogInRepoDir {
		t.Fatalf("expected forced error log in repo logs dir %s, got entries: %+v", repoLogsDir, repoEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-v1-messages-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config logs dir %s", configLogsDir)
		}
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil && !os.IsNotExist(errReadAuthDir) {
		t.Fatalf("failed to inspect auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-v1-messages-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in auth logs dir %s", authLogsDir)
		}
	}
}
