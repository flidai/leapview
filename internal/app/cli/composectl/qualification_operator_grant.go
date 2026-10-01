package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const qualificationPipelineGrantID = "qualification-pipeline-run"

func qualificationPipelineRunPermission(projectID string) (access.PermissionPair, error) {
	resource, err := access.NewResourceRef(projectgraph.ResourceID(qualificationRefreshPipelineID), projectgraph.KindPipeline)
	if err != nil {
		return access.PermissionPair{}, err
	}
	return access.NewExactPermissionPair(access.ActionPipelineRun, projectgraph.ResourceID(projectID), resource)
}

func (c *Controller) stageQualificationPipelineGrant(ctx context.Context, options qualificationAuthoringOptions, client *http.Client, token, principalID string, revision int64) (int64, string, error) {
	output, err := c.qualificationCompose(ctx, options.BundleRoot, "exec", "-T", "leapview",
		"leapview", "admin", "access", "stage-grant", "--project", options.ProjectID,
		"--id", qualificationPipelineGrantID, "--principal", principalID,
		"--resource", qualificationRefreshPipelineID, "--kind", string(projectgraph.KindPipeline),
		"--action", string(access.ActionPipelineRun), "--expected-revision", strconv.FormatInt(revision, 10),
		"--operation-id", qualificationPipelineGrantID, "--apply")
	if err != nil {
		return 0, "", fmt.Errorf("stage explicit qualification pipeline authority: %w", err)
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
		return 0, "", fmt.Errorf("decode staged qualification grant: %w", err)
	}
	if !staged.Applied || !staged.RequiresPublication || staged.ProjectID != options.ProjectID || staged.Environment != options.Environment || staged.PolicyRevision != revision+1 {
		return 0, "", errors.New("staged qualification pipeline grant has unexpected policy evidence")
	}
	grant, err := readQualificationPipelineGrant(ctx, client, options.Target, options.ProjectID, options.Environment, token, principalID, staged.TargetID, staged.PolicyRevision, staged.PolicyDigest)
	if err != nil {
		return 0, "", err
	}
	endpoint := strings.TrimRight(options.Target, "/") + "/api/v1/projects/" + url.PathEscape(options.ProjectID) + "/role-bindings"
	policy, bindings, err := retrieveQualificationRoleBindingPolicy(ctx, client, endpoint, options.ProjectID, options.Environment, token, grant)
	if err != nil {
		return 0, "", err
	}
	if policy.TargetID != staged.TargetID || policy.PolicyRevision != staged.PolicyRevision || policy.PolicyDigest != staged.PolicyDigest || !qualificationAdministratorBound(bindings, principalID) {
		return 0, "", errors.New("staged qualification grant did not match canonical policy readback")
	}
	return policy.PolicyRevision, policy.PolicyDigest, nil
}

func readQualificationPipelineGrant(ctx context.Context, client *http.Client, target, projectID, environment, token, principalID, targetID string, revision int64, digest string) (access.AuthorizationGrant, error) {
	endpoint := strings.TrimRight(target, "/") + "/api/v1/projects/" + url.PathEscape(projectID) + "/grants?limit=200"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return access.AuthorizationGrant{}, fmt.Errorf("qualification grant readback returned HTTP %d", response.StatusCode)
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
		return access.AuthorizationGrant{}, err
	}
	if len(result.Items) != 1 || result.Page.NextCursor != "" || result.TargetID != targetID || result.ProjectID != projectID || result.Environment != environment || result.PolicyRevision != revision || result.PolicyDigest != digest {
		return access.AuthorizationGrant{}, errors.New("qualification grant list scope or revision differs from the staged policy")
	}
	item := result.Items[0]
	pair, err := qualificationPipelineRunPermission(projectID)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	if item.ID != qualificationPipelineGrantID || item.Name != "" || item.SubjectType != string(access.SubjectKindPrincipal) || item.SubjectID != principalID || item.ResourceID != qualificationRefreshPipelineID || item.ResourceKind != string(projectgraph.KindPipeline) || item.Capability != "" || item.PermissionProfile != access.PermissionCatalogProfile || len(item.Permissions) != 1 || item.Permissions[0] != pair || item.PolicyRevision != revision || item.PolicyDigest != digest {
		return access.AuthorizationGrant{}, errors.New("qualification grant differs from the exact intended pipeline authority")
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(item.ResourceID), projectgraph.KindPipeline)
	if err != nil {
		return access.AuthorizationGrant{}, err
	}
	return access.AuthorizationGrant{ID: item.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, Resource: resource, PermissionProfile: item.PermissionProfile, Permissions: item.Permissions}, nil
}
