package postgres

import (
	"context"
	"errors"

	product "github.com/flidai/leapview/internal/admin/product"
	"github.com/jackc/pgx/v5"
)

// AuditInput is the product-owned strict handoff to the application adapter.
// The transaction is native pgx.Tx and remains owned by the product mutation.
type AuditInput struct {
	EventID           string
	PrincipalID       string
	Source            string
	Operation         string
	Action            string
	ResourceKind      string
	ResourceID        string
	Capability        string
	Outcome           string
	RequestID         string
	CorrelationID     string
	AggregateKey      string
	AggregateSequence int64
	RequestDigest     string
	MetadataJSON      string
}

type AuditPort interface {
	RecordAuditEvent(context.Context, pgx.Tx, AuditInput) error
}

var ErrAuditUnavailable = errors.New("product PostgreSQL audit port is unavailable")

var _ product.Storage = (*Repository)(nil)
