package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type savedExplorationServiceStub struct {
	fixture           savedExplorationHTTPFixture
	create            func(context.Context, saved.CreateRequest) (saved.MutationResult, error)
	reopen            func(context.Context, saved.ReopenRequest) (saved.ReopenResult, error)
	list              func(context.Context, saved.ListRequest) ([]saved.Lifecycle, error)
	update            func(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error)
	duplicate         func(context.Context, saved.DuplicateRequest) (saved.MutationResult, error)
	archive           func(context.Context, saved.ArchiveRequest) (saved.MutationResult, error)
	authorizeReplay   func(context.Context, savedapplication.MutationReplayAuthorizationRequest) (bool, error)
	createRequest     saved.CreateRequest
	createRequests    []saved.CreateRequest
	updateRequest     saved.UpdateVersionRequest
	duplicateRequest  saved.DuplicateRequest
	duplicateRequests []saved.DuplicateRequest
}

func (stub *savedExplorationServiceStub) AuthorizeMutationReplay(ctx context.Context, request savedapplication.MutationReplayAuthorizationRequest) (bool, error) {
	if stub.authorizeReplay != nil {
		return stub.authorizeReplay(ctx, request)
	}
	return false, nil
}

func (stub *savedExplorationServiceStub) Create(ctx context.Context, request saved.CreateRequest) (saved.MutationResult, error) {
	stub.createRequest = request
	stub.createRequests = append(stub.createRequests, request)
	if stub.create != nil {
		return stub.create(ctx, request)
	}
	return stub.fixture.result, nil
}

func (stub *savedExplorationServiceStub) Reopen(ctx context.Context, request saved.ReopenRequest) (saved.ReopenResult, error) {
	if stub.reopen != nil {
		return stub.reopen(ctx, request)
	}
	return stub.fixture.reopen, nil
}

func (stub *savedExplorationServiceStub) List(ctx context.Context, request saved.ListRequest) ([]saved.Lifecycle, error) {
	if stub.list != nil {
		return stub.list(ctx, request)
	}
	return []saved.Lifecycle{stub.fixture.lifecycle}, nil
}

func (stub *savedExplorationServiceStub) ListPage(ctx context.Context, request saved.ListRequest) (saved.ListPage, error) {
	items, err := stub.List(ctx, request)
	if err != nil {
		return saved.ListPage{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID.String() < items[j].ID.String() })
	start, err := saved.DecodeListCursor(request.PageToken, request.ProjectID, request.IncludeArchived)
	if err != nil {
		return saved.ListPage{}, err
	}
	if start != "" {
		items = loAfterSavedID(items, start)
	}
	if request.Limit > 0 && len(items) > request.Limit {
		next, err := saved.EncodeListCursor(request.ProjectID, request.IncludeArchived, items[request.Limit-1].ID)
		if err != nil {
			return saved.ListPage{}, err
		}
		return saved.ListPage{Items: items[:request.Limit], NextCursor: next}, nil
	}
	return saved.ListPage{Items: items}, nil
}

func loAfterSavedID(items []saved.Lifecycle, start saved.ExplorationID) []saved.Lifecycle {
	for index, item := range items {
		if item.ID.String() > start.String() {
			return items[index:]
		}
	}
	return nil
}

func (stub *savedExplorationServiceStub) UpdateVersion(ctx context.Context, request saved.UpdateVersionRequest) (saved.MutationResult, error) {
	stub.updateRequest = request
	if stub.update != nil {
		return stub.update(ctx, request)
	}
	return stub.fixture.result, nil
}

func (stub *savedExplorationServiceStub) Duplicate(ctx context.Context, request saved.DuplicateRequest) (saved.MutationResult, error) {
	stub.duplicateRequest = request
	stub.duplicateRequests = append(stub.duplicateRequests, request)
	if stub.duplicate != nil {
		return stub.duplicate(ctx, request)
	}
	return stub.fixture.result, nil
}

func (stub *savedExplorationServiceStub) Archive(ctx context.Context, request saved.ArchiveRequest) (saved.MutationResult, error) {
	if stub.archive != nil {
		return stub.archive(ctx, request)
	}
	return stub.fixture.result, nil
}

type savedExplorationHTTPFixture struct {
	spec      canonical.ExplorationSpec
	lifecycle saved.Lifecycle
	revision  saved.Revision
	result    saved.MutationResult
	reopen    saved.ReopenResult
}

func newSavedExplorationHTTPFixture(t *testing.T) savedExplorationHTTPFixture {
	t.Helper()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	spec := canonical.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []canonical.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []canonical.ExplorationFilter{},
		Sort:          []canonical.ExplorationSort{},
		Limit:         100,
	}
	payload, err := saved.NewExplorationSpecPayload(spec)
	if err != nil {
		t.Fatalf("new payload: %v", err)
	}
	identity, err := projectgraph.NewServingIdentity("project:sales", "production", "generation-1")
	if err != nil {
		t.Fatalf("serving identity: %v", err)
	}
	revision, err := saved.NewRevision("revision-1", 1, now, "principal-1", payload, identity)
	if err != nil {
		t.Fatalf("new revision: %v", err)
	}
	lifecycle := saved.Lifecycle{
		ProjectID: "project:sales", ID: "exploration-1", OwnerPrincipalID: "principal-1",
		Title: "Orders", Slug: "orders", Visibility: saved.VisibilityPrivate,
		SemanticModelID: "semantic:sales", Status: saved.StatusActive, CreatedAt: now, UpdatedAt: now,
		CurrentRevision: revision.Metadata,
	}
	return savedExplorationHTTPFixture{
		spec: spec, lifecycle: lifecycle, revision: revision,
		result: saved.MutationResult{Lifecycle: lifecycle, Revision: &revision, AppliedRevision: revision.Token(), ConcurrencyRevision: revision.Token()},
		reopen: saved.ReopenResult{Lifecycle: lifecycle, Revision: revision.Metadata, Spec: spec},
	}
}

