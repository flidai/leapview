package http

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	stdhttp "net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata"
	apigenapi "github.com/flidai/leapview/internal/manageddata/api"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	defaultPageLimit  = 50
	maxPageLimit      = 200
	maxManifestFiles  = 10_000
	maxCompletedParts = 10_000
)

var (
	scopeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	revisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (h *Handler) GetActiveManagedDataRevision(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection string) {
	collection, ok := h.collection(w, r, project, connection)
	if !ok {
		return
	}
	environment := h.options.Environment
	if !validScopeID(environment) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	pointer, err := h.options.Repository.EnvironmentPointer(r.Context(), collection.ID.String(), manageddata.Environment(environment))
	if errors.Is(err, manageddata.ErrNotFound) || errors.Is(err, ErrNotFound) {
		h.writeJSON(w, stdhttp.StatusOK, apigenapi.ManagedDataActiveRevisionResponse{})
		return
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if pointer.CollectionID.String() != collection.ID.String() || string(pointer.Environment) != environment {
		h.writeError(w, r, ErrNotFound)
		return
	}
	if pointer.RevisionDigest == "" {
		h.writeError(w, r, ErrNotFound)
		return
	}
	metadata, err := h.options.Repository.RevisionByID(r.Context(), collection.ID.String(), pointer.RevisionDigest)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	summary, err := revisionSummary(metadata, collection.ID.String())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response := apigenapi.ManagedDataActiveRevisionResponse{Revision: &summary}
	response.DeploymentId = stringPointer(pointer.DeploymentID)
	response.ActivatedAt = stringPointer(pointer.UpdatedAt)
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) ListManagedDataRevisions(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection string, params apigenapi.GenListManagedDataRevisionsParams) {
	collection, ok := h.collection(w, r, project, connection)
	if !ok {
		return
	}
	rows, err := h.options.Repository.ListRevisions(r.Context(), collection.ID.String())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Revision.CreatedAt == rows[j].Revision.CreatedAt {
			return rows[i].PublicID > rows[j].PublicID
		}
		return rows[i].Revision.CreatedAt > rows[j].Revision.CreatedAt
	})
	items := make([]apigenapi.ManagedDataRevisionSummaryResponse, 0, len(rows))
	for _, row := range rows {
		if row.Revision.Status != manageddata.RevisionStatusReady {
			continue
		}
		item, mapErr := revisionSummary(row, collection.ID.String())
		if mapErr != nil {
			h.writeError(w, r, mapErr)
			return
		}
		items = append(items, item)
	}
	page, next, err := pageSlice(items, params.Limit, params.PageToken, "revisions\x00"+collection.ID.String(), func(item apigenapi.ManagedDataRevisionSummaryResponse) string {
		return item.CreatedAt + "\x00" + item.Id
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, apigenapi.ManagedDataRevisionListResponse{Items: page, Page: apigenapi.PageInfo{NextCursor: next}})
}

func (h *Handler) GetManagedDataRevision(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, revisionID string) {
	collection, ok := h.collection(w, r, project, connection)
	if !ok {
		return
	}
	if !revisionPattern.MatchString(revisionID) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	metadata, err := h.options.Repository.RevisionByID(r.Context(), collection.ID.String(), revisionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response, err := revisionResponse(metadata, collection.ID.String())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) CreateManagedDataUploadSession(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection string, headers apigenapi.GenCreateManagedDataUploadSessionHeaders) {
	if h.options.Uploads == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession())
		return
	}
	if !validScope(project, connection) || !validIdempotencyKey(headers.IdempotencyKey) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	if !h.authorizeConnection(w, r, project, connection, access.CapabilityResourceEdit) {
		return
	}
	var body apigenapi.ManagedDataUploadSessionCreateRequest
	if err := h.decodeRequiredJSON(w, r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	manifest, err := manifestFromWire(body.Manifest)
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
		return
	}
	actor, ok := h.commandAuditActorForOperation(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession())
	if !ok {
		return
	}
	targetID, err := control.DeterministicUploadID(project, connection, headers.IdempotencyKey)
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
		return
	}
	auditIntent, err := h.buildAuditIntent(r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), actor, project, connection, "managed_data_upload_session", targetID)
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
		return
	}
	result, err := h.options.Uploads.BeginUpload(r.Context(), control.BeginUploadRequest{
		Project: project, Connection: connection, Manifest: manifest, Actor: actor, IdempotencyKey: headers.IdempotencyKey, AuditIntent: auditIntent,
	})
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
		return
	}
	if h.options.RecordUploadCreated != nil {
		if err := h.options.RecordUploadCreated(r.Context(), result); err != nil {
			h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
			return
		}
	}
	response, err := uploadResponse(result, project, connection, "")
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
		return
	}
	if err := h.completeCommand(r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession()); err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCreateManagedDataUploadSession(), err)
		return
	}
	h.writeJSON(w, stdhttp.StatusCreated, response)
}

