package module

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/admin/product"
	productpostgres "github.com/flidai/leapview/internal/admin/product/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNewProductServiceWithStorageRejectsUnmarkedStorage(t *testing.T) {
	_, err := NewProductServiceWithStorage(unmarkedProductStorageStub{}, productBlobStoreStub{})
	if err == nil || !strings.Contains(err.Error(), "native PostgreSQL") {
		t.Fatalf("unmarked product storage error = %v, want native PostgreSQL rejection", err)
	}
}

func TestNewProductServiceWithStorageRejectsUnconfiguredStorage(t *testing.T) {
	_, err := NewProductServiceWithStorage(markedProductStorageStub{}, productBlobStoreStub{})
	if err == nil || !strings.Contains(err.Error(), "configured native PostgreSQL") {
		t.Fatalf("unconfigured product storage error = %v, want configured native PostgreSQL rejection", err)
	}
}

func TestNewProductServiceWithStorageAcceptsConfiguredPostgreSQLRepository(t *testing.T) {
	repository, err := productpostgres.NewWithOptions(productModuleDBStub{}, productpostgres.Options{Audit: productModuleAuditStub{}})
	if err != nil {
		t.Fatalf("construct PostgreSQL product repository: %v", err)
	}
	service, err := NewProductServiceWithStorage(repository, productBlobStoreStub{})
	if err != nil {
		t.Fatalf("configured PostgreSQL product storage error = %v", err)
	}
	if service == nil {
		t.Fatal("configured PostgreSQL product service is nil")
	}
}

type unmarkedProductStorageStub struct{ product.Storage }

type markedProductStorageStub struct {
	product.Storage
	configured bool
}

func (markedProductStorageStub) PostgreSQLAuthority() {}
func (s markedProductStorageStub) Configured() bool   { return s.configured }

type productBlobStoreStub struct{}

func (productBlobStoreStub) Put(context.Context, product.Blob, io.Reader) (product.Blob, error) {
	return product.Blob{}, nil
}
func (productBlobStoreStub) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

type productModuleDBStub struct{}

func (productModuleDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (productModuleDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (productModuleDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type productModuleAuditStub struct{}

func (productModuleAuditStub) RecordAuditEvent(context.Context, pgx.Tx, productpostgres.AuditInput) error {
	return nil
}
