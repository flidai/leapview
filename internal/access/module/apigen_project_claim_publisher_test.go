package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestProjectClaimPublisherAcknowledgeBootstrapUsesCurrentTypedPublisher(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, projectID)
	if err != nil {
		t.Fatal(err)
	}
	contract := APIGenOperationContract{
		OperationID: "acknowledgeProjectClaimPublisher", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/project-claim-publisher/acknowledge", Protected: true, AuthzMode: "privilege",
		Action: string(access.ActionProjectAccessManage), Resolver: string(access.TypedOperationResolverProject),
		Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "PROJECT_ADMIN", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{
			"x-authz":                  map[string]any{"mode": "privilege", "privilege": "PROJECT_ADMIN", "action": "project.access.manage", "resolver": "project"},
			apiGenObjectScopeExtension: "project",
		},
	}
	module := browserGuardModule(browserGuardRepository{admin: false}, Principal{ID: "bootstrap-admin"}, true)
	authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{contract.OperationID: contract}, APIGenResourceResolvers{
		Project: apigenResolver("project", projectgraph.KindProjectNamespace),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operationID string, project projectgraph.ResourceID, _ access.Capability) (APIGenBootstrapDecision, error) {
		if operationID != "acknowledgeProjectClaimPublisher" || project != projectID {
			t.Fatalf("bootstrap decision received %q/%q", operationID, project)
		}
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok {
		t.Fatal("publisher acknowledgement route was not protected")
	}
	request := apigenRequest(http.MethodPost, contract.Path, map[string]string{"project": projectID.String()})
	request.Header.Set("Authorization", "Bearer publisher-token")
	request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
		Principal: access.Principal{ID: "bootstrap-admin"},
		Token: access.APIToken{
			ID: "publisher", PrincipalID: "bootstrap-admin", Name: access.InitialProjectClaimPublisherTokenName("claim"),
			PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{manage},
		},
	}))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("typed publisher ACK status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}
}
