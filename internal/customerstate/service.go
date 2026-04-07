package customerstate

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	CustomerIDHeader = "X-CheapRouter-User-ID"
	defaultStateDir  = ".caching-proxy-admin"
	defaultStateFile = "customers.json"
	LedgerTypeTopUp  = "credit_top_up"
	LedgerTypeUsage  = "usage_debit"
)

var (
	ErrCustomerNotFound   = errors.New("customer not found")
	ErrCustomerKeyMissing = errors.New("customer api key not found")
	ErrCustomerInactive   = errors.New("customer inactive")
	ErrInvalidAmount      = errors.New("amount must be greater than zero")
)

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name,omitempty"`
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"hash"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type APIKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name,omitempty"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type Customer struct {
	ID             string    `json:"id"`
	Email          string    `json:"email,omitempty"`
	DisplayName    string    `json:"display_name,omitempty"`
	EmailVerified  bool      `json:"email_verified"`
	Active         bool      `json:"active"`
	CreditsBalance int64     `json:"credits_balance"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	APIKeys        []APIKey  `json:"api_keys,omitempty"`
}

type CustomerView struct {
	ID             string       `json:"id"`
	Email          string       `json:"email,omitempty"`
	DisplayName    string       `json:"display_name,omitempty"`
	EmailVerified  bool         `json:"email_verified"`
	Active         bool         `json:"active"`
	CreditsBalance int64        `json:"credits_balance"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	APIKeys        []APIKeyView `json:"api_keys,omitempty"`
}

