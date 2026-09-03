// Package module owns serving-state composition. PostgreSQL is the immutable
// production authority.
package module

import (
	"context"
	"errors"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

// NativePersistence is the immutable production reader/lease seam. Admission
// is exposed by the concrete PostgreSQL package with caller-owned pgx Tx.
type NativePersistence interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
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

// Persistence is the typed serving-state storage selection passed into Build.
// Callers choose NewPostgresPersistence for production; Build never infers an
// adapter from a raw database handle.
type Persistence struct {
	native NativePersistence
}

// NewPostgresPersistence wraps the configured immutable production authority.
func NewPostgresPersistence(native NativePersistence) (Persistence, error) {
	if native == nil {
		return Persistence{}, errors.New("PostgreSQL serving-state persistence is required")
	}
	if !native.Configured() {
		return Persistence{}, errors.New("PostgreSQL serving-state persistence is not configured")
	}
	return Persistence{native: native}, nil
}

func (p Persistence) isPostgres() bool {
	return p.native != nil
}

func (p Persistence) validate() error {
	if !p.isPostgres() || !p.native.Configured() {
		return errors.New("PostgreSQL serving-state persistence is not configured")
	}
	return nil
}

type Module struct {
	native NativePersistence
}
type Config struct {
	Persistence *Persistence
	Production  bool
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Persistence == nil {
		return nil, errors.New("serving-state persistence is required")
	}
	if err := config.Persistence.validate(); err != nil {
		return nil, err
	}
	if !config.Production || !config.Persistence.isPostgres() {
		return nil, errors.New("native PostgreSQL persistence requires production serving-state mode")
	}
	return &Module{native: config.Persistence.native}, nil
}

func (m *Module) ByID(c context.Context, id servingstate.ID) (servingstate.State, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return servingstate.State{}, err
	}
	return native.ByID(c, id)
}
func (m *Module) RecordDuckLakeSnapshot(c context.Context, id servingstate.ID, s int64) error {
	native, err := m.nativeOrErr()
	if err != nil {
		return err
	}
	return native.RecordDuckLakeSnapshot(c, id, s)
}
func (m *Module) CreateQuerySnapshotLease(c context.Context, i servingstate.SnapshotLeaseInput) (string, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return "", err
	}
	return native.CreateQuerySnapshotLease(c, i)
}
func (m *Module) ReleaseQuerySnapshotLease(c context.Context, id string) error {
	native, err := m.nativeOrErr()
	if err != nil {
		return err
	}
	return native.ReleaseQuerySnapshotLease(c, id)
}
func (m *Module) ExtendQuerySnapshotLease(c context.Context, id string, t time.Time) error {
	native, err := m.nativeOrErr()
	if err != nil {
		return err
	}
	return native.ExtendQuerySnapshotLease(c, id, t)
}
func (m *Module) ActiveArtifact(c context.Context, p projectgraph.ResourceID, e servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return servingstate.State{}, servingstate.Artifact{}, err
	}
	return native.ActiveArtifact(c, p, e)
}
func (m *Module) ListActiveScopes(c context.Context) ([]servingstate.ActiveScope, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return nil, err
	}
	return native.ListActiveScopes(c)
}
func (m *Module) ArtifactByServingState(c context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return servingstate.Artifact{}, err
	}
	return native.ArtifactByServingState(c, id)
}
func (m *Module) ActiveServingStateGraph(c context.Context, p projectgraph.ResourceID, e string) (servingstate.AssetGraph, bool, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	return native.ActiveServingStateGraph(c, p, e)
}
func (m *Module) ServingStateGraph(c context.Context, p projectgraph.ResourceID, e string, id servingstate.ID) (servingstate.AssetGraph, bool, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	return native.ServingStateGraph(c, p, e, id)
}
func (m *Module) AssetVersions(c context.Context, p projectgraph.ResourceID, e string, a projectgraph.ResourceID) ([]servingstate.AssetVersion, error) {
	native, err := m.nativeOrErr()
	if err != nil {
		return nil, err
	}
	return native.AssetVersions(c, p, e, a)
}

func (m *Module) nativeOrErr() (NativePersistence, error) {
	if m == nil || m.native == nil {
		return nil, errors.New("serving-state persistence is unavailable")
	}
	return m.native, nil
}
