package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func (h *Handler) GetCacheStatistics(c *gin.Context) {
	limit := readPositiveIntQuery(c, "limit", 50)
	modelLimit := readPositiveIntQuery(c, "model_limit", 10)
	days := readPositiveIntQuery(c, "days", 14)

	store := usage.GetCacheStatisticsStore()
	snapshot, err := store.Snapshot(c.Request.Context(), limit, modelLimit, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cache_statistics": snapshot.Redacted()})
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
