package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type grantRepositoryStub struct {
	access.Repository
	input access.AuthorizationGrantInput
	audit access.AuditEventInput
	calls int
}
type grantTransactionStub struct {
	access.Repository
	parent *grantRepositoryStub
}

type grantReadRepositoryStub struct {
	access.Repository
	policy access.AuthorizationPolicy
	scope  access.AuthorizationPolicyScope
}

func (s *grantReadRepositoryStub) AuthorizationPolicy(_ context.Context, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	s.scope = scope
	return s.policy, nil
}

func (s *grantReadRepositoryStub) AuthorizationPolicyRevision(_ context.Context, scope access.AuthorizationPolicyScope, revision int64) (access.AuthorizationPolicy, error) {
	s.scope = scope
	if s.policy.Revision != revision {
		return access.AuthorizationPolicy{}, access.ErrAuthorizationPolicyStaleRevision
	}
	return s.policy, nil
}

func (tx *grantTransactionStub) UpsertAuthorizationGrant(_ context.Context, in access.AuthorizationGrantInput) (access.AuthorizationPolicy, error) {
	tx.parent.input = in
	tx.parent.calls++
	return access.AuthorizationPolicy{Scope: in.Scope, Revision: in.ExpectedRevision + 1, Digest: "sha256:" + strings.Repeat("a", 64), Grants: []access.AuthorizationGrant{in.Grant}}, nil
}
func (s *grantRepositoryStub) RunAuditedMutation(_ context.Context, fn func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := fn(&grantTransactionStub{parent: s})
	s.audit = event
	return err
}
func TestGrantCommandUsesTransactionalPolicyAndServerScope(t *testing.T) {
	repo := &grantRepositoryStub{}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, AuthorizationPolicyTargetID: "server-target", AuthorizationPolicyEnvironment: "prod"}
	body := `{"id":"demo-read","resourceKind":"dashboard","resourceId":"dashboard:sales","subjectType":"principal","subjectId":"demo","capability":"RESOURCE_READ","expectedRevision":4}`
	request := withProjectRoute(httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:demo/grants", strings.NewReader(body)), "project:demo")
	ctx, guard, err := accessgen.BeginGenCreateGrantCommand(request.Context(), accessgen.GenCreateGrantCommandInvocation{Surface: apigencommand.SurfaceAPI, Project: "project:demo", IdempotencyKey: "grant-1"})
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	NewAPIGenDispatcher(handler).CreateGrant(recorder, request, "project:demo", accessgen.GenCreateGrantHeaders{IdempotencyKey: "grant-1"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if !guard.Completed() {
		t.Fatal("grant command did not complete generated transactional contract")
	}
	if repo.calls != 1 || repo.input.IdempotencyKey != "grant-1" || repo.input.ExpectedRevision != 4 || repo.input.Scope.TargetID != "server-target" || repo.input.Scope.Environment != "prod" {
		t.Fatalf("wrong policy command: %+v", repo.input)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(repo.audit.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["resourceId"] != "dashboard:sales" || payload["subjectId"] != "demo" {
		t.Fatalf("missing audit payload: %v", metadata)
	}
	for _, invalid := range []string{strings.Replace(body, `,"expectedRevision":4`, "", 1), strings.Replace(body, "RESOURCE_READ", "PROJECT_ADMIN", 1), strings.Replace(body, `"id":"demo-read"`, `"id":"demo-read","targetId":"attacker"`, 1)} {
		req := withProjectRoute(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(invalid)), "project:demo")
		rec := httptest.NewRecorder()
		handler.CreateGrant(rec, req)
		if rec.Code != 400 || repo.calls != 1 {
			t.Fatalf("invalid request changed policy: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestListGrantsReadsLegacyAndTypedGrantRepresentations(t *testing.T) {
	projectID := projectgraph.ResourceID("project:demo")
	resource, err := access.NewResourceRef("dashboard:sales", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	legacy := access.AuthorizationGrant{
		ID: "legacy-read", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "legacy-user"},
		Resource: resource, Capability: access.CapabilityResourceRead,
	}
	read, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	typed := access.AuthorizationGrant{
		ID: "typed-read", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "typed-user"},
		Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{read},
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target-server", ProjectID: projectID.String(), Environment: "prod"}
	repo := &grantReadRepositoryStub{policy: access.AuthorizationPolicy{
		Scope: scope, Revision: 4, Digest: "sha256:" + strings.Repeat("a", 64), Grants: []access.AuthorizationGrant{legacy, typed},
	}}
	handler := Handler{
		Repository:                  func() (access.Repository, error) { return repo, nil },
		AuthorizationPolicyTargetID: "target-server", AuthorizationPolicyEnvironment: "prod",
	}
	request := withProjectRoute(httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:demo/grants", nil), projectID.String())
	response := httptest.NewRecorder()

	handler.ListGrants(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if repo.scope != scope {
		t.Fatalf("policy scope = %#v, want %#v", repo.scope, scope)
	}
	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("grant items = %d, want 2", len(payload.Items))
	}
	var legacyBody, typedBody map[string]json.RawMessage
	for _, item := range payload.Items {
		var id string
		if err := json.Unmarshal(item["id"], &id); err != nil {
			t.Fatal(err)
		}
		switch id {
		case "legacy-read":
			legacyBody = item
		case "typed-read":
			typedBody = item
		}
	}
	if len(legacyBody) == 0 || len(legacyBody["capability"]) == 0 || len(legacyBody["permissionProfile"]) != 0 || len(legacyBody["permissions"]) != 0 {
		t.Fatalf("legacy grant response = %s", legacyBody)
	}
	if len(typedBody) == 0 || len(typedBody["capability"]) != 0 {
		t.Fatalf("typed grant response includes legacy fields: %s", typedBody)
	}
	var profile string
	var permissions []access.PermissionPair
	if err := json.Unmarshal(typedBody["permissionProfile"], &profile); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(typedBody["permissions"], &permissions); err != nil {
		t.Fatal(err)
	}
	if profile != access.PermissionCatalogProfile || len(permissions) != 1 || permissions[0] != read {
		t.Fatalf("typed grant profile/permissions = %q %#v", profile, permissions)
	}
	if len(typedBody["resourceId"]) == 0 || len(typedBody["resourceKind"]) == 0 {
		t.Fatalf("typed grant lost its exact resource: %s", typedBody)
	}
}
