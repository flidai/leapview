package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	httpmodel "github.com/flidai/leapview/internal/platform/http/model"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
)

type Principal struct {
	ID string
}

type Handler struct {
	Repository            func() (refreshrun.RunRepository, error)
	RunnerConfigured      func() bool
	DispatchQueued        func()
	CurrentPrincipal      func(*nethttp.Request) (Principal, bool)
	WorkspaceID           func(string) string
	Environment           func(*nethttp.Request) string
	RunCreated            func(context.Context, refreshrun.RunRecord) error
	AuthorizePipelineView func(*nethttp.Request, string, string) (bool, error)
	AuthorizePipelineRun  func(*nethttp.Request, string, string) (bool, error)
	QueuePipeline         func(context.Context, string, string, string, string, string) (refreshrun.RunRecord, error)
}

type materializationRunRequest struct {
	PipelineID string `json:"pipelineId"`
	RetryOf    string `json:"retryOf,omitempty"`
}

// PipelineRunResponse is the public representation of a root refresh-pipeline
// run. Model-table dependency runs and queue implementation details are never
// part of the API contract.
type PipelineRunResponse struct {
	ID                   string `json:"id"`
	WorkspaceID          string `json:"workspaceId"`
	PipelineID           string `json:"pipelineId"`
	SemanticModel        string `json:"semanticModel"`
	PrincipalID          string `json:"principalId,omitempty"`
	PrincipalDisplayName string `json:"principalDisplayName,omitempty"`
	Trigger              string `json:"trigger"`
	RetryOf              string `json:"retryOf,omitempty"`
	Status               string `json:"status"`
	Error                string `json:"error,omitempty"`
	CreatedAt            string `json:"createdAt"`
	StartedAt            string `json:"startedAt,omitempty"`
	FinishedAt           string `json:"finishedAt,omitempty"`
}

func PipelineRunResponseFor(run refreshrun.RunRecord) (PipelineRunResponse, bool) {
	prefix := run.WorkspaceID + "."
	if run.ParentRunID != "" || run.TargetType != refreshrun.TargetRefreshPipeline || !strings.HasPrefix(run.TargetID, prefix) {
		return PipelineRunResponse{}, false
	}
	pipelineID := strings.TrimSpace(strings.TrimPrefix(run.TargetID, prefix))
	if pipelineID == "" {
		return PipelineRunResponse{}, false
	}
	createdAt, err := httptransport.NormalizeTimestamp(run.CreatedAt)
	if err != nil || createdAt == "" {
		return PipelineRunResponse{}, false
	}
	startedAt, err := httptransport.NormalizeTimestamp(run.StartedAt)
	if err != nil {
		return PipelineRunResponse{}, false
	}
	finishedAt, err := httptransport.NormalizeTimestamp(run.FinishedAt)
	if err != nil {
		return PipelineRunResponse{}, false
	}
	return PipelineRunResponse{
		ID: run.ID, WorkspaceID: run.WorkspaceID, PipelineID: pipelineID, SemanticModel: run.ModelID,
		PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName, Trigger: run.TriggerType,
		RetryOf: run.RetryOf, Status: run.Status, Error: run.Error, CreatedAt: createdAt,
		StartedAt: startedAt, FinishedAt: finishedAt,
	}, true
}

