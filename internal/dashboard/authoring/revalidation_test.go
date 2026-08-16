package authoring

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type revalidationStore struct {
	mu        sync.Mutex
	lifecycle DashboardLifecycle
	revision  Revision
	commits   []RevalidationCommit
	failures  []RevalidationFailureInput
	active    projectgraph.ServingIdentity
	commitErr error
}

func (s *revalidationStore) List(context.Context, projectgraph.ResourceID) ([]DashboardLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []DashboardLifecycle{s.lifecycle}, nil
}
func (s *revalidationStore) GetRevision(context.Context, projectgraph.ResourceID, DashboardID, RevisionID) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, nil
}
func (s *revalidationStore) CommitRevalidation(_ context.Context, input RevalidationCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	if s.active != input.Generation.Identity {
		return ErrGenerationSuperseded
	}
	s.commits = append(s.commits, input)
	s.lifecycle.Published.Compilation = input.Compilation.Token()
	s.lifecycle.Revalidation = nil
	return nil
}
func (s *revalidationStore) RecordRevalidationFailure(_ context.Context, input RevalidationFailureInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, input)
	s.lifecycle.Revalidation = &input.Failure
	return nil
}

type revalidationCompiler struct {
	err error
}

func (c revalidationCompiler) Compile(_ context.Context, generation RevalidationGeneration, revision Revision) (CompiledRevision, error) {
	if c.err != nil {
		return CompiledRevision{}, c.err
	}
	return NewCompiledRevision(generation.Identity.ProjectID, revision.DashboardID, revision.Token(), dashboarddefinition.Definition{ID: revision.DashboardID.String(), Title: revision.Document.Title, SemanticModel: revision.Document.SemanticModel.String()}, generation.Identity, time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC))
}

