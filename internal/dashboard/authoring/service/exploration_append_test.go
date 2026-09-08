package service_test

import (
	"context"
	"errors"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const appendRequestID = "01912f14-7b3c-7e31-8a74-6a6e8f9d4c20"

func TestAppendExplorationReplaysBeforePreparationAfterDraftAdvance(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-append", "draft-append", "revision-append", "revision-append-result")
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "actor", Title: "Append", Slug: "append", SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-append"})
	if err != nil {
		t.Fatal(err)
	}
	intent := appendIntent(created)
	prepares := 0
	releases := 0
	first, err := svc.AppendExploration(t.Context(), intent, func(_ context.Context, value service.AppendExplorationIntent, _ authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		prepares++
		prepared := preparedAppend(value)
		prepared.Release = func() { releases++ }
		return prepared, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.Number != 2 || prepares != 1 || releases != 1 {
		t.Fatalf("first append = %#v, prepares=%d releases=%d", first, prepares, releases)
	}
	// The current draft is now advanced. Replay must use the durable command
	// result before attempting stale-draft or runtime admission.
	replay, err := svc.AppendExploration(t.Context(), intent, func(context.Context, service.AppendExplorationIntent, authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		prepares++
		return service.AppendExplorationPreparation{}, errors.New("runtime unavailable")
	})
	if err != nil || replay.Revision != first.Revision || prepares != 1 || releases != 1 {
		t.Fatalf("replay = %#v, err=%v, prepares=%d releases=%d", replay, err, prepares, releases)
	}
	if repository.lookupCommandCalls != 2 {
		t.Fatalf("durable command lookups = %d, want 2", repository.lookupCommandCalls)
	}
}

func TestAppendExplorationRejectsChangedBodyWithSameRequestID(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-append", "draft-append", "revision-append", "revision-append-result")
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "actor", Title: "Append", Slug: "append", SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-append"})
	if err != nil {
		t.Fatal(err)
	}
	intent := appendIntent(created)
	if _, err := svc.AppendExploration(t.Context(), intent, func(_ context.Context, value service.AppendExplorationIntent, _ authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		return preparedAppend(value), nil
	}); err != nil {
		t.Fatal(err)
	}
	// The target and page remain unchanged: the nested canonical spec is part
	// of the command fingerprint, so a reused request ID cannot alter it.
	intent.Spec.Metrics[0].Field = "cost"
	if _, err := svc.AppendExploration(t.Context(), intent, func(context.Context, service.AppendExplorationIntent, authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		t.Fatal("changed body reached preparation")
		return service.AppendExplorationPreparation{}, nil
	}); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed body error = %v, want command reuse", err)
	}
	// A placement change is equally part of the canonical input. It must not
	// reuse the first append command merely because the page and spec match.
	intent.Spec.Metrics[0].Field = "revenue"
	intent.PlacementChoice = "full"
	if _, err := svc.AppendExploration(t.Context(), intent, func(context.Context, service.AppendExplorationIntent, authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		t.Fatal("changed placement reached preparation")
		return service.AppendExplorationPreparation{}, nil
	}); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed placement error = %v, want command reuse", err)
	}
}

func TestAppendExplorationAuthorizationPrecedesDurableLookupAndPreparation(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{denied: true}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-append", "draft-append", "revision-append", "revision-append-result")
	// Seed a valid lifecycle without going through the denied create path.
	seedAuthorizer := &canonicalAuthorizer{}
	seedService := newCanonicalService(t, repository, seedAuthorizer, compiler, "dashboard-append", "draft-append", "revision-append", "revision-append-result")
	created, err := seedService.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "actor", Title: "Append", Slug: "append", SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-append"})
	if err != nil {
		t.Fatal(err)
	}
	prepared := false
	if _, err := svc.AppendExploration(t.Context(), appendIntent(created), func(context.Context, service.AppendExplorationIntent, authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		prepared = true
		return preparedAppend(appendIntent(created)), nil
	}); err == nil || !authorizer.denied || prepared {
		t.Fatalf("denied append err=%v prepared=%v", err, prepared)
	}
	if repository.lookupCommandCalls != 0 {
		t.Fatalf("denied append performed durable lookup: calls=%d", repository.lookupCommandCalls)
	}
}

func TestAppendExplorationRejectsNonNativeRequestIDBeforeDurableLookup(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-append", "draft-append", "revision-append", "revision-append-result")
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "actor", Title: "Append", Slug: "append", SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-append"})
	if err != nil {
		t.Fatal(err)
	}
	intent := appendIntent(created)
	intent.RequestID = "legacy-request-id"
	if _, err := svc.AppendExploration(t.Context(), intent, func(context.Context, service.AppendExplorationIntent, authoring.DashboardLifecycle) (service.AppendExplorationPreparation, error) {
		t.Fatal("invalid request identity reached preparation")
		return service.AppendExplorationPreparation{}, nil
	}); err == nil {
		t.Fatal("invalid request identity was accepted")
	}
	if repository.lookupCommandCalls != 0 {
		t.Fatalf("invalid request identity reached durable lookup: calls=%d", repository.lookupCommandCalls)
	}
}

func appendIntent(result service.Result) service.AppendExplorationIntent {
	return service.AppendExplorationIntent{ProjectID: projectgraph.ResourceID("project:test"), ActorID: "actor", DashboardID: result.Lifecycle.ID, DraftID: result.Lifecycle.Draft.ID, ExpectedRevision: result.Revision, PageID: "overview", RequestID: appendRequestID, PlacementChoice: "half", Spec: structExplorationSpec()}
}

func preparedAppend(input service.AppendExplorationIntent) service.AppendExplorationPreparation {
	return service.AppendExplorationPreparation{Command: authoring.Command{ID: authoring.CommandID(input.RequestID), DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: input.ActorID}, AppendExplorationVisual: &authoring.AppendExplorationVisualPayload{PageID: input.PageID, VisualID: "append_visual", ComponentID: "append_component", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}, SemanticModel: "model:test", Visual: appendVisual()}}, Release: func() {}}
}

func appendVisual() document.DashboardVisual {
	return document.DashboardVisual{Type: "table", Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}}}}, Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}}}
}

func stringPtr(value string) *string { return &value }

// Keep the service test independent of exploration contract construction; the
// service fingerprints the closed value and leaves shape/lineage to the app
// preparer. This non-zero JSON value also proves changed-body detection.
func structExplorationSpec() exploration.ExplorationSpec {
	return exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "model:test", Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10}
}
