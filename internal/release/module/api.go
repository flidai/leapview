package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
	projectapi "github.com/flidai/leapview/internal/project/api"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releaseapi "github.com/flidai/leapview/internal/release/api"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
	releasefilesystem "github.com/flidai/leapview/internal/release/filesystem"
	releasehttp "github.com/flidai/leapview/internal/release/http"
	"github.com/flidai/leapview/pkg/jobs"
)

type Principal struct {
	ID string
}

type PageParams = releaseapi.PageParams

// Search dispatches through the active-lease project catalog. The catalog is
// authorization-filtered and snapshot-bound; release does not maintain a
// second index or accept a project selector from the request.
func (m *Module) Search(w http.ResponseWriter, r *http.Request, params projectapi.SearchParams) {
	if m == nil || m.searchCatalog == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Project search is unavailable", nil)
		return
	}
	principal, ok := m.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	kinds, err := searchKinds(params.Kind)
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_SEARCH_KIND", err.Error(), nil)
		return
	}
	request := projectcatalog.SearchRequest{
		PrincipalID: principal.ID, Query: strings.TrimSpace(params.Q), Kinds: kinds,
		Limit: searchLimit(params.Limit), Cursor: searchCursor(params.Cursor),
	}
	if params.Domain != nil {
		request.Domain = strings.TrimSpace(*params.Domain)
	}
	page, err := m.searchCatalog.Search(r.Context(), request)
	if err != nil {
		status, code := searchErrorStatus(err)
		detail := err.Error()
		if status == http.StatusServiceUnavailable {
			if m.logger != nil {
				m.logger.WarnContext(r.Context(), "project catalog search unavailable", "error", err)
			}
			detail = "Project search is temporarily unavailable"
		}
		apitransport.WriteProblem(w, r, status, code, detail, nil)
		return
	}
	items := make([]searchResultResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, searchResult(item))
	}
	apitransport.WriteJSON(w, http.StatusOK, searchResponse{
		Items: items,
		Page:  searchPageInfo{NextCursor: searchStringPointer(page.NextCursor)},
	})
}

type searchResponse struct {
	Items []searchResultResponse `json:"items"`
	Page  searchPageInfo         `json:"page"`
}

type searchPageInfo struct {
	NextCursor *string `json:"nextCursor,omitempty"`
}

type searchResultResponse struct {
	Description *string `json:"description,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
	Domain      *string `json:"domain,omitempty"`
	Href        *string `json:"href,omitempty"`
	Name        string  `json:"name"`
	Owner       *string `json:"owner,omitempty"`
	Reference   struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"reference"`
	Tags []string `json:"tags"`
}

var publicSearchKinds = []projectgraph.Kind{
	projectgraph.KindProject, projectgraph.KindConnection, projectgraph.KindSource,
	projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline,
	projectgraph.KindDashboard,
}

func searchKinds(values *[]projectapi.SearchKind) ([]projectgraph.Kind, error) {
	if values == nil || len(*values) == 0 {
		return append([]projectgraph.Kind(nil), publicSearchKinds...), nil
	}
	allowed := make([]projectgraph.Kind, 0, len(*values))
	for _, value := range *values {
		kind, err := projectgraph.ParseKind(string(value))
		if err != nil || !containsSearchKind(kind) {
			return nil, fmt.Errorf("unsupported search kind %q", value)
		}
		allowed = append(allowed, kind)
	}
	return allowed, nil
}

func containsSearchKind(want projectgraph.Kind) bool {
	for _, kind := range publicSearchKinds {
		if kind == want {
			return true
		}
	}
	return false
}

func searchLimit(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func searchCursor(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func searchErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, projectcatalog.ErrInvalidRequest), errors.Is(err, projectcatalog.ErrInvalidCursor):
		return http.StatusBadRequest, "INVALID_SEARCH_REQUEST"
	case errors.Is(err, projectcatalog.ErrUnavailable), errors.Is(err, projectcatalog.ErrSnapshotChanged):
		return http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE"
	default:
		// Authorization and lease errors fail closed without exposing internal
		// details through a successful or ambiguous response.
		return http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE"
	}
}

