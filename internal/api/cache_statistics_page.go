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
const managementCacheStatisticsEndMarker = "/cliproxy-cache-stats-overlay"

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
	if len(page) == 0 {
		return page
	}
	page = replaceExistingManagementEnhancer(page)
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

func replaceExistingManagementEnhancer(page []byte) []byte {
	startComment := []byte("<!-- " + managementCacheStatisticsMarker + " -->")
	endComment := []byte("<!-- " + managementCacheStatisticsEndMarker + " -->")
	start := bytes.Index(page, startComment)
	if start < 0 {
		markerStart := bytes.Index(page, []byte(managementCacheStatisticsMarker))
		if markerStart < 0 {
			return page
		}
		start = markerStart
		if commentStart := bytes.LastIndex(page[:markerStart], []byte("<!--")); commentStart >= 0 {
			if commentEnd := bytes.Index(page[commentStart:], []byte("-->")); commentEnd >= 0 && commentStart+commentEnd+len("-->") >= markerStart {
				start = commentStart
			}
		}
	}
	if end := bytes.Index(page[start:], endComment); end >= 0 {
		end += start + len(endComment)
		return append(page[:start], page[end:]...)
	}
	lower := strings.ToLower(string(page[start:]))
	if scriptEnd := strings.Index(lower, "</script>"); scriptEnd >= 0 {
		end := start + scriptEnd + len("</script>")
		return append(page[:start], page[end:]...)
	}
	return page
}
