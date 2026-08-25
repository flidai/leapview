package release

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// WithAuditIntent carries a source-built durable audit intent into a release
// repository transaction. The repository owns the transaction and records the
// intent immediately before commit.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// AuditIntentFromContext returns the command intent supplied by the release
// command producer, when present.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}
