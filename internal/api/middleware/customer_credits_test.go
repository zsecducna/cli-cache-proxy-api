package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	gin "github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/customerstate"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func setTestCustomerService(t *testing.T) *customerstate.Service {
	t.Helper()
	svc, err := customerstate.NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	customerstate.SetDefaultService(svc)
	t.Cleanup(func() { customerstate.SetDefaultService(nil) })
	return svc
}

func mustCreateCustomer(t *testing.T, svc *customerstate.Service, customerID string, credits int64) {
	t.Helper()
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: customerID, InitialCredits: &credits}); err != nil {
		t.Fatalf("UpsertCustomer(%q) error = %v", customerID, err)
	}
}

func TestCustomerCreditsMiddlewareAllowsActiveCustomerWithBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := setTestCustomerService(t)
	mustCreateCustomer(t, svc, "cust-ok", 5)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("customerID", "cust-ok")
		c.Next()
	})
	engine.Use(CustomerCreditsMiddleware())
	engine.GET("/v1/models", func(c *gin.Context) {
		if _, exists := c.Get("customer"); !exists {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
	}
}

func TestCustomerCreditsMiddlewareAllowsExhaustedCustomer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := setTestCustomerService(t)
	mustCreateCustomer(t, svc, "cust-zero", 0)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("customerID", "cust-zero")
		c.Next()
	})
	engine.Use(CustomerCreditsMiddleware())
	engine.GET("/v1/models", func(c *gin.Context) {
		if _, exists := c.Get("customer"); !exists {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusNoContent, resp.Body.String())
	}
}

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