func savedExplorationHTTPHandler(stub SavedExplorationService, principal string, authenticated bool) savedExplorationAPIHandler {
	return savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service:          stub,
		CurrentPrincipal: func(*http.Request) (string, bool) { return principal, authenticated },
	}}
}

func savedExplorationHTTPRequest(method, path string, body any) *http.Request {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func savedExplorationProblemCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem response: %v (%s)", err, recorder.Body.String())
	}
	return problem.Code
}

func TestSavedExplorationHTTPCreateDerivesActorIDAndEvidence(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	expectedID, err := stableSavedID("exploration-", "project:sales", "principal-1", "create-1", "create")
	if err != nil {
		t.Fatal(err)
	}
	result := fixture.result
	result.Lifecycle.ID = saved.ExplorationID(expectedID)
	stub := &savedExplorationServiceStub{fixture: fixture, create: func(context.Context, saved.CreateRequest) (saved.MutationResult, error) {
		return result, nil
	}}
	handler := savedExplorationHTTPHandler(stub, "principal-1", true)
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations", map[string]any{
		"title": "Orders", "visibility": "private", "spec": fixture.spec,
	})
	recorder := httptest.NewRecorder()
	request.Header.Set("X-Request-ID", "request-http-1")
	request.Header.Set("X-Correlation-ID", "correlation-http-1")
	handler.Create(recorder, request, "project:sales", analyticsgen.GenCreateSavedExplorationHeaders{IdempotencyKey: "create-1"})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", recorder.Code, recorder.Body.String())
	}
	if stub.createRequest.ID.String() != expectedID {
		t.Fatalf("derived id = %q, want %q", stub.createRequest.ID, expectedID)
	}
	if stub.createRequest.ActorID != "principal-1" || stub.createRequest.Evidence.ActorID != "principal-1" {
		t.Fatalf("actor derivation = request %q evidence %q", stub.createRequest.ActorID, stub.createRequest.Evidence.ActorID)
	}
	if stub.createRequest.Evidence.IdempotencyKey != "create-1" || stub.createRequest.Evidence.Fingerprint == "" {
		t.Fatalf("mutation evidence = %#v", stub.createRequest.Evidence)
	}
	if got, want := recorder.Header().Get("ETag"), revisionETag(fixture.result.AppliedRevision); got != want {
		t.Fatalf("create ETag = %q, want %q", got, want)
	}
	if stub.createRequest.Evidence.Action != saved.MutationActionCreate || stub.createRequest.Evidence.Version != saved.MutationEvidenceVersion ||
		stub.createRequest.Evidence.RequestID != "request-http-1" || stub.createRequest.Evidence.CorrelationID != "correlation-http-1" {
		t.Fatalf("derived request evidence = %#v", stub.createRequest.Evidence)
	}
	var response analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !reflect.DeepEqual(response.Spec, fixture.spec) {
		t.Fatalf("create returned spec = %#v, want %#v", response.Spec, fixture.spec)
	}
	if got, want := recorder.Header().Get("Location"), "/api/v1/projects/project:sales/saved-explorations/"+expectedID; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestSavedExplorationHTTPCreateIdempotencyDerivesStableIdentityAndFingerprint(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	newRequest := func(title string) *http.Request {
		return savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations", map[string]any{
			"title": title, "visibility": "private", "spec": fixture.spec,
		})
	}
	invoke := func(t *testing.T, stub *savedExplorationServiceStub, request *http.Request) {
		t.Helper()
		recorder := httptest.NewRecorder()
		savedExplorationHTTPHandler(stub, "principal-1", true).Create(recorder, request, "project:sales", analyticsgen.GenCreateSavedExplorationHeaders{IdempotencyKey: "same-key"})
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create status = %d (%s)", recorder.Code, recorder.Body.String())
		}
	}

	same := &savedExplorationServiceStub{fixture: fixture}
	invoke(t, same, newRequest("Orders"))
	invoke(t, same, newRequest("Orders"))
	if len(same.createRequests) != 2 {
		t.Fatalf("captured create requests = %d, want 2", len(same.createRequests))
	}
	if same.createRequests[0].ID != same.createRequests[1].ID || same.createRequests[0].Evidence.Fingerprint != same.createRequests[1].Evidence.Fingerprint {
		t.Fatalf("same-key retry identity changed: first=%#v second=%#v", same.createRequests[0], same.createRequests[1])
	}

	changed := &savedExplorationServiceStub{fixture: fixture}
	invoke(t, changed, newRequest("Orders"))
	invoke(t, changed, newRequest("Orders Revised"))
	if changed.createRequests[0].ID != changed.createRequests[1].ID {
		t.Fatalf("changed-body retry ID changed: first=%q second=%q", changed.createRequests[0].ID, changed.createRequests[1].ID)
	}
	if changed.createRequests[0].Evidence.Fingerprint == changed.createRequests[1].Evidence.Fingerprint {
		t.Fatal("changed-body retry fingerprint did not change")
	}
}