type LedgerEntry struct {
	ID           string    `json:"id"`
	CustomerID   string    `json:"customer_id"`
	Type         string    `json:"type"`
	Delta        int64     `json:"delta"`
	BalanceAfter int64     `json:"balance_after"`
	Reason       string    `json:"reason,omitempty"`
	Actor        string    `json:"actor,omitempty"`
	RequestID    string    `json:"request_id,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type ResolveResult struct {
	Customer CustomerView `json:"customer"`
	APIKey   APIKeyView   `json:"api_key"`
}

type UpsertCustomerInput struct {
	ID             string
	Email          string
	DisplayName    string
	EmailVerified  *bool
	Active         *bool
	InitialCredits *int64
}

type stateFile struct {
	Customers map[string]*Customer `json:"customers"`
	Ledger    []LedgerEntry        `json:"ledger"`
}

type Service struct {
	mu    sync.RWMutex
	path  string
	state stateFile
}

var (
	defaultServiceMu  sync.Mutex
	defaultService    *Service
	defaultServiceErr error
)

func ResolveStorePath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("CLIPROXY_CUSTOMER_STATE_PATH")); explicit != "" {
		return normalizeStorePath(explicit), nil
	}
	if writable := strings.TrimSpace(util.WritablePath()); writable != "" {
		return filepath.Join(writable, defaultStateDir, defaultStateFile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("customer state: resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultStateDir, defaultStateFile), nil
}

func normalizeStorePath(value string) string {
	cleaned := filepath.Clean(value)
	if strings.EqualFold(filepath.Ext(cleaned), ".json") {
		return cleaned
	}
	return filepath.Join(cleaned, defaultStateFile)
}

func DefaultService() (*Service, error) {
	defaultServiceMu.Lock()
	defer defaultServiceMu.Unlock()
	if defaultService != nil {
		return defaultService, nil
	}
	if defaultServiceErr != nil {
		return nil, defaultServiceErr
	}
	svc, err := NewService("")
	if err != nil {
		defaultServiceErr = err
		return nil, err
	}
	defaultService = svc
	return defaultService, nil
}

func SetDefaultService(svc *Service) {
	defaultServiceMu.Lock()
	defer defaultServiceMu.Unlock()
	defaultService = svc
	defaultServiceErr = nil
}

func NewService(path string) (*Service, error) {
	if strings.TrimSpace(path) == "" {
		resolved, err := ResolveStorePath()
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	svc := &Service{
		path: filepath.Clean(path),
		state: stateFile{
			Customers: make(map[string]*Customer),
		},
	}
	if err := svc.load(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) StorePath() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *Service) ListCustomers() ([]CustomerView, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.state.Customers))
	for id := range s.state.Customers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]CustomerView, 0, len(ids))
	for _, id := range ids {
		out = append(out, publicCustomer(s.state.Customers[id]))
	}
	return out, nil
}

func (s *Service) GetCustomer(customerID string) (CustomerView, error) {
	if s == nil {
		return CustomerView{}, ErrCustomerNotFound
	}
	trimmed := strings.TrimSpace(customerID)
	if trimmed == "" {
		return CustomerView{}, ErrCustomerNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	customer := s.state.Customers[trimmed]
	if customer == nil {
		return CustomerView{}, ErrCustomerNotFound
	}
	return publicCustomer(customer), nil
}

func (s *Service) UpsertCustomer(input UpsertCustomerInput) (CustomerView, error) {
	if s == nil {
		return CustomerView{}, ErrCustomerNotFound
	}
	customerID := strings.TrimSpace(input.ID)
	if customerID == "" {
		return CustomerView{}, ErrCustomerNotFound
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	customer := s.state.Customers[customerID]
	if customer == nil {
		active := true
		if input.Active != nil {
			active = *input.Active
		}
		emailVerified := false
		if input.EmailVerified != nil {
			emailVerified = *input.EmailVerified
		}
		credits := int64(0)
		if input.InitialCredits != nil {
			credits = *input.InitialCredits
		}
		customer = &Customer{
			ID:             customerID,
			Email:          strings.TrimSpace(input.Email),
			DisplayName:    strings.TrimSpace(input.DisplayName),
			EmailVerified:  emailVerified,
			Active:         active,
			CreditsBalance: credits,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		s.state.Customers[customerID] = customer
	} else {
		if trimmed := strings.TrimSpace(input.Email); trimmed != "" {
			customer.Email = trimmed
		}
		if trimmed := strings.TrimSpace(input.DisplayName); trimmed != "" {
			customer.DisplayName = trimmed
		}
		if input.EmailVerified != nil {
			customer.EmailVerified = *input.EmailVerified
		}
		if input.Active != nil {
			customer.Active = *input.Active
		}
		customer.UpdatedAt = now
	}

	if err := s.persistLocked(); err != nil {
		return CustomerView{}, err
	}
	return publicCustomer(customer), nil
}

func (s *Service) IssueAPIKey(customerID, name string) (CustomerView, APIKeyView, string, error) {
	if s == nil {
		return CustomerView{}, APIKeyView{}, "", ErrCustomerNotFound
	}
	trimmedID := strings.TrimSpace(customerID)
	if trimmedID == "" {
		return CustomerView{}, APIKeyView{}, "", ErrCustomerNotFound
	}
	rawKey, err := generateAPIKey()
	if err != nil {
		return CustomerView{}, APIKeyView{}, "", err
	}
	now := time.Now().UTC()
	key := APIKey{
		ID:        uuid.NewString(),
		Name:      strings.TrimSpace(name),
		Prefix:    apiKeyPrefix(rawKey),
		Hash:      hashAPIKey(rawKey),
		CreatedAt: now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	customer := s.state.Customers[trimmedID]
	if customer == nil {
		return CustomerView{}, APIKeyView{}, "", ErrCustomerNotFound
	}
	customer.APIKeys = append(customer.APIKeys, key)
	customer.UpdatedAt = now
	if err := s.persistLocked(); err != nil {
		return CustomerView{}, APIKeyView{}, "", err
	}
	return publicCustomer(customer), publicAPIKey(key), rawKey, nil
}

func (s *Service) RevokeAPIKey(customerID, keyID string) (CustomerView, error) {
	if s == nil {
		return CustomerView{}, ErrCustomerNotFound
	}
	trimmedCustomerID := strings.TrimSpace(customerID)
	trimmedKeyID := strings.TrimSpace(keyID)
	if trimmedCustomerID == "" || trimmedKeyID == "" {
		return CustomerView{}, ErrCustomerKeyMissing
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	customer := s.state.Customers[trimmedCustomerID]
	if customer == nil {
		return CustomerView{}, ErrCustomerNotFound
	}
	for index := range customer.APIKeys {
		if customer.APIKeys[index].ID != trimmedKeyID {
			continue
		}
		if customer.APIKeys[index].RevokedAt == nil {
			customer.APIKeys[index].RevokedAt = &now
			customer.UpdatedAt = now
			if err := s.persistLocked(); err != nil {
				return CustomerView{}, err
			}
		}
		return publicCustomer(customer), nil
	}
	return CustomerView{}, ErrCustomerKeyMissing
}

func (s *Service) ResolveAPIKey(rawKey string) (ResolveResult, error) {
	if s == nil {
		return ResolveResult{}, ErrCustomerKeyMissing
	}
	hash := hashAPIKey(rawKey)
	if hash == "" {
		return ResolveResult{}, ErrCustomerKeyMissing
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, customer := range s.state.Customers {
		if customer == nil {
			continue
		}
		for _, key := range customer.APIKeys {
			if key.Hash != hash || key.RevokedAt != nil {
				continue
			}
			if !customer.Active {
				return ResolveResult{}, ErrCustomerInactive
			}
			return ResolveResult{Customer: publicCustomer(customer), APIKey: publicAPIKey(key)}, nil
		}
	}
	return ResolveResult{}, ErrCustomerKeyMissing
}

func (s *Service) TopUpCredits(customerID string, amount int64, reason, actor string) (CustomerView, LedgerEntry, error) {
	if s == nil {
		return CustomerView{}, LedgerEntry{}, ErrCustomerNotFound
	}
	if amount <= 0 {
		return CustomerView{}, LedgerEntry{}, ErrInvalidAmount
	}
	trimmedID := strings.TrimSpace(customerID)
	if trimmedID == "" {
		return CustomerView{}, LedgerEntry{}, ErrCustomerNotFound
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	customer := s.state.Customers[trimmedID]
	if customer == nil {
		return CustomerView{}, LedgerEntry{}, ErrCustomerNotFound
	}
	customer.CreditsBalance += amount
	customer.UpdatedAt = now
	entry := LedgerEntry{
		ID:           uuid.NewString(),
		CustomerID:   trimmedID,
		Type:         LedgerTypeTopUp,
		Delta:        amount,
		BalanceAfter: customer.CreditsBalance,
		Reason:       strings.TrimSpace(reason),
		Actor:        strings.TrimSpace(actor),
		CreatedAt:    now,
	}
	s.state.Ledger = append(s.state.Ledger, entry)
	if err := s.persistLocked(); err != nil {
		return CustomerView{}, LedgerEntry{}, err
	}
	return publicCustomer(customer), entry, nil
}

func (s *Service) RecordUsageDebit(customerID string, amount int64, requestID, provider, model string) (CustomerView, LedgerEntry, error) {
	if s == nil {
		return CustomerView{}, LedgerEntry{}, ErrCustomerNotFound
	}
	if amount <= 0 {
		return CustomerView{}, LedgerEntry{}, ErrInvalidAmount
	}
	trimmedID := strings.TrimSpace(customerID)
	if trimmedID == "" {
		return CustomerView{}, LedgerEntry{}, ErrCustomerNotFound
	}
	trimmedRequestID := strings.TrimSpace(requestID)
	trimmedProvider := strings.TrimSpace(provider)
	trimmedModel := strings.TrimSpace(model)
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	customer := s.state.Customers[trimmedID]
	if customer == nil {
		return CustomerView{}, LedgerEntry{}, ErrCustomerNotFound
	}
	if trimmedRequestID != "" {
		for _, entry := range s.state.Ledger {
			if entry.CustomerID == trimmedID && entry.Type == LedgerTypeUsage && entry.RequestID == trimmedRequestID {
				return publicCustomer(customer), entry, nil
			}
		}
	}
	customer.CreditsBalance -= amount
	customer.UpdatedAt = now
	entry := LedgerEntry{
		ID:           uuid.NewString(),
		CustomerID:   trimmedID,
		Type:         LedgerTypeUsage,
		Delta:        -amount,
		BalanceAfter: customer.CreditsBalance,
		Reason:       usageDebitReason(trimmedProvider, trimmedModel),
		RequestID:    trimmedRequestID,
		Provider:     trimmedProvider,
		Model:        trimmedModel,
		CreatedAt:    now,
	}
	s.state.Ledger = append(s.state.Ledger, entry)
	if err := s.persistLocked(); err != nil {
		return CustomerView{}, LedgerEntry{}, err
	}
	return publicCustomer(customer), entry, nil
}

func (s *Service) ListLedger(customerID string, limit int) ([]LedgerEntry, error) {
	if s == nil {
		return nil, ErrCustomerNotFound
	}
	trimmedID := strings.TrimSpace(customerID)
	if trimmedID == "" {
		return nil, ErrCustomerNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.state.Customers[trimmedID]; !ok {
		return nil, ErrCustomerNotFound
	}
	entries := make([]LedgerEntry, 0)
	for _, entry := range s.state.Ledger {
		if entry.CustomerID == trimmedID {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Service) loadLocked() error {
	if s.state.Customers == nil {
		s.state.Customers = make(map[string]*Customer)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("customer state: read store: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var decoded stateFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("customer state: decode store: %w", err)
	}
	if decoded.Customers == nil {
		decoded.Customers = make(map[string]*Customer)
	}
	s.state = decoded
	return nil
}

func (s *Service) persistLocked() error {
	if s == nil {
		return nil
	}
	if s.state.Customers == nil {
		s.state.Customers = make(map[string]*Customer)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("customer state: create store dir: %w", err)
	}
	payload, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("customer state: encode store: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("customer state: write temp store: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("customer state: commit store: %w", err)
	}
	return nil
}

func publicCustomer(customer *Customer) CustomerView {
	if customer == nil {
		return CustomerView{}
	}
	keys := make([]APIKeyView, 0, len(customer.APIKeys))
	for _, key := range customer.APIKeys {
		keys = append(keys, publicAPIKey(key))
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].CreatedAt.After(keys[j].CreatedAt)
	})
	return CustomerView{
		ID:             customer.ID,
		Email:          customer.Email,
		DisplayName:    customer.DisplayName,
		EmailVerified:  customer.EmailVerified,
		Active:         customer.Active,
		CreditsBalance: customer.CreditsBalance,
		CreatedAt:      customer.CreatedAt,
		UpdatedAt:      customer.UpdatedAt,
		APIKeys:        keys,
	}
}

func publicAPIKey(key APIKey) APIKeyView {
	return APIKeyView{
		ID:         key.ID,
		Name:       key.Name,
		Prefix:     key.Prefix,
		CreatedAt:  key.CreatedAt,
		LastUsedAt: key.LastUsedAt,
		RevokedAt:  key.RevokedAt,
	}
}

func generateAPIKey() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("customer state: generate api key: %w", err)
	}
	return "crk_" + hex.EncodeToString(buffer), nil
}

func hashAPIKey(rawKey string) string {
	trimmed := strings.TrimSpace(rawKey)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func apiKeyPrefix(rawKey string) string {
	trimmed := strings.TrimSpace(rawKey)
	if len(trimmed) <= 12 {
		return trimmed
	}
	return trimmed[:12]
}

func usageDebitReason(provider, model string) string {
	parts := make([]string, 0, 2)
	if provider != "" {
		parts = append(parts, provider)
	}
	if model != "" {
		parts = append(parts, model)
	}
	if len(parts) == 0 {
		return "usage debit"
	}
	return strings.Join(parts, ":")
}
