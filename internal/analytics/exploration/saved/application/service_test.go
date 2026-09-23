package application

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type testRuntime struct {
	model       *semanticmodel.Model
	projections *int
	identity    projectgraph.ServingIdentity
}

func (testRuntime) Close() error                             { return nil }
func (r testRuntime) Identity() projectgraph.ServingIdentity { return r.identity }
func (r testRuntime) SemanticModelProjection(_ projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if r.projections != nil {
		(*r.projections)++
	}
	return r.model, r.model != nil
}

type testLease struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
	released int
}

func (l *testLease) Runtime() projectruntime.Runtime        { return l.runtime }
func (l *testLease) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *testLease) Release()                               { l.released++ }

type testProvider struct {
	lease        *testLease
	acquisitions int
	projections  int
}

func (p *testProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquisitions++
	if p.lease == nil {
		identity, _ := projectgraph.NewServingIdentity("project:sales", "production", "generation-current")
		p.lease = &testLease{runtime: testRuntime{model: testModel(), projections: &p.projections, identity: identity}, identity: identity}
	}
	return p.lease, nil
}

type testAuthorizer struct {
	deny      bool
	denyErr   error
	denyAt    int
	denyModel projectgraph.ResourceID
	requests  []AuthorizationRequest
	leases    []projectruntime.Lease
}

func (a *testAuthorizer) Authorize(_ context.Context, lease projectruntime.Lease, request AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	a.leases = append(a.leases, lease)
	if a.denyAt > 0 && len(a.requests) == a.denyAt {
		if a.denyErr != nil {
			return a.denyErr
		}
		return saved.ErrUnauthorized
	}
	if a.deny {
		if a.denyErr != nil {
			return a.denyErr
		}
		return saved.ErrUnauthorized
	}
	if a.denyModel != "" && request.SemanticModelID == a.denyModel {
		if a.denyErr != nil {
			return a.denyErr
		}
		return saved.ErrUnauthorized
	}
	return nil
}

type testExecutor struct {
	lease projectruntime.Lease
	actor string
	query dataquery.Query
}

func (e *testExecutor) Execute(_ context.Context, lease projectruntime.Lease, actor string, query dataquery.Query) (dataquery.Result, error) {
	e.lease, e.actor, e.query = lease, actor, query
	return dataquery.Result{Status: dataquery.StatusSuccess}, nil
}

type testRepository struct {
	lifecycle      saved.Lifecycle
	revision       saved.Revision
	lifecycleErr   error
	lifecycleReads int
	revisionReads  int
	listCalls      int
	createCalls    int
	updateCalls    int
	archiveCalls   int
	duplicateCalls int
	lookupCalls    int
	listPages      map[string]saved.ListPage
	listRows       []saved.Lifecycle
	listPageInputs []saved.ListInput
	replay         saved.MutationReplayMetadata
	replayRevision saved.Revision
	replayFound    bool
	lastCreate     saved.CreateInput
	lastUpdate     saved.UpdateVersionInput
	lastDuplicate  saved.DuplicateInput
	lastArchive    saved.ArchiveInput
	auditIntent    access.AuditIntent
	auditPresent   bool
}