func (h *Handler) GetManagedDataUploadSession(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession string) {
	result, ok := h.recoverUpload(w, r, project, connection, uploadSession)
	if !ok {
		return
	}
	response, err := uploadResponse(result, project, connection, uploadSession)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) ListManagedDataUploadSessions(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection string, params apigenapi.GenListManagedDataUploadSessionsParams) {
	if h.options.Uploads == nil {
		h.writeUnavailable(w, r)
		return
	}
	collection, ok := h.collection(w, r, project, connection)
	if !ok {
		return
	}
	rows, err := h.options.Repository.ListUploadSessions(r.Context(), collection.ID.String())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt == rows[j].CreatedAt {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt > rows[j].CreatedAt
	})
	page, next, err := pageSlice(rows, params.Limit, params.PageToken, "upload-sessions\x00"+collection.ID.String(), func(item manageddata.UploadSession) string { return item.CreatedAt + "\x00" + item.ID.String() })
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]apigenapi.ManagedDataUploadSessionResponse, 0, len(page))
	for _, row := range page {
		result, recoverErr := h.options.Uploads.RecoverUpload(r.Context(), control.UploadRequest{Project: project, Connection: connection, UploadID: row.ID.String()})
		if recoverErr != nil {
			h.writeError(w, r, recoverErr)
			return
		}
		item, mapErr := uploadResponse(result, project, connection, row.ID.String())
		if mapErr != nil {
			h.writeError(w, r, mapErr)
			return
		}
		items = append(items, item)
	}
	h.writeJSON(w, stdhttp.StatusOK, apigenapi.ManagedDataUploadSessionListResponse{Items: items, Page: apigenapi.PageInfo{NextCursor: next}})
}

func (h *Handler) CancelManagedDataUploadSession(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession string, headers apigenapi.GenCancelManagedDataUploadSessionHeaders) {
	if h.options.Uploads == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession())
		return
	}
	if !validUploadScope(project, connection, uploadSession) || !validIdempotencyKey(headers.IdempotencyKey) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	if !h.authorizeConnection(w, r, project, connection, access.CapabilityResourceEdit) {
		return
	}
	actor, ok := h.commandAuditActorForOperation(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession())
	if !ok {
		return
	}
	auditIntent, err := h.buildAuditIntent(r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession(), actor, project, connection, "managed_data_upload_session", uploadSession)
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession(), err)
		return
	}
	abort := h.options.Uploads.AbortUpload
	if h.options.AbortUpload != nil {
		abort = h.options.AbortUpload
	}
	result, err := abort(r.Context(), control.UploadRequest{Project: project, Connection: connection, UploadID: uploadSession, Actor: actor, AuditIntent: auditIntent})
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession(), err)
		return
	}
	// An explicitly supplied AbortUpload is the module's atomic capability: it
	// records the terminal workflow event in the same transaction as the state
	// transition. Do not perform a fallible post-commit event append on that
	// path. The callback remains for adapters that only expose the legacy
	// control service.
	if h.options.AbortUpload == nil && result.Status == manageddata.UploadStatusAborted && h.options.RecordUploadCancelled != nil {
		if err := h.options.RecordUploadCancelled(r.Context(), result); err != nil {
			h.writeCommandError(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession(), err)
			return
		}
	}
	response, err := uploadResponse(result, project, connection, uploadSession)
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession(), err)
		return
	}
	if err := h.completeCommand(r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession()); err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCancelManagedDataUploadSession(), err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) FinalizeManagedDataUploadSession(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession string, headers apigenapi.GenFinalizeManagedDataUploadSessionHeaders) {
	if h.options.Uploads == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession())
		return
	}
	if !validUploadScope(project, connection, uploadSession) || !validIdempotencyKey(headers.IdempotencyKey) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	if !h.authorizeConnection(w, r, project, connection, access.CapabilityResourceEdit) {
		return
	}
	actor, ok := h.commandAuditActorForOperation(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession())
	if !ok {
		return
	}
	auditIntent, err := h.buildAuditIntent(r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession(), actor, project, connection, "managed_data_upload_session", uploadSession)
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession(), err)
		return
	}
	request := control.UploadRequest{Project: project, Connection: connection, UploadID: uploadSession, Actor: actor, AuditIntent: auditIntent}
	var result control.UploadResult
	if h.options.BeginFinalize != nil {
		result, err = h.options.BeginFinalize(r.Context(), request)
	} else {
		result, err = h.options.Uploads.BeginFinalizeUpload(r.Context(), request)
	}
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession(), err)
		return
	}
	response, err := uploadResponse(result, project, connection, uploadSession)
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession(), err)
		return
	}
	if h.options.BeginFinalize == nil && h.options.EnqueueFinalize == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession())
		return
	}
	if h.options.BeginFinalize == nil {
		err = h.options.EnqueueFinalize(r.Context(), request)
	}
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession(), err)
		return
	}
	if err := h.completeCommand(r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession()); err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationFinalizeManagedDataUploadSession(), err)
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+project+"/connections/"+connection+"/upload-sessions/"+uploadSession)
	h.writeJSON(w, stdhttp.StatusAccepted, response)
}

