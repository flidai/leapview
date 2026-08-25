package agent

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

type auditIntentContextKey struct{}

// WithAuditIntent carries a command's source-owned durable audit intent into
// the repository transaction that commits the corresponding agent state.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// AuditIntentFromContext returns the command intent supplied by the transport
// boundary. The repository remains responsible for filling IDs that are only
// known after an insert (for example a newly created conversation).
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}
