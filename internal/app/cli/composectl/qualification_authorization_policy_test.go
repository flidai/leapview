package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func qualificationAdminBinding(t *testing.T, id, name, principalID, projectID string) access.RoleBinding {
	t.Helper()
	binding, err := access.NewTypedRoleBinding(
		id, name, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
		access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID),
	)
	require.NoError(t, err)
	return binding
}

func qualificationReviewerBinding(t *testing.T, id, reviewerID, projectID string) access.RoleBinding {
	t.Helper()
	binding, err := access.NewTypedRoleBinding(
		id, string(access.PermissionRoleReleaseApprover),
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: reviewerID},
		access.PermissionRoleReleaseApprover, projectgraph.ResourceID(projectID),
	)
	require.NoError(t, err)
	return binding
}

func qualificationBindingResponse(binding access.RoleBinding, revision int64, digest string) qualificationRoleBindingResponse {
	return qualificationRoleBindingResponse{
		ID: binding.ID, Name: binding.Name, SubjectType: string(binding.Subject.Kind), SubjectID: binding.Subject.ID,
		Role: string(binding.PermissionRole), PermissionProfile: binding.PermissionProfile,
		Permissions: access.ClonePermissionPairs(binding.Permissions), PolicyRevision: revision, PolicyDigest: digest,
	}
}

func TestBootstrapQualificationRoleBindingsUsesBrowserGrantAndVerifiesCanonicalPolicy(t *testing.T) {
	administratorID := "10000000-0000-4000-8000-000000000001"
	reviewerID := "10000000-0000-4000-8000-000000000002"
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	roleBindings := []access.RoleBinding{
		qualificationAdminBinding(t, "project-bootstrap-owner", "Project bootstrap owner", administratorID, scope.ProjectID),
		qualificationReviewerBinding(t, qualificationReviewerBindingID(reviewerID), reviewerID, scope.ProjectID),
	}
	firstDigest, err := access.AuthorizationPolicyDigest(scope, roleBindings[:1])
	require.NoError(t, err)
	finalDigest, err := access.AuthorizationPolicyDigest(scope, roleBindings)
	require.NoError(t, err)
	getCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected direct policy mutation %s %s", request.Method, request.URL.EscapedPath())
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		getCount++
		if request.URL.EscapedPath() != "/api/v1/projects/project:test/role-bindings" || request.URL.Query().Get("limit") != "200" {
			t.Errorf("role-binding policy request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer qualification-token" {
			t.Errorf("role-binding policy request has no expected bearer token")
		}
		response.Header().Set("Content-Type", "application/json")
		items := []qualificationRoleBindingResponse{qualificationBindingResponse(roleBindings[0], 1, firstDigest)}
		revision, digest := int64(1), firstDigest
		if getCount == 2 {
			items = []qualificationRoleBindingResponse{
				qualificationBindingResponse(roleBindings[0], 2, finalDigest),
				qualificationBindingResponse(roleBindings[1], 2, finalDigest),
			}
			revision, digest = 2, finalDigest
		}
		_ = json.NewEncoder(response).Encode(qualificationRoleBindingListResponse{
			Items: items, TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation", PolicyRevision: revision, PolicyDigest: digest,
		})
	}))
	defer server.Close()

	grantCount := 0
	revision, digest, err := bootstrapQualificationRoleBindings(
		t.Context(), server.Client(), server.URL, "project:test", "evaluation", "qualification-token", administratorID, reviewerID,
		func(expectedRevision int64) error {
			grantCount++
			require.EqualValues(t, 1, expectedRevision, "browser grant must use the initial API policy revision")
			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, 2, getCount)
	require.Equal(t, 1, grantCount)
	require.EqualValues(t, 2, revision)
	require.Equal(t, finalDigest, digest)
}

