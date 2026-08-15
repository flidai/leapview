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
)

func TestPublishedCompilationResolverResolvesExactCompilationAndEvidence(t *testing.T) {
	compiled := testCompiledRevision(t, "workspace", "sales", "state-1")
	reader := fakeCompilationReader{compiled: compiled}
	resolver := NewPublishedCompilationResolver("workspace", "state-1", reader, fakeSemanticModels{model: &semanticmodel.Model{Name: "sales_model"}})

	resolved, err := resolver.Resolve(" sales ")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Definition.ID != "sales" || resolved.Model == nil || resolved.Model.Name != "sales_model" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if resolved.Source.Kind != SourceWorkspace || resolved.Source.WorkspaceID != "workspace" || resolved.Source.SemanticServingStateID != "state-1" {
		t.Fatalf("source = %#v", resolved.Source)
	}
	if resolved.Source.AuthoredRevision.ID != string(compiled.AuthoredRevision.RevisionID) || resolved.Source.AuthoredRevision.Number != compiled.AuthoredRevision.Number || resolved.Source.AuthoredRevision.ContentHash != compiled.AuthoredRevision.ContentHash {
		t.Fatalf("authored evidence = %#v", resolved.Source.AuthoredRevision)
	}
}

func TestPublishedCompilationResolverMapsNotFound(t *testing.T) {
	resolver := NewPublishedCompilationResolver("workspace", "state-1", fakeCompilationReader{err: authoring.ErrNotFound}, fakeSemanticModels{})
	if _, err := resolver.Resolve("sales"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestPublishedCompilationResolverPreservesReaderErrors(t *testing.T) {
	wantErr := errors.New("repository unavailable")
	resolver := NewPublishedCompilationResolver("workspace", "state-1", fakeCompilationReader{err: wantErr}, fakeSemanticModels{})
	if _, err := resolver.Resolve("sales"); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestPublishedCompilationResolverRejectsStaleState(t *testing.T) {
	resolver := NewPublishedCompilationResolver("workspace", "state-2", fakeCompilationReader{compiled: testCompiledRevision(t, "workspace", "sales", "state-1")}, fakeSemanticModels{model: &semanticmodel.Model{Name: "sales_model"}})
	if _, err := resolver.Resolve("sales"); !errors.Is(err, ErrStaleSemanticState) {
		t.Fatalf("error = %v, want ErrStaleSemanticState", err)
	}
}

func TestPublishedCompilationResolverRejectsScopeAndMissingModel(t *testing.T) {
	t.Run("workspace mismatch", func(t *testing.T) {
		resolver := NewPublishedCompilationResolver("workspace", "state-1", fakeCompilationReader{compiled: testCompiledRevision(t, "other", "sales", "state-1")}, fakeSemanticModels{model: &semanticmodel.Model{Name: "sales_model"}})
		if _, err := resolver.Resolve("sales"); !errors.Is(err, ErrScopeMismatch) {
			t.Fatalf("error = %v, want ErrScopeMismatch", err)
		}
	})
	t.Run("missing model", func(t *testing.T) {
		resolver := NewPublishedCompilationResolver("workspace", "state-1", fakeCompilationReader{compiled: testCompiledRevision(t, "workspace", "sales", "state-1")}, fakeSemanticModels{})
		if _, err := resolver.Resolve("sales"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
	})
}

func TestPublishedCompilationResolverRejectsMalformedCompilation(t *testing.T) {
	compiled := testCompiledRevision(t, "workspace", "sales", "state-1")
	compiled.DefinitionHash = "sha256:" + strings.Repeat("0", 64)
	resolver := NewPublishedCompilationResolver("workspace", "state-1", fakeCompilationReader{compiled: compiled}, fakeSemanticModels{model: &semanticmodel.Model{Name: "sales_model"}})
	if _, err := resolver.Resolve("sales"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestAuthoredRevisionEvidenceValidate(t *testing.T) {
	evidence := AuthoredRevisionEvidence{ID: "rev-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []AuthoredRevisionEvidence{{ID: "rev-1", Number: 1}, {ID: "rev-1", ContentHash: evidence.ContentHash}, {ID: "rev-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("A", 64)}} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("evidence %#v unexpectedly valid", invalid)
		}
	}
}

type fakeCompilationReader struct {
	compiled authoring.CompiledRevision
	err      error
}

func (r fakeCompilationReader) GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error) {
	if r.err != nil {
		return authoring.CompiledRevision{}, r.err
	}
	return r.compiled, nil
}

type fakeSemanticModels struct{ model *semanticmodel.Model }

func (m fakeSemanticModels) SemanticModel(string) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil
}

func testCompiledRevision(t *testing.T, workspace, dashboardID, stateID string) authoring.CompiledRevision {
	t.Helper()
	definition, err := dashboarddefinition.New(dashboardID, "Sales", "", "sales_model", []dashboard.Page{{ID: "overview", Title: "Overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision(workspace, authoring.DashboardID(dashboardID), authoring.RevisionToken{
		RevisionID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64),
	}, definition, stateID, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
