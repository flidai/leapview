package module

import (
	"context"
	"errors"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectapi "github.com/flidai/leapview/internal/project/api"
	releaseapi "github.com/flidai/leapview/internal/release/api"
)

func (m *Module) GetProject(w http.ResponseWriter, r *http.Request, projectID string) {
	if m == nil || m.catalog == nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "Project not found", nil)
		return
	}
	row, err := m.catalog.GetProject(r.Context(), projectID)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "Project not found", nil)
		return
	}
	item := projectapi.ProjectResponse{ID: projectID, Title: projectID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.LatestReleaseID != "" {
		item.LatestReleaseID = &row.LatestReleaseID
	}
	if row.ActiveDeploymentID != "" {
		item.ActiveDeploymentID = &row.ActiveDeploymentID
	}
	apitransport.WriteJSON(w, http.StatusOK, item)
}

func (m *Module) ListManagedConnections(w http.ResponseWriter, r *http.Request, projectID string, limit *int32, pageToken *string) {
	if m == nil || m.catalog == nil {
		apitransport.WriteJSON(w, http.StatusOK, releaseapi.ManagedConnectionListResponse{Items: []releaseapi.ManagedConnectionResponse{}, Page: releaseapi.PageInfo{}})
		return
	}
	principal, ok := m.currentPrincipal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	if m.api.AuthorizeConnection == nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "CONNECTION_AUTHORIZATION_FAILED", "Connection authorization could not be evaluated", nil)
		return
	}
	rows, err := m.catalog.ListConnections(r.Context(), projectID, m.environment)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "CONNECTION_LIST_FAILED", "Connections could not be loaded", nil)
		return
	}
	items := make([]releaseapi.ManagedConnectionResponse, 0, len(rows))
	for _, row := range rows {
		allowed, err := m.authorizeConnection(r.Context(), principal.ID, projectID, row.ID, access.CapabilityResourceRead)
		if err != nil {
			apitransport.WriteProblem(w, r, http.StatusInternalServerError, "CONNECTION_AUTHORIZATION_FAILED", "Connection authorization could not be evaluated", nil)
			return
		}
		if !allowed {
			continue
		}
		item := releaseapi.ManagedConnectionResponse{ID: row.ID, ProjectID: projectID, Title: row.Title}
		if row.Description != "" {
			item.Description = &row.Description
		}
		if row.ActiveRevisionID != "" {
			item.ActiveRevisionID = &row.ActiveRevisionID
		}
		items = append(items, item)
	}
	page, next, err := apitransport.KeysetPage(items, limit, pageToken, func(item releaseapi.ManagedConnectionResponse) string { return item.ID })
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, releaseapi.ManagedConnectionListResponse{Items: page, Page: releaseapi.PageInfo{NextCursor: next}})
}

func (m *Module) GetManagedConnection(w http.ResponseWriter, r *http.Request, projectID, connectionID string) {
	if m == nil || m.catalog == nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "CONNECTION_NOT_FOUND", "Connection not found", nil)
		return
	}
	principal, ok := m.currentPrincipal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	allowed, err := m.authorizeConnection(r.Context(), principal.ID, projectID, connectionID, access.CapabilityResourceRead)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "CONNECTION_AUTHORIZATION_FAILED", "Connection authorization could not be evaluated", nil)
		return
	}
	if !allowed {
		apitransport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Connection access is forbidden", nil)
		return
	}
	row, err := m.catalog.GetConnection(r.Context(), projectID, connectionID, m.environment)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "CONNECTION_NOT_FOUND", "Connection not found", nil)
		return
	}
	item := releaseapi.ManagedConnectionResponse{ID: connectionID, ProjectID: projectID, Title: row.Title}
	if row.Description != "" {
		item.Description = &row.Description
	}
	if row.ActiveRevisionID != "" {
		item.ActiveRevisionID = &row.ActiveRevisionID
	}
	apitransport.WriteJSON(w, http.StatusOK, item)
}

func (m *Module) authorizeConnection(ctx context.Context, principalID, projectID, connectionID string, capability access.Capability) (bool, error) {
	if m == nil || m.api.AuthorizeConnection == nil {
		return false, errors.New("connection authorization is unavailable")
	}
	return m.api.AuthorizeConnection(ctx, principalID, projectID, connectionID, capability)
}

func (m *Module) ProjectCursorSnapshot(r *http.Request, projectID string) string {
	if m == nil || m.catalog == nil {
		return ""
	}
	row, err := m.catalog.GetProject(r.Context(), projectID)
	if err != nil {
		return ""
	}
	if row.ActiveDeploymentID != "" {
		return "deployment:" + row.ActiveDeploymentID
	}
	if row.LatestReleaseID != "" {
		return "release:" + row.LatestReleaseID
	}
	return ""
}
