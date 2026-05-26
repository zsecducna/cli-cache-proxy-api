package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestShouldSkipMethodForRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		skip bool
	}{
		{
			name: "nil request",
			req:  nil,
			skip: true,
		},
		{
			name: "post request should not skip",
			req: &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Path: "/v1/responses"},
			},
			skip: false,
		},
		{
			name: "plain get should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/models"},
				Header: http.Header{},
			},
			skip: true,
		},
		{
			name: "responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "responses get without upgrade should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{},
			},
			skip: true,
		},
	}

	for i := range tests {
		got := shouldSkipMethodForRequestLogging(tests[i].req)
		if got != tests[i].skip {
			t.Fatalf("%s: got skip=%t, want %t", tests[i].name, got, tests[i].skip)
		}
	}
}

func TestShouldCaptureRequestBody(t *testing.T) {
	tests := []struct {
		name          string
		loggerEnabled bool
		req           *http.Request
		want          bool
	}{
		{
			name:          "logger enabled always captures",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "nil request",
			loggerEnabled: false,
			req:           nil,
			want:          false,
		},
		{
			name:          "small known size json in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "large known size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: maxErrorOnlyCapturedRequestBodyBytes + 1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "unknown size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "multipart skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: 1,
				Header:        http.Header{"Content-Type": []string{"multipart/form-data; boundary=abc"}},
			},
			want: false,
		},
	}

	for i := range tests {
		got := shouldCaptureRequestBody(tests[i].loggerEnabled, tests[i].req)
		if got != tests[i].want {
			t.Fatalf("%s: got %t, want %t", tests[i].name, got, tests[i].want)
		}
	}
}

func TestAttachWebsocketLogSourcesUsesLoggerLogsDir(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 0)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("Upgrade", "websocket")

	attachWebsocketLogSources(c, logger, true)
	defer cleanupFileBodySourcesFromContext(c)

	for _, key := range []string{
		logging.WebsocketTimelineSourceContextKey,
		logging.APIWebsocketTimelineSourceContextKey,
	} {
		value, exists := c.Get(key)
		if !exists {
			t.Fatalf("expected %s source to be attached", key)
		}
		source, ok := value.(*logging.FileBodySource)
		if !ok || source == nil {
			t.Fatalf("%s source type = %T", key, value)
		}
		file, errPart := source.CreatePart("probe")
		if errPart != nil {
			t.Fatalf("CreatePart(%s): %v", key, errPart)
		}
		path := file.Name()
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("close part: %v", errClose)
		}
		if !strings.HasPrefix(path, logsDir+string(os.PathSeparator)) {
			t.Fatalf("%s part path %s is not under logs dir %s", key, path, logsDir)
		}
	}
}

func cleanupFileBodySourcesFromContext(c *gin.Context) {
	if c == nil {
		return
	}
	for _, key := range []string{
		logging.WebsocketTimelineSourceContextKey,
		logging.APIWebsocketTimelineSourceContextKey,
	} {
		value, exists := c.Get(key)
		if !exists {
			continue
		}
		if source, ok := value.(*logging.FileBodySource); ok && source != nil {
			_ = source.Cleanup()
		}
	}
}

func TestCaptureRequestInfoDecodesZstdRequestBodyForLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := []byte(`{"model":"test-model","stream":true}`)
	var compressed bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&compressed)
	if errNewWriter != nil {
		t.Fatalf("zstd.NewWriter: %v", errNewWriter)
	}
	if _, errWrite := encoder.Write(payload); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	compressedBytes := compressed.Bytes()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressedBytes))
	req.Header.Set("Content-Encoding", "zstd")
	c.Request = req

	info, errCapture := captureRequestInfo(c, true)
	if errCapture != nil {
		t.Fatalf("captureRequestInfo: %v", errCapture)
	}
	if !bytes.Equal(info.Body, payload) {
		t.Fatalf("logged request body = %q, want %q", string(info.Body), string(payload))
	}

	restoredBody, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		t.Fatalf("read restored request body: %v", errRead)
	}
	if !bytes.Equal(restoredBody, compressedBytes) {
		t.Fatal("request body was not restored with the original compressed bytes")
	}
}

func TestRequestLoggingMiddleware_SkipsHeadRootProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.HEAD("/", func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if logger.logRequestCalls != 0 {
		t.Fatalf("LogRequest calls = %d, want 0", logger.logRequestCalls)
	}
	if logger.logRequestWithOptionsCalls != 0 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 0", logger.logRequestWithOptionsCalls)
	}
}

func TestRequestLoggingMiddleware_ForcesLogOnRealProxyError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("API_UPSTREAM_ATTEMPTED", true)
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream failure"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if logger.logRequestWithOptionsCalls != 1 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 1", logger.logRequestWithOptionsCalls)
	}
	if !logger.lastForce {
		t.Fatal("expected forced log on real proxy error when logger is disabled")
	}
	if logger.lastMethod != http.MethodPost {
		t.Fatalf("last method = %q, want %q", logger.lastMethod, http.MethodPost)
	}
	if logger.lastURL != "/v1/messages" {
		t.Fatalf("last url = %q, want %q", logger.lastURL, "/v1/messages")
	}
}

