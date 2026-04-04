package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
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
	body := rr.Body.String()
	if !strings.Contains(body, "cliproxy-cache-stats-overlay") {
		t.Fatalf("management page missing cache stats injection: %s", body)
	}
	if !strings.Contains(body, "Prompt Cache Statistics") || !strings.Contains(body, "Open Cache Statistics") {
		t.Fatalf("management page missing inline cache launcher: %s", body)
	}
	if !strings.Contains(body, "Usage Overview") {
		t.Fatalf("management page missing targeted Usage Overview attachment hint: %s", body)
	}
	if !strings.Contains(body, "Daily Cached Tokens") || !strings.Contains(body, "Daily Cache Ratio") {
		t.Fatalf("management page missing embedded cache charts: %s", body)
	}
	if !strings.Contains(body, "Today") || !strings.Contains(body, "Last 7 Days") || !strings.Contains(body, "This Month") {
		t.Fatalf("management page missing time preset filters: %s", body)
	}
	if !strings.Contains(body, "auto-refreshing every 3s while open") {
		t.Fatalf("management page missing near-real-time refresh status copy: %s", body)
	}
	if strings.Contains(body, "localStorage") {
		t.Fatalf("management page should not persist the management key in browser storage: %s", body)
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
			Timestamp: now.Add(-2 * time.Hour),
			Provider:  "codex",
			Model:     "gpt-5.4",
			Source:    "recent-a@example.com",
			AuthID:    "auth-a",
			AuthIndex: "1",
			LatencyMs: 1200,
			Tokens:    usage.TokenStats{InputTokens: 1000, OutputTokens: 100, CachedTokens: 900, TotalTokens: 1100},
			Cache:     &helps.CodexCacheObservability{PromptCacheKey: "cache-a", ResponseID: "resp-a"},
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
		CacheStatistics usage.CacheStatisticsSnapshot `json:"cache_statistics"`
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
	if recent.Source != "" || recent.AuthID != "" || recent.AuthIndex != "" {
		t.Fatalf("sensitive auth fields should be redacted: %+v", recent)
	}
	if recent.PromptCacheKey != "" || recent.PreviousResponseID != "" || recent.ResponseID != "" || recent.PromptCacheRetention != "" {
		t.Fatalf("cache identifiers should be redacted: %+v", recent)
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
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
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
