package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	gin "github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/customerstate"
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
	router.POST("/v1/internal/customers/:id/api-keys", h.PostCustomerAPIKey)
	router.DELETE("/v1/internal/customers/:id/api-keys/:key_id", h.DeleteCustomerAPIKey)
	router.POST("/v1/internal/customers/resolve", h.ResolveCustomerAPIKey)
	router.POST("/v0/management/customers/:id/credits/top-up", h.PostCustomerCreditsTopUp)
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

	ledgerResp := performJSONRequest(t, router, http.MethodGet, "/v1/internal/customers/customer-1/ledger?limit=10", nil)
	if ledgerResp.Code != http.StatusOK {
		t.Fatalf("ledger status = %d, want %d body=%s", ledgerResp.Code, http.StatusOK, ledgerResp.Body.String())
	}
	ledgerBody := decodeJSONBody(t, ledgerResp)
	ledger := ledgerBody["ledger"].([]any)
	if len(ledger) != 1 {
		t.Fatalf("ledger length = %d, want 1", len(ledger))
	}
	latestEntry := ledger[0].(map[string]any)
	if latestEntry["type"] != customerstate.LedgerTypeTopUp {
		t.Fatalf("ledger entry type = %v, want %s", latestEntry["type"], customerstate.LedgerTypeTopUp)
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