func TestSavedExplorationRESTReplayReauthorizesAndNeverDispatchesMutation(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	var allowed atomic.Bool
	allowed.Store(true)
	stub := &savedExplorationServiceStub{fixture: fixture, authorizeReplay: func(_ context.Context, request savedapplication.MutationReplayAuthorizationRequest) (bool, error) {
		if request.Action != saved.MutationActionCreate || request.ProjectID.String() != "project:sales" || request.ActorID != "principal-1" || request.IdempotencyKey != "rest-replay-1" || request.Fingerprint == "" {
			return false, errors.New("unexpected replay authorization request")
		}
		return allowed.Load(), nil
	}}
	handler := savedExplorationHTTPHandler(stub, "principal-1", true)
	handler.config.ReplayContext = func(ctx context.Context, _ *http.Request, actor string) (context.Context, bool) {
		return ctx, actor == "principal-1"
	}
	protocol, err := apiprotocol.Build(t.Context(), apiprotocol.Config{
		BearerToken:   func(*http.Request) string { return "credential" },
		AcceptsBearer: func(*http.Request) bool { return true },
		PrincipalID:   func(*http.Request) (string, bool) { return "principal-1", true },
		ReplayAuthorize: func(request *http.Request) bool {
			return AuthorizeSavedExplorationMutationReplay(handler.config, request)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var dispatches atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		dispatches.Add(1)
		handler.Create(w, request, "project:sales", analyticsgen.GenCreateSavedExplorationHeaders{IdempotencyKey: request.Header.Get("Idempotency-Key")})
	})
	serve := protocol.Middleware(next)
	request := func() *httptest.ResponseRecorder {
		req := savedExplorationHTTPRequest(http.MethodPost, "/api/v1/projects/project:sales/saved-explorations", map[string]any{
			"title": "Orders", "visibility": "private", "spec": fixture.spec,
		})
		req.Header.Set("Authorization", "Bearer credential")
		req.Header.Set("Idempotency-Key", "rest-replay-1")
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		return rec
	}
	first := request()
	if first.Code != http.StatusCreated || first.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("first response = %d headers=%#v body=%s", first.Code, first.Header(), first.Body.String())
	}
	if dispatches.Load() != 1 {
		t.Fatalf("first dispatches = %d, want 1", dispatches.Load())
	}
	replayed := request()
	if replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" || replayed.Body.String() != first.Body.String() {
		t.Fatalf("authorized replay = %d headers=%#v body=%s", replayed.Code, replayed.Header(), replayed.Body.String())
	}
	if dispatches.Load() != 1 {
		t.Fatalf("dispatches after authorized replay = %d, want 1", dispatches.Load())
	}
	allowed.Store(false)
	denied := request()
	if denied.Code != http.StatusForbidden || denied.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("revoked replay = %d headers=%#v body=%s", denied.Code, denied.Header(), denied.Body.String())
	}
	if dispatches.Load() != 1 {
		t.Fatalf("dispatches after denied replay = %d, want 1", dispatches.Load())
	}
}

