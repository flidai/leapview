//go:build duckdb_arrow

package ducklake

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/extension"
)

func TestPostgresRuntimeBranchRequiresPoolAdmissionAndNoFileCatalog(t *testing.T) {
	requestDigest := digestForRuntimeTest("request")
	planDigest := digestForRuntimeTest("plan")
	marker := CommitMarker{SchemaVersion: CommitMarkerSchemaVersion, DeliveryID: "delivery", GenerationID: "generation", AttemptID: "attempt", LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project", Environment: "prod", PhysicalPoolID: "pool"}
	config := Config{CatalogPath: "/tmp/catalog.duckdb", PostgresCatalog: &PostgresCatalogConfig{PhysicalPoolID: "pool", DuckLakeSecret: "lake_secret", PostgresSecret: "pg_secret", MetadataSchema: MetadataSchemaForPool("pool"), Mode: PostgresCatalogWriter}, CommitMarker: &marker}
	if _, err := Open(context.Background(), config); err == nil || !strings.Contains(err.Error(), "extension admission") {
		t.Fatalf("error=%v, want extension admission before catalog construction", err)
	}
}

func TestPostgresRuntimeCommitMarkerReconcilesExactSnapshot(t *testing.T) {
	ctx := context.Background()
	digest := func(value string) string { return digestForRuntimeTest(value) }
	marker := CommitMarker{SchemaVersion: CommitMarkerSchemaVersion, DeliveryID: "delivery", GenerationID: "generation", AttemptID: "attempt", LeaseEpoch: 1, RequestDigest: digest("request"), PlanDigest: digest("plan"), Project: "project", Environment: "prod", PhysicalPoolID: "pool"}
	env, err := Open(ctx, Config{RootDir: t.TempDir(), CommitMarker: &marker, ExtensionAdmission: runtimeExtensionAdmission(t)})
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	snapshot, err := env.Commit(ctx, "ignored", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE model_marker AS SELECT 1 AS id")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveCommittedSnapshot(ctx, env.sqlDB(), marker)
	if err != nil {
		t.Fatalf("resolve exact marker: %v", err)
	}
	if resolved != snapshot {
		t.Fatalf("resolved snapshot=%d, committed=%d", resolved, snapshot)
	}
	called := false
	if _, err := env.Commit(ctx, "ignored", nil, func(*sql.Tx) error {
		called = true
		return nil
	}); err != ErrCommitMarkerAlreadyUsed {
		t.Fatalf("second marker-mode commit error=%v, want %v", err, ErrCommitMarkerAlreadyUsed)
	}
	if called {
		t.Fatal("second marker-mode commit invoked materialization callback")
	}
}

func TestCommitMarkerAckFailureReconcilesOnFreshSession(t *testing.T) {
	marker := CommitMarker{SchemaVersion: CommitMarkerSchemaVersion, DeliveryID: "delivery", GenerationID: "generation", AttemptID: "attempt", LeaseEpoch: 1, RequestDigest: digestForRuntimeTest("request"), PlanDigest: digestForRuntimeTest("plan"), Project: "project", Environment: "prod", PhysicalPoolID: "pool"}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	state := &ackFailureState{marker: canonical}
	connector := ackFailureConnector{state: state}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	env := &Environment{db: db, connector: connector, catalogLock: "ack-failure", commitMarker: &marker, fatal: make(chan struct{}), extensions: map[string]*extensionLoad{}}
	ctx := context.Background()
	snapshot, err := env.Commit(ctx, "ignored", nil, func(*sql.Tx) error { return nil })
	if err != nil {
		t.Fatalf("commit ACK reconciliation: %v", err)
	}
	if snapshot != 42 {
		t.Fatalf("reconciled snapshot=%d, want 42", snapshot)
	}
	if got := state.opens.Load(); got != 2 {
		t.Fatalf("physical sessions opened=%d, want transaction + fresh lookup", got)
	}
	if got := state.brokenQueries.Load(); got != 0 {
		t.Fatalf("broken transaction session served %d reconciliation queries", got)
	}
	if got := state.connectorCloses.Load(); got != 0 {
		t.Fatalf("reconciliation closed the environment connector %d times", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := state.connectorCloses.Load(); got != 1 {
		t.Fatalf("environment connector closes=%d, want 1 after environment DB close", got)
	}
}

var errAckUnknown = errors.New("commit acknowledgement unavailable")

type ackFailureState struct {
	marker          string
	opens           atomic.Int64
	brokenQueries   atomic.Int64
	connectorCloses atomic.Int64
}

type ackFailureConnector struct{ state *ackFailureState }

func (c ackFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return &ackFailureConn{state: c.state, ordinal: c.state.opens.Add(1)}, nil
}

func (c ackFailureConnector) Driver() driver.Driver { return c }

func (c ackFailureConnector) Close() error {
	c.state.connectorCloses.Add(1)
	return nil
}

func (c ackFailureConnector) Open(string) (driver.Conn, error) {
	return c.Connect(context.Background())
}

type ackFailureConn struct {
	state   *ackFailureState
	ordinal int64
}

func (c *ackFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *ackFailureConn) Close() error              { return nil }
func (c *ackFailureConn) Begin() (driver.Tx, error) { return &ackFailureTx{}, nil }

func (c *ackFailureConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &ackFailureTx{}, nil
}

func (c *ackFailureConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (c *ackFailureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.ordinal == 1 {
		c.state.brokenQueries.Add(1)
		return nil, errors.New("transaction session cannot reconcile")
	}
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "last_committed_snapshot"):
		return &ackFailureRows{columns: []string{"id"}, values: []driver.Value{int64(42)}}, nil
	case strings.Contains(lower, "snapshots()") && strings.Contains(lower, "where snapshot_id"):
		return &ackFailureRows{columns: []string{"commit_extra_info"}, values: []driver.Value{c.state.marker}}, nil
	case strings.Contains(lower, "snapshots()"):
		return &ackFailureRows{columns: []string{"snapshot_id", "commit_extra_info"}, values: []driver.Value{int64(42), c.state.marker}}, nil
	default:
		return nil, errors.New("unexpected reconciliation query")
	}
}

type ackFailureTx struct{}

func (*ackFailureTx) Commit() error   { return errAckUnknown }
func (*ackFailureTx) Rollback() error { return nil }

type ackFailureRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *ackFailureRows) Columns() []string { return r.columns }
func (r *ackFailureRows) Close() error      { return nil }
func (r *ackFailureRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}

func digestForRuntimeTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func runtimeExtensionAdmission(t *testing.T) extension.Admission {
	t.Helper()
	return extensionfixture.New(t, "ducklake").Admission
}
