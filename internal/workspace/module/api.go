package module

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/workspace"
	workspaceapi "github.com/flidai/leapview/internal/workspace/api"
	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
)

func (m *Module) GetWorkspaceAdministration(w http.ResponseWriter, r *http.Request, workspaceID string) {
	if m == nil || m.readModel == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace service is unavailable", nil)
		return
	}
	reader, ok := m.readModel.(workspace.AdministrationReadModel)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "WORKSPACE_ADMINISTRATION_UNAVAILABLE", "Workspace administration is unavailable", nil)
		return
	}
	environment := m.runtimeEnvironment
	if m.handler.Environment != nil {
		environment = m.handler.Environment(r)
	}
	state, err := reader.AdministrationByID(r.Context(), workspace.WorkspaceID(workspaceID), environment)
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			apitransport.WriteProblem(w, r, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "Workspace not found", nil)
			return
		}
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "WORKSPACE_ADMINISTRATION_FAILED", "Workspace administration could not be loaded", nil)
		return
	}

	response := workspaceAdministrationResponse(state)
	var accessService access.WorkspaceAccessService
	var accessErr error
	if m.handler.ReadModel.AccessService != nil {
		accessService, accessErr = m.handler.ReadModel.AccessService()
	}
	if accessErr != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "WORKSPACE_ACCESS_FAILED", "Workspace access details could not be loaded", nil)
		return
	}
	if accessService != nil {
		response.Owner, response.Administrators, err = workspaceAdministrationAccess(r, accessService, workspaceID)
		if err != nil {
			apitransport.WriteProblem(w, r, http.StatusInternalServerError, "WORKSPACE_ACCESS_FAILED", "Workspace access details could not be loaded", nil)
			return
		}
	}
	response.Capabilities = m.workspaceAdministrationCapabilities(r, accessService, workspaceID)
	response.Links = workspaceAdministrationLinks(workspaceID, state.ProjectID, response.Capabilities)
	apitransport.WriteJSON(w, http.StatusOK, response)
}

func workspaceAdministrationResponse(state workspace.AdministrationState) workspaceapi.WorkspaceAdministrationResponse {
	return workspaceapi.WorkspaceAdministrationResponse{
		Workspace: workspaceapi.WorkspaceResponse{
			ID: string(state.Workspace.ID), Title: state.Workspace.Title, Description: state.Workspace.Description,
			ActiveServingStateID: string(state.Workspace.ActiveServingStateID),
			CreatedAt:            state.Workspace.CreatedAt, UpdatedAt: state.Workspace.UpdatedAt,
		},
		Administrators: []workspaceapi.WorkspaceAdministrationSubjectResponse{},
		Runtime: workspaceapi.WorkspaceAdministrationRuntimeResponse{
			Environment: state.Environment, ActiveServingStateID: string(state.Workspace.ActiveServingStateID),
			ActiveServingStateStatus: state.ActiveServingStateStatus, ActiveServingStateSince: state.ActiveServingStateSince,
			ProjectID: state.ProjectID, CurrentDeploymentID: state.CurrentDeploymentID,
			CurrentDeploymentStatus: state.CurrentDeploymentStatus, CurrentDeploymentSince: state.CurrentDeploymentSince,
			CurrentReleaseID: state.CurrentReleaseID,
		},
	}
}