func (r *testRepository) Create(ctx context.Context, input saved.CreateInput) (saved.MutationResult, error) {
	r.createCalls++
	r.lastCreate = input
	r.captureAudit(ctx)
	r.revision = input.Revision.Clone()
	r.lifecycle = saved.SavedExploration{ProjectID: input.ProjectID, ID: input.ID, OwnerPrincipalID: input.OwnerPrincipalID, Title: input.Title, Slug: input.Slug, Visibility: input.Visibility, SemanticModelID: input.SemanticModelID, Status: saved.StatusActive, CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt, Revision: input.Revision}.Lifecycle()
	return saved.MutationResult{Lifecycle: r.lifecycle, Revision: &r.revision, AppliedRevision: r.revision.Token(), Evidence: input.Evidence}, nil
}
func (r *testRepository) LookupMutation(_ context.Context, _ saved.MutationLookupInput) (saved.MutationReplayMetadata, bool, error) {
	r.lookupCalls++
	if !r.replayFound {
		return saved.MutationReplayMetadata{}, false, nil
	}
	return r.replay.Clone(), true, nil
}
func (r *testRepository) GetLifecycle(_ context.Context, input saved.ReadInput) (saved.Lifecycle, error) {
	r.lifecycleReads++
	if r.lifecycleErr != nil {
		return saved.Lifecycle{}, r.lifecycleErr
	}
	if len(r.listRows) > 0 {
		for _, lifecycle := range r.listRows {
			if lifecycle.ID == input.ID {
				return lifecycle, nil
			}
		}
	}
	if r.lifecycle.ID == "" {
		return saved.Lifecycle{}, saved.ErrNotFound
	}
	return r.lifecycle, nil
}
func (r *testRepository) GetRevision(_ context.Context, input saved.RevisionReadInput) (saved.Revision, error) {
	r.revisionReads++
	if r.replayRevision.Metadata.ID != "" && r.replayRevision.Token() == input.Revision {
		return r.replayRevision.Clone(), nil
	}
	if r.revision.Token() != input.Revision {
		return saved.Revision{}, saved.ErrNotFound
	}
	return r.revision.Clone(), nil
}
func (r *testRepository) UpdateVersion(ctx context.Context, input saved.UpdateVersionInput) (saved.MutationResult, error) {
	r.updateCalls++
	r.lastUpdate = input
	r.captureAudit(ctx)
	current, err := r.currentAggregate()
	if err != nil {
		return saved.MutationResult{}, err
	}
	updated, err := saved.AppendVersionWithMetadata(current, input.ExpectedRevision, input.Revision, input.UpdatedAt, input.Title, input.Slug, input.Visibility, input.SemanticModelID)
	if err != nil {
		return saved.MutationResult{}, err
	}
	r.lifecycle, r.revision = updated.Lifecycle(), updated.Revision.Clone()
	return mutationResult(updated, input.Evidence, input.ExpectedRevision), nil
}
func (r *testRepository) Duplicate(ctx context.Context, input saved.DuplicateInput) (saved.MutationResult, error) {
	r.duplicateCalls++
	r.lastDuplicate = input
	r.captureAudit(ctx)
	destination, err := saved.NewSavedExploration(saved.NewInput{ProjectID: input.Destination.ProjectID, ID: input.Destination.ID, OwnerPrincipalID: input.Destination.OwnerPrincipalID, Title: input.Destination.Title, Slug: input.Destination.Slug, Visibility: input.Destination.Visibility, SemanticModelID: input.Destination.SemanticModelID, CreatedAt: input.Destination.CreatedAt, Revision: input.Destination.Revision})
	if err != nil {
		return saved.MutationResult{}, err
	}
	r.lifecycle, r.revision = destination.Lifecycle(), destination.Revision.Clone()
	return mutationResult(destination, input.Evidence, input.ExpectedSourceRevision), nil
}
func (r *testRepository) List(context.Context, saved.ListInput) ([]saved.Lifecycle, error) {
	r.listCalls++
	if r.lifecycle.ID == "" {
		return nil, nil
	}
	return []saved.Lifecycle{r.lifecycle}, nil
}
func (r *testRepository) ListPage(ctx context.Context, input saved.ListInput) (saved.ListPage, error) {
	r.listPageInputs = append(r.listPageInputs, input)
	if r.listPages != nil {
		page, ok := r.listPages[input.Cursor]
		if !ok {
			return saved.ListPage{}, nil
		}
		return page, nil
	}
	items, err := r.List(ctx, input)
	if err != nil {
		return saved.ListPage{}, err
	}
	if input.Cursor != "" && len(items) > 0 && items[0].ID.String() <= input.Cursor {
		items = nil
	}
	return saved.ListPage{Items: items}, nil
}
func (r *testRepository) Archive(ctx context.Context, input saved.ArchiveInput) (saved.MutationResult, error) {
	r.archiveCalls++
	r.lastArchive = input
	r.captureAudit(ctx)
	current, err := r.currentAggregate()
	if err != nil {
		return saved.MutationResult{}, err
	}
	archived, err := saved.Archive(current, input.ExpectedRevision, input.ArchivedAt)
	if err != nil {
		return saved.MutationResult{}, err
	}
	r.lifecycle = archived.Lifecycle()
	r.revision = archived.Revision.Clone()
	return saved.MutationResult{Lifecycle: r.lifecycle, AppliedRevision: r.lifecycle.CurrentRevision.Token(), ConcurrencyRevision: input.ExpectedRevision, Evidence: input.Evidence}, nil
}

func (r *testRepository) currentAggregate() (saved.SavedExploration, error) {
	return saved.NewSavedExploration(saved.NewInput{ProjectID: r.lifecycle.ProjectID, ID: r.lifecycle.ID, OwnerPrincipalID: r.lifecycle.OwnerPrincipalID, Title: r.lifecycle.Title, Slug: r.lifecycle.Slug, Visibility: r.lifecycle.Visibility, SemanticModelID: r.lifecycle.SemanticModelID, CreatedAt: r.lifecycle.CreatedAt, Revision: r.revision})
}

func (r *testRepository) captureAudit(ctx context.Context) {
	r.auditIntent, r.auditPresent = saved.AuditIntentFromContext(ctx)
}

func mutationResult(value saved.SavedExploration, evidence saved.MutationEvidence, concurrencyRevision saved.RevisionToken) saved.MutationResult {
	result := saved.MutationResult{Lifecycle: value.Lifecycle(), AppliedRevision: value.Revision.Token(), Evidence: evidence}
	result.ConcurrencyRevision = concurrencyRevision
	if value.Status != saved.StatusArchived {
		revision := value.Revision.Clone()
		result.Revision = &revision
	}
	return result
}

func TestReadAuthorizesBeforeExactRevisionAndMapsDeniedToNotFound(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{deny: true}
	service := mustService(t, repo, provider, authorizer, nil)
	_, err := service.Read(t.Context(), saved.ReadRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "other"})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("Read denied error = %v, want ErrNotFound", err)
	}
	if repo.revisionReads != 0 {
		t.Fatalf("revision reads = %d, want deny before payload", repo.revisionReads)
	}
	if provider.lease.released != 1 {
		t.Fatalf("lease releases = %d, want one", provider.lease.released)
	}
}

func TestReadRejectsRuntimeWhoseIdentityDoesNotMatchLease(t *testing.T) {
	repo := seededRepository(t)
	leaseIdentity, err := projectgraph.NewServingIdentity("project:sales", "production", "generation-current")
	if err != nil {
		t.Fatal(err)
	}
	runtimeIdentity := leaseIdentity
	runtimeIdentity.GenerationID = "generation-other"
	provider := &testProvider{lease: &testLease{
		identity: leaseIdentity,
		runtime:  testRuntime{model: testModel(), identity: runtimeIdentity},
	}}
	service := mustService(t, repo, provider, &testAuthorizer{}, nil)
	_, err = service.Read(t.Context(), saved.ReadRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "owner"})
	if !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("Read error = %v, want unavailable", err)
	}
	if repo.lifecycleReads != 0 {
		t.Fatalf("lifecycle reads = %d, want zero before repository access", repo.lifecycleReads)
	}
	if provider.lease.released != 1 {
		t.Fatalf("lease releases = %d, want one", provider.lease.released)
	}
}

