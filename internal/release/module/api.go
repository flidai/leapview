package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobhttp "github.com/flidai/leapview/internal/platform/jobs/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releaseapi "github.com/flidai/leapview/internal/release/api"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
	releasefilesystem "github.com/flidai/leapview/internal/release/filesystem"
	releasehttp "github.com/flidai/leapview/internal/release/http"
)

type Principal struct {
	ID string
}

type PageParams = releaseapi.PageParams

type JobStore interface {
	Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error)
	AppendEvent(context.Context, string, string, string, []byte) (jobs.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error)
}

type APIConfig struct {
	CurrentPrincipal func(*http.Request) (Principal, bool)
	Jobs             JobStore
	Workflow         jobs.WorkflowRecorder
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
	created, err := m.service.Create(r.Context(), input)
	if err != nil {
		m.writeCommandFailure(w, r, releasegen.GenCommandOperationCreateRelease(), err)
		return
	}
	m.recordBestEffortEvent(
		r.Context(), string(releasegen.GenOperationCreateRelease), created.ID,
		releaseCreatedAuditAction, releasegen.GenSchemaReleaseCreatedAuditPayload{
			OperationId: string(releasegen.GenOperationCreateRelease), ReleaseId: created.ID,
			ProjectId: created.ServingIdentity.ProjectID.String(), ProjectDigest: created.ProjectDigest,
			Status: string(created.Status), CreatedBy: created.CreatedBy,
		},
	)
	w.Header().Set("Location", location(project, created.ID))
	apitransport.WriteJSON(w, http.StatusCreated, response(created))
}

func (m *Module) ListReleases(w http.ResponseWriter, r *http.Request, project string, limit *int32, pageToken *string) {
	rows, err := m.service.List(r.Context(), project)
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
	row, err := m.service.Get(r.Context(), project, releaseID)
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
	artifact, err := m.service.UploadArtifact(r.Context(), project, releaseID, contentDigest, http.MaxBytesReader(w, r.Body, releasefilesystem.MaxUploadBytes))
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
	w.Header().Set("Location", location(project, releaseID)+"/artifact")
	apitransport.WriteJSON(w, http.StatusCreated, result)
}

func (m *Module) FinalizeRelease(w http.ResponseWriter, r *http.Request, project, releaseID, _ string) {
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
	var row release.Release
	executor, err := apigencommand.NewExecutor(releasegen.GetAPIGenCommandRuntimeContract, m.logger)
	if err == nil {
		err = executor.Execute(r.Context(), string(releasegen.GenOperationFinalizeRelease), apigencommand.Execution{
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
						WorkloadClass: "control", WorkspaceID: "_node",
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
	if _, err := m.service.Get(r.Context(), project, releaseID); err != nil {
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
