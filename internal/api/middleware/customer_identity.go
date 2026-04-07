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
const CustomerEmailHeader = "X-CheapRouter-User-Email"

// CustomerIdentityMiddleware accepts a trusted gateway-supplied customer header
// only for loopback requests authenticated by the config inline API key provider.
func CustomerIdentityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		customerID := strings.TrimSpace(c.GetHeader(CustomerIDHeader))
		customerEmail := strings.TrimSpace(c.GetHeader(CustomerEmailHeader))
		if customerID == "" && customerEmail == "" {
			c.Next()
			return
		}
		if !trustedInternalCustomerIdentityRequest(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "customer identity header is only accepted from trusted internal callers",
			})
			return
		}
		ctx := c.Request.Context()
		if customerID != "" {
			c.Set("customerID", customerID)
			ctx = helps.WithCustomerID(ctx, customerID)
		}
		if customerEmail != "" {
			c.Set("customerEmail", customerEmail)
			ctx = helps.WithCustomerEmail(ctx, customerEmail)
		}
		c.Request = c.Request.WithContext(ctx)
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