func TestUpdateDeniedBeforeCurrentRevisionAndModelProjection(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{deny: true}
	service := mustService(t, repo, provider, authorizer, nil)
	request := saved.UpdateVersionRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Title: "Orders v2", Slug: "orders-v2", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionUpdate, "update-denied")}
	fingerprint, err := FingerprintUpdate(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Evidence.Fingerprint = fingerprint
	_, err = service.UpdateVersion(t.Context(), request)
	if !errors.Is(err, saved.ErrNotFound) || repo.revisionReads != 0 || provider.projections != 0 {
		t.Fatalf("denied update error=%v revisionReads=%d projections=%d", err, repo.revisionReads, provider.projections)
	}
}

func TestUpdateTargetModelAuthorizationPrecedesTargetProjection(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{denyModel: "semantic:marketing"}
	service := mustService(t, repo, provider, authorizer, nil)
	marketing := testSpec()
	marketing.ModelID = "semantic:marketing"
	request := saved.UpdateVersionRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Title: "Orders v2", Slug: "orders-v2", Visibility: saved.VisibilityPrivate, Spec: marketing, Evidence: validEvidence(t, saved.MutationActionUpdate, "update-target-denied")}
	fingerprint, err := FingerprintUpdate(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Evidence.Fingerprint = fingerprint
	_, err = service.UpdateVersion(t.Context(), request)
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("target-model denial error = %v, want ErrNotFound", err)
	}
	if provider.projections != 0 {
		t.Fatalf("model projections = %d, want target denial before projection", provider.projections)
	}
	if len(authorizer.requests) != 1 || authorizer.requests[0].SemanticModelID != "semantic:marketing" {
		t.Fatalf("authorization requests = %#v, want target model before projection", authorizer.requests)
	}
}

func TestRestrictedExistingResourceIsNotReadableOrListed(t *testing.T) {
	repo := seededRepository(t)
	repo.lifecycle.Visibility = saved.VisibilityRestricted
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, nil)
	if _, err := service.Read(t.Context(), saved.ReadRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner"}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("restricted Read error = %v, want ErrNotFound", err)
	}
	if repo.revisionReads != 0 || len(authorizer.requests) != 0 {
		t.Fatalf("restricted Read leaked access: revisionReads=%d authCalls=%d", repo.revisionReads, len(authorizer.requests))
	}
	rows, err := service.List(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner"})
	if err != nil {
		t.Fatalf("restricted List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("restricted List rows = %#v, want empty", rows)
	}
}

func TestRestrictedExistingResourceIsHiddenAcrossOperations(t *testing.T) {
	repo := seededRepository(t)
	repo.lifecycle.Visibility = saved.VisibilityRestricted
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, &testExecutor{})
	read := saved.ReadRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner"}
	if _, err := service.Reopen(t.Context(), saved.ReopenRequest(read)); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("restricted Reopen error=%v, want ErrNotFound", err)
	}
	if _, err := service.Execute(t.Context(), saved.ExecuteRequest{ProjectID: read.ProjectID, ID: read.ID, ActorID: read.ActorID}); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("restricted Execute error=%v, want ErrNotFound", err)
	}
	update := saved.UpdateVersionRequest{ProjectID: read.ProjectID, ID: read.ID, ActorID: read.ActorID, ExpectedRevision: repo.revision.Token(), Title: "Updated", Slug: "updated", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionUpdate, "restricted-update")}
	update.Evidence.Fingerprint = mustUpdateFingerprint(t, update)
	if _, err := service.UpdateVersion(t.Context(), update); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("restricted Update error=%v, want ErrNotFound", err)
	}
	duplicate := saved.DuplicateRequest{ProjectID: read.ProjectID, SourceID: read.ID, ExpectedSourceRevision: repo.revision.Token(), ID: "restricted-copy", ActorID: read.ActorID, Title: "Copy", Slug: "copy", Visibility: saved.VisibilityPrivate, Evidence: validEvidence(t, saved.MutationActionDuplicate, "restricted-duplicate")}
	duplicate.Evidence.Fingerprint = mustDuplicateFingerprint(t, duplicate)
	if _, err := service.Duplicate(t.Context(), duplicate); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("restricted Duplicate error=%v, want ErrNotFound", err)
	}
	archive := saved.ArchiveRequest{ProjectID: read.ProjectID, ID: read.ID, ActorID: read.ActorID, ExpectedRevision: repo.revision.Token(), Evidence: validEvidence(t, saved.MutationActionArchive, "restricted-archive")}
	archive.Evidence.Fingerprint = mustArchiveFingerprint(t, archive)
	if _, err := service.Archive(t.Context(), archive); !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("restricted Archive error=%v, want ErrNotFound", err)
	}
	if repo.revisionReads != 0 || repo.updateCalls != 0 || repo.duplicateCalls != 0 || repo.archiveCalls != 0 || len(authorizer.requests) != 0 {
		t.Fatalf("restricted resource leaked: revisions=%d update=%d duplicate=%d archive=%d auth=%d", repo.revisionReads, repo.updateCalls, repo.duplicateCalls, repo.archiveCalls, len(authorizer.requests))
	}
}

func TestReopenIsDetachedAndExecuteUsesSameLeaseAndActor(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	executor := &testExecutor{}
	service := mustService(t, repo, provider, authorizer, executor)
	reopened, err := service.Reopen(t.Context(), saved.ReopenRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "owner"})
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	reopened.Spec.Dimensions[0].Field = "orders.changed"
	fresh, err := service.Reopen(t.Context(), saved.ReopenRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "owner"})
	if err != nil {
		t.Fatalf("second Reopen: %v", err)
	}
	if fresh.Spec.Dimensions[0].Field == "orders.changed" || repo.revisionReads != 2 {
		t.Fatal("reopen mutated persisted/current working copy")
	}
	result, err := service.Execute(t.Context(), saved.ExecuteRequest{ProjectID: "project:sales", ID: repo.lifecycle.ID, ActorID: "owner", RequestID: "request-1", CorrelationID: "correlation-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if executor.lease != provider.lease || executor.actor != "owner" || executor.query.PrincipalID != "owner" {
		t.Fatalf("executor binding = lease %p actor %q query=%#v", executor.lease, executor.actor, executor.query)
	}
	if result.Evidence.ServingIdentity != provider.lease.identity {
		t.Fatalf("execution evidence identity = %#v, want %#v", result.Evidence.ServingIdentity, provider.lease.identity)
	}
	if provider.acquisitions != 3 || provider.lease.released != 3 {
		t.Fatalf("acquire/release = %d/%d, want 3/3", provider.acquisitions, provider.lease.released)
	}
}

