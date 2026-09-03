package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type trackingRows struct {
	scanErr    error
	valuesErr  error
	closeCalls int
}

func (r *trackingRows) Close()                                       { r.closeCalls++ }
func (r *trackingRows) Err() error                                   { return nil }
func (r *trackingRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *trackingRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *trackingRows) Next() bool                                   { return false }
func (r *trackingRows) Scan(...any) error                            { return r.scanErr }
func (r *trackingRows) Values() ([]any, error)                       { return nil, r.valuesErr }
func (r *trackingRows) RawValues() [][]byte                          { return nil }
func (r *trackingRows) Conn() *pgx.Conn                              { return nil }

type trackingLease struct{ releases int }

func (l *trackingLease) Release() { l.releases++ }

func TestLeasedRowsScanErrorClosesRowsAndReleasesLease(t *testing.T) {
	sentinel := errors.New("scan failed")
	rows := &trackingRows{scanErr: sentinel}
	lease := &trackingLease{}
	wrapped := &leasedRows{Rows: rows, conn: lease}

	if err := wrapped.Scan(new(int)); !errors.Is(err, sentinel) {
		t.Fatalf("leasedRows.Scan() error = %v, want %v", err, sentinel)
	}
	if rows.closeCalls != 1 {
		t.Fatalf("underlying rows Close calls = %d, want 1", rows.closeCalls)
	}
	if lease.releases != 1 {
		t.Fatalf("pool lease releases = %d, want 1", lease.releases)
	}
}

func TestLeasedRowsValuesErrorClosesRowsAndReleasesLease(t *testing.T) {
	sentinel := errors.New("values failed")
	rows := &trackingRows{valuesErr: sentinel}
	lease := &trackingLease{}
	wrapped := &leasedRows{Rows: rows, conn: lease}

	if _, err := wrapped.Values(); !errors.Is(err, sentinel) {
		t.Fatalf("leasedRows.Values() error = %v, want %v", err, sentinel)
	}
	if rows.closeCalls != 1 {
		t.Fatalf("underlying rows Close calls = %d, want 1", rows.closeCalls)
	}
	if lease.releases != 1 {
		t.Fatalf("pool lease releases = %d, want 1", lease.releases)
	}
}

func TestLeasedRowsNilRowsReleasesLeaseOnError(t *testing.T) {
	lease := &trackingLease{}
	wrapped := &leasedRows{conn: lease}

	if err := wrapped.Scan(new(int)); err == nil {
		t.Fatal("leasedRows.Scan() unexpectedly succeeded with nil rows")
	}
	if lease.releases != 1 {
		t.Fatalf("pool lease releases after nil rows = %d, want 1", lease.releases)
	}
}

func TestLeasedRowsSuccessRetainsLeaseUntilClose(t *testing.T) {
	rows := &trackingRows{}
	lease := &trackingLease{}
	wrapped := &leasedRows{Rows: rows, conn: lease}

	if err := wrapped.Scan(new(int)); err != nil {
		t.Fatalf("leasedRows.Scan() error = %v", err)
	}
	if rows.closeCalls != 0 || lease.releases != 0 {
		t.Fatalf("successful scan closed rows/released lease = %d/%d, want 0/0", rows.closeCalls, lease.releases)
	}
	wrapped.Close()
	if rows.closeCalls != 1 || lease.releases != 1 {
		t.Fatalf("after Close rows/lease = %d/%d, want 1/1", rows.closeCalls, lease.releases)
	}
}