func TestBootstrapQualificationRoleBindingsReusesExistingReviewer(t *testing.T) {
	administratorID := "10000000-0000-4000-8000-000000000001"
	reviewerID := "10000000-0000-4000-8000-000000000002"
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	bindings := []access.RoleBinding{
		qualificationAdminBinding(t, "project-bootstrap-owner", "Project bootstrap owner", administratorID, scope.ProjectID),
		qualificationReviewerBinding(t, qualificationReviewerBindingID(reviewerID), reviewerID, scope.ProjectID),
	}
	digest, err := access.AuthorizationPolicyDigest(scope, bindings)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("unexpected replay mutation %s", request.Method)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(qualificationRoleBindingListResponse{
			Items: []qualificationRoleBindingResponse{
				qualificationBindingResponse(bindings[0], 2, digest),
				qualificationBindingResponse(bindings[1], 2, digest),
			},
			TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, PolicyRevision: 2, PolicyDigest: digest,
		})
	}))
	defer server.Close()

	grantCount := 0
	revision, gotDigest, err := bootstrapQualificationRoleBindings(
		t.Context(), server.Client(), server.URL, "project:test", "evaluation", "qualification-token",
		administratorID, reviewerID, func(int64) error {
			grantCount++
			return nil
		},
	)
	require.NoError(t, err)
	require.Zero(t, grantCount, "existing release-approver binding should not be granted again")
	require.EqualValues(t, 2, revision)
	require.Equal(t, digest, gotDigest)
}

func TestBootstrapQualificationRoleBindingsRejectsMalformedPolicyEvidence(t *testing.T) {
	owner := qualificationAdminBinding(t, "project-bootstrap-owner", "Project bootstrap owner", "10000000-0000-4000-8000-000000000001", "project:test")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(qualificationRoleBindingListResponse{
			Items:    []qualificationRoleBindingResponse{qualificationBindingResponse(owner, 1, "sha256:not-a-digest")},
			TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation", PolicyRevision: 1, PolicyDigest: "sha256:not-a-digest",
		})
	}))
	defer server.Close()

	_, _, err := bootstrapQualificationRoleBindings(
		t.Context(), server.Client(), server.URL, "project:test", "evaluation", "qualification-token",
		"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", nil,
	)
	require.ErrorContains(t, err, "invalid policy digest")
}

func TestQualificationOAuthActionsMatchPublicationAndReviewOperations(t *testing.T) {
	require.Equal(t, []access.Action{
		access.ActionProjectAccessRead,
		access.ActionDeliveryRead,
		access.ActionDeliveryPublish,
	}, qualificationAdministratorActions())
	require.Equal(t, []access.Action{
		access.ActionDeliveryRead,
		access.ActionDeliveryApprove,
	}, qualificationReviewerActions())
	var expectedWorkloadActions []access.Action
	for _, role := range []access.PermissionRole{access.PermissionRoleEditor, access.PermissionRoleReleaseOperator} {
		actions, ok := access.PermissionRoleActions(role)
		require.True(t, ok)
		expectedWorkloadActions = append(expectedWorkloadActions, actions...)
	}
	slices.Sort(expectedWorkloadActions)
	expectedWorkloadActions = slices.Compact(expectedWorkloadActions)
	require.Equal(t, expectedWorkloadActions, qualificationWorkloadActions(), "workload token matches the bootstrap owner's Editor and ReleaseOperator roles")
	require.NotContains(t, qualificationWorkloadActions(), access.ActionDashboardPublish, "the bootstrap owner has no Publisher role")
	initial, err := access.InitialProjectPublisherPermissions(projectgraph.ResourceID("project:qualification"))
	require.NoError(t, err)
	workload, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID("project:qualification"), qualificationWorkloadActions())
	require.NoError(t, err)
	for _, pair := range workload {
		require.True(t, access.PermissionSetAllows(initial, pair), "workload pair %s must be within initial owner authority", pair.Key())
	}
}

func TestValidateQualificationAuthoringPolicyEvidenceFailsClosed(t *testing.T) {
	valid := qualificationAuthoringReport{
		AuthorizationPolicyRevision: 2,
		AuthorizationPolicyDigest:   "sha256:" + strings.Repeat("a", 64),
	}
	require.NoError(t, validateQualificationAuthoringPolicyEvidence(valid))

	missingRevision := valid
	missingRevision.AuthorizationPolicyRevision = 0
	require.ErrorContains(t, validateQualificationAuthoringPolicyEvidence(missingRevision), "no authorization policy revision")

	malformedDigest := valid
	malformedDigest.AuthorizationPolicyDigest = "sha256:not-a-digest"
	require.ErrorContains(t, validateQualificationAuthoringPolicyEvidence(malformedDigest), "invalid authorization policy digest")
}
