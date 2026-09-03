package ducklake

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type maintenanceExecStub struct {
	mu         sync.Mutex
	statements []string
	err        error
	block      <-chan struct{}
	started    chan struct{}
}

func (s *maintenanceExecStub) ExecContext(ctx context.Context, statement string, _ ...any) (sql.Result, error) {
	if s.started != nil {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	s.statements = append(s.statements, statement)
	s.mu.Unlock()
	return nil, s.err
}

func (s *maintenanceExecStub) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.statements...)
}

func maintenanceContract() PostgresCatalogMaintenanceContract {
	pool := "pool-maintenance"
	expires := time.Now().Add(time.Hour)
	return PostgresCatalogMaintenanceContract{
		Catalog: PostgresCatalogConfig{
			PhysicalPoolID: pool,
			DuckLakeSecret: "ducklake_maintenance",
			PostgresSecret: "postgres_maintenance",
			MetadataSchema: MetadataSchemaForPool(pool),
			Mode:           PostgresCatalogWriter,
		},
		CatalogAlias:    catalogAlias,
		CatalogID:       "catalog-maintenance",
		PhysicalPoolID:  pool,
		MetadataSchema:  MetadataSchemaForPool(pool),
		DataPath:        "s3://bucket/objects",
		MaintenanceRole: "leapview_ducklake_maintenance",
		RuntimeRole:     defaultDuckLakeRuntimeRole,
		Lease:           PostgresCatalogMaintenanceLease{LeaseID: "lease-maintenance", OwnerID: "worker-maintenance", ExpiresAt: expires},
		Fence:           PostgresCatalogMaintenanceFence{OwnerID: "worker-maintenance", FencingEpoch: 1, LeaseExpiresAt: expires},
	}
}

func TestPostgresCatalogMaintenanceRunsExplicitBoundedSequence(t *testing.T) {
	stub := &maintenanceExecStub{}
	maintenance, err := NewPostgresCatalogMaintenance(stub, maintenanceContract())
	if err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Run(context.Background(), PostgresCatalogMaintenanceRequest{SnapshotIDs: []int64{9, 2, 2}, FileGrace: 2 * time.Hour, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Snapshots || !result.OldFiles || !result.Orphans || !result.DryRun {
		t.Fatalf("result = %#v", result)
	}
	calls := stub.calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %#v, want three phases", calls)
	}
	if !strings.Contains(calls[0], "CALL ducklake_expire_snapshots('lake', versions => [2, 9], dry_run => true)") {
		t.Fatalf("expiry call = %q", calls[0])
	}
	if !strings.Contains(calls[1], "CALL ducklake_cleanup_old_files('lake', older_than => now() - INTERVAL '") || !strings.Contains(calls[1], "dry_run => true)") {
		t.Fatalf("old-file call = %q", calls[1])
	}
	if !strings.Contains(calls[2], "CALL ducklake_delete_orphaned_files('lake', older_than => now() - INTERVAL '") {
		t.Fatalf("orphan call = %q", calls[2])
	}
	for _, call := range calls {
		if strings.Contains(strings.ToLower(call), "checkpoint") || strings.Contains(strings.ToLower(call), "sqlite") {
			t.Fatalf("unsafe/legacy path in call %q", call)
		}
	}
}

func TestPostgresCatalogMaintenanceFailsClosedForSharedRuntimeAndAmbiguousCatalog(t *testing.T) {
	stub := &maintenanceExecStub{}
	shared := maintenanceContract()
	shared.SharedRuntimePool = true
	if _, err := NewPostgresCatalogMaintenance(stub, shared); !errors.Is(err, ErrSharedPoolMaintenance) {
		t.Fatalf("shared contract error = %v", err)
	}
	for name, mutate := range map[string]func(*PostgresCatalogMaintenanceContract){
		"missing PostgreSQL catalog": func(c *PostgresCatalogMaintenanceContract) { c.Catalog = PostgresCatalogConfig{} },
		"missing alias":              func(c *PostgresCatalogMaintenanceContract) { c.CatalogAlias = "" },
		"wrong alias":                func(c *PostgresCatalogMaintenanceContract) { c.CatalogAlias = "other" },
		"wrong schema":               func(c *PostgresCatalogMaintenanceContract) { c.MetadataSchema = "lake" },
		"missing role":               func(c *PostgresCatalogMaintenanceContract) { c.MaintenanceRole = "" },
		"runtime role":               func(c *PostgresCatalogMaintenanceContract) { c.MaintenanceRole = defaultDuckLakeRuntimeRole },
		"expired lease": func(c *PostgresCatalogMaintenanceContract) {
			c.Lease.ExpiresAt = time.Now().Add(-time.Minute)
			c.Fence.LeaseExpiresAt = c.Lease.ExpiresAt
		},
	} {
		t.Run(name, func(t *testing.T) {
			contract := maintenanceContract()
			mutate(&contract)
			if _, err := NewPostgresCatalogMaintenance(stub, contract); err == nil {
				t.Fatal("invalid contract unexpectedly accepted")
			}
			if got := len(stub.calls()); got != 0 {
				t.Fatalf("constructor sent %d SQL calls", got)
			}
		})
	}
}

func TestPostgresCatalogMaintenanceAcceptsDigitFirstOpaqueLeaseID(t *testing.T) {
	stub := &maintenanceExecStub{}
	contract := maintenanceContract()
	contract.Lease.LeaseID = "0198f2c0-7c7a-7f00-0000-000000000081"
	if _, err := NewPostgresCatalogMaintenance(stub, contract); err != nil {
		t.Fatalf("UUIDv7 lease identity rejected: %v", err)
	}
	contract.Lease.LeaseID = " 0198f2c0-7c7a-7f00-0000-000000000081"
	if _, err := NewPostgresCatalogMaintenance(stub, contract); !errors.Is(err, ErrPostgresCatalogMaintenanceLease) {
		t.Fatalf("whitespace-padded lease identity error = %v, want lease validation error", err)
	}

	// The relaxed first-rune rule applies only to the opaque lease identity;
	// owner identities remain subject to maintenanceID's strict contract.
	contract.Lease.LeaseID = "0198f2c0-7c7a-7f00-0000-000000000081"
	contract.Lease.OwnerID = "0198f2c0-7c7a-7f00-0000-000000000082"
	contract.Fence.OwnerID = contract.Lease.OwnerID
	if _, err := NewPostgresCatalogMaintenance(stub, contract); !errors.Is(err, ErrPostgresCatalogMaintenanceLease) {
		t.Fatalf("digit-first owner identity error = %v, want lease validation error", err)
	}
}

func TestPostgresCatalogMaintenanceRejectsRuntimeDBPool(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewPostgresCatalogMaintenance(db, maintenanceContract()); !errors.Is(err, ErrPostgresCatalogMaintenanceConnection) {
		t.Fatalf("runtime pool error = %v", err)
	}
}

func TestPostgresCatalogMaintenanceVerifySnapshotsExpired(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE SCHEMA lake; CREATE TABLE lake.snapshot_rows(snapshot_id BIGINT); CREATE MACRO lake.snapshots() AS TABLE SELECT snapshot_id FROM lake.snapshot_rows`); err != nil {
		t.Fatal(err)
	}
	maintenance, err := NewPostgresCatalogMaintenance(conn, maintenanceContract())
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.VerifySnapshotsExpired(context.Background(), nil); err != nil {
		t.Fatalf("empty verification = %v", err)
	}
	if err := maintenance.VerifySnapshotsExpired(context.Background(), []int64{11}); err != nil {
		t.Fatalf("missing snapshot verification = %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO lake.snapshot_rows VALUES (11)`); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.VerifySnapshotsExpired(context.Background(), []int64{11}); err == nil {
		t.Fatal("remaining snapshot was accepted as expired")
	}
}

