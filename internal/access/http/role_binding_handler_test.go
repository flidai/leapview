package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type roleBindingPolicyRepositoryStub struct {
	access.Repository
	policy       access.AuthorizationPolicy
	history      map[int64]access.AuthorizationPolicy
	deleteReplay map[string]access.AuthorizationPolicy
	readScope    access.AuthorizationPolicyScope
	input        access.AuthorizationRoleBindingInput
	deleteInput  access.AuthorizationRoleBindingDeleteInput
	audit        access.AuditEventInput
	writerCalls  int
	deleteCalls  int
	directCalls  int
}

func (s *roleBindingPolicyRepositoryStub) AuthorizationPolicy(_ context.Context, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	s.readScope = scope
	return s.policy, nil
}

func (s *roleBindingPolicyRepositoryStub) AuthorizationPolicyRevision(_ context.Context, _ access.AuthorizationPolicyScope, revision int64) (access.AuthorizationPolicy, error) {
	if policy, ok := s.history[revision]; ok {
		return policy, nil
	}
	return s.policy, nil
}

func (s *roleBindingPolicyRepositoryStub) UpsertAuthorizationRoleBinding(_ context.Context, _ access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	s.directCalls++
	return access.AuthorizationPolicy{}, errors.New("policy mutation must use the audited transaction")
}

func (s *roleBindingPolicyRepositoryStub) DeleteAuthorizationRoleBinding(_ context.Context, _ access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
	s.directCalls++
	return access.AuthorizationPolicy{}, errors.New("policy mutation must use the audited transaction")
}

func (s *roleBindingPolicyRepositoryStub) ListAuditEvents(_ context.Context, filter access.AuditEventFilter) ([]access.AuditEvent, error) {
	if strings.TrimSpace(filter.ProjectID) == "" || filter.ProjectID != s.audit.ProjectID {
		return nil, nil
	}
	return []access.AuditEvent{{
		ID: "audit-role-binding", ProjectID: s.audit.ProjectID, PrincipalID: s.audit.PrincipalID,
		Action: s.audit.Action, ResourceKind: s.audit.ResourceKind, ResourceID: s.audit.ResourceID,
		Capability: s.audit.Capability, Status: s.audit.Status, RequestID: s.audit.RequestID,
		CorrelationID: s.audit.CorrelationID, MetadataJSON: s.audit.MetadataJSON,
	}}, nil
}

func (s *roleBindingPolicyRepositoryStub) applyRoleBinding(input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	s.writerCalls++
	s.input = input
	s.policy.Revision = input.ExpectedRevision + 1
	s.policy.Digest = "sha256:" + strings.Repeat("a", 64)
	s.policy.Scope = input.Scope
	s.policy.RoleBindings = []access.RoleBinding{input.Binding}
	return s.policy, nil
}

func (s *roleBindingPolicyRepositoryStub) applyDelete(input access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
	s.deleteCalls++
	s.deleteInput = input
	if replay, ok := s.deleteReplay[input.IdempotencyKey]; ok {
		return replay, nil
	}
	if input.ExpectedRevision != s.policy.Revision {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: expected %d, current %d", access.ErrAuthorizationPolicyStaleRevision, input.ExpectedRevision, s.policy.Revision)
	}
	for index, binding := range s.policy.RoleBindings {
		if binding.ID != input.BindingID {
			continue
		}
		if s.history == nil {
			s.history = make(map[int64]access.AuthorizationPolicy)
		}
		if s.deleteReplay == nil {
			s.deleteReplay = make(map[string]access.AuthorizationPolicy)
		}
		s.history[s.policy.Revision] = s.policy
		s.policy.RoleBindings = append(s.policy.RoleBindings[:index], s.policy.RoleBindings[index+1:]...)
		s.policy.Revision = input.ExpectedRevision + 1
		s.policy.Digest = "sha256:" + strings.Repeat("c", 64)
		s.policy.Scope = input.Scope
		s.deleteReplay[input.IdempotencyKey] = s.policy
		return s.policy, nil
	}
	return access.AuthorizationPolicy{}, fmt.Errorf("%w: role binding %q", access.ErrAuthorizationPolicyNotFound, input.BindingID)
}

type roleBindingPolicyTransactionStub struct {
	access.Repository
	parent *roleBindingPolicyRepositoryStub
}

func (tx *roleBindingPolicyTransactionStub) AuthorizationPolicy(ctx context.Context, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	return tx.parent.AuthorizationPolicy(ctx, scope)
}

func (tx *roleBindingPolicyTransactionStub) AuthorizationPolicyRevision(ctx context.Context, scope access.AuthorizationPolicyScope, revision int64) (access.AuthorizationPolicy, error) {
	return tx.parent.AuthorizationPolicyRevision(ctx, scope, revision)
}

func (tx *roleBindingPolicyTransactionStub) UpsertAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	return tx.parent.applyRoleBinding(input)
}

