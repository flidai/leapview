package composectl

import (
	"bytes"
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
)

type qualificationRoleBindingResponse struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	SubjectType    string   `json:"subjectType"`
	SubjectID      string   `json:"subjectId"`
	Role           string   `json:"role"`
	Capabilities   []string `json:"capabilities"`
	PolicyRevision int64    `json:"policyRevision"`
	PolicyDigest   string   `json:"policyDigest"`
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

func bootstrapQualificationRoleBindings(ctx context.Context, client *http.Client, target, projectID, environment, token, administratorID, reviewerID string) (int64, string, error) {
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
	reviewer := access.RoleBinding{
		ID: "qualification-reviewer-" + reviewerID, Name: "Qualification reviewer",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: reviewerID}, Role: access.ProjectRoleAdmin,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin),
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

	created, err := createQualificationRoleBinding(ctx, client, endpoint, token, reviewer, initial.PolicyRevision)
	if err != nil {
		return 0, "", err
	}
	if created.PolicyRevision != initial.PolicyRevision+1 {
		return 0, "", fmt.Errorf("qualification reviewer policy revision is %d, want %d", created.PolicyRevision, initial.PolicyRevision+1)
	}
	final, finalBindings, err := retrieveQualificationRoleBindingPolicy(ctx, client, endpoint, projectID, environment, token)
	if err != nil {
		return 0, "", err
	}
	if final.PolicyRevision != created.PolicyRevision || final.PolicyDigest != created.PolicyDigest {
		return 0, "", errors.New("qualification authorization policy identity does not match role-binding result")
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

func retrieveQualificationRoleBindingPolicy(ctx context.Context, client *http.Client, endpoint, projectID, environment, token string) (qualificationRoleBindingListResponse, []access.RoleBinding, error) {
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
	bindings, err := validateQualificationRoleBindingPolicy(policy, projectID, environment)
	if err != nil {
		return qualificationRoleBindingListResponse{}, nil, err
	}
	return policy, bindings, nil
}

func validateQualificationRoleBindingPolicy(policy qualificationRoleBindingListResponse, projectID, environment string) ([]access.RoleBinding, error) {
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
		role, err := access.ParseProjectRole(item.Role)
		if err != nil {
			return nil, fmt.Errorf("qualification authorization policy binding %q role: %w", item.ID, err)
		}
		capabilities := make([]access.Capability, len(item.Capabilities))
		for index, capability := range item.Capabilities {
			capabilities[index] = access.Capability(capability)
		}
		binding := access.RoleBinding{ID: item.ID, Name: item.Name, Subject: subject, Role: role, Capabilities: capabilities}
		if err := access.ValidateAuthorizationRoleBinding(binding); err != nil {
			return nil, fmt.Errorf("qualification authorization policy binding %q: %w", item.ID, err)
		}
		bindings = append(bindings, binding)
	}
	canonicalDigest, err := access.AuthorizationPolicyDigest(scope, bindings)
	if err != nil {
		return nil, fmt.Errorf("canonicalize qualification authorization policy: %w", err)
	}
	if canonicalDigest != policy.PolicyDigest {
		return nil, errors.New("qualification authorization policy digest does not match canonical policy identity")
	}
	return bindings, nil
}

func createQualificationRoleBinding(ctx context.Context, client *http.Client, endpoint, token string, binding access.RoleBinding, expectedRevision int64) (qualificationRoleBindingResponse, error) {
	body, err := json.Marshal(map[string]any{
		"id": binding.ID, "name": binding.Name, "subjectType": string(binding.Subject.Kind), "subjectId": binding.Subject.ID,
		"role": string(binding.Role), "expectedRevision": expectedRevision,
	})
	if err != nil {
		return qualificationRoleBindingResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return qualificationRoleBindingResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "qualification-policy-reviewer-"+binding.Subject.ID)
	response, err := client.Do(request)
	if err != nil {
		return qualificationRoleBindingResponse{}, fmt.Errorf("create qualification role binding %q: %w", binding.ID, err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil {
		return qualificationRoleBindingResponse{}, fmt.Errorf("read qualification role binding %q response: %w", binding.ID, readErr)
	}
	if closeErr != nil {
		return qualificationRoleBindingResponse{}, fmt.Errorf("close qualification role binding %q response: %w", binding.ID, closeErr)
	}
	if response.StatusCode != http.StatusCreated {
		return qualificationRoleBindingResponse{}, fmt.Errorf("create qualification role binding %q returned HTTP %d: %s", binding.ID, response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var created qualificationRoleBindingResponse
	if err := json.Unmarshal(responseBody, &created); err != nil {
		return qualificationRoleBindingResponse{}, fmt.Errorf("decode qualification role binding %q: %w", binding.ID, err)
	}
	if created.ID != binding.ID || created.Name != binding.Name || created.SubjectType != string(binding.Subject.Kind) || created.SubjectID != binding.Subject.ID || created.Role != string(binding.Role) || len(created.Capabilities) != len(binding.Capabilities) {
		return qualificationRoleBindingResponse{}, fmt.Errorf("qualification role binding %q returned incompatible policy evidence", binding.ID)
	}
	if err := platformdigest.ValidateSHA256Identity(created.PolicyDigest); err != nil {
		return qualificationRoleBindingResponse{}, fmt.Errorf("qualification role binding %q returned an invalid policy digest: %w", binding.ID, err)
	}
	for index, capability := range binding.Capabilities {
		if created.Capabilities[index] != string(capability) {
			return qualificationRoleBindingResponse{}, fmt.Errorf("qualification role binding %q returned non-canonical role capabilities", binding.ID)
		}
	}
	return created, nil
}

func qualificationAdministratorBound(bindings []access.RoleBinding, principalID string) bool {
	for _, binding := range bindings {
		if binding.Subject.Kind == access.SubjectKindPrincipal && binding.Subject.ID == principalID &&
			(binding.Role == access.ProjectRoleOwner || binding.Role == access.ProjectRoleAdmin) {
			return true
		}
	}
	return false
}

func sameQualificationRoleBinding(left, right access.RoleBinding) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Subject != right.Subject || left.Role != right.Role || len(left.Capabilities) != len(right.Capabilities) {
		return false
	}
	for index := range left.Capabilities {
		if left.Capabilities[index] != right.Capabilities[index] {
			return false
		}
	}
	return true
}
