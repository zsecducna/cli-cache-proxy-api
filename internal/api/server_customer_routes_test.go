package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/customerstate"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func setServerCustomerService(t *testing.T) *customerstate.Service {
	t.Helper()
	svc, err := customerstate.NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	customerstate.SetDefaultService(svc)
	t.Cleanup(func() { customerstate.SetDefaultService(nil) })
	return svc
}

func TestServerTrustedCustomerIdentityAllowsUnknownCustomer(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set(customerstate.CustomerIDHeader, "cust-unknown")
	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
}

func TestServerInternalCustomerResolveRouteWorks(t *testing.T) {
	server := newTestServer(t)
	svc := setServerCustomerService(t)
	credits := int64(25)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "cust-one", InitialCredits: &credits}); err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}
	_, _, plainAPIKey, err := svc.IssueAPIKey("cust-one", "primary")
	if err != nil {
		t.Fatalf("IssueAPIKey() error = %v", err)
	}

	body, err := json.Marshal(map[string]string{"api_key": plainAPIKey})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/internal/customers/resolve", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	customer := payload["customer"].(map[string]any)
	if customer["id"] != "cust-one" {
		t.Fatalf("customer id = %v, want cust-one", customer["id"])
	}
}

func TestServerInternalCustomerUsageRouteWorks(t *testing.T) {
	server := newTestServer(t)
	svc := setServerCustomerService(t)
	customerOneCredits := int64(25)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "cust-one", InitialCredits: &customerOneCredits}); err != nil {
		t.Fatalf("UpsertCustomer(cust-one) error = %v", err)
	}
	customerTwoCredits := int64(25)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "cust-two", InitialCredits: &customerTwoCredits}); err != nil {
		t.Fatalf("UpsertCustomer(cust-two) error = %v", err)
	}

	stats := usage.NewRequestStatistics()
	server.mgmt.SetUsageStatistics(stats)
	stats.Record(context.Background(), coreusage.Record{
		Provider:   "openai",
		Model:      "gpt-4.1",
		APIKey:     "shared-system-key",
		CustomerID: "cust-one",
		Detail: coreusage.Detail{
			TotalTokens: 17,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		Provider:   "openai",
		Model:      "gpt-4.1",
		APIKey:     "shared-system-key",
		CustomerID: "cust-two",
		Detail: coreusage.Detail{
			TotalTokens: 23,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/internal/customers/cust-one/usage", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer test-key")
	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	usageBody := payload["usage"].(map[string]any)
	if usageBody["total_requests"].(float64) != 1 {
		t.Fatalf("usage total_requests = %v, want 1", usageBody["total_requests"])
	}
	apis := usageBody["apis"].(map[string]any)
	if _, ok := apis["cust-one"]; !ok {
		t.Fatalf("usage apis missing cust-one: %+v", apis)
	}
	if _, ok := apis["cust-two"]; ok {
		t.Fatalf("usage apis unexpectedly contains cust-two: %+v", apis)
	}
}

func TestManagementCustomerTopUpRouteRegistered(t *testing.T) {
	server := newManagementTestServer(t)
	svc := setServerCustomerService(t)
	credits := int64(0)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "cust-mgmt", InitialCredits: &credits}); err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}

	body, err := json.Marshal(map[string]any{"amount": 10, "reason": "manual", "actor": "admin"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/customers/cust-mgmt/credits/top-up", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Key", "test-secret")
	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	customer := payload["customer"].(map[string]any)
	if customer["credits_balance"].(float64) != 10 {
		t.Fatalf("credits balance = %v, want 10", customer["credits_balance"])
	}
}

func TestManagementCustomerDeductRouteRegistered(t *testing.T) {
	server := newManagementTestServer(t)
	svc := setServerCustomerService(t)
	credits := int64(25)
	if _, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{ID: "cust-mgmt", InitialCredits: &credits}); err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}

	body, err := json.Marshal(map[string]any{"amount": 10, "reason": "manual", "actor": "admin"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/customers/cust-mgmt/credits/deduct", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Key", "test-secret")
	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	customer := payload["customer"].(map[string]any)
	if customer["credits_balance"].(float64) != 15 {
		t.Fatalf("credits balance = %v, want 15", customer["credits_balance"])
	}
}
