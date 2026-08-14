package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	workspacegen "github.com/flidai/leapview/internal/workspace/api/gen"
	"github.com/flidai/leapview/internal/workspace/ui"
	"github.com/go-chi/chi/v5"
)

func (m *Module) UpdateDashboardAppearanceFromUI(w http.ResponseWriter, r *http.Request) {
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), workspacegen.GenOperationUpdateDashboardAppearance); err != nil {
		http.Error(w, "invalid dashboard appearance command", http.StatusBadRequest)
		return
	}
	var signals struct {
		Query   string `json:"entityListQuery"`
		Command struct {
			WorkspaceID string  `json:"workspaceId"`
			DashboardID string  `json:"dashboardId"`
			Icon        *string `json:"icon"`
			Color       *string `json:"color"`
		} `json:"dashboardAppearanceCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	routeWorkspaceID := strings.TrimSpace(chi.URLParam(r, "workspace"))
	commandWorkspaceID := strings.TrimSpace(signals.Command.WorkspaceID)
	if routeWorkspaceID == "" || commandWorkspaceID == "" || routeWorkspaceID != commandWorkspaceID {
		http.Error(w, "dashboard appearance workspace does not match route", http.StatusBadRequest)
		return
	}
	if _, err := m.saveDashboardAppearance(r, routeWorkspaceID, signals.Command.DashboardID, dashboardappearance.Patch{Icon: signals.Command.Icon, Color: signals.Command.Color}, apigencommand.SurfaceUI); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	catalogs, err := m.catalogsWithAppearances(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = pagestream.PatchResponse(w, r, ui.CatalogListPatchForCatalogsQuery(catalogs, signals.Query))
}

func (m *Module) UpdateDashboardAppearance(w http.ResponseWriter, r *http.Request, workspaceID, dashboardID string, verifyUIClaim bool) {
	if m.appearance == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "DASHBOARD_APPEARANCE_UNAVAILABLE", "Dashboard appearance persistence is unavailable.", nil)
		return
	}
	if verifyUIClaim {
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), workspacegen.GenOperationUpdateDashboardAppearance); err != nil {
			http.Error(w, "invalid dashboard appearance command", http.StatusBadRequest)
			return
		}
	}
	workspaceID, dashboardID = strings.TrimSpace(workspaceID), strings.TrimSpace(dashboardID)
	if !m.dashboardExists(workspaceID, dashboardID) {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "DASHBOARD_NOT_FOUND", "Dashboard not found.", nil)
		return
	}
	var body workspacegen.DashboardAppearancePatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid dashboard appearance request", http.StatusBadRequest)
		return
	}
	record, err := m.saveDashboardAppearance(r, workspaceID, dashboardID, dashboardappearance.Patch{Icon: body.Icon, Color: body.Color}, dashboardAppearanceAPISurface(r))
	if err != nil {
		if errors.Is(err, dashboardappearance.ErrInvalid) {
			apitransport.WriteProblem(w, r, http.StatusUnprocessableEntity, "INVALID_DASHBOARD_APPEARANCE", "The selected dashboard appearance is invalid.", nil)
			return
		}
		if strings.Contains(err.Error(), "patch is empty") {
			apitransport.WriteProblem(w, r, http.StatusUnprocessableEntity, "INVALID_DASHBOARD_APPEARANCE", "The selected dashboard appearance is invalid.", nil)
			return
		}
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "DASHBOARD_APPEARANCE_UPDATE_FAILED", "Dashboard appearance could not be updated.", nil)
		return
	}
	resolved := dashboardappearance.Resolve(record.Value)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(workspacegen.DashboardAppearanceResponse{
		Icon: resolved.Icon, Color: resolved.Color, Revision: record.Revision,
	})
}

func (m *Module) saveDashboardAppearance(r *http.Request, workspaceID, dashboardID string, patch dashboardappearance.Patch, surface apigencommand.Surface) (dashboardappearance.Record, error) {
	workspaceID, dashboardID = strings.TrimSpace(workspaceID), strings.TrimSpace(dashboardID)
	if !m.dashboardExists(workspaceID, dashboardID) {
		return dashboardappearance.Record{}, sql.ErrNoRows
	}
	actorID := ""
	if m.currentPrincipal != nil {
		if principal, ok := m.currentPrincipal(r); ok {
			actorID = principal.ID
		}
	}
	invocation := dashboardAppearanceInvocation(r, workspaceID, surface)
	ctx := r.Context()
	if _, started := apigencommand.OperationID(ctx); !started {
		var err error
		ctx, _, err = workspacegen.BeginGenUpdateDashboardAppearanceCommand(ctx, invocation)
		if err != nil {
			return dashboardappearance.Record{}, err
		}
	}
	record, err := m.appearance.ApplyPatch(ctx, dashboardappearance.Key{WorkspaceID: workspaceID, DashboardID: dashboardID}, "", actorID, patch)
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	executor, err := apigencommand.NewExecutor(workspacegen.GetAPIGenCommandRuntimeContract, m.logger)
	if err != nil {
		return record, err
	}
	fields := dashboardAppearancePatchFields(patch)
	err = workspacegen.ExecuteGenUpdateDashboardAppearanceCommand(ctx, executor, invocation, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			return m.recordDashboardAppearanceAudit(ctx, contract, r, workspaceID, dashboardID, actorID, fields)
		},
		LogMessage: "best-effort dashboard appearance command audit failed",
		LogAttributes: []slog.Attr{
			slog.String("workspace_id", workspaceID), slog.String("dashboard_id", dashboardID), slog.String("principal_id", actorID),
		},
	})
	return record, err
}

func (m *Module) recordDashboardAppearanceAudit(ctx context.Context, contract apigencommand.Contract, r *http.Request, workspaceID, dashboardID, actorID string, fields []string) error {
	if m.recordAudit == nil {
		return fmt.Errorf("dashboard appearance command audit is unavailable")
	}
	privilege, ok := access.ParsePrivilege(contract.Privilege)
	if !ok {
		return fmt.Errorf("dashboard appearance command privilege %q is invalid", contract.Privilege)
	}
	metadata, err := workspacegen.EncodeGenUpdateDashboardAppearanceAuditPayload(workspacegen.GenSchemaDashboardAppearanceUpdatedAuditPayload{Fields: fields})
	if err != nil {
		return err
	}
	requestID := firstDashboardAppearanceHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := firstDashboardAppearanceHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	return m.recordAudit(ctx, access.AuditEventInput{
		WorkspaceID: workspaceID, PrincipalID: actorID, Action: contract.AuditAction,
		TargetType: "dashboard", TargetID: dashboardID, Privilege: privilege, Status: "success",
		RequestID: requestID, CorrelationID: correlationID, MetadataJSON: metadata,
	})
}

func dashboardAppearanceInvocation(r *http.Request, workspaceID string, surface apigencommand.Surface) workspacegen.GenUpdateDashboardAppearanceCommandInvocation {
	return workspacegen.GenUpdateDashboardAppearanceCommandInvocation{
		Surface: surface, Workspace: strings.TrimSpace(workspaceID),
		RequestID:     firstDashboardAppearanceHeader(r, "X-Request-Id", "X-Request-ID"),
		CorrelationID: firstDashboardAppearanceHeader(r, "X-Correlation-Id", "X-Correlation-ID"),
	}
}

func dashboardAppearanceAPISurface(r *http.Request) apigencommand.Surface {
	if strings.EqualFold(firstDashboardAppearanceHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client"), string(apigencommand.SurfaceCLI)) {
		return apigencommand.SurfaceCLI
	}
	return apigencommand.SurfaceAPI
}

func dashboardAppearancePatchFields(patch dashboardappearance.Patch) []string {
	fields := make([]string, 0, 2)
	if patch.Icon != nil {
		fields = append(fields, "icon")
	}
	if patch.Color != nil {
		fields = append(fields, "color")
	}
	return fields
}

func firstDashboardAppearanceHeader(r *http.Request, names ...string) string {
	if r == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func (m *Module) dashboardExists(workspaceID, dashboardID string) bool {
	catalog := m.handler.ReadModel.CatalogForWorkspace(workspaceID)
	for _, dashboard := range catalog.Dashboards {
		if dashboard.ID == dashboardID {
			return true
		}
	}
	return false
}
