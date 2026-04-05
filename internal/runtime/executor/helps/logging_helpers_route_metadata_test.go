package helps

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestLoggingHelpersRouteClassSelectedSurfaceSelectedProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:              "https://example.com/v1/responses",
		Method:           "POST",
		Body:             []byte(`{"model":"gpt-5.4"}`),
		Provider:         "openrouter",
		RouteClass:       "claude_via_openai_compat",
		SelectedProvider: "openrouter",
		SelectedSurface:  "responses",
	})

	value, exists := ginCtx.Get(apiRequestKey)
	if !exists {
		t.Fatal("API_REQUEST missing")
	}
	text := string(value.([]byte))
	for _, want := range []string{
		"route_class: claude_via_openai_compat",
		"selected_provider: openrouter",
		"selected_surface: responses",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request log missing %q:\n%s", want, text)
		}
	}
}
