package analyticsruntime

import (
	"context"
	"errors"
	"testing"

	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refresh "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestRefreshMaterializerResolvesCandidateManagedDataAndReleasesLifetime(t *testing.T) {
	lifetime := &recordingManagedDataLifetime{}
	resolver := &recordingManagedDataResolver{resolution: runtimehost.ManagedDataResolution{
		Roots: map[string]string{}, Lifetime: lifetime,
	}}
	materializer := RefreshMaterializer{ManagedData: resolver}
	_, _ = materializer.Materialize(t.Context(), refresh.MaterializeInput{
		Definition: &artifact.Definition{Models: map[string]*semanticmodel.Model{}},
		Candidate:  servingstate.State{ID: "candidate-sales", ProjectID: projectgraph.ResourceID("sales"), Environment: "dev"}, Environment: "dev",
	})
	if resolver.identity.GenerationID != "candidate-sales" || resolver.identity.ProjectID != "sales" {
		t.Fatalf("resolved serving identity = %+v", resolver.identity)
	}
	if !lifetime.released {
		t.Fatal("managed data lifetime was not released after materialization")
	}
}

func TestRefreshMaterializerUsesActiveReleaseConnectionEvidence(t *testing.T) {
	executor := &recordingExecutor{}
	materializer := RefreshMaterializer{Executor: executor}

	_, err := materializer.Materialize(t.Context(), refresh.MaterializeInput{
		Definition:  &artifact.Definition{Models: map[string]*semanticmodel.Model{}},
		Active:      servingstate.State{ID: "active-sales", ProjectID: projectgraph.ResourceID("sales"), Environment: "prod"},
		Candidate:   servingstate.State{ID: "refresh-sales", ProjectID: projectgraph.ResourceID("sales"), Environment: "prod"},
		Environment: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.request.Identity.GenerationID != "refresh-sales" {
		t.Fatalf("materialization serving generation = %q", executor.request.Identity.GenerationID)
	}
	if executor.request.ConnectionEvidenceServingStateID != "active-sales" {
		t.Fatalf("connection evidence serving state = %q", executor.request.ConnectionEvidenceServingStateID)
	}
	if executor.request.Identity.ProjectID != "sales" || executor.request.Identity.GenerationID != "refresh-sales" {
		t.Fatalf("materialization identity = %+v", executor.request.Identity)
	}
}

type recordingExecutor struct {
	request analyticsmaterialization.Request
}

func (e *recordingExecutor) Materialize(_ context.Context, request analyticsmaterialization.Request) (int64, error) {
	e.request = request
	return 42, nil
}

type recordingManagedDataResolver struct {
	resolution runtimehost.ManagedDataResolution
	identity   projectgraph.ServingIdentity
}

func (r *recordingManagedDataResolver) ResolveManagedDataForIdentity(_ context.Context, identity projectgraph.ServingIdentity) (runtimehost.ManagedDataResolution, error) {
	r.identity = identity
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
