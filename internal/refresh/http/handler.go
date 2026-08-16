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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

type Principal struct {
	ID string
}

type Handler struct {
	Repository            func() (refreshrun.RunRepository, error)
	RunnerConfigured      func() bool
	DispatchQueued        func()
	CurrentPrincipal      func(*nethttp.Request) (Principal, bool)
	ServingIdentity       func(*nethttp.Request) (projectgraph.ServingIdentity, error)
	RunCreated            func(context.Context, refreshrun.RunRecord) error
	AuthorizePipelineView func(*nethttp.Request, projectgraph.ServingIdentity, string) (bool, error)
	AuthorizePipelineRun  func(*nethttp.Request, projectgraph.ServingIdentity, string) (bool, error)
	QueuePipeline         func(context.Context, projectgraph.ServingIdentity, string, string, string) (refreshrun.RunRecord, error)
}

var errAuthorizationUnavailable = errors.New("refresh authorization is unavailable")

type materializationRunRequest struct {
	PipelineID string `json:"pipelineId"`
	RetryOf    string `json:"retryOf,omitempty"`
}

// PipelineRunResponse is the public representation of a root refresh-pipeline
// run. Model-table dependency runs and queue implementation details are never
// part of the API contract.
type PipelineRunResponse struct {
	ID                   string                       `json:"id"`
	Identity             projectgraph.ServingIdentity `json:"identity"`
	PipelineID           string                       `json:"pipelineId"`
	SemanticModel        string                       `json:"semanticModel"`
	PrincipalID          string                       `json:"principalId,omitempty"`
	PrincipalDisplayName string                       `json:"principalDisplayName,omitempty"`
	Trigger              string                       `json:"trigger"`
	RetryOf              string                       `json:"retryOf,omitempty"`
	Status               string                       `json:"status"`
	Error                string                       `json:"error,omitempty"`
	CreatedAt            string                       `json:"createdAt"`
	StartedAt            string                       `json:"startedAt,omitempty"`
	FinishedAt           string                       `json:"finishedAt,omitempty"`
}

func PipelineRunResponseFor(run refreshrun.RunRecord) (PipelineRunResponse, bool) {
	if run.ParentRunID != "" || run.TargetType != refreshrun.TargetRefreshPipeline || run.PipelineID == "" || run.TargetID != run.PipelineID {
		return PipelineRunResponse{}, false
	}
	if run.Identity.Validate() != nil || run.PipelineID.Validate() != nil || run.SemanticModelID.Validate() != nil {
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
		ID: run.ID, Identity: run.Identity, PipelineID: run.PipelineID.String(), SemanticModel: run.SemanticModelID.String(),
		PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName, Trigger: run.TriggerType,
		RetryOf: run.RetryOf, Status: run.Status, Error: run.Error, CreatedAt: createdAt,
		StartedAt: startedAt, FinishedAt: finishedAt,
	}, true
}

func (h Handler) CreateRun(w nethttp.ResponseWriter, r *nethttp.Request, project string) {
	operationID := refreshgen.GenCommandOperationCreateRefreshRun()
	repo, identity, ok := h.commandRunRepository(w, r, operationID, project)
	if !ok {
		return
	}
	if h.RunnerConfigured != nil && !h.RunnerConfigured() {
		writeCommandFailure(w, r, operationID, apigenfailure.New("unavailable", "materialization refresh runner is not configured"))
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
		writeCommandFailure(w, r, operationID, apigenfailure.New("not_found", "refresh pipeline not found"))
		return
	}
	pipelineID, err := projectgraph.NewResourceID(input.PipelineID)
	if err != nil || pipelineID.String() != input.PipelineID {
		writeCommandFailure(w, r, operationID, apigenfailure.New("not_found", "refresh pipeline not found"))
		return
	}
	if h.AuthorizePipelineRun == nil {
		writeCommandFailure(w, r, operationID, apigenfailure.Wrap("unavailable", errAuthorizationUnavailable))
		return
	}
	allowed, err := h.AuthorizePipelineRun(r, identity, input.PipelineID)
	if err != nil {
		writeCommandFailure(w, r, operationID, apigenfailure.Wrap("unavailable", err))
		return
	}
	if !allowed {
		writeCommandFailure(w, r, operationID, apigenfailure.New("not_found", "refresh pipeline not found"))
		return
	}
	if input.RetryOf != "" {
		prior, err := repo.GetRun(r.Context(), identity, input.RetryOf)
		if err != nil {
			writeCommandFailure(w, r, operationID, apigenfailure.New("not_found", "refresh run not found"))
			return
		}
		if prior.Identity != identity || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.PipelineID != pipelineID {
			writeCommandFailure(w, r, operationID, apigenfailure.New("not_found", "refresh run not found"))
			return
		}
		if prior.Status == refreshrun.RunStatusQueued || prior.Status == refreshrun.RunStatusRunning {
			writeCommandFailure(w, r, operationID, apigenfailure.New("conflict", "retryOf refresh run is not terminal"))
			return
		}
	}
	if h.QueuePipeline == nil {
		writeCommandFailure(w, r, operationID, apigenfailure.New("unavailable", "refresh pipeline runner is not configured"))
		return
	}
	run, err := h.QueuePipeline(r.Context(), identity, input.PipelineID, principalID, input.RetryOf)
	if err != nil {
		if _, classified := apigenfailure.KindOf(err); !classified {
			err = apigenfailure.Wrap("unavailable", err)
		}
		writeCommandFailure(w, r, operationID, err)
		return
	}
	if h.RunCreated != nil {
		if err := h.RunCreated(r.Context(), run); err != nil {
			writeCommandFailure(w, r, operationID, apigenfailure.Wrap("unavailable", err))
			return
		}
	}
	if h.DispatchQueued != nil {
		h.DispatchQueued()
	}
	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+run.ID)
	response, ok := PipelineRunResponseFor(run)
	if !ok {
		writeCommandFailure(w, r, operationID, fmt.Errorf("refresh service returned a non-pipeline run"))
		return
	}
	writeJSON(w, nethttp.StatusAccepted, response)
}