func TestRequestLoggingMiddleware_ForcesLogOnResponsesProxyError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/v1/responses", func(c *gin.Context) {
		c.Set("API_UPSTREAM_ATTEMPTED", true)
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream failure"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if logger.logRequestWithOptionsCalls != 1 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 1", logger.logRequestWithOptionsCalls)
	}
	if !logger.lastForce {
		t.Fatal("expected forced log on /v1/responses proxy error when logger is disabled")
	}
	if logger.lastURL != "/v1/responses" {
		t.Fatalf("last url = %q, want %q", logger.lastURL, "/v1/responses")
	}
}

func TestRequestLoggingMiddleware_SkipsResponsesAuthErrorBeforeProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/v1/responses", func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing api key"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if logger.logRequestCalls != 0 {
		t.Fatalf("LogRequest calls = %d, want 0", logger.logRequestCalls)
	}
	if logger.logRequestWithOptionsCalls != 0 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 0", logger.logRequestWithOptionsCalls)
	}
}

func TestRequestLoggingMiddleware_SkipsMessagesValidationErrorBeforeProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if logger.logRequestCalls != 0 {
		t.Fatalf("LogRequest calls = %d, want 0", logger.logRequestCalls)
	}
	if logger.logRequestWithOptionsCalls != 0 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 0", logger.logRequestWithOptionsCalls)
	}
}

func TestRequestLoggingMiddleware_SkipsNonProxyErrorsInErrorOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "non-proxy failure"})
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if logger.logRequestCalls != 0 {
		t.Fatalf("LogRequest calls = %d, want 0", logger.logRequestCalls)
	}
	if logger.logRequestWithOptionsCalls != 0 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 0", logger.logRequestWithOptionsCalls)
	}
}

func TestRequestLoggingMiddleware_SkipsCancelledProxyRequestInErrorOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("API_UPSTREAM_ATTEMPTED", true)
		c.Status(clientClosedRequestStatusCode)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != clientClosedRequestStatusCode {
		t.Fatalf("status code = %d, want %d", rec.Code, clientClosedRequestStatusCode)
	}
	if logger.logRequestCalls != 0 {
		t.Fatalf("LogRequest calls = %d, want 0", logger.logRequestCalls)
	}
	if logger.logRequestWithOptionsCalls != 0 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 0", logger.logRequestWithOptionsCalls)
	}
}

func TestRequestLoggingMiddleware_SkipsMarkedCanceledRequestTimeoutInErrorOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := &recordingRequestLogger{enabled: false}

	engine := gin.New()
	engine.Use(RequestLoggingMiddleware(logger))
	engine.POST("/v1/messages", func(c *gin.Context) {
		c.Set("API_UPSTREAM_ATTEMPTED", true)
		logging.MarkRequestCanceled(c)
		c.Status(http.StatusRequestTimeout)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusRequestTimeout)
	}
	if logger.logRequestCalls != 0 {
		t.Fatalf("LogRequest calls = %d, want 0", logger.logRequestCalls)
	}
	if logger.logRequestWithOptionsCalls != 0 {
		t.Fatalf("LogRequestWithOptions calls = %d, want 0", logger.logRequestWithOptionsCalls)
	}
}

type recordingRequestLogger struct {
	enabled                    bool
	logRequestCalls            int
	logRequestWithOptionsCalls int
	lastForce                  bool
	lastURL                    string
	lastMethod                 string
}

func (l *recordingRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	l.logRequestCalls++
	l.lastURL = url
	l.lastMethod = method
	return nil
}

func (l *recordingRequestLogger) LogRequestWithOptions(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	l.logRequestWithOptionsCalls++
	l.lastURL = url
	l.lastMethod = method
	l.lastForce = force
	return nil
}

func (l *recordingRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &noopStreamingWriter{}, nil
}

func (l *recordingRequestLogger) IsEnabled() bool {
	return l.enabled
}

type noopStreamingWriter struct{}

func (w *noopStreamingWriter) WriteChunkAsync([]byte) {}

func (w *noopStreamingWriter) WriteStatus(int, map[string][]string) error { return nil }

func (w *noopStreamingWriter) WriteAPIRequest([]byte) error { return nil }

func (w *noopStreamingWriter) WriteAPIResponse([]byte) error { return nil }

func (w *noopStreamingWriter) WriteAPIWebsocketTimeline([]byte) error { return nil }

func (w *noopStreamingWriter) WriteAPIResponseErrors([]*interfaces.ErrorMessage) error { return nil }

func (w *noopStreamingWriter) SetFirstChunkTimestamp(time.Time) {}

func (w *noopStreamingWriter) Close() error { return nil }
