package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestListEffectiveCapabilitiesUsesOneBoundProjection(t *testing.T) {
	handler, _ := authorizationProjectionFixture(t)
	handler.RequestEffectiveCapabilities = func(context.Context, *stdhttp.Request, string) ([]access.Capability, error) {
		panic("effective capability handler acquired a second serving snapshot")
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/effective-capabilities?resourceKind=dashboard&resourceId=dashboard_main", nil)
	response := httptest.NewRecorder()
	handler.ListEffectiveCapabilities(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body accessgen.EffectiveCapabilityListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ResourceId != "dashboard_main" || body.ResourceKind != accessgen.AccessResourceKindDashboard {
		t.Fatalf("resource = %#v", body)
	}
	if len(body.Capabilities) != 1 || string(body.Capabilities[0]) != string(access.CapabilityResourceRead) {
		t.Fatalf("capabilities = %#v", body.Capabilities)
	}
	if len(body.EffectiveGrants) != 1 || body.EffectiveGrants[0].GrantId == nil || *body.EffectiveGrants[0].GrantId != "grant_reader" || body.EffectiveGrants[0].Reason != "direct_grant" {
		t.Fatalf("decisions = %#v", body.EffectiveGrants)
	}
}

func TestAuthorizationBatchPreservesDenialsAndCredentialAttenuation(t *testing.T) {
	handler, _ := authorizationProjectionFixture(t)
	handler.CurrentCredential = func(*stdhttp.Request) (access.APICredential, bool) {
		return access.APICredential{Token: access.APIToken{ID: "token-edit-only", Capabilities: []access.Capability{access.CapabilityResourceEdit}}}, true
	}
	body := []byte(`{"checks":[{"capability":"RESOURCE_READ","resourceKind":"dashboard","resourceId":"dashboard_main"}]}`)
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/authorization-checks", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.CheckAuthorizationBatch(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var result accessgen.AuthorizationBatchCheckResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 1 || result.Decisions[0].Allowed == nil || *result.Decisions[0].Allowed || result.Decisions[0].Reason != "credential_attenuation" {
		t.Fatalf("decisions = %#v", result.Decisions)
	}
}

func TestAuthorizationProjectionRejectsImplicitScopeAndUnknownResources(t *testing.T) {
	handler, _ := authorizationProjectionFixture(t)
	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/api/v1/effective-capabilities", want: stdhttp.StatusBadRequest},
		{path: "/api/v1/effective-capabilities?resourceKind=dashboard&resourceId=missing", want: stdhttp.StatusNotFound},
		{path: "/api/v1/effective-capabilities?resourceKind=project&resourceId=project_demo", want: stdhttp.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		handler.ListEffectiveCapabilities(response, httptest.NewRequest(stdhttp.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s status = %d body=%s, want %d", test.path, response.Code, response.Body.String(), test.want)
		}
	}
}

func TestAuthorizationBatchRejectsMalformedOrKindChangingChecks(t *testing.T) {
	handler, _ := authorizationProjectionFixture(t)
	for _, body := range []string{
		`{"checks":[]}`,
		`{"checks":[{"capability":"RESOURCE_READ","resourceKind":"model","resourceId":"dashboard_main"}]}`,
		`{"checks":[{"capability":"PROJECT_ADMIN","resourceKind":"dashboard","resourceId":"dashboard_main"}]}`,
		`{"checks":[],"projectId":"project_demo"}`,
	} {
		response := httptest.NewRecorder()
		handler.CheckAuthorizationBatch(response, httptest.NewRequest(stdhttp.MethodPost, "/api/v1/authorization-checks", bytes.NewBufferString(body)))
		if response.Code < 400 || response.Code >= 500 {
			t.Fatalf("body %s status = %d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func authorizationProjectionFixture(t *testing.T) (Handler, accesssnapshot.AuthorizationSnapshot) {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: projectgraph.KindDashboard, Name: "main"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_reader")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("dashboard_main", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := access.NewCanonicalGrant(graph, subject, resource, access.CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{{ID: "grant_reader", Canonical: grant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: subject.ID}, true
		},
		AuthorizationSnapshot: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
		AuthorizationSubjects: func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{subject}, nil },
		RequestEffectiveCapabilities: func(context.Context, *stdhttp.Request, string) ([]access.Capability, error) {
			return snapshot.EffectiveCapabilities([]access.SubjectRef{subject})
		},
	}
	return handler, snapshot
}
