package module

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
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

func (m *Module) verifyRunCreated(ctx context.Context, run refreshrun.RunRecord) error {
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
			if contract.Execution == nil || contract.Execution.InitialEvent != contract.AuditAction {
				return errors.New("refresh audit and initial lifecycle event disagree")
			}
			if m.events == nil {
				return errors.New("refresh event store is unavailable")
			}
			events, err := m.events.ListEvents(ctx, contract.Execution.ResourceKind, run.ID, 0, 200)
			if err != nil {
				return err
			}
			for _, event := range events {
				if event.EventType == contract.Execution.InitialEvent {
					return nil
				}
			}
			return errors.New("refresh initial lifecycle event is unavailable")
		},
		LogMessage:    "refresh persisted audit verification failed",
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
		_ = jobs.AppendJSONEvent(ctx, m.events, m.refreshExecution.ResourceKind, run.ID, "refresh."+run.Status, response)
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
	operationID := refreshgen.GenCommandOperationCancelRefreshRun()
	if m == nil || m.runs == nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("unavailable", "Refresh service is unavailable"))
		return
	}
	resolvedWorkspaceID := m.workspaceID(workspaceID)
	prior, err := m.GetRun(r.Context(), resolvedWorkspaceID, runID)
	if err != nil || prior.Environment != m.environment {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	publicPrior, ok := materializehttp.PipelineRunResponseFor(prior)
	if !ok {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	allowed, err := m.authorize(r, resolvedWorkspaceID, publicPrior.PipelineID, true)
	if err != nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.Wrap("unavailable", err))
		return
	}
	if !allowed {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("forbidden", "Refresh run is not accessible"))
		return
	}
	row, err := m.CancelRun(r.Context(), resolvedWorkspaceID, runID)
	if err != nil {
		if errors.Is(err, refreshrun.ErrRunNotCancellable) {
			writeRefreshCommandFailure(m, w, r, operationID, err)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.Wrap("not_found", err))
		} else {
			writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.Wrap("unavailable", err))
		}
		return
	}
	response, ok := materializehttp.PipelineRunResponseFor(row)
	if !ok {
		writeRefreshCommandFailure(m, w, r, operationID, errors.New("refresh response is invalid"))
		return
	}
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		writeRefreshCommandFailure(m, w, r, operationID, err)
		return
	}
	if err := executor.Execute(r.Context(), string(refreshgen.GenOperationCancelRefreshRun), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			return jobs.AppendJSONEvent(ctx, m.events, m.refreshExecution.ResourceKind, runID, contract.AuditAction, response)
		},
		LogMessage:    "refresh audit failed",
		LogAttributes: []slog.Attr{slog.String("refresh_run_id", runID)},
	}); err != nil {
		writeRefreshCommandFailure(m, w, r, operationID, err)
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+workspaceID+"/refresh-runs/"+runID)
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func writeRefreshCommandFailure(_ *Module, w http.ResponseWriter, r *http.Request, operationID refreshgen.GenCommandOperationID, err error) {
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, refreshgen.GetAPIGenCommandFailureContracts, err)
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
	jobhttp.WriteEventPage(w, r, m.events, m.refreshExecution.ResourceKind, runID, limit, pageToken, m.refreshExecution.ResourceKind+":"+workspaceID+":"+runID)
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