func TestSavedExplorationReplayAuthorizationReconstructsMutationIdentity(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	const (
		project = "project:sales"
		actor   = "principal-1"
	)
	expectedRevision := fixture.revision.Token()

	createSlug := "orders-created"
	createBody := analyticsgen.GenCreateSavedExplorationBody{
		Slug: &createSlug, Spec: fixture.spec, Title: "Orders Created",
		Visibility: analyticsgen.SavedExplorationVisibilityOrganization,
	}
	createID, err := stableSavedID("exploration-", project, actor, "replay-create", "create")
	if err != nil {
		t.Fatal(err)
	}
	createFingerprint, err := savedapplication.FingerprintCreate(saved.CreateRequest{
		ProjectID: projectgraph.ResourceID(project), ID: saved.ExplorationID(createID), ActorID: actor,
		Title: createBody.Title, Slug: createSlug, Visibility: saved.VisibilityOrganization, Spec: createBody.Spec,
	})
	if err != nil {
		t.Fatal(err)
	}

	updateBody := analyticsgen.GenUpdateSavedExplorationBody{
		Slug: "orders-updated", Spec: fixture.spec, Title: "Orders Updated",
		Visibility: analyticsgen.SavedExplorationVisibilityOrganization,
	}
	updateFingerprint, err := savedapplication.FingerprintUpdate(saved.UpdateVersionRequest{
		ProjectID: projectgraph.ResourceID(project), ID: "exploration-1", ActorID: actor,
		ExpectedRevision: expectedRevision, Title: updateBody.Title, Slug: updateBody.Slug,
		Visibility: saved.VisibilityOrganization, Spec: updateBody.Spec,
	})
	if err != nil {
		t.Fatal(err)
	}

	duplicateTitle := "Orders Copy"
	duplicateSlug := "orders-copy"
	duplicateVisibility := analyticsgen.SavedExplorationVisibilityOrganization
	duplicateBody := analyticsgen.GenDuplicateSavedExplorationBody{
		Slug: &duplicateSlug, Title: &duplicateTitle, Visibility: &duplicateVisibility,
	}
	duplicateID, err := stableSavedID("exploration-", project, actor, "replay-duplicate", "duplicate:exploration-1")
	if err != nil {
		t.Fatal(err)
	}
	duplicateFingerprint, err := savedapplication.FingerprintDuplicate(saved.DuplicateRequest{
		ProjectID: projectgraph.ResourceID(project), SourceID: "exploration-1", ExpectedSourceRevision: expectedRevision,
		ID: saved.ExplorationID(duplicateID), ActorID: actor, Title: duplicateTitle, Slug: duplicateSlug,
		Visibility: saved.VisibilityOrganization,
	})
	if err != nil {
		t.Fatal(err)
	}

	archiveFingerprint, err := savedapplication.FingerprintArchive(saved.ArchiveRequest{
		ProjectID: projectgraph.ResourceID(project), ID: "exploration-1", ActorID: actor, ExpectedRevision: expectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		method  string
		path    string
		key     string
		ifMatch string
		body    any
		want    savedapplication.MutationReplayAuthorizationRequest
	}{
		{
			name: "create", method: http.MethodPost,
			path: "/api/v1/projects/" + project + "/saved-explorations", key: "replay-create", body: createBody,
			want: savedapplication.MutationReplayAuthorizationRequest{
				ProjectID: project, ActorID: actor, Action: saved.MutationActionCreate, IdempotencyKey: "replay-create",
				Fingerprint: createFingerprint, TargetID: saved.ExplorationID(createID),
			},
		},
		{
			name: "update", method: http.MethodPatch,
			path: "/api/v1/projects/" + project + "/saved-explorations/exploration-1", key: "replay-update",
			ifMatch: revisionETag(expectedRevision), body: updateBody,
			want: savedapplication.MutationReplayAuthorizationRequest{
				ProjectID: project, ActorID: actor, Action: saved.MutationActionUpdate, IdempotencyKey: "replay-update",
				Fingerprint: updateFingerprint, TargetID: "exploration-1",
			},
		},
		{
			name: "duplicate", method: http.MethodPost,
			path: "/api/v1/projects/" + project + "/saved-explorations/exploration-1/duplicate", key: "replay-duplicate",
			ifMatch: revisionETag(expectedRevision), body: duplicateBody,
			want: savedapplication.MutationReplayAuthorizationRequest{
				ProjectID: project, ActorID: actor, Action: saved.MutationActionDuplicate, IdempotencyKey: "replay-duplicate",
				Fingerprint: duplicateFingerprint, TargetID: "exploration-1",
			},
		},
		{
			name: "archive", method: http.MethodPost,
			path: "/api/v1/projects/" + project + "/saved-explorations/exploration-1/archive", key: "replay-archive",
			ifMatch: revisionETag(expectedRevision), body: nil,
			want: savedapplication.MutationReplayAuthorizationRequest{
				ProjectID: project, ActorID: actor, Action: saved.MutationActionArchive, IdempotencyKey: "replay-archive",
				Fingerprint: archiveFingerprint, TargetID: "exploration-1",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got savedapplication.MutationReplayAuthorizationRequest
			var authorizationCalls, mutationCalls atomic.Int32
			stub := &savedExplorationServiceStub{fixture: fixture, authorizeReplay: func(_ context.Context, request savedapplication.MutationReplayAuthorizationRequest) (bool, error) {
				authorizationCalls.Add(1)
				got = request
				return true, nil
			}}
			stub.create = func(context.Context, saved.CreateRequest) (saved.MutationResult, error) {
				mutationCalls.Add(1)
				return fixture.result, nil
			}
			stub.update = func(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error) {
				mutationCalls.Add(1)
				return fixture.result, nil
			}
			stub.duplicate = func(context.Context, saved.DuplicateRequest) (saved.MutationResult, error) {
				mutationCalls.Add(1)
				return fixture.result, nil
			}
			stub.archive = func(context.Context, saved.ArchiveRequest) (saved.MutationResult, error) {
				mutationCalls.Add(1)
				return fixture.result, nil
			}
			handler := savedExplorationHTTPHandler(stub, actor, true)
			handler.config.ReplayContext = func(ctx context.Context, _ *http.Request, replayActor string) (context.Context, bool) {
				return ctx, replayActor == actor
			}

			encoded, err := json.Marshal(testCase.body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(testCase.method, testCase.path, bytes.NewReader(encoded))
			request.Header.Set("Idempotency-Key", testCase.key)
			if testCase.ifMatch != "" {
				request.Header.Set("If-Match", testCase.ifMatch)
			}
			if !AuthorizeSavedExplorationMutationReplay(handler.config, request) {
				t.Fatal("replay authorization denied for valid mutation")
			}
			if authorizationCalls.Load() != 1 {
				t.Fatalf("authorization calls = %d, want 1", authorizationCalls.Load())
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("replay request = %#v, want %#v", got, testCase.want)
			}
			remaining, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(remaining, encoded) {
				t.Fatalf("request body after authorization = %q, want %q", remaining, encoded)
			}
			if mutationCalls.Load() != 0 {
				t.Fatalf("mutation calls = %d, want 0", mutationCalls.Load())
			}
		})
	}
}

func TestSavedExplorationReplayAuthorizationRejectsInvalidRouteAndETag(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	validBody := analyticsgen.GenUpdateSavedExplorationBody{
		Slug: "orders-updated", Spec: fixture.spec, Title: "Orders Updated",
		Visibility: analyticsgen.SavedExplorationVisibilityPrivate,
	}
	validPath := "/api/v1/projects/project:sales/saved-explorations/exploration-1"
	validETag := revisionETag(fixture.revision.Token())
	validFingerprint, err := savedapplication.FingerprintUpdate(saved.UpdateVersionRequest{
		ProjectID: "project:sales", ID: "exploration-1", ActorID: "principal-1", ExpectedRevision: fixture.revision.Token(),
		Title: validBody.Title, Slug: validBody.Slug, Visibility: saved.VisibilityPrivate, Spec: validBody.Spec,
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		method   string
		path     string
		key      string
		ifMatch  string
		wantCall bool
	}{
		{name: "wrong method", method: http.MethodPost, path: validPath, key: "invalid-method", ifMatch: validETag},
		{name: "wrong subresource", method: http.MethodPatch, path: validPath + "/unknown", key: "invalid-route", ifMatch: validETag},
		{name: "missing key", method: http.MethodPatch, path: validPath, ifMatch: validETag},
		{name: "weak etag", method: http.MethodPatch, path: validPath, key: "weak-etag", ifMatch: "W/" + validETag},
		{name: "empty etag", method: http.MethodPatch, path: validPath, key: "empty-etag", ifMatch: `""`},
		{name: "mismatched etag", method: http.MethodPatch, path: validPath, key: "mismatched-etag", ifMatch: revisionETag(saved.RevisionToken{RevisionID: "revision-2", Number: 2, ContentHash: fixture.revision.Metadata.ContentHash}), wantCall: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var authorizationCalls, mutationCalls atomic.Int32
			stub := &savedExplorationServiceStub{fixture: fixture, authorizeReplay: func(_ context.Context, request savedapplication.MutationReplayAuthorizationRequest) (bool, error) {
				authorizationCalls.Add(1)
				return request.Fingerprint == validFingerprint, nil
			}}
			stub.update = func(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error) {
				mutationCalls.Add(1)
				return fixture.result, nil
			}
			handler := savedExplorationHTTPHandler(stub, "principal-1", true)
			handler.config.ReplayContext = func(ctx context.Context, _ *http.Request, actor string) (context.Context, bool) {
				return ctx, actor == "principal-1"
			}
			encoded, err := json.Marshal(validBody)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(testCase.method, testCase.path, bytes.NewReader(encoded))
			request.Header.Set("Idempotency-Key", testCase.key)
			request.Header.Set("If-Match", testCase.ifMatch)
			if AuthorizeSavedExplorationMutationReplay(handler.config, request) {
				t.Fatal("invalid replay request was authorized")
			}
			wantCalls := int32(0)
			if testCase.wantCall {
				wantCalls = 1
			}
			if authorizationCalls.Load() != wantCalls {
				t.Fatalf("authorization calls = %d, want %d", authorizationCalls.Load(), wantCalls)
			}
			if mutationCalls.Load() != 0 {
				t.Fatalf("mutation calls = %d, want 0", mutationCalls.Load())
			}
		})
	}
}

func TestSavedExplorationHTTPSlugFallbackUsesStableIDForNonASCIIAndPunctuationTitles(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)

	createID, err := stableSavedID("exploration-", "project:sales", "principal-1", "unicode-title", "create")
	if err != nil {
		t.Fatal(err)
	}
	createStub := &savedExplorationServiceStub{fixture: fixture}
	createRecorder := httptest.NewRecorder()
	createRequest := savedExplorationHTTPRequest(http.MethodPost, "/api/v1/projects/project:sales/saved-explorations", map[string]any{
		"title": "日本語", "visibility": "private", "spec": fixture.spec,
	})
	savedExplorationHTTPHandler(createStub, "principal-1", true).Create(createRecorder, createRequest, "project:sales", analyticsgen.GenCreateSavedExplorationHeaders{IdempotencyKey: "unicode-title"})
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", createRecorder.Code, createRecorder.Body.String())
	}
	createSuffix := strings.TrimPrefix(createID, "exploration-")
	if createStub.createRequest.Slug != createSuffix {
		t.Fatalf("non-ASCII title slug = %q, want stable ID suffix %q", createStub.createRequest.Slug, createSuffix)
	}

	duplicateID, err := stableSavedID("exploration-", "project:sales", "principal-1", "punctuation-title", "duplicate:exploration-1")
	if err != nil {
		t.Fatal(err)
	}
	duplicateStub := &savedExplorationServiceStub{fixture: fixture}
	duplicateRecorder := httptest.NewRecorder()
	duplicateRequest := savedExplorationHTTPRequest(http.MethodPost, "/api/v1/projects/project:sales/saved-explorations/exploration-1/duplicate", map[string]any{"title": "!!!"})
	savedExplorationHTTPHandler(duplicateStub, "principal-1", true).Duplicate(duplicateRecorder, duplicateRequest, "project:sales", "exploration-1", analyticsgen.GenDuplicateSavedExplorationHeaders{IdempotencyKey: "punctuation-title", IfMatch: revisionETag(fixture.revision.Token())})
	if duplicateRecorder.Code != http.StatusCreated {
		t.Fatalf("duplicate status = %d (%s)", duplicateRecorder.Code, duplicateRecorder.Body.String())
	}
	duplicateSuffix := strings.TrimPrefix(duplicateID, "exploration-")
	if duplicateStub.duplicateRequest.Slug != duplicateSuffix {
		t.Fatalf("punctuation title slug = %q, want stable ID suffix %q", duplicateStub.duplicateRequest.Slug, duplicateSuffix)
	}
}

