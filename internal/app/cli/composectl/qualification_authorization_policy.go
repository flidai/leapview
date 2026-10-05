package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/access"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type qualificationRoleBindingResponse struct {
	ID                string                  `json:"id"`
	Name              string                  `json:"name"`
	SubjectType       string                  `json:"subjectType"`
	SubjectID         string                  `json:"subjectId"`
	Role              string                  `json:"role"`
	PermissionProfile string                  `json:"permissionProfile"`
	Permissions       []access.PermissionPair `json:"permissions"`
	Capabilities      []string                `json:"capabilities"`
	PolicyRevision    int64                   `json:"policyRevision"`
	PolicyDigest      string                  `json:"policyDigest"`
}

type qualificationRoleBindingListResponse struct {
	Items          []qualificationRoleBindingResponse `json:"items"`
	TargetID       string                             `json:"targetId"`
	ProjectID      string                             `json:"projectId"`
	Environment    string                             `json:"environment"`
	PolicyRevision int64                              `json:"policyRevision"`
	PolicyDigest   string                             `json:"policyDigest"`
	Page           struct {
		NextCursor string `json:"nextCursor"`
	} `json:"page"`
}

func validateQualificationAuthoringPolicyEvidence(report qualificationAuthoringReport) error {
	if report.AuthorizationPolicyRevision < 1 {
		return errors.New("authoring report has no authorization policy revision")
	}
	if err := platformdigest.ValidateSHA256Identity(report.AuthorizationPolicyDigest); err != nil {
		return fmt.Errorf("authoring report has an invalid authorization policy digest: %w", err)
	}
	return nil
}

func qualificationReviewerBindingID(reviewerID string) string {
	return "qualification-reviewer-" + reviewerID
}

func bootstrapQualificationRoleBindings(
	ctx context.Context,
	client *http.Client,
	target, projectID, environment, token, administratorID, reviewerID string,
	grantReviewer func(expectedRevision int64) error,
) (int64, string, error) {
	if client == nil {
		return 0, "", errors.New("qualification role-binding client is required")
	}
	endpoint := strings.TrimRight(target, "/") + "/api/v1/projects/" + url.PathEscape(projectID) + "/role-bindings"
	initial, bindings, err := retrieveQualificationRoleBindingPolicy(ctx, client, endpoint, projectID, environment, token)
	if err != nil {
		return 0, "", err
	}
	if !qualificationAdministratorBound(bindings, administratorID) {
		return 0, "", errors.New("qualification administrator is not bound by the bootstrapped policy")
	}
	reviewer, err := access.NewTypedRoleBinding(
		qualificationReviewerBindingID(reviewerID), string(access.PermissionRoleReleaseApprover),
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: reviewerID},
		access.PermissionRoleReleaseApprover, projectgraph.ResourceID(projectID),
	)
	if err != nil {
		return 0, "", fmt.Errorf("construct qualification reviewer role binding: %w", err)
	}
	for _, binding := range bindings {
		if binding.ID != reviewer.ID {
			continue
		}
		if !sameQualificationRoleBinding(binding, reviewer) {
			return 0, "", errors.New("existing qualification reviewer binding is incompatible")
		}
		return initial.PolicyRevision, initial.PolicyDigest, nil
	}

	if grantReviewer == nil {
		return 0, "", errors.New("qualification reviewer browser role command is required")
	}
	if err := grantReviewer(initial.PolicyRevision); err != nil {
		return 0, "", fmt.Errorf("grant qualification reviewer role through browser session: %w", err)
	}
	final, finalBindings, err := retrieveQualificationRoleBindingPolicy(ctx, client, endpoint, projectID, environment, token)
	if err != nil {
		return 0, "", err
	}
	if final.PolicyRevision != initial.PolicyRevision+1 {
		return 0, "", fmt.Errorf("qualification reviewer policy revision is %d, want %d", final.PolicyRevision, initial.PolicyRevision+1)
	}
	if !qualificationAdministratorBound(finalBindings, administratorID) {
		return 0, "", errors.New("qualification administrator binding disappeared from the current policy")
	}
	reviewerFound := false
	for _, binding := range finalBindings {
		if binding.ID == reviewer.ID {
			reviewerFound = sameQualificationRoleBinding(binding, reviewer)
			break
		}
	}
	if !reviewerFound {
		return 0, "", errors.New("qualification reviewer is not bound by the current policy")
	}
	return final.PolicyRevision, final.PolicyDigest, nil
}

