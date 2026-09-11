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
	type binding struct {
		id, name, subjectID, idempotencyKey string
		expectedRevision                    int64
	}
	bindings := []binding{
		{id: "qualification-administrator-" + administratorID, name: "Qualification administrator", subjectID: administratorID, idempotencyKey: "qualification-policy-administrator-" + administratorID, expectedRevision: 0},
		{id: "qualification-reviewer-" + reviewerID, name: "Qualification reviewer", subjectID: reviewerID, idempotencyKey: "qualification-policy-reviewer-" + reviewerID, expectedRevision: 1},
	}
	wantCapabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	var final qualificationRoleBindingResponse
	for _, item := range bindings {
		body, err := json.Marshal(map[string]any{"id": item.id, "name": item.name, "subjectType": "principal", "subjectId": item.subjectID, "role": string(access.ProjectRoleAdmin), "expectedRevision": item.expectedRevision})
		if err != nil {
			return 0, "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", item.idempotencyKey)
		response, err := client.Do(request)
		if err != nil {
			return 0, "", fmt.Errorf("create qualification role binding %q: %w", item.id, err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		closeErr := response.Body.Close()
		if readErr != nil {
			return 0, "", fmt.Errorf("read qualification role binding %q response: %w", item.id, readErr)
		}
		if closeErr != nil {
			return 0, "", fmt.Errorf("close qualification role binding %q response: %w", item.id, closeErr)
		}
		if response.StatusCode != http.StatusCreated {
			return 0, "", fmt.Errorf("create qualification role binding %q returned HTTP %d: %s", item.id, response.StatusCode, strings.TrimSpace(string(responseBody)))
		}
		var created qualificationRoleBindingResponse
		if err := json.Unmarshal(responseBody, &created); err != nil {
			return 0, "", fmt.Errorf("decode qualification role binding %q: %w", item.id, err)
		}
		if created.ID != item.id || created.Name != item.name || created.SubjectType != "principal" || created.SubjectID != item.subjectID || created.Role != string(access.ProjectRoleAdmin) || created.PolicyRevision != item.expectedRevision+1 || len(created.Capabilities) != len(wantCapabilities) {
			return 0, "", fmt.Errorf("qualification role binding %q returned incompatible policy evidence", item.id)
		}
		if err := platformdigest.ValidateSHA256Identity(created.PolicyDigest); err != nil {
			return 0, "", fmt.Errorf("qualification role binding %q returned an invalid policy digest: %w", item.id, err)
		}
		for index, capability := range wantCapabilities {
			if created.Capabilities[index] != string(capability) {
				return 0, "", fmt.Errorf("qualification role binding %q returned non-canonical role capabilities", item.id)
			}
		}
		final = created
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?limit=200", nil)
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("retrieve qualification authorization policy: %w", err)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	closeErr := response.Body.Close()
	if readErr != nil {
		return 0, "", fmt.Errorf("read qualification authorization policy response: %w", readErr)
	}
	if closeErr != nil {
		return 0, "", fmt.Errorf("close qualification authorization policy response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("retrieve qualification authorization policy returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var policy qualificationRoleBindingListResponse
	if err := json.Unmarshal(responseBody, &policy); err != nil {
		return 0, "", fmt.Errorf("decode qualification authorization policy response: %w", err)
	}
	if policy.Page.NextCursor != "" {
		return 0, "", errors.New("qualification authorization policy response is paginated")
	}
	if policy.ProjectID != projectID || policy.Environment != environment || policy.PolicyRevision != final.PolicyRevision || policy.PolicyDigest != final.PolicyDigest {
		return 0, "", errors.New("qualification authorization policy identity does not match role-binding result")
	}
	if err := platformdigest.ValidateSHA256Identity(policy.PolicyDigest); err != nil {
		return 0, "", fmt.Errorf("qualification authorization policy returned an invalid policy digest: %w", err)
	}
	if len(policy.Items) != len(bindings) {
		return 0, "", fmt.Errorf("qualification authorization policy returned %d bindings, want %d", len(policy.Items), len(bindings))
	}
	expectedByID := make(map[string]binding, len(bindings))
	for _, item := range bindings {
		expectedByID[item.id] = item
	}
	canonicalBindings := make([]access.RoleBinding, 0, len(policy.Items))
	seen := make(map[string]struct{}, len(policy.Items))
	for _, created := range policy.Items {
		item, ok := expectedByID[created.ID]
		if !ok {
			return 0, "", fmt.Errorf("qualification authorization policy returned unexpected binding %q", created.ID)
		}
		if _, ok := seen[created.ID]; ok {
			return 0, "", fmt.Errorf("qualification authorization policy returned duplicate binding %q", created.ID)
		}
		seen[created.ID] = struct{}{}
		if created.Name != item.name || created.SubjectType != "principal" || created.SubjectID != item.subjectID || created.Role != string(access.ProjectRoleAdmin) || created.PolicyRevision != policy.PolicyRevision || created.PolicyDigest != policy.PolicyDigest || len(created.Capabilities) != len(wantCapabilities) {
			return 0, "", fmt.Errorf("qualification authorization policy returned incompatible binding %q", created.ID)
		}
		capabilities := make([]access.Capability, len(created.Capabilities))
		for index, capability := range created.Capabilities {
			if capability != string(wantCapabilities[index]) {
				return 0, "", fmt.Errorf("qualification authorization policy returned non-canonical role capabilities for %q", created.ID)
			}
			capabilities[index] = access.Capability(capability)
		}
		canonicalBindings = append(canonicalBindings, access.RoleBinding{ID: created.ID, Name: created.Name, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: created.SubjectID}, Role: access.ProjectRoleAdmin, Capabilities: capabilities})
	}
	scope := access.AuthorizationPolicyScope{TargetID: policy.TargetID, ProjectID: policy.ProjectID, Environment: policy.Environment}
	canonicalDigest, err := access.AuthorizationPolicyDigest(scope, canonicalBindings)
	if err != nil {
		return 0, "", fmt.Errorf("canonicalize qualification authorization policy: %w", err)
	}
	if canonicalDigest != policy.PolicyDigest {
		return 0, "", fmt.Errorf("qualification authorization policy digest does not match canonical policy identity")
	}
	return policy.PolicyRevision, policy.PolicyDigest, nil
}
