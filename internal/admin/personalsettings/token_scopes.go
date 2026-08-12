package personalsettings

import (
	"context"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

type tokenPrivilegeDescriptor struct {
	label       string
	description string
	category    string
}

func (s *Service) tokenScopes(ctx context.Context, principalID string) ([]TokenScopeSignal, error) {
	scopes := []TokenScopeSignal{}
	platformPrivileges, err := s.Repository.EffectivePrivileges(ctx, principalID, access.PlatformObject())
	if err != nil {
		return nil, err
	}
	if options := tokenPrivilegeOptions(platformPrivileges); len(options) > 0 {
		scopes = append(scopes, TokenScopeSignal{
			Kind: "platform", Label: "All workspaces and product",
			Description: "Access can apply across every current and future workspace. Prefer a single workspace.", Privileges: options,
		})
	}
	if s.Workspaces == nil {
		return scopes, nil
	}
	rows, err := s.Workspaces.List(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return tokenWorkspaceLabel(rows[i].Title, string(rows[i].ID)) < tokenWorkspaceLabel(rows[j].Title, string(rows[j].ID))
	})
	for _, row := range rows {
		workspaceID := string(row.ID)
		privileges, privilegesErr := s.Repository.EffectivePrivileges(ctx, principalID, access.WorkspaceObject(workspaceID))
		if privilegesErr != nil {
			return nil, privilegesErr
		}
		options := tokenPrivilegeOptions(privileges)
		if len(options) == 0 {
			continue
		}
		description := strings.TrimSpace(row.Description)
		if description == "" {
			description = "Access limited to this workspace."
		}
		scopes = append(scopes, TokenScopeSignal{
			Kind: "workspace", WorkspaceID: workspaceID,
			Label: tokenWorkspaceLabel(row.Title, workspaceID), Description: description, Privileges: options,
		})
	}
	return scopes, nil
}

func tokenWorkspaceLabel(title, id string) string {
	if label := strings.TrimSpace(title); label != "" {
		return label
	}
	return strings.TrimSpace(id)
}

func tokenPrivilegeOptions(effective []access.Privilege) []TokenPrivilegeSignal {
	allowed := make(map[access.Privilege]struct{}, len(effective))
	for _, privilege := range effective {
		allowed[privilege] = struct{}{}
	}
	options := make([]TokenPrivilegeSignal, 0, len(effective))
	for _, privilege := range access.KnownPrivileges() {
		if _, ok := allowed[privilege]; !ok {
			continue
		}
		descriptor := describeTokenPrivilege(privilege)
		options = append(options, TokenPrivilegeSignal{
			Value: string(privilege), Label: descriptor.label,
			Description: descriptor.description, Category: descriptor.category,
		})
	}
	return options
}

var tokenPrivilegeDescriptors = map[access.Privilege]tokenPrivilegeDescriptor{
	access.PrivilegeUseWorkspace:             {"Use workspace", "Open and use the workspace.", "Workspace"},
	access.PrivilegeViewItem:                 {"View content", "View dashboards and other workspace content.", "Workspace"},
	access.PrivilegeEditItem:                 {"Edit content", "Create and update workspace content.", "Workspace"},
	access.PrivilegeManageItem:               {"Manage content", "Delete and administer workspace content.", "Workspace"},
	access.PrivilegeQueryData:                {"Query data", "Run governed queries against workspace data.", "Data"},
	access.PrivilegePreviewData:              {"Preview data", "Preview source and model data.", "Data"},
	access.PrivilegeTestDataPolicy:           {"Test data policies", "Evaluate data-policy behavior.", "Data"},
	access.PrivilegeRefreshData:              {"Refresh data", "Start and manage data refreshes.", "Data"},
	access.PrivilegeViewData:                 {"View managed data", "View managed-data metadata and revisions.", "Data"},
	access.PrivilegeIngestData:               {"Ingest data", "Upload and ingest managed data.", "Data"},
	access.PrivilegeAuthorProject:            {"Author project", "Create and synchronize project candidates.", "Projects and releases"},
	access.PrivilegePublishRelease:           {"Publish releases", "Publish project releases.", "Projects and releases"},
	access.PrivilegeReviewCandidate:          {"Review candidates", "Review project candidates before deployment.", "Projects and releases"},
	access.PrivilegeRequestDeployment:        {"Request deployments", "Create deployments and request approval.", "Projects and releases"},
	access.PrivilegeApproveDeployment:        {"Approve deployments", "Approve or revoke deployment approvals.", "Projects and releases"},
	access.PrivilegeDeploy:                   {"Deploy", "Run deployment operations.", "Projects and releases"},
	access.PrivilegeActivateDeployment:       {"Activate deployments", "Promote a deployment to active serving state.", "Projects and releases"},
	access.PrivilegeVerifyDeployment:         {"Verify deployments", "Run deployment verification checks.", "Projects and releases"},
	access.PrivilegeRollbackDeployment:       {"Rollback deployments", "Restore a previous serving state.", "Projects and releases"},
	access.PrivilegeUseAgent:                 {"Use agent", "Start and continue agent conversations.", "Agent"},
	access.PrivilegeViewAgent:                {"View agent", "View agent status and conversations.", "Agent"},
	access.PrivilegeManageConnectionMetadata: {"Manage connections", "Create and update connection metadata.", "Connections"},
	access.PrivilegeTestConnection:           {"Test connections", "Run connection tests.", "Connections"},
	access.PrivilegeViewConnectionHealth:     {"View connection health", "Inspect connection availability and health.", "Connections"},
	access.PrivilegeManagePublications:       {"Manage publications", "Configure and control public dashboards.", "Administration"},
	access.PrivilegeManageGrants:             {"Manage access", "Manage members, groups, roles, and grants.", "Administration"},
	access.PrivilegeViewAudit:                {"View audit log", "Read security and administration events.", "Administration"},
	access.PrivilegeManageWorkspace:          {"Manage workspace", "Manage workspace ownership and administration.", "Administration"},
	access.PrivilegeManagePlatform:           {"Manage product", "Manage product-wide configuration and resources.", "Administration"},
}

func describeTokenPrivilege(privilege access.Privilege) tokenPrivilegeDescriptor {
	if descriptor, ok := tokenPrivilegeDescriptors[privilege]; ok {
		return descriptor
	}
	return tokenPrivilegeDescriptor{label: string(privilege), description: "Use this API capability.", category: "Other"}
}