func (h Handler) ListRuns(w nethttp.ResponseWriter, r *nethttp.Request, project string) {
	repo, identity, ok := h.runRepository(w, r, project)
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
		runs, err := repo.ListRuns(r.Context(), identity, refreshrun.RunPage{Limit: maxAPILimit, After: after})
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
			allowed, err := h.pipelineAllowed(r, identity, response.PipelineID)
			if err != nil {
				writeJSONError(w, err, nethttp.StatusServiceUnavailable)
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

func (h Handler) GetRun(w nethttp.ResponseWriter, r *nethttp.Request, project, runID string) {
	repo, identity, ok := h.runRepository(w, r, project)
	if !ok {
		return
	}
	run, err := repo.GetRun(r.Context(), identity, runID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if run.Identity != identity {
		writeJSONError(w, sql.ErrNoRows, nethttp.StatusNotFound)
		return
	}
	response, valid := PipelineRunResponseFor(run)
	if !valid {
		writeJSONError(w, sql.ErrNoRows, nethttp.StatusNotFound)
		return
	}
	allowed, err := h.pipelineAllowed(r, identity, response.PipelineID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	if !allowed {
		writeJSONError(w, sql.ErrNoRows, nethttp.StatusNotFound)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func (h Handler) pipelineAllowed(r *nethttp.Request, identity projectgraph.ServingIdentity, pipelineID string) (bool, error) {
	if h.AuthorizePipelineView == nil {
		return false, errAuthorizationUnavailable
	}
	return h.AuthorizePipelineView(r, identity, pipelineID)
}

// ProjectMatchesIdentity binds the project route to the active serving
// identity. A request for another project must not enumerate or mutate runs
// from the active project.
func (h Handler) ProjectMatchesIdentity(project string, identity projectgraph.ServingIdentity) bool {
	if project == "" || project != strings.TrimSpace(project) {
		return false
	}
	projectID, err := projectgraph.NewResourceID(project)
	return err == nil && projectID == identity.ProjectID
}

func (h Handler) runRepository(w nethttp.ResponseWriter, r *nethttp.Request, project string) (refreshrun.RunRepository, projectgraph.ServingIdentity, bool) {
	if h.Repository == nil {
		writeJSONError(w, fmt.Errorf("platform store is required"), nethttp.StatusServiceUnavailable)
		return nil, projectgraph.ServingIdentity{}, false
	}
	repo, err := h.Repository()
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return nil, projectgraph.ServingIdentity{}, false
	}
	identity, err := h.servingIdentity(r)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return nil, projectgraph.ServingIdentity{}, false
	}
	if !h.ProjectMatchesIdentity(project, identity) {
		writeJSONError(w, sql.ErrNoRows, nethttp.StatusNotFound)
		return nil, projectgraph.ServingIdentity{}, false
	}
	return repo, identity, true
}

func (h Handler) commandRunRepository(w nethttp.ResponseWriter, r *nethttp.Request, operationID refreshgen.GenCommandOperationID, project string) (refreshrun.RunRepository, projectgraph.ServingIdentity, bool) {
	if h.Repository == nil {
		writeCommandFailure(w, r, operationID, apigenfailure.New("unavailable", "refresh persistence is not configured"))
		return nil, projectgraph.ServingIdentity{}, false
	}
	repo, err := h.Repository()
	if err != nil {
		writeCommandFailure(w, r, operationID, apigenfailure.Wrap("unavailable", err))
		return nil, projectgraph.ServingIdentity{}, false
	}
	identity, err := h.servingIdentity(r)
	if err != nil {
		writeCommandFailure(w, r, operationID, apigenfailure.Wrap("not_found", err))
		return nil, projectgraph.ServingIdentity{}, false
	}
	if !h.ProjectMatchesIdentity(project, identity) {
		writeCommandFailure(w, r, operationID, apigenfailure.New("not_found", "refresh run not found"))
		return nil, projectgraph.ServingIdentity{}, false
	}
	return repo, identity, true
}

func (h Handler) servingIdentity(r *nethttp.Request) (projectgraph.ServingIdentity, error) {
	if h.ServingIdentity == nil {
		return projectgraph.ServingIdentity{}, errors.New("exact serving identity resolver is required")
	}
	identity, err := h.ServingIdentity(r)
	if err != nil {
		return projectgraph.ServingIdentity{}, err
	}
	if err := identity.Validate(); err != nil {
		return projectgraph.ServingIdentity{}, err
	}
	return identity, nil
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
// compiler-checked generated operation vocabulary.
func writeCommandFailure(w nethttp.ResponseWriter, r *nethttp.Request, operationID refreshgen.GenCommandOperationID, err error) {
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
