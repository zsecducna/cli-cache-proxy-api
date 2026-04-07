package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

func (h *Handler) currentUsageSnapshot(ctx *gin.Context) usage.StatisticsSnapshot {
	provider := strings.TrimSpace(ctx.Query("provider"))
	var persisted usage.StatisticsSnapshot
	hasPersisted := false
	if store := usage.GetCacheStatisticsStore(); store != nil {
		if snapshot, err := store.StatisticsSnapshotByProvider(ctx.Request.Context(), provider); err == nil {
			persisted = snapshot
			hasPersisted = true
		}
	}
	hasLive := h != nil && h.usageStats != nil
	if !hasLive {
		if hasPersisted {
			return persisted
		}
		return usage.StatisticsSnapshot{}
	}
	live := h.usageStats.Snapshot()
	if provider != "" {
		live = usage.FilterStatisticsSnapshotByProvider(live, provider)
	}
	if !hasPersisted {
		return live
	}
	return usage.MergeStatisticsSnapshots(persisted, live)
}

// GetUsageStatistics returns the request statistics snapshot, preferring persisted cache-statistics history when available.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	snapshot := h.currentUsageSnapshot(c)
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	snapshot := h.currentUsageSnapshot(c)
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.usageStats.MergeSnapshot(payload.Usage)
	snapshot := h.usageStats.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}
