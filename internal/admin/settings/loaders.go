package settings

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/workspace"
)

// WorkspaceAdministrationReader is implemented by workspace repositories and
// keeps this adapter independent from the HTTP API module.
type WorkspaceAdministrationReader interface {
	workspace.ReadModel
	workspace.AdministrationReadModel
}

// LoadWorkspaceRegistry joins workspace-owned runtime projections with the
// access-owned owner/administrator projection. A missing access service is
// treated as an intentionally redacted owner/admin view.
func LoadWorkspaceRegistry(ctx context.Context, reader WorkspaceAdministrationReader, accessService access.WorkspaceAccessService, environment string) (WorkspaceRegistrySignal, error) {
	if reader == nil {
		return WorkspaceRegistrySignal{Error: "Workspace registry is unavailable."}, errors.New("workspace registry reader is nil")
	}
	summaries, err := reader.List(ctx)
	if err != nil {
		return WorkspaceRegistrySignal{Error: err.Error()}, err
	}
	result := WorkspaceRegistrySignal{Items: make([]WorkspaceRegistryItemSignal, 0, len(summaries))}
	for _, summary := range summaries {
		state, stateErr := reader.AdministrationByID(ctx, summary.ID, environment)
		if stateErr != nil {
			if errors.Is(stateErr, workspace.ErrNotFound) {
				continue
			}
			return WorkspaceRegistrySignal{Error: stateErr.Error()}, stateErr
		}
		item := WorkspaceSignalFromSummary(summary, environment)
		item.Environment = state.Environment
		item.ActiveServingStateID = string(state.Workspace.ActiveServingStateID)
		item.ServingStateStatus = state.ActiveServingStateStatus
		item.ServingStateSince = state.ActiveServingStateSince
		item.ProjectID = state.ProjectID
		item.CurrentDeploymentID = state.CurrentDeploymentID
		item.DeploymentStatus = state.CurrentDeploymentStatus
		item.DeploymentSince = state.CurrentDeploymentSince
		item.CurrentReleaseID = state.CurrentReleaseID
		item.Links = workspaceLinks(item, state)
		if accessService != nil {
			owner, admins, accessErr := workspaceSubjects(ctx, accessService, string(summary.ID))
			if accessErr != nil {
				return WorkspaceRegistrySignal{Error: accessErr.Error()}, accessErr
			}
			item.Owner, item.Administrators = owner, admins
		}
		result.Items = append(result.Items, item)
	}
	SortWorkspaceItems(result.Items)
	if len(result.Items) == 0 {
		result.Empty = "No workspaces are available."
	}
	return result, nil
}

