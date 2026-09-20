package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	apigenruntime "github.com/flidai/leapview/internal/app/api/apigenruntime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

const (
	durableHTTPIssuerID    = "00000000-0000-7000-8000-000000000301"
	durableHTTPRecipientID = "00000000-0000-7000-8000-000000000302"
	durableHTTPGrantID     = "grant-http-1"
	durableHTTPResourceUID = "00000000-0000-7000-8000-000000000303"
)

type durableGrantGeneratedAuthorizer struct{}

func (durableGrantGeneratedAuthorizer) Protect(_ string, next http.Handler) (http.Handler, bool) {
	return next, true
}

func runDurableGrantGeneratedRequest(t *testing.T, operation string, handler Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	runtime, err := apigenruntime.Build(durableGrantGeneratedAuthorizer{}, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		switch operationID {
		case "issueResourceShareGrant":
			handler.IssueResourceShareGrant(w, r)
		case "revokeResourceShareGrant":
			handler.RevokeResourceShareGrant(w, r)
		case "issueGrantAdminEnvelope":
			handler.IssueGrantAdminEnvelope(w, r)
		case "revokeGrantAdminEnvelope":
			handler.RevokeGrantAdminEnvelope(w, r)
		default:
			return false
		}
		return true
	}, accessgen.GetAPIGenCommandRuntimeContract)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	runtime.HandleAPIGen(operation, recorder, request)
	return recorder
}

type durableGrantHTTPRepository struct {
	access.Repository
	grant         access.ResourceShareGrant
	createInput   access.ResourceShareGrantInput
	envelope      access.GrantAdminEnvelope
	envelopeInput access.GrantAdminEnvelopeInput
	revokeCalls   int
	revokeActor   string
	revokeTarget  access.DurableGrantTarget
}

func (r *durableGrantHTTPRepository) CreateResourceShareGrant(_ context.Context, input access.ResourceShareGrantInput) (access.ResourceShareGrant, error) {
	r.createInput = input
	r.grant = access.ResourceShareGrant{
		ID: input.ID, Profile: input.Profile, Target: input.Target, Issuer: input.Issuer,
		Recipient: input.Recipient, Permissions: access.ClonePermissionPairs(input.Permissions),
		IssuedAt: time.Now().UTC(), Fingerprint: "sha256:" + strings.Repeat("a", 64),
		IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest,
	}
	return r.grant, nil
}

func (r *durableGrantHTTPRepository) ResourceShareGrant(_ context.Context, id string) (access.ResourceShareGrant, error) {
	if id == "" || id != r.grant.ID {
		return access.ResourceShareGrant{}, access.ErrGrantNotFound
	}
	return r.grant, nil
}

func (r *durableGrantHTTPRepository) RevokeResourceShareGrant(_ context.Context, id, actor, reason string) error {
	if id != r.grant.ID {
		return access.ErrGrantNotFound
	}
	r.revokeCalls++
	r.revokeActor = actor
	r.grant.RevokedAt = time.Now().UTC()
	r.grant.RevokedByPrincipalID = actor
	r.grant.RevocationReason = reason
	return nil
}

func (r *durableGrantHTTPRepository) CreateExecutionGrant(context.Context, access.ExecutionGrantInput) (access.ExecutionGrant, error) {
	return access.ExecutionGrant{}, errors.New("not used")
}

func (r *durableGrantHTTPRepository) CreateGrantAdminEnvelope(_ context.Context, input access.GrantAdminEnvelopeInput) (access.GrantAdminEnvelope, error) {
	r.envelopeInput = input
	r.envelope = access.GrantAdminEnvelope{
		ID: input.ID, Profile: input.Profile, Issuer: input.Issuer, BoundPrincipalID: input.BoundPrincipalID,
		Permissions: access.ClonePermissionPairs(input.Permissions), TargetProjectID: input.TargetProjectID,
		TargetResourceKind: input.TargetResourceKind, TargetResourceID: input.TargetResourceID,
		RecipientSelector: input.RecipientSelector, RoleVersion: input.RoleVersion, IssuedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(input.TTL), Fingerprint: "sha256:" + strings.Repeat("c", 64),
		IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest,
	}
	return r.envelope, nil
}

