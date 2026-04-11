package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

func TestExtractRequestBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body = wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}
}

func TestExtractRequestBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	c.Set(requestBodyOverrideContextKey, "override-as-string")

	body := wrapper.extractRequestBody(c)
	if string(body) != "override-as-string" {
		t.Fatalf("request body = %q, want %q", string(body), "override-as-string")
	}
}

func TestExtractResponseBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	wrapper.body.WriteString("original-response")

	body := wrapper.extractResponseBody(c)
	if string(body) != "original-response" {
		t.Fatalf("response body = %q, want %q", string(body), "original-response")
	}

	c.Set(responseBodyOverrideContextKey, []byte("override-response"))
	body = wrapper.extractResponseBody(c)
	if string(body) != "override-response" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response")
	}

	body[0] = 'X'
	if got := wrapper.extractResponseBody(c); string(got) != "override-response" {
		t.Fatalf("response override should be cloned, got %q", string(got))
	}
}

func TestExtractResponseBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	c.Set(responseBodyOverrideContextKey, "override-response-as-string")

	body := wrapper.extractResponseBody(c)
	if string(body) != "override-response-as-string" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response-as-string")
	}
}

func TestExtractBodyOverrideClonesBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	override := []byte("body-override")
	c.Set(requestBodyOverrideContextKey, override)

	body := extractBodyOverride(c, requestBodyOverrideContextKey)
	if !bytes.Equal(body, override) {
		t.Fatalf("body override = %q, want %q", string(body), string(override))
	}

	body[0] = 'X'
	if !bytes.Equal(override, []byte("body-override")) {
		t.Fatalf("override mutated: %q", string(override))
	}
}

func TestExtractWebsocketTimelineUsesOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	if got := wrapper.extractWebsocketTimeline(c); got != nil {
		t.Fatalf("expected nil websocket timeline, got %q", string(got))
	}

	c.Set(websocketTimelineOverrideContextKey, []byte("timeline"))
	body := wrapper.extractWebsocketTimeline(c)
	if string(body) != "timeline" {
		t.Fatalf("websocket timeline = %q, want %q", string(body), "timeline")
	}
}

func TestFinalizeStreamingWritesAPIWebsocketTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	streamWriter := &testStreamingLogWriter{}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         &testRequestLogger{enabled: true},
		requestInfo: &RequestInfo{
			URL:       "/v1/responses",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-1",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		isStreaming:  true,
		streamWriter: streamWriter,
	}

	c.Set("API_WEBSOCKET_TIMELINE", []byte("Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if string(streamWriter.apiWebsocketTimeline) != "Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}" {
		t.Fatalf("stream writer websocket timeline = %q", string(streamWriter.apiWebsocketTimeline))
	}
	if !streamWriter.closed {
		t.Fatal("expected stream writer to be closed")
	}
}

type testRequestLogger struct {
	enabled bool
}

func (l *testRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	return nil
}

func (l *testRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *testRequestLogger) IsEnabled() bool {
	return l.enabled
}

type testStreamingLogWriter struct {
	apiWebsocketTimeline []byte
	apiResponseErrors    []*interfaces.ErrorMessage
	closed               bool
}

func (w *testStreamingLogWriter) WriteChunkAsync([]byte) {}

