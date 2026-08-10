package module

import (
	"context"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

func (m *Module) PublicationsConfigured() bool {
	return m != nil && m.publications != nil && m.publicationService != nil
}

func (m *Module) ResolvePublic(ctx context.Context, publicID string) (publication.Publication, error) {
	if m == nil || m.publicationService == nil {
		return publication.Publication{}, publication.ErrNotFound
	}
	return m.publicationService.ResolvePublic(ctx, publicID)
}

func (m *Module) PublicationByPublicID(ctx context.Context, publicID string) (publication.Publication, error) {
	if m == nil || m.publications == nil {
		return publication.Publication{}, publication.ErrNotFound
	}
	return m.publications.GetByPublicID(ctx, publicID)
}

func (m *Module) MutatePublication(ctx context.Context, workspaceID, name, actorID string, action publication.Action) (publication.Publication, error) {
	if m == nil || m.publicationService == nil || m.recordPublicationCommandAudit == nil {
		return publication.Publication{}, publication.ErrNotFound
	}
	row, err := m.publicationService.Mutate(ctx, workspaceID, name, actorID, action)
	if err != nil {
		return row, err
	}
	operationID, ok := publicationOperationID(action)
	if !ok {
		return row, publication.ErrConflict
	}
	executor, err := apigencommand.NewExecutor(dashboardgen.GetAPIGenCommandRuntimeContract, m.logger)
	if err != nil {
		return row, err
	}
	err = executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, _ apigencommand.Contract) error {
			return m.recordPublicationCommandAudit(ctx, publicationCommandAuditInput{
				operationID: operationID, workspaceID: strings.TrimSpace(workspaceID), principalID: strings.TrimSpace(actorID),
				targetID: strings.TrimSpace(row.ID), surface: "ui",
			})
		},
		LogMessage: "best-effort dashboard publication command audit failed",
		LogAttributes: []slog.Attr{
			slog.String("workspace_id", strings.TrimSpace(workspaceID)),
			slog.String("principal_id", strings.TrimSpace(actorID)),
			slog.String("target_id", strings.TrimSpace(row.ID)),
		},
	})
	return row, err
}

func (m *Module) AllPublications(ctx context.Context) ([]publication.Publication, error) {
	if m == nil || m.publications == nil {
		return nil, publication.ErrNotFound
	}
	return m.publications.ListAll(ctx)
}

func (m *Module) PublicationEvents(ctx context.Context, publicationID string) ([]publication.Event, error) {
	if m == nil || m.publications == nil {
		return nil, publication.ErrNotFound
	}
	return m.publications.ListEvents(ctx, publicationID)
}

func (m *Module) PublicationDTO(row publication.Publication) dashboardapi.PublicationResponse {
	return m.dashboardPublicationDTO(row)
}

func (m *Module) ListDashboardPublications(w http.ResponseWriter, r *http.Request, workspaceID string) {
	if m == nil || m.publications == nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PUBLICATIONS_NOT_AVAILABLE", "Dashboard publications are not available", nil)
		return
	}
	rows, err := m.publications.List(r.Context(), workspaceID)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "PUBLICATION_LIST_FAILED", "Dashboard publications could not be loaded", nil)
		return
	}
	items := make([]dashboardapi.PublicationResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, m.dashboardPublicationDTO(row))
	}
	writeJSON(w, http.StatusOK, dashboardapi.PublicationListResponse{Items: items})
}

func (m *Module) GetDashboardPublication(w http.ResponseWriter, r *http.Request, workspaceID, name string) {
	row, ok := m.dashboardPublication(w, r, workspaceID, name)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, m.dashboardPublicationDTO(row))
}

func (m *Module) SuspendDashboardPublication(w http.ResponseWriter, r *http.Request, workspaceID, name string) {
	m.mutateDashboardPublication(w, r, workspaceID, name, publication.ActionSuspend)
}

func (m *Module) ResumeDashboardPublication(w http.ResponseWriter, r *http.Request, workspaceID, name string) {
	m.mutateDashboardPublication(w, r, workspaceID, name, publication.ActionResume)
}

func (m *Module) RotateDashboardPublication(w http.ResponseWriter, r *http.Request, workspaceID, name string) {
	m.mutateDashboardPublication(w, r, workspaceID, name, publication.ActionRotate)
}