func searchResult(item projectcatalog.Result) searchResultResponse {
	return searchResultResponse{
		Reference: struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}{ID: item.Ref.ID.String(), Kind: string(item.Ref.Kind)},
		Name: item.Name, DisplayName: searchOptionalString(item.DisplayName), Description: searchOptionalString(item.Description),
		Domain: searchOptionalString(item.Domain), Owner: searchOptionalString(item.Owner), Tags: append([]string(nil), item.Tags...),
		Href: searchOptionalString(searchResultHref(item)),
	}
}

func searchResultHref(item projectcatalog.Result) string {
	id := url.PathEscape(item.Ref.ID.String())
	switch item.Ref.Kind {
	case projectgraph.KindProject:
		return "/"
	case projectgraph.KindConnection:
		return "/connections/" + id + "/details"
	case projectgraph.KindSource:
		return "/sources/" + id + "/details"
	case projectgraph.KindModel:
		return "/models/" + id + "/details"
	case projectgraph.KindSemanticModel:
		return "/semantic-models/" + id + "/details"
	case projectgraph.KindPipeline:
		return "/pipelines/" + id + "/details"
	case projectgraph.KindDashboard:
		return "/dashboards/" + id
	default:
		return ""
	}
}

func searchOptionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func searchStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type JobStore interface {
	Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error)
	AppendEvent(context.Context, string, string, string, []byte) (jobs.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error)
}

type APIConfig struct {
	CurrentPrincipal     func(*http.Request) (Principal, bool)
	ProjectSearchCatalog projectcatalogSearcher
	AuthorizeConnection  func(context.Context, string, string, string, access.Capability) (bool, error)
	Jobs                 JobStore
	Workflow             jobplatform.WorkflowRecorder
}

// SetProjectSearchCatalog binds the one active-lease catalog assembled by application
// composition. Keeping the setter separate lets release persistence be built
// before the runtime host while still sharing the exact same catalog service.
func (m *Module) SetProjectSearchCatalog(catalog projectcatalogSearcher) {
	if m != nil {
		m.searchCatalog = catalog
	}
}

func (m *Module) CreateRelease(w http.ResponseWriter, r *http.Request, project, idempotencyKey string) {
	principal, ok := m.currentPrincipal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	var body releaseapi.CreateRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", err.Error(), nil)
		return
	}
	identity, identityErr := projectgraph.NewServingIdentity(projectgraph.ResourceID(project), body.Environment, body.GenerationID)
	if identityErr != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationCreateRelease(), identityErr)
		return
	}
	input := release.CreateInput{ServingIdentity: identity, ProjectDigest: body.ProjectDigest, ArtifactDigest: body.ArtifactDigest, RequestDigest: body.RequestDigest, IdempotencyKey: idempotencyKey, CreatedBy: principal.ID}
	provenance, err := releaseProvenanceFromAPI(body.Provenance)
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationCreateRelease(), fmt.Errorf("%w: invalid provenance", release.ErrInvalid))
		return
	}
	input.Provenance = provenance
	for _, item := range body.Connections {
		input.Connections = append(input.Connections, release.ConnectionPin{ConnectionID: item.Connection, RevisionID: item.RevisionID})
	}
	ctx := r.Context()
	if m.auditIntentConfigured {
		projectID := identity.ProjectID
		releaseID := release.IDFor(projectID, idempotencyKey)
		requestID, correlationID := releaseAuditRequestIdentity(r)
		intent, intentErr := buildReleaseCreatedAuditIntent(releaseAuditCommandInput{
			OperationID: string(releasegen.GenOperationCreateRelease), ProjectID: projectID, ReleaseID: releaseID,
			IdempotencyKey: idempotencyKey, PrincipalID: principal.ID, RequestID: requestID, CorrelationID: correlationID,
			Surface: "api", ProjectDigest: input.ProjectDigest, Status: string(release.StatusDraft), CreatedBy: principal.ID,
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationCreateRelease(), intentErr)
			return
		}
		ctx = release.WithAuditIntent(ctx, intent)
	}
	created, err := m.service.Create(ctx, input)
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationCreateRelease(), err)
		return
	}
	if m.auditIntentConfigured {
		if err := m.completeCommand(ctx, string(releasegen.GenOperationCreateRelease)); err != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationCreateRelease(), err)
			return
		}
	} else {
		m.recordBestEffortEvent(
			r.Context(), string(releasegen.GenOperationCreateRelease), created.ID,
			releaseCreatedAuditAction, releasegen.GenSchemaReleaseCreatedAuditPayload{
				OperationId: string(releasegen.GenOperationCreateRelease), ReleaseId: created.ID,
				ProjectId: created.ServingIdentity.ProjectID.String(), ProjectDigest: created.ProjectDigest,
				Status: string(created.Status), CreatedBy: created.CreatedBy,
			},
		)
	}
	w.Header().Set("Location", location(project, created.ID))
	apitransport.WriteJSON(w, http.StatusCreated, response(created))
}

