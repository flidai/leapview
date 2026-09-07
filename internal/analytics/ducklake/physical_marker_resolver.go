package ducklake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DuckLakePhysicalMarkerResolverFactory opens the narrow read-only
// environment used by native build recovery. The supplied configuration is
// copied and forced onto the marker-reconciliation attachment mode; caller
// materialization, commit markers, and file-catalog paths cannot leak into
// this environment.
type DuckLakePhysicalMarkerResolverFactory struct {
	Config Config
}

var _ PhysicalMarkerResolverFactory = DuckLakePhysicalMarkerResolverFactory{}

// OpenReadOnly opens one read-only resolver environment. Resolver calls use a
// borrowed connector to create a fresh physical session, so no connection-
// local DuckLake state is reused across resolutions.
func (f DuckLakePhysicalMarkerResolverFactory) OpenReadOnly(ctx context.Context) (PhysicalMarkerResolver, error) {
	if f.Config.PostgresCatalog == nil {
		return nil, errors.New("DuckLake physical marker resolver requires a PostgreSQL catalog configuration")
	}
	config := f.Config
	postgres := *config.PostgresCatalog
	postgres.Mode = PostgresCatalogMarkerReadOnly
	postgres.DataPath = ""
	postgres.SnapshotVersion = 0
	config.PostgresCatalog = &postgres
	config.CatalogPath = ""
	config.CommitMarker = nil
	config.ReadOnly = true
	// A resolver never needs a pool of query sessions. Limiting the backing
	// environment to one warm session also bounds startup resource use; each
	// actual resolution is still opened through a fresh borrowed connector.
	config.MaxConnections = 1
	environment, err := Open(ctx, config)
	if err != nil {
		return nil, err
	}
	return &duckLakePhysicalMarkerResolver{environment: environment}, nil
}

// Open is a convenience alias for callers that use the same verb across
// native physical factories. It has the exact OpenReadOnly semantics.
type duckLakePhysicalMarkerResolver struct {
	environment *Environment
}

var _ PhysicalMarkerResolver = (*duckLakePhysicalMarkerResolver)(nil)

func (r *duckLakePhysicalMarkerResolver) ResolveCommittedMarker(ctx context.Context, marker CommitMarker) (PhysicalMarkerResolution, error) {
	if r == nil || r.environment == nil {
		return PhysicalMarkerResolution{}, errors.New("DuckLake physical marker resolver is not initialized")
	}
	return r.environment.ResolveCommittedMarkerFresh(ctx, marker)
}

func (r *duckLakePhysicalMarkerResolver) Close() error {
	if r == nil || r.environment == nil {
		return nil
	}
	return r.environment.Close()
}

// ResolveCommittedMarkerFresh resolves an exact marker through a newly opened
// read-only DuckDB session. The method is intentionally unavailable to
// writable or non-PostgreSQL environments: recovery must never infer an
// outcome from a writer session or a local catalog file.
func (e *Environment) ResolveCommittedMarkerFresh(ctx context.Context, marker CommitMarker) (PhysicalMarkerResolution, error) {
	if e == nil || e.db == nil {
		return PhysicalMarkerResolution{}, fmt.Errorf("ducklake environment is not initialized")
	}
	if e.closed.Load() {
		return PhysicalMarkerResolution{}, ErrEnvironmentClosed
	}
	if !e.postgresCatalog {
		return PhysicalMarkerResolution{}, errors.New("PostgreSQL DuckLake environment is required for marker reconciliation")
	}
	if !e.readOnly {
		return PhysicalMarkerResolution{}, ErrReadOnlyEnvironment
	}
	if e.physicalPoolID == "" || marker.PhysicalPoolID != e.physicalPoolID {
		return PhysicalMarkerResolution{}, errors.New("DuckLake marker physical pool differs from the recovery environment")
	}
	if e.connector == nil {
		return PhysicalMarkerResolution{}, errors.New("DuckLake marker reconciliation requires a fresh-session connector")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Do not borrow e.db: database/sql may return a previously used physical
	// session whose connection-local last_committed_snapshot is stale. The
	// borrowed connector runs the read-only ATTACH initializer again and gives
	// every call an independent session.
	lookupDB := sql.OpenDB(borrowedConnector{inner: e.connector})
	lookupDB.SetMaxOpenConns(1)
	lookupDB.SetMaxIdleConns(0)
	defer lookupDB.Close()
	conn, err := lookupDB.Conn(ctx)
	if err != nil {
		return PhysicalMarkerResolution{}, err
	}
	defer conn.Close()
	return ResolveCommittedMarker(ctx, conn, marker)
}
