package module

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAPIGenTypedResourceAuthorizationUnionsPrincipalAndGroupPrerequisites(t *testing.T) {
	identity, snapshot := typedResourceSnapshot(t, []typedGrantSpec{
		{ID: "principal-query", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"}, Actions: []access.Action{access.ActionSemanticQuery}},
		{ID: "group-consume", Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group_analysts"}, Actions: []access.Action{access.ActionSemanticConsume}},
	})
	module := browserGuardModule(browserGuardRepository{groups: []string{"group_analysts"}}, Principal{ID: "principal"}, true)
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: identity.ProjectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
		map[string]APIGenOperationContract{"queryModel": {
			OperationID: "queryModel", Method: http.MethodGet, Path: "/api/v1/projects/{project}/semantic-models/{model}",
			Protected: true, AuthzMode: "privilege", Action: string(access.ActionSemanticQuery), Resolver: string(access.TypedOperationResolverSemanticModel),
			Extensions: map[string]any{apiGenObjectScopeExtension: "semantic-model", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"}},
		}},
		APIGenResourceResolvers{SemanticModel: apigenResolver("model", projectgraph.KindSemanticModel)},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, ok := authorizer.Protect("queryModel", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || handler == nil {
		t.Fatal("typed operation was not protected")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/semantic-models/model_sales", map[string]string{"project": "project_demo", "model": "model_sales"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenTypedResourceAuthorizationRequiresBoundCredentialCeiling(t *testing.T) {
	identity, snapshot := typedResourceSnapshot(t, []typedGrantSpec{
		{ID: "principal-query", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"}, Actions: []access.Action{access.ActionSemanticQuery}},
		{ID: "group-consume", Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group_analysts"}, Actions: []access.Action{access.ActionSemanticConsume}},
	})
	model, err := access.NewResourceRef("model_sales", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	query, err := access.NewExactPermissionPair(access.ActionSemanticQuery, identity.ProjectID, model)
	if err != nil {
		t.Fatal(err)
	}
	consume, err := access.NewExactPermissionPair(access.ActionSemanticConsume, identity.ProjectID, model)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		credential access.APICredential
		wantStatus int
	}{
		{
			name: "principal and group grants plus exact token ceiling",
			credential: access.APICredential{
				Principal: access.Principal{ID: "principal"},
				Token:     access.APIToken{ID: "typed-query", PrincipalID: "principal", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{query, consume}},
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "token omits semantic consume",
			credential: access.APICredential{
				Principal: access.Principal{ID: "principal"},
				Token:     access.APIToken{ID: "typed-query", PrincipalID: "principal", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{query}},
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "token is bound to another principal",
			credential: access.APICredential{
				Principal: access.Principal{ID: "other-principal"},
				Token:     access.APIToken{ID: "typed-query", PrincipalID: "other-principal", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{query, consume}},
			},
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := browserGuardModule(browserGuardRepository{groups: []string{"group_analysts"}}, Principal{ID: "principal"}, true)
			authorizer, err := module.APIGenAuthorizer(
				apigenRuntimeFake{project: identity.ProjectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
				map[string]APIGenOperationContract{"queryModel": {
					OperationID: "queryModel", Method: http.MethodGet, Path: "/api/v1/projects/{project}/semantic-models/{model}",
					Protected: true, AuthzMode: "privilege", Action: string(access.ActionSemanticQuery), Resolver: string(access.TypedOperationResolverSemanticModel),
					Extensions: map[string]any{apiGenObjectScopeExtension: "semantic-model", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_USE"}},
				}},
				APIGenResourceResolvers{SemanticModel: apigenResolver("model", projectgraph.KindSemanticModel)},
			)
			if err != nil {
				t.Fatal(err)
			}
			handler, ok := authorizer.Protect("queryModel", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			if !ok || handler == nil {
				t.Fatal("typed operation was not protected")
			}
			request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/semantic-models/model_sales", map[string]string{"project": "project_demo", "model": "model_sales"})
			request = request.WithContext(WithAPICredential(request.Context(), test.credential))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d body = %q, want %d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestAPIGenTypedResourceAuthorizationRequiresEveryResolvedTarget(t *testing.T) {
	identity, snapshot := typedResourceSnapshot(t, []typedGrantSpec{
		{ID: "principal-one", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"}, Actions: []access.Action{access.ActionSemanticRead}, ResourceID: "model_one"},
	})
	module := browserGuardModule(browserGuardRepository{}, Principal{ID: "principal"}, true)
	resolver := func(*http.Request, projectgraph.ResourceID) []access.ResourceRef {
		one, _ := access.NewResourceRef("model_one", projectgraph.KindSemanticModel)
		two, _ := access.NewResourceRef("model_two", projectgraph.KindSemanticModel)
		return []access.ResourceRef{one, two}
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: identity.ProjectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
		map[string]APIGenOperationContract{"readModels": {
			OperationID: "readModels", Method: http.MethodGet, Path: "/api/v1/projects/{project}/semantic-models/{model}",
			Protected: true, AuthzMode: "privilege", Action: string(access.ActionSemanticRead), Resolver: string(access.TypedOperationResolverSemanticModel),
			Extensions: map[string]any{apiGenObjectScopeExtension: "semantic-model", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"}},
		}},
		APIGenResourceResolvers{SemanticModel: resolver},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, ok := authorizer.Protect("readModels", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || handler == nil {
		t.Fatal("typed operation was not protected")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/semantic-models/model_one", map[string]string{"project": "project_demo", "model": "model_one"}))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d when second target is not granted", recorder.Code, http.StatusForbidden)
	}
}

type typedGrantSpec struct {
	ID         string
	Subject    access.SubjectRef
	Actions    []access.Action
	ResourceID string
}

func typedResourceSnapshot(t *testing.T, specs []typedGrantSpec) (projectgraph.ServingIdentity, accesssnapshot.AuthorizationSnapshot) {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_typed")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model_sales", Kind: projectgraph.KindSemanticModel, Name: "model_sales"},
		{ID: "model_one", Kind: projectgraph.KindSemanticModel, Name: "model_one"},
		{ID: "model_two", Kind: projectgraph.KindSemanticModel, Name: "model_two"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	grants := make([]accesssnapshot.Grant, 0, len(specs))
	for _, spec := range specs {
		pairs := make([]access.PermissionPair, 0, len(spec.Actions))
		for _, action := range spec.Actions {
			resourceID := spec.ResourceID
			if resourceID == "" {
				resourceID = "model_sales"
			}
			resource, err := access.NewResourceRef(projectgraph.ResourceID(resourceID), projectgraph.KindSemanticModel)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := access.NewExactPermissionPair(action, identity.ProjectID, resource)
			if err != nil {
				t.Fatal(err)
			}
			pairs = append(pairs, pair)
		}
		grant, err := accesssnapshot.NewTypedGrant(spec.ID, spec.ID, spec.Subject, pairs)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	return identity, snapshot
}
