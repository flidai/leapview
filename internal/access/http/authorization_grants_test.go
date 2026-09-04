package http

import (
	"context"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// grantControlStoreFake is intentionally a call-recording fake. These tests
// exercise the HTTP authority boundary without coupling it to a SQL dialect.
type grantControlStoreFake struct {
	grants map[string]access.ControlGrant
	rows   []access.ControlGrant

	listInstance   string
	listProject    string
	getInstance    string
	getID          string
	revokeInstance string
	revokeID       string
	revokeRevision int64
	revokeActor    string

	createdInput *access.ControlGrantInput
	updatedInput *access.ControlGrantInput
	createResult access.ControlGrant
	updateResult access.ControlGrant
	revokeResult access.ControlGrant
	createErr    error
	updateErr    error
	listErr      error
	getErr       error
	revokeErr    error
}

func (f *grantControlStoreFake) InitializeControlState(context.Context, access.ControlStateSeed, projectgraph.ProjectGraph) (access.ControlState, error) {
	return access.ControlState{}, nil
}
func (f *grantControlStoreFake) RoleAssignment(context.Context, string, string) (access.RoleAssignment, error) {
	return access.RoleAssignment{}, nil
}
func (f *grantControlStoreFake) ListRoleAssignments(context.Context, string) ([]access.RoleAssignment, error) {
	return nil, nil
}
func (f *grantControlStoreFake) CreateRoleAssignment(context.Context, access.RoleAssignmentInput) (access.RoleAssignment, error) {
	return access.RoleAssignment{}, nil
}
func (f *grantControlStoreFake) UpdateRoleAssignment(context.Context, access.RoleAssignmentInput) (access.RoleAssignment, error) {
	return access.RoleAssignment{}, nil
}
func (f *grantControlStoreFake) RevokeRoleAssignment(context.Context, string, string, int64, string) (access.RoleAssignment, error) {
	return access.RoleAssignment{}, nil
}
func (f *grantControlStoreFake) DeleteRoleAssignment(context.Context, string, string, int64, string) (access.RoleAssignment, error) {
	return access.RoleAssignment{}, nil
}

func (f *grantControlStoreFake) ControlGrant(_ context.Context, instanceID, grantID string) (access.ControlGrant, error) {
	f.getInstance, f.getID = instanceID, grantID
	if f.getErr != nil {
		return access.ControlGrant{}, f.getErr
	}
	if row, ok := f.grants[grantID]; ok {
		return row, nil
	}
	return access.ControlGrant{}, access.ErrControlNotFound
}

func (f *grantControlStoreFake) ListControlGrants(_ context.Context, instanceID, projectID string) ([]access.ControlGrant, error) {
	f.listInstance, f.listProject = instanceID, projectID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]access.ControlGrant(nil), f.rows...), nil
}

func (f *grantControlStoreFake) CreateGrant(_ context.Context, input access.ControlGrantInput, _ projectgraph.ProjectGraph) (access.ControlGrant, error) {
	copy := input
	f.createdInput = &copy
	if f.createErr != nil {
		return access.ControlGrant{}, f.createErr
	}
	if f.createResult.ID != "" {
		return f.createResult, nil
	}
	return access.ControlGrant{ID: "grant-created", InstanceID: input.InstanceID, ProjectID: input.ProjectID, Subject: input.Subject, Resource: input.Resource, Capability: input.Capability, Revision: 1, CreatedAt: "2026-09-04T00:00:00Z"}, nil
}

func (f *grantControlStoreFake) UpdateGrant(_ context.Context, input access.ControlGrantInput, _ projectgraph.ProjectGraph) (access.ControlGrant, error) {
	copy := input
	f.updatedInput = &copy
	if f.updateErr != nil {
		return access.ControlGrant{}, f.updateErr
	}
	if f.updateResult.ID != "" {
		return f.updateResult, nil
	}
	return access.ControlGrant{ID: input.ID, InstanceID: input.InstanceID, ProjectID: input.ProjectID, Subject: input.Subject, Resource: input.Resource, Capability: input.Capability, Revision: input.ExpectedRevision + 1, CreatedAt: "2026-09-04T00:00:00Z"}, nil
}

