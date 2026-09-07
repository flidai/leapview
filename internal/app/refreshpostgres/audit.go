// Package refreshpostgres contains process-composition bridges that connect
// the refresh module's narrow contracts to sibling PostgreSQL authorities.
//
// These adapters intentionally live outside refresh/module: refresh module
// code must not import another capability's concrete persistence adapter.
package refreshpostgres

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/google/uuid"
)

// PostgresCancelAuditWriterAdapter appends the immutable access audit event through
// the cancellation transaction. The access repository owns canonical event
// identity, metadata validation and replay conflict handling.
type PostgresCancelAuditWriterAdapter struct {
	Audit *accesspostgres.AuditRepository
}

func NewPostgresCancelAuditWriterAdapter(audit *accesspostgres.AuditRepository) (*PostgresCancelAuditWriterAdapter, error) {
	if audit == nil {
		return nil, errors.New("access PostgreSQL audit repository is required")
	}
	return &PostgresCancelAuditWriterAdapter{Audit: audit}, nil
}

func (w *PostgresCancelAuditWriterAdapter) RecordRefreshCancelAuditTx(ctx context.Context, tx refreshpostgres.Tx, intent access.AuditIntent) error {
	if w == nil || w.Audit == nil {
		return errors.New("access PostgreSQL audit repository is required")
	}
	if tx == nil {
		return errors.New("refresh cancellation audit transaction is required")
	}
	_, err := w.Audit.RecordAuditEvent(ctx, tx, intent)
	return err
}

// RecordRefreshAuditTx appends the generated create-refresh audit intent in
// the caller-owned admission transaction. Older generated contracts use a
// sha256 event identity while the native Access table stores UUIDs; the
// deterministic UUID-shaped projection below preserves replay identity
// without introducing a second write or transaction.
func (w *PostgresCancelAuditWriterAdapter) RecordRefreshAuditTx(ctx context.Context, tx refreshpostgres.Tx, intent access.AuditIntent) error {
	if w == nil || w.Audit == nil {
		return errors.New("access PostgreSQL audit repository is required")
	}
	if tx == nil {
		return errors.New("refresh audit transaction is required")
	}
	intent.EventID = nativeAuditEventID(intent.EventID)
	_, err := w.Audit.RecordAuditEvent(ctx, tx, intent)
	return err
}

func nativeAuditEventID(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return value
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	if err != nil || len(decoded) < 16 {
		return value
	}
	return uuid.UUID(decoded[:16]).String()
}
