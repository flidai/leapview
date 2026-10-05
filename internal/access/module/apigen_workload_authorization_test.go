package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAPIGenActiveSourcePlanAcceptsOnlyGrantedWorkloadAuthoringScope(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	principal := access.Principal{ID: "publisher", Kind: access.PrincipalKindServicePrincipal}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectPair, err := access.NewProjectPermissionPair(access.ActionDeliveryPlan, projectID)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := accesssnapshot.NewTypedGrant("delivery-plan", "Delivery plan", subject, []access.PermissionPair{projectPair})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	authoringAuth, err := access.NewAuthoringAuthService(store.repository, access.AuthoringAuthConfig{
		InstanceID: "instance-prod", CanonicalOrigin: "https://example.test",
		AccessTokenTTL: time.Hour, RefreshTokenTTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := APIGenOperationContract{
		OperationID: "planProjectCandidateSynchronization", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/candidate-sync/plan", Protected: true, AuthzMode: "privilege",
		Action: string(access.ActionDeliveryPlan), Resolver: string(access.TypedOperationResolverDelivery),
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_EDIT", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "delivery"},
	}

	tests := []struct {
		name                  string
		snapshotGrant         bool
		scopeTarget           string
		scopeProject          projectgraph.ResourceID
		scopeAction           access.Action
		tokenID               string
		tokenPrincipalID      string
		credentialPrincipalID string
		configuredTarget      bool
		workload              bool
		wantStatus            int
	}{
		{name: "exact workload scope and active grant", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: principal.ID, configuredTarget: true, workload: true, wantStatus: http.StatusNoContent},
		{name: "wrong instance scope", snapshotGrant: true, scopeTarget: "instance-other", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: principal.ID, configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "wrong project scope", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: "project_other", scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: principal.ID, configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "wrong action scope", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPublish, tokenID: "workload-token", tokenPrincipalID: principal.ID, configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "scope without active grant", scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: principal.ID, configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "missing configured instance binding", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: principal.ID, workload: true, wantStatus: http.StatusForbidden},
		{name: "missing credential token identity", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenPrincipalID: principal.ID, configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "mismatched token principal", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: "someone-else", configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "mismatched credential principal", snapshotGrant: true, scopeTarget: "instance-prod", scopeProject: projectID, scopeAction: access.ActionDeliveryPlan, tokenID: "workload-token", tokenPrincipalID: principal.ID, credentialPrincipalID: "someone-else", configuredTarget: true, workload: true, wantStatus: http.StatusForbidden},
		{name: "legacy token remains denied", snapshotGrant: true, tokenID: "legacy-token", tokenPrincipalID: principal.ID, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var grants []accesssnapshot.Grant
			if test.snapshotGrant {
				grants = append(grants, grant)
			}
			snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
			if err != nil {
				t.Fatal(err)
			}
			module := browserGuardModule(browserGuardRepository{}, Principal{ID: principal.ID, Kind: principal.Kind}, true)
			if test.configuredTarget {
				module.authoringAuth = authoringAuth
			}
			authorizer, err := module.APIGenAuthorizer(
				apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
				map[string]APIGenOperationContract{contract.OperationID: contract},
				APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProjectNamespace)},
			)
			if err != nil {
				t.Fatal(err)
			}
			authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
				return APIGenBootstrapDecision{Handled: false}, nil
			})
			protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok || protected == nil {
				t.Fatal("candidate-sync authorizer was not created")
			}

			credentialPrincipal := principal
			if test.credentialPrincipalID != "" {
				credentialPrincipal.ID = test.credentialPrincipalID
			}
			credential := access.APICredential{
				Principal: credentialPrincipal,
				Token: access.APIToken{
					ID: test.tokenID, PrincipalID: test.tokenPrincipalID,
					Capabilities: []access.Capability{access.CapabilityResourceEdit},
				},
			}
			if test.workload {
				scope, err := access.NewAuthoringScope(test.scopeTarget, test.scopeProject, []access.PermissionPair{bootstrapProjectPair(t, test.scopeProject, test.scopeAction)})
				if err != nil {
					t.Fatal(err)
				}
				credential.Authoring = &access.AuthoringSession{
					ID: "workload-session", Kind: access.AuthoringSessionWorkload, ClientID: principal.ID,
					PrincipalID: principal.ID, Scope: scope,
				}
			}
			r := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/candidate-sync/plan", map[string]string{"project": projectID.String()})
			r.Header.Set("Authorization", "Bearer workload-token")
			r = r.WithContext(WithAPICredential(r.Context(), credential))
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, r)
			if recorder.Code != test.wantStatus {
				t.Fatalf("active candidate-sync status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
		})
	}
}
