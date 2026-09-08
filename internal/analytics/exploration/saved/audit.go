package saved

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// WithAuditIntent carries the source-built Access audit intent into a future
// repository transaction. The repository adapter owns the transaction and
// must require an access.AuditIntentRecorder before committing the mutation.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// AuditIntentFromContext returns the typed Access audit intent supplied by the
// command producer. A missing value is distinct from a zero intent so adapters
// can fail closed when a recorder is required.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}
