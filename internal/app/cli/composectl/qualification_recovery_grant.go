package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/access"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const qualificationRecoveryUploadGrantID = "qualification-recovery-upload"

func qualificationRecoveryUploadPermissions(projectID string) ([]access.PermissionPair, error) {
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return nil, err
	}
	connection, err := access.NewResourceRef(
		projectgraph.ResourceID(qualificationManagedConnectionID), projectgraph.KindConnection,
	)
	if err != nil {
		return nil, err
	}
	permissions := make([]access.PermissionPair, 0, 2)
	for _, action := range []access.Action{access.ActionConnectionRead, access.ActionConnectionUpload} {
		pair, err := access.NewExactPermissionPair(action, project, connection)
		if err != nil {
			return nil, fmt.Errorf("qualification recovery upload permission %s: %w", action, err)
		}
		permissions = append(permissions, pair)
	}
	return permissions, nil
}

func qualificationRecoveryUploadGrant(projectID, principalID string) (access.AuthorizationGrant, error) {
	resource, err := access.NewResourceRef(
		projectgraph.ResourceID(qualificationManagedConnectionID), projectgraph.KindConnection,
	)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	permissions, err := qualificationRecoveryUploadPermissions(projectID)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	grant := access.AuthorizationGrant{
		ID:       qualificationRecoveryUploadGrantID,
		Subject:  access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
		Resource: resource, PermissionProfile: access.PermissionCatalogProfile,
		Permissions: permissions,
	}
	if err := access.ValidateAuthorizationGrant(grant); err != nil {
		return access.AuthorizationGrant{}, fmt.Errorf("qualification recovery upload grant: %w", err)
	}
	return grant, nil
}

func qualificationPipelineRunGrant(projectID, principalID string) (access.AuthorizationGrant, error) {
	resource, err := access.NewResourceRef(
		projectgraph.ResourceID(qualificationRefreshPipelineID), projectgraph.KindPipeline,
	)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	pair, err := qualificationPipelineRunPermission(projectID)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	return access.AuthorizationGrant{
		ID:       qualificationPipelineGrantID,
		Subject:  access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
		Resource: resource, PermissionProfile: access.PermissionCatalogProfile,
		Permissions: []access.PermissionPair{pair},
	}, nil
}

func (c *Controller) stageQualificationRecoveryUploadGrant(
	ctx context.Context,
	options qualificationAuthoringOptions,
	client *http.Client,
	token, principalID string,
	revision int64,
	digest string,
) (int64, string, error) {
	pipelineGrant, err := qualificationPipelineRunGrant(options.ProjectID, principalID)
	if err != nil {
		return 0, "", err
	}
	return c.stageQualificationUploadGrant(ctx, options, client, token, principalID, revision, digest, pipelineGrant)
}

func (c *Controller) stageQualificationUploadGrant(ctx context.Context, options qualificationAuthoringOptions, client *http.Client, token, principalID string, revision int64, digest string, previous ...access.AuthorizationGrant) (int64, string, error) {
	if ctx == nil || client == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(principalID) == "" {
		return 0, "", errors.New("qualification recovery grant staging inputs are required")
	}
	if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
		return 0, "", fmt.Errorf("qualification pipeline policy digest: %w", err)
	}
	grant, err := qualificationRecoveryUploadGrant(options.ProjectID, principalID)
	if err != nil {
		return 0, "", err
	}
	endpoint := strings.TrimRight(options.Target, "/") + "/api/v1/projects/" + url.PathEscape(options.ProjectID) + "/role-bindings"
	current, bindings, err := retrieveQualificationRoleBindingPolicy(ctx, client, endpoint, options.ProjectID, options.Environment, token, previous...)
	if err != nil {
		return 0, "", err
	}
	if current.PolicyRevision != revision || current.PolicyDigest != digest || !qualificationAdministratorBound(bindings, principalID) {
		return 0, "", errors.New("qualification pipeline grant is not the current exact policy")
	}
	output, err := c.qualificationCompose(
		ctx, options.BundleRoot, "exec", "-T", "leapview",
		"leapview", "admin", "access", "stage-grant", "--project", options.ProjectID,
		"--id", qualificationRecoveryUploadGrantID, "--principal", principalID,
		"--resource", qualificationManagedConnectionID, "--kind", string(projectgraph.KindConnection),
		"--action", string(access.ActionConnectionRead),
		"--action", string(access.ActionConnectionUpload),
		"--expected-revision", strconv.FormatInt(revision, 10),
		"--operation-id", qualificationRecoveryUploadGrantID, "--apply",
	)
	if err != nil {
		return 0, "", fmt.Errorf("stage exact qualification recovery upload grant: %w", err)
	}
	var staged struct {
		TargetID            string `json:"targetId"`
		ProjectID           string `json:"projectId"`
		Environment         string `json:"environment"`
		PolicyRevision      int64  `json:"policyRevision"`
		PolicyDigest        string `json:"policyDigest"`
		Applied             bool   `json:"applied"`
		RequiresPublication bool   `json:"requiresPublication"`
	}
	if err := json.Unmarshal(output, &staged); err != nil {
		return 0, "", fmt.Errorf("decode staged recovery upload grant: %w", err)
	}
	if !staged.Applied || !staged.RequiresPublication || staged.ProjectID != options.ProjectID ||
		staged.Environment != options.Environment || staged.PolicyRevision != revision+1 {
		return 0, "", errors.New("staged recovery upload grant has unexpected policy evidence")
	}
	if err := platformdigest.ValidateSHA256Identity(staged.PolicyDigest); err != nil {
		return 0, "", fmt.Errorf("staged recovery upload grant has an invalid policy digest: %w", err)
	}
	expected := append(append([]access.AuthorizationGrant(nil), previous...), grant)
	if err := readQualificationRecoveryOwnerGrants(
		ctx, client, options.Target, options.ProjectID, options.Environment, token,
		staged.TargetID, staged.PolicyRevision, staged.PolicyDigest, expected...,
	); err != nil {
		return 0, "", err
	}
	policy, bindings, err := retrieveQualificationRoleBindingPolicy(
		ctx, client, endpoint, options.ProjectID, options.Environment, token, expected...,
	)
	if err != nil {
		return 0, "", err
	}
	if policy.TargetID != staged.TargetID || policy.PolicyRevision != staged.PolicyRevision ||
		policy.PolicyDigest != staged.PolicyDigest || !qualificationAdministratorBound(bindings, principalID) {
		return 0, "", errors.New("recovery upload grant is absent from the canonical policy digest")
	}
	return policy.PolicyRevision, policy.PolicyDigest, nil
}

