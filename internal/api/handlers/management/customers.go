package management

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/customerstate"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func (h *Handler) GetCustomers(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	customers, err := svc.ListCustomers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"customers": customers})
}

func (h *Handler) GetCustomer(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	customer, err := svc.GetCustomer(c.Param("id"))
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer": customer})
}

func (h *Handler) PutCustomer(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	var body struct {
		Email          string `json:"email"`
		DisplayName    string `json:"display_name"`
		EmailVerified  *bool  `json:"email_verified"`
		Active         *bool  `json:"active"`
		InitialCredits *int64 `json:"initial_credits"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	customer, err := svc.UpsertCustomer(customerstate.UpsertCustomerInput{
		ID:             c.Param("id"),
		Email:          body.Email,
		DisplayName:    body.DisplayName,
		EmailVerified:  body.EmailVerified,
		Active:         body.Active,
		InitialCredits: body.InitialCredits,
	})
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer": customer})
}

func (h *Handler) PostCustomerAPIKey(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	customer, apiKey, plainAPIKey, err := svc.IssueAPIKey(c.Param("id"), body.Name)
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"customer":      customer,
		"api_key":       apiKey,
		"plain_api_key": plainAPIKey,
	})
}

func (h *Handler) DeleteCustomerAPIKey(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	customer, err := svc.RevokeAPIKey(c.Param("id"), c.Param("key_id"))
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer": customer})
}

func (h *Handler) GetCustomerLedger(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	entries, err := svc.ListLedger(c.Param("id"), readPositiveIntQuery(c, "limit", 50))
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ledger": entries})
}

func (h *Handler) GetCustomerUsage(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	customer, err := svc.GetCustomer(c.Param("id"))
	if !handleCustomerStateError(c, err) {
		return
	}
	snapshot := usage.FilterStatisticsSnapshotByCustomer(h.currentUsageSnapshot(c), customer.ID)
	c.JSON(http.StatusOK, gin.H{
		"customer":        customer,
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

func (h *Handler) PostCustomerCreditsTopUp(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	var body struct {
		Amount int64  `json:"amount"`
		Reason string `json:"reason"`
		Actor  string `json:"actor"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	customer, entry, err := svc.TopUpCredits(c.Param("id"), body.Amount, body.Reason, body.Actor)
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer": customer, "entry": entry})
}

func (h *Handler) PostCustomerCreditsDeduct(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	var body struct {
		Amount int64  `json:"amount"`
		Reason string `json:"reason"`
		Actor  string `json:"actor"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	customer, entry, err := svc.DeductCredits(c.Param("id"), body.Amount, body.Reason, body.Actor)
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer": customer, "entry": entry})
}

func (h *Handler) ResolveCustomerAPIKey(c *gin.Context) {
	svc, ok := customerStateService(c)
	if !ok {
		return
	}
	var body struct {
		APIKey         string `json:"api_key"`
		CustomerAPIKey string `json:"customer_api_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	apiKey := strings.TrimSpace(body.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(body.CustomerAPIKey)
	}
	resolved, err := svc.ResolveAPIKey(apiKey)
	if !handleCustomerStateError(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"customer": resolved.Customer, "api_key": resolved.APIKey})
}

func customerStateService(c *gin.Context) (*customerstate.Service, bool) {
	svc, err := customerstate.DefaultService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer state unavailable"})
		return nil, false
	}
	return svc, true
}

func handleCustomerStateError(c *gin.Context, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, customerstate.ErrCustomerNotFound), errors.Is(err, customerstate.ErrCustomerKeyMissing):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, customerstate.ErrCustomerInactive):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, customerstate.ErrInvalidAmount):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
	return false
}
