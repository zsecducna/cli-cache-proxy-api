package middleware

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestFinalizeStreamingPersistsAPIErrorResponsesToFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, logsDir, "", 0)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	streamWriter, err := logger.LogStreamingRequest(
		"/v1/messages",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"stream":true}`),
		"req-stream-error",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest error: %v", err)
	}

	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logger,
		requestInfo: &RequestInfo{
			URL:       "/v1/messages",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			Body:      []byte(`{"stream":true}`),
			RequestID: "req-stream-error",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		isStreaming:         true,
		streamWriter:        streamWriter,
		firstChunkTimestamp: time.Date(2026, time.April, 1, 12, 0, 1, 0, time.UTC),
	}

	c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{{
		StatusCode: 429,
		Error:      errors.New("rate limit"),
	}})

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}

	files, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("log files = %d, want 1", len(files))
	}

	data, err := os.ReadFile(filepath.Join(logsDir, files[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "=== API ERROR RESPONSE ===") {
		t.Fatalf("log file missing API error response section:\n%s", content)
	}
	if !strings.Contains(content, "HTTP Status: 429") {
		t.Fatalf("log file missing API error status:\n%s", content)
	}
	if !strings.Contains(content, "rate limit") {
		t.Fatalf("log file missing API error body:\n%s", content)
	}
}
