package authoring

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
)

type revalidationTestStore struct {
	mu        sync.Mutex
	lifecycle DashboardLifecycle
	revision  Revision
	commits   []RevalidationCommit
	failures  []RevalidationFailureInput
	active    graph.ServingIdentity
	commitErr error
}

func (s *revalidationTestStore) List(context.Context, graph.ResourceID) ([]DashboardLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []DashboardLifecycle{s.lifecycle}, nil
}
func (s *revalidationTestStore) GetRevision(context.Context, graph.ResourceID, DashboardID, RevisionID) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, nil
}
func (s *revalidationTestStore) CommitRevalidation(_ context.Context, input RevalidationCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	if input.Generation.Identity != s.active {
		return ErrGenerationSuperseded
	}
	s.commits = append(s.commits, input)
	s.lifecycle.Published.Compilation = input.Compilation.Token()
	s.lifecycle.Revalidation = nil
	return nil
}
func (s *revalidationTestStore) RecordRevalidationFailure(_ context.Context, input RevalidationFailureInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, input)
	s.lifecycle.Revalidation = &input.Failure
	return nil
}

type revalidationTestCompiler struct{ err error }

func (c revalidationTestCompiler) Compile(_ context.Context, generation RevalidationGeneration, revision Revision) (CompiledRevision, error) {
	if c.err != nil {
		return CompiledRevision{}, c.err
	}
	return NewCompiledRevision(generation.Identity.ProjectID, revision.DashboardID, revision.Token(), dashboarddefinition.Definition{ID: revision.Document.Metadata.ID, Title: *revision.Document.Metadata.DisplayName, SemanticModel: revision.Document.Spec.SemanticModel}, generation.Identity, time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC))
}

