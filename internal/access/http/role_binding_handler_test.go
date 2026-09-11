package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/go-chi/chi/v5"
)

type roleBindingPolicyRepositoryStub struct {
	access.Repository
	policy      access.AuthorizationPolicy
	readScope   access.AuthorizationPolicyScope
	input       access.AuthorizationRoleBindingInput
	audit       access.AuditEventInput
	writerCalls int
	directCalls int
}

func (s *roleBindingPolicyRepositoryStub) AuthorizationPolicy(_ context.Context, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	s.readScope = scope
	return s.policy, nil
}

func (s *roleBindingPolicyRepositoryStub) AuthorizationPolicyRevision(context.Context, access.AuthorizationPolicyScope, int64) (access.AuthorizationPolicy, error) {
	return s.policy, nil
}

func (s *roleBindingPolicyRepositoryStub) UpsertAuthorizationRoleBinding(_ context.Context, _ access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	s.directCalls++
	return access.AuthorizationPolicy{}, errors.New("policy mutation must use the audited transaction")
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

type roleBindingPolicyTransactionStub struct {
	access.Repository
	parent *roleBindingPolicyRepositoryStub
}

func (tx *roleBindingPolicyTransactionStub) UpsertAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	return tx.parent.applyRoleBinding(input)
}

func (s *roleBindingPolicyRepositoryStub) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(&roleBindingPolicyTransactionStub{Repository: s.Repository, parent: s})
	if err == nil {
		s.audit = event
	}
	return err
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
	if got, want := recorder.Header().Get("Location"), "/api/v1/projects/project_demo/role-bindings/binding-1"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if repo.writerCalls != 1 {
		t.Fatalf("writer calls = %d, want 1", repo.writerCalls)
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
}

func TestProjectRoleBindingLocationEscapesResourceIdentifiers(t *testing.T) {
	if got, want := projectRoleBindingLocation("project_demo", "binding/one?two"), "/api/v1/projects/project_demo/role-bindings/binding%2Fone%3Ftwo"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
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