func (h Handler) CreateRun(w nethttp.ResponseWriter, r *nethttp.Request) {
	repo, workspaceID, ok := h.commandRunRepository(w, r, "createRefreshRun")
	if !ok {
		return
	}
	if h.RunnerConfigured != nil && !h.RunnerConfigured() {
		writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("unavailable", "materialization refresh runner is not configured"), nethttp.StatusServiceUnavailable)
		return
	}
	var input materializationRunRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return
	}
	principalID := ""
	if h.CurrentPrincipal != nil {
		if principal, ok := h.CurrentPrincipal(r); ok {
			principalID = principal.ID
		}
	}
	if strings.TrimSpace(input.PipelineID) == "" {
		writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("invalid", "pipelineId is required"), nethttp.StatusUnprocessableEntity)
		return
	}
	if h.AuthorizePipelineRun != nil {
		allowed, err := h.AuthorizePipelineRun(r, workspaceID, input.PipelineID)
		if err != nil {
			writeCommandFailure(w, r, "createRefreshRun", apigenfailure.Wrap("unavailable", err), nethttp.StatusInternalServerError)
			return
		}
		if !allowed {
			writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("forbidden", "refresh run is not permitted"), nethttp.StatusForbidden)
			return
		}
	}
	if input.RetryOf != "" {
		prior, err := repo.GetRun(r.Context(), workspaceID, input.RetryOf)
		if err != nil {
			writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("invalid", "retryOf does not identify a refresh run in this workspace"), nethttp.StatusUnprocessableEntity)
			return
		}
		if prior.Status == refreshrun.RunStatusQueued || prior.Status == refreshrun.RunStatusRunning {
			writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("conflict", "retryOf refresh run is not terminal"), nethttp.StatusConflict)
			return
		}
		if prior.Environment != h.environment(r) || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.TargetID != workspaceID+"."+input.PipelineID {
			writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("invalid", "retryOf does not belong to pipelineId"), nethttp.StatusUnprocessableEntity)
			return
		}
	}
	if h.QueuePipeline == nil {
		writeCommandFailure(w, r, "createRefreshRun", apigenfailure.New("unavailable", "refresh pipeline runner is not configured"), nethttp.StatusServiceUnavailable)
		return
	}
	run, err := h.QueuePipeline(r.Context(), workspaceID, h.environment(r), input.PipelineID, principalID, input.RetryOf)
	if err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		writeCommandFailure(w, r, "createRefreshRun", err, nethttp.StatusServiceUnavailable)
		return
	}
	if h.RunCreated != nil {
		if err := h.RunCreated(r.Context(), run); err != nil {
			writeCommandFailure(w, r, "createRefreshRun", apigenfailure.Wrap("unavailable", err), nethttp.StatusServiceUnavailable)
			return
		}
	}
	if h.DispatchQueued != nil {
		h.DispatchQueued()
	}
	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+run.ID)
	response, ok := PipelineRunResponseFor(run)
	if !ok {
		writeCommandFailure(w, r, "createRefreshRun", fmt.Errorf("refresh service returned a non-pipeline run"), nethttp.StatusInternalServerError)
		return
	}
	writeJSON(w, nethttp.StatusAccepted, response)
}

func (h Handler) ListRuns(w nethttp.ResponseWriter, r *nethttp.Request) {
	repo, workspaceID, ok := h.runRepository(w, r)
	if !ok {
		return
	}
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return
	}
	responses := make([]PipelineRunResponse, 0, limit+1)
	after := firstNonEmpty(r.URL.Query().Get("pageToken"), r.URL.Query().Get("after"))
	for len(responses) <= limit {
		runs, err := repo.ListRuns(r.Context(), workspaceID, refreshrun.RunPage{Limit: maxAPILimit, After: after, Environment: h.environment(r)})
		if err != nil {
			writeJSONError(w, err, nethttp.StatusInternalServerError)
			return
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			response, valid := PipelineRunResponseFor(run)
			if !valid {
				continue
			}
			allowed, err := h.pipelineAllowed(r, workspaceID, response.PipelineID)
			if err != nil {
				writeJSONError(w, err, nethttp.StatusInternalServerError)
				return
			}
			if allowed {
				responses = append(responses, response)
				if len(responses) > limit {
					break
				}
			}
		}
		after = runs[len(runs)-1].ID
		if len(runs) < maxAPILimit {
			break
		}
	}
	nextCursor := ""
	if len(responses) > limit {
		nextCursor = responses[limit-1].ID
		responses = responses[:limit]
	}
	writeJSON(w, nethttp.StatusOK, pagedResponseWithCursor(responses, nextCursor))
}