func (f *grantControlStoreFake) RevokeGrant(_ context.Context, instanceID, grantID string, revision int64, actorID string) (access.ControlGrant, error) {
	f.revokeInstance, f.revokeID, f.revokeRevision, f.revokeActor = instanceID, grantID, revision, actorID
	if f.revokeErr != nil {
		return access.ControlGrant{}, f.revokeErr
	}
	if f.revokeResult.ID != "" {
		return f.revokeResult, nil
	}
	return access.ControlGrant{ID: grantID, InstanceID: instanceID, Revision: revision + 1}, nil
}

func (f *grantControlStoreFake) ReactivateGrant(context.Context, string, string, int64, projectgraph.ProjectGraph, string) (access.ControlGrant, error) {
	return access.ControlGrant{}, nil
}
func (f *grantControlStoreFake) ControlState(context.Context, string) (access.ControlState, error) {
	return access.ControlState{}, nil
}

func TestGrantCreateRequiresIdempotencyAndCarriesInstanceAuthority(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	body := grantBody("dashboard_main", "principal", "principal-reader", "RESOURCE_READ")

	missingKey := httptest.NewRecorder()
	handler.CreateGrant(missingKey, httptest.NewRequest(stdhttp.MethodPost, "/api/v1/grants", strings.NewReader(body)))
	if missingKey.Code != stdhttp.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key status = %d, want %d", missingKey.Code, stdhttp.StatusBadRequest)
	}
	if store.createdInput != nil {
		t.Fatal("missing Idempotency-Key reached control store")
	}

	store.createResult = grantRow(t, "grant-created", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 7, access.ControlReferenceActive)
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/grants", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "grant-create-1")
	response := httptest.NewRecorder()
	handler.CreateGrant(response, request)
	if response.Code != stdhttp.StatusCreated {
		t.Fatalf("create status = %d body=%s, want %d", response.Code, response.Body.String(), stdhttp.StatusCreated)
	}
	if got, want := response.Header().Get("ETag"), `"revision-7"`; got != want {
		t.Fatalf("create ETag = %q, want %q", got, want)
	}
	if got, want := response.Header().Get("Location"), "/api/v1/grants/grant-created"; got != want {
		t.Fatalf("create Location = %q, want %q", got, want)
	}
	if store.createdInput == nil || store.createdInput.InstanceID != "instance-live" || store.createdInput.ProjectID != "project_demo" || store.createdInput.ActorID != "principal-admin" {
		t.Fatalf("create input = %#v, want instance/project/actor binding", store.createdInput)
	}
}