func (tx *roleBindingPolicyTransactionStub) DeleteAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
	return tx.parent.applyDelete(input)
}

func (s *roleBindingPolicyRepositoryStub) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(&roleBindingPolicyTransactionStub{Repository: s.Repository, parent: s})
	if err == nil {
		s.audit = event
	}
	return err
}

func assertProjectScopedRoleBindingAudit(t *testing.T, handler Handler, action, resourceID string) {
	t.Helper()
	handler.CurrentPrincipal = func(*http.Request) (Principal, bool) {
		return Principal{ID: "principal-admin"}, true
	}
	handler.PlatformAdmin = func(context.Context, string) (bool, error) { return true, nil }
	handler.CurrentProjectID = func(context.Context) (projectgraph.ResourceID, error) { return "project_demo", nil }
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_demo/audit-events", nil)
	response := httptest.NewRecorder()
	handler.ListAuditEventsForProject(response, request, "project_demo")
	if response.Code != http.StatusOK {
		t.Fatalf("project audit status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []struct {
			ProjectID  string `json:"projectId"`
			Action     string `json:"action"`
			ResourceID string `json:"resourceId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode project audit response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ProjectID != "project_demo" || payload.Items[0].Action != action || payload.Items[0].ResourceID != resourceID {
		t.Fatalf("project audit items = %#v, want %s/%s for project_demo", payload.Items, action, resourceID)
	}
}

func TestCreateProjectRoleBindingDerivesCapabilitiesAndBindsServerScope(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{}
	handler := Handler{
		Repository:                     func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID:    "target-server",
		AuthorizationPolicyEnvironment: "prod",
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","expectedRevision":0}`))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "idem-1")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want no unresolvable item URL", got)
	}
	if repo.writerCalls != 1 {
		t.Fatalf("writer calls = %d, want 1", repo.writerCalls)
	}
	if repo.audit.ProjectID != "project_demo" {
		t.Fatalf("create audit project id = %q, want project_demo", repo.audit.ProjectID)
	}
	if repo.directCalls != 0 {
		t.Fatalf("direct writer calls = %d, want 0", repo.directCalls)
	}
	if got, want := repo.input.Scope, (access.AuthorizationPolicyScope{TargetID: "target-server", ProjectID: "project_demo", Environment: "prod"}); got != want {
		t.Fatalf("scope = %#v, want %#v", got, want)
	}
	if got, want := repo.input.IdempotencyKey, "idem-1"; got != want {
		t.Fatalf("idempotency key = %q, want %q", got, want)
	}
	wantCapabilities := access.ProjectRoleCapabilities(access.ProjectRoleViewer)
	if len(repo.input.Binding.Capabilities) != len(wantCapabilities) {
		t.Fatalf("capabilities = %#v, want %#v", repo.input.Binding.Capabilities, wantCapabilities)
	}
	for index, capability := range wantCapabilities {
		if repo.input.Binding.Capabilities[index] != capability {
			t.Fatalf("capability[%d] = %q, want %q", index, repo.input.Binding.Capabilities[index], capability)
		}
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, supplied := response["capabilities"]; !supplied {
		t.Fatalf("response omitted derived capabilities: %#v", response)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(repo.audit.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok {
		t.Fatalf("audit payload = %#v", metadata)
	}
	if payload["targetId"] != "target-server" || payload["environment"] != "prod" || payload["role"] != "viewer" {
		t.Fatalf("audit metadata = %#v", metadata)
	}
	assertProjectScopedRoleBindingAudit(t, handler, "role_binding.created", "binding-1")
}

func TestCreateProjectRoleBindingRejectsCallerCapabilities(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"principal","subjectId":"principal-1","role":"viewer","capabilities":["PROJECT_ADMIN"],"expectedRevision":0}`))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "idem-1")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 0 {
		t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
	}
}

func TestListProjectRoleBindingsUsesBoundServerScope(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{policy: access.AuthorizationPolicy{
		Revision: 2, Digest: "sha256:" + strings.Repeat("b", 64),
		RoleBindings: []access.RoleBinding{{ID: "binding-1", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}},
	}}
	handler := Handler{
		Repository:                     func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID:    "target-server",
		AuthorizationPolicyEnvironment: "prod",
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_demo/role-bindings", nil)
	request = withProjectRoute(request, "project_demo")
	recorder := httptest.NewRecorder()
	handler.ListProjectRoleBindings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got, want := repo.readScope, (access.AuthorizationPolicyScope{TargetID: "target-server", ProjectID: "project_demo", Environment: "prod"}); got != want {
		t.Fatalf("read scope = %#v, want %#v", got, want)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["targetId"] != "target-server" || response["environment"] != "prod" {
		t.Fatalf("scope metadata = %#v", response)
	}
}

func TestDeleteProjectRoleBindingUsesCASIdempotencyAndTransactionalAudit(t *testing.T) {
	binding := access.RoleBinding{ID: "binding-delete", Name: "Delete me", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}
	repo := &roleBindingPolicyRepositoryStub{policy: access.AuthorizationPolicy{Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64), RoleBindings: []access.RoleBinding{binding}}}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod"}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/role-bindings/binding-delete", strings.NewReader(`{"expectedRevision":1}`))
		request = withProjectRouteAndBinding(request, "project_demo", "binding-delete")
		request.Header.Set("Idempotency-Key", "delete-1")
		recorder := httptest.NewRecorder()
		handler.DeleteProjectRoleBinding(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("attempt %d status = %d, body = %s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if repo.deleteCalls != 2 {
		t.Fatalf("delete calls = %d, want two command invocations", repo.deleteCalls)
	}
	if len(repo.policy.RoleBindings) != 0 || repo.policy.Revision != 2 {
		t.Fatalf("policy after retry = %+v, want empty revision 2", repo.policy)
	}
	if repo.audit.Action != "role_binding.deleted" || repo.audit.ResourceID != "binding-delete" {
		t.Fatalf("audit = %+v, want role_binding.deleted for binding-delete", repo.audit)
	}
	if repo.audit.ProjectID != "project_demo" {
		t.Fatalf("delete audit project id = %q, want project_demo", repo.audit.ProjectID)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(repo.audit.MetadataJSON), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["bindingId"] != "binding-delete" || payload["policyRevision"] != float64(2) {
		t.Fatalf("audit payload = %#v, want deleted binding and successor revision", metadata)
	}
	assertProjectScopedRoleBindingAudit(t, handler, "role_binding.deleted", "binding-delete")
}

func TestDeleteProjectRoleBindingRejectsStaleRevision(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{policy: access.AuthorizationPolicy{Revision: 2, RoleBindings: []access.RoleBinding{{ID: "binding-delete"}}}}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod"}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/role-bindings/binding-delete", strings.NewReader(`{"expectedRevision":1}`))
	request = withProjectRouteAndBinding(request, "project_demo", "binding-delete")
	request.Header.Set("Idempotency-Key", "delete-stale")
	recorder := httptest.NewRecorder()
	handler.DeleteProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s; want conflict", recorder.Code, recorder.Body.String())
	}
	if repo.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want one CAS attempt", repo.deleteCalls)
	}
}

func TestDeleteProjectRoleBindingReportsMissingBinding(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{policy: access.AuthorizationPolicy{Revision: 1, RoleBindings: []access.RoleBinding{{ID: "other-binding"}}}}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod"}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/role-bindings/missing-binding", strings.NewReader(`{"expectedRevision":1}`))
	request = withProjectRouteAndBinding(request, "project_demo", "missing-binding")
	request.Header.Set("Idempotency-Key", "delete-missing")
	recorder := httptest.NewRecorder()
	handler.DeleteProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want not found", recorder.Code, recorder.Body.String())
	}
	if repo.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want no mutation for missing binding", repo.deleteCalls)
	}
}

func TestDeleteProjectRoleBindingRejectsUnconfiguredScope(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/role-bindings/binding-delete", strings.NewReader(`{"expectedRevision":1}`))
	request = withProjectRouteAndBinding(request, "project_demo", "binding-delete")
	request.Header.Set("Idempotency-Key", "delete-scope")
	recorder := httptest.NewRecorder()
	handler.DeleteProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want service unavailable", recorder.Code, recorder.Body.String())
	}
	if repo.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want no mutation for invalid scope", repo.deleteCalls)
	}
}

func withProjectRouteAndBinding(request *http.Request, project, binding string) *http.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", project)
	ctx.URLParams.Add("binding", binding)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}

func TestCreateProjectRoleBindingRequiresExpectedRevision(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"principal","subjectId":"principal-1","role":"viewer"}`))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "idem-1")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 0 {
		t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
	}
}

func TestCreateProjectRoleBindingRejectsNonCanonicalSubjectOrRole(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "subject", body: `{"id":"binding-1","subjectType":"domain","subjectId":"domain-1","role":"viewer","expectedRevision":0}`},
		{name: "role", body: `{"id":"binding-1","subjectType":"principal","subjectId":"principal-1","role":"platform_admin","expectedRevision":0}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &roleBindingPolicyRepositoryStub{}
			handler := Handler{
				Repository:                  func() (access.Repository, error) { return repo, nil },
				AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(test.body))
			request = withProjectRoute(request, "project_demo")
			request.Header.Set("Idempotency-Key", "idem-1")
			recorder := httptest.NewRecorder()
			handler.CreateProjectRoleBinding(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if repo.writerCalls != 0 {
				t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
			}
		})
	}
}

func withProjectRoute(request *http.Request, project string) *http.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", project)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}