func (m *Module) completeCommand(ctx context.Context, operationID string) error {
	executor, err := apigencommand.NewExecutor(releasegen.GetAPIGenCommandRuntimeContract, m.logger)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID, apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error { return nil },
	})
}

func releaseAuditRequestIdentity(r *http.Request) (string, string) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = strings.TrimSpace(r.Header.Get("X-Request-Id"))
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = strings.TrimSpace(r.Header.Get("X-Correlation-Id"))
	}
	if correlationID == "" {
		correlationID = requestID
	}
	return requestID, correlationID
}

func (m *Module) ListReleases(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		writeError(w, r, release.ErrInvalid)
		return
	}
	rows, err := m.service.List(r.Context(), projectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]releaseapi.Response, 0, len(rows))
	for _, row := range rows {
		items = append(items, response(row))
	}
	page, next, err := apitransport.KeysetPage(items, limit, pageToken, func(item releaseapi.Response) string { return item.CreatedAt + "\x00" + item.ID })
	if err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, releaseapi.ListResponse{Items: page, Page: releaseapi.PageInfo{NextCursor: next}})
}

func (m *Module) GetRelease(w http.ResponseWriter, r *http.Request, project, releaseID string) {
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		writeError(w, r, release.ErrInvalid)
		return
	}
	row, err := m.service.Get(r.Context(), projectID, releaseID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", apitransport.StrongETag(row.RequestDigest+":"+string(row.Status)))
	apitransport.WriteJSON(w, http.StatusOK, response(row))
}

func (m *Module) UploadReleaseArtifact(w http.ResponseWriter, r *http.Request, project, releaseID, contentType, contentDigest string) {
	if contentType != "application/octet-stream" {
		apitransport.WriteProblem(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Release artifacts require application/octet-stream", nil)
		return
	}
	ctx := r.Context()
	if m.auditIntentConfigured {
		principal, ok := m.currentPrincipal(r)
		if !ok {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), apigenfailure.New("authentication_required", "Bearer authentication is required"))
			return
		}
		projectID, projectErr := projectgraph.NewResourceID(project)
		if projectErr != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), projectErr)
			return
		}
		current, getErr := m.service.Get(ctx, projectID, releaseID)
		if getErr != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), getErr)
			return
		}
		requestID, correlationID := releaseAuditRequestIdentity(r)
		intent, intentErr := buildReleaseCreatedAuditIntent(releaseAuditCommandInput{
			OperationID: string(releasegen.GenOperationUploadReleaseArtifact), ProjectID: projectID, ReleaseID: releaseID,
			GenerationID: current.ServingIdentity.GenerationID, ArtifactDigest: current.ArtifactDigest,
			IdempotencyKey: "artifact:" + releaseID + ":" + contentDigest, PrincipalID: principal.ID,
			RequestID: requestID, CorrelationID: correlationID, Status: string(current.Status),
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), intentErr)
			return
		}
		ctx = release.WithAuditIntent(ctx, intent)
	}
	artifact, err := m.service.UploadArtifact(ctx, project, releaseID, contentDigest, http.MaxBytesReader(w, r.Body, releasefilesystem.MaxUploadBytes))
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), err)
		return
	}
	identity, identityErr := artifact.Identity()
	if identityErr != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), identityErr)
		return
	}
	result := releaseapi.ArtifactResponse{ReleaseID: releaseID, GenerationID: identity.GenerationID, Digest: artifact.ExpectedDigest, ActualDigest: artifact.ActualDigest, SizeBytes: artifact.SizeBytes}
	if err := m.completeCommand(ctx, string(releasegen.GenOperationUploadReleaseArtifact)); err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationUploadReleaseArtifact(), err)
		return
	}
	w.Header().Set("Location", location(project, releaseID)+"/artifact")
	apitransport.WriteJSON(w, http.StatusCreated, result)
}