func TestCreateUsesCurrentLeaseIdentityAndRejectsRestrictedBeforePersistence(t *testing.T) {
	repo := &testRepository{}
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, nil)
	request := saved.CreateRequest{ProjectID: "project:sales", ID: "exploration-new", ActorID: "owner", Title: "Orders", Slug: "orders-new", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionCreate, "create-1")}
	fingerprint, err := FingerprintCreate(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Evidence.Fingerprint = fingerprint
	if _, err := service.Create(t.Context(), request); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.revision.Metadata.ServingIdentity != provider.lease.identity {
		t.Fatalf("created provenance = %#v, want current %#v", repo.revision.Metadata.ServingIdentity, provider.lease.identity)
	}
	restricted := request
	restricted.ID = "exploration-restricted"
	restricted.Visibility = saved.VisibilityRestricted
	restricted.Evidence = validEvidence(t, saved.MutationActionCreate, "create-restricted")
	fingerprint, _ = FingerprintCreate(restricted)
	restricted.Evidence.Fingerprint = fingerprint
	if _, err := service.Create(t.Context(), restricted); !errors.Is(err, saved.ErrInvalid) {
		t.Fatalf("restricted create error = %v, want ErrInvalid", err)
	}
	if repo.createCalls != 1 {
		t.Fatalf("create calls = %d, want restricted rejected before persistence", repo.createCalls)
	}
}

func TestForbiddenReadIsPublicNotFoundBeforePayload(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{deny: true, denyErr: access.ErrForbidden}
	service := mustService(t, repo, provider, authorizer, nil)
	_, err := service.Read(t.Context(), saved.ReadRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner"})
	if !errors.Is(err, saved.ErrNotFound) || repo.revisionReads != 0 || provider.lease.released != 1 {
		t.Fatalf("forbidden read error=%v revisions=%d releases=%d", err, repo.revisionReads, provider.lease.released)
	}
}

func TestMissingReadMatchesDeniedRead(t *testing.T) {
	repo := &testRepository{}
	provider := &testProvider{}
	service := mustService(t, repo, provider, &testAuthorizer{}, nil)
	_, err := service.Read(t.Context(), saved.ReadRequest{ProjectID: "project:sales", ID: "missing", ActorID: "owner"})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Fatalf("missing read error=%v, want ErrNotFound", err)
	}
	if repo.revisionReads != 0 || provider.acquisitions != 1 || provider.lease.released != 1 {
		t.Fatalf("missing read revisions=%d acquire/release=%d/%d", repo.revisionReads, provider.acquisitions, provider.lease.released)
	}
}

func TestListUsesLifecycleOnlyAndFiltersDeniedRows(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{deny: true}
	service := mustService(t, repo, provider, authorizer, nil)
	rows, err := service.List(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 || repo.revisionReads != 0 || repo.listCalls != 1 {
		t.Fatalf("List rows=%#v revisions=%d calls=%d", rows, repo.revisionReads, repo.listCalls)
	}
	if provider.acquisitions != 1 || provider.lease.released != 1 {
		t.Fatalf("List acquire/release=%d/%d, want 1/1", provider.acquisitions, provider.lease.released)
	}
}

func TestListPagePaginatesAuthorizedRowsWithoutLeakingDeniedCursor(t *testing.T) {
	repo := seededRepository(t)
	rows := []saved.Lifecycle{repo.lifecycle}
	rows[0].ID = "exploration-a"
	hidden := repo.lifecycle
	hidden.ID = "exploration-hidden"
	hidden.SemanticModelID = "semantic:hidden"
	visible := repo.lifecycle
	visible.ID = "exploration-c"
	visible.SemanticModelID = "semantic:sales"
	rows = append(rows, hidden, visible)
	repo.listRows = rows
	repo.listPages = map[string]saved.ListPage{
		"":                   {Items: rows},
		"exploration-a":      {Items: []saved.Lifecycle{hidden, visible}},
		"exploration-hidden": {Items: []saved.Lifecycle{visible}},
	}
	provider := &testProvider{}
	authorizer := &testAuthorizer{denyModel: "semantic:hidden"}
	service := mustService(t, repo, provider, authorizer, nil)

	first, err := service.ListPage(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Limit: 1})
	if err != nil {
		t.Fatalf("first ListPage: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].ID != "exploration-a" || first.NextCursor == "" {
		t.Fatalf("first page = %#v, want only authorized a and a cursor", first)
	}
	cursor, err := saved.DecodeListCursor(first.NextCursor, repo.lifecycle.ProjectID, false)
	if err != nil || cursor != "exploration-a" {
		t.Fatalf("first cursor = %q, %v, want authorized a", cursor, err)
	}

	second, err := service.ListPage(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Limit: 1, PageToken: first.NextCursor})
	if err != nil {
		t.Fatalf("second ListPage: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "exploration-c" || second.NextCursor != "" {
		t.Fatalf("second page = %#v, want c and no cursor", second)
	}
	for _, input := range repo.listPageInputs {
		if input.Cursor == "exploration-hidden" {
			t.Fatalf("repository was advanced with denied cursor: %#v", input)
		}
	}
}

func TestListPageMapsOnlyInaccessibleCursorTargetsToInvalid(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "not found", err: saved.ErrNotFound},
		{name: "unauthorized", err: saved.ErrUnauthorized},
		{name: "forbidden", err: access.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := seededRepository(t)
			repo.lifecycleErr = test.err
			service := mustService(t, repo, &testProvider{}, &testAuthorizer{}, nil)
			cursor, err := saved.EncodeListCursor(repo.lifecycle.ProjectID, false, repo.lifecycle.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.ListPage(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Limit: 1, PageToken: cursor})
			if !errors.Is(err, saved.ErrInvalid) || errors.Is(err, test.err) {
				t.Fatalf("cursor lookup error = %v, want only ErrInvalid", err)
			}
		})
	}
	repo := seededRepository(t)
	cursor, err := saved.EncodeListCursor(repo.lifecycle.ProjectID, false, repo.lifecycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := mustService(t, repo, &testProvider{}, &testAuthorizer{deny: true}, nil)
	_, err = service.ListPage(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Limit: 1, PageToken: cursor})
	if !errors.Is(err, saved.ErrInvalid) {
		t.Fatalf("denied cursor lookup error = %v, want ErrInvalid", err)
	}
}

func TestListPagePreservesCursorLookupFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "unavailable", err: saved.ErrUnavailable},
		{name: "cancelled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "storage failure", err: errors.New("storage read failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := seededRepository(t)
			repo.lifecycleErr = test.err
			service := mustService(t, repo, &testProvider{}, &testAuthorizer{}, nil)
			cursor, err := saved.EncodeListCursor(repo.lifecycle.ProjectID, false, repo.lifecycle.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.ListPage(t.Context(), saved.ListRequest{ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Limit: 1, PageToken: cursor})
			if !errors.Is(err, test.err) || errors.Is(err, saved.ErrInvalid) {
				t.Fatalf("cursor lookup error = %v, want preserved %v", err, test.err)
			}
		})
	}
}

func TestUpdateRepairsIncompatibleCurrentWithoutReadingOldPayload(t *testing.T) {
	old := testSpec()
	old.Dimensions[0].Field = "orders.removed"
	repo := seededRepositoryWithSpec(t, old)
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, nil)
	request := saved.UpdateVersionRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Title: "Orders repaired", Slug: "orders-repaired", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionUpdate, "repair")}
	request.Evidence.Fingerprint = mustUpdateFingerprint(t, request)
	result, err := service.UpdateVersion(t.Context(), request)
	if err != nil {
		t.Fatalf("repair UpdateVersion: %v", err)
	}
	if repo.revisionReads != 0 || repo.updateCalls != 1 || result.Revision == nil || result.Revision.Metadata.Number != 2 || result.ConcurrencyRevision != request.ExpectedRevision {
		t.Fatalf("repair revisions=%d updates=%d result=%#v", repo.revisionReads, repo.updateCalls, result)
	}
	if repo.lastUpdate.Revision.Metadata.ServingIdentity != provider.lease.identity {
		t.Fatalf("repair provenance=%#v, want current %#v", repo.lastUpdate.Revision.Metadata.ServingIdentity, provider.lease.identity)
	}
	if !repo.auditPresent || repo.auditIntent.PrincipalID != "owner" || repo.auditIntent.RequestID != request.Evidence.RequestID {
		t.Fatalf("repair audit present=%t intent=%#v", repo.auditPresent, repo.auditIntent)
	}
	if provider.acquisitions != 1 || provider.lease.released != 1 {
		t.Fatalf("repair acquire/release=%d/%d, want 1/1", provider.acquisitions, provider.lease.released)
	}
}

func TestReadAndReopenIncompatibleRevisionPreserveCASRepairPath(t *testing.T) {
	old := testSpec()
	old.Dimensions[0].Field = "orders.removed"
	repo := seededRepositoryWithSpec(t, old)
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	executor := &testExecutor{}
	service := mustService(t, repo, provider, authorizer, executor)
	request := saved.ReadRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner"}

	read, err := service.Read(t.Context(), request)
	if err != nil {
		t.Fatalf("Read incompatible revision: %v", err)
	}
	if read.Revision.Token() != repo.revision.Token() || read.Revision.Payload.Canonical() == nil {
		t.Fatalf("Read revision = %#v, want exact current token and payload", read.Revision.Metadata)
	}
	reopened, err := service.Reopen(t.Context(), saved.ReopenRequest(request))
	if err != nil {
		t.Fatalf("Reopen incompatible revision: %v", err)
	}
	if reopened.Revision.Token() != repo.revision.Token() || reopened.Spec.Dimensions[0].Field != "orders.removed" {
		t.Fatalf("Reopen = %#v, want exact token and authored removed field", reopened)
	}
	if provider.projections != 0 {
		t.Fatalf("view/reopen model projections = %d, want zero", provider.projections)
	}

	if _, err := service.Execute(t.Context(), saved.ExecuteRequest{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID}); !errors.Is(err, saved.ErrInvalidPayload) {
		t.Fatalf("Execute incompatible revision error = %v, want invalid payload", err)
	}
	if executor.lease != nil {
		t.Fatal("incompatible revision reached governed executor")
	}

	update := saved.UpdateVersionRequest{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, ExpectedRevision: read.Revision.Token(), Title: "Orders repaired", Slug: "orders-repaired", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionUpdate, "read-repair")}
	update.Evidence.Fingerprint = mustUpdateFingerprint(t, update)
	if _, err := service.UpdateVersion(t.Context(), update); err != nil {
		t.Fatalf("CAS repair after Read/Reopen: %v", err)
	}
}

