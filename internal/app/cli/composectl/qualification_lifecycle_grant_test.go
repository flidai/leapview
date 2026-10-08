package composectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/stretchr/testify/require"
)

func TestQualificationLifecycleUploadGrantIsOptInAndPartOfPublicationPolicy(t *testing.T) {
	const principal = "principal:author"
	scope := access.AuthorizationPolicyScope{TargetID: "target:qualification", ProjectID: qualificationProjectID, Environment: "prod"}
	admin := qualificationAdminBinding(t, "administrator", "", principal, scope.ProjectID)
	grant, err := qualificationRecoveryUploadGrant(scope.ProjectID, principal)
	require.NoError(t, err)
	before, err := access.AuthorizationPolicyDigest(scope, []access.RoleBinding{admin})
	require.NoError(t, err)
	after, err := access.AuthorizationPolicyDigest(scope, []access.RoleBinding{admin}, grant)
	require.NoError(t, err)
	var applied atomic.Bool
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer administrator-test-token", r.Header.Get("Authorization"))
		revision, digest := int64(3), before
		if applied.Load() {
			revision, digest = 4, after
		}
		switch r.URL.Path {
		case "/api/v1/projects/" + scope.ProjectID + "/role-bindings":
			require.NoError(t, json.NewEncoder(w).Encode(qualificationRoleBindingListResponse{
				Items:    []qualificationRoleBindingResponse{qualificationBindingResponse(admin, revision, digest)},
				TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment,
				PolicyRevision: revision, PolicyDigest: digest,
			}))
		case "/api/v1/projects/" + scope.ProjectID + "/grants":
			require.True(t, applied.Load())
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"targetId": scope.TargetID, "projectId": scope.ProjectID, "environment": scope.Environment,
				"policyRevision": revision, "policyDigest": digest,
				"items": []any{map[string]any{
					"id": grant.ID, "subjectType": grant.Subject.Kind, "subjectId": grant.Subject.ID,
					"resourceKind": grant.Resource.Kind(), "resourceId": grant.Resource.ID(),
					"permissionProfile": grant.PermissionProfile, "permissions": grant.Permissions,
					"policyRevision": revision, "policyDigest": digest,
				}},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	controller, root := bootstrapLifecycleController(t, true)
	var mutations int
	controller.qualificationExecutor = qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		mutations++
		require.Contains(t, request.Arguments, "stage-grant")
		require.Contains(t, request.Arguments, qualificationRecoveryUploadGrantID)
		require.Contains(t, request.Arguments, qualificationManagedConnectionID)
		require.NotContains(t, request.Arguments, qualificationPipelineGrantID)
		require.NotContains(t, request.Arguments, string(access.ActionPipelineRun))
		applied.Store(true)
		return json.Marshal(map[string]any{
			"targetId": scope.TargetID, "projectId": scope.ProjectID, "environment": scope.Environment,
			"policyRevision": 4, "policyDigest": after, "applied": true, "requiresPublication": true,
		})
	})
	options := qualificationAuthoringOptions{BundleRoot: root, FirstPublicationOnly: true, Target: server.URL, ProjectID: scope.ProjectID, Environment: scope.Environment}
	revision, digest, err := controller.stageQualificationAuthoringGrants(t.Context(), options, server.Client(), "administrator-test-token", principal, 3, before)
	require.NoError(t, err)
	require.Equal(t, int64(3), revision)
	require.Equal(t, before, digest)
	require.Zero(t, reads.Load())
	require.Zero(t, mutations)
	options.LifecycleCredentialFile = "private-lifecycle-output"
	revision, digest, err = controller.stageQualificationAuthoringGrants(t.Context(), options, server.Client(), "administrator-test-token", principal, 3, before)
	require.NoError(t, err)
	require.Equal(t, int64(4), revision)
	require.Equal(t, after, digest)
	require.Equal(t, int32(3), reads.Load())
	require.Equal(t, 1, mutations)
}
