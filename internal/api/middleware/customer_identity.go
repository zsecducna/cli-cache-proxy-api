package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

const CustomerIDHeader = "X-CheapRouter-User-ID"

// CustomerIdentityMiddleware accepts a trusted gateway-supplied customer header
// only for loopback requests authenticated by the config inline API key provider.
func CustomerIdentityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		customerID := strings.TrimSpace(c.GetHeader(CustomerIDHeader))
		if customerID == "" {
			c.Next()
			return
		}
		if !trustedInternalCustomerIdentityRequest(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "customer identity header is only accepted from trusted internal callers",
			})
			return
		}
		c.Set("customerID", customerID)
		c.Request = c.Request.WithContext(helps.WithCustomerID(c.Request.Context(), customerID))
		c.Next()
	}
}

func trustedInternalCustomerIdentityRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || !isLoopbackRemoteAddr(c.Request.RemoteAddr) {
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

func isLoopbackRemoteAddr(remoteAddr string) bool {
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
