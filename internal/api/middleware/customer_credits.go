package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/customerstate"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func CustomerCreditsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		customerID := strings.TrimSpace(c.GetString("customerID"))
		if customerID == "" {
			c.Next()
			return
		}
		svc, err := customerstate.DefaultService()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "customer state unavailable"})
			return
		}
		customer, err := svc.GetCustomer(customerID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{
					"code":    "invalid_customer",
					"message": "customer account not found",
				},
			})
			return
		}
		if !customer.Active {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":    "customer_inactive",
					"message": "customer account is inactive",
				},
			})
			return
		}
		c.Set("customer", customer)
		c.Next()
	}
}

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
