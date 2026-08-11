package analyticsruntime

import (
	"context"
	"errors"
	"testing"

	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refresh "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestWorkspaceRefreshMaterializerResolvesCandidateManagedDataAndReleasesLifetime(t *testing.T) {
	lifetime := &recordingManagedDataLifetime{}
	resolver := &recordingManagedDataResolver{resolution: runtimehost.ManagedDataResolution{
		Roots: map[string]string{}, Lifetime: lifetime,
	}}
	materializer := WorkspaceRefreshMaterializer{ManagedData: resolver}
	_, _ = materializer.Materialize(t.Context(), refresh.MaterializeInput{
		Definition: &artifact.Definition{Models: map[string]*semanticmodel.Model{}},
		Candidate:  servingstate.State{ID: "candidate-sales", WorkspaceID: "sales", Environment: "dev"}, Environment: "dev",
	})
	if resolver.servingStateID != "candidate-sales" {
		t.Fatalf("resolved serving state = %q", resolver.servingStateID)
	}
	if !lifetime.released {
		t.Fatal("managed data lifetime was not released after materialization")
	}
}

func TestWorkspaceRefreshMaterializerUsesActiveReleaseConnectionEvidence(t *testing.T) {
	executor := &recordingWorkspaceExecutor{}
	materializer := WorkspaceRefreshMaterializer{Executor: executor}

	_, err := materializer.Materialize(t.Context(), refresh.MaterializeInput{
		Definition:  &artifact.Definition{Models: map[string]*semanticmodel.Model{}},
		Active:      servingstate.State{ID: "active-sales", WorkspaceID: "sales", Environment: "prod"},
		Candidate:   servingstate.State{ID: "refresh-sales", WorkspaceID: "sales", Environment: "prod"},
		Environment: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.request.ServingStateID != "refresh-sales" {
		t.Fatalf("materialization serving state = %q", executor.request.ServingStateID)
	}
	if executor.request.ConnectionEvidenceServingStateID != "active-sales" {
		t.Fatalf("connection evidence serving state = %q", executor.request.ConnectionEvidenceServingStateID)
	}
}

type recordingWorkspaceExecutor struct {
	request analyticsmaterialization.WorkspaceRequest
}

func (e *recordingWorkspaceExecutor) MaterializeWorkspace(_ context.Context, request analyticsmaterialization.WorkspaceRequest) (int64, error) {
	e.request = request
	return 42, nil
}

type recordingManagedDataResolver struct {
	resolution     runtimehost.ManagedDataResolution
	servingStateID servingstate.ID
}

func (r *recordingManagedDataResolver) ResolveManagedData(_ context.Context, id servingstate.ID) (runtimehost.ManagedDataResolution, error) {
	r.servingStateID = id
	return r.resolution, nil
}

type recordingManagedDataLifetime struct{ released bool }

func (l *recordingManagedDataLifetime) Release() error {
	if l.released {
		return errors.New("released twice")
	}
	l.released = true
	return nil
}
