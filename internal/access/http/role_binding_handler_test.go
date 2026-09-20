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
	deleteInput access.AuthorizationRoleBindingDeleteInput
	audit       access.AuditEventInput
	envelope    access.GrantAdminEnvelope
	envelopeErr error
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

func (s *roleBindingPolicyRepositoryStub) RemoveAuthorizationRoleBinding(_ context.Context, _ access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
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

func (tx *roleBindingPolicyTransactionStub) CurrentGrantAdminEnvelopeForMutation(_ context.Context, id, principalID string) (access.GrantAdminEnvelope, error) {
	if tx.parent.envelopeErr != nil {
		return access.GrantAdminEnvelope{}, tx.parent.envelopeErr
	}
	if id == "" || id != tx.parent.envelope.ID || principalID != tx.parent.envelope.BoundPrincipalID {
		return access.GrantAdminEnvelope{}, access.ErrGrantNotFound
	}
	return tx.parent.envelope, nil
}

func (tx *roleBindingPolicyTransactionStub) AuthorizationPolicy(_ context.Context, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	tx.parent.readScope = scope
	return tx.parent.policy, nil
}

func (tx *roleBindingPolicyTransactionStub) AuthorizationPolicyRevision(context.Context, access.AuthorizationPolicyScope, int64) (access.AuthorizationPolicy, error) {
	return tx.parent.policy, nil
}

func (tx *roleBindingPolicyTransactionStub) RemoveAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
	tx.parent.writerCalls++
	tx.parent.deleteInput = input
	next := make([]access.RoleBinding, 0, len(tx.parent.policy.RoleBindings))
	for _, binding := range tx.parent.policy.RoleBindings {
		if binding.ID != input.BindingID {
			next = append(next, binding)
		}
	}
	tx.parent.policy.Scope = input.Scope
	tx.parent.policy.Revision = input.ExpectedRevision + 1
	tx.parent.policy.Digest = "sha256:" + strings.Repeat("c", 64)
	tx.parent.policy.RoleBindings = next
	return tx.parent.policy, nil
}

func (s *roleBindingPolicyRepositoryStub) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(&roleBindingPolicyTransactionStub{Repository: s.Repository, parent: s})
	if err == nil {
		s.audit = event
	}
	return err
}

