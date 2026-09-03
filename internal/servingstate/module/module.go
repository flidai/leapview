// Package module owns serving-state composition. PostgreSQL is the immutable
// production authority; SQLite is an explicitly selected development adapter.
package module

import (
	"context"
	"database/sql"
	"errors"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
)

// LegacyPersistence is the SQLite-only staging lifecycle.
type LegacyPersistence interface {
	Create(context.Context, servingstate.CreateInput) (servingstate.State, error)
	CreateWithID(context.Context, servingstate.ID, servingstate.CreateInput) (servingstate.State, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	MarkFailed(context.Context, servingstate.ID, error) error
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
	CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error)
	ReleaseQuerySnapshotLease(context.Context, string) error
	ExtendQuerySnapshotLease(context.Context, string, time.Time) error
	SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error)
	Activate(context.Context, projectgraph.ResourceID, servingstate.Environment, servingstate.ID, servingstate.ID) (servingstate.State, error)
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
	ServingStateGraph(context.Context, projectgraph.ResourceID, string, servingstate.ID) (servingstate.AssetGraph, bool, error)
	AssetVersions(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID) ([]servingstate.AssetVersion, error)
}

// NativePersistence is the immutable production reader/lease seam. Admission
// is exposed by the concrete PostgreSQL package with caller-owned pgx Tx.
type NativePersistence interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error)
	CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error)
	ReleaseQuerySnapshotLease(context.Context, string) error
	ExtendQuerySnapshotLease(context.Context, string, time.Time) error
	ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
	ServingStateGraph(context.Context, projectgraph.ResourceID, string, servingstate.ID) (servingstate.AssetGraph, bool, error)
	AssetVersions(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID) ([]servingstate.AssetVersion, error)
	NativePersistence()
	Configured() bool
}

type persistenceBackend uint8

const (
	backendSQLite persistenceBackend = iota + 1
	backendPostgres
)

// Persistence is the typed serving-state storage selection passed into Build.
// Callers choose NewSQLitePersistence for local/evaluation or
// NewPostgresPersistence for production; Build never infers an adapter from a
// raw database handle or a legacy interface field.
type Persistence struct {
	legacy  LegacyPersistence
	native  NativePersistence
	backend persistenceBackend
}

// NewSQLitePersistence constructs the explicit local/evaluation serving-state
// adapter.
func NewSQLitePersistence(database *sql.DB) (Persistence, error) {
	if database == nil {
		return Persistence{}, errors.New("SQLite serving-state database is required")
	}
	return Persistence{legacy: servingstatesqlite.NewRepository(database), backend: backendSQLite}, nil
}

// NewPostgresPersistence wraps the configured immutable production authority.
func NewPostgresPersistence(native NativePersistence) (Persistence, error) {
	if native == nil {
		return Persistence{}, errors.New("PostgreSQL serving-state persistence is required")
	}
	if !native.Configured() {
		return Persistence{}, errors.New("PostgreSQL serving-state persistence is not configured")
	}
	return Persistence{native: native, backend: backendPostgres}, nil
}

func (p Persistence) isPostgres() bool {
	return p.backend == backendPostgres && p.native != nil
}

func (p Persistence) isSQLite() bool {
	return p.backend == backendSQLite && p.legacy != nil
}

func (p Persistence) validate() error {
	switch {
	case p.isPostgres():
		if !p.native.Configured() {
			return errors.New("PostgreSQL serving-state persistence is not configured")
		}
		return nil
	case p.isSQLite():
		return nil
	default:
		return errors.New("serving-state persistence backend is not configured")
	}
}