func TestUpdateStaleCASDoesNotMutate(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, nil)
	request := saved.UpdateVersionRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: saved.RevisionToken{RevisionID: "revision-stale", Number: 1, ContentHash: repo.revision.Token().ContentHash}, Title: "Orders stale", Slug: "orders-stale", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionUpdate, "stale")}
	request.Evidence.Fingerprint = mustUpdateFingerprint(t, request)
	_, err := service.UpdateVersion(t.Context(), request)
	if !errors.Is(err, saved.ErrStaleRevision) || repo.updateCalls != 0 || repo.revisionReads != 0 {
		t.Fatalf("stale update error=%v updates=%d revisions=%d", err, repo.updateCalls, repo.revisionReads)
	}
	if provider.acquisitions != 1 || provider.lease.released != 1 {
		t.Fatalf("stale acquire/release=%d/%d, want 1/1", provider.acquisitions, provider.lease.released)
	}
}

func TestDuplicateCopiesExactBytesAndUsesCurrentLeaseAndAudit(t *testing.T) {
	repo := seededRepository(t)
	sourcePayload := repo.revision.Payload.Canonical()
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, nil)
	request := saved.DuplicateRequest{ProjectID: repo.lifecycle.ProjectID, SourceID: repo.lifecycle.ID, ExpectedSourceRevision: repo.revision.Token(), ID: "exploration-copy", ActorID: "owner", Title: "Orders copy", Slug: "orders-copy", Visibility: saved.VisibilityOrganization, Evidence: validEvidence(t, saved.MutationActionDuplicate, "duplicate")}
	request.Evidence.Fingerprint = mustDuplicateFingerprint(t, request)
	result, err := service.Duplicate(t.Context(), request)
	if err != nil {
		t.Fatalf("Duplicate: %v", err)
	}
	if repo.duplicateCalls != 1 || result.Lifecycle.ID != request.ID || result.Revision == nil || result.ConcurrencyRevision != request.ExpectedSourceRevision {
		t.Fatalf("duplicate calls=%d result=%#v", repo.duplicateCalls, result)
	}
	if got := repo.lastDuplicate.Destination.Revision.Payload.Canonical(); !bytes.Equal(got, sourcePayload) {
		t.Fatalf("duplicate payload bytes changed: got %x want %x", got, sourcePayload)
	}
	if repo.lastDuplicate.Destination.Revision.Metadata.ServingIdentity != provider.lease.identity {
		t.Fatalf("duplicate provenance=%#v, want current %#v", repo.lastDuplicate.Destination.Revision.Metadata.ServingIdentity, provider.lease.identity)
	}
	if repo.lastDuplicate.ExpectedSourceRevision != request.ExpectedSourceRevision || !repo.auditPresent || repo.auditIntent.PrincipalID != "owner" {
		t.Fatalf("duplicate CAS/audit token=%#v audit=%#v present=%t", repo.lastDuplicate.ExpectedSourceRevision, repo.auditIntent, repo.auditPresent)
	}
	if provider.acquisitions != 1 || provider.lease.released != 1 || repo.revisionReads != 1 {
		t.Fatalf("duplicate acquire/release/revisions=%d/%d/%d", provider.acquisitions, provider.lease.released, repo.revisionReads)
	}
}

func TestDuplicateReplayReauthorizesDestinationAndDenialIsNotFound(t *testing.T) {
	for _, test := range []struct {
		name    string
		denyAt  int
		wantErr error
	}{
		{name: "allow", wantErr: nil},
		{name: "deny", denyAt: 2, wantErr: saved.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := seededRepository(t)
			request := saved.DuplicateRequest{ProjectID: repo.lifecycle.ProjectID, SourceID: repo.lifecycle.ID, ExpectedSourceRevision: repo.revision.Token(), ID: "exploration-replay", ActorID: "owner", Title: "Replay", Slug: "replay", Visibility: saved.VisibilityPrivate, Evidence: validEvidence(t, saved.MutationActionDuplicate, "replay-"+test.name)}
			request.Evidence.Fingerprint = mustDuplicateFingerprint(t, request)
			repo.replay, repo.replayRevision = replayDestination(t, request.ID, request.Evidence)
			repo.replayFound = true
			provider := &testProvider{}
			authorizer := &testAuthorizer{denyAt: test.denyAt, denyErr: access.ErrForbidden}
			service := mustService(t, repo, provider, authorizer, nil)
			result, err := service.Duplicate(t.Context(), request)
			if test.wantErr == nil {
				if err != nil || !result.Replayed {
					t.Fatalf("replay result=%#v err=%v", result, err)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("replay denial error=%v, want %v", err, test.wantErr)
			}
			wantRevisionReads := 1
			if test.denyAt > 0 {
				wantRevisionReads = 0
			}
			if repo.duplicateCalls != 0 || repo.revisionReads != wantRevisionReads || provider.acquisitions != 1 || provider.lease.released != 1 {
				t.Fatalf("replay mutations=%d revisions=%d (want %d) acquire/release=%d/%d", repo.duplicateCalls, repo.revisionReads, wantRevisionReads, provider.acquisitions, provider.lease.released)
			}
			if test.denyAt == 0 {
				if result.Revision == nil || !bytes.Equal(result.Revision.Payload.Canonical(), repo.replayRevision.Payload.Canonical()) {
					t.Fatalf("replay revision=%#v, want exact historic payload", result.Revision)
				}
				if len(authorizer.requests) != 2 || authorizer.requests[1].Action != AuthorizationActionCreate || authorizer.requests[1].ExplorationID != repo.replay.Lifecycle.ID || authorizer.requests[1].Lifecycle != repo.replay.Lifecycle {
					t.Fatalf("replay authorization requests=%#v", authorizer.requests)
				}
				if authorizer.leases[0] != provider.lease || authorizer.leases[1] != provider.lease {
					t.Fatalf("replay authorization did not reuse lease")
				}
			} else if len(authorizer.requests) != 2 {
				t.Fatalf("replay denied authorization calls=%d, want source + destination", len(authorizer.requests))
			}
		})
	}
}

