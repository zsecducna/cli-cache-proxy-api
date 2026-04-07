package customerstate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestUsageDebitPluginDebitsSuccessfulUsage(t *testing.T) {
	svc, err := NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	SetDefaultService(svc)
	t.Cleanup(func() { SetDefaultService(nil) })

	initialCredits := int64(100)
	if _, err := svc.UpsertCustomer(UpsertCustomerInput{ID: "cust-plugin", InitialCredits: &initialCredits}); err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}

	ctx := logging.WithRequestID(context.Background(), "req-plugin-success")
	NewUsageDebitPlugin().HandleUsage(ctx, coreusage.Record{
		Provider:   "openai",
		Model:      "gpt-4.1",
		CustomerID: "cust-plugin",
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  20,
		},
	})

	customer, err := svc.GetCustomer("cust-plugin")
	if err != nil {
		t.Fatalf("GetCustomer() error = %v", err)
	}
	if customer.CreditsBalance != 80 {
		t.Fatalf("credits balance = %d, want 80", customer.CreditsBalance)
	}

	ledger, err := svc.ListLedger("cust-plugin", 10)
	if err != nil {
		t.Fatalf("ListLedger() error = %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger length = %d, want 1", len(ledger))
	}
	if ledger[0].Type != LedgerTypeUsage {
		t.Fatalf("ledger entry type = %q, want %q", ledger[0].Type, LedgerTypeUsage)
	}
	if ledger[0].RequestID != "req-plugin-success" {
		t.Fatalf("ledger request_id = %q, want req-plugin-success", ledger[0].RequestID)
	}
}

func TestUsageDebitPluginSkipsFailedUsage(t *testing.T) {
	svc, err := NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	SetDefaultService(svc)
	t.Cleanup(func() { SetDefaultService(nil) })

	initialCredits := int64(75)
	if _, err := svc.UpsertCustomer(UpsertCustomerInput{ID: "cust-plugin-failed", InitialCredits: &initialCredits}); err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}

	ctx := logging.WithRequestID(context.Background(), "req-plugin-failed")
	NewUsageDebitPlugin().HandleUsage(ctx, coreusage.Record{
		Provider:   "openai",
		Model:      "gpt-4.1",
		CustomerID: "cust-plugin-failed",
		Failed:     true,
		Detail: coreusage.Detail{
			TotalTokens: 20,
		},
	})

	customer, err := svc.GetCustomer("cust-plugin-failed")
	if err != nil {
		t.Fatalf("GetCustomer() error = %v", err)
	}
	if customer.CreditsBalance != 75 {
		t.Fatalf("credits balance = %d, want 75", customer.CreditsBalance)
	}

	ledger, err := svc.ListLedger("cust-plugin-failed", 10)
	if err != nil {
		t.Fatalf("ListLedger() error = %v", err)
	}
	if len(ledger) != 0 {
		t.Fatalf("ledger length = %d, want 0", len(ledger))
	}
}
