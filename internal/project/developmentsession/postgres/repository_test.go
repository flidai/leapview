package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/project/developmentsession"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *[]byte:
			*target = append((*target)[:0], value.([]byte)...)
		case *int64:
			*target = value.(int64)
		case *time.Time:
			*target = value.(time.Time)
		default:
			panic("unsupported fake row destination")
		}
	}
	return nil
}

type fakeDB struct {
	row   pgx.Row
	query string
	args  []any
}

func (db *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (db *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not implemented")
}
func (db *fakeDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	db.query, db.args = query, args
	return db.row
}

func TestResolveIsOwnerAndScopeBound(t *testing.T) {
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	now := time.Now().UTC()
	db := &fakeDB{row: fakeRow{values: []any{key.ID(), key.OwnerID, key.CheckoutID, key.WorktreeID, key.ProjectID.String(), key.TargetID, key.Environment, "candidate_1", "sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("b", 64), "https://target.example/candidates/candidate_1", "candidate_1", "sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("b", 64), "https://target.example/candidates/candidate_1", []byte(`[]`), int64(2), now, now}}}
	record, err := New(db).Resolve(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != key.ID() || record.Revision != 2 {
		t.Fatalf("record = %#v", record)
	}
	if len(db.args) != 6 || db.args[0] != key.OwnerID || db.args[1] != key.CheckoutID || db.args[2] != key.WorktreeID || db.args[3] != key.ProjectID.String() {
		t.Fatalf("query scope args = %#v", db.args)
	}
	if _, err := New(&fakeDB{row: fakeRow{err: pgx.ErrNoRows}}).Resolve(context.Background(), key); !errors.Is(err, developmentsession.ErrNotFound) {
		t.Fatalf("missing row error = %v", err)
	}
}

func TestMarshalDiagnosticsPreservesEmptyArrayForFirstSession(t *testing.T) {
	encoded, err := marshalDiagnostics(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("first session diagnostics = %s, want []", encoded)
	}
	encoded, err = marshalDiagnostics([]developmentsession.Diagnostic{{Code: "INVALID", Message: "fix model"}})
	if err != nil || !strings.Contains(string(encoded), `"code":"INVALID"`) {
		t.Fatalf("retained diagnostics = %s, %v", encoded, err)
	}
}
