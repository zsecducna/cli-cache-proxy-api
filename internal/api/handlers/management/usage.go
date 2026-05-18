package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

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
	providers := providerNamesForUsageFilter(h.cfg, provider)
	filterSnapshot := func(snapshot usage.StatisticsSnapshot) usage.StatisticsSnapshot {
		if len(providers) == 0 {
			return snapshot
		}
		return usage.FilterStatisticsSnapshotByProviders(snapshot, providers)
	}
	var persisted usage.StatisticsSnapshot
	hasPersisted := false
	if store := usage.GetCacheStatisticsStore(); store != nil {
		if snapshot, err := store.StatisticsSnapshotByProviders(ctx.Request.Context(), providers); err == nil {
			persisted = snapshot
			hasPersisted = true
		}
	}
	hasLive := h != nil && h.usageStats != nil
	if !hasLive {
		if hasPersisted {
			return filterSnapshot(persisted)
		}
		return usage.StatisticsSnapshot{}
	}
	live := h.usageStats.Snapshot()
	live = filterSnapshot(live)
	if !hasPersisted {
		return live
	}
	return filterSnapshot(usage.MergeStatisticsSnapshots(persisted, live))
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

	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
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

// GetUsageQueue pops queued usage records from the Redis-compatible usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}
