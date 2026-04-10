package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	gin "github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/customerstate"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func setupCustomerHandlerTest(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc, err := customerstate.NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	customerstate.SetDefaultService(svc)
	t.Cleanup(func() { customerstate.SetDefaultService(nil) })

	h := &Handler{}
	router := gin.New()
	router.PUT("/v1/internal/customers/:id", h.PutCustomer)
	router.GET("/v1/internal/customers/:id", h.GetCustomer)
	router.GET("/v1/internal/customers/:id/ledger", h.GetCustomerLedger)
	router.GET("/v1/internal/customers/:id/usage", h.GetCustomerUsage)
	router.POST("/v1/internal/customers/:id/api-keys", h.PostCustomerAPIKey)
	router.DELETE("/v1/internal/customers/:id/api-keys/:key_id", h.DeleteCustomerAPIKey)
	router.POST("/v1/internal/customers/resolve", h.ResolveCustomerAPIKey)
	router.POST("/v0/management/customers/:id/credits/top-up", h.PostCustomerCreditsTopUp)
	router.POST("/v0/management/customers/:id/credits/deduct", h.PostCustomerCreditsDeduct)
	return router
}

func performJSONRequest(t *testing.T, router *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func decodeJSONBody(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return body
}

func TestCustomerHandlersEndToEnd(t *testing.T) {
	router := setupCustomerHandlerTest(t)

	createResp := performJSONRequest(t, router, http.MethodPut, "/v1/internal/customers/customer-1", map[string]any{
		"email":           "alice@example.com",
		"display_name":    "Alice",
		"email_verified":  true,
		"initial_credits": 50,
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d body=%s", createResp.Code, http.StatusOK, createResp.Body.String())
	}

	issueResp := performJSONRequest(t, router, http.MethodPost, "/v1/internal/customers/customer-1/api-keys", map[string]any{
		"name": "primary",
	})
	if issueResp.Code != http.StatusOK {
		t.Fatalf("issue status = %d, want %d body=%s", issueResp.Code, http.StatusOK, issueResp.Body.String())
	}
	issueBody := decodeJSONBody(t, issueResp)
	plainAPIKey, _ := issueBody["plain_api_key"].(string)
	if plainAPIKey == "" {
		t.Fatal("plain_api_key is empty")
	}
	apiKeyData := issueBody["api_key"].(map[string]any)
	apiKeyID, _ := apiKeyData["id"].(string)
	if apiKeyID == "" {
		t.Fatal("api_key.id is empty")
	}

	resolveResp := performJSONRequest(t, router, http.MethodPost, "/v1/internal/customers/resolve", map[string]any{
		"api_key": plainAPIKey,
	})
	if resolveResp.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d body=%s", resolveResp.Code, http.StatusOK, resolveResp.Body.String())
	}
	resolveBody := decodeJSONBody(t, resolveResp)
	resolvedCustomer := resolveBody["customer"].(map[string]any)
	if resolvedCustomer["id"] != "customer-1" {
		t.Fatalf("resolved customer id = %v, want customer-1", resolvedCustomer["id"])
	}

	topUpResp := performJSONRequest(t, router, http.MethodPost, "/v0/management/customers/customer-1/credits/top-up", map[string]any{
		"amount": 25,
		"reason": "manual top up",
		"actor":  "admin",
	})
	if topUpResp.Code != http.StatusOK {
		t.Fatalf("top-up status = %d, want %d body=%s", topUpResp.Code, http.StatusOK, topUpResp.Body.String())
	}
	topUpBody := decodeJSONBody(t, topUpResp)
	toppedUpCustomer := topUpBody["customer"].(map[string]any)
	if toppedUpCustomer["credits_balance"].(float64) != 75 {
		t.Fatalf("credits balance = %v, want 75", toppedUpCustomer["credits_balance"])
	}

	deductResp := performJSONRequest(t, router, http.MethodPost, "/v0/management/customers/customer-1/credits/deduct", map[string]any{
		"amount": 5,
		"reason": "manual deduction",
		"actor":  "admin",
	})
	if deductResp.Code != http.StatusOK {
		t.Fatalf("deduct status = %d, want %d body=%s", deductResp.Code, http.StatusOK, deductResp.Body.String())
	}
	deductBody := decodeJSONBody(t, deductResp)
	deductedCustomer := deductBody["customer"].(map[string]any)
	if deductedCustomer["credits_balance"].(float64) != 70 {
		t.Fatalf("credits balance = %v, want 70", deductedCustomer["credits_balance"])
	}

	ledgerResp := performJSONRequest(t, router, http.MethodGet, "/v1/internal/customers/customer-1/ledger?limit=10", nil)
	if ledgerResp.Code != http.StatusOK {
		t.Fatalf("ledger status = %d, want %d body=%s", ledgerResp.Code, http.StatusOK, ledgerResp.Body.String())
	}
	ledgerBody := decodeJSONBody(t, ledgerResp)
	ledger := ledgerBody["ledger"].([]any)
	if len(ledger) != 2 {
		t.Fatalf("ledger length = %d, want 2", len(ledger))
	}
	latestEntry := ledger[0].(map[string]any)
	if latestEntry["type"] != customerstate.LedgerTypeDeduct {
		t.Fatalf("ledger entry type = %v, want %s", latestEntry["type"], customerstate.LedgerTypeDeduct)
	}

	deleteResp := performJSONRequest(t, router, http.MethodDelete, "/v1/internal/customers/customer-1/api-keys/"+apiKeyID, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", deleteResp.Code, http.StatusOK, deleteResp.Body.String())
	}

	resolveAfterDelete := performJSONRequest(t, router, http.MethodPost, "/v1/internal/customers/resolve", map[string]any{
		"api_key": plainAPIKey,
	})
	if resolveAfterDelete.Code != http.StatusNotFound {
		t.Fatalf("resolve after delete status = %d, want %d body=%s", resolveAfterDelete.Code, http.StatusNotFound, resolveAfterDelete.Body.String())
	}
}

func TestGetCustomerUsageReturnsOnlyRequestedCustomer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, err := customerstate.NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	customerstate.SetDefaultService(svc)
	t.Cleanup(func() { customerstate.SetDefaultService(nil) })

	customerOneCredits := int64(50)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "customer-1", InitialCredits: &customerOneCredits}); err != nil {
		t.Fatalf("UpsertCustomer(customer-1) error = %v", err)
	}
	customerTwoCredits := int64(25)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "customer-2", InitialCredits: &customerTwoCredits}); err != nil {
		t.Fatalf("UpsertCustomer(customer-2) error = %v", err)
	}

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Provider:   "openai",
		Model:      "gpt-4.1",
		APIKey:     "shared-system-key",
		CustomerID: "customer-1",
		Detail: coreusage.Detail{
			TotalTokens: 17,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		Provider:   "openai",
		Model:      "gpt-4.1",
		APIKey:     "shared-system-key",
		CustomerID: "customer-2",
		Detail: coreusage.Detail{
			TotalTokens: 23,
		},
	})

	h := &Handler{}
	h.SetUsageStatistics(stats)
	router := gin.New()
	router.GET("/v1/internal/customers/:id/usage", h.GetCustomerUsage)

	resp := performJSONRequest(t, router, http.MethodGet, "/v1/internal/customers/customer-1/usage", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("usage status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	body := decodeJSONBody(t, resp)
	usageBody := body["usage"].(map[string]any)
	if usageBody["total_requests"].(float64) != 1 {
		t.Fatalf("usage total_requests = %v, want 1", usageBody["total_requests"])
	}
	apis := usageBody["apis"].(map[string]any)
	if len(apis) != 1 {
		t.Fatalf("usage apis len = %d, want 1", len(apis))
	}
	if _, ok := apis["customer-1"]; !ok {
		t.Fatalf("usage apis missing customer-1: %+v", apis)
	}
	if _, ok := apis["customer-2"]; ok {
		t.Fatalf("usage apis unexpectedly contains customer-2: %+v", apis)
	}
}
