package authoring

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// WithAuditIntent carries the command's source-built durable audit intent into
// the authoring repository transaction. The repository fills identities that
// are allocated as part of the mutation before handing the intent to Access.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// AuditIntentFromContext returns the intent supplied by the command producer.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}
