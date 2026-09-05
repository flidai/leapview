package ducklake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResolveCommittedMarkerFreshUsesAReadOnlyFreshSessionPerCall(t *testing.T) {
	marker := CommitMarker{
		SchemaVersion: CommitMarkerSchemaVersion, DeliveryID: "delivery-fresh",
		GenerationID: "generation-fresh", AttemptID: "attempt-fresh",
		LeaseEpoch: 3, RequestDigest: "sha256:" + strings.Repeat("b", 64), PlanDigest: "sha256:" + strings.Repeat("a", 64), Project: "project:fresh",
		Environment: "production", PhysicalPoolID: "pool-fresh",
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	state := &freshMarkerState{canonical: canonical}
	connector := &freshMarkerConnector{state: state}
	db := sql.OpenDB(connector)
	defer db.Close()
	environment := &Environment{db: db, connector: connector, postgresCatalog: true, readOnly: true, physicalPoolID: marker.PhysicalPoolID}

	for i := 0; i < 2; i++ {
		got, err := environment.ResolveCommittedMarkerFresh(context.Background(), marker)
		if err != nil {
			t.Fatalf("fresh resolution %d: %v", i, err)
		}
		if want := (PhysicalMarkerResolution{SnapshotID: 73, Found: true}); got != want {
			t.Fatalf("fresh resolution %d = %#v, want %#v", i, got, want)
		}
	}
	if got := state.opens.Load(); got != 2 {
		t.Fatalf("fresh physical sessions opened = %d, want 2", got)
	}
}

func TestResolveCommittedMarkerFreshRequiresReadOnlyPostgresEnvironment(t *testing.T) {
	marker := CommitMarker{SchemaVersion: CommitMarkerSchemaVersion, DeliveryID: "delivery", GenerationID: "generation", AttemptID: "attempt", LeaseEpoch: 1, RequestDigest: "sha256:" + strings.Repeat("b", 64), PlanDigest: "sha256:" + strings.Repeat("a", 64), Project: "project", Environment: "production", PhysicalPoolID: "pool"}
	state := &freshMarkerState{}
	connector := &freshMarkerConnector{state: state}
	db := sql.OpenDB(connector)
	defer db.Close()
	for name, environment := range map[string]*Environment{
		"writer":       {db: db, connector: connector, postgresCatalog: true, physicalPoolID: marker.PhysicalPoolID},
		"non-postgres": {db: db, connector: connector, readOnly: true, physicalPoolID: marker.PhysicalPoolID},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := environment.ResolveCommittedMarkerFresh(context.Background(), marker); err == nil {
				t.Fatal("writable/non-PostgreSQL environment unexpectedly resolved marker")
			}
		})
	}
	closed := &Environment{db: db, connector: connector, postgresCatalog: true, readOnly: true, physicalPoolID: marker.PhysicalPoolID}
	closed.closed.Store(true)
	if _, err := closed.ResolveCommittedMarkerFresh(context.Background(), marker); !errors.Is(err, ErrEnvironmentClosed) {
		t.Fatalf("closed environment error = %v, want %v", err, ErrEnvironmentClosed)
	}
	crossPool := &Environment{db: db, connector: connector, postgresCatalog: true, readOnly: true, physicalPoolID: "other-pool"}
	if _, err := crossPool.ResolveCommittedMarkerFresh(context.Background(), marker); err == nil {
		t.Fatal("cross-pool marker unexpectedly resolved")
	}
}

type freshMarkerState struct {
	canonical string
	opens     atomic.Int64
}

type freshMarkerConnector struct{ state *freshMarkerState }

func (c *freshMarkerConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.opens.Add(1)
	return &freshMarkerConn{state: c.state}, nil
}

func (c *freshMarkerConnector) Driver() driver.Driver { return c }
func (c *freshMarkerConnector) Open(string) (driver.Conn, error) {
	return c.Connect(context.Background())
}

type freshMarkerConn struct{ state *freshMarkerState }

func (*freshMarkerConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not used")
}
func (*freshMarkerConn) Close() error { return nil }
func (*freshMarkerConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not used")
}
func (c *freshMarkerConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "last_committed_snapshot") {
		return &freshMarkerRows{columns: []string{"id"}}, nil
	}
	return &freshMarkerRows{columns: []string{"snapshot_id", "commit_extra_info"}, values: [][]driver.Value{{int64(73), c.state.canonical}}}, nil
}

type freshMarkerRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *freshMarkerRows) Columns() []string { return r.columns }
func (*freshMarkerRows) Close() error        { return nil }
func (r *freshMarkerRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
