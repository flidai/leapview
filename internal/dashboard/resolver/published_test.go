package resolver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPublishedCompilationResolverResolvesExactIdentity(t *testing.T) {
	identity := mustIdentity(t, "project_1", "production", "generation_1")
	compiled := testCompiledRevision(t, "project_1", "dashboard_1", "model_1", identity.GenerationID)
	resolver, err := NewPublishedCompilationResolver(identity, fakeCompilationReader{compiled: compiled}, fakeSemanticModels{models: map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": {Name: "display-name"}}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(projectgraph.ResourceID("dashboard_1"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SemanticModelID != "model_1" || resolved.Source.Identity != identity || resolved.Source.Kind != SourceInstance {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestPublishedCompilationResolverRejectsStaleAndMissing(t *testing.T) {
	identity := mustIdentity(t, "project_1", "production", "generation_2")
	stale := testCompiledRevision(t, "project_1", "dashboard_1", "model_1", "generation_1")
	resolver, err := NewPublishedCompilationResolver(identity, fakeCompilationReader{compiled: stale}, fakeSemanticModels{models: map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": {}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("dashboard_1"); !errors.Is(err, ErrStaleSemanticState) {
		t.Fatalf("error = %v, want stale state", err)
	}
	missing, err := NewPublishedCompilationResolver(identity, fakeCompilationReader{err: authoring.ErrNotFound}, fakeSemanticModels{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Resolve(projectgraph.ResourceID("dashboard_1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestPublishedCompilationResolverRejectsSemanticModelEvidenceMismatch(t *testing.T) {
	identity := mustIdentity(t, "project_1", "production", "generation_1")
	compiled := testCompiledRevision(t, "project_1", "dashboard_1", "model_1", identity.GenerationID)
	compiled.SemanticModelID = "model_other"
	resolver, err := NewPublishedCompilationResolver(identity, fakeCompilationReader{compiled: compiled}, fakeSemanticModels{models: map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": {}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("dashboard_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want rejected stale semantic model evidence", err)
	}
}

func TestPublishedCompilationResolverRequiresCanonicalResourceID(t *testing.T) {
	identity := mustIdentity(t, "project_1", "production", "generation_1")
	resolver, err := NewPublishedCompilationResolver(identity, fakeCompilationReader{}, fakeSemanticModels{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(projectgraph.ResourceID(" dashboard_1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
}

type fakeCompilationReader struct {
	compiled authoring.CompiledRevision
	err      error
}

func (r fakeCompilationReader) GetPublishedCompilation(context.Context, projectgraph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	if r.err != nil {
		return authoring.CompiledRevision{}, r.err
	}
	return r.compiled, nil
}

type fakeSemanticModels struct {
	models map[projectgraph.ResourceID]*semanticmodel.Model
}

func (m fakeSemanticModels) SemanticModelByID(id projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	model, ok := m.models[id]
	return model, ok
}

func mustIdentity(t *testing.T, project, environment, generation string) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(project), environment, generation)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func testCompiledRevision(t *testing.T, projectID, dashboardID, modelID, stateID string) authoring.CompiledRevision {
	t.Helper()
	definition, err := dashboarddefinition.New(dashboardID, "Sales", "", modelID, []dashboard.Page{{ID: "overview", Title: "Overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision(projectgraph.ResourceID(projectID), authoring.DashboardID(dashboardID), authoring.RevisionToken{
		RevisionID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64),
	}, definition, mustIdentity(t, projectID, "production", stateID), time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