func (h *Handler) CreateManagedDataS3MultipartUpload(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession string, headers apigenapi.GenCreateManagedDataS3MultipartUploadHeaders) {
	if h.options.Multipart == nil || h.options.Uploads == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload())
		return
	}
	if !validUploadScope(project, connection, uploadSession) || !validIdempotencyKey(headers.IdempotencyKey) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	var body apigenapi.ManagedDataS3MultipartCreateRequest
	if err := h.decodeRequiredJSON(w, r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	upload, ok := h.recoverUploadCommand(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), project, connection, uploadSession)
	if !ok {
		return
	}
	file, found := uploadFile(upload, body.Path)
	if !found {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), ErrNotFound)
		return
	}
	if file.Status == control.FileStatusVerified || file.Transport.Protocol != control.ProtocolS3Multipart {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), ErrConflict)
		return
	}
	actor, ok := h.commandAuditActorForOperation(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload())
	if !ok {
		return
	}
	targetID := s3multipart.DeterministicMultipartUploadID(uploadSession, headers.IdempotencyKey)
	auditIntent, err := h.buildAuditIntent(r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), actor, project, connection, "managed_data_s3_multipart_upload", targetID)
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), err)
		return
	}
	result, err := h.options.Multipart.Create(r.Context(), s3multipart.CreateRequest{
		Project: project, Connection: connection, UploadSessionID: uploadSession,
		Path: body.Path, IdempotencyKey: headers.IdempotencyKey, AuditIntent: auditIntent,
	})
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), err)
		return
	}
	response, err := multipartResponse(result, upload, "")
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), err)
		return
	}
	if err := h.completeCommand(r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload()); err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCreateManagedDataS3MultipartUpload(), err)
		return
	}
	h.writeJSON(w, stdhttp.StatusCreated, response)
}

func (h *Handler) SignManagedDataS3MultipartPart(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession, multipartUpload string, partNumber int32) {
	if h.options.Multipart == nil || h.options.Uploads == nil {
		h.writeUnavailable(w, r)
		return
	}
	if !validUploadScope(project, connection, uploadSession) || !validResourceID(multipartUpload, 160) || partNumber < 1 || partNumber > 10_000 {
		h.writeError(w, r, ErrInvalid)
		return
	}
	var body apigenapi.ManagedDataS3MultipartSignPartRequest
	if err := h.decodeRequiredJSON(w, r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	if body.Size < 1 || body.Sha256 != nil && !digestPattern.MatchString(*body.Sha256) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	if _, ok := h.recoverUploadWithCapability(w, r, project, connection, uploadSession, access.CapabilityResourceEdit); !ok {
		return
	}
	if _, ok := h.actor(w, r); !ok {
		return
	}
	result, err := h.options.Multipart.SignPart(r.Context(), s3multipart.SignPartRequest{
		Project: project, Connection: connection, UploadSessionID: uploadSession, MultipartUploadID: multipartUpload,
		PartNumber: partNumber, Size: int64(body.Size), SHA256: valueOrEmpty(body.Sha256),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response, err := signedPartResponse(result, uploadSession, multipartUpload, partNumber)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) CompleteManagedDataS3MultipartUpload(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession, multipartUpload string, headers apigenapi.GenCompleteManagedDataS3MultipartUploadHeaders) {
	if h.options.Multipart == nil || h.options.Uploads == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload())
		return
	}
	if !validMultipartMutation(project, connection, uploadSession, multipartUpload, headers.IdempotencyKey) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	var body apigenapi.ManagedDataS3MultipartCompleteRequest
	if err := h.decodeRequiredJSON(w, r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	if len(body.Parts) < 1 || len(body.Parts) > maxCompletedParts {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), ErrTooLarge)
		return
	}
	parts := make([]s3multipart.CompletedPart, len(body.Parts))
	seen := make(map[int32]struct{}, len(body.Parts))
	for i, part := range body.Parts {
		if part.PartNumber < 1 || part.PartNumber > 10_000 || strings.TrimSpace(part.Etag) == "" || len(part.Etag) > 1024 || part.Sha256 != nil && !digestPattern.MatchString(*part.Sha256) {
			h.writeCommandError(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), ErrInvalid)
			return
		}
		if _, exists := seen[part.PartNumber]; exists {
			h.writeCommandError(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), ErrInvalid)
			return
		}
		seen[part.PartNumber] = struct{}{}
		parts[i] = s3multipart.CompletedPart{PartNumber: part.PartNumber, ETag: part.Etag, SHA256: valueOrEmpty(part.Sha256)}
	}
	upload, ok := h.recoverUploadCommand(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), project, connection, uploadSession)
	if !ok {
		return
	}
	actor, ok := h.commandAuditActorForOperation(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload())
	if !ok {
		return
	}
	auditIntent, err := h.buildAuditIntent(r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), actor, project, connection, "managed_data_s3_multipart_upload", multipartUpload)
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), err)
		return
	}
	result, err := h.options.Multipart.Complete(r.Context(), s3multipart.CompleteRequest{
		Project: project, Connection: connection, UploadSessionID: uploadSession, MultipartUploadID: multipartUpload,
		IdempotencyKey: headers.IdempotencyKey, Parts: parts, AuditIntent: auditIntent,
	})
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), err)
		return
	}
	response, err := multipartResponse(result, upload, multipartUpload)
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), err)
		return
	}
	if err := h.completeCommand(r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload()); err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationCompleteManagedDataS3MultipartUpload(), err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) AbortManagedDataS3MultipartUpload(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession, multipartUpload string, headers apigenapi.GenAbortManagedDataS3MultipartUploadHeaders) {
	if h.options.Multipart == nil || h.options.Uploads == nil {
		h.writeCommandUnavailable(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload())
		return
	}
	if !validMultipartMutation(project, connection, uploadSession, multipartUpload, headers.IdempotencyKey) {
		h.writeError(w, r, ErrInvalid)
		return
	}
	upload, ok := h.recoverUploadCommand(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload(), project, connection, uploadSession)
	if !ok {
		return
	}
	actor, ok := h.commandAuditActorForOperation(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload())
	if !ok {
		return
	}
	auditIntent, err := h.buildAuditIntent(r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload(), actor, project, connection, "managed_data_s3_multipart_upload", multipartUpload)
	if err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload(), err)
		return
	}
	result, err := h.options.Multipart.Abort(r.Context(), s3multipart.AbortRequest{
		Project: project, Connection: connection, UploadSessionID: uploadSession,
		MultipartUploadID: multipartUpload, IdempotencyKey: headers.IdempotencyKey, AuditIntent: auditIntent,
	})
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload(), err)
		return
	}
	response, err := multipartResponse(result, upload, multipartUpload)
	if err != nil {
		h.writeCommandError(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload(), err)
		return
	}
	if err := h.completeCommand(r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload()); err != nil {
		h.writeCommandUnavailableCause(w, r, manageddatagen.GenCommandOperationAbortManagedDataS3MultipartUpload(), err)
		return
	}
	h.writeJSON(w, stdhttp.StatusOK, response)
}

