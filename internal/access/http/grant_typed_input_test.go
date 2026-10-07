package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

// The generic legacy grant route must not turn project.access.manage into
// arbitrary typed authority. Typed policy staging remains an offline operator
// operation; bounded delegation has its own separate public contracts.
func TestGrantCommandRejectsUnsupportedTypedAuthority(t *testing.T) {
	for _, test := range []struct {
		action access.Action
		kind   projectgraph.Kind
		id     projectgraph.ResourceID
	}{
		{access.ActionConnectionUpload, projectgraph.KindConnection, "connection:sample"},
		{access.ActionSemanticQuery, projectgraph.KindSemanticModel, "semantic-model:sales"},
	} {
		t.Run(string(test.action), func(t *testing.T) {
			resource, err := access.NewResourceRef(test.id, test.kind)
			require.NoError(t, err)
			pair, err := access.NewExactPermissionPair(test.action, "project:demo", resource)
			require.NoError(t, err)
			body, err := json.Marshal(map[string]any{
				"id": "self-grant", "resourceKind": test.kind, "resourceId": test.id,
				"subjectType": "principal", "subjectId": "project-admin",
				"permissionProfile": access.PermissionCatalogProfile, "permissions": []access.PermissionPair{pair}, "expectedRevision": 4,
			})
			require.NoError(t, err)
			repo := &grantRepositoryStub{}
			handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, AuthorizationPolicyTargetID: "target", AuthorizationPolicyEnvironment: "dev"}
			request := withProjectRoute(httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:demo/grants", strings.NewReader(string(body))), "project:demo")
			ctx, _, err := accessgen.BeginGenCreateGrantCommand(request.Context(), accessgen.GenCreateGrantCommandInvocation{Surface: apigencommand.SurfaceAPI, Project: "project:demo", IdempotencyKey: "self-grant"})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			NewAPIGenDispatcher(handler).CreateGrant(response, request.WithContext(ctx), "project:demo", accessgen.GenCreateGrantHeaders{IdempotencyKey: "self-grant"})
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Zero(t, repo.calls, "unsupported typed authority must never reach the policy writer")
			require.Empty(t, repo.audit.Action, "rejected authority must not record a successful grant mutation")
		})
	}
}