func TestGrantCreateCompletesGeneratedTransactionalCommand(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	store.createResult = grantRow(t, "grant-created", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 1, access.ControlReferenceActive)
	contract, ok := accessgen.GetAPIGenCommandRuntimeContract("createGrant")
	if !ok {
		t.Fatal("createGrant command contract is missing")
	}
	ctx, guard, err := apigencommand.BeginInvocation(t.Context(), contract, apigencommand.Invocation{
		Surface: apigencommand.SurfaceAPI, IdempotencyKey: "grant-create-generated",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/grants", strings.NewReader(grantBody("dashboard_main", "principal", "principal-reader", "RESOURCE_READ"))).WithContext(ctx)
	request.Header.Set("Idempotency-Key", "grant-create-generated")
	response := httptest.NewRecorder()
	handler.CreateGrant(response, request)
	if response.Code != stdhttp.StatusCreated {
		t.Fatalf("generated create status = %d body=%s", response.Code, response.Body.String())
	}
	if !guard.Completed() {
		t.Fatal("generated createGrant command was not completed through its transactional contract")
	}
}

func TestGrantUpdateCompletesGeneratedConcurrencyContract(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	store.grants = map[string]access.ControlGrant{
		"grant-reader": grantRow(t, "grant-reader", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 4, access.ControlReferenceActive),
	}
	contract, ok := accessgen.GetAPIGenCommandRuntimeContract("updateGrant")
	if !ok {
		t.Fatal("updateGrant command contract is missing")
	}
	request := grantRouteRequest(stdhttp.MethodPatch, "/api/v1/grants/grant-reader", "grant-reader", strings.NewReader(grantBody("dashboard_main", "principal", "principal-reader", "RESOURCE_EDIT")))
	ctx, guard, err := apigencommand.BeginInvocation(request.Context(), contract, apigencommand.Invocation{
		Surface: apigencommand.SurfaceAPI, TargetValues: map[string]string{"grant": "grant-reader"}, ConcurrencyToken: `"revision-4"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(ctx)
	request.Header.Set("If-Match", `"revision-4"`)
	response := httptest.NewRecorder()
	handler.UpdateGrant(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("generated update status = %d body=%s", response.Code, response.Body.String())
	}
	if !guard.Completed() {
		t.Fatal("generated updateGrant command did not complete its concurrency and transactional contract")
	}
}

func TestGrantListIncludesSuspendedRowsAndScopesInstanceAndProject(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	store.rows = []access.ControlGrant{
		grantRow(t, "grant-active", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 1, access.ControlReferenceActive),
		grantRow(t, "grant-suspended", "dashboard_main", "principal-reader", access.CapabilityResourceEdit, 2, access.ControlReferenceSuspended),
	}
	response := httptest.NewRecorder()
	handler.ListGrants(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/grants", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("list status = %d body=%s", response.Code, response.Body.String())
	}
	var body accessgen.GrantListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].Id != "grant-active" || body.Items[1].Id != "grant-suspended" {
		t.Fatalf("list items = %#v, want active and suspended grants", body.Items)
	}
	if store.listInstance != "instance-live" || store.listProject != "project_demo" {
		t.Fatalf("list scope = (%q, %q), want (instance-live, project_demo)", store.listInstance, store.listProject)
	}
}

func TestGrantListRejectsUnsupportedInheritedExpansion(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	response := httptest.NewRecorder()
	handler.ListGrants(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/grants?includeInherited=true", nil))
	if response.Code != stdhttp.StatusBadRequest {
		t.Fatalf("inherited list status = %d body=%s, want %d", response.Code, response.Body.String(), stdhttp.StatusBadRequest)
	}
	if store.listInstance != "" {
		t.Fatal("unsupported inherited expansion reached the live grant store")
	}
}

func TestGrantGetUsesInstanceScopeAndRevisionETag(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	store.grants = map[string]access.ControlGrant{
		"grant-reader": grantRow(t, "grant-reader", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 4, access.ControlReferenceActive),
	}
	response := httptest.NewRecorder()
	handler.GetGrant(response, grantRouteRequest(stdhttp.MethodGet, "/api/v1/grants/grant-reader", "grant-reader", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("get status = %d body=%s", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("ETag"), `"revision-4"`; got != want {
		t.Fatalf("get ETag = %q, want %q", got, want)
	}
	if store.getInstance != "instance-live" || store.getID != "grant-reader" {
		t.Fatalf("get scope = (%q, %q), want (instance-live, grant-reader)", store.getInstance, store.getID)
	}
}

func TestGrantUpdateEnforcesIfMatchAndPropagatesImmutableRetargetRejection(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	store.grants = map[string]access.ControlGrant{
		"grant-reader": grantRow(t, "grant-reader", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 4, access.ControlReferenceActive),
	}

	stale := httptest.NewRecorder()
	staleRequest := grantRouteRequest(stdhttp.MethodPatch, "/api/v1/grants/grant-reader", "grant-reader", strings.NewReader(grantBody("dashboard_main", "principal", "principal-reader", "RESOURCE_EDIT")))
	staleRequest.Header.Set("If-Match", `"revision-3"`)
	handler.UpdateGrant(stale, staleRequest)
	if stale.Code != stdhttp.StatusPreconditionFailed {
		t.Fatalf("stale If-Match status = %d body=%s, want %d", stale.Code, stale.Body.String(), stdhttp.StatusPreconditionFailed)
	}
	if store.updatedInput != nil {
		t.Fatal("stale If-Match reached UpdateGrant")
	}

	store.updateErr = access.ErrControlIdentityConflict
	retarget := httptest.NewRecorder()
	retargetRequest := grantRouteRequest(stdhttp.MethodPatch, "/api/v1/grants/grant-reader", "grant-reader", strings.NewReader(grantBody("dashboard_other", "principal", "principal-reader", "RESOURCE_EDIT")))
	retargetRequest.Header.Set("If-Match", `"revision-4"`)
	handler.UpdateGrant(retarget, retargetRequest)
	if retarget.Code != stdhttp.StatusConflict {
		t.Fatalf("immutable retarget status = %d body=%s, want %d", retarget.Code, retarget.Body.String(), stdhttp.StatusConflict)
	}
	if store.updatedInput == nil || store.updatedInput.Resource.ID().String() != "dashboard_other" || store.updatedInput.ExpectedRevision != 4 || store.updatedInput.InstanceID != "instance-live" {
		t.Fatalf("retarget input = %#v, want original revision plus retargeted resource and instance", store.updatedInput)
	}

	store.updateErr = access.ErrControlRevisionConflict
	conflict := httptest.NewRecorder()
	conflictRequest := grantRouteRequest(stdhttp.MethodPatch, "/api/v1/grants/grant-reader", "grant-reader", strings.NewReader(grantBody("dashboard_main", "principal", "principal-reader", "RESOURCE_EDIT")))
	conflictRequest.Header.Set("If-Match", `"revision-4"`)
	handler.UpdateGrant(conflict, conflictRequest)
	if conflict.Code != stdhttp.StatusPreconditionFailed {
		t.Fatalf("store revision conflict status = %d body=%s, want %d", conflict.Code, conflict.Body.String(), stdhttp.StatusPreconditionFailed)
	}
}

func TestGrantDeleteUsesCurrentRevisionAndInstanceScope(t *testing.T) {
	handler, store := grantHandlerFixture(t)
	store.grants = map[string]access.ControlGrant{
		"grant-reader": grantRow(t, "grant-reader", "dashboard_main", "principal-reader", access.CapabilityResourceRead, 9, access.ControlReferenceActive),
	}
	response := httptest.NewRecorder()
	handler.DeleteGrant(response, grantRouteRequest(stdhttp.MethodDelete, "/api/v1/grants/grant-reader", "grant-reader", nil))
	if response.Code != stdhttp.StatusNoContent {
		t.Fatalf("delete status = %d body=%s, want %d", response.Code, response.Body.String(), stdhttp.StatusNoContent)
	}
	if store.getInstance != "instance-live" || store.revokeInstance != "instance-live" || store.revokeID != "grant-reader" || store.revokeRevision != 9 || store.revokeActor != "principal-admin" {
		t.Fatalf("delete calls = get(%q) revoke(%q,%q,%d,%q), want instance/revision/actor binding", store.getInstance, store.revokeInstance, store.revokeID, store.revokeRevision, store.revokeActor)
	}
}

func grantHandlerFixture(t *testing.T) (Handler, *grantControlStoreFake) {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: projectgraph.KindDashboard, Name: "main"},
		{ID: "dashboard_other", Kind: projectgraph.KindDashboard, Name: "other"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &grantControlStoreFake{}
	return Handler{
		InstanceID: "instance-live", Control: store,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-admin"}, true
		},
		AuthorizationSnapshot: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
	}, store
}

func grantRow(t *testing.T, id, resourceID, subjectID string, capability access.Capability, revision int64, lifecycle access.ControlReferenceLifecycle) access.ControlGrant {
	t.Helper()
	resource, err := access.NewResourceRef(projectgraph.ResourceID(resourceID), projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, subjectID)
	if err != nil {
		t.Fatal(err)
	}
	return access.ControlGrant{ID: id, InstanceID: "instance-live", ProjectID: "project_demo", Subject: subject, Resource: resource, Capability: capability, Revision: revision, CreatedAt: "2026-09-04T00:00:00Z", ReferenceLifecycle: lifecycle}
}

func grantBody(resourceID, subjectType, subjectID, capability string) string {
	return `{"resourceKind":"dashboard","resourceId":"` + resourceID + `","subjectType":"` + subjectType + `","subjectId":"` + subjectID + `","capability":"` + capability + `"}`
}

func grantRouteRequest(method, path, grantID string, body io.Reader) *stdhttp.Request {
	request := httptest.NewRequest(method, path, body)
	route := chi.NewRouteContext()
	route.URLParams.Add("grant", grantID)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
}

var _ access.ControlStore = (*grantControlStoreFake)(nil)