type Module struct {
	legacy LegacyPersistence
	native NativePersistence
}
type Config struct {
	Persistence *Persistence
	Production  bool
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Persistence == nil {
		return nil, errors.New("serving-state persistence is required; choose an explicit PostgreSQL or SQLite persistence bundle")
	}
	if err := config.Persistence.validate(); err != nil {
		return nil, err
	}
	if config.Production {
		if !config.Persistence.isPostgres() {
			return nil, errors.New("production serving-state module requires native PostgreSQL persistence")
		}
		return &Module{native: config.Persistence.native}, nil
	}
	if config.Persistence.isPostgres() {
		return nil, errors.New("native PostgreSQL persistence requires production serving-state mode")
	}
	if !config.Persistence.isSQLite() {
		return nil, errors.New("development serving-state module requires explicit SQLite persistence")
	}
	return &Module{legacy: config.Persistence.legacy}, nil
}
func (m *Module) legacyOrErr() (LegacyPersistence, error) {
	if m == nil || m.legacy == nil {
		return nil, errors.New("legacy serving-state lifecycle is unavailable in production")
	}
	return m.legacy, nil
}
func (m *Module) Create(c context.Context, i servingstate.CreateInput) (servingstate.State, error) {
	r, e := m.legacyOrErr()
	if e != nil {
		return servingstate.State{}, e
	}
	return r.Create(c, i)
}
func (m *Module) CreateWithID(c context.Context, id servingstate.ID, i servingstate.CreateInput) (servingstate.State, error) {
	r, e := m.legacyOrErr()
	if e != nil {
		return servingstate.State{}, e
	}
	return r.CreateWithID(c, id, i)
}
func (m *Module) ByID(c context.Context, id servingstate.ID) (servingstate.State, error) {
	if m.native != nil {
		return m.native.ByID(c, id)
	}
	r, e := m.legacyOrErr()
	if e != nil {
		return servingstate.State{}, e
	}
	return r.ByID(c, id)
}
func (m *Module) MarkFailed(c context.Context, id servingstate.ID, x error) error {
	r, e := m.legacyOrErr()
	if e != nil {
		return e
	}
	return r.MarkFailed(c, id, x)
}
func (m *Module) RecordDuckLakeSnapshot(c context.Context, id servingstate.ID, s int64) error {
	r, e := m.legacyOrErr()
	if e != nil {
		return e
	}
	return r.RecordDuckLakeSnapshot(c, id, s)
}
func (m *Module) CreateQuerySnapshotLease(c context.Context, i servingstate.SnapshotLeaseInput) (string, error) {
	if m.native != nil {
		return m.native.CreateQuerySnapshotLease(c, i)
	}
	r, e := m.legacyOrErr()
	if e != nil {
		return "", e
	}
	return r.CreateQuerySnapshotLease(c, i)
}
func (m *Module) ReleaseQuerySnapshotLease(c context.Context, id string) error {
	if m.native != nil {
		return m.native.ReleaseQuerySnapshotLease(c, id)
	}
	r, e := m.legacyOrErr()
	if e != nil {
		return e
	}
	return r.ReleaseQuerySnapshotLease(c, id)
}
func (m *Module) ExtendQuerySnapshotLease(c context.Context, id string, t time.Time) error {
	if m.native != nil {
		return m.native.ExtendQuerySnapshotLease(c, id, t)
	}
	r, e := m.legacyOrErr()
	if e != nil {
		return e
	}
	return r.ExtendQuerySnapshotLease(c, id, t)
}
func (m *Module) SaveValidated(c context.Context, id servingstate.ID, v servingstate.Validation, a servingstate.Artifact) (servingstate.State, error) {
	r, e := m.legacyOrErr()
	if e != nil {
		return servingstate.State{}, e
	}
	return r.SaveValidated(c, id, v, a)
}
func (m *Module) Activate(c context.Context, p projectgraph.ResourceID, e servingstate.Environment, id, expected servingstate.ID) (servingstate.State, error) {
	r, x := m.legacyOrErr()
	if x != nil {
		return servingstate.State{}, x
	}
	return r.Activate(c, p, e, id, expected)
}
func (m *Module) ActiveArtifact(c context.Context, p projectgraph.ResourceID, e servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	if m.native != nil {
		return m.native.ActiveArtifact(c, p, e)
	}
	r, x := m.legacyOrErr()
	if x != nil {
		return servingstate.State{}, servingstate.Artifact{}, x
	}
	return r.ActiveArtifact(c, p, e)
}
func (m *Module) ListActiveScopes(c context.Context) ([]servingstate.ActiveScope, error) {
	if m.native != nil {
		return m.native.ListActiveScopes(c)
	}
	r, x := m.legacyOrErr()
	if x != nil {
		return nil, x
	}
	return r.ListActiveScopes(c)
}
func (m *Module) ArtifactByServingState(c context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	if m.native != nil {
		return m.native.ArtifactByServingState(c, id)
	}
	r, x := m.legacyOrErr()
	if x != nil {
		return servingstate.Artifact{}, x
	}
	return r.ArtifactByServingState(c, id)
}
func (m *Module) ActiveServingStateGraph(c context.Context, p projectgraph.ResourceID, e string) (servingstate.AssetGraph, bool, error) {
	if m.native != nil {
		return m.native.ActiveServingStateGraph(c, p, e)
	}
	r, x := m.legacyOrErr()
	if x != nil {
		return servingstate.AssetGraph{}, false, x
	}
	return r.ActiveServingStateGraph(c, p, e)
}
func (m *Module) ServingStateGraph(c context.Context, p projectgraph.ResourceID, e string, id servingstate.ID) (servingstate.AssetGraph, bool, error) {
	if m.native != nil {
		return m.native.ServingStateGraph(c, p, e, id)
	}
	r, x := m.legacyOrErr()
	if x != nil {
		return servingstate.AssetGraph{}, false, x
	}
	return r.ServingStateGraph(c, p, e, id)
}
func (m *Module) AssetVersions(c context.Context, p projectgraph.ResourceID, e string, a projectgraph.ResourceID) ([]servingstate.AssetVersion, error) {
	if m.native != nil {
		return m.native.AssetVersions(c, p, e, a)
	}
	r, x := m.legacyOrErr()
	if x != nil {
		return nil, x
	}
	return r.AssetVersions(c, p, e, a)
}
