package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	materializehttp "github.com/flidai/leapview/internal/refresh/http"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

type EventStore interface {
	jobs.EventAppender
	jobhttp.EventReader
}

func (m *Module) recordRunCreated(ctx context.Context, run refreshrun.RunRecord) error {
	response, ok := materializehttp.PipelineRunResponseFor(run)
	if !ok {
		return fmt.Errorf("refresh service returned a non-pipeline run")
	}
	return jobs.AppendJSONEvent(ctx, m.events, "refresh", run.ID, refreshQueuedAuditAction, response)
}

func (m *Module) recordRunCreatedBestEffort(ctx context.Context, run refreshrun.RunRecord) error {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, string(refreshgen.GenOperationCreateRefreshRun), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if contract.AuditAction != refreshQueuedAuditAction {
				return fmt.Errorf("refresh audit action %q does not match %q", contract.AuditAction, refreshQueuedAuditAction)
			}
			return m.recordRunCreated(ctx, run)
		},
		LogMessage:    "refresh audit failed",
		LogAttributes: []slog.Attr{slog.String("refresh_run_id", run.ID)},
	})
}

func (m *Module) runFinished(after func(context.Context, refreshrun.RunRecord)) func(context.Context, refreshrun.JobRecord) {
	return func(ctx context.Context, job refreshrun.JobRecord) {
		run, err := m.GetRun(ctx, job.WorkspaceID, job.RunID)
		if err != nil {
			return
		}
		if after != nil {
			after(ctx, run)
		}
		response, ok := materializehttp.PipelineRunResponseFor(run)
		if !ok || m.events == nil {
			return
		}
		_ = jobs.AppendJSONEvent(ctx, m.events, "refresh", run.ID, "refresh."+run.Status, response)
	}
}

func (m *Module) CreateRefreshRun(w http.ResponseWriter, r *http.Request, _ string) {
	m.handler.CreateRun(w, r)
}

func (m *Module) ListRefreshRuns(w http.ResponseWriter, r *http.Request, _ string) {
	m.handler.ListRuns(w, r)
}

func (m *Module) GetRefreshRun(w http.ResponseWriter, r *http.Request, _, _ string) {
	m.handler.GetRun(w, r)
}

func (m *Module) CancelRefreshRun(w http.ResponseWriter, r *http.Request, workspaceID, runID string) {
	if m == nil || m.runs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "REFRESH_SERVICE_UNAVAILABLE", "Refresh service is unavailable", nil)
		return
	}
	resolvedWorkspaceID := m.workspaceID(workspaceID)
	prior, err := m.GetRun(r.Context(), resolvedWorkspaceID, runID)
	if err != nil || prior.Environment != m.environment {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	publicPrior, ok := materializehttp.PipelineRunResponseFor(prior)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	allowed, err := m.authorize(r, resolvedWorkspaceID, publicPrior.PipelineID, true)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "REFRESH_AUTHORIZATION_FAILED", "Refresh authorization failed", nil)
		return
	}
	if !allowed {
		apitransport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Refresh run is not accessible", nil)
		return
	}
	row, err := m.CancelRun(r.Context(), resolvedWorkspaceID, runID)
	if err != nil {
		if errors.Is(err, refreshrun.ErrRunNotCancellable) {
			apitransport.WriteProblem(w, r, http.StatusConflict, "REFRESH_NOT_CANCELLABLE", "Only queued refresh runs can be cancelled", nil)
			return
		}
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	response, ok := materializehttp.PipelineRunResponseFor(row)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "REFRESH_RESPONSE_INVALID", "Refresh response is invalid", nil)
		return
	}
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "INVALID_COMMAND_CONTRACT", "Refresh command contract is unavailable", nil)
		return
	}
	if err := executor.Execute(r.Context(), string(refreshgen.GenOperationCancelRefreshRun), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			return jobs.AppendJSONEvent(ctx, m.events, "refresh", runID, contract.AuditAction, response)
		},
		LogMessage:    "refresh audit failed",
		LogAttributes: []slog.Attr{slog.String("refresh_run_id", runID)},
	}); err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "INVALID_COMMAND_CONTRACT", "Refresh command contract is unavailable", nil)
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+workspaceID+"/refresh-runs/"+runID)
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func (m *Module) ListRefreshRunEvents(w http.ResponseWriter, r *http.Request, workspaceID, runID string, limit *int32, pageToken *string) {
	if m == nil || m.runs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "REFRESH_SERVICE_UNAVAILABLE", "Refresh service is unavailable", nil)
		return
	}
	resolvedWorkspaceID := m.workspaceID(workspaceID)
	run, err := m.GetRun(r.Context(), resolvedWorkspaceID, runID)
	if err != nil || run.Environment != m.environment {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	response, ok := materializehttp.PipelineRunResponseFor(run)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	allowed, err := m.authorize(r, resolvedWorkspaceID, response.PipelineID, false)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "REFRESH_AUTHORIZATION_FAILED", "Refresh authorization failed", nil)
		return
	}
	if !allowed {
		apitransport.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Refresh run is not accessible", nil)
		return
	}
	if m.events == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_EVENT_STORE_UNAVAILABLE", "Refresh events are unavailable", nil)
		return
	}
	jobhttp.WriteEventPage(w, r, m.events, "refresh", runID, limit, pageToken, "refresh:"+workspaceID+":"+runID)
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return materializehttp.DispatchAPIGenOperation(operationID, m, logger, w, r)
}

func (m *Module) workspaceID(workspaceID string) string {
	if m.handler.WorkspaceID != nil {
		return m.handler.WorkspaceID(workspaceID)
	}
	return workspaceID
}

func (m *Module) authorize(r *http.Request, workspaceID, pipelineID string, execute bool) (bool, error) {
	authorize := m.handler.AuthorizePipelineView
	if execute {
		authorize = m.handler.AuthorizePipelineRun
	}
	if authorize == nil {
		return true, nil
	}
	return authorize(r, workspaceID, pipelineID)
}
