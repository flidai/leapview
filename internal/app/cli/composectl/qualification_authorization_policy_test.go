package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/stretchr/testify/require"
)

func TestBootstrapQualificationRoleBindingsUsesPublicCASAPI(t *testing.T) {
	administratorID := "10000000-0000-4000-8000-000000000001"
	reviewerID := "10000000-0000-4000-8000-000000000002"
	wantCapabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	capabilities := make([]string, len(wantCapabilities))
	for index, capability := range wantCapabilities {
		capabilities[index] = string(capability)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	roleBindings := []access.RoleBinding{
		{ID: "project-bootstrap-owner", Name: "Project bootstrap owner", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: administratorID}, Role: access.ProjectRoleAdmin, Capabilities: wantCapabilities},
		{ID: "qualification-reviewer-" + reviewerID, Name: "Qualification reviewer", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: reviewerID}, Role: access.ProjectRoleAdmin, Capabilities: wantCapabilities},
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
			items := []qualificationRoleBindingResponse{{ID: roleBindings[0].ID, Name: roleBindings[0].Name, SubjectType: "principal", SubjectID: administratorID, Role: string(access.ProjectRoleAdmin), Capabilities: capabilities, PolicyRevision: 1, PolicyDigest: firstDigest}}
			revision, digest := int64(1), firstDigest
			if getCount == 2 {
				items = []qualificationRoleBindingResponse{
					{ID: roleBindings[0].ID, Name: roleBindings[0].Name, SubjectType: "principal", SubjectID: administratorID, Role: string(access.ProjectRoleAdmin), Capabilities: capabilities, PolicyRevision: 2, PolicyDigest: finalDigest},
					{ID: roleBindings[1].ID, Name: roleBindings[1].Name, SubjectType: "principal", SubjectID: reviewerID, Role: string(access.ProjectRoleAdmin), Capabilities: capabilities, PolicyRevision: 2, PolicyDigest: finalDigest},
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
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode role-binding request: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.ID != roleBindings[1].ID || body.Name != roleBindings[1].Name || body.SubjectType != "principal" || body.SubjectID != reviewerID ||
			body.Role != string(access.ProjectRoleAdmin) || body.ExpectedRevision == nil || *body.ExpectedRevision != 1 || body.Capabilities != nil {
			t.Errorf("role-binding request body = %+v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(response).Encode(qualificationRoleBindingResponse{
			ID: roleBindings[1].ID, Name: roleBindings[1].Name, SubjectType: "principal", SubjectID: reviewerID,
			Role: string(access.ProjectRoleAdmin), Capabilities: capabilities,
			PolicyRevision: 2, PolicyDigest: finalDigest,
		}); err != nil {
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
	wantCapabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	capabilities := make([]string, len(wantCapabilities))
	for index, capability := range wantCapabilities {
		capabilities[index] = string(capability)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	bindings := []access.RoleBinding{
		{ID: "project-bootstrap-owner", Name: "Project bootstrap owner", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: administratorID}, Role: access.ProjectRoleAdmin, Capabilities: wantCapabilities},
		{ID: "qualification-reviewer-" + reviewerID, Name: "Qualification reviewer", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: reviewerID}, Role: access.ProjectRoleAdmin, Capabilities: wantCapabilities},
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
				{ID: bindings[0].ID, Name: bindings[0].Name, SubjectType: "principal", SubjectID: administratorID, Role: "admin", Capabilities: capabilities, PolicyRevision: 2, PolicyDigest: digest},
				{ID: bindings[1].ID, Name: bindings[1].Name, SubjectType: "principal", SubjectID: reviewerID, Role: "admin", Capabilities: capabilities, PolicyRevision: 2, PolicyDigest: digest},
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
	wantCapabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	capabilities := make([]string, len(wantCapabilities))
	for index, capability := range wantCapabilities {
		capabilities[index] = string(capability)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(qualificationRoleBindingListResponse{
			Items: []qualificationRoleBindingResponse{{
				ID: "project-bootstrap-owner", Name: "Project bootstrap owner", SubjectType: "principal", SubjectID: "10000000-0000-4000-8000-000000000001",
				Role: string(access.ProjectRoleAdmin), Capabilities: capabilities, PolicyRevision: 1, PolicyDigest: "sha256:not-a-digest",
			}},
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