func (h *Handler) collection(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection string) (manageddata.Collection, bool) {
	if h.options.Repository == nil {
		h.writeUnavailable(w, r)
		return manageddata.Collection{}, false
	}
	if !validScope(project, connection) {
		h.writeError(w, r, ErrInvalid)
		return manageddata.Collection{}, false
	}
	if !h.authorizeConnection(w, r, project, connection, access.CapabilityResourceRead) {
		return manageddata.Collection{}, false
	}
	collection, err := h.options.Repository.CollectionByProjectConnection(r.Context(), project, connection)
	if err != nil {
		h.writeError(w, r, err)
		return manageddata.Collection{}, false
	}
	if collection.ProjectID.String() != project || collection.ConnectionID.String() != connection || collection.Status != manageddata.CollectionStatusActive {
		h.writeError(w, r, ErrNotFound)
		return manageddata.Collection{}, false
	}
	return collection, true
}

func (h *Handler) recoverUpload(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession string) (control.UploadResult, bool) {
	result, err := h.recoverUploadResult(r, project, connection, uploadSession, access.CapabilityResourceRead)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnavailable):
			h.writeUnavailable(w, r)
		default:
			h.writeError(w, r, err)
		}
		return control.UploadResult{}, false
	}
	return result, true
}

func (h *Handler) recoverUploadCommand(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID manageddatagen.GenCommandOperationID, project, connection, uploadSession string) (control.UploadResult, bool) {
	result, err := h.recoverUploadResult(r, project, connection, uploadSession, access.CapabilityResourceEdit)
	if err != nil {
		h.writeCommandError(w, r, operationID, err)
		return control.UploadResult{}, false
	}
	return result, true
}

func (h *Handler) recoverUploadWithCapability(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection, uploadSession string, capability access.Capability) (control.UploadResult, bool) {
	result, err := h.recoverUploadResult(r, project, connection, uploadSession, capability)
	if err != nil {
		h.writeError(w, r, err)
		return control.UploadResult{}, false
	}
	return result, true
}

func (h *Handler) recoverUploadResult(r *stdhttp.Request, project, connection, uploadSession string, capability access.Capability) (control.UploadResult, error) {
	if h.options.Uploads == nil {
		return control.UploadResult{}, ErrUnavailable
	}
	if !validUploadScope(project, connection, uploadSession) {
		return control.UploadResult{}, ErrInvalid
	}
	if err := h.authorizeConnectionContext(r, project, connection, capability); err != nil {
		return control.UploadResult{}, err
	}
	result, err := h.options.Uploads.RecoverUpload(r.Context(), control.UploadRequest{Project: project, Connection: connection, UploadID: uploadSession})
	if err != nil {
		return control.UploadResult{}, err
	}
	if result.ID != uploadSession || result.Collection.Project != project || result.Collection.Connection != connection || result.Collection.ID == "" {
		return control.UploadResult{}, ErrNotFound
	}
	return result, nil
}

func (h *Handler) authorizeConnection(w stdhttp.ResponseWriter, r *stdhttp.Request, project, connection string, capability access.Capability) bool {
	if err := h.authorizeConnectionContext(r, project, connection, capability); err != nil {
		h.writeError(w, r, err)
		return false
	}
	return true
}