func revalidationTestFixture(t *testing.T) (RevalidationGeneration, DashboardLifecycle, Revision, CompiledRevision) {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{{ID: "project", Kind: graph.KindProject, Name: "project"}, {ID: "dashboard", Kind: graph.KindDashboard, Name: "dashboard", Provenance: graph.Provenance{Origin: "instance"}}, {ID: "semantic", Kind: graph.KindSemanticModel, Name: "semantic"}, {ID: "model", Kind: graph.KindModel, Name: "model"}}, []graph.Edge{{From: "dashboard", To: "semantic"}, {From: "semantic", To: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := graph.NewServingIdentity("project", "prod", "generation-2")
	auth, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := revalidationDocument()
	provenance := Provenance{Origin: OriginUI, ActorID: "actor"}
	revision, err := NewRevision("revision-1", "dashboard", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity, _ := graph.NewServingIdentity("project", "prod", "generation-1")
	compiled, err := NewCompiledRevision("project", "dashboard", revision.Token(), dashboarddefinition.Definition{ID: "dashboard", Title: "Dashboard", SemanticModel: "semantic"}, oldIdentity, time.Date(2026, 8, 18, 0, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := DashboardLifecycle{ProjectID: "project", ID: "dashboard", OwnerPrincipalID: "owner", Slug: "dashboard", Title: "Dashboard", SemanticModel: "semantic", Visibility: VisibilityOrganization, Status: LifecycleStatusPublished, Draft: &Draft{ID: "draft", DashboardID: "dashboard", Revision: revision.Token(), Provenance: provenance}, Published: &Published{Revision: revision.Token(), Compilation: compiled.Token(), PublishedAt: compiled.CompiledAt, Provenance: provenance}}
	return RevalidationGeneration{Identity: identity, Graph: project, Authorization: auth, ChangedIDs: []graph.ResourceID{"model"}}, lifecycle, revision, compiled
}
func revalidationDocument() document.DashboardDocument {
	return document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "dashboard", Name: "dashboard", DisplayName: revalidationStringPtr("Dashboard")}, Spec: document.DashboardSpec{SemanticModel: "semantic", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}
}
func revalidationStringPtr(value string) *string { return &value }

func TestGenerationRevalidatorSelectsDependenciesAndAdvancesEvidence(t *testing.T) {
	generation, lifecycle, revision, prior := revalidationTestFixture(t)
	store := &revalidationTestStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	revalidator, err := NewGenerationRevalidator(store, revalidationTestCompiler{}, func() time.Time { return time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	results, err := revalidator.GenerationActivated(t.Context(), generation)
	if err != nil || len(results) != 1 || results[0].Status != RevalidationSucceeded || len(store.commits) != 1 {
		t.Fatalf("results=%#v commits=%d err=%v", results, len(store.commits), err)
	}
	if store.commits[0].Compilation.SemanticIdentity != generation.Identity || store.commits[0].PriorCompilation.SemanticIdentity != prior.SemanticIdentity || store.lifecycle.Published.Compilation.SemanticIdentity != generation.Identity {
		t.Fatalf("evidence=%#v lifecycle=%#v", store.commits[0], store.lifecycle)
	}
}

func TestGenerationRevalidatorFailurePreservesPublishedEvidenceAndSupersededCAS(t *testing.T) {
	generation, lifecycle, revision, _ := revalidationTestFixture(t)
	store := &revalidationTestStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	prior := lifecycle.Published.Compilation
	revalidator, _ := NewGenerationRevalidator(store, revalidationTestCompiler{err: errors.New("missing semantic column")}, nil)
	results, err := revalidator.GenerationActivated(t.Context(), generation)
	if err != nil || len(results) != 1 || results[0].Status != RevalidationFailed || len(store.commits) != 0 || store.lifecycle.Published.Compilation != prior || store.lifecycle.Revalidation == nil {
		t.Fatalf("failure results=%#v err=%v lifecycle=%#v", results, err, store.lifecycle)
	}
	if store.lifecycle.Revalidation.Code != "REVALIDATION_FAILED" || !strings.Contains(store.lifecycle.Revalidation.Message, "missing") {
		t.Fatalf("failure=%#v", store.lifecycle.Revalidation)
	}
	store = &revalidationTestStore{lifecycle: lifecycle, revision: revision, active: generation.Identity, commitErr: ErrGenerationSuperseded}
	revalidator, _ = NewGenerationRevalidator(store, revalidationTestCompiler{}, nil)
	results, err = revalidator.GenerationActivated(t.Context(), generation)
	if err != nil || results[0].Status != RevalidationSuperseded {
		t.Fatalf("superseded=%#v err=%v", results, err)
	}
}

func TestGenerationRevalidatorSkipsUnrelatedAndRejectsAuthorizationMismatch(t *testing.T) {
	generation, lifecycle, revision, _ := revalidationTestFixture(t)
	generation.ChangedIDs = []graph.ResourceID{"connection-unrelated"}
	store := &revalidationTestStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	revalidator, _ := NewGenerationRevalidator(store, revalidationTestCompiler{}, nil)
	results, err := revalidator.GenerationActivated(t.Context(), generation)
	if err != nil || len(results) != 0 || len(store.commits) != 0 || len(store.failures) != 0 || !reflect.DeepEqual(store.lifecycle.Published.Compilation.SemanticIdentity, lifecycle.Published.Compilation.SemanticIdentity) {
		t.Fatalf("unrelated=%#v err=%v", results, err)
	}
	generation, _, _, _ = revalidationTestFixture(t)
	badIdentity, _ := graph.NewServingIdentity("project", "prod", "other-generation")
	generation.Authorization, _ = accesssnapshot.NewAuthorizationSnapshot(badIdentity, generation.Graph, nil, nil)
	if _, err := revalidator.GenerationActivated(t.Context(), generation); err == nil {
		t.Fatal("authorization identity mismatch accepted")
	}
}

func TestRevalidationAttemptIDCanonicalValidation(t *testing.T) {
	id, err := NewRevalidationAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRevalidationAttemptID(id); err != nil {
		t.Fatalf("generated ID %q rejected: %v", id, err)
	}
	for _, invalid := range []string{"", "attempt-short", "attempt_0000000000000000000000000000000G", "attempt_00000000000000000000000000000001 "} {
		if err := ValidateRevalidationAttemptID(invalid); err == nil {
			t.Fatalf("invalid ID %q accepted", invalid)
		}
	}
}
