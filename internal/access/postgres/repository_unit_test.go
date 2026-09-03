package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var validFingerprintKey = []byte("0123456789abcdef0123456789abcdef")

type nonTransactionalDB struct{}

func (nonTransactionalDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (nonTransactionalDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (nonTransactionalDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type transactionalDB struct{ nonTransactionalDB }

func (transactionalDB) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("not used") }

// explicitTransactionDB models passing a caller-owned pgx.Tx to NewAccess.
// Embedding the interface keeps this test independent of pgx's concrete
// transaction implementation while preserving its complete method set.
type explicitTransactionDB struct{ pgx.Tx }

func TestNewAccessRequiresTransactionCapableDatabase(t *testing.T) {
	if _, err := NewAccess(nonTransactionalDB{}, FingerprintConfig{Key: validFingerprintKey}); err == nil {
		t.Fatal("NewAccess accepted a database without Begin")
	} else if !strings.Contains(err.Error(), "Begin(context.Context)") {
		t.Fatalf("NewAccess transaction capability error = %v", err)
	}
}

func TestNewAccessAcceptsTransactionCapableDatabase(t *testing.T) {
	repo, err := NewAccess(transactionalDB{}, FingerprintConfig{Key: validFingerprintKey})
	if err != nil {
		t.Fatalf("NewAccess(transactionalDB): %v", err)
	}
	if repo == nil {
		t.Fatal("NewAccess(transactionalDB) returned nil repository")
	}
}

func TestNewAccessAcceptsExplicitPostgreSQLTransaction(t *testing.T) {
	repo, err := NewAccess(explicitTransactionDB{}, FingerprintConfig{Key: validFingerprintKey})
	if err != nil {
		t.Fatalf("NewAccess(pgx.Tx): %v", err)
	}
	if repo == nil {
		t.Fatal("NewAccess(pgx.Tx) returned nil repository")
	}
}