func workspaceAdministrationAccess(r *http.Request, service access.WorkspaceAccessService, workspaceID string) (*workspaceapi.WorkspaceAdministrationSubjectResponse, []workspaceapi.WorkspaceAdministrationSubjectResponse, error) {
	var owner *workspaceapi.WorkspaceAdministrationSubjectResponse
	object, err := service.GetSecurableObject(r.Context(), access.WorkspaceObject(workspaceID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	if object.OwnerPrincipalID != "" {
		principal, principalErr := service.PrincipalByID(r.Context(), object.OwnerPrincipalID)
		if principalErr != nil && !errors.Is(principalErr, sql.ErrNoRows) {
			return nil, nil, principalErr
		}
		value := administrationPrincipalSubject(principal, object.OwnerPrincipalID, access.RoleOwner)
		owner = &value
	}

	bindings, err := service.ListRoleBindings(r.Context(), workspaceID)
	if err != nil {
		return nil, nil, err
	}
	administrators := make([]workspaceapi.WorkspaceAdministrationSubjectResponse, 0)
	for _, binding := range bindings {
		if binding.Role != access.RoleOwner && binding.Role != access.RoleAdmin {
			continue
		}
		displayName := firstAdministrationValue(binding.DisplayName, binding.Email, binding.GroupName, binding.SubjectID)
		administrators = append(administrators, workspaceapi.WorkspaceAdministrationSubjectResponse{
			SubjectType: string(binding.SubjectType), SubjectID: binding.SubjectID,
			Email: binding.Email, DisplayName: displayName, Role: binding.Role,
		})
	}
	sort.Slice(administrators, func(i, j int) bool {
		if administrators[i].Role != administrators[j].Role {
			return administrators[i].Role < administrators[j].Role
		}
		if administrators[i].SubjectType != administrators[j].SubjectType {
			return administrators[i].SubjectType < administrators[j].SubjectType
		}
		return administrators[i].SubjectID < administrators[j].SubjectID
	})
	return owner, administrators, nil
}

func administrationPrincipalSubject(principal access.Principal, fallbackID, role string) workspaceapi.WorkspaceAdministrationSubjectResponse {
	id := firstAdministrationValue(principal.ID, fallbackID)
	return workspaceapi.WorkspaceAdministrationSubjectResponse{
		SubjectType: string(access.SubjectPrincipal), SubjectID: id, Email: principal.Email,
		DisplayName: firstAdministrationValue(principal.DisplayName, principal.Email, id), Role: role,
	}
}

func (m *Module) workspaceAdministrationCapabilities(r *http.Request, service access.WorkspaceAccessService, workspaceID string) workspaceapi.WorkspaceAdministrationCapabilitiesResponse {
	capabilities := workspaceapi.WorkspaceAdministrationCapabilitiesResponse{}
	var principal workspacehttp.Principal
	var hasPrincipal bool
	if m.handler.ReadModel.CurrentPrincipal != nil {
		principal, hasPrincipal = m.handler.ReadModel.CurrentPrincipal(r)
	}
	if !m.handler.ReadModel.AuthConfigured || (hasPrincipal && principal.DevBypass) {
		return workspaceapi.WorkspaceAdministrationCapabilitiesResponse{
			ManageWorkspace: true, ManageAccess: true, ManagePublications: true, ManageConnections: true,
			ViewManagedData: true, IngestManagedData: true, PublishReleases: true, RequestDeployments: true,
			ViewDeployments: true, UseAgent: true, ViewAgent: true,
		}
	}
	if service == nil || !hasPrincipal {
		return capabilities
	}
	has := func(privilege access.Privilege) bool {
		if m.currentCredential != nil {
			if credential, ok := m.currentCredential(r); ok && !access.TokenAllows(credential.Token, workspaceID, privilege) {
				return false
			}
		}
		decision, err := service.Authorize(r.Context(), principal.ID, privilege, access.WorkspaceObject(workspaceID))
		return err == nil && decision.Allowed
	}
	capabilities.ManageWorkspace = has(access.PrivilegeManageWorkspace)
	capabilities.ManageAccess = has(access.PrivilegeManageGrants)
	capabilities.ManagePublications = has(access.PrivilegeManagePublications)
	capabilities.ManageConnections = has(access.PrivilegeManageConnectionMetadata)
	capabilities.ViewManagedData = has(access.PrivilegeViewData)
	capabilities.IngestManagedData = has(access.PrivilegeIngestData)
	capabilities.PublishReleases = has(access.PrivilegePublishRelease)
	capabilities.RequestDeployments = has(access.PrivilegeRequestDeployment)
	capabilities.ViewDeployments = has(access.PrivilegeViewItem)
	capabilities.UseAgent = has(access.PrivilegeUseAgent)
	capabilities.ViewAgent = has(access.PrivilegeViewAgent)
	return capabilities
}

func workspaceAdministrationLinks(workspaceID, projectID string, capabilities workspaceapi.WorkspaceAdministrationCapabilitiesResponse) workspaceapi.WorkspaceAdministrationLinksResponse {
	workspacePath := "/api/v1/workspaces/" + url.PathEscape(workspaceID)
	links := workspaceapi.WorkspaceAdministrationLinksResponse{
		Self: workspacePath + "/administration", Workspace: workspacePath,
	}
	if capabilities.ManageAccess {
		links.Groups = workspacePath + "/groups"
		links.Roles = workspacePath + "/roles"
		links.RoleBindings = workspacePath + "/role-bindings"
		links.Grants = workspacePath + "/grants"
	}
	if capabilities.ManagePublications {
		links.Publications = workspacePath + "/dashboard-publications"
	}
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		projectPath := "/api/v1/projects/" + url.PathEscape(projectID)
		if capabilities.ViewManagedData {
			links.ManagedConnections = projectPath + "/connections"
		}
		if capabilities.PublishReleases {
			links.Releases = projectPath + "/releases"
		}
		if capabilities.ViewDeployments {
			links.Deployments = projectPath + "/deployments"
		}
	}
	if capabilities.ViewAgent {
		links.AgentConversations = "/api/v1/agent/conversations"
	}
	return links
}

func firstAdministrationValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (m *Module) GetWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) {
	if m == nil || m.handler.ReadModel.WorkspaceRepository == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace service is unavailable", nil)
		return
	}
	repo, err := m.handler.ReadModel.WorkspaceRepository()
	if err != nil || repo == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "WORKSPACE_SERVICE_UNAVAILABLE", "Workspace service is unavailable", nil)
		return
	}
	row, err := repo.ByID(r.Context(), workspace.WorkspaceID(workspaceID))
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "WORKSPACE_NOT_FOUND", "Workspace not found", nil)
		return
	}
	item := workspaceapi.WorkspaceResponse{
		ID: string(row.ID), Title: row.Title, Description: row.Description,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.ActiveServingStateID != "" {
		item.ActiveServingStateID = string(row.ActiveServingStateID)
	}
	apitransport.WriteJSON(w, http.StatusOK, item)
}
