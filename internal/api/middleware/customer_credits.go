package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func InternalCustomerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || !isLoopbackRemoteAddrInternal(c.Request.RemoteAddr) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "internal customer routes are restricted to loopback callers"})
			return
		}
		if !trustedInternalCustomerCaller(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "internal customer routes require trusted inline api-key authentication"})
			return
		}
		c.Next()
	}
}

func trustedInternalCustomerCaller(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if metadataValue, ok := c.Get("accessMetadata"); ok {
		if metadata, ok := metadataValue.(map[string]string); ok {
			if strings.TrimSpace(metadata["provider_type"]) == sdkaccess.AccessProviderTypeConfigAPIKey {
				return true
			}
		}
	}
	return strings.TrimSpace(c.GetString("accessProvider")) == sdkaccess.DefaultAccessProviderName
}

func isLoopbackRemoteAddrInternal(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
