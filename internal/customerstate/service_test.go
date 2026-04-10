package customerstate

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(filepath.Join(t.TempDir(), "customers.json"))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func TestServiceIssueResolveRevokeAndLedger(t *testing.T) {
	svc := newTestService(t)
	emailVerified := true
	initialCredits := int64(100)

	customer, err := svc.UpsertCustomer(UpsertCustomerInput{
		ID:             "cust-1",
		Email:          "alice@example.com",
		DisplayName:    "Alice",
		EmailVerified:  &emailVerified,
		InitialCredits: &initialCredits,
	})
	if err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}
	if customer.CreditsBalance != 100 {
		t.Fatalf("credits = %d, want 100", customer.CreditsBalance)
	}

	customer, apiKey, plainAPIKey, err := svc.IssueAPIKey("cust-1", "primary")
	if err != nil {
		t.Fatalf("IssueAPIKey() error = %v", err)
	}
	if plainAPIKey == "" {
		t.Fatal("IssueAPIKey() returned empty plain api key")
	}
	if len(customer.APIKeys) != 1 {
		t.Fatalf("api key count = %d, want 1", len(customer.APIKeys))
	}

	resolved, err := svc.ResolveAPIKey(plainAPIKey)
	if err != nil {
		t.Fatalf("ResolveAPIKey() error = %v", err)
	}
	if resolved.Customer.ID != "cust-1" {
		t.Fatalf("resolved customer id = %q, want cust-1", resolved.Customer.ID)
	}
	if resolved.APIKey.ID != apiKey.ID {
		t.Fatalf("resolved key id = %q, want %q", resolved.APIKey.ID, apiKey.ID)
	}

	customer, debitEntry, err := svc.RecordUsageDebit("cust-1", 40, "req-1", "openai", "gpt-4.1")
	if err != nil {
		t.Fatalf("RecordUsageDebit() error = %v", err)
	}
	if customer.CreditsBalance != 60 {
		t.Fatalf("credits after debit = %d, want 60", customer.CreditsBalance)
	}

	customer, duplicateDebit, err := svc.RecordUsageDebit("cust-1", 40, "req-1", "openai", "gpt-4.1")
	if err != nil {
		t.Fatalf("RecordUsageDebit() duplicate error = %v", err)
	}
	if customer.CreditsBalance != 60 {
		t.Fatalf("credits after duplicate debit = %d, want 60", customer.CreditsBalance)
	}
	if duplicateDebit.ID != debitEntry.ID {
		t.Fatalf("duplicate debit id = %q, want %q", duplicateDebit.ID, debitEntry.ID)
	}

	customer, _, err = svc.TopUpCredits("cust-1", 15, "manual top up", "admin")
	if err != nil {
		t.Fatalf("TopUpCredits() error = %v", err)
	}
	if customer.CreditsBalance != 75 {
		t.Fatalf("credits after top-up = %d, want 75", customer.CreditsBalance)
	}

	var deductionEntry LedgerEntry
	customer, deductionEntry, err = svc.DeductCredits("cust-1", 5, "manual deduction", "admin")
	if err != nil {
		t.Fatalf("DeductCredits() error = %v", err)
	}
	if customer.CreditsBalance != 70 {
		t.Fatalf("credits after deduction = %d, want 70", customer.CreditsBalance)
	}

	ledger, err := svc.ListLedger("cust-1", 10)
	if err != nil {
		t.Fatalf("ListLedger() error = %v", err)
	}
	if len(ledger) != 3 {
		t.Fatalf("ledger length = %d, want 3", len(ledger))
	}
	if ledger[0].ID != deductionEntry.ID {
		t.Fatalf("latest ledger entry = %q, want %q", ledger[0].ID, deductionEntry.ID)
	}

	if _, err := svc.RevokeAPIKey("cust-1", apiKey.ID); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}
	if _, err := svc.ResolveAPIKey(plainAPIKey); !errors.Is(err, ErrCustomerKeyMissing) {
		t.Fatalf("ResolveAPIKey() after revoke error = %v, want ErrCustomerKeyMissing", err)
	}
}

func TestTopUpCreditsIsAtomicAcrossConcurrentCalls(t *testing.T) {
	svc := newTestService(t)
	initialCredits := int64(10)
	if _, err := svc.UpsertCustomer(UpsertCustomerInput{ID: "cust-atomic", InitialCredits: &initialCredits}); err != nil {
		t.Fatalf("UpsertCustomer() error = %v", err)
	}

	const topUpCount = 8
	var wg sync.WaitGroup
	errCh := make(chan error, topUpCount)
	for i := 0; i < topUpCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := svc.TopUpCredits("cust-atomic", 5, "manual top up", "admin"); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("TopUpCredits() concurrent error = %v", err)
		}
	}

	customer, err := svc.GetCustomer("cust-atomic")
	if err != nil {
		t.Fatalf("GetCustomer() error = %v", err)
	}
	wantBalance := initialCredits + int64(topUpCount*5)
	if customer.CreditsBalance != wantBalance {
		t.Fatalf("credits balance = %d, want %d", customer.CreditsBalance, wantBalance)
	}

	ledger, err := svc.ListLedger("cust-atomic", 100)
	if err != nil {
		t.Fatalf("ListLedger() error = %v", err)
	}
	if len(ledger) != topUpCount {
		t.Fatalf("ledger length = %d, want %d", len(ledger), topUpCount)
	}
}