func TestSavedExplorationHTTPGeneratedSlugsAreBoundedAndUnique(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture}
	handler := savedExplorationHTTPHandler(stub, "principal-1", true)

	for _, key := range []string{"create-repeated-1", "create-repeated-2"} {
		recorder := httptest.NewRecorder()
		request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations", map[string]any{
			"title": "Repeated title", "visibility": "private", "spec": fixture.spec,
		})
		handler.Create(recorder, request, "project:sales", analyticsgen.GenCreateSavedExplorationHeaders{IdempotencyKey: key})
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create status = %d (%s)", recorder.Code, recorder.Body.String())
		}
	}
	if len(stub.createRequests) != 2 || stub.createRequests[0].Slug == stub.createRequests[1].Slug {
		t.Fatalf("repeated-title create slugs = %#v, want distinct values", stub.createRequests)
	}
	for _, request := range stub.createRequests {
		if len(request.Slug) == 0 || len(request.Slug) > saved.MaxSlugLength {
			t.Fatalf("generated create slug length = %d, want 1-%d", len(request.Slug), saved.MaxSlugLength)
		}
	}

	for _, key := range []string{"duplicate-repeated-1", "duplicate-repeated-2"} {
		recorder := httptest.NewRecorder()
		request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/duplicate", map[string]any{
			"title": "Repeated copy",
		})
		handler.Duplicate(recorder, request, "project:sales", "exploration-1", analyticsgen.GenDuplicateSavedExplorationHeaders{IdempotencyKey: key, IfMatch: revisionETag(fixture.revision.Token())})
		if recorder.Code != http.StatusCreated {
			t.Fatalf("duplicate status = %d (%s)", recorder.Code, recorder.Body.String())
		}
	}
	if len(stub.duplicateRequests) != 2 || stub.duplicateRequests[0].Slug == stub.duplicateRequests[1].Slug {
		t.Fatalf("repeated-title duplicate slugs = %#v, want distinct values", stub.duplicateRequests)
	}
	for _, request := range stub.duplicateRequests {
		if len(request.Slug) == 0 || len(request.Slug) > saved.MaxSlugLength {
			t.Fatalf("generated duplicate slug length = %d, want 1-%d", len(request.Slug), saved.MaxSlugLength)
		}
	}
}

