package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func qualificationBindingResponse(binding access.RoleBinding, revision int64, digest string) qualificationRoleBindingResponse {
	return qualificationRoleBindingResponse{
		ID: binding.ID, Name: binding.Name, SubjectType: string(binding.Subject.Kind), SubjectID: binding.Subject.ID,
		Role: string(binding.PermissionRole), PermissionProfile: binding.PermissionProfile,
		Permissions: access.ClonePermissionPairs(binding.Permissions), PolicyRevision: revision, PolicyDigest: digest,
	}
}

func TestBootstrapQualificationRoleBindingsUsesPublicCASAPI(t *testing.T) {
	administratorID := "10000000-0000-4000-8000-000000000001"
	reviewerID := "10000000-0000-4000-8000-000000000002"
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	roleBindings := []access.RoleBinding{
		qualificationAdminBinding(t, "project-bootstrap-owner", "Project bootstrap owner", administratorID, scope.ProjectID),
		qualificationAdminBinding(t, "qualification-reviewer-"+reviewerID, "Qualification reviewer", reviewerID, scope.ProjectID),
	}
	firstDigest, err := access.AuthorizationPolicyDigest(scope, roleBindings[:1])
	require.NoError(t, err)
	finalDigest, err := access.AuthorizationPolicyDigest(scope, roleBindings)
	require.NoError(t, err)
	getCount, postCount := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			getCount++
			if request.URL.EscapedPath() != "/api/v1/projects/project:test/role-bindings" || request.URL.Query().Get("limit") != "200" {
				t.Errorf("role-binding policy request = %s %s", request.Method, request.URL.String())
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
			return
		}
		postCount++
		if postCount > 1 {
			t.Errorf("unexpected role-binding request %d", postCount)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Method != http.MethodPost || request.URL.EscapedPath() != "/api/v1/projects/project:test/role-bindings" {
			t.Errorf("role-binding request = %s %s", request.Method, request.URL.EscapedPath())
		}
		if request.Header.Get("Authorization") != "Bearer qualification-token" || request.Header.Get("Idempotency-Key") != "qualification-policy-reviewer-"+reviewerID {
			t.Errorf("role-binding request headers are incomplete")
		}
		var body struct {
			ID               string          `json:"id"`
			Name             string          `json:"name"`
			SubjectType      string          `json:"subjectType"`
			SubjectID        string          `json:"subjectId"`
			Role             string          `json:"role"`
			ExpectedRevision *int64          `json:"expectedRevision"`
			Capabilities     json.RawMessage `json:"capabilities"`
			Permissions      json.RawMessage `json:"permissions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode role-binding request: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.ID != roleBindings[1].ID || body.Name != roleBindings[1].Name || body.SubjectType != "principal" || body.SubjectID != reviewerID ||
			body.Role != string(access.PermissionRoleProjectAdmin) || body.ExpectedRevision == nil || *body.ExpectedRevision != 1 || body.Capabilities != nil || body.Permissions != nil {
			t.Errorf("role-binding request body = %+v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(response).Encode(qualificationBindingResponse(roleBindings[1], 2, finalDigest)); err != nil {
			t.Errorf("encode role-binding response: %v", err)
		}
	}))
	defer server.Close()

	revision, digest, err := bootstrapQualificationRoleBindings(
		t.Context(), server.Client(), server.URL, "project:test", "evaluation", "qualification-token", administratorID, reviewerID,
	)
	require.NoError(t, err)
	require.Equal(t, 2, getCount)
	require.Equal(t, 1, postCount)
	require.EqualValues(t, 2, revision)
	require.Equal(t, finalDigest, digest)
}

func TestBootstrapQualificationRoleBindingsReusesExistingReviewer(t *testing.T) {
	administratorID := "10000000-0000-4000-8000-000000000001"
	reviewerID := "10000000-0000-4000-8000-000000000002"
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	bindings := []access.RoleBinding{
		qualificationAdminBinding(t, "project-bootstrap-owner", "Project bootstrap owner", administratorID, scope.ProjectID),
		qualificationAdminBinding(t, "qualification-reviewer-"+reviewerID, "Qualification reviewer", reviewerID, scope.ProjectID),
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

	revision, gotDigest, err := bootstrapQualificationRoleBindings(
		t.Context(), server.Client(), server.URL, "project:test", "evaluation", "qualification-token",
		administratorID, reviewerID,
	)
	require.NoError(t, err)
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
		"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002",
	)
	require.ErrorContains(t, err, "invalid policy digest")
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