func TestAuthorizeMutationReplayOnlyLooksUpAndReauthorizes(t *testing.T) {
	t.Run("missing ledger does not mutate", func(t *testing.T) {
		repo := seededRepository(t)
		service := mustService(t, repo, &testProvider{}, &testAuthorizer{}, nil)
		allowed, err := service.AuthorizeMutationReplay(t.Context(), MutationReplayAuthorizationRequest{
			ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Action: saved.MutationActionUpdate,
			IdempotencyKey: "ui:missing", Fingerprint: "sha256:" + strings.Repeat("0", 64), TargetID: repo.lifecycle.ID,
		})
		if err != nil || allowed || repo.lookupCalls != 1 || repo.createCalls != 0 || repo.updateCalls != 0 || repo.duplicateCalls != 0 || repo.archiveCalls != 0 {
			t.Fatalf("missing replay allowed=%t err=%v lookup=%d mutations=%d/%d/%d/%d", allowed, err, repo.lookupCalls, repo.createCalls, repo.updateCalls, repo.duplicateCalls, repo.archiveCalls)
		}
	})

	t.Run("durable replay is reauthorized", func(t *testing.T) {
		repo := seededRepository(t)
		evidence := validEvidence(t, saved.MutationActionUpdate, "ui:existing")
		repo.replay = saved.MutationReplayMetadata{Lifecycle: repo.lifecycle, AppliedRevision: repo.revision.Token(), Evidence: evidence}
		repo.replayFound = true
		authorizer := &testAuthorizer{}
		service := mustService(t, repo, &testProvider{}, authorizer, nil)
		allowed, err := service.AuthorizeMutationReplay(t.Context(), MutationReplayAuthorizationRequest{
			ProjectID: repo.lifecycle.ProjectID, ActorID: "owner", Action: evidence.Action,
			IdempotencyKey: evidence.IdempotencyKey, Fingerprint: evidence.Fingerprint, TargetID: repo.lifecycle.ID,
		})
		if err != nil || !allowed || len(authorizer.requests) != 1 || authorizer.requests[0].Action != AuthorizationActionEdit || repo.createCalls != 0 || repo.updateCalls != 0 || repo.duplicateCalls != 0 || repo.archiveCalls != 0 {
			t.Fatalf("existing replay allowed=%t err=%v auth=%#v mutations=%d/%d/%d/%d", allowed, err, authorizer.requests, repo.createCalls, repo.updateCalls, repo.duplicateCalls, repo.archiveCalls)
		}
	})
}

func TestArchiveUsesCASWithoutRevisionReadAndCarriesAudit(t *testing.T) {
	repo := seededRepository(t)
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, nil)
	request := saved.ArchiveRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Evidence: validEvidence(t, saved.MutationActionArchive, "archive")}
	request.Evidence.Fingerprint = mustArchiveFingerprint(t, request)
	result, err := service.Archive(t.Context(), request)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if repo.archiveCalls != 1 || repo.revisionReads != 0 || result.Revision != nil || result.Lifecycle.Status != saved.StatusArchived || result.ConcurrencyRevision != request.ExpectedRevision {
		t.Fatalf("archive calls=%d revisions=%d result=%#v", repo.archiveCalls, repo.revisionReads, result)
	}
	if repo.lastArchive.ExpectedRevision != request.ExpectedRevision || !repo.auditPresent || repo.auditIntent.PrincipalID != "owner" {
		t.Fatalf("archive CAS/audit token=%#v audit=%#v present=%t", repo.lastArchive.ExpectedRevision, repo.auditIntent, repo.auditPresent)
	}
	if provider.acquisitions != 1 || provider.lease.released != 1 {
		t.Fatalf("archive acquire/release=%d/%d, want 1/1", provider.acquisitions, provider.lease.released)
	}
}

func TestArchivedExecuteAndUpdateAreRejectedWithoutPayloadRead(t *testing.T) {
	repo := seededRepository(t)
	archivedAt := repo.lifecycle.UpdatedAt.Add(time.Minute)
	repo.lifecycle.Status = saved.StatusArchived
	repo.lifecycle.ArchivedAt = &archivedAt
	repo.lifecycle.UpdatedAt = archivedAt
	provider := &testProvider{}
	authorizer := &testAuthorizer{}
	service := mustService(t, repo, provider, authorizer, &testExecutor{})
	if _, err := service.Execute(t.Context(), saved.ExecuteRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner"}); !errors.Is(err, saved.ErrArchived) {
		t.Fatalf("archived Execute error=%v, want ErrArchived", err)
	}
	request := saved.UpdateVersionRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Title: "Archived update", Slug: "archived-update", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidence(t, saved.MutationActionUpdate, "archived-update")}
	request.Evidence.Fingerprint = mustUpdateFingerprint(t, request)
	if _, err := service.UpdateVersion(t.Context(), request); !errors.Is(err, saved.ErrArchived) {
		t.Fatalf("archived Update error=%v, want ErrArchived", err)
	}
	if repo.revisionReads != 0 || repo.updateCalls != 0 || provider.acquisitions != 2 || provider.lease.released != 2 {
		t.Fatalf("archived revisions=%d updates=%d acquire/release=%d/%d", repo.revisionReads, repo.updateCalls, provider.acquisitions, provider.lease.released)
	}
}

func TestDeniedMutationsDoNotReachRepository(t *testing.T) {
	for _, test := range []struct {
		name  string
		call  func(*Service, *testRepository) error
		count func(*testRepository) int
	}{
		{name: "update", call: deniedUpdate, count: func(r *testRepository) int { return r.updateCalls }},
		{name: "duplicate", call: deniedDuplicate, count: func(r *testRepository) int { return r.duplicateCalls }},
		{name: "archive", call: deniedArchive, count: func(r *testRepository) int { return r.archiveCalls }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := seededRepository(t)
			provider := &testProvider{}
			authorizer := &testAuthorizer{deny: true}
			service := mustService(t, repo, provider, authorizer, nil)
			if err := test.call(service, repo); !errors.Is(err, saved.ErrNotFound) {
				t.Fatalf("denied %s error=%v", test.name, err)
			}
			if test.count(repo) != 0 || repo.revisionReads != 0 || provider.acquisitions != 1 || provider.lease.released != 1 {
				t.Fatalf("denied %s repository/revisions/acquire=%d/%d/%d", test.name, test.count(repo), repo.revisionReads, provider.acquisitions)
			}
		})
	}
}