func (m *Module) FinalizeRelease(w http.ResponseWriter, r *http.Request, project, releaseID, idempotencyKey string) {
	principal, ok := m.currentPrincipal(r)
	if !ok {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return
	}
	payload, err := json.Marshal(FinalizeJob{Project: project, Release: releaseID})
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationFinalizeRelease(), err)
		return
	}
	event, err := releasegen.EncodeGenFinalizeReleaseAuditPayload(releasegen.GenSchemaReleaseValidatingAuditPayload{
		OperationId: string(releasegen.GenOperationFinalizeRelease), ReleaseId: releaseID,
		ProjectId: project, Status: m.finalizeExecution.InitialState,
	})
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationFinalizeRelease(), err)
		return
	}
	if m.api.Workflow == nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationFinalizeRelease(), apigenfailure.New("queue_unavailable", "release workflow is unavailable"))
		return
	}
	ctx := r.Context()
	if m.auditIntentConfigured {
		projectID, projectErr := projectgraph.NewResourceID(project)
		if projectErr != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationFinalizeRelease(), projectErr)
			return
		}
		requestID, correlationID := releaseAuditRequestIdentity(r)
		intent, intentErr := buildReleaseCreatedAuditIntent(releaseAuditCommandInput{
			OperationID: string(releasegen.GenOperationFinalizeRelease), ProjectID: projectID, ReleaseID: releaseID,
			IdempotencyKey: idempotencyKey, PrincipalID: principal.ID, RequestID: requestID, CorrelationID: correlationID,
			Status: m.finalizeExecution.InitialState,
		})
		if intentErr != nil {
			m.writeCommandFailure(w, r, releasegen.GenCommandOperationFinalizeRelease(), intentErr)
			return
		}
		ctx = release.WithAuditIntent(ctx, intent)
	}
	var row release.Release
	executor, err := apigencommand.NewExecutor(releasegen.GetAPIGenCommandRuntimeContract, m.logger)
	if err == nil {
		err = executor.Execute(ctx, string(releasegen.GenOperationFinalizeRelease), apigencommand.Execution{
			Transactional: func(ctx context.Context, contract apigencommand.Contract) error {
				if contract.Execution == nil {
					return fmt.Errorf("finalize release async execution contract is unavailable")
				}
				execution := contract.Execution
				var mutationErr error
				row, mutationErr = m.service.BeginFinalization(ctx, project, releaseID, jobs.WorkflowIntent{
					Event: jobs.EventInput{
						Key: execution.InitialEvent, ResourceKind: execution.ResourceKind, ResourceID: releaseID,
						EventType: execution.InitialEvent, Data: []byte(event),
					},
					Job: jobs.EnqueueInput{
						ID: "release:" + releaseID + ":finalize", Kind: execution.JobKind,
						WorkloadClass: "control", PrincipalID: principal.ID, GroupIDs: nil, EstimatedMemoryBytes: 16 << 20,
						PartitionKey: "release:" + project,
						ResourceKind: execution.ResourceKind, ResourceID: releaseID, Payload: payload,
					},
				})
				return mutationErr
			},
		})
	}
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationFinalizeRelease(), err)
		return
	}
	w.Header().Set("Location", location(project, releaseID))
	apitransport.WriteJSON(w, http.StatusAccepted, response(row))
}

