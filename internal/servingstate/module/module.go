// Package module owns construction of serving-state persistence and exposes
// capability contracts without leaking its SQLite adapter.
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

type Config struct{ Database *sql.DB }

type Module struct {
	states *servingstatesqlite.Repository
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Database == nil {
		return nil, errors.New("serving-state database is required")
	}
	return &Module{states: servingstatesqlite.NewRepository(config.Database)}, nil
}

func (m *Module) Create(ctx context.Context, input servingstate.CreateInput) (servingstate.State, error) {
	return m.states.Create(ctx, input)
}
func (m *Module) ByID(ctx context.Context, id servingstate.ID) (servingstate.State, error) {
	return m.states.ByID(ctx, id)
}
func (m *Module) MarkFailed(ctx context.Context, id servingstate.ID, cause error) error {
	return m.states.MarkFailed(ctx, id, cause)
}
func (m *Module) RecordDuckLakeSnapshot(ctx context.Context, id servingstate.ID, snapshotID int64) error {
	return m.states.RecordDuckLakeSnapshot(ctx, id, snapshotID)
}
func (m *Module) ReferencedDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return m.states.ReferencedDuckLakeSnapshots(ctx, environment)
}
func (m *Module) ActiveDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return m.states.ActiveDuckLakeSnapshots(ctx, environment)
}
func (m *Module) LeasedDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return m.states.LeasedDuckLakeSnapshots(ctx, environment)
}
func (m *Module) ForeignEnvironmentDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return m.states.ForeignEnvironmentDuckLakeSnapshots(ctx, environment)
}
func (m *Module) CreateQuerySnapshotLease(ctx context.Context, input servingstate.SnapshotLeaseInput) (string, error) {
	return m.states.CreateQuerySnapshotLease(ctx, input)
}
func (m *Module) ReleaseQuerySnapshotLease(ctx context.Context, id string) error {
	return m.states.ReleaseQuerySnapshotLease(ctx, id)
}
func (m *Module) ExtendQuerySnapshotLease(ctx context.Context, id string, expiresAt time.Time) error {
	return m.states.ExtendQuerySnapshotLease(ctx, id, expiresAt)
}
func (m *Module) ReleaseExpiredQuerySnapshotLeases(ctx context.Context, environment string) error {
	return m.states.ReleaseExpiredQuerySnapshotLeases(ctx, environment)
}
func (m *Module) ExpireInactiveServingStates(ctx context.Context, environment string) error {
	return m.states.ExpireInactiveServingStates(ctx, environment)
}
func (m *Module) ScheduleExpiredServingStateDeletion(ctx context.Context, environment string) error {
	return m.states.ScheduleExpiredServingStateDeletion(ctx, environment)
}
func (m *Module) MarkDeleteScheduledServingStatesDeleted(ctx context.Context, environment string) error {
	return m.states.MarkDeleteScheduledServingStatesDeleted(ctx, environment)
}
func (m *Module) ReconcileRetention(ctx context.Context, environment string, now time.Time) error {
	return m.states.ReconcileRetention(ctx, environment, now)
}
func (m *Module) SaveValidated(ctx context.Context, id servingstate.ID, validation servingstate.Validation, artifact servingstate.Artifact) (servingstate.State, error) {
	return m.states.SaveValidated(ctx, id, validation, artifact)
}
func (m *Module) Activate(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment, id, expectedActiveID servingstate.ID) (servingstate.State, error) {
	return m.states.Activate(ctx, projectID, environment, id, expectedActiveID)
}
func (m *Module) ActiveArtifact(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return m.states.ActiveArtifact(ctx, projectID, environment)
}
func (m *Module) ListActiveScopes(ctx context.Context) ([]servingstate.ActiveScope, error) {
	return m.states.ListActiveScopes(ctx)
}
func (m *Module) ArtifactByServingState(ctx context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	return m.states.ArtifactByServingState(ctx, id)
}