func (w *testStreamingLogWriter) WriteStatus(int, map[string][]string) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIRequest([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIResponse([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *testStreamingLogWriter) WriteAPIResponseErrors(apiResponseErrors []*interfaces.ErrorMessage) error {
	w.apiResponseErrors = make([]*interfaces.ErrorMessage, 0, len(apiResponseErrors))
	for i := 0; i < len(apiResponseErrors); i++ {
		if apiResponseErrors[i] == nil {
			continue
		}
		copied := &interfaces.ErrorMessage{StatusCode: apiResponseErrors[i].StatusCode}
		if apiResponseErrors[i].Error != nil {
			copied.Error = errors.New(apiResponseErrors[i].Error.Error())
		}
		w.apiResponseErrors = append(w.apiResponseErrors, copied)
	}
	return nil
}

func (w *testStreamingLogWriter) SetFirstChunkTimestamp(time.Time) {}

func (w *testStreamingLogWriter) Close() error {
	w.closed = true
	return nil
}

type captureRequestLogger struct {
	enabled  bool
	called   bool
	forced   bool
	lastURL  string
	lastCode int
}

func (l *captureRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	l.called = true
	return nil
}

func (l *captureRequestLogger) LogRequestWithOptions(url, _ string, _ map[string][]string, _ []byte, statusCode int, _ map[string][]string, _ []byte, _ []byte, _ []byte, _ []byte, _ []byte, _ []*interfaces.ErrorMessage, force bool, _ string, _, _ time.Time) error {
	l.called = true
	l.forced = force
	l.lastURL = url
	l.lastCode = statusCode
	return nil
}

func (l *captureRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *captureRequestLogger) IsEnabled() bool {
	return l.enabled
}

func TestFinalizeForceLog_OnlyForProxyFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		url        string
		method     string
		statusCode int
		setAttempt bool
		wantCalled bool
		wantForced bool
	}{
		{
			name:       "suppress benign root head 404",
			url:        "/",
			method:     http.MethodHead,
			statusCode: http.StatusNotFound,
			setAttempt: false,
			wantCalled: false,
			wantForced: false,
		},
		{
			name:       "force proxy failure messages 502",
			url:        "/v1/messages",
			method:     http.MethodPost,
			statusCode: http.StatusBadGateway,
			setAttempt: true,
			wantCalled: true,
			wantForced: true,
		},
		{
			name:       "force proxy failure responses 502",
			url:        "/v1/responses",
			method:     http.MethodPost,
			statusCode: http.StatusBadGateway,
			setAttempt: true,
			wantCalled: true,
			wantForced: true,
		},
		{
			name:       "suppress proxy route pre-auth 401 without upstream attempt",
			url:        "/v1/responses",
			method:     http.MethodPost,
			statusCode: http.StatusUnauthorized,
			setAttempt: false,
			wantCalled: false,
			wantForced: false,
		},
		{
			name:       "suppress proxy route pre-validation 400 without upstream attempt",
			url:        "/v1/messages",
			method:     http.MethodPost,
			statusCode: http.StatusBadRequest,
			setAttempt: false,
			wantCalled: false,
			wantForced: false,
		},
		{
			name:       "suppress cancelled proxy request 499",
			url:        "/v1/messages",
			method:     http.MethodPost,
			statusCode: clientClosedRequestStatusCode,
			setAttempt: true,
			wantCalled: false,
			wantForced: false,
		},
		{
			name:       "suppress non proxy failure 502",
			url:        "/",
			method:     http.MethodPost,
			statusCode: http.StatusBadGateway,
			setAttempt: true,
			wantCalled: false,
			wantForced: false,
		},
	}

	for i := range tests {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		logger := &captureRequestLogger{enabled: false}
		wrapper := &ResponseWriterWrapper{
			ResponseWriter: c.Writer,
			logger:         logger,
			requestInfo: &RequestInfo{
				URL:       tests[i].url,
				Method:    tests[i].method,
				Headers:   map[string][]string{"Content-Type": {"application/json"}},
				RequestID: "req-force-log",
				Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
			},
			statusCode:     tests[i].statusCode,
			logOnErrorOnly: true,
		}
		if tests[i].setAttempt {
			c.Set("API_UPSTREAM_ATTEMPTED", true)
		}

		if err := wrapper.Finalize(c); err != nil {
			t.Fatalf("%s: Finalize() error = %v", tests[i].name, err)
		}
		if logger.called != tests[i].wantCalled {
			t.Fatalf("%s: log called = %t, want %t", tests[i].name, logger.called, tests[i].wantCalled)
		}
		if logger.forced != tests[i].wantForced {
			t.Fatalf("%s: force flag = %t, want %t", tests[i].name, logger.forced, tests[i].wantForced)
		}
	}
}

func TestFinalizeSuppressesContextCanceledAPIError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("API_UPSTREAM_ATTEMPTED", true)
	c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{{
		StatusCode: clientClosedRequestStatusCode,
		Error:      context.Canceled,
	}})

	logger := &captureRequestLogger{enabled: false}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logger,
		requestInfo: &RequestInfo{
			URL:       "/v1/messages",
			Method:    http.MethodPost,
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-cancel",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		statusCode:     http.StatusOK,
		logOnErrorOnly: true,
	}

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if logger.called {
		t.Fatal("expected canceled API error to skip forced logging")
	}
}

func TestFinalizeSuppressesMarkedCanceledRequestTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("API_UPSTREAM_ATTEMPTED", true)
	logging.MarkRequestCanceled(c)

	logger := &captureRequestLogger{enabled: false}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logger,
		requestInfo: &RequestInfo{
			URL:       "/v1/messages",
			Method:    http.MethodPost,
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-timeout-cancel",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		statusCode:     http.StatusRequestTimeout,
		logOnErrorOnly: true,
	}

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if logger.called {
		t.Fatal("expected marked canceled timeout to skip forced logging")
	}
}

func TestFinalizeSuppressesCreditsExhaustedProxyErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		body    []byte
		apiErrs []*interfaces.ErrorMessage
	}{
		{
			name: "customer credits exhausted body",
			body: []byte(`{"error":{"code":"credits_exhausted","message":"customer credits exhausted"}}`),
		},
		{
			name: "quota exhausted api error",
			apiErrs: []*interfaces.ErrorMessage{{
				StatusCode: http.StatusTooManyRequests,
				Error:      errors.New(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`),
			}},
		},
	}

	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("API_UPSTREAM_ATTEMPTED", true)
		if len(tt.body) > 0 {
			c.Set("API_RESPONSE", tt.body)
		}
		if len(tt.apiErrs) > 0 {
			c.Set("API_RESPONSE_ERROR", tt.apiErrs)
		}

		logger := &captureRequestLogger{enabled: false}
		wrapper := &ResponseWriterWrapper{
			ResponseWriter: c.Writer,
			logger:         logger,
			requestInfo: &RequestInfo{
				URL:       "/v1/messages",
				Method:    http.MethodPost,
				Headers:   map[string][]string{"Content-Type": {"application/json"}},
				RequestID: "req-credits-exhausted",
				Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
			},
			statusCode:     http.StatusTooManyRequests,
			logOnErrorOnly: true,
		}

		if err := wrapper.Finalize(c); err != nil {
			t.Fatalf("%s: Finalize() error = %v", tt.name, err)
		}
		if logger.called {
			t.Fatalf("%s: expected credits/quota exhaustion to skip forced logging", tt.name)
		}
	}
}

func TestFinalizeStillForceLogsGenericRateLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Set("API_UPSTREAM_ATTEMPTED", true)
	c.Set("API_RESPONSE", []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`))

	logger := &captureRequestLogger{enabled: false}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logger,
		requestInfo: &RequestInfo{
			URL:       "/v1/messages",
			Method:    http.MethodPost,
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-rate-limit",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		statusCode:     http.StatusTooManyRequests,
		logOnErrorOnly: true,
	}

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if !logger.called || !logger.forced {
		t.Fatal("expected generic rate limit to remain force-logged")
	}
}
