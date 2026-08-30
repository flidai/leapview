package publication

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// CommandInvocation carries transport identity into a dashboard publication
// command without exposing APIGen's generic target-map assembly to callers.
type CommandInvocation struct {
	OperationID    string
	Surface        string
	IdempotencyKey string
	RequestID      string
	CorrelationID  string
	// ExpectedRevision is the caller-observed publication revision used for
	// optimistic concurrency. Command producers must supply it explicitly;
	// the service must never perform a hidden read to infer a token.
	ExpectedRevision int64
}

// WithAuditIntent carries the source-built audit intent into the publication
// repository. The repository owns the SQLite transaction and records this
// intent before committing the publication mutation; callers never hand a
// concrete database adapter across the service boundary.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// AuditIntentFromContext returns the transaction-scoped audit intent, when a
// command producer supplied one.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}