func workspaceSubjects(ctx context.Context, service access.WorkspaceAccessService, id string) (*WorkspaceSubjectSignal, []WorkspaceSubjectSignal, error) {
	object, err := service.GetSecurableObject(ctx, access.WorkspaceObject(id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// A workspace can legitimately have no owner yet. Other errors should be
		// surfaced so a partial access projection is not mistaken for truth.
		return nil, nil, err
	}
	var owner *WorkspaceSubjectSignal
	if object.OwnerPrincipalID != "" {
		principal, principalErr := service.PrincipalByID(ctx, object.OwnerPrincipalID)
		if principalErr != nil && !errors.Is(principalErr, sql.ErrNoRows) && !strings.Contains(strings.ToLower(principalErr.Error()), "no rows") {
			return nil, nil, principalErr
		}
		if principal.ID != "" {
			owner = &WorkspaceSubjectSignal{SubjectType: string(access.SubjectPrincipal), SubjectID: principal.ID,
				Email: principal.Email, DisplayName: firstNonEmpty(principal.DisplayName, principal.Email, principal.ID), Role: access.RoleOwner}
		}
	}
	bindings, err := service.ListRoleBindings(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	admins := make([]WorkspaceSubjectSignal, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Role != access.RoleOwner && binding.Role != access.RoleAdmin {
			continue
		}
		admins = append(admins, WorkspaceSubjectSignal{SubjectType: string(binding.SubjectType), SubjectID: binding.SubjectID,
			Email: binding.Email, DisplayName: firstNonEmpty(binding.DisplayName, binding.Email, binding.GroupName, binding.SubjectID), Role: binding.Role})
	}
	sort.SliceStable(admins, func(i, j int) bool {
		if admins[i].Role != admins[j].Role {
			return admins[i].Role < admins[j].Role
		}
		return admins[i].SubjectID < admins[j].SubjectID
	})
	return owner, admins, nil
}

func workspaceLinks(item WorkspaceRegistryItemSignal, state workspace.AdministrationState) WorkspaceLinksSignal {
	id := url.PathEscape(item.ID)
	links := WorkspaceLinksSignal{Self: "/api/v1/workspaces/" + id, Workspace: "/workspaces/" + id}
	project := url.PathEscape(state.ProjectID)
	if strings.TrimSpace(state.ProjectID) == "" {
		return links
	}
	links.Project = "/api/v1/projects/" + project
	if state.CurrentReleaseID != "" {
		links.Release = links.Project + "/releases/" + url.PathEscape(state.CurrentReleaseID)
	}
	if state.CurrentDeploymentID != "" {
		links.Deployment = links.Project + "/deployments/" + url.PathEscape(state.CurrentDeploymentID)
	}
	links.Deployments = links.Project + "/deployments"
	links.Connections = "/connections"
	links.Publications = "/admin/publications"
	links.Agent = "/admin/agent"
	return links
}

// ServicePrincipalSecretReader is the optional metadata extension implemented
// by the production repository. It intentionally does not expose raw secrets.
type ServicePrincipalSecretReader interface {
	ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error)
}

type ServiceAccountReader interface {
	ListServicePrincipals(context.Context) ([]access.Principal, error)
	ServicePrincipalSecretReader
}

type Repository interface {
	access.Repository
	ServicePrincipalSecretReader
}

type ServiceAccountMutator interface {
	CreateServicePrincipal(context.Context, access.ServicePrincipalInput) (access.Principal, error)
	UpdateServicePrincipal(context.Context, string, access.ServicePrincipalInput) (access.Principal, error)
	DeleteServicePrincipal(context.Context, string) error
	CreateServicePrincipalSecret(context.Context, string, access.ServicePrincipalSecretInput) (string, access.ServicePrincipalSecret, error)
	RevokeServicePrincipalSecret(context.Context, string, string) error
}

func LoadServiceAccounts(ctx context.Context, store ServiceAccountReader, selectedID string) (ServiceAccountsSignal, error) {
	if store == nil {
		return ServiceAccountsSignal{Error: "Service account store is unavailable."}, errors.New("service account store is nil")
	}
	principals, err := store.ListServicePrincipals(ctx)
	if err != nil {
		return ServiceAccountsSignal{Error: err.Error()}, err
	}
	result := ServiceAccountsSignal{Items: make([]ServiceAccountSignal, 0, len(principals)), SelectedID: strings.TrimSpace(selectedID)}
	for _, principal := range principals {
		result.Items = append(result.Items, ServiceAccountSignalFromPrincipal(principal))
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		if strings.EqualFold(result.Items[i].DisplayName, result.Items[j].DisplayName) {
			return result.Items[i].ID < result.Items[j].ID
		}
		return strings.ToLower(result.Items[i].DisplayName) < strings.ToLower(result.Items[j].DisplayName)
	})
	if result.SelectedID == "" && len(result.Items) > 0 {
		result.SelectedID = result.Items[0].ID
	}
	if result.SelectedID != "" {
		secrets, secretErr := store.ListServicePrincipalSecrets(ctx, result.SelectedID)
		if secretErr != nil {
			return ServiceAccountsSignal{Items: result.Items, SelectedID: result.SelectedID, Error: secretErr.Error()}, secretErr
		}
		result.Secrets = make([]ServiceAccountSecretSignal, 0, len(secrets))
		for _, secret := range secrets {
			result.Secrets = append(result.Secrets, ServiceAccountSecretSignalFromDomain(secret))
		}
	}
	return result, nil
}

func ServiceAccountsSignalFromPrincipals(principals []access.Principal) ServiceAccountsSignal {
	result := ServiceAccountsSignal{Items: make([]ServiceAccountSignal, 0, len(principals))}
	for _, principal := range principals {
		result.Items = append(result.Items, ServiceAccountSignalFromPrincipal(principal))
	}
	sort.SliceStable(result.Items, func(i, j int) bool { return result.Items[i].ID < result.Items[j].ID })
	return result
}

// LoadAuditLog requests one extra row so callers can safely expose HasMore
// without a count query. Repository implementations apply all filter fields
// and cursor validation.
func LoadAuditLog(ctx context.Context, repository access.Repository, filters AuditLogFilters, pageToken string, limit int) (AuditLogSignal, error) {
	if repository == nil {
		return AuditLogSignal{Error: "Audit log is unavailable."}, errors.New("audit repository is nil")
	}
	filters = NormalizeAuditLogFilters(filters)
	limit = normalizeLimit(limit)
	rows, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: filters.WorkspaceID, PrincipalID: filters.PrincipalID,
		Action: filters.Action, TargetType: filters.TargetType, TargetID: filters.TargetID, From: filters.From, To: filters.To,
		PageToken: strings.TrimSpace(pageToken), Limit: limit + 1})
	if err != nil {
		return AuditLogSignal{Filters: filters, Error: err.Error()}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]AuditEventSignal, 0, len(rows))
	for _, row := range rows {
		items = append(items, AuditEventSignalFromDomain(row))
	}
	result := AuditLogSignal{Items: items, Filters: filters, HasMore: hasMore, LoadedCount: len(items)}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.NextCursor = AuditPageToken(last.CreatedAt, last.ID)
	}
	return result, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
