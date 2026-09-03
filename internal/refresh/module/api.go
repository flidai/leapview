package module

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	materializehttp "github.com/flidai/leapview/internal/refresh/http"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
)

type EventStore interface {
	jobs.EventAppender
	jobhttp.EventReader
}

// CreateRefreshRunOperationID exposes the generated command identity through
// the refresh module boundary so application composition does not import a
// product capability's generated transport package directly.
const CreateRefreshRunOperationID = string(refreshgen.GenOperationCreateRefreshRun)

// CancelRefreshRunOperationID exposes the generated cancellation identity for
// production protocol composition.
const CancelRefreshRunOperationID = string(refreshgen.GenOperationCancelRefreshRun)

func (m *Module) verifyRunCreated(ctx context.Context, run refreshrun.RunRecord) error {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	if m != nil && m.durableAudit {
		return executor.Execute(ctx, string(refreshgen.GenOperationCreateRefreshRun), apigencommand.Execution{
			Transactional: func(context.Context, apigencommand.Contract) error { return nil },
		})
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

func (m *Module) verifyRunCancelled(ctx context.Context, run refreshrun.RunRecord) error {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(refreshgen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		return err
	}
	if m != nil && m.durableAudit {
		return executor.Execute(ctx, string(refreshgen.GenOperationCancelRefreshRun), apigencommand.Execution{
			Transactional: func(context.Context, apigencommand.Contract) error { return nil },
		})
	}
	return executor.Execute(ctx, string(refreshgen.GenOperationCancelRefreshRun), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if m.events == nil {
				return errors.New("refresh event store is unavailable")
			}
			encoded, err := refreshgen.EncodeGenCancelRefreshRunAuditPayload(refreshgen.GenSchemaRefreshCancelledAuditPayload{
				Id:                  run.ID,
				PipelineId:          run.PipelineID.String(),
				Status:              run.Status,
				InvocationSource:    run.InvocationSource,
				MatchingScheduleIds: append([]string{}, run.MatchingScheduleIDs...),
			})
			if err != nil {
				return err
			}
			_, err = m.events.AppendEvent(ctx, m.refreshExecution.ResourceKind, run.ID, contract.AuditAction, []byte(encoded))
			return err
		},
		LogMessage:    "refresh audit failed",
		LogAttributes: []slog.Attr{slog.String("refresh_run_id", run.ID)},
	})
}

// VerifyRunCreated completes the generated createRefreshRun policy after a
// non-API surface has atomically queued the same refresh lifecycle.
func (m *Module) VerifyRunCreated(ctx context.Context, run refreshrun.RunRecord) error {
	return m.verifyRunCreated(ctx, run)
}

func (m *Module) VerifyRunCancelled(ctx context.Context, run refreshrun.RunRecord) error {
	return m.verifyRunCancelled(ctx, run)
}