func (h *Handler) authorizeConnectionContext(r *stdhttp.Request, project, connection string, capability access.Capability) error {
	// Route parameters are still validated for development principals. The
	// bypass skips only the mutable authorization snapshot; it never admits an
	// arbitrary project/connection selector or malformed resource identity.
	if !validScope(project, connection) {
		return ErrInvalid
	}
	if h.options.CurrentPrincipal == nil {
		return ErrUnavailable
	}
	principal, ok := h.options.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return ErrUnauthorized
	}
	if principal.DevBypass {
		return nil
	}
	if h.options.AuthorizeConnection == nil {
		return ErrUnavailable
	}
	allowed, err := h.options.AuthorizeConnection(r.Context(), strings.TrimSpace(principal.ID), project, connection, capability)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (h *Handler) actor(w stdhttp.ResponseWriter, r *stdhttp.Request) (string, bool) {
	if h.options.CurrentPrincipal == nil {
		h.writeError(w, r, errors.New("current principal callback is not configured"))
		return "", false
	}
	principal, ok := h.options.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		h.writeError(w, r, errors.New("current principal is unavailable"))
		return "", false
	}
	return strings.TrimSpace(principal.ID), true
}

func (h *Handler) decodeRequiredJSON(w stdhttp.ResponseWriter, r *stdhttp.Request, target any) error {
	if r.Body == nil || r.Body == stdhttp.NoBody {
		return ErrInvalid
	}
	return h.decodeJSON(w, r, target, false)
}

func (h *Handler) decodeOptionalJSON(w stdhttp.ResponseWriter, r *stdhttp.Request, target any) error {
	if r.Body == nil || r.Body == stdhttp.NoBody {
		return nil
	}
	return h.decodeJSON(w, r, target, true)
}

func (h *Handler) decodeJSON(w stdhttp.ResponseWriter, r *stdhttp.Request, target any, optional bool) error {
	r.Body = stdhttp.MaxBytesReader(w, r.Body, h.options.MaxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxErr *stdhttp.MaxBytesError
		if errors.As(err, &maxErr) {
			return ErrTooLarge
		}
		if optional && errors.Is(err, io.EOF) {
			return nil
		}
		return ErrInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		var maxErr *stdhttp.MaxBytesError
		if errors.As(err, &maxErr) {
			return ErrTooLarge
		}
		return ErrInvalid
	}
	return nil
}

func (h *Handler) writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) writeUnavailable(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.writePublicError(w, r, stdhttp.StatusServiceUnavailable, "managed-data service is not configured")
}

// writeCommandError resolves classified domain failures through the generated
// operation contract.
func (h *Handler) writeCommandError(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID manageddatagen.GenCommandOperationID, err error) {
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, h.options.Logger, operationID, manageddatagen.GetAPIGenCommandFailureContracts, err)
}

func (h *Handler) writeCommandUnavailable(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID manageddatagen.GenCommandOperationID) {
	h.writeCommandError(w, r, operationID, ErrUnavailable)
}

func (h *Handler) writeCommandUnavailableCause(w stdhttp.ResponseWriter, r *stdhttp.Request, operationID manageddatagen.GenCommandOperationID, cause error) {
	if cause == nil {
		cause = ErrUnavailable
	} else {
		cause = apigenfailure.Wrap("unavailable", cause)
	}
	h.writeCommandError(w, r, operationID, cause)
}

func (h *Handler) writeError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	status := statusForError(err)
	message := "managed-data service failed"
	switch status {
	case stdhttp.StatusBadRequest:
		message = "invalid managed-data request"
	case stdhttp.StatusNotFound:
		message = "managed-data resource not found"
	case stdhttp.StatusConflict:
		message = "managed-data request conflicts with current state"
	case stdhttp.StatusRequestEntityTooLarge:
		message = "managed-data request is too large"
	case stdhttp.StatusBadGateway:
		message = "managed-data storage is unavailable"
	}
	h.writePublicError(w, r, status, message)
}

func (h *Handler) writePublicError(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, message string) {
	details := map[string]any{}
	requestID := r.Header.Get("X-Request-Id")
	_ = details
	w.Header().Set("Content-Type", "application/problem+json")
	h.writeJSON(w, status, apigenapi.ProblemDetails{
		Type: "https://leapview.dev/problems/managed-data", Title: stdhttp.StatusText(status), Status: int32(status),
		Detail: message, Instance: r.URL.Path, Code: fmt.Sprintf("MANAGED_DATA_%d", status), RequestId: requestID,
		Errors: []apigenapi.ProblemFieldError{},
	})
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return stdhttp.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return stdhttp.StatusForbidden
	case errors.Is(err, ErrTooLarge):
		return stdhttp.StatusRequestEntityTooLarge
	case errors.Is(err, ErrInvalid), errors.Is(err, control.ErrInvalid):
		return stdhttp.StatusBadRequest
	case errors.Is(err, ErrNotFound), errors.Is(err, manageddata.ErrNotFound), errors.Is(err, control.ErrNotFound):
		return stdhttp.StatusNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, manageddata.ErrConflict), errors.Is(err, control.ErrConflict), errors.Is(err, control.ErrIncomplete), errors.Is(err, control.ErrExpired), errors.Is(err, control.ErrIntegrity):
		return stdhttp.StatusConflict
	case errors.Is(err, ErrBackend), errors.Is(err, control.ErrBackend):
		return stdhttp.StatusBadGateway
	case errors.Is(err, ErrUnavailable):
		return stdhttp.StatusServiceUnavailable
	default:
		return stdhttp.StatusInternalServerError
	}
}