func (m *Module) writeCommandFailure(w http.ResponseWriter, r *http.Request, operationID releasegen.GenCommandOperationID, err error) {
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, m.logger, operationID, releasegen.GetAPIGenCommandFailureContracts, err)
}

func (m *Module) ListReleaseEvents(w http.ResponseWriter, r *http.Request, project, releaseID string, limit *int32, pageToken *string) {
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		writeError(w, r, release.ErrInvalid)
		return
	}
	if _, err := m.service.Get(r.Context(), projectID, releaseID); err != nil {
		writeError(w, r, err)
		return
	}
	if m.api.Jobs == nil {
		apitransport.WriteProblem(w, r, http.StatusServiceUnavailable, "ASYNC_EVENT_STORE_UNAVAILABLE", "Release events are unavailable", nil)
		return
	}
	jobhttp.WriteEventPage(w, r, m.api.Jobs, m.finalizeExecution.ResourceKind, releaseID, limit, pageToken, "release:"+project+":"+releaseID)
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return releasehttp.DispatchAPIGenOperation(operationID, m, logger, w, r)
}

func (m *Module) currentPrincipal(r *http.Request) (Principal, bool) {
	if m == nil || m.api.CurrentPrincipal == nil {
		return Principal{}, false
	}
	return m.api.CurrentPrincipal(r)
}

func (m *Module) appendEvent(ctx context.Context, releaseID, eventType string, data any) error {
	if m == nil || m.api.Jobs == nil {
		return errors.New("release event store is unavailable")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = m.api.Jobs.AppendEvent(ctx, "release", releaseID, eventType, encoded)
	return err
}

func (m *Module) appendEncodedEvent(ctx context.Context, releaseID, eventType, data string) error {
	if m == nil || m.api.Jobs == nil {
		return errors.New("release event store is unavailable")
	}
	_, err := m.api.Jobs.AppendEvent(ctx, "release", releaseID, eventType, []byte(data))
	return err
}

func (m *Module) recordBestEffortEvent(
	ctx context.Context,
	operationID, releaseID, eventType string,
	data any,
) {
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	executor, err := apigencommand.NewExecutor(releasegen.GetAPIGenCommandRuntimeContract, logger)
	if err != nil {
		logger.ErrorContext(ctx, "release command executor is unavailable", "operation_id", operationID, "error", err)
		return
	}
	err = executor.Execute(ctx, operationID, apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if eventType != contract.AuditAction {
				return fmt.Errorf("release audit action %q does not match generated action %q", eventType, contract.AuditAction)
			}
			encoded, err := encodeReleaseAuditPayload(operationID, data)
			if err != nil {
				return err
			}
			return m.appendEncodedEvent(ctx, releaseID, contract.AuditAction, encoded)
		},
		LogMessage:    "release audit failed",
		LogAttributes: []slog.Attr{slog.String("release_id", releaseID)},
	})
	if err != nil {
		logger.ErrorContext(ctx, "release command contract execution failed", "operation_id", operationID, "error", err)
	}
}