func TestCreateProjectRoleBindingCapturesTypedRoleExpansionAndBindsServerScope(t *testing.T) {
	wantPermissions, err := access.ExpandPermissionRole(access.PermissionRoleViewer, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	repo := &roleBindingPolicyRepositoryStub{envelope: access.GrantAdminEnvelope{
		ID: "envelope-1", BoundPrincipalID: "actor-1", TargetProjectID: "project_demo",
		Issuer: access.GrantIssuerEvidence{PrincipalID: "actor-1", Credential: access.GrantCredentialEvidence{
			Class: access.GrantCredentialClassAPIToken, ID: "token-1", Fingerprint: "fingerprint-token-1",
		}},
		RecipientSelector: "group:group-1", RoleVersion: access.PermissionRoleVersion(access.PermissionRoleViewer),
		Permissions: wantPermissions,
	}}
	handler := Handler{
		Repository:                     func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID:    "target-server",
		AuthorizationPolicyEnvironment: "prod",
		CurrentPrincipal:               func(*http.Request) (Principal, bool) { return Principal{ID: "actor-1"}, true },
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return append([]access.PermissionPair{manage, delegate}, wantPermissions...), nil
		},
		CurrentCredential: func(*http.Request) (access.APICredential, bool) {
			return access.APICredential{Principal: access.Principal{ID: "actor-1"}, Token: access.APIToken{
				ID: "token-1", PrincipalID: "actor-1", TokenFingerprint: "fingerprint-token-1",
				PermissionProfile: access.PermissionCatalogProfile, Permissions: append([]access.PermissionPair{manage, delegate}, wantPermissions...),
			}}, true
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","grantAdminEnvelopeId":"envelope-1","expectedRevision":0}`))
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
	if repo.directCalls != 0 {
		t.Fatalf("direct writer calls = %d, want 0", repo.directCalls)
	}
	if got, want := repo.input.Scope, (access.AuthorizationPolicyScope{TargetID: "target-server", ProjectID: "project_demo", Environment: "prod"}); got != want {
		t.Fatalf("scope = %#v, want %#v", got, want)
	}
	if got, want := repo.input.IdempotencyKey, "idem-1"; got != want {
		t.Fatalf("idempotency key = %q, want %q", got, want)
	}
	if repo.input.Binding.PermissionRole != access.PermissionRoleViewer || repo.input.Binding.PermissionProfile != access.PermissionCatalogProfile {
		t.Fatalf("typed role binding = %#v", repo.input.Binding)
	}
	if len(repo.input.Binding.Permissions) != len(wantPermissions) {
		t.Fatalf("permissions = %#v, want %#v", repo.input.Binding.Permissions, wantPermissions)
	}
	for index := range wantPermissions {
		if repo.input.Binding.Permissions[index].Key() != wantPermissions[index].Key() {
			t.Fatalf("permission[%d] = %#v, want %#v", index, repo.input.Binding.Permissions[index], wantPermissions[index])
		}
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["permissionProfile"] != access.PermissionCatalogProfile {
		t.Fatalf("response omitted typed profile: %#v", response)
	}
	if _, supplied := response["permissions"]; !supplied {
		t.Fatalf("response omitted exact permission expansion: %#v", response)
	}
	if _, supplied := response["capabilities"]; supplied {
		t.Fatalf("typed response exposed legacy capabilities: %#v", response)
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
	handler.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
		return append([]access.PermissionPair{manage}, wantPermissions...), nil
	}
	denied := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","grantAdminEnvelopeId":"envelope-1","expectedRevision":0}`))
	denied = withProjectRoute(denied, "project_demo")
	denied.Header.Set("Idempotency-Key", "idem-2")
	denial := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(denial, denied)
	if denial.Code != http.StatusForbidden || repo.writerCalls != 1 {
		t.Fatalf("principal without delegate status=%d writerCalls=%d body=%s", denial.Code, repo.writerCalls, denial.Body.String())
	}
}

func TestCreateProjectRoleBindingRejectsTokenWithManageButNoDelegate(t *testing.T) {
	wantPermissions, err := access.ExpandPermissionRole(access.PermissionRoleViewer, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	repo := &roleBindingPolicyRepositoryStub{envelope: access.GrantAdminEnvelope{
		ID: "envelope-1", BoundPrincipalID: "actor-1", TargetProjectID: "project_demo",
		Issuer: access.GrantIssuerEvidence{PrincipalID: "actor-1", Credential: access.GrantCredentialEvidence{
			Class: access.GrantCredentialClassAPIToken, ID: "token-1", Fingerprint: "fingerprint-token-1",
		}},
		RecipientSelector: "group:group-1", RoleVersion: access.PermissionRoleVersion(access.PermissionRoleViewer), Permissions: wantPermissions,
	}}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "actor-1"}, true },
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return append([]access.PermissionPair{manage, delegate}, wantPermissions...), nil
		},
		CurrentCredential: func(*http.Request) (access.APICredential, bool) {
			return access.APICredential{Principal: access.Principal{ID: "actor-1"}, Token: access.APIToken{
				ID: "token-1", PrincipalID: "actor-1", TokenFingerprint: "fingerprint-token-1",
				PermissionProfile: access.PermissionCatalogProfile, Permissions: append([]access.PermissionPair{manage}, wantPermissions...),
			}}, true
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","grantAdminEnvelopeId":"envelope-1","expectedRevision":0}`))
	request = withProjectRoute(request, "project_demo")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 0 {
		t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
	}
}

func TestCreateProjectRoleBindingRejectsTokenPairCeilingNarrowerThanRole(t *testing.T) {
	wantPermissions, err := access.ExpandPermissionRole(access.PermissionRoleViewer, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	repo := &roleBindingPolicyRepositoryStub{envelope: access.GrantAdminEnvelope{
		ID: "envelope-1", BoundPrincipalID: "actor-1", TargetProjectID: "project_demo",
		Issuer: access.GrantIssuerEvidence{PrincipalID: "actor-1", Credential: access.GrantCredentialEvidence{
			Class: access.GrantCredentialClassAPIToken, ID: "token-1", Fingerprint: "fingerprint-token-1",
		}},
		RecipientSelector: "group:group-1", RoleVersion: access.PermissionRoleVersion(access.PermissionRoleViewer), Permissions: wantPermissions,
	}}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "actor-1"}, true },
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return append([]access.PermissionPair{manage, delegate}, wantPermissions...), nil
		},
		CurrentCredential: func(*http.Request) (access.APICredential, bool) {
			return access.APICredential{Principal: access.Principal{ID: "actor-1"}, Token: access.APIToken{
				ID: "token-1", PrincipalID: "actor-1", TokenFingerprint: "fingerprint-token-1",
				PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{manage, delegate, wantPermissions[0]},
			}}, true
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","grantAdminEnvelopeId":"envelope-1","expectedRevision":0}`))
	request = withProjectRoute(request, "project_demo")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 0 {
		t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
	}
}

func TestCreateProjectRoleBindingRejectsEnvelopeIssuedByAnotherToken(t *testing.T) {
	wantPermissions, err := access.ExpandPermissionRole(access.PermissionRoleViewer, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	repo := &roleBindingPolicyRepositoryStub{envelope: access.GrantAdminEnvelope{
		ID: "envelope-1", BoundPrincipalID: "actor-1", TargetProjectID: "project_demo",
		Issuer: access.GrantIssuerEvidence{PrincipalID: "actor-1", Credential: access.GrantCredentialEvidence{
			Class: access.GrantCredentialClassAPIToken, ID: "token-2", Fingerprint: "fingerprint-token-2",
		}},
		RecipientSelector: "group:group-1", RoleVersion: access.PermissionRoleVersion(access.PermissionRoleViewer), Permissions: wantPermissions,
	}}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "actor-1"}, true },
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return append([]access.PermissionPair{manage, delegate}, wantPermissions...), nil
		},
		CurrentCredential: func(*http.Request) (access.APICredential, bool) {
			return access.APICredential{Principal: access.Principal{ID: "actor-1"}, Token: access.APIToken{
				ID: "token-1", PrincipalID: "actor-1", TokenFingerprint: "fingerprint-token-1",
				PermissionProfile: access.PermissionCatalogProfile, Permissions: append([]access.PermissionPair{manage, delegate}, wantPermissions...),
			}}, true
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","grantAdminEnvelopeId":"envelope-1","expectedRevision":0}`))
	request = withProjectRoute(request, "project_demo")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 0 {
		t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
	}
}