func TestStableSavedIDIsProjectIsolated(t *testing.T) {
	first, err := stableSavedID("exploration-", "project:sales", "principal-1", "same-key", "create")
	if err != nil {
		t.Fatal(err)
	}
	second, err := stableSavedID("exploration-", "project:marketing", "principal-1", "same-key", "create")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("project-scoped IDs collided: %q", first)
	}
}

func TestSavedExplorationHTTPGetReopensDetachedWorkingCopy(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture}
	called := false
	stub.reopen = func(_ context.Context, request saved.ReopenRequest) (saved.ReopenResult, error) {
		called = true
		if request.ActorID != "principal-1" || request.ID.String() != "exploration-1" {
			t.Fatalf("reopen request = %#v", request)
		}
		return fixture.reopen, nil
	}
	handler := savedExplorationHTTPHandler(stub, "principal-1", true)
	recorder := httptest.NewRecorder()
	handler.Get(recorder, httptest.NewRequest(http.MethodGet, "/projects/project:sales/saved-explorations/exploration-1", nil), "project:sales", "exploration-1")

	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("GET status = %d, reopen called = %v", recorder.Code, called)
	}
	if got, want := recorder.Header().Get("ETag"), revisionETag(fixture.revision.Token()); got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
	var response analyticsgen.SavedExplorationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode working copy: %v", err)
	}
	if response.Spec.ModelID != fixture.spec.ModelID || response.Spec.Limit != fixture.spec.Limit {
		t.Fatalf("working copy spec = %#v", response.Spec)
	}
}

func TestSavedExplorationHTTPDuplicateUsesCanonicalDestinationLocationAndExactIfMatch(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	destination := fixture
	destination.lifecycle.ID = "exploration-copy"
	destination.lifecycle.Title = "Orders Copy"
	destination.lifecycle.Slug = "orders-copy"
	destination.result.Lifecycle = destination.lifecycle
	stub := &savedExplorationServiceStub{fixture: destination}
	handler := savedExplorationHTTPHandler(stub, "principal-1", true)
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/duplicate", map[string]any{"title": "Orders Copy"})
	request.Header.Set("If-Match", revisionETag(fixture.revision.Token()))
	recorder := httptest.NewRecorder()
	handler.Duplicate(recorder, request, "project:sales", "exploration-1", analyticsgen.GenDuplicateSavedExplorationHeaders{IdempotencyKey: "duplicate-1", IfMatch: revisionETag(fixture.revision.Token())})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("duplicate status = %d, want 201 (%s)", recorder.Code, recorder.Body.String())
	}
	if got, want := recorder.Header().Get("Location"), "/api/v1/projects/project:sales/saved-explorations/exploration-copy"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if stub.duplicateRequest.ActorID != "principal-1" || stub.duplicateRequest.ExpectedSourceRevision != fixture.revision.Token() {
		t.Fatalf("duplicate request = %#v", stub.duplicateRequest)
	}
	if stub.duplicateRequest.Evidence.Fingerprint == "" || stub.duplicateRequest.Evidence.IdempotencyKey != "duplicate-1" {
		t.Fatalf("duplicate evidence = %#v", stub.duplicateRequest.Evidence)
	}
}