func encodeReleaseAuditPayload(operationID string, data any) (string, error) {
	if values, ok := data.(map[string]any); ok {
		str := func(key string) string { value, _ := values[key].(string); return value }
		switch operationID {
		case string(releasegen.GenOperationCreateRelease):
			return releasegen.EncodeGenCreateReleaseAuditPayload(releasegen.GenSchemaReleaseCreatedAuditPayload{OperationId: operationID, ReleaseId: str("releaseId"), ProjectId: str("projectId"), ProjectDigest: str("projectDigest"), Status: str("status"), CreatedBy: str("createdBy")})
		case string(releasegen.GenOperationUploadReleaseArtifact):
			return "", fmt.Errorf("release artifact audit contract is not generation-scoped")
		case string(releasegen.GenOperationFinalizeRelease):
			return releasegen.EncodeGenFinalizeReleaseAuditPayload(releasegen.GenSchemaReleaseValidatingAuditPayload{OperationId: operationID, ReleaseId: str("releaseId"), ProjectId: str("projectId"), Status: str("status")})
		}
	}
	switch operationID {
	case string(releasegen.GenOperationCreateRelease):
		payload, ok := data.(releasegen.GenSchemaReleaseCreatedAuditPayload)
		if !ok {
			return "", fmt.Errorf("release create audit payload has type %T", data)
		}
		return releasegen.EncodeGenCreateReleaseAuditPayload(payload)
	case string(releasegen.GenOperationUploadReleaseArtifact):
		return "", fmt.Errorf("release artifact audit contract is not generation-scoped")
	case string(releasegen.GenOperationFinalizeRelease):
		payload, ok := data.(releasegen.GenSchemaReleaseValidatingAuditPayload)
		if !ok {
			return "", fmt.Errorf("release finalize audit payload has type %T", data)
		}
		return releasegen.EncodeGenFinalizeReleaseAuditPayload(payload)
	default:
		return "", fmt.Errorf("release operation %q has no audit payload encoder", operationID)
	}
}

func response(row release.Release) releaseapi.Response {
	result := releaseapi.Response{
		ID: row.ID, ProjectID: row.ServingIdentity.ProjectID.String(), Environment: row.ServingIdentity.Environment, GenerationID: row.ServingIdentity.GenerationID,
		ArtifactDigest: row.ArtifactDigest, ActualDigest: row.ActualDigest, ArtifactSize: row.ArtifactSizeBytes,
		ProjectDigest: row.ProjectDigest, Status: releaseapi.Status(row.Status), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
		Connections: make([]releaseapi.ConnectionPin, 0, len(row.Manifest.Connections)),
		Provenance:  releaseProvenanceToAPI(row.Provenance),
	}
	for _, item := range row.Manifest.Connections {
		result.Connections = append(result.Connections, releaseapi.ConnectionPin{Connection: item.ConnectionID, RevisionID: item.RevisionID})
	}
	if row.FinalizedAt != "" {
		result.FinalizedAt = &row.FinalizedAt
	}
	if row.Error != "" {
		result.Error = &row.Error
	}
	return result
}

func releaseProvenanceFromAPI(value *releaseapi.Provenance) (*release.Provenance, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var mapped release.Provenance
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil, err
	}
	if err := mapped.Validate(); err != nil {
		return nil, err
	}
	return &mapped, nil
}

func releaseProvenanceToAPI(value *release.Provenance) *releaseapi.Provenance {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var mapped releaseapi.Provenance
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil
	}
	return &mapped
}

func location(project, releaseID string) string {
	return "/api/v1/projects/" + project + "/releases/" + releaseID
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
	switch {
	case errors.Is(err, release.ErrInvalid):
		status, code = http.StatusUnprocessableEntity, "INVALID_RELEASE"
	case errors.Is(err, release.ErrNotFound):
		status, code = http.StatusNotFound, "RELEASE_NOT_FOUND"
	case errors.Is(err, release.ErrIncomplete), errors.Is(err, release.ErrConflict), errors.Is(err, release.ErrImmutable):
		status, code = http.StatusConflict, "RELEASE_CONFLICT"
	case errors.Is(err, release.ErrDigest):
		status, code = http.StatusUnprocessableEntity, "CONTENT_DIGEST_MISMATCH"
	}
	detail := err.Error()
	if status == http.StatusInternalServerError {
		detail = "The release request could not be completed"
	}
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}