func (r *durableGrantHTTPRepository) RevokeExecutionGrant(context.Context, string, string, string) error {
	return errors.New("not used")
}

func (r *durableGrantHTTPRepository) GrantAdminEnvelope(_ context.Context, id string) (access.GrantAdminEnvelope, error) {
	if id == "" || id != r.envelope.ID {
		return access.GrantAdminEnvelope{}, access.ErrGrantNotFound
	}
	return r.envelope, nil
}

func (r *durableGrantHTTPRepository) RevokeGrantAdminEnvelope(_ context.Context, id, actor, reason string) error {
	if id != r.envelope.ID {
		return access.ErrGrantNotFound
	}
	r.revokeCalls++
	r.revokeActor = actor
	r.envelope.RevokedAt = time.Now().UTC()
	r.envelope.RevokedByPrincipalID = actor
	r.envelope.RevocationReason = reason
	return nil
}

func durableHTTPTarget() access.DurableGrantTarget {
	return access.DurableGrantTarget{
		InstanceID: "instance-http", ProjectID: "project_demo", ResourceUID: durableHTTPResourceUID,
		ResourceID: "dashboard_sales", ResourceKind: projectgraph.KindDashboard,
	}
}

func durableHTTPPermission(t *testing.T, action access.Action, target access.DurableGrantTarget) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(action, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func durableHTTPAuthority(t *testing.T, principalID string, target access.DurableGrantTarget) access.CurrentAuthoritySnapshot {
	t.Helper()
	share := durableHTTPPermission(t, access.ActionResourceShare, target)
	read := durableHTTPPermission(t, access.ActionDashboardRead, target)
	return access.CurrentAuthoritySnapshot{
		Principal:   access.Principal{ID: principalID, Kind: access.PrincipalKindUser},
		Permissions: []access.PermissionPair{share, read}, CredentialPermissions: []access.PermissionPair{share, read},
		Policy: access.GrantIssuancePolicy{Scope: access.AuthorizationPolicyScope{TargetID: target.InstanceID, ProjectID: target.ProjectID.String(), Environment: "production"}, Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)},
		Credential: access.CredentialEvidence{
			Class: access.GrantCredentialClassAPIToken, ID: "token-http-1", Fingerprint: "sha256:" + strings.Repeat("b", 64),
			PrincipalID: principalID, ExpiresAt: time.Now().Add(time.Hour),
		},
	}
}

func durableHTTPEnvelopeAuthority(t *testing.T, principalID string, projectID projectgraph.ResourceID) access.CurrentAuthoritySnapshot {
	t.Helper()
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, projectID)
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return access.CurrentAuthoritySnapshot{
		Principal: principalIDSnapshot(principalID), Permissions: []access.PermissionPair{manage, delegate},
		CredentialPermissions: []access.PermissionPair{manage, delegate},
		Policy:                access.GrantIssuancePolicy{Scope: access.AuthorizationPolicyScope{TargetID: "instance-http", ProjectID: projectID.String(), Environment: "production"}, Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)},
		Credential:            access.CredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token-envelope-http", Fingerprint: "sha256:" + strings.Repeat("d", 64), PrincipalID: principalID, ExpiresAt: time.Now().Add(time.Hour)},
	}
}

func principalIDSnapshot(id string) access.Principal {
	return access.Principal{ID: id, Kind: access.PrincipalKindUser}
}

func withGrantRoute(request *http.Request, grant string) *http.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("grant", grant)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}

