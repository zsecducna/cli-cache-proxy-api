package helps

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

type customerIDContextKey struct{}
type customerEmailContextKey struct{}

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

// WithCustomerEmail attaches a trusted customer email to the request context.
func WithCustomerEmail(ctx context.Context, customerEmail string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	trimmed := strings.TrimSpace(customerEmail)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, customerEmailContextKey{}, trimmed)
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

// CustomerEmailFromContext returns the trusted customer email propagated from
// the internal gateway, if present.
func CustomerEmailFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if customerEmail, _ := ctx.Value(customerEmailContextKey{}).(string); strings.TrimSpace(customerEmail) != "" {
		return strings.TrimSpace(customerEmail)
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	value, exists := ginCtx.Get("customerEmail")
	if !exists {
		return ""
	}
	customerEmail, _ := value.(string)
	return strings.TrimSpace(customerEmail)
}
