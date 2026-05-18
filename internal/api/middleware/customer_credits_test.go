package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gin "github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestInternalCustomerMiddlewareRejectsRemoteCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("accessProvider", sdkaccess.DefaultAccessProviderName)
		c.Next()
	})
	engine.Use(InternalCustomerMiddleware())
	engine.GET("/v1/internal/customers/cust-1", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/v1/internal/customers/cust-1", nil)
	req.RemoteAddr = "10.0.0.10:4123"
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
	}
}

func TestInternalCustomerMiddlewareAllowsTrustedLoopbackCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("accessMetadata", map[string]string{"provider_type": sdkaccess.AccessProviderTypeConfigAPIKey})
		c.Next()
	})
	engine.Use(InternalCustomerMiddleware())
	engine.GET("/v1/internal/customers/cust-1", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/v1/internal/customers/cust-1", nil)
	req.RemoteAddr = "127.0.0.1:4123"
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
	}
}