func TestIssueResourceShareGrantDerivesIssuerAndCeilingServerSide(t *testing.T) {
	target := durableHTTPTarget()
	repository := &durableGrantHTTPRepository{}
	authority := durableHTTPAuthority(t, durableHTTPIssuerID, target)
	service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
		if request.Target != target {
			t.Fatalf("resolved target = %#v, want %#v", request.Target, target)
		}
		return authority, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		DurableGrantService:    func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
		DurableGrantInstanceID: target.InstanceID,
	}
	body := `{"resourceUid":"` + target.ResourceUID + `","resourceKind":"dashboard","resourceId":"` + target.ResourceID.String() + `","recipientType":"principal","recipientId":"` + durableHTTPRecipientID + `","permissions":[{"action":"dashboard.read","profile":"leapview.permissions/v1","target":{"scope":"resource","projectId":"project_demo","resourceKind":"dashboard","resourceId":"dashboard_sales"}}],"ttlSeconds":3600}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/resource-share-grants", strings.NewReader(body))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "issue-http-1")
	recorder := httptest.NewRecorder()
	handler.IssueResourceShareGrant(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.createInput.Issuer.PrincipalID != durableHTTPIssuerID {
		t.Fatalf("issuer = %#v, want server principal", repository.createInput.Issuer)
	}
	if repository.createInput.IdempotencyKey != "issue-http-1" {
		t.Fatalf("idempotency key = %q", repository.createInput.IdempotencyKey)
	}
	if repository.createInput.Target != target || repository.createInput.Recipient.ID != durableHTTPRecipientID {
		t.Fatalf("durable input = %#v", repository.createInput)
	}
	if len(repository.createInput.IssuancePermissions) != 2 {
		t.Fatalf("issuance ceiling = %#v", repository.createInput.IssuancePermissions)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"issuer", "credential", "issuancePermissions"} {
		if _, present := response[forbidden]; present {
			t.Fatalf("response exposed %q: %#v", forbidden, response)
		}
	}
}

func TestIssueResourceShareGrantRejectsExpiryBeyondBound(t *testing.T) {
	serviceCalls := 0
	handler := Handler{
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) {
			serviceCalls++
			return nil, errors.New("service must not run for an invalid expiry")
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/resource-share-grants", strings.NewReader(`{"recipientType":"principal","recipientId":"`+durableHTTPRecipientID+`","ttlSeconds":31536001}`))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "expiry-http-1")
	recorder := httptest.NewRecorder()
	handler.IssueResourceShareGrant(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if serviceCalls != 0 {
		t.Fatalf("service calls = %d, want 0", serviceCalls)
	}
}

func TestRevokeResourceShareGrantRejectsUnrelatedPrincipalBeforeMutation(t *testing.T) {
	target := durableHTTPTarget()
	repository := &durableGrantHTTPRepository{grant: access.ResourceShareGrant{
		ID: durableHTTPGrantID, Target: target, Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID, Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token-http-1", Fingerprint: "sha256:" + strings.Repeat("b", 64)}},
	}}
	serviceCalls := 0
	handler := Handler{
		Repository:       func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: durableHTTPRecipientID}, true },
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) {
			serviceCalls++
			return nil, errors.New("must not construct service for unrelated principal")
		},
	}
	request := withGrantRoute(httptest.NewRequest(http.MethodDelete, "/api/v1/resource-share-grants/"+durableHTTPGrantID, strings.NewReader(`{}`)), durableHTTPGrantID)
	recorder := httptest.NewRecorder()
	handler.RevokeResourceShareGrant(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.revokeCalls != 0 || serviceCalls != 0 {
		t.Fatalf("cross-principal revoke mutated state: revoke=%d service=%d", repository.revokeCalls, serviceCalls)
	}
}

func TestRevokeResourceShareGrantBindsAuthorityToStoredTarget(t *testing.T) {
	target := durableHTTPTarget()
	repository := &durableGrantHTTPRepository{grant: access.ResourceShareGrant{
		ID: durableHTTPGrantID, Target: target, Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID, Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token-http-1", Fingerprint: "sha256:" + strings.Repeat("b", 64)}},
	}}
	authority := durableHTTPAuthority(t, durableHTTPIssuerID, target)
	var resolved access.CurrentAuthorityRequest
	service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
		resolved = request
		return authority, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository:          func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal:    func(*http.Request) (Principal, bool) { return Principal{ID: durableHTTPIssuerID}, true },
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
	}
	request := withGrantRoute(httptest.NewRequest(http.MethodDelete, "/api/v1/resource-share-grants/"+durableHTTPGrantID, strings.NewReader(`{"reason":"security review"}`)), durableHTTPGrantID)
	recorder := httptest.NewRecorder()
	handler.RevokeResourceShareGrant(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if resolved.Target != target || repository.revokeActor != durableHTTPIssuerID || repository.grant.RevocationReason != "security review" {
		t.Fatalf("revoke target/actor/reason = %#v/%q/%q", resolved.Target, repository.revokeActor, repository.grant.RevocationReason)
	}
}

func TestIssueGrantAdminEnvelopeDerivesIssuerAndManageDelegateCeiling(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	repository := &durableGrantHTTPRepository{}
	authority := durableHTTPEnvelopeAuthority(t, durableHTTPIssuerID, projectID)
	service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
		want := access.DurableGrantTarget{ProjectID: projectID}
		if request.Target != want {
			t.Fatalf("resolved target = %#v, want %#v", request.Target, want)
		}
		return authority, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/grant-admin-envelopes", strings.NewReader(`{"permissions":[{"profile":"leapview.permissions/v1","action":"project.access.manage","target":{"scope":"project","projectId":"project_demo"}}],"recipientSelector":"group:`+durableHTTPRecipientID+`","roleVersion":"leapview.permissions/v1:role:viewer","ttlSeconds":3600}`))
	request = withProjectRoute(request, "project_demo")
	request.Header.Set("Idempotency-Key", "envelope-http-1")
	recorder := httptest.NewRecorder()
	handler.IssueGrantAdminEnvelope(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.envelopeInput.BoundPrincipalID != durableHTTPIssuerID || len(repository.envelopeInput.IssuancePermissions) != 2 || repository.envelopeInput.IdempotencyKey != "envelope-http-1" {
		t.Fatalf("derived envelope input = %#v", repository.envelopeInput)
	}
}

func TestRevokeGrantAdminEnvelopeRejectsUnrelatedPrincipal(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	repository := &durableGrantHTTPRepository{envelope: access.GrantAdminEnvelope{
		ID: "envelope-http-1", TargetProjectID: projectID, BoundPrincipalID: durableHTTPIssuerID,
		Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID},
	}}
	serviceCalls := 0
	handler := Handler{
		Repository:       func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: durableHTTPRecipientID}, true },
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) {
			serviceCalls++
			return nil, errors.New("must not construct service for unrelated principal")
		},
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/grant-admin-envelopes/envelope-http-1", strings.NewReader(`{}`))
	request = withProjectRoute(request, "project_demo")
	chi.RouteContext(request.Context()).URLParams.Add("envelope", "envelope-http-1")
	recorder := httptest.NewRecorder()
	handler.RevokeGrantAdminEnvelope(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.revokeCalls != 0 || serviceCalls != 0 {
		t.Fatalf("cross-principal revoke mutated state: revoke=%d service=%d", repository.revokeCalls, serviceCalls)
	}
}

func TestRevokeGrantAdminEnvelopeRejectsProjectMismatch(t *testing.T) {
	repository := &durableGrantHTTPRepository{envelope: access.GrantAdminEnvelope{
		ID: "envelope-http-1", TargetProjectID: "project_demo", BoundPrincipalID: durableHTTPIssuerID,
		Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID},
	}}
	serviceCalls := 0
	handler := Handler{
		Repository:       func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: durableHTTPIssuerID}, true },
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) {
			serviceCalls++
			return nil, errors.New("must not construct service for a project mismatch")
		},
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_other/grant-admin-envelopes/envelope-http-1", strings.NewReader(`{}`))
	request = withProjectRoute(request, "project_other")
	chi.RouteContext(request.Context()).URLParams.Add("envelope", "envelope-http-1")
	recorder := httptest.NewRecorder()
	handler.RevokeGrantAdminEnvelope(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.revokeCalls != 0 || serviceCalls != 0 {
		t.Fatalf("project-mismatch revoke mutated state: revoke=%d service=%d", repository.revokeCalls, serviceCalls)
	}
}

func TestRevokeGrantAdminEnvelopeBindsStoredProjectTarget(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	repository := &durableGrantHTTPRepository{envelope: access.GrantAdminEnvelope{
		ID: "envelope-http-1", TargetProjectID: projectID, TargetResourceKind: projectgraph.KindDashboard, TargetResourceID: "dashboard_sales",
		BoundPrincipalID: durableHTTPIssuerID, Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID},
	}}
	authority := durableHTTPEnvelopeAuthority(t, durableHTTPIssuerID, projectID)
	var resolved access.CurrentAuthorityRequest
	service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
		resolved = request
		return authority, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository:          func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal:    func(*http.Request) (Principal, bool) { return Principal{ID: durableHTTPIssuerID}, true },
		DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/grant-admin-envelopes/envelope-http-1", strings.NewReader(`{"reason":"security review"}`))
	request = withProjectRoute(request, "project_demo")
	chi.RouteContext(request.Context()).URLParams.Add("envelope", "envelope-http-1")
	recorder := httptest.NewRecorder()
	handler.RevokeGrantAdminEnvelope(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := access.DurableGrantTarget{ProjectID: projectID, ResourceKind: projectgraph.KindDashboard, ResourceID: "dashboard_sales"}
	if resolved.Target != want || repository.revokeActor != durableHTTPIssuerID || repository.envelope.RevocationReason != "security review" {
		t.Fatalf("revoke target/actor/reason = %#v/%q/%q", resolved.Target, repository.revokeActor, repository.envelope.RevocationReason)
	}
}

func TestDurableGrantCommandsCompleteGeneratedRuntimeContract(t *testing.T) {
	t.Run("issue resource share", func(t *testing.T) {
		target := durableHTTPTarget()
		repository := &durableGrantHTTPRepository{}
		authority := durableHTTPAuthority(t, durableHTTPIssuerID, target)
		service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
			if request.Target != target {
				t.Fatalf("resolved target = %#v, want %#v", request.Target, target)
			}
			return authority, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		handler := Handler{
			DurableGrantService:    func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
			DurableGrantInstanceID: target.InstanceID,
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/resource-share-grants", strings.NewReader(`{"resourceUid":"`+target.ResourceUID+`","resourceKind":"dashboard","resourceId":"dashboard_sales","recipientType":"principal","recipientId":"`+durableHTTPRecipientID+`","permissions":[{"action":"dashboard.read","profile":"leapview.permissions/v1","target":{"scope":"resource","projectId":"project_demo","resourceKind":"dashboard","resourceId":"dashboard_sales"}}],"ttlSeconds":3600}`))
		request = withProjectRoute(request, "project_demo")
		request.Header.Set("Idempotency-Key", "generated-issue-share")
		response := runDurableGrantGeneratedRequest(t, "issueResourceShareGrant", handler, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if repository.createInput.Target != target || repository.createInput.IdempotencyKey != "generated-issue-share" {
			t.Fatalf("durable input = %#v", repository.createInput)
		}
	})

	t.Run("revoke resource share", func(t *testing.T) {
		target := durableHTTPTarget()
		repository := &durableGrantHTTPRepository{grant: access.ResourceShareGrant{
			ID: durableHTTPGrantID, Target: target,
			Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID, Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token-generated-share", Fingerprint: "sha256:" + strings.Repeat("b", 64)}},
		}}
		authority := durableHTTPAuthority(t, durableHTTPIssuerID, target)
		service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
			if request.Target != target {
				t.Fatalf("resolved target = %#v, want %#v", request.Target, target)
			}
			return authority, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		handler := Handler{
			Repository: func() (access.Repository, error) { return repository, nil },
			CurrentPrincipal: func(*http.Request) (Principal, bool) {
				return Principal{ID: durableHTTPIssuerID}, true
			},
			DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
		}
		request := withGrantRoute(httptest.NewRequest(http.MethodDelete, "/api/v1/resource-share-grants/"+durableHTTPGrantID, strings.NewReader(`{"reason":"generated revoke"}`)), durableHTTPGrantID)
		response := runDurableGrantGeneratedRequest(t, "revokeResourceShareGrant", handler, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if repository.revokeActor != durableHTTPIssuerID || repository.grant.RevocationReason != "generated revoke" {
			t.Fatalf("revoke actor/reason = %q/%q", repository.revokeActor, repository.grant.RevocationReason)
		}
	})

	t.Run("issue grant admin envelope", func(t *testing.T) {
		projectID := projectgraph.ResourceID("project_demo")
		repository := &durableGrantHTTPRepository{}
		authority := durableHTTPEnvelopeAuthority(t, durableHTTPIssuerID, projectID)
		service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
			want := access.DurableGrantTarget{ProjectID: projectID}
			if request.Target != want {
				t.Fatalf("resolved target = %#v, want %#v", request.Target, want)
			}
			return authority, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		handler := Handler{DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) { return service, nil }}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/grant-admin-envelopes", strings.NewReader(`{"permissions":[{"profile":"leapview.permissions/v1","action":"project.access.manage","target":{"scope":"project","projectId":"project_demo"}}],"recipientSelector":"group:`+durableHTTPRecipientID+`","roleVersion":"leapview.permissions/v1:role:viewer","ttlSeconds":3600}`))
		request = withProjectRoute(request, "project_demo")
		request.Header.Set("Idempotency-Key", "generated-issue-envelope")
		response := runDurableGrantGeneratedRequest(t, "issueGrantAdminEnvelope", handler, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if repository.envelopeInput.BoundPrincipalID != durableHTTPIssuerID || repository.envelopeInput.IdempotencyKey != "generated-issue-envelope" {
			t.Fatalf("durable input = %#v", repository.envelopeInput)
		}
	})

	t.Run("revoke grant admin envelope", func(t *testing.T) {
		projectID := projectgraph.ResourceID("project_demo")
		repository := &durableGrantHTTPRepository{envelope: access.GrantAdminEnvelope{
			ID: "envelope-generated-1", TargetProjectID: projectID, TargetResourceKind: projectgraph.KindDashboard, TargetResourceID: "dashboard_sales",
			BoundPrincipalID: durableHTTPIssuerID, Issuer: access.GrantIssuerEvidence{PrincipalID: durableHTTPIssuerID},
		}}
		authority := durableHTTPEnvelopeAuthority(t, durableHTTPIssuerID, projectID)
		service, err := access.NewDurableGrantService(repository, access.CurrentAuthorityResolverFunc(func(_ context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
			want := access.DurableGrantTarget{ProjectID: projectID, ResourceKind: projectgraph.KindDashboard, ResourceID: "dashboard_sales"}
			if request.Target != want {
				t.Fatalf("resolved target = %#v, want %#v", request.Target, want)
			}
			return authority, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		handler := Handler{
			Repository: func() (access.Repository, error) { return repository, nil },
			CurrentPrincipal: func(*http.Request) (Principal, bool) {
				return Principal{ID: durableHTTPIssuerID}, true
			},
			DurableGrantService: func(*http.Request) (*access.DurableGrantService, error) { return service, nil },
		}
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project_demo/grant-admin-envelopes/envelope-generated-1", strings.NewReader(`{"reason":"generated revoke"}`))
		request = withProjectRoute(request, "project_demo")
		chi.RouteContext(request.Context()).URLParams.Add("envelope", "envelope-generated-1")
		response := runDurableGrantGeneratedRequest(t, "revokeGrantAdminEnvelope", handler, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if repository.revokeActor != durableHTTPIssuerID || repository.envelope.RevocationReason != "generated revoke" {
			t.Fatalf("revoke actor/reason = %q/%q", repository.revokeActor, repository.envelope.RevocationReason)
		}
	})
}