func (h Handler) GetRun(w nethttp.ResponseWriter, r *nethttp.Request) {
	repo, workspaceID, ok := h.runRepository(w, r)
	if !ok {
		return
	}
	run, err := repo.GetRun(r.Context(), workspaceID, chi.URLParam(r, "run"))
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if run.Environment != h.environment(r) {
		writeJSONError(w, sql.ErrNoRows, nethttp.StatusNotFound)
		return
	}
	response, valid := PipelineRunResponseFor(run)
	if !valid {
		writeJSONError(w, sql.ErrNoRows, nethttp.StatusNotFound)
		return
	}
	allowed, err := h.pipelineAllowed(r, workspaceID, response.PipelineID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	if !allowed {
		writeJSONError(w, fmt.Errorf("forbidden"), nethttp.StatusForbidden)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func (h Handler) pipelineAllowed(r *nethttp.Request, workspaceID, pipelineID string) (bool, error) {
	if h.AuthorizePipelineView == nil {
		return true, nil
	}
	return h.AuthorizePipelineView(r, workspaceID, pipelineID)
}

func (h Handler) environment(r *nethttp.Request) string {
	if h.Environment == nil {
		return string(servingstate.DefaultEnvironment)
	}
	return string(servingstate.NormalizeEnvironment(servingstate.Environment(h.Environment(r))))
}

func (h Handler) runRepository(w nethttp.ResponseWriter, r *nethttp.Request) (refreshrun.RunRepository, string, bool) {
	if h.Repository == nil {
		writeJSONError(w, fmt.Errorf("platform store is required"), nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	repo, err := h.Repository()
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	workspaceID := chi.URLParam(r, "workspace")
	if h.WorkspaceID != nil {
		workspaceID = h.WorkspaceID(workspaceID)
	}
	if workspaceID == "" {
		writeJSONError(w, fmt.Errorf("workspace id is required"), nethttp.StatusBadRequest)
		return nil, "", false
	}
	return repo, workspaceID, true
}

func (h Handler) commandRunRepository(w nethttp.ResponseWriter, r *nethttp.Request, operationID string) (refreshrun.RunRepository, string, bool) {
	if h.Repository == nil {
		writeCommandFailure(w, r, operationID, apigenfailure.New("unavailable", "refresh persistence is not configured"), nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	repo, err := h.Repository()
	if err != nil {
		writeCommandFailure(w, r, operationID, apigenfailure.Wrap("unavailable", err), nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	workspaceID := chi.URLParam(r, "workspace")
	if h.WorkspaceID != nil {
		workspaceID = h.WorkspaceID(workspaceID)
	}
	if workspaceID == "" {
		writeCommandFailure(w, r, operationID, apigenfailure.New("invalid", "workspace id is required"), nethttp.StatusUnprocessableEntity)
		return nil, "", false
	}
	return repo, workspaceID, true
}

type pageResponse struct {
	NextCursor string `json:"nextCursor"`
}

func pagedResponseWithCursor(items any, nextCursor string) map[string]any {
	return map[string]any{"items": items, "page": pageResponse{NextCursor: nextCursor}}
}

const (
	defaultAPILimit = 50
	maxAPILimit     = 100
)

func apiLimitForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (int, bool) {
	limit, err := parseAPILimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func parseAPILimit(value string) (int, error) {
	if value == "" {
		return defaultAPILimit, nil
	}
	var limit int
	if _, err := fmt.Sscanf(value, "%d", &limit); err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if limit < 1 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if limit > maxAPILimit {
		return maxAPILimit, nil
	}
	return limit, nil
}

func statusForNotFound(err error) int {
	if err == sql.ErrNoRows || errors.Is(err, sql.ErrNoRows) {
		return nethttp.StatusNotFound
	}
	return nethttp.StatusInternalServerError
}

func writeJSON(w nethttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}

func writeJSONError(w nethttp.ResponseWriter, err error, status int) {
	writeJSON(w, status, httpmodel.ErrorResponse{
		Code:      status,
		Message:   err.Error(),
		Details:   map[string]any{},
		RequestID: "",
	})
}

// writeCommandFailure resolves classified refresh command errors through the
// generated operation vocabulary. The final argument is retained only for
// source compatibility with transport-owned call sites.
func writeCommandFailure(w nethttp.ResponseWriter, r *nethttp.Request, operationID string, err error, _ int) {
	httptransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, refreshgen.GetAPIGenCommandFailureContracts, err)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
