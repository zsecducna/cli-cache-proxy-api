package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/customerstate"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func (h *Handler) GetCacheStatistics(c *gin.Context) {
	limit := readPositiveIntQuery(c, "limit", 50)
	modelLimit := readPositiveIntQuery(c, "model_limit", 10)
	days := readPositiveIntQuery(c, "days", 14)
	provider := strings.TrimSpace(c.Query("provider"))

	store := usage.GetCacheStatisticsStore()
	var (
		snapshot usage.CacheStatisticsSnapshot
		err      error
	)
	if sinceRaw := c.Query("since"); sinceRaw != "" {
		since, errParse := time.Parse(time.RFC3339Nano, sinceRaw)
		if errParse != nil {
			since, errParse = time.Parse(time.RFC3339, sinceRaw)
		}
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since parameter"})
			return
		}
		snapshot, err = store.SnapshotSinceByProvider(c.Request.Context(), limit, modelLimit, since, provider)
	} else {
		snapshot, err = store.SnapshotByProvider(c.Request.Context(), limit, modelLimit, days, provider)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cache_statistics": enrichCacheStatisticsCustomerEmails(snapshot).Redacted()})
}

func readPositiveIntQuery(c *gin.Context, key string, fallback int) int {
	if c == nil {
		return fallback
	}
	raw := c.Query(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func enrichCacheStatisticsCustomerEmails(snapshot usage.CacheStatisticsSnapshot) usage.CacheStatisticsSnapshot {
	svc, err := customerstate.DefaultService()
	if err != nil || svc == nil {
		return snapshot
	}
	emailsByCustomerID := make(map[string]string, len(snapshot.RecentRequests))
	for i := range snapshot.RecentRequests {
		if strings.TrimSpace(snapshot.RecentRequests[i].CustomerEmail) != "" {
			continue
		}
		customerID := strings.TrimSpace(snapshot.RecentRequests[i].CustomerID)
		if customerID == "" {
			continue
		}
		email, ok := emailsByCustomerID[customerID]
		if !ok {
			customer, err := svc.GetCustomer(customerID)
			if err == nil {
				email = strings.TrimSpace(customer.Email)
			}
			emailsByCustomerID[customerID] = email
		}
		snapshot.RecentRequests[i].CustomerEmail = email
	}
	return snapshot
}
