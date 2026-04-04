package api

import (
	"bytes"
	_ "embed"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const managementCacheStatisticsHash = "#cache-statistics"
const managementCacheStatisticsMarker = "cliproxy-cache-stats-overlay"

//go:embed assets/management-cache-statistics-inject.html
var managementCacheStatisticsInjectHTML []byte

func (s *Server) serveCacheStatisticsPage(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.RemoteManagement.DisableControlPanel || !s.managementRoutesEnabled.Load() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Redirect(http.StatusFound, "/management.html"+managementCacheStatisticsHash)
}

func enhanceManagementControlPanelHTML(page []byte) []byte {
	if len(page) == 0 || bytes.Contains(page, []byte(managementCacheStatisticsMarker)) {
		return page
	}
	lower := strings.ToLower(string(page))
	idx := strings.LastIndex(lower, "</body>")
	if idx < 0 {
		result := make([]byte, 0, len(page)+len(managementCacheStatisticsInjectHTML))
		result = append(result, page...)
		result = append(result, managementCacheStatisticsInjectHTML...)
		return result
	}
	result := make([]byte, 0, len(page)+len(managementCacheStatisticsInjectHTML))
	result = append(result, page[:idx]...)
	result = append(result, managementCacheStatisticsInjectHTML...)
	result = append(result, page[idx:]...)
	return result
}