func (m *Module) runFinished(after func(context.Context, refreshrun.RunRecord)) func(context.Context, refreshrun.JobRecord) {
	return func(ctx context.Context, job refreshrun.JobRecord) {
		scope, scopeErr := refreshrun.ReadScopeForIdentity(job.Identity)
		if scopeErr != nil {
			return
		}
		runs, readErr := m.readRuns()
		if readErr != nil {
			return
		}
		run, err := runs.GetRun(ctx, scope, job.RunID)
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

func (m *Module) CreateRefreshRun(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	m.handler.CreateRun(w, r, project, idempotencyKey)
}

func (m *Module) ListRefreshRuns(w http.ResponseWriter, r *http.Request, project string) {
	m.handler.ListRuns(w, r, project)
}

func (m *Module) GetRefreshRun(w http.ResponseWriter, r *http.Request, project, runID string) {
	m.handler.GetRun(w, r, project, runID)
}

func (m *Module) CancelRefreshRun(w http.ResponseWriter, r *http.Request, project, runID, idempotencyKey string) {
	operationID := refreshgen.GenCommandOperationCancelRefreshRun()
	if m == nil || m.runs == nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("unavailable", "Refresh service is unavailable"))
		return
	}
	identity, identityErr := m.handler.ServingIdentity(r)
	if identityErr != nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	if !m.handler.ProjectMatchesIdentity(project, identity) {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	scope, scopeErr := refreshrun.ReadScopeForIdentity(identity)
	if scopeErr != nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	runs, readErr := m.readRuns()
	if readErr != nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("unavailable", "Refresh service is unavailable"))
		return
	}
	prior, err := runs.GetRun(r.Context(), scope, runID)
	if err != nil || !scope.Matches(prior.Identity) {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	publicPrior, ok := materializehttp.PipelineRunResponseFor(prior)
	if !ok {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	allowed, err := m.authorize(r, identity, publicPrior.PipelineID, true)
	if err != nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.Wrap("unavailable", err))
		return
	}
	if !allowed {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("not_found", "Refresh run not found"))
		return
	}
	// Cancellation remains generation-fenced: use the run's originating
	// identity for the mutation after authorizing it in the active scope.
	cancelCtx := r.Context()
	principalID := ""
	if m.handler.CurrentPrincipal != nil {
		if principal, ok := m.handler.CurrentPrincipal(r); ok {
			principalID = principal.ID
		}
	}
	intent, intentErr := buildRefreshAuditIntent(cancelCtx, operationID.APIGenOperationID(), principalID, identity.ProjectID.String(), idempotencyKey, r.Header.Get("X-Correlation-Id"))
	if intentErr != nil || intent == nil {
		if intentErr == nil {
			intentErr = errors.New("refresh cancellation audit intent is unavailable")
		}
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.Wrap("unavailable", intentErr))
		return
	}
	intent.EventID = ""
	cancelCtx = refreshrun.WithAuditIntent(cancelCtx, *intent)
	cancel, cancelErr := m.cancelRuns()
	if cancelErr != nil {
		writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("unavailable", "Refresh service is unavailable"))
		return
	}
	var row refreshrun.RunRecord
	replayed := false
	if strings.TrimSpace(idempotencyKey) != "" {
		keyed, keyedOK := cancel.(KeyedCancelRunPersistence)
		if keyedOK {
			requestDigest, digestErr := refreshrun.CancelRequestDigest(prior.Identity, principalID, runID)
			if digestErr != nil {
				writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.Wrap("unavailable", digestErr))
				return
			}
			row, replayed, err = keyed.CancelRunWithAuditKeyed(cancelCtx, prior.Identity, runID, principalID, idempotencyKey, requestDigest, nil)
		} else {
			// Repositories without the keyed operation port rely on their
			// surrounding API idempotency layer for replay handling.
			row, err = cancel.CancelRunWithAudit(cancelCtx, prior.Identity, runID, nil)
		}
	} else {
		// Direct callers without a generated idempotency key use the
		// repository's non-keyed cancellation operation.
		row, err = cancel.CancelRunWithAudit(cancelCtx, prior.Identity, runID, nil)
	}
	if err != nil {
		if errors.Is(err, refreshrun.ErrRunNotCancellable) {
			writeRefreshCommandFailure(m, w, r, operationID, err)
			return
		}
		if errors.Is(err, refreshpostgres.ErrStaleFence) {
			writeRefreshCommandFailure(m, w, r, operationID, refreshrun.ErrRunNotCancellable)
			return
		}
		if errors.Is(err, refreshpostgres.ErrConflict) {
			writeRefreshCommandFailure(m, w, r, operationID, apigenfailure.New("conflict", "refresh cancellation request conflicts with a prior request"))
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
	if !replayed {
		if err := m.verifyRunCancelled(r.Context(), row); err != nil {
			writeRefreshCommandFailure(m, w, r, operationID, err)
			return
		}
	}
	w.Header().Set("Location", "/api/v1/projects/"+identity.ProjectID.String()+"/refresh-runs/"+runID)
	apitransport.WriteJSON(w, http.StatusAccepted, response)
}

func writeRefreshCommandFailure(_ *Module, w http.ResponseWriter, r *http.Request, operationID refreshgen.GenCommandOperationID, err error) {
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, refreshgen.GetAPIGenCommandFailureContracts, err)
}

func (m *Module) ListRefreshRunEvents(w http.ResponseWriter, r *http.Request, project, runID string, limit *int32, pageToken *string) {
	if m == nil || m.runs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "REFRESH_SERVICE_UNAVAILABLE", "Refresh service is unavailable", nil)
		return
	}
	identity, identityErr := m.handler.ServingIdentity(r)
	if identityErr != nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	if !m.handler.ProjectMatchesIdentity(project, identity) {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	scope, scopeErr := refreshrun.ReadScopeForIdentity(identity)
	if scopeErr != nil {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	runs, readErr := m.readRuns()
	if readErr != nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "REFRESH_SERVICE_UNAVAILABLE", "Refresh service is unavailable", nil)
		return
	}
	run, err := runs.GetRun(r.Context(), scope, runID)
	if err != nil || !scope.Matches(run.Identity) {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	response, ok := materializehttp.PipelineRunResponseFor(run)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	allowed, err := m.authorize(r, identity, response.PipelineID, false)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "REFRESH_AUTHORIZATION_UNAVAILABLE", "Refresh authorization is unavailable", nil)
		return
	}
	if !allowed {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "REFRESH_RUN_NOT_FOUND", "Refresh run not found", nil)
		return
	}
	if m.events == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_EVENT_STORE_UNAVAILABLE", "Refresh events are unavailable", nil)
		return
	}
	jobhttp.WriteEventPage(w, r, m.events, m.refreshExecution.ResourceKind, runID, limit, pageToken, m.refreshExecution.ResourceKind+":"+runID)
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return materializehttp.DispatchAPIGenOperation(operationID, m, logger, w, r)
}

func (m *Module) authorize(r *http.Request, identity projectgraph.ServingIdentity, pipelineID string, execute bool) (bool, error) {
	authorize := m.handler.AuthorizePipelineView
	if execute {
		authorize = m.handler.AuthorizePipelineRun
	}
	if authorize == nil {
		return false, errors.New("refresh authorization is unavailable")
	}
	return authorize(r, identity, pipelineID)
}