func retrieveQualificationRoleBindingPolicy(ctx context.Context, client *http.Client, endpoint, projectID, environment, token string, grants ...access.AuthorizationGrant) (qualificationRoleBindingListResponse, []access.RoleBinding, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?limit=200", nil)
	if err != nil {
		return qualificationRoleBindingListResponse{}, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return qualificationRoleBindingListResponse{}, nil, fmt.Errorf("retrieve qualification authorization policy: %w", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil {
		return qualificationRoleBindingListResponse{}, nil, fmt.Errorf("read qualification authorization policy response: %w", readErr)
	}
	if closeErr != nil {
		return qualificationRoleBindingListResponse{}, nil, fmt.Errorf("close qualification authorization policy response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return qualificationRoleBindingListResponse{}, nil, fmt.Errorf("retrieve qualification authorization policy returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var policy qualificationRoleBindingListResponse
	if err := json.Unmarshal(responseBody, &policy); err != nil {
		return qualificationRoleBindingListResponse{}, nil, fmt.Errorf("decode qualification authorization policy response: %w", err)
	}
	bindings, err := validateQualificationRoleBindingPolicy(policy, projectID, environment, grants...)
	if err != nil {
		return qualificationRoleBindingListResponse{}, nil, err
	}
	return policy, bindings, nil
}

func validateQualificationRoleBindingPolicy(policy qualificationRoleBindingListResponse, projectID, environment string, grants ...access.AuthorizationGrant) ([]access.RoleBinding, error) {
	if policy.Page.NextCursor != "" {
		return nil, errors.New("qualification authorization policy response is paginated")
	}
	if policy.ProjectID != projectID || policy.Environment != environment || policy.PolicyRevision < 1 {
		return nil, errors.New("qualification authorization policy scope or revision is incompatible")
	}
	if err := platformdigest.ValidateSHA256Identity(policy.PolicyDigest); err != nil {
		return nil, fmt.Errorf("qualification authorization policy returned an invalid policy digest: %w", err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: policy.TargetID, ProjectID: policy.ProjectID, Environment: policy.Environment}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return nil, fmt.Errorf("qualification authorization policy returned an invalid scope: %w", err)
	}
	bindings := make([]access.RoleBinding, 0, len(policy.Items))
	seen := make(map[string]struct{}, len(policy.Items))
	for _, item := range policy.Items {
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, fmt.Errorf("qualification authorization policy returned duplicate binding %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.PolicyRevision != policy.PolicyRevision || item.PolicyDigest != policy.PolicyDigest {
			return nil, fmt.Errorf("qualification authorization policy returned stale binding %q", item.ID)
		}
		subject, err := access.NewSubjectRef(access.SubjectKind(item.SubjectType), item.SubjectID)
		if err != nil {
			return nil, fmt.Errorf("qualification authorization policy binding %q subject: %w", item.ID, err)
		}
		if item.PermissionProfile != access.PermissionCatalogProfile || len(item.Capabilities) != 0 {
			return nil, fmt.Errorf("qualification authorization policy binding %q has no typed permission profile", item.ID)
		}
		binding, err := access.NewTypedRoleBinding(item.ID, item.Name, subject, access.PermissionRole(item.Role), projectgraph.ResourceID(projectID))
		if err != nil {
			return nil, fmt.Errorf("qualification authorization policy binding %q role: %w", item.ID, err)
		}
		if !samePermissionPairs(item.Permissions, binding.Permissions) {
			return nil, fmt.Errorf("qualification authorization policy binding %q returned incompatible typed permissions", item.ID)
		}
		bindings = append(bindings, binding)
	}
	canonicalDigest, err := access.AuthorizationPolicyDigest(scope, bindings, grants...)
	if err != nil {
		return nil, fmt.Errorf("canonicalize qualification authorization policy: %w", err)
	}
	if canonicalDigest != policy.PolicyDigest {
		return nil, errors.New("qualification authorization policy digest does not match canonical policy identity")
	}
	return bindings, nil
}

func qualificationAdministratorBound(bindings []access.RoleBinding, principalID string) bool {
	for _, binding := range bindings {
		if binding.Subject.Kind == access.SubjectKindPrincipal && binding.Subject.ID == principalID &&
			binding.PermissionRole == access.PermissionRoleProjectAdmin {
			return true
		}
	}
	return false
}

func sameQualificationRoleBinding(left, right access.RoleBinding) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Subject == right.Subject &&
		left.PermissionProfile == right.PermissionProfile && left.PermissionRole == right.PermissionRole &&
		samePermissionPairs(left.Permissions, right.Permissions)
}

func samePermissionPairs(left, right []access.PermissionPair) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