func TestCreateProjectRoleBindingRejectsEnvelopeForDifferentRecipient(t *testing.T) {
	permissions, err := access.ExpandPermissionRole(access.PermissionRoleViewer, "project_demo")
	if err != nil {
		t.Fatal(err)
	}
	repo := &roleBindingPolicyRepositoryStub{envelope: access.GrantAdminEnvelope{
		ID: "envelope-1", BoundPrincipalID: "actor-1", TargetProjectID: "project_demo",
		RecipientSelector: "group:other-group", RoleVersion: access.PermissionRoleVersion(access.PermissionRoleViewer),
		Permissions: permissions,
	}}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "actor-1"}, true },
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"group","subjectId":"group-1","role":"viewer","grantAdminEnvelopeId":"envelope-1","expectedRevision":0}`))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "idem-1")
	recorder := httptest.NewRecorder()
	handler.CreateProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 0 {
		t.Fatalf("writer calls = %d, want 0", repo.writerCalls)
	}
}

func TestCreateProjectRoleBindingRejectsCallerAuthorityExpansion(t *testing.T) {
	repo := &roleBindingPolicyRepositoryStub{}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/role-bindings", strings.NewReader(`{"id":"binding-1","subjectType":"principal","subjectId":"principal-1","role":"viewer","permissions":[],"expectedRevision":0}`))
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

func TestDeleteProjectRoleBindingUsesAuditedCASAndReturnsNewPolicyReceipt(t *testing.T) {
	binding := access.RoleBinding{
		ID: "binding-1", Name: "Viewer",
		Subject:           access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		PermissionProfile: access.PermissionCatalogProfile, PermissionRole: access.PermissionRoleViewer,
	}
	repo := &roleBindingPolicyRepositoryStub{policy: access.AuthorizationPolicy{
		Scope:    access.AuthorizationPolicyScope{TargetID: "target-server", ProjectID: "project_demo", Environment: "prod"},
		Revision: 3, Digest: "sha256:" + strings.Repeat("b", 64), RoleBindings: []access.RoleBinding{binding},
	}}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/role-bindings/binding-1", strings.NewReader(`{"expectedRevision":3}`))
	request = withProjectAndBindingRoute(request, "project_demo", "binding-1")
	request.Header.Set("Idempotency-Key", "remove-1")
	recorder := httptest.NewRecorder()
	handler.DeleteProjectRoleBinding(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.writerCalls != 1 || repo.directCalls != 0 {
		t.Fatalf("writer/direct calls = %d/%d, want 1/0", repo.writerCalls, repo.directCalls)
	}
	if got, want := repo.deleteInput, (access.AuthorizationRoleBindingDeleteInput{
		Scope:     access.AuthorizationPolicyScope{TargetID: "target-server", ProjectID: "project_demo", Environment: "prod"},
		BindingID: "binding-1", ExpectedRevision: 3, IdempotencyKey: "remove-1",
	}); got != want {
		t.Fatalf("delete input = %#v, want %#v", got, want)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["policyRevision"] != float64(4) || response["policyDigest"] == "" {
		t.Fatalf("response = %#v, want revision receipt", response)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(repo.audit.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	payload := metadata["payload"].(map[string]any)
	if payload["bindingId"] != "binding-1" || payload["policyRevision"] != float64(4) || payload["role"] != "viewer" {
		t.Fatalf("audit payload = %#v", payload)
	}
}

func withProjectRoute(request *http.Request, project string) *http.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", project)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}

func withProjectAndBindingRoute(request *http.Request, project, binding string) *http.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", project)
	ctx.URLParams.Add("binding", binding)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}
