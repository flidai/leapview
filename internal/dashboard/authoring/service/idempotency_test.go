package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
)

type idempotentFakeRepository struct {
	*fakeRepository
	operations map[string]authoring.CreateOperationResult
}

func newIdempotentFakeRepository() *idempotentFakeRepository {
	return &idempotentFakeRepository{fakeRepository: newFakeRepository(), operations: map[string]authoring.CreateOperationResult{}}
}

func operationMapKey(operation authoring.CreateOperation) string {
	return operation.ProjectID.String() + "|" + operation.ActorID + "|" + operation.Kind + "|" + operation.IdempotencyKey
}

func (r *idempotentFakeRepository) LookupCreateOperation(_ context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	result, ok := r.operations[operationMapKey(operation)]
	return result, ok, nil
}

func (r *idempotentFakeRepository) Create(ctx context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	if input.Operation.Enabled() {
		key := operationMapKey(input.Operation)
		if existing, ok := r.operations[key]; ok {
			if existing.Fingerprint != input.Operation.Fingerprint {
				return authoring.DashboardLifecycle{}, authoring.ErrCommandReuse
			}
			return r.lifecycle, nil
		}
		created, err := r.fakeRepository.Create(ctx, input)
		if err != nil {
			return authoring.DashboardLifecycle{}, err
		}
		r.operations[key] = authoring.CreateOperationResult{DashboardID: created.ID, Revision: input.Revision.Token(), Fingerprint: input.Operation.Fingerprint}
		return created, nil
	}
	return r.fakeRepository.Create(ctx, input)
}

func TestCreateIdempotencyReplaysExactResultAndDoesNotAllocate(t *testing.T) {
	repository := newIdempotentFakeRepository()
	auth, compiler := &fakeAuthorizer{}, &fakeCompiler{}
	dashboardCalls, draftCalls, revisionCalls, clockCalls := 0, 0, 0, 0
	svc, err := service.NewService(service.Options{
		Repository: repository, Authorizer: auth, Compiler: compiler,
		Now:            func() (now time.Time) { clockCalls++; return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) { dashboardCalls++; return "dash-id", nil },
		NewDraftID:     func() (authoring.DraftID, error) { draftCalls++; return "draft-id", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { revisionCalls++; return "revision-id", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := service.CreateRequest{ProjectID: "project", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", SemanticModel: "sales", Origin: authoring.OriginAgent, ConversationID: "conversation", ToolCallID: "tool-call", IdempotencyKey: "retry-1"}
	first, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	firstCounts := [4]int{dashboardCalls, draftCalls, revisionCalls, clockCalls}
	replay, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Revision != first.Revision || replay.Lifecycle.ID != first.Lifecycle.ID || repository.createCalls != 1 {
		t.Fatalf("replay = %#v, first = %#v, creates = %d", replay, first, repository.createCalls)
	}
	if [4]int{dashboardCalls, draftCalls, revisionCalls, clockCalls} != firstCounts {
		t.Fatalf("replay allocated identities/time: first=%v now=%v", firstCounts, [4]int{dashboardCalls, draftCalls, revisionCalls, clockCalls})
	}
	changed := request
	changed.Title = "Different"
	if _, err := svc.Create(t.Context(), changed); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed payload error = %v", err)
	}
	other := request
	other.IdempotencyKey = "retry-2"
	if _, err := svc.Create(t.Context(), other); err != nil {
		t.Fatalf("different key create error = %v", err)
	}
	if repository.createCalls != 2 {
		t.Fatalf("different key creates = %d", repository.createCalls)
	}
}

func TestCreateIdempotencyAuthorizesStoredTargetBeforeReuseConflict(t *testing.T) {
	repository := newIdempotentFakeRepository()
	auth := &targetAuthorizer{}
	// Build a service with the target-sensitive authorizer while retaining the
	// deterministic ID generators used by the existing helper.
	svc, err := service.NewService(service.Options{Repository: repository, Authorizer: auth, Compiler: &fakeCompiler{}, Now: func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) }, NewDashboardID: func() (authoring.DashboardID, error) { return "dash-id", nil }, NewDraftID: func() (authoring.DraftID, error) { return "draft-id", nil }, NewRevisionID: func() (authoring.RevisionID, error) { return "revision-id", nil }})
	if err != nil {
		t.Fatal(err)
	}
	request := service.CreateRequest{ProjectID: "project", ActorID: "actor", Title: "Orders", Slug: "orders", SemanticModel: "sales", IdempotencyKey: "retry-1"}
	if _, err := svc.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	auth.deny = true
	changed := request
	changed.Title = "Different"
	if _, err := svc.Create(t.Context(), changed); err == nil || !errors.Is(err, errDenied) {
		t.Fatalf("revoked changed replay error = %v", err)
	}
}

func TestCreateIdempotencyRejectsUnsupportedRepository(t *testing.T) {
	repository := newFakeRepository()
	svc := newService(t, repository, &fakeAuthorizer{}, &fakeCompiler{})
	_, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project", ActorID: "actor", Title: "Orders", Slug: "orders", SemanticModel: "sales", IdempotencyKey: "retry-1"})
	if err == nil || repository.createCalls != 0 {
		t.Fatalf("unsupported repository err=%v createCalls=%d", err, repository.createCalls)
	}
}

func TestCreateOperationIdempotencyKeyLength(t *testing.T) {
	base := authoring.CreateOperation{ProjectID: "project", ActorID: "actor", Kind: "create", IdempotencyKey: "retry", Fingerprint: "sha256:fingerprint"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", string(make([]byte, 201))} {
		invalid := base
		invalid.IdempotencyKey = key
		if key == "" {
			// Empty keys disable optional idempotency and therefore remain valid.
			continue
		}
		if err := invalid.Validate(); err == nil {
			t.Fatalf("key length %d unexpectedly valid", len(key))
		}
	}
}

var errDenied = errors.New("denied")

type targetAuthorizer struct{ deny bool }

func (a *targetAuthorizer) Authorize(_ context.Context, _ service.AuthorizationRequest) error {
	if a.deny {
		return errDenied
	}
	return nil
}
