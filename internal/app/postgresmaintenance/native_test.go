package postgresmaintenance

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type previewDB struct{ tx *previewTx }

func (db previewDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (previewDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (previewDB) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (db previewDB) Begin(context.Context) (pgx.Tx, error)                { return db.tx, nil }

type previewTx struct {
	pgx.Tx
	rolledBack bool
}

func (tx *previewTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

func TestNewNativeRejectsNilDatabase(t *testing.T) {
	if _, err := NewNative(nil); err == nil {
		t.Fatal("NewNative(nil) unexpectedly succeeded")
	}
}

func TestPreviewAlwaysRollsBackInvalidPolicy(t *testing.T) {
	tx := &previewTx{}
	native, err := NewNative(previewDB{tx: tx})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := native.Preview(t.Context(), Policy{}); err == nil {
		t.Fatal("Preview accepted an invalid policy")
	}
	if !tx.rolledBack {
		t.Fatal("Preview did not roll back its outer transaction")
	}
}

func TestNilNativeFailsClosed(t *testing.T) {
	var native *Native
	if _, err := native.Run(t.Context(), Policy{}); err == nil {
		t.Fatal("nil Native.Run unexpectedly succeeded")
	}
	if _, err := native.Preview(t.Context(), Policy{}); err == nil {
		t.Fatal("nil Native.Preview unexpectedly succeeded")
	}
}