func revisionSummary(metadata RevisionMetadata, collectionID string) (apigenapi.ManagedDataRevisionSummaryResponse, error) {
	revision := metadata.Revision
	publicID := metadata.PublicID
	fileCount, err := checkedInt32(revision.FileCount)
	if err != nil || revision.CollectionID.String() != collectionID || revision.Status != manageddata.RevisionStatusReady || !revisionPattern.MatchString(publicID) || !validResourceID(metadata.UploadSessionID, 160) || revision.CreatedAt == "" {
		if revision.CollectionID.String() != collectionID {
			return apigenapi.ManagedDataRevisionSummaryResponse{}, ErrNotFound
		}
		return apigenapi.ManagedDataRevisionSummaryResponse{}, errors.New("invalid revision metadata")
	}
	if fileCount < 1 || revision.SizeBytes < 0 {
		return apigenapi.ManagedDataRevisionSummaryResponse{}, errors.New("invalid revision metadata")
	}
	return apigenapi.ManagedDataRevisionSummaryResponse{Id: publicID, Status: apigenapi.ManagedDataRevisionStatusAvailable, FileCount: fileCount, Size: revision.SizeBytes, CreatedAt: revision.CreatedAt, UploadSessionId: metadata.UploadSessionID}, nil
}

func revisionResponse(metadata RevisionMetadata, collectionID string) (apigenapi.ManagedDataRevisionResponse, error) {
	summary, err := revisionSummary(metadata, collectionID)
	if err != nil {
		return apigenapi.ManagedDataRevisionResponse{}, err
	}
	var manifest manageddata.Manifest
	decoder := json.NewDecoder(strings.NewReader(metadata.Revision.ManifestJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.Validate(manageddata.Limits{MaxFiles: maxManifestFiles}) != nil {
		return apigenapi.ManagedDataRevisionResponse{}, errors.New("invalid revision manifest")
	}
	wired, err := manifestToWire(manifest)
	if err != nil {
		return apigenapi.ManagedDataRevisionResponse{}, err
	}
	return apigenapi.ManagedDataRevisionResponse{Id: summary.Id, Status: summary.Status, Manifest: wired, FileCount: summary.FileCount, Size: summary.Size, CreatedAt: summary.CreatedAt, UploadSessionId: summary.UploadSessionId}, nil
}

func manifestFromWire(value apigenapi.ManagedDataManifest) (manageddata.Manifest, error) {
	if len(value.Files) < 1 {
		return manageddata.Manifest{}, ErrInvalid
	}
	if len(value.Files) > maxManifestFiles {
		return manageddata.Manifest{}, ErrTooLarge
	}
	manifest := manageddata.Manifest{Files: make([]manageddata.File, len(value.Files))}
	for i, file := range value.Files {
		if len(file.Path) > 1024 || file.Size < 0 {
			return manageddata.Manifest{}, ErrInvalid
		}
		manifest.Files[i] = manageddata.File{Path: file.Path, Size: int64(file.Size), SHA256: file.Sha256}
	}
	if err := manifest.Validate(manageddata.Limits{MaxFiles: maxManifestFiles}); err != nil {
		return manageddata.Manifest{}, ErrInvalid
	}
	return manifest, nil
}

func manifestToWire(manifest manageddata.Manifest) (apigenapi.ManagedDataManifest, error) {
	if len(manifest.Files) < 1 || len(manifest.Files) > maxManifestFiles {
		return apigenapi.ManagedDataManifest{}, errors.New("invalid manifest size")
	}
	files := make([]apigenapi.ManagedDataFileMetadata, len(manifest.Files))
	for i, file := range manifest.Files {
		if file.Size < 0 {
			return apigenapi.ManagedDataManifest{}, errors.New("invalid manifest file size")
		}
		files[i] = apigenapi.ManagedDataFileMetadata{Path: file.Path, Size: file.Size, Sha256: file.SHA256}
	}
	return apigenapi.ManagedDataManifest{Files: files}, nil
}

func uploadResponse(result control.UploadResult, project, connection, expectedID string) (apigenapi.ManagedDataUploadSessionResponse, error) {
	if result.Collection.Project != project || result.Collection.Connection != connection || result.Collection.ID == "" || !validResourceID(result.ID, 160) || expectedID != "" && result.ID != expectedID {
		return apigenapi.ManagedDataUploadSessionResponse{}, ErrNotFound
	}
	manifest, err := manifestToWire(result.Manifest)
	if err != nil {
		return apigenapi.ManagedDataUploadSessionResponse{}, err
	}
	status, ok := uploadStatus(result.Status)
	if !ok || result.CreatedAt == "" || result.ExpiresAt == "" {
		return apigenapi.ManagedDataUploadSessionResponse{}, errors.New("invalid upload metadata")
	}
	files := make([]apigenapi.ManagedDataFileUploadResponse, len(result.Files))
	for i, file := range result.Files {
		metadata, err := fileToWire(file.File)
		if err != nil {
			return apigenapi.ManagedDataUploadSessionResponse{}, err
		}
		fileStatus := apigenapi.ManagedDataFileUploadStatusPending
		if file.Status == control.FileStatusVerified {
			fileStatus = apigenapi.ManagedDataFileUploadStatusVerified
		} else if result.Status == manageddata.UploadStatusFailed {
			fileStatus = apigenapi.ManagedDataFileUploadStatusFailed
		} else if result.Status == manageddata.UploadStatusAborted || result.Status == manageddata.UploadStatusExpired {
			fileStatus = apigenapi.ManagedDataFileUploadStatusSkipped
		}
		var negotiation *apigenapi.ManagedDataUploadNegotiation
		if file.Transport.Protocol != "" {
			value, negotiationErr := negotiationToWire(file.Transport)
			if negotiationErr != nil {
				return apigenapi.ManagedDataUploadSessionResponse{}, negotiationErr
			}
			negotiation = &value
		}
		files[i] = apigenapi.ManagedDataFileUploadResponse{File: metadata, Status: fileStatus, Negotiation: negotiation}
	}
	revisionID := result.Manifest.RevisionID()
	if !revisionPattern.MatchString(revisionID) {
		return apigenapi.ManagedDataUploadSessionResponse{}, errors.New("invalid upload revision identity")
	}
	response := apigenapi.ManagedDataUploadSessionResponse{Id: result.ID, Project: project, Connection: connection, RevisionId: revisionID, Status: status, Manifest: manifest, Files: files, CreatedAt: result.CreatedAt, ExpiresAt: result.ExpiresAt}
	response.CompletedAt = stringPointer(result.CompletedAt)
	if result.Status == manageddata.UploadStatusFailed {
		response.Error = stringPointer("managed-data upload failed")
	}
	return response, nil
}

func uploadStatus(status manageddata.UploadStatus) (apigenapi.ManagedDataUploadSessionStatus, bool) {
	switch status {
	case manageddata.UploadStatusOpen:
		return apigenapi.ManagedDataUploadSessionStatusOpen, true
	case manageddata.UploadStatusCommitting:
		return apigenapi.ManagedDataUploadSessionStatusFinalizing, true
	case manageddata.UploadStatusComplete:
		return apigenapi.ManagedDataUploadSessionStatusCompleted, true
	case manageddata.UploadStatusAborted:
		return apigenapi.ManagedDataUploadSessionStatusCancelled, true
	case manageddata.UploadStatusFailed:
		return apigenapi.ManagedDataUploadSessionStatusFailed, true
	case manageddata.UploadStatusExpired:
		return apigenapi.ManagedDataUploadSessionStatusExpired, true
	default:
		return "", false
	}
}

func negotiationToWire(value control.TransportDescription) (apigenapi.ManagedDataUploadNegotiation, error) {
	switch value.Protocol {
	case control.ProtocolAlreadyPresent:
		return apigenapi.ManagedDataUploadNegotiation{Protocol: apigenapi.ManagedDataUploadProtocolAlreadyPresent}, nil
	case control.ProtocolTus:
		if value.Tus == nil || value.S3Multipart != nil || value.Tus.Endpoint == "" || value.Tus.UploadID == "" || value.Tus.ExpiresAt == "" {
			return apigenapi.ManagedDataUploadNegotiation{}, errors.New("invalid tus negotiation")
		}
		if value.Tus.Offset < 0 {
			return apigenapi.ManagedDataUploadNegotiation{}, errors.New("invalid tus offset")
		}
		return apigenapi.ManagedDataUploadNegotiation{Protocol: apigenapi.ManagedDataUploadProtocolTus, Tus: &apigenapi.ManagedDataTusUploadNegotiation{Endpoint: value.Tus.Endpoint, UploadId: value.Tus.UploadID, Offset: value.Tus.Offset, ExpiresAt: value.Tus.ExpiresAt}}, nil
	case control.ProtocolS3Multipart:
		if value.S3Multipart == nil || value.Tus != nil {
			return apigenapi.ManagedDataUploadNegotiation{}, errors.New("invalid multipart negotiation")
		}
		if value.S3Multipart.MinimumPartSize <= 0 {
			return apigenapi.ManagedDataUploadNegotiation{}, errors.New("invalid multipart minimum part size")
		}
		if value.S3Multipart.MaximumPartSize < value.S3Multipart.MinimumPartSize {
			return apigenapi.ManagedDataUploadNegotiation{}, errors.New("invalid multipart maximum part size")
		}
		return apigenapi.ManagedDataUploadNegotiation{Protocol: apigenapi.ManagedDataUploadProtocolS3Multipart, S3Multipart: &apigenapi.ManagedDataS3MultipartNegotiation{CreateEndpoint: value.S3Multipart.CreateEndpoint, MinimumPartSize: value.S3Multipart.MinimumPartSize, MaximumPartSize: value.S3Multipart.MaximumPartSize, MaximumParts: value.S3Multipart.MaximumParts}}, nil
	default:
		return apigenapi.ManagedDataUploadNegotiation{}, errors.New("invalid upload negotiation")
	}
}

func multipartResponse(result s3multipart.UploadResult, upload control.UploadResult, expectedID string) (apigenapi.ManagedDataS3MultipartUploadResponse, error) {
	if result.UploadSessionID != upload.ID || !validResourceID(result.ID, 160) || expectedID != "" && result.ID != expectedID || result.CreatedAt == "" {
		return apigenapi.ManagedDataS3MultipartUploadResponse{}, ErrNotFound
	}
	expectedFile, ok := uploadFile(upload, result.File.Path)
	if !ok || expectedFile.File != result.File {
		return apigenapi.ManagedDataS3MultipartUploadResponse{}, ErrNotFound
	}
	file, err := fileToWire(result.File)
	if err != nil {
		return apigenapi.ManagedDataS3MultipartUploadResponse{}, err
	}
	status := apigenapi.ManagedDataS3MultipartStatus(result.Status)
	if status != apigenapi.ManagedDataS3MultipartStatusOpen && status != apigenapi.ManagedDataS3MultipartStatusCompleted && status != apigenapi.ManagedDataS3MultipartStatusAborted {
		return apigenapi.ManagedDataS3MultipartUploadResponse{}, errors.New("invalid multipart status")
	}
	return apigenapi.ManagedDataS3MultipartUploadResponse{Id: result.ID, UploadSessionId: result.UploadSessionID, File: file, Status: status, Existing: result.Existing, CreatedAt: result.CreatedAt, ExpiresAt: stringPointer(result.ExpiresAt)}, nil
}

func signedPartResponse(result s3multipart.SignedPartResult, uploadSession, multipartUpload string, partNumber int32) (apigenapi.ManagedDataS3MultipartSignedPartResponse, error) {
	if result.UploadSessionID != uploadSession || result.MultipartUploadID != multipartUpload || result.PartNumber != partNumber || result.URL == "" || len(result.URL) > 8192 || result.ExpiresAt == "" || len(result.Headers) > 32 {
		return apigenapi.ManagedDataS3MultipartSignedPartResponse{}, ErrNotFound
	}
	headers := make([]apigenapi.ManagedDataHTTPHeader, len(result.Headers))
	for i, header := range result.Headers {
		if header.Name == "" || len(header.Name) > 256 || header.Value == "" || len(header.Value) > 8192 || strings.ContainsAny(header.Name+header.Value, "\x00\r\n") {
			return apigenapi.ManagedDataS3MultipartSignedPartResponse{}, errors.New("invalid signed part headers")
		}
		headers[i] = apigenapi.ManagedDataHTTPHeader{Name: header.Name, Value: header.Value}
	}
	return apigenapi.ManagedDataS3MultipartSignedPartResponse{PartNumber: result.PartNumber, Url: result.URL, Headers: headers, ExpiresAt: result.ExpiresAt}, nil
}

func uploadFile(upload control.UploadResult, path string) (control.UploadFile, bool) {
	for _, file := range upload.Files {
		if file.File.Path == path {
			return file, true
		}
	}
	return control.UploadFile{}, false
}

func fileToWire(file manageddata.File) (apigenapi.ManagedDataFileMetadata, error) {
	if file.Size < 0 || file.Path == "" || len(file.Path) > 1024 || !digestPattern.MatchString(file.SHA256) {
		return apigenapi.ManagedDataFileMetadata{}, errors.New("invalid managed-data file metadata")
	}
	return apigenapi.ManagedDataFileMetadata{Path: file.Path, Size: file.Size, Sha256: file.SHA256}, nil
}

func pageSlice[T any](items []T, limitValue *int32, tokenValue *string, scope string, key func(T) string) ([]T, *string, error) {
	limit := defaultPageLimit
	if limitValue != nil {
		if *limitValue < 1 || *limitValue > maxPageLimit {
			return nil, nil, ErrInvalid
		}
		limit = int(*limitValue)
	}
	cursorKey, err := decodePageToken(valueOrEmpty(tokenValue), scope)
	if err != nil {
		return nil, nil, err
	}
	start := 0
	if cursorKey != "" {
		start = -1
		for index, item := range items {
			if key(item) == cursorKey {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, nil, ErrInvalid
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	var next *string
	if end < len(items) {
		next = stringPointer(encodePageToken(scope, key(items[end-1])))
	}
	return append(make([]T, 0, end-start), items[start:end]...), next, nil
}

type pageToken struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
}

func encodePageToken(scope, key string) string {
	value, _ := json.Marshal(pageToken{Scope: scope, Key: key})
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodePageToken(value, scope string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 2048 {
		return "", ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", ErrInvalid
	}
	var token pageToken
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil || token.Scope != scope || token.Key == "" {
		return "", ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", ErrInvalid
	}
	return token.Key, nil
}

func validScope(project, connection string) bool {
	return validProjectResourceID(project) && validProjectResourceID(connection)
}

func validScopeID(value string) bool {
	return scopeIDPattern.MatchString(value)
}

func validProjectResourceID(value string) bool {
	_, err := projectgraph.NewResourceID(value)
	return err == nil
}

func validUploadScope(project, connection, uploadSession string) bool {
	return validScope(project, connection) && validResourceID(uploadSession, 160)
}

func validMultipartMutation(project, connection, uploadSession, multipartUpload, key string) bool {
	return validUploadScope(project, connection, uploadSession) && validResourceID(multipartUpload, 160) && validIdempotencyKey(key)
}

func validIdempotencyKey(value string) bool {
	return validResourceID(value, 255)
}

func validResourceID(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func checkedInt32(value int64) (int32, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, fmt.Errorf("managed-data integer %s is outside the generated wire range", strconv.FormatInt(value, 10))
	}
	return int32(value), nil
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func valueOrEmpty[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