func deniedUpdate(service *Service, repo *testRepository) error {
	request := saved.UpdateVersionRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Title: "Denied", Slug: "denied", Visibility: saved.VisibilityPrivate, Spec: testSpec(), Evidence: validEvidenceForTest(saved.MutationActionUpdate, "denied-update")}
	request.Evidence.Fingerprint = mustUpdateFingerprintForTest(request)
	_, err := service.UpdateVersion(context.Background(), request)
	return err
}

func deniedDuplicate(service *Service, repo *testRepository) error {
	request := saved.DuplicateRequest{ProjectID: repo.lifecycle.ProjectID, SourceID: repo.lifecycle.ID, ExpectedSourceRevision: repo.revision.Token(), ID: "denied-copy", ActorID: "owner", Title: "Denied", Slug: "denied-copy", Visibility: saved.VisibilityPrivate, Evidence: validEvidenceForTest(saved.MutationActionDuplicate, "denied-duplicate")}
	request.Evidence.Fingerprint = mustDuplicateFingerprintForTest(request)
	_, err := service.Duplicate(context.Background(), request)
	return err
}

func deniedArchive(service *Service, repo *testRepository) error {
	request := saved.ArchiveRequest{ProjectID: repo.lifecycle.ProjectID, ID: repo.lifecycle.ID, ActorID: "owner", ExpectedRevision: repo.revision.Token(), Evidence: validEvidenceForTest(saved.MutationActionArchive, "denied-archive")}
	request.Evidence.Fingerprint = mustArchiveFingerprintForTest(request)
	_, err := service.Archive(context.Background(), request)
	return err
}

func replayDestination(t *testing.T, id saved.ExplorationID, evidence saved.MutationEvidence) (saved.MutationReplayMetadata, saved.Revision) {
	t.Helper()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := saved.NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:sales", "production", "generation-replay")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := saved.NewRevision("revision-replay", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	value, err := saved.NewSavedExploration(saved.NewInput{ProjectID: "project:sales", ID: id, OwnerPrincipalID: "owner", Title: "Replay", Slug: "replay", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision})
	if err != nil {
		t.Fatal(err)
	}
	return saved.MutationReplayMetadata{Lifecycle: value.Lifecycle(), AppliedRevision: revision.Token(), Evidence: evidence}, revision
}

func mustUpdateFingerprint(t *testing.T, request saved.UpdateVersionRequest) string {
	value, err := FingerprintUpdate(request)
	return mustFingerprint(t, value, err)
}

func mustDuplicateFingerprint(t *testing.T, request saved.DuplicateRequest) string {
	value, err := FingerprintDuplicate(request)
	return mustFingerprint(t, value, err)
}

func mustArchiveFingerprint(t *testing.T, request saved.ArchiveRequest) string {
	value, err := FingerprintArchive(request)
	return mustFingerprint(t, value, err)
}

func mustFingerprint(t *testing.T, value string, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func validEvidenceForTest(action saved.MutationAction, key string) saved.MutationEvidence {
	evidence, err := saved.NewMutationEvidence("owner", action, key, "sha256:0000000000000000000000000000000000000000000000000000000000000000", "request-"+key, "correlation-"+key, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		panic(err)
	}
	return evidence
}

func mustFingerprintForTest(value string, err error) string {
	if err != nil {
		panic(err)
	}
	return value
}

func mustUpdateFingerprintForTest(request saved.UpdateVersionRequest) string {
	value, err := FingerprintUpdate(request)
	return mustFingerprintForTest(value, err)
}

func mustDuplicateFingerprintForTest(request saved.DuplicateRequest) string {
	value, err := FingerprintDuplicate(request)
	return mustFingerprintForTest(value, err)
}

func mustArchiveFingerprintForTest(request saved.ArchiveRequest) string {
	value, err := FingerprintArchive(request)
	return mustFingerprintForTest(value, err)
}

func mustService(t *testing.T, repo *testRepository, provider *testProvider, authorizer *testAuthorizer, executor LeaseBoundExecutor) *Service {
	t.Helper()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	service, err := NewService(Options{Repository: repo, Runtime: provider, Authorizer: authorizer, Executor: executor, Now: func() time.Time { return now }, NewRevisionID: func() (saved.RevisionID, error) { return "revision-service", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seededRepository(t *testing.T) *testRepository {
	return seededRepositoryWithSpec(t, testSpec())
}

func seededRepositoryWithSpec(t *testing.T, spec exploration.ExplorationSpec) *testRepository {
	t.Helper()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := saved.NewExplorationSpecPayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := projectgraph.NewServingIdentity("project:sales", "production", "generation-old")
	revision, err := saved.NewRevision("revision-old", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	exploration, err := saved.NewSavedExploration(saved.NewInput{ProjectID: "project:sales", ID: "exploration-1", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision})
	if err != nil {
		t.Fatal(err)
	}
	return &testRepository{lifecycle: exploration.Lifecycle(), revision: revision}
}

func testSpec() exploration.ExplorationSpec {
	dataset := "orders"
	return exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
}

func testModel() *semanticmodel.Model {
	return &semanticmodel.Model{Tables: map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate"}}}
}

func validEvidence(t *testing.T, action saved.MutationAction, key string) saved.MutationEvidence {
	t.Helper()
	evidence, err := saved.NewMutationEvidence("owner", action, key, "sha256:0000000000000000000000000000000000000000000000000000000000000000", "request-"+key, "correlation-"+key, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}
