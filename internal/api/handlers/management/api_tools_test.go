package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestIsQuotaRefreshAPICallMatchesKnownQuotaEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "antigravity models", raw: "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", want: true},
		{name: "gemini quota", raw: "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota", want: true},
		{name: "gemini code assist", raw: "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", want: true},
		{name: "claude quota", raw: "https://api.anthropic.com/api/oauth/usage", want: true},
		{name: "codex quota", raw: "https://chatgpt.com/backend-api/wham/usage", want: true},
		{name: "kimi quota", raw: "https://api.kimi.com/coding/v1/usages", want: true},
		{name: "unrelated endpoint", raw: "https://api.example.com/v1/ping", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", tt.raw, err)
			}
			if got := isQuotaRefreshAPICall(http.MethodPost, parsed); got != tt.want {
				t.Fatalf("isQuotaRefreshAPICall(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestAPICallThrottlesQuotaRefreshFanoutRequests(t *testing.T) {
	t.Parallel()

	const requestCount = 3

	var current int32
	var peak int32
	entered := make(chan struct{}, requestCount)
	release := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&current, 1)
		for {
			prev := atomic.LoadInt32(&peak)
			if cur <= prev {
				break
			}
			if atomic.CompareAndSwapInt32(&peak, prev, cur) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		atomic.AddInt32(&current, -1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	h := &Handler{}
	var wg sync.WaitGroup
	recorders := make([]*httptest.ResponseRecorder, requestCount)
	errCh := make(chan error, requestCount)
	requestURL := upstream.URL + "/v1internal:fetchAvailableModels"

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			payload, errMarshal := json.Marshal(apiCallRequest{
				Method: http.MethodPost,
				URL:    requestURL,
				Data:   "{}",
			})
			if errMarshal != nil {
				errCh <- errMarshal
				return
			}
			req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			h.APICall(ctx)
			recorders[index] = rec
		}(i)
	}

	waitForEnter := func(want int) {
		t.Helper()
		for i := 0; i < want; i++ {
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for %d throttled upstream requests", want)
			}
		}
	}

	initialBurst := quotaRefreshAPICallMaxConcurrent
	if initialBurst > requestCount {
		initialBurst = requestCount
	}
	waitForEnter(initialBurst)

	if requestCount > quotaRefreshAPICallMaxConcurrent {
		select {
		case <-entered:
			t.Fatalf("quota refresh fanout exceeded throttle limit %d", quotaRefreshAPICallMaxConcurrent)
		case <-time.After(150 * time.Millisecond):
		}
	}

	close(release)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent APICall setup failed: %v", err)
		}
	}
	if got := atomic.LoadInt32(&peak); got > quotaRefreshAPICallMaxConcurrent {
		t.Fatalf("peak upstream concurrency = %d, want <= %d", got, quotaRefreshAPICallMaxConcurrent)
	}
	for i, rec := range recorders {
		if rec == nil {
			t.Fatalf("missing recorder for request %d", i)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want %d (body=%s)", i, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}
