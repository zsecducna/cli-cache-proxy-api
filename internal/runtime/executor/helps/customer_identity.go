package helps

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

type customerIDContextKey struct{}

// WithCustomerID attaches a trusted customer identity to the request context.
func WithCustomerID(ctx context.Context, customerID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	trimmed := strings.TrimSpace(customerID)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, customerIDContextKey{}, trimmed)
}

// CustomerIDFromContext returns the trusted customer identity propagated from
// the internal gateway, if present.
func CustomerIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if customerID, _ := ctx.Value(customerIDContextKey{}).(string); strings.TrimSpace(customerID) != "" {
		return strings.TrimSpace(customerID)
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	value, exists := ginCtx.Get("customerID")
	if !exists {
		return ""
	}
	customerID, _ := value.(string)
	return strings.TrimSpace(customerID)
}