func readQualificationRecoveryOwnerGrants(
	ctx context.Context,
	client *http.Client,
	target, projectID, environment, token, targetID string,
	revision int64,
	digest string,
	expected ...access.AuthorizationGrant,
) error {
	if client == nil || strings.TrimSpace(token) == "" || len(expected) == 0 {
		return errors.New("qualification owner grant readback inputs are required")
	}
	if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
		return fmt.Errorf("qualification owner grant policy digest: %w", err)
	}
	endpoint := strings.TrimRight(target, "/") + "/api/v1/projects/" + url.PathEscape(projectID) + "/grants?limit=200"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("qualification owner grant readback returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Items []struct {
			ID                string                  `json:"id"`
			Name              string                  `json:"name"`
			SubjectType       string                  `json:"subjectType"`
			SubjectID         string                  `json:"subjectId"`
			ResourceKind      string                  `json:"resourceKind"`
			ResourceID        string                  `json:"resourceId"`
			Capability        string                  `json:"capability"`
			PermissionProfile string                  `json:"permissionProfile"`
			Permissions       []access.PermissionPair `json:"permissions"`
			PolicyRevision    int64                   `json:"policyRevision"`
			PolicyDigest      string                  `json:"policyDigest"`
		} `json:"items"`
		TargetID       string `json:"targetId"`
		ProjectID      string `json:"projectId"`
		Environment    string `json:"environment"`
		PolicyRevision int64  `json:"policyRevision"`
		PolicyDigest   string `json:"policyDigest"`
		Page           struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return fmt.Errorf("decode qualification owner grant readback: %w", err)
	}
	if len(result.Items) != len(expected) || result.Page.NextCursor != "" || result.TargetID != targetID ||
		result.ProjectID != projectID || result.Environment != environment ||
		result.PolicyRevision != revision || result.PolicyDigest != digest {
		return errors.New("qualification owner grant list scope or revision differs from staged policy")
	}
	expectedByID := make(map[string]access.AuthorizationGrant, len(expected))
	for _, grant := range expected {
		if _, duplicate := expectedByID[grant.ID]; duplicate || grant.ID == "" {
			return errors.New("qualification owner grant expectations contain a duplicate or empty ID")
		}
		expectedByID[grant.ID] = grant
	}
	seen := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		grant, ok := expectedByID[item.ID]
		if !ok {
			return errors.New("qualification owner grant list contains an unexpected grant")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return errors.New("qualification owner grant list contains a duplicate grant")
		}
		seen[item.ID] = struct{}{}
		if item.Name != grant.Name || item.SubjectType != string(grant.Subject.Kind) || item.SubjectID != grant.Subject.ID ||
			item.ResourceKind != string(grant.Resource.Kind()) || item.ResourceID != string(grant.Resource.ID()) ||
			item.Capability != string(grant.Capability) || item.PermissionProfile != grant.PermissionProfile ||
			!sameQualificationPermissionPairSet(item.Permissions, grant.Permissions) ||
			item.PolicyRevision != revision || item.PolicyDigest != digest {
			return fmt.Errorf("qualification owner grant %q differs from exact staged authority", item.ID)
		}
	}
	return nil
}

func sameQualificationPermissionPairSet(left, right []access.PermissionPair) bool {
	if len(left) != len(right) {
		return false
	}
	leftSorted := append([]access.PermissionPair(nil), left...)
	rightSorted := append([]access.PermissionPair(nil), right...)
	sort.Slice(leftSorted, func(i, j int) bool { return leftSorted[i].Key() < leftSorted[j].Key() })
	sort.Slice(rightSorted, func(i, j int) bool { return rightSorted[i].Key() < rightSorted[j].Key() })
	for index := range leftSorted {
		if leftSorted[index] != rightSorted[index] {
			return false
		}
	}
	return true
}
