package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/customerstate"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

	t.Run("GET", func(t *testing.T) {
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
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("expected empty body for HEAD request, got %q", rr.Body.String())
		}
	})
}

func TestCustomerIdentityMiddlewareTrustsLocalSharedKey(t *testing.T) {
	server := newTestServer(t)
	server.engine.GET("/capture-customer",
		AuthMiddleware(server.accessManager),
		middleware.CustomerIdentityMiddleware(),
		func(c *gin.Context) {
			customerID, _ := c.Get("customerID")
			customerEmail, _ := c.Get("customerEmail")
			c.JSON(http.StatusOK, gin.H{
				"customer_id":        customerID,
				"customer_email":     customerEmail,
				"context_user_id":    helps.CustomerIDFromContext(c.Request.Context()),
				"context_user_email": helps.CustomerEmailFromContext(c.Request.Context()),
				"api_key":            c.GetString("apiKey"),
			})
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/capture-customer", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set(middleware.CustomerIDHeader, "customer-123")
	req.Header.Set(middleware.CustomerEmailHeader, "customer@example.com")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		CustomerID       string `json:"customer_id"`
		CustomerEmail    string `json:"customer_email"`
		ContextUserID    string `json:"context_user_id"`
		ContextUserEmail string `json:"context_user_email"`
		APIKey           string `json:"api_key"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.CustomerID != "customer-123" {
		t.Fatalf("customer_id = %q, want %q", payload.CustomerID, "customer-123")
	}
	if payload.ContextUserID != "customer-123" {
		t.Fatalf("context_user_id = %q, want %q", payload.ContextUserID, "customer-123")
	}
	if payload.CustomerEmail != "customer@example.com" {
		t.Fatalf("customer_email = %q, want %q", payload.CustomerEmail, "customer@example.com")
	}
	if payload.ContextUserEmail != "customer@example.com" {
		t.Fatalf("context_user_email = %q, want %q", payload.ContextUserEmail, "customer@example.com")
	}
	if payload.APIKey != "test-key" {
		t.Fatalf("api_key = %q, want %q", payload.APIKey, "test-key")
	}
}

func TestCustomerIdentityMiddlewareRejectsSpoofedRemoteHeader(t *testing.T) {
	server := newTestServer(t)
	server.engine.GET("/capture-customer",
		AuthMiddleware(server.accessManager),
		middleware.CustomerIdentityMiddleware(),
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/capture-customer", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set(middleware.CustomerIDHeader, "customer-123")
	req.Header.Set(middleware.CustomerEmailHeader, "customer@example.com")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "trusted internal callers") {
		t.Fatalf("unexpected response body: %s", rr.Body.String())
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
	if !strings.Contains(body, "function applyQuotaMode(context, mode, options)") || !strings.Contains(body, "const shouldUpdatePageSize = context.state.pageSize !== nextPageSize;") || !strings.Contains(body, "if (shouldUpdatePageSize && context.state.pageSizeDispatch) context.state.pageSizeDispatch(nextPageSize);") || !strings.Contains(body, "applyQuotaMode(context, mode, { resetPage: false });") {
		t.Fatalf("management enhancer should preserve the active quota page during background route syncs by skipping redundant page-size dispatches and only resetting pagination for explicit quota mode changes: %s", body)
	}
	if !strings.Contains(body, "DEFAULT_COLLAPSED_SECTION_TITLES") || !strings.Contains(body, "API Details") || !strings.Contains(body, "Model Statistics") || !strings.Contains(body, "Credential Statistics") || !strings.Contains(body, "Model Pricing Settings") || !strings.Contains(body, "collapseDefaultSections(container);") {
		t.Fatalf("management page should auto-collapse the requested detail sections by default, including model pricing settings: %s", body)
	}
	if !strings.Contains(body, "cliproxy-usage-service-health") || !strings.Contains(body, "cliproxy-request-events") {
		t.Fatalf("management page missing stable custom section markers: %s", body)
	}
	if !strings.Contains(body, "Service Health") || !strings.Contains(body, "Cached Tokens") || !strings.Contains(body, "Reasoning Effort") {
		t.Fatalf("management page missing usage/request-events labels: %s", body)
	}
	if !strings.Contains(body, "Cache Read") || !strings.Contains(body, "Cache Write") || !strings.Contains(body, "anthropic_cache_read_input_tokens") || !strings.Contains(body, "anthropic_cache_creation_input_tokens") {
		t.Fatalf("management page missing anthropic cache accounting labels/fields: %s", body)
	}
	if !strings.Contains(body, "Input Tokens (effective)") || !strings.Contains(body, "effective_input_tokens") {
		t.Fatalf("management page missing effective input token evidence for anthropic rows: %s", body)
	}
	if strings.Contains(body, "Total Input") {
		t.Fatalf("management page should not render the removed Total Input column: %s", body)
	}
	if !strings.Contains(body, "cliproxy-usage-provider-filter") || !strings.Contains(body, "OpenAI compatible Providers") || !strings.Contains(body, "ampcode") || !strings.Contains(body, "url.searchParams.set('provider'") || !strings.Contains(body, "function getUsageProviderFilter()") || !strings.Contains(body, "appendUsageProviderFilter") {
		t.Fatalf("management page missing provider filter controls/options and usage request rewrite: %s", body)
	}
	if !strings.Contains(body, "append-reasoning-effort-to-model-percent") || !strings.Contains(body, "% of matching requests") || !strings.Contains(body, "deterministic percentage-based sampling") {
		t.Fatalf("management page missing openai-compatible percentage control wiring/copy: %s", body)
	}
	if !strings.Contains(body, "hideUsageActionButtons(container);") || !strings.Contains(body, "text !== 'Export' && text !== 'Import' && text !== 'Refresh'") {
		t.Fatalf("management page should hide the native usage Export/Import/Refresh buttons while keeping refresh automation available: %s", body)
	}
	if !strings.Contains(body, "const sameFilter = lastUsageStatisticsProvider === getUsageProviderFilter();") || !strings.Contains(body, "lastUsageStatisticsProvider = getUsageProviderFilter();") {
		t.Fatalf("management page should invalidate cached usage stats when provider filter changes: %s", body)
	}
	if !strings.Contains(body, "readStoredManagementAuthorization") || !strings.Contains(body, "captureManagementAuthFromLoginPage") || !strings.Contains(body, "sessionStorage.setItem('cliproxy-management-key-v1'") {
		t.Fatalf("management page should persist the login key for same-tab usage stats requests when request sniffing does not capture Authorization headers: %s", body)
	}
	if strings.Contains(body, "cliproxy-cache-stats-inline-host") {
		t.Fatalf("management page should not include the removed inline host: %s", body)
	}
	if strings.Contains(body, "Prompt Cache Statistics") || strings.Contains(body, "Open Cache Statistics") {
		t.Fatalf("management page should not include the legacy overlay launcher: %s", body)
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
	if !strings.Contains(body, "patchUsageSummaryCards") || !strings.Contains(body, "setUsageCardValue") || !strings.Contains(body, "summary.total_requests") || !strings.Contains(body, "summary.total_tokens") || !strings.Contains(body, "const tpmCard = findUsageMetricCard(metricsBlock, 'TPM');") || !strings.Contains(body, "const totalCostCard = findUsageMetricCard(metricsBlock, 'Total Cost');") || !strings.Contains(body, "calculateTotalCostFromModelSummaries") || !strings.Contains(body, "MODEL_PRICES_STORAGE_KEY") {
		t.Fatalf("management enhancer should refresh all visible summary cards from persisted cache statistics and stored pricing, not only the request-events table: %s", body)
	}
	if !strings.Contains(body, "function isUsageMetricsBlock(node)") || !strings.Contains(body, "text.includes('Cached Tokens')") || !strings.Contains(body, "text.includes('Avg Latency')") || !strings.Contains(body, "className.includes('statsGrid') && isUsageMetricsBlock(node)") {
		t.Fatalf("management enhancer should keep finding the usage stats grid after the first card rewrite so provider/time-range refreshes continue updating the visible cards: %s", body)
	}
	if !strings.Contains(body, "patchAverageLatencyCard") || !strings.Contains(body, "Avg Latency") || !strings.Contains(body, "summary.avg_latency_ms") {
		t.Fatalf("management enhancer should render an average latency card from the usage summary: %s", body)
	}
	if !strings.Contains(body, "grid-auto-rows: 1fr;") || !strings.Contains(body, ".cliproxy-enhancer__usage-metrics-grid") || !strings.Contains(body, "display: grid !important;") || !strings.Contains(body, "grid-template-columns: repeat(12, minmax(0, 1fr)) !important;") || !strings.Contains(body, "grid-column: span 12 !important;") || !strings.Contains(body, "@media (min-width: 900px)") || !strings.Contains(body, "grid-column: span 6 !important;") || !strings.Contains(body, "@media (min-width: 1280px)") || !strings.Contains(body, "grid-column: span 4 !important;") || !strings.Contains(body, "[class*=\"UsagePage-module__statsGrid\"] > *") || !strings.Contains(body, "height: 132px;") || !strings.Contains(body, "min-height: 132px;") || !strings.Contains(body, "width: 100%;") || !strings.Contains(body, "padding: 16px;") || !strings.Contains(body, "border: 1px solid var(--border-color, #d5d9e0);") || !strings.Contains(body, "border-radius: 12px;") || !strings.Contains(body, "background: var(--bg-primary, #fff);") || !strings.Contains(body, "justify-content: space-between;") || !strings.Contains(body, "-webkit-line-clamp: 2;") || !strings.Contains(body, "metricsBlock.classList.add('cliproxy-enhancer__usage-metrics-grid');") {
		t.Fatalf("management enhancer should normalize all summary sub-cards to one consistent size and style contract after it rewrites their contents: %s", body)
	}
	if !strings.Contains(body, "triggerNativeUsageRefresh") || !strings.Contains(body, "scheduleNativeUsageRefreshRetry") || !strings.Contains(body, "provider.addEventListener('change'") {
		t.Fatalf("management enhancer should also trigger the native usage refresh path so React-owned cards and charts follow provider filter and auto-refresh updates: %s", body)
	}
	if !strings.Contains(body, "scheduleUsagePatchAfterNativeRefresh") || !strings.Contains(body, "window.setTimeout(() => {") || !strings.Contains(body, "refreshUsagePage(false).catch(() => {})") {
		t.Fatalf("management enhancer should re-apply filtered summary-card patches after the native usage refresh completes: %s", body)
	}
	if !strings.Contains(body, "['usage-total-requests', timeRange && timeRange.key || '', getUsageProviderFilter(), totalRequests]") || !strings.Contains(body, "['cached-card', timeRange && timeRange.key || '', getUsageProviderFilter(), cachedTokens") {
		t.Fatalf("management enhancer should encode time-range/provider context into summary-card signatures so filter changes always invalidate stale card markup: %s", body)
	}
	if !strings.Contains(body, "getUsageFilterKey") || !strings.Contains(body, "invalidateUsageRefreshState") || !strings.Contains(body, "scheduleUsageFilterSync") {
		t.Fatalf("management enhancer should track the active usage filter key and invalidate cached refresh state when provider or time-range filters change: %s", body)
	}
	if !strings.Contains(body, "isNativeUsageRequestURL") || !strings.Contains(body, "scheduleUsagePatchAfterNativeRefresh(0)") {
		t.Fatalf("management enhancer should re-apply filtered cards after the native usage endpoint actually returns, not only on speculative timers: %s", body)
	}
	if !strings.Contains(body, "<th>Time</th>") || !strings.Contains(body, "formatDurationMs(item.latency_ms)") {
		t.Fatalf("management enhancer should render request completion time in the Request Events table: %s", body)
	}
	if !strings.Contains(body, "<th>User</th>") || !strings.Contains(body, "normalize(item.customer_email || item.customer_id) || 'Admin'") {
		t.Fatalf("management enhancer should render the trusted X-CheapRouter-User-ID value in the Request Events table and default empty values to Admin: %s", body)
	}
	if !strings.Contains(body, "item.customer_email || ''") {
		t.Fatalf("management enhancer should include customer_email in the Request Events table signature so stored display-email updates repaint existing rows: %s", body)
	}
	if !strings.Contains(body, "applyUsageChartPeriodDefaults") || !strings.Contains(body, "normalize(button.textContent) === 'By Hour'") || !strings.Contains(body, "normalize(button.textContent) === 'By Day'") || !strings.Contains(body, "usageChartDefaultsApplied = true") {
		t.Fatalf("management enhancer should force chart period controls to default to By Hour once per usage page activation: %s", body)
	}
	if !strings.Contains(body, "renderUsageTrendCharts") || !strings.Contains(body, "trend_by_model") || !strings.Contains(body, "Token Usage Trends") || !strings.Contains(body, "Request Trends") {
		t.Fatalf("management enhancer should replace the native aggregate trend charts with provider/time-filtered model trend charts driven by cache statistics: %s", body)
	}
	if strings.Contains(body, "renderUsageModelBreakdownCharts") || strings.Contains(body, "renderTokenTypeBreakdownChart") || strings.Contains(body, "renderCostOverviewChart") {
		t.Fatalf("management enhancer should not include the removed Token Type Breakdown or Cost Overview chart renderers: %s", body)
	}
	if !strings.Contains(body, "captureNativeUsageSnapshotFromResponse") || !strings.Contains(body, "getUsageRenderSnapshot") || !strings.Contains(body, "filterUsageSnapshotDetails") || !strings.Contains(body, "renderCredentialStatistics") || !strings.Contains(body, "renderModelStatistics") {
		t.Fatalf("management enhancer should capture the filtered native usage snapshot and reuse it for the drift-prone usage detail sections: %s", body)
	}
	if !strings.Contains(body, "disableChartAnimationOptions") || !strings.Contains(body, "scheduleChartAnimationDisable") || !strings.Contains(body, "canvas.$chartjs") || !strings.Contains(body, "options.animation = false;") || !strings.Contains(body, "findChartInstanceForCanvas") || !strings.Contains(body, "chart.update('none')") {
		t.Fatalf("management enhancer should disable canvas chart animations through both the chart fiber options path and the live chart instance redraw path so all management charts redraw without motion: %s", body)
	}
}

func TestEnhanceManagementControlPanelHTMLReplacesExistingInjectedOverlay(t *testing.T) {
	page := []byte(`<html><body><main>panel</main><!-- cliproxy-cache-stats-overlay --><script>window.__cliproxyManagementEnhancer=true;const staleOverlay=true;</script></body></html>`)

	body := string(enhanceManagementControlPanelHTML(page))

	startComment := "<!-- " + managementCacheStatisticsMarker + " -->"
	endComment := "<!-- " + managementCacheStatisticsEndMarker + " -->"
	if strings.Count(body, startComment) != 1 {
		t.Fatalf("overlay start marker count = %d, want 1; body=%s", strings.Count(body, startComment), body)
	}
	if strings.Count(body, endComment) != 1 {
		t.Fatalf("overlay end marker count = %d, want 1; body=%s", strings.Count(body, endComment), body)
	}
	if strings.Contains(body, "<!-- <!--") {
		t.Fatalf("management enhancer replacement should not leave nested comment prefixes behind: %s", body)
	}
	if strings.Contains(body, "staleOverlay=true") {
		t.Fatalf("management enhancer should replace stale injected overlays instead of preserving them: %s", body)
	}
	if !strings.Contains(body, "function isUsageMetricsBlock(node)") || !strings.Contains(body, "<th>User</th>") || !strings.Contains(body, managementCacheStatisticsEndMarker) {
		t.Fatalf("management enhancer replacement should inject the current overlay content: %s", body)
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
				TotalRequests     int64   `json:"total_requests"`
				CachedTokens      int64   `json:"cached_tokens"`
				SuccessPercentage float64 `json:"success_percentage"`
				GPT54             struct {
					Standard struct {
						RequestCount int64 `json:"request_count"`
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"standard"`
					LongContext struct {
						RequestCount int64 `json:"request_count"`
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"long_context"`
				} `json:"gpt_5_4"`
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
	if payload.CacheStatistics.Summary.SuccessPercentage != 100 {
		t.Fatalf("success_percentage = %v, want 100", payload.CacheStatistics.Summary.SuccessPercentage)
	}
	if payload.CacheStatistics.Summary.GPT54.Standard.RequestCount != 1 {
		t.Fatalf("gpt_5_4.standard.request_count = %d, want 1", payload.CacheStatistics.Summary.GPT54.Standard.RequestCount)
	}
	if payload.CacheStatistics.Summary.GPT54.Standard.InputTokens != 1000 {
		t.Fatalf("gpt_5_4.standard.input_tokens = %d, want 1000", payload.CacheStatistics.Summary.GPT54.Standard.InputTokens)
	}
	if payload.CacheStatistics.Summary.GPT54.Standard.OutputTokens != 100 {
		t.Fatalf("gpt_5_4.standard.output_tokens = %d, want 100", payload.CacheStatistics.Summary.GPT54.Standard.OutputTokens)
	}
	if payload.CacheStatistics.Summary.GPT54.LongContext.RequestCount != 0 {
		t.Fatalf("gpt_5_4.long_context.request_count = %d, want 0", payload.CacheStatistics.Summary.GPT54.LongContext.RequestCount)
	}
	if payload.CacheStatistics.Summary.GPT54.LongContext.InputTokens != 0 {
		t.Fatalf("gpt_5_4.long_context.input_tokens = %d, want 0", payload.CacheStatistics.Summary.GPT54.LongContext.InputTokens)
	}
	if payload.CacheStatistics.Summary.GPT54.LongContext.OutputTokens != 0 {
		t.Fatalf("gpt_5_4.long_context.output_tokens = %d, want 0", payload.CacheStatistics.Summary.GPT54.LongContext.OutputTokens)
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

func TestManagementCacheStatisticsEndpointProviderFilterGroupsProviders(t *testing.T) {
	server := newManagementTestServer(t)
	server.cfg.OpenAICompatibility = []proxyconfig.OpenAICompatibility{
		{
			Name:    "Bohe",
			BaseURL: "https://bohe.example.com/v1",
		},
	}
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	now := time.Now().UTC().Truncate(time.Second)
	events := []usage.CacheStatisticsEvent{
		{
			Timestamp: now.Add(-6 * time.Minute),
			Provider:  "codex",
			Model:     "gpt-5.4",
			Tokens:    usage.TokenStats{InputTokens: 100, OutputTokens: 10, TotalTokens: 110},
		},
		{
			Timestamp: now.Add(-5 * time.Minute),
			Provider:  "gemini",
			Model:     "gemini-2.5-pro",
			Tokens:    usage.TokenStats{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
		{
			Timestamp: now.Add(-4 * time.Minute),
			Provider:  "gemini-cli",
			Model:     "gemini-2.5-flash",
			Tokens:    usage.TokenStats{InputTokens: 20, OutputTokens: 5, TotalTokens: 25},
		},
		{
			Timestamp: now.Add(-3 * time.Minute),
			Provider:  "openai-compatibility",
			Model:     "gpt-4.1",
			Tokens:    usage.TokenStats{InputTokens: 30, OutputTokens: 5, TotalTokens: 35},
		},
		{
			Timestamp: now.Add(-2 * time.Minute),
			Provider:  "openrouter",
			Model:     "gpt-4.1-mini",
			Tokens:    usage.TokenStats{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
		},
		{
			Timestamp: now.Add(-90 * time.Second),
			Provider:  "bohe",
			Model:     "gpt-4.1-nano",
			Tokens:    usage.TokenStats{InputTokens: 35, OutputTokens: 5, TotalTokens: 40},
		},
		{
			Timestamp: now.Add(-1 * time.Minute),
			Provider:  "claude",
			Model:     "claude-sonnet-4-6",
			Tokens:    usage.TokenStats{InputTokens: 50, OutputTokens: 5, TotalTokens: 55},
		},
	}
	for _, event := range events {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	assertProviders := func(path string, wantTotal int64, wantGPT54StandardRequests int64, wantGPT54LongContextRequests int64, wantProviders ...string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-secret")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var payload struct {
			CacheStatistics struct {
				Summary struct {
					TotalRequests int64 `json:"total_requests"`
					GPT54         struct {
						Standard struct {
							RequestCount int64 `json:"request_count"`
						} `json:"standard"`
						LongContext struct {
							RequestCount int64 `json:"request_count"`
						} `json:"long_context"`
					} `json:"gpt_5_4"`
				} `json:"summary"`
				RecentRequests []struct {
					Provider string `json:"provider"`
				} `json:"recent_requests"`
			} `json:"cache_statistics"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if payload.CacheStatistics.Summary.TotalRequests != wantTotal {
			t.Fatalf("total_requests = %d, want %d", payload.CacheStatistics.Summary.TotalRequests, wantTotal)
		}
		if payload.CacheStatistics.Summary.GPT54.Standard.RequestCount != wantGPT54StandardRequests {
			t.Fatalf("gpt_5_4.standard.request_count = %d, want %d", payload.CacheStatistics.Summary.GPT54.Standard.RequestCount, wantGPT54StandardRequests)
		}
		if payload.CacheStatistics.Summary.GPT54.LongContext.RequestCount != wantGPT54LongContextRequests {
			t.Fatalf("gpt_5_4.long_context.request_count = %d, want %d", payload.CacheStatistics.Summary.GPT54.LongContext.RequestCount, wantGPT54LongContextRequests)
		}
		if len(payload.CacheStatistics.RecentRequests) != len(wantProviders) {
			t.Fatalf("recent requests len = %d, want %d", len(payload.CacheStatistics.RecentRequests), len(wantProviders))
		}
		got := make([]string, 0, len(payload.CacheStatistics.RecentRequests))
		for _, item := range payload.CacheStatistics.RecentRequests {
			got = append(got, item.Provider)
		}
		for _, want := range wantProviders {
			found := false
			for _, candidate := range got {
				if candidate == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("providers = %v, want to contain %q", got, want)
			}
		}
	}

	assertProviders("/v0/management/cache-statistics?days=7&limit=10&model_limit=10&provider=codex", 1, 1, 0, "codex")
	assertProviders("/v0/management/cache-statistics?days=7&limit=10&model_limit=10&provider=gemini", 2, 0, 0, "gemini", "gemini-cli")
	assertProviders("/v0/management/cache-statistics?days=7&limit=10&model_limit=10&provider=openai-compatible", 3, 0, 0, "openai-compatibility", "openrouter", "bohe")
	assertProviders("/v0/management/cache-statistics?days=7&limit=10&model_limit=10&provider=claude", 1, 0, 0, "claude")
}

func TestManagementCacheStatisticsEndpointIncludesAnthropicEffectiveInputTokens(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	event := usage.CacheStatisticsEvent{
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Provider:  "claude",
		Model:     "claude-opus-4-6",
		Tokens: usage.TokenStats{
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

	req := httptest.NewRequest(http.MethodGet, "/v0/management/cache-statistics?days=7&limit=10&model_limit=10&provider=claude", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		CacheStatistics struct {
			Summary struct {
				EffectiveInputTokens int64 `json:"effective_input_tokens"`
			} `json:"summary"`
			RecentRequests []struct {
				EffectiveInputTokens int64 `json:"effective_input_tokens"`
			} `json:"recent_requests"`
		} `json:"cache_statistics"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.CacheStatistics.Summary.EffectiveInputTokens != 165689 {
		t.Fatalf("summary effective_input_tokens = %d, want 165689", payload.CacheStatistics.Summary.EffectiveInputTokens)
	}
	if len(payload.CacheStatistics.RecentRequests) != 1 {
		t.Fatalf("recent requests len = %d, want 1", len(payload.CacheStatistics.RecentRequests))
	}
	if payload.CacheStatistics.RecentRequests[0].EffectiveInputTokens != 165689 {
		t.Fatalf("recent effective_input_tokens = %d, want 165689", payload.CacheStatistics.RecentRequests[0].EffectiveInputTokens)
	}
}

func TestManagementCacheStatisticsEndpointIncludesCustomerEmail(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	svc, err := customerstate.NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	customerstate.SetDefaultService(svc)
	t.Cleanup(func() { customerstate.SetDefaultService(nil) })

	customer, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{
		ID:    "cust_5080263b-fbd1-485a-b017-13437036d8e2",
		Email: "client@example.com",
	})
	if err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}
	if customer.Email != "client@example.com" {
		t.Fatalf("customer email = %q, want %q", customer.Email, "client@example.com")
	}

	event := usage.CacheStatisticsEvent{
		Timestamp:  time.Now().UTC().Truncate(time.Second),
		Provider:   "codex",
		Model:      "gpt-5.4",
		CustomerID: customer.ID,
		Tokens: usage.TokenStats{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
		},
	}
	if err := store.InsertEvent(context.Background(), event); err != nil {
		t.Fatalf("InsertEvent() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/cache-statistics?days=7&limit=10&model_limit=10", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		CacheStatistics struct {
			RecentRequests []struct {
				CustomerID    string `json:"customer_id"`
				CustomerEmail string `json:"customer_email"`
			} `json:"recent_requests"`
		} `json:"cache_statistics"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(payload.CacheStatistics.RecentRequests) != 1 {
		t.Fatalf("recent requests len = %d, want 1", len(payload.CacheStatistics.RecentRequests))
	}
	if payload.CacheStatistics.RecentRequests[0].CustomerID != customer.ID {
		t.Fatalf("recent customer_id = %q, want %q", payload.CacheStatistics.RecentRequests[0].CustomerID, customer.ID)
	}
	if payload.CacheStatistics.RecentRequests[0].CustomerEmail != customer.Email {
		t.Fatalf("recent customer_email = %q, want %q", payload.CacheStatistics.RecentRequests[0].CustomerEmail, customer.Email)
	}
}

func TestManagementCacheStatisticsEndpointPreservesStoredCustomerEmail(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	event := usage.CacheStatisticsEvent{
		Timestamp:     time.Now().UTC().Truncate(time.Second),
		Provider:      "codex",
		Model:         "gpt-5.4",
		CustomerID:    "cust-direct",
		CustomerEmail: "direct@example.com",
		Tokens: usage.TokenStats{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
		},
	}
	if err := store.InsertEvent(context.Background(), event); err != nil {
		t.Fatalf("InsertEvent() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/cache-statistics?days=7&limit=10&model_limit=10", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		CacheStatistics struct {
			RecentRequests []struct {
				CustomerID    string `json:"customer_id"`
				CustomerEmail string `json:"customer_email"`
			} `json:"recent_requests"`
		} `json:"cache_statistics"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(payload.CacheStatistics.RecentRequests) != 1 {
		t.Fatalf("recent requests len = %d, want 1", len(payload.CacheStatistics.RecentRequests))
	}
	if payload.CacheStatistics.RecentRequests[0].CustomerID != "cust-direct" {
		t.Fatalf("recent customer_id = %q, want %q", payload.CacheStatistics.RecentRequests[0].CustomerID, "cust-direct")
	}
	if payload.CacheStatistics.RecentRequests[0].CustomerEmail != "direct@example.com" {
		t.Fatalf("recent customer_email = %q, want %q", payload.CacheStatistics.RecentRequests[0].CustomerEmail, "direct@example.com")
	}
}

func TestManagementUsageEndpointUsesPersistedCacheStatistics(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
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

	decodeUsage := func(path string) struct {
		Usage          usage.StatisticsSnapshot `json:"usage"`
		FailedRequests int64                    `json:"failed_requests"`
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-secret")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", path, rr.Code, http.StatusOK, rr.Body.String())
		}
		var payload struct {
			Usage          usage.StatisticsSnapshot `json:"usage"`
			FailedRequests int64                    `json:"failed_requests"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode response for %s: %v", path, err)
		}
		return payload
	}

	payload := decodeUsage("/v0/management/usage")
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

	filtered := decodeUsage("/v0/management/usage?provider=claude")
	if filtered.Usage.TotalRequests != 0 || filtered.Usage.TotalTokens != 0 || filtered.FailedRequests != 0 {
		t.Fatalf("filtered usage = %+v, want empty snapshot for unmatched provider", filtered)
	}
	if len(filtered.Usage.APIs) != 0 {
		t.Fatalf("filtered apis len = %d, want 0", len(filtered.Usage.APIs))
	}
}

func TestManagementUsageEndpointMergesPersistedAndLiveInMemoryStatistics(t *testing.T) {
	server := newManagementTestServer(t)
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	now := time.Now().UTC().Truncate(time.Second)
	persisted := usage.CacheStatisticsEvent{
		Timestamp:       now.Add(-2 * time.Minute),
		Provider:        "codex",
		Model:           "gpt-5.4",
		ReasoningEffort: "medium",
		Source:          "persisted@example.com",
		AuthID:          "codex-persisted.json",
		AuthIndex:       "idx-persisted",
		LatencyMs:       1200,
		Tokens:          usage.TokenStats{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	}
	if err := store.InsertEvent(context.Background(), persisted); err != nil {
		t.Fatalf("InsertEvent() error = %v", err)
	}

	liveStats := usage.NewRequestStatistics()
	server.mgmt.SetUsageStatistics(liveStats)
	liveStats.Record(context.Background(), coreusage.Record{
		Provider:        "codex",
		Model:           "gpt-5.4-mini",
		APIKey:          "live-key-456",
		ReasoningEffort: "high",
		RequestedAt:     now.Add(-30 * time.Second),
		Source:          "live@example.com",
		AuthID:          "codex-live.json",
		AuthIndex:       "idx-live",
		Detail: coreusage.Detail{
			InputTokens:  55,
			OutputTokens: 11,
			TotalTokens:  66,
		},
	})
	liveStats.Record(context.Background(), coreusage.Record{
		Provider:        "claude",
		Model:           "claude-sonnet-4-5",
		APIKey:          "claude-live-key-789",
		ReasoningEffort: "medium",
		RequestedAt:     now.Add(-20 * time.Second),
		Source:          "claude-live@example.com",
		AuthID:          "claude-live.json",
		AuthIndex:       "idx-claude-live",
		Detail: coreusage.Detail{
			InputTokens:  90,
			OutputTokens: 10,
			TotalTokens:  100,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage?provider=codex", nil)
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
	if payload.Usage.TotalTokens != 186 {
		t.Fatalf("total_tokens = %d, want 186", payload.Usage.TotalTokens)
	}
	if payload.Usage.SuccessCount != 2 || payload.Usage.FailureCount != 0 || payload.FailedRequests != 0 {
		t.Fatalf("success/failure counts = %+v failed_requests=%d, want 2/0/0", payload.Usage, payload.FailedRequests)
	}
	if _, ok := payload.Usage.APIs["codex-persisted.json"]; !ok {
		t.Fatalf("missing persisted api bucket")
	}
	if _, ok := payload.Usage.APIs["live-key-456"]; !ok {
		t.Fatalf("missing live in-memory api bucket")
	}
	if _, ok := payload.Usage.APIs["claude-live-key-789"]; ok {
		t.Fatalf("unexpected provider-mismatched live api bucket in filtered snapshot")
	}
}

func TestManagementUsageEndpointFiltersOpenAICompatibleProviderGroup(t *testing.T) {
	server := newManagementTestServer(t)
	server.cfg.OpenAICompatibility = []proxyconfig.OpenAICompatibility{
		{
			Name:    "Bohe",
			BaseURL: "https://bohe.example.com/v1",
		},
	}
	store := usage.GetCacheStatisticsStore()
	if store == nil {
		t.Fatal("expected cache statistics store to be configured")
	}

	now := time.Now().UTC().Truncate(time.Second)
	for _, event := range []usage.CacheStatisticsEvent{
		{
			Timestamp: now.Add(-2 * time.Minute),
			Provider:  "openrouter",
			Model:     "gpt-4.1",
			AuthID:    "openrouter-persisted.json",
			AuthIndex: "idx-openrouter",
			Tokens:    usage.TokenStats{InputTokens: 20, OutputTokens: 5, TotalTokens: 25},
		},
		{
			Timestamp: now.Add(-90 * time.Second),
			Provider:  "bohe",
			Model:     "gpt-4.1-mini",
			AuthID:    "bohe-persisted.json",
			AuthIndex: "idx-bohe",
			Tokens:    usage.TokenStats{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		},
	} {
		if err := store.InsertEvent(context.Background(), event); err != nil {
			t.Fatalf("InsertEvent() error = %v", err)
		}
	}

	liveStats := usage.NewRequestStatistics()
	server.mgmt.SetUsageStatistics(liveStats)
	liveStats.Record(context.Background(), coreusage.Record{
		Provider:    "bohe",
		Model:       "gpt-4.1-nano",
		APIKey:      "bohe-live-key",
		RequestedAt: now.Add(-30 * time.Second),
		AuthID:      "bohe-live.json",
		AuthIndex:   "idx-bohe-live",
		Detail:      coreusage.Detail{InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
	})
	liveStats.Record(context.Background(), coreusage.Record{
		Provider:    "claude",
		Model:       "claude-sonnet-4-6",
		APIKey:      "claude-live-key",
		RequestedAt: now.Add(-20 * time.Second),
		AuthID:      "claude-live.json",
		AuthIndex:   "idx-claude-live",
		Detail:      coreusage.Detail{InputTokens: 100, OutputTokens: 10, TotalTokens: 110},
	})

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage?provider=openai-compatible", nil)
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

	if payload.Usage.TotalRequests != 3 {
		t.Fatalf("total_requests = %d, want 3", payload.Usage.TotalRequests)
	}
	if payload.Usage.TotalTokens != 41 {
		t.Fatalf("total_tokens = %d, want 41", payload.Usage.TotalTokens)
	}
	if _, ok := payload.Usage.APIs["openrouter-persisted.json"]; !ok {
		t.Fatalf("missing openrouter persisted bucket")
	}
	if _, ok := payload.Usage.APIs["bohe-persisted.json"]; !ok {
		t.Fatalf("missing bohe persisted bucket")
	}
	if _, ok := payload.Usage.APIs["bohe-live-key"]; !ok {
		t.Fatalf("missing bohe live bucket")
	}
	if _, ok := payload.Usage.APIs["claude-live-key"]; ok {
		t.Fatalf("unexpected non-openai-compatible live bucket in filtered snapshot")
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

func assertCodexSupportedReasoningLevels(t *testing.T, model map[string]any, want []string) {
	t.Helper()

	rawLevels, ok := model["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("expected supported_reasoning_levels, got %#v", model["supported_reasoning_levels"])
	}
	if len(rawLevels) != len(want) {
		t.Fatalf("supported_reasoning_levels length = %d, want %d: %#v", len(rawLevels), len(want), rawLevels)
	}
	for index, rawLevel := range rawLevels {
		levelEntry, ok := rawLevel.(map[string]any)
		if !ok {
			t.Fatalf("supported_reasoning_levels[%d] = %#v, want object", index, rawLevel)
		}
		if got, _ := levelEntry["effort"].(string); got != want[index] {
			t.Fatalf("supported_reasoning_levels[%d].effort = %q, want %q", index, got, want[index])
		}
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

func TestInstallerPlatformEntryPointsExist(t *testing.T) {
	_, filePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filePath), "..", ".."))

	for _, rel := range []string{"install_mac.sh", "install_linux.sh", "install_windows.ps1"} {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}

	shimBytes, err := os.ReadFile(filepath.Join(repoRoot, "install_mac.sh"))
	if err != nil {
		t.Fatalf("failed to read installer script: %v", err)
	}
	if !strings.Contains(string(shimBytes), "install_mac.sh") {
		t.Fatalf("expected install.sh compatibility shim to delegate to install_mac.sh")
	}
}

func TestInstallerPlatformScriptsContainExpectedServiceSetup(t *testing.T) {
	_, filePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filePath), "..", ".."))

	linuxBytes, err := os.ReadFile(filepath.Join(repoRoot, "install_linux.sh"))
	if err != nil {
		t.Fatalf("failed to read install_linux.sh: %v", err)
	}
	linuxBody := string(linuxBytes)
	if !strings.Contains(linuxBody, "systemctl --user") {
		t.Fatalf("expected install_linux.sh to use systemctl --user")
	}
	if !strings.Contains(linuxBody, "systemctl --user restart") {
		t.Fatalf("expected install_linux.sh to restart the user service after creating the unit")
	}
	if strings.Contains(linuxBody, "launchctl") {
		t.Fatalf("install_linux.sh should not reference launchctl")
	}
	if strings.Contains(linuxBody, "Start service after install?") {
		t.Fatalf("install_linux.sh should start the user service automatically once it is created")
	}
	if !strings.Contains(linuxBody, "validate_postgres_bootstrap_permissions") {
		t.Fatalf("expected install_linux.sh to preflight Postgres bootstrap permissions before writing PGSTORE env")
	}
	if !strings.Contains(linuxBody, "cannot create proxy tables in schema") {
		t.Fatalf("expected install_linux.sh to fail fast when Postgres can connect but cannot create proxy tables")
	}

	windowsBytes, err := os.ReadFile(filepath.Join(repoRoot, "install_windows.ps1"))
	if err != nil {
		t.Fatalf("failed to read install_windows.ps1: %v", err)
	}
	windowsBody := string(windowsBytes)
	if !strings.Contains(windowsBody, "Register-ScheduledTask") || !strings.Contains(windowsBody, "Start-ScheduledTask") {
		t.Fatalf("expected install_windows.ps1 to manage a scheduled task")
	}
}

func TestInstallWindowsScriptContainsPostgresBootstrap(t *testing.T) {
	_, filePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filePath), "..", ".."))

	windowsBytes, err := os.ReadFile(filepath.Join(repoRoot, "install_windows.ps1"))
	if err != nil {
		t.Fatalf("failed to read install_windows.ps1: %v", err)
	}
	windowsBody := string(windowsBytes)

	for _, needle := range []string{
		"CLI_PROXY_INSTALLER_POSTGRES_INSTALLER_URL",
		"CLI_PROXY_INSTALLER_POSTGRES_SERVICE_NAME",
		"Install-ManagedPostgres",
		"get.enterprisedb.com/postgresql/postgresql-",
		"'--mode', 'unattended'",
		"'--servicename', $script:ManagedPostgresServiceName",
	} {
		if !strings.Contains(windowsBody, needle) {
			t.Fatalf("expected install_windows.ps1 to contain PostgreSQL bootstrap marker %q", needle)
		}
	}
}

func TestInstallWindowsScriptContainsPostgresBootstrapSupport(t *testing.T) {
	_, filePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filePath), "..", ".."))

	windowsBytes, err := os.ReadFile(filepath.Join(repoRoot, "install_windows.ps1"))
	if err != nil {
		t.Fatalf("failed to read install_windows.ps1: %v", err)
	}
	windowsBody := string(windowsBytes)

	for _, needle := range []string{
		"Wait-ManagedPostgresReady",
		"Resolve-PostgresTool 'pg_isready'",
		"Get-Service -Name $script:ManagedPostgresServiceName",
		"Start-Process -FilePath $installerPath -ArgumentList $installerArgs -Wait -PassThru",
		"Resolve-PostgresMaintenanceDsn",
		"Build-ManagedPostgresMaintenanceDsn",
	} {
		if !strings.Contains(windowsBody, needle) {
			t.Fatalf("expected install_windows.ps1 to include PostgreSQL bootstrap support containing %q", needle)
		}
	}
}

func TestInstallScriptRefusesSkipBuildWhenExistingBinaryWouldHideSourceChanges(t *testing.T) {
	_, filePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filePath), "..", ".."))
	installRoot := t.TempDir()
	authDir := filepath.Join(installRoot, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	binaryPath := filepath.Join(installRoot, "cli-proxy-api")
	if err := os.WriteFile(binaryPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to seed existing binary: %v", err)
	}

	scriptBytes, err := os.ReadFile(filepath.Join(repoRoot, "install_mac.sh"))
	if err != nil {
		t.Fatalf("failed to read installer script: %v", err)
	}
	scriptBody := strings.TrimSuffix(string(scriptBytes), "main \"$@\"\n")
	scriptBody = strings.TrimSuffix(scriptBody, "main \"$@\"")
	harness := fmt.Sprintf(`%s
prompt_with_default() {
  case "$1" in
    "Install location") printf '%%s' %q ;;
    "Auth folder") printf '%%s' %q ;;
    *) printf '%%s' '' ;;
  esac
}
confirm_yes_no() {
  case "$1" in
    "Build binary from source now?") return 1 ;;
    "Create launchd service?") return 1 ;;
    *) return 1 ;;
  esac
}
require_tools() { :; }
detect_sources() { CONFIG_SOURCES=(); AUTH_SOURCES=(); DB_SOURCES=(); BINARY_SOURCES=(); }
print_detection_summary() { :; }
choose_config_source() { return 1; }
copy_or_patch_config() { :; }
append_sources_excluding_target() { :; }
ensure_cache_stats_schema() { :; }
print_next_steps() { :; }
main
`, scriptBody, installRoot, authDir)
	harnessPath := filepath.Join(t.TempDir(), "install-harness.sh")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o700); err != nil {
		t.Fatalf("failed to write install harness: %v", err)
	}

	cmd := exec.Command("bash", harnessPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected install_mac.sh to fail when build is skipped and an existing binary would hide source changes; output=%s", string(output))
	}
	if !strings.Contains(string(output), "source changes") {
		t.Fatalf("expected skip-build failure to mention source changes, got: %s", string(output))
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