func revalidationFixture(t *testing.T) (RevalidationGeneration, DashboardLifecycle, Revision, CompiledRevision) {
	t.Helper()
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project", Kind: projectgraph.KindProject, Name: "project"},
		{ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"},
		{ID: "semantic", Kind: projectgraph.KindSemanticModel, Name: "semantic"},
		{ID: "model", Kind: projectgraph.KindModel, Name: "model"},
	}, []projectgraph.Edge{{From: "dashboard", To: "semantic"}, {From: "semantic", To: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project", "prod", "generation-2")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := Dashboard{ID: "dashboard", Title: "Dashboard", SemanticModel: "semantic", Pages: []dashboardmodel.Page{{ID: "overview"}}}
	revision, err := NewRevision("revision-1", "dashboard", 1, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), revisionDashboard(doc), Provenance{Origin: OriginUI, ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity, _ := projectgraph.NewServingIdentity("project", "prod", "generation-1")
	compiled, err := NewCompiledRevision("project", "dashboard", revision.Token(), dashboarddefinition.Definition{ID: "dashboard", Title: doc.Title, SemanticModel: "semantic"}, oldIdentity, time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := DashboardLifecycle{ProjectID: "project", ID: "dashboard", OwnerPrincipalID: "owner", Slug: "dashboard", Title: doc.Title, SemanticModel: "semantic", Visibility: VisibilityOrganization, Status: LifecycleStatusPublished, Draft: &Draft{ID: "draft", DashboardID: "dashboard", Revision: revision.Token(), Provenance: revision.Provenance}, Published: &Published{Revision: revision.Token(), Compilation: compiled.Token(), PublishedAt: time.Date(2026, 8, 15, 1, 0, 0, 0, time.UTC), Provenance: revision.Provenance}}
	return RevalidationGeneration{Identity: identity, Graph: project, Authorization: auth, ChangedIDs: []projectgraph.ResourceID{"model"}}, lifecycle, revision, compiled
}

func revisionDashboard(doc Dashboard) Dashboard {
	if doc.Visuals == nil {
		doc.Visuals = map[string]AuthoringVisualization{}
	}
	if doc.Pages == nil {
		doc.Pages = []dashboardmodel.Page{{ID: "overview"}}
	}
	return doc
}

func TestGenerationRevalidatorSelectsByDependencyAndAdvancesEvidence(t *testing.T) {
	generation, lifecycle, revision, old := revalidationFixture(t)
	store := &revalidationStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	revalidator, err := NewGenerationRevalidator(store, revalidationCompiler{}, func() time.Time { return time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	results, err := revalidator.GenerationActivated(context.Background(), generation)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != RevalidationSucceeded || len(store.commits) != 1 {
		t.Fatalf("results=%#v commits=%d", results, len(store.commits))
	}
	if store.commits[0].Compilation.SemanticIdentity != generation.Identity || store.commits[0].PriorCompilation.SemanticIdentity != old.SemanticIdentity {
		t.Fatalf("compiled evidence=%#v", store.commits[0])
	}
	if store.lifecycle.Published.Compilation.SemanticIdentity != generation.Identity || store.lifecycle.Revalidation != nil {
		t.Fatalf("lifecycle after success=%#v", store.lifecycle)
	}
}

func TestGenerationRevalidatorFailurePreservesPublishedEvidence(t *testing.T) {
	generation, lifecycle, revision, _ := revalidationFixture(t)
	store := &revalidationStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	prior := lifecycle.Published.Compilation
	revalidator, _ := NewGenerationRevalidator(store, revalidationCompiler{err: errors.New("missing semantic column")}, func() time.Time { return time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC) })
	results, err := revalidator.GenerationActivated(context.Background(), generation)
	if err != nil || len(results) != 1 || results[0].Status != RevalidationFailed {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	if len(store.commits) != 0 || store.lifecycle.Published.Compilation != prior || store.lifecycle.Revalidation == nil {
		t.Fatalf("failure mutated published evidence: %#v", store.lifecycle)
	}
	if store.lifecycle.Revalidation.Code != "REVALIDATION_FAILED" || !strings.Contains(store.lifecycle.Revalidation.Message, "missing") {
		t.Fatalf("failure=%#v", store.lifecycle.Revalidation)
	}
}

func TestGenerationRevalidatorRejectsSupersededCommitAndAuthorizationMismatch(t *testing.T) {
	generation, lifecycle, revision, _ := revalidationFixture(t)
	store := &revalidationStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	store.commitErr = ErrGenerationSuperseded
	revalidator, _ := NewGenerationRevalidator(store, revalidationCompiler{}, nil)
	results, err := revalidator.GenerationActivated(context.Background(), generation)
	if err != nil || len(results) != 1 || results[0].Status != RevalidationSuperseded {
		t.Fatalf("superseded results=%#v err=%v", results, err)
	}
	bad := generation
	bad.Authorization, _ = accesssnapshot.NewAuthorizationSnapshot(mustRevalidationIdentity(t, "project", "prod", "other-generation"), generation.Graph, nil, nil)
	if _, err := revalidator.GenerationActivated(context.Background(), bad); err == nil {
		t.Fatal("authorization identity mismatch unexpectedly accepted")
	}
}

func mustRevalidationIdentity(t *testing.T, project, environment, generation string) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(project), environment, generation)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestGenerationRevalidatorDependencySelectionDoesNotTouchUnrelated(t *testing.T) {
	generation, lifecycle, revision, _ := revalidationFixture(t)
	generation.ChangedIDs = []projectgraph.ResourceID{"connection-unrelated"}
	store := &revalidationStore{lifecycle: lifecycle, revision: revision, active: generation.Identity}
	revalidator, _ := NewGenerationRevalidator(store, revalidationCompiler{}, nil)
	results, err := revalidator.GenerationActivated(context.Background(), generation)
	if err != nil || len(results) != 0 || len(store.commits) != 0 || len(store.failures) != 0 {
		t.Fatalf("unrelated results=%#v err=%v commits=%d failures=%d", results, err, len(store.commits), len(store.failures))
	}
	if !reflect.DeepEqual(store.lifecycle.Published.Compilation.SemanticIdentity, lifecycle.Published.Compilation.SemanticIdentity) {
		t.Fatal("unrelated dashboard evidence changed")
	}
}

func TestRevalidationAttemptIDCanonicalValidation(t *testing.T) {
	id, err := NewRevalidationAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRevalidationAttemptID(id); err != nil {
		t.Fatalf("generated attempt ID %q rejected: %v", id, err)
	}
	for _, invalid := range []string{"", "attempt-short", "attempt_0000000000000000000000000000000G", "attempt_00000000000000000000000000000001 "} {
		if err := ValidateRevalidationAttemptID(invalid); err == nil {
			t.Fatalf("ValidateRevalidationAttemptID(%q) unexpectedly succeeded", invalid)
		}
	}
}

func (s *revalidationStore) String() string { return fmt.Sprintf("%#v", s.lifecycle) }