func TestPostgresCatalogMaintenanceCancellationAndFenceStopBeforeNextPhase(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	stub := &maintenanceExecStub{block: block}
	contract := maintenanceContract()
	contract.Fence.Verify = func(context.Context) error {
		select {
		case <-started:
		default:
			close(started)
		}
		return nil
	}
	maintenance, err := NewPostgresCatalogMaintenance(stub, contract)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := maintenance.Run(ctx, PostgresCatalogMaintenanceRequest{SnapshotIDs: []int64{1}, FileGrace: time.Hour})
		done <- runErr
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run error = %v", err)
	}
	if got := len(stub.calls()); got != 0 {
		t.Fatalf("canceled phase sent %d SQL calls", got)
	}

	stale := maintenanceContract()
	stale.Fence.Verify = func(context.Context) error { return errors.New("stale maintenance fence") }
	staleMaintenance, err := NewPostgresCatalogMaintenance(&maintenanceExecStub{}, stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staleMaintenance.Run(context.Background(), PostgresCatalogMaintenanceRequest{SnapshotIDs: []int64{1}, FileGrace: time.Hour}); err == nil || !strings.Contains(err.Error(), "stale maintenance fence") {
		t.Fatalf("stale fence run error = %v", err)
	}
}

func TestPostgresCatalogMaintenanceLeaseExpiryCancelsBlockedPhase(t *testing.T) {
	started := make(chan struct{})
	block := make(chan struct{})
	stub := &maintenanceExecStub{block: block, started: started}
	contract := maintenanceContract()
	expires := time.Now().Add(200 * time.Millisecond)
	contract.Lease.ExpiresAt = expires
	contract.Fence.LeaseExpiresAt = expires
	maintenance, err := NewPostgresCatalogMaintenance(stub, contract)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct {
		result PostgresCatalogMaintenanceResult
		err    error
	}, 1)
	go func() {
		result, runErr := maintenance.Run(context.Background(), PostgresCatalogMaintenanceRequest{SnapshotIDs: []int64{1}, FileGrace: time.Hour})
		done <- struct {
			result PostgresCatalogMaintenanceResult
			err    error
		}{result: result, err: runErr}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance phase did not start")
	}

	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Fatalf("expired lease run error = %v", outcome.err)
		}
		if outcome.result.Snapshots || outcome.result.OldFiles || outcome.result.Orphans {
			t.Fatalf("expired lease reported phase success: %#v", outcome.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked maintenance phase was not canceled at lease expiry")
	}
	if got := len(stub.calls()); got != 0 {
		t.Fatalf("expired phase recorded %d SQL calls", got)
	}
}

func TestPostgresCatalogMaintenanceRevalidatesFenceAfterPhase(t *testing.T) {
	stub := &maintenanceExecStub{}
	contract := maintenanceContract()
	verifyCalls := 0
	contract.Fence.Verify = func(context.Context) error {
		verifyCalls++
		if verifyCalls > 1 {
			return errors.New("maintenance fence lost after native call")
		}
		return nil
	}
	maintenance, err := NewPostgresCatalogMaintenance(stub, contract)
	if err != nil {
		t.Fatal(err)
	}

	result, err := maintenance.Run(context.Background(), PostgresCatalogMaintenanceRequest{SnapshotIDs: []int64{1}, FileGrace: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "maintenance fence lost after native call") {
		t.Fatalf("post-call fence error = %v", err)
	}
	if result.Snapshots || result.OldFiles || result.Orphans {
		t.Fatalf("post-call fence loss reported phase success: %#v", result)
	}
	if got := len(stub.calls()); got != 1 {
		t.Fatalf("calls after post-call fence loss = %d, want one", got)
	}
}
