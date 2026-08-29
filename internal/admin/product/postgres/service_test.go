package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	product "github.com/flidai/leapview/internal/admin/product"
	"github.com/jackc/pgx/v5"
)

type serviceBlobStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *serviceBlobStore) Put(_ context.Context, blob product.Blob, body io.Reader) (product.Blob, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return product.Blob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[blob.SHA256] = b
	return blob, nil
}

func (s *serviceBlobStore) Open(_ context.Context, digest string) (io.ReadCloser, error) {
	s.mu.Lock()
	b, ok := s.values[digest]
	s.mu.Unlock()
	if !ok {
		return nil, product.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

type probeAudit struct {
	fail bool
}

func (a probeAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, input AuditInput) error {
	if a.fail {
		return errors.New("audit unavailable")
	}
	_, err := tx.Exec(ctx, `INSERT INTO product_audit_probe(event_id, principal_id) VALUES ($1, $2)`, input.EventID, input.PrincipalID)
	return err
}

func TestProductServiceUsesNativePostgresStorageAndAuditTransaction(t *testing.T) {
	db := productTestDB(t)
	if _, err := db.Exec(t.Context(), `CREATE TABLE product_audit_probe(event_id text PRIMARY KEY, principal_id text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	audit := probeAudit{}
	repo, err := NewWithOptions(db, Options{Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	service, err := product.NewWithStorage(repo, &serviceBlobStore{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.SetDisplayName(t.Context(), initial.Revision, "Native PG", product.Mutation{PrincipalID: "00000000-0000-0000-0000-000000000001", RequestID: "00000000-0000-0000-0000-000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Native PG" || updated.Revision != initial.Revision+1 {
		t.Fatalf("updated identity = %#v", updated)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM product_audit_probe`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit rows = %d, want 1", count)
	}
	if product.AuditEventID(product.Mutation{PrincipalID: "p", RequestID: "r"}, "product.identity.updated", `{"fields":["displayName"]}`, 1) != product.AuditEventID(product.Mutation{PrincipalID: "p", RequestID: "r"}, "product.identity.updated", `{"fields":["displayName"]}`, 1) {
		t.Fatal("audit event id is not deterministic")
	}
}

func TestProductServiceAuditFailureRollsBackNativeMutation(t *testing.T) {
	db := productTestDB(t)
	if _, err := db.Exec(t.Context(), `CREATE TABLE product_audit_probe(event_id text PRIMARY KEY, principal_id text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	repo, err := NewWithOptions(db, Options{Audit: probeAudit{fail: true}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := product.NewWithStorage(repo, &serviceBlobStore{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetDisplayName(t.Context(), initial.Revision, "Must Roll Back", product.Mutation{PrincipalID: "00000000-0000-0000-0000-000000000001", RequestID: "00000000-0000-0000-0000-000000000003"}); err == nil {
		t.Fatal("mutation succeeded despite audit failure")
	}
	got, err := service.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != initial {
		t.Fatalf("identity changed after audit rollback: before=%#v after=%#v", initial, got)
	}
}
