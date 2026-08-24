package deployment

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// WithAuditIntent carries a command's durable audit handoff into the
// transaction that owns the deployment or approval mutation.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// WithoutAuditIntent prevents an automatic housekeeping transition (for
// example approval expiry) from consuming the caller's command intent.
func WithoutAuditIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, nil)
}

// AuditIntentFromContext returns a command intent supplied by the transport,
// when present.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}
