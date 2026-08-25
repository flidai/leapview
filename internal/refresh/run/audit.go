package run

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

type auditIntentContextKey struct{}

func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}