func (m *Module) mutateDashboardPublication(w http.ResponseWriter, r *http.Request, workspaceID, name string, action publication.Action) {
	operationID, operationKnown := publicationOperationID(action)
	if !operationKnown {
		m.writePublicationMutation(w, r, "", publication.Publication{}, publication.ErrConflict)
		return
	}
	if m == nil || m.publicationService == nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, publication.ErrNotFound)
		return
	}
	// Publication commands require the generated success audit contract. Reject
	// an incompletely constructed module before performing the state transition.
	if m.recordPublicationCommandAudit == nil {
		m.writePublicationMutation(w, r, operationID, publication.Publication{}, errPublicationCommandAuditUnavailable)
		return
	}
	actor := ""
	if m.currentActor != nil {
		actor = m.currentActor(r)
	}
	row, err := m.publicationService.Mutate(r.Context(), workspaceID, name, actor, action)
	if err == nil {
		logger := m.logger
		if logger == nil {
			logger = slog.Default()
		}
		executor, executorErr := apigencommand.NewExecutor(dashboardgen.GetAPIGenCommandRuntimeContract, logger)
		if executorErr != nil {
			err = executorErr
		} else {
			err = executor.Execute(r.Context(), operationID, apigencommand.Execution{
				BestEffortAudit: func(ctx context.Context, _ apigencommand.Contract) error {
					return m.recordPublicationCommandAudit(ctx, publicationAuditRequestInput(r, operationID, workspaceID, actor, row.ID))
				},
				LogMessage: "best-effort dashboard publication command audit failed",
				LogAttributes: []slog.Attr{
					slog.String("workspace_id", strings.TrimSpace(workspaceID)),
					slog.String("principal_id", strings.TrimSpace(actor)),
					slog.String("target_type", "dashboard_publication"),
					slog.String("target_id", strings.TrimSpace(row.ID)),
					slog.String("request_id", firstPublicationHeader(r, "X-Request-Id", "X-Request-ID")),
				},
			})
		}
	}
	m.writePublicationMutation(w, r, operationID, row, err)
}

func (m *Module) dashboardPublication(w http.ResponseWriter, r *http.Request, workspaceID, name string) (publication.Publication, bool) {
	if m == nil || m.publications == nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PUBLICATION_NOT_FOUND", "Dashboard publication not found", nil)
		return publication.Publication{}, false
	}
	row, err := m.publications.Get(r.Context(), workspaceID, name)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, publication.ErrNotFound) {
			status = http.StatusNotFound
		}
		apitransport.WriteProblem(w, r, status, "PUBLICATION_NOT_FOUND", "Dashboard publication not found", nil)
		return publication.Publication{}, false
	}
	return row, true
}

func (m *Module) writePublicationMutation(w http.ResponseWriter, r *http.Request, operationID string, row publication.Publication, err error) {
	if err != nil {
		var logger *slog.Logger
		if m != nil {
			logger = m.logger
		}
		apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, logger, operationID, dashboardgen.GetAPIGenCommandFailureContracts, err)
		return
	}
	writeJSON(w, http.StatusOK, m.dashboardPublicationDTO(row))
}

func (m *Module) dashboardPublicationDTO(row publication.Publication) dashboardapi.PublicationResponse {
	publicPath := "/public/dashboards/" + row.PublicID
	embedPath := "/embed/dashboards/" + row.PublicID
	publicURL := m.absolutePublicURL(publicPath)
	embedURL := m.absolutePublicURL(embedPath)
	iframe := `<iframe src="` + html.EscapeString(embedURL) + `" title="` + html.EscapeString(row.Name) + `" loading="lazy" sandbox="allow-scripts allow-same-origin" referrerpolicy="no-referrer"></iframe>`
	dto := dashboardapi.PublicationResponse{
		Name: row.Name, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, Dashboard: row.Dashboard,
		DefaultPage: row.DefaultPage, Status: dashboardapi.PublicationStatus(row.Status()), Configured: row.Configured,
		AllowedOrigins: append([]string(nil), row.AllowedOrigins...), PublicURL: publicURL, EmbedURL: embedURL, IFrameSnippet: iframe,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	optionalString := func(value string) *string {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		copy := value
		return &copy
	}
	dto.ActiveServingStateID = optionalString(row.ServingStateID)
	dto.ConfiguredAt = optionalString(row.ConfiguredAt)
	dto.DisabledAt = optionalString(row.DisabledAt)
	dto.SuspendedAt = optionalString(row.SuspendedAt)
	dto.SuspendedBy = optionalString(row.SuspendedBy)
	dto.RotatedAt = optionalString(row.RotatedAt)
	return dto
}

func (m *Module) absolutePublicURL(path string) string {
	if m == nil || m.publicURL == "" {
		return path
	}
	return m.publicURL + path
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	apitransport.WriteJSON(w, status, value)
}