func TestSavedExplorationHTTPMutationAttestationComparesIndependentRevision(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	validToken := revisionETag(fixture.revision.Token())
	body := map[string]any{"title": "Orders", "slug": "orders", "visibility": "private", "spec": fixture.spec}

	for _, test := range []struct {
		name       string
		concurrent saved.RevisionToken
		wantStatus int
	}{
		{name: "matching", concurrent: fixture.revision.Token(), wantStatus: http.StatusOK},
		{name: "stale mismatch", concurrent: saved.RevisionToken{RevisionID: fixture.revision.Metadata.ID, Number: 2, ContentHash: fixture.revision.Metadata.ContentHash}, wantStatus: http.StatusPreconditionFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := fixture.result
			result.ConcurrencyRevision = test.concurrent
			stub := &savedExplorationServiceStub{fixture: fixture, update: func(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error) {
				return result, nil
			}}
			recorder := httptest.NewRecorder()
			savedExplorationHTTPHandler(stub, "principal-1", true).Update(recorder, savedExplorationHTTPRequest(http.MethodPatch, "/", body), "project:sales", "exploration-1", analyticsgen.GenUpdateSavedExplorationHeaders{IdempotencyKey: "attestation-" + test.name, IfMatch: validToken})
			if recorder.Code != test.wantStatus {
				t.Fatalf("attestation status = %d, want %d (%s)", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus == http.StatusPreconditionFailed && savedExplorationProblemCode(t, recorder) != "SAVED_EXPLORATION_PRECONDITION_FAILED" {
				t.Fatalf("attestation problem = %s", recorder.Body.String())
			}
		})
	}
}

func TestSavedExplorationHTTPFailureMappingAndTransportGuards(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	validToken := revisionETag(fixture.revision.Token())
	validUpdateBody := map[string]any{"title": "Orders", "slug": "orders", "visibility": "private", "spec": fixture.spec}

	t.Run("authentication required", func(t *testing.T) {
		stub := &savedExplorationServiceStub{fixture: fixture}
		recorder := httptest.NewRecorder()
		savedExplorationHTTPHandler(stub, "", false).Get(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "project:sales", "exploration-1")
		if recorder.Code != http.StatusUnauthorized || savedExplorationProblemCode(t, recorder) != "AUTHENTICATION_REQUIRED" {
			t.Fatalf("authentication response = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("service unavailable", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true }}}.Get(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "project:sales", "exploration-1")
		if recorder.Code != http.StatusServiceUnavailable || savedExplorationProblemCode(t, recorder) != "SAVED_EXPLORATION_UNAVAILABLE" {
			t.Fatalf("unavailable response = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("malformed body has stable decode code", func(t *testing.T) {
		stub := &savedExplorationServiceStub{fixture: fixture}
		request := savedExplorationHTTPRequest(http.MethodPost, "/", map[string]any{"title": "Orders", "visibility": "private", "spec": fixture.spec, "actorId": "attacker"})
		recorder := httptest.NewRecorder()
		savedExplorationHTTPHandler(stub, "principal-1", true).Create(recorder, request, "project:sales", analyticsgen.GenCreateSavedExplorationHeaders{IdempotencyKey: "create-1"})
		if recorder.Code != http.StatusBadRequest || savedExplorationProblemCode(t, recorder) != "INVALID_REQUEST_BODY" {
			t.Fatalf("malformed body response = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("invalid If-Match does not call service", func(t *testing.T) {
		called := false
		stub := &savedExplorationServiceStub{fixture: fixture, update: func(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error) {
			called = true
			return fixture.result, nil
		}}
		recorder := httptest.NewRecorder()
		savedExplorationHTTPHandler(stub, "principal-1", true).Update(recorder, savedExplorationHTTPRequest(http.MethodPatch, "/", validUpdateBody), "project:sales", "exploration-1", analyticsgen.GenUpdateSavedExplorationHeaders{IdempotencyKey: "update-1", IfMatch: "not-a-token"})
		if recorder.Code != http.StatusPreconditionFailed || called || savedExplorationProblemCode(t, recorder) != "SAVED_EXPLORATION_PRECONDITION_FAILED" {
			t.Fatalf("invalid If-Match response = %d called=%v %s", recorder.Code, called, recorder.Body.String())
		}
	})

	for _, test := range []struct {
		name   string
		err    error
		code   int
		public string
	}{
		{name: "forbidden hidden", err: access.ErrForbidden, code: http.StatusNotFound, public: "SAVED_EXPLORATION_NOT_FOUND"},
		{name: "not found", err: saved.ErrNotFound, code: http.StatusNotFound, public: "SAVED_EXPLORATION_NOT_FOUND"},
		{name: "invalid identifier", err: saved.ErrInvalidIdentifier, code: http.StatusUnprocessableEntity, public: "INVALID_SAVED_EXPLORATION"},
		{name: "unavailable", err: saved.ErrUnavailable, code: http.StatusServiceUnavailable, public: "SAVED_EXPLORATION_UNAVAILABLE"},
		{name: "audit outbox capacity", err: access.ErrAuditOutboxCapacity, code: http.StatusServiceUnavailable, public: "SAVED_EXPLORATION_UNAVAILABLE"},
		{name: "wrapped audit recorder failure", err: errors.Join(saved.ErrUnavailable, errors.New("injected audit failure")), code: http.StatusServiceUnavailable, public: "SAVED_EXPLORATION_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &savedExplorationServiceStub{fixture: fixture, reopen: func(context.Context, saved.ReopenRequest) (saved.ReopenResult, error) {
				return saved.ReopenResult{}, test.err
			}}
			recorder := httptest.NewRecorder()
			savedExplorationHTTPHandler(stub, "principal-1", true).Get(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "project:sales", "exploration-1")
			if recorder.Code != test.code || savedExplorationProblemCode(t, recorder) != test.public {
				t.Fatalf("failure response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}

	t.Run("stale revision maps to 412", func(t *testing.T) {
		stub := &savedExplorationServiceStub{fixture: fixture, update: func(context.Context, saved.UpdateVersionRequest) (saved.MutationResult, error) {
			return saved.MutationResult{}, saved.ErrStaleRevision
		}}
		recorder := httptest.NewRecorder()
		savedExplorationHTTPHandler(stub, "principal-1", true).Update(recorder, savedExplorationHTTPRequest(http.MethodPatch, "/", validUpdateBody), "project:sales", "exploration-1", analyticsgen.GenUpdateSavedExplorationHeaders{IdempotencyKey: "update-1", IfMatch: validToken})
		if recorder.Code != http.StatusPreconditionFailed || savedExplorationProblemCode(t, recorder) != "SAVED_EXPLORATION_PRECONDITION_FAILED" {
			t.Fatalf("stale response = %d %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestSavedExplorationResponseFailsClosedOnPayloadDecodeError(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	_, err := savedExplorationResponse(fixture.lifecycle, saved.Revision{})
	if err == nil {
		t.Fatal("malformed revision payload unexpectedly produced a response")
	}
	if errors.Is(err, saved.ErrInvalidPayload) == false {
		t.Fatalf("payload decode error = %v, want invalid payload", err)
	}
}

func TestSavedExplorationHTTPListRejectsExplicitZeroLimit(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	called := false
	stub := &savedExplorationServiceStub{fixture: fixture, list: func(context.Context, saved.ListRequest) ([]saved.Lifecycle, error) {
		called = true
		return []saved.Lifecycle{fixture.lifecycle}, nil
	}}
	limit := int32(0)
	recorder := httptest.NewRecorder()
	savedExplorationHTTPHandler(stub, "principal-1", true).List(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:sales/saved-explorations?limit=0", nil), "project:sales", analyticsgen.GenListSavedExplorationsParams{Limit: &limit})
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("zero-limit response = %d called=%t body=%s, want direct 400 without service call", recorder.Code, called, recorder.Body.String())
	}
	if got := savedExplorationProblemCode(t, recorder); got != "INVALID_REQUEST" {
		t.Fatalf("zero-limit problem code = %q, want INVALID_REQUEST", got)
	}
}

func TestSavedExplorationHTTPListDefaultsAbsentLimitToFifty(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	var observed saved.ListRequest
	stub := &savedExplorationServiceStub{fixture: fixture, list: func(_ context.Context, request saved.ListRequest) ([]saved.Lifecycle, error) {
		observed = request
		return []saved.Lifecycle{fixture.lifecycle}, nil
	}}
	recorder := httptest.NewRecorder()
	savedExplorationHTTPHandler(stub, "principal-1", true).List(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:sales/saved-explorations", nil), "project:sales", analyticsgen.GenListSavedExplorationsParams{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("absent-limit response = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	if observed.Limit != saved.DefaultListLimit {
		t.Fatalf("absent-limit request = %#v, want default limit %d", observed, saved.DefaultListLimit)
	}
}

func TestSavedExplorationHTTPListUsesStableIDCursor(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	items := make([]saved.Lifecycle, 3)
	for index, id := range []saved.ExplorationID{"exploration-a", "exploration-b", "exploration-c"} {
		items[index] = fixture.lifecycle
		items[index].ID = id
		// Deliberately vary editable metadata independently from the immutable
		// identity: pagination must follow the ID order promised by the query.
		items[index].Title = string(id)
		items[index].UpdatedAt = fixture.lifecycle.UpdatedAt.Add(time.Duration(2-index) * time.Hour)
	}
	stub := &savedExplorationServiceStub{fixture: fixture, list: func(context.Context, saved.ListRequest) ([]saved.Lifecycle, error) {
		return items, nil
	}}
	handler := savedExplorationHTTPHandler(stub, "principal-1", true)
	limit := int32(2)
	first := httptest.NewRecorder()
	handler.List(first, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:sales/saved-explorations", nil), "project:sales", analyticsgen.GenListSavedExplorationsParams{Limit: &limit})
	if first.Code != http.StatusOK {
		t.Fatalf("first list status = %d (%s)", first.Code, first.Body.String())
	}
	var firstResponse analyticsgen.SavedExplorationListResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatalf("decode first list: %v", err)
	}
	if len(firstResponse.Items) != 2 || firstResponse.Items[0].Id != "exploration-a" || firstResponse.Items[1].Id != "exploration-b" || firstResponse.Page.NextCursor == nil {
		t.Fatalf("first page = %#v, want a,b and cursor", firstResponse)
	}

	second := httptest.NewRecorder()
	handler.List(second, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:sales/saved-explorations", nil), "project:sales", analyticsgen.GenListSavedExplorationsParams{Limit: &limit, PageToken: firstResponse.Page.NextCursor})
	if second.Code != http.StatusOK {
		t.Fatalf("second list status = %d (%s)", second.Code, second.Body.String())
	}
	var secondResponse analyticsgen.SavedExplorationListResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
		t.Fatalf("decode second list: %v", err)
	}
	if len(secondResponse.Items) != 1 || secondResponse.Items[0].Id != "exploration-c" || secondResponse.Page.NextCursor != nil {
		t.Fatalf("second page = %#v, want c and no cursor", secondResponse)
	}
}
