package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	platformdb "github.com/flidai/leapview/internal/manageddata/internal/db"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
)

type Repository struct {
	db       *sql.DB
	q        *platformdb.Queries
	workflow jobplatform.WorkflowRecorder
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db, q: platformdb.New(db)} }
func NewRepositoryWithWorkflow(db *sql.DB, workflow jobplatform.WorkflowRecorder) *Repository {
	return &Repository{db: db, q: platformdb.New(db), workflow: workflow}
}

func (r *Repository) CreateCollection(ctx context.Context, input manageddata.CreateCollectionInput) (manageddata.Collection, error) {
	if input.ID.String() != strings.TrimSpace(input.ID.String()) || input.ProjectID.String() != strings.TrimSpace(input.ProjectID.String()) || input.ConnectionID.String() != strings.TrimSpace(input.ConnectionID.String()) {
		return manageddata.Collection{}, fmt.Errorf("managed data collection graph identities must be canonical")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.ID != "" {
		if err := manageddata.ValidateCollectionID(input.ID.String()); err != nil {
			return manageddata.Collection{}, err
		}
	}
	if !input.ProjectID.Valid() {
		return manageddata.Collection{}, fmt.Errorf("project id is invalid")
	}
	if !input.ConnectionID.Valid() {
		return manageddata.Collection{}, fmt.Errorf("connection id is invalid")
	}
	if input.Name == "" {
		input.Name = input.ConnectionID.String()
	}
	if existing, err := r.CollectionByProjectConnection(ctx, input.ProjectID, input.ConnectionID); err == nil {
		return idempotentCollection(existing, input)
	} else if !errors.Is(err, manageddata.ErrNotFound) {
		return manageddata.Collection{}, err
	}
	var err error
	if input.ID == "" {
		var generated string
		generated, err = newID("collection")
		input.ID = projectgraph.ResourceID(generated)
		if err != nil {
			return manageddata.Collection{}, err
		}
	}
	err = r.q.CreateManagedDataCollection(ctx, platformdb.CreateManagedDataCollectionParams{
		ID: input.ID.String(), ProjectID: input.ProjectID.String(), ConnectionID: input.ConnectionID.String(),
		Name: input.Name, Description: strings.TrimSpace(input.Description), CreatedBy: strings.TrimSpace(input.CreatedBy),
	})
	if err != nil {
		if existing, lookupErr := r.CollectionByProjectConnection(ctx, input.ProjectID, input.ConnectionID); lookupErr == nil {
			return idempotentCollection(existing, input)
		}
		return manageddata.Collection{}, mapError(err)
	}
	return r.CollectionByID(ctx, input.ID)
}

func (r *Repository) CollectionByProjectConnection(ctx context.Context, projectID, connectionID projectgraph.ResourceID) (manageddata.Collection, error) {
	row, err := r.q.GetManagedDataCollectionByProjectConnection(ctx, platformdb.GetManagedDataCollectionByProjectConnectionParams{
		ProjectID: projectID.String(), ConnectionID: connectionID.String(),
	})
	if err != nil {
		return manageddata.Collection{}, mapError(err)
	}
	return mapCollection(row), nil
}

func (r *Repository) CollectionByID(ctx context.Context, id projectgraph.ResourceID) (manageddata.Collection, error) {
	row, err := r.q.GetManagedDataCollection(ctx, id.String())
	if err != nil {
		return manageddata.Collection{}, mapError(err)
	}
	return mapCollection(row), nil
}

func (r *Repository) ListCollections(ctx context.Context, includeArchived bool) ([]manageddata.Collection, error) {
	var rows []platformdb.ManagedDataCollection
	var err error
	if includeArchived {
		rows, err = r.q.ListAllManagedDataCollections(ctx)
	} else {
		rows, err = r.q.ListActiveManagedDataCollections(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.Collection, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapCollection(row))
	}
	return out, nil
}

func (r *Repository) ArchiveCollection(ctx context.Context, id projectgraph.ResourceID) error {
	result, err := r.q.ArchiveManagedDataCollection(ctx, id.String())
	return expectOne(result, err, "collection is not active")
}

func (r *Repository) CreateUploadSession(ctx context.Context, input manageddata.CreateUploadSessionInput) (manageddata.UploadSession, error) {
	if input.CollectionID.String() != strings.TrimSpace(input.CollectionID.String()) || input.BaseRevisionID != manageddata.RevisionID(strings.TrimSpace(string(input.BaseRevisionID))) {
		return manageddata.UploadSession{}, fmt.Errorf("upload graph and revision identities must be canonical")
	}
	input.StorageBackend = strings.TrimSpace(input.StorageBackend)
	input.StagingPrefix = strings.TrimSpace(input.StagingPrefix)
	if !input.CollectionID.Valid() {
		return manageddata.UploadSession{}, fmt.Errorf("collection id is required")
	}
	if input.StorageBackend == "" {
		return manageddata.UploadSession{}, fmt.Errorf("storage backend is required")
	}
	if input.StagingPrefix == "" {
		return manageddata.UploadSession{}, fmt.Errorf("staging prefix is required")
	}
	if input.ExpiresAt.IsZero() || !input.ExpiresAt.After(time.Now()) {
		return manageddata.UploadSession{}, fmt.Errorf("upload session expiry must be in the future")
	}
	manifestJSON, err := input.Manifest.CanonicalJSON()
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	fileCount, sizeBytes := manifestTotals(input.Manifest)
	id := input.ID.String()
	if id == "" {
		id, err = newID("upload")
		if err != nil {
			return manageddata.UploadSession{}, err
		}
	}
	err = r.q.CreateManagedDataUploadSession(ctx, platformdb.CreateManagedDataUploadSessionParams{
		ID: id, CollectionID: input.CollectionID.String(), BaseRevisionID: nullable(string(input.BaseRevisionID)), ManifestJson: string(manifestJSON),
		ExpectedFileCount: fileCount, ExpectedSizeBytes: sizeBytes, StorageBackend: input.StorageBackend,
		StagingPrefix: input.StagingPrefix, CreatedBy: strings.TrimSpace(input.CreatedBy), ExpiresAt: timestamp(input.ExpiresAt),
	})
	if err != nil {
		return manageddata.UploadSession{}, mapError(err)
	}
	return r.UploadSessionByID(ctx, manageddata.UploadID(id))
}

func (r *Repository) UploadSessionByID(ctx context.Context, id manageddata.UploadID) (manageddata.UploadSession, error) {
	row, err := r.q.GetManagedDataUploadSession(ctx, id.String())
	if err != nil {
		return manageddata.UploadSession{}, mapError(err)
	}
	return mapUploadSession(row), nil
}

func (r *Repository) ListUploadSessions(ctx context.Context, collectionID projectgraph.ResourceID) ([]manageddata.UploadSession, error) {
	rows, err := r.q.ListManagedDataUploadSessions(ctx, collectionID.String())
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]manageddata.UploadSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapUploadSession(row))
	}
	return out, nil
}

func (r *Repository) ListUploadSessionsForCleanup(ctx context.Context, limit int64) ([]manageddata.UploadSession, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.q.ListManagedDataUploadSessionsForCleanup(ctx, limit)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]manageddata.UploadSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapUploadSession(row))
	}
	return out, nil
}

func (r *Repository) MarkUploadCleanupComplete(ctx context.Context, id manageddata.UploadID) error {
	_, err := r.q.MarkManagedDataUploadCleanupComplete(ctx, id.String())
	return err
}

func (r *Repository) UpdateUploadProgress(ctx context.Context, id manageddata.UploadID, progress manageddata.UploadProgress) error {
	if progress.UploadedFileCount < 0 || progress.UploadedSizeBytes < 0 {
		return fmt.Errorf("upload progress cannot be negative")
	}
	result, err := r.q.UpdateManagedDataUploadProgress(ctx, platformdb.UpdateManagedDataUploadProgressParams{
		UploadedFileCount: progress.UploadedFileCount, UploadedSizeBytes: progress.UploadedSizeBytes, ID: id.String(),
		ExpectedFileCount: progress.UploadedFileCount, ExpectedSizeBytes: progress.UploadedSizeBytes,
	})
	return expectOne(result, err, "upload session is not open or progress exceeds its manifest")
}

func (r *Repository) BeginUploadFinalization(ctx context.Context, id manageddata.UploadID, workflow jobs.WorkflowIntent) (manageddata.UploadSession, error) {
	idString := id.String()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	result, err := q.BeginManagedDataUploadFinalization(ctx, idString)
	if err := expectOne(result, err, "upload session changed while beginning finalization"); err != nil {
		row, getErr := q.GetManagedDataUploadSession(ctx, idString)
		if getErr != nil || mapUploadSession(row).Status != manageddata.UploadStatusCommitting {
			return manageddata.UploadSession{}, err
		}
	}
	if workflow.Job.ID != "" {
		if r.workflow == nil {
			return manageddata.UploadSession{}, fmt.Errorf("managed-data workflow recorder is required")
		}
		if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return manageddata.UploadSession{}, err
		}
	}
	row, err := q.GetManagedDataUploadSession(ctx, idString)
	if err != nil {
		return manageddata.UploadSession{}, mapError(err)
	}
	if err := tx.Commit(); err != nil {
		return manageddata.UploadSession{}, mapError(err)
	}
	return mapUploadSession(row), nil
}

func (r *Repository) FailUploadFinalization(ctx context.Context, id manageddata.UploadID, message string) (manageddata.UploadSession, error) {
	idString, message := id.String(), strings.TrimSpace(message)
	if message == "" {
		message = "upload finalization failed"
	}
	result, err := r.q.FailManagedDataUploadFinalization(ctx, platformdb.FailManagedDataUploadFinalizationParams{Error: message, ID: idString})
	if err := expectOne(result, err, "upload session changed while failing finalization"); err != nil {
		return manageddata.UploadSession{}, err
	}
	row, err := r.q.GetManagedDataUploadSession(ctx, idString)
	return mapUploadSession(row), mapError(err)
}

func (r *Repository) AbortUploadSession(ctx context.Context, id manageddata.UploadID) error {
	result, err := r.q.AbortManagedDataUploadSession(ctx, id.String())
	return expectOne(result, err, "upload session is not open")
}

func (r *Repository) AbortUploadSessionWithWorkflow(ctx context.Context, id manageddata.UploadID, workflow jobs.WorkflowIntent) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	result, err := q.AbortManagedDataUploadSession(ctx, id.String())
	if err := expectOne(result, err, "upload session is not open"); err != nil {
		return err
	}
	if r.workflow == nil {
		return fmt.Errorf("managed-data workflow recorder is required")
	}
	if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ExpireUploadSessions(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	result, err := r.q.ExpireManagedDataUploadSessions(ctx, timestamp(now))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) CreateS3MultipartUpload(ctx context.Context, input manageddata.CreateS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	if input.ID.String() != strings.TrimSpace(input.ID.String()) || input.UploadSessionID.String() != strings.TrimSpace(input.UploadSessionID.String()) {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart operational identities must be canonical")
	}
	inputID, inputSessionID := input.ID.String(), input.UploadSessionID.String()
	input.LogicalPath = strings.TrimSpace(input.LogicalPath)
	input.SHA256 = strings.TrimSpace(input.SHA256)
	input.IdempotencyIdentity = strings.TrimSpace(input.IdempotencyIdentity)
	if inputID == "" || inputSessionID == "" || input.LogicalPath == "" {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart upload id, session id, and logical path are required")
	}
	if err := validateHexIdentity("multipart SHA-256", input.SHA256); err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if input.SizeBytes < 0 {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart size cannot be negative")
	}
	if err := validateHexIdentity("multipart idempotency identity", input.IdempotencyIdentity); err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	err := r.q.CreateManagedDataS3MultipartUpload(ctx, platformdb.CreateManagedDataS3MultipartUploadParams{
		ID: inputID, UploadSessionID: inputSessionID, LogicalPath: input.LogicalPath, Sha256: input.SHA256,
		SizeBytes: input.SizeBytes, IdempotencyIdentity: input.IdempotencyIdentity,
	})
	if err == nil {
		return r.S3MultipartUploadByID(ctx, input.ID)
	}
	if row, lookupErr := r.q.GetManagedDataS3MultipartUpload(ctx, inputID); lookupErr == nil {
		return idempotentS3MultipartUpload(mapS3MultipartUpload(row), input)
	}
	if _, lookupErr := r.q.GetManagedDataS3MultipartUploadByIdentity(ctx, platformdb.GetManagedDataS3MultipartUploadByIdentityParams{
		UploadSessionID: inputSessionID, IdempotencyIdentity: input.IdempotencyIdentity,
	}); lookupErr == nil {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("%w: multipart idempotency identity is already in use", manageddata.ErrConflict)
	}
	return manageddata.S3MultipartUpload{}, mapError(err)
}

func (r *Repository) S3MultipartUploadByID(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	row, err := r.q.GetManagedDataS3MultipartUpload(ctx, id.String())
	if err != nil {
		return manageddata.S3MultipartUpload{}, mapError(err)
	}
	return mapS3MultipartUpload(row), nil
}

func (r *Repository) InitializeS3MultipartUpload(ctx context.Context, input manageddata.InitializeS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	if input.ID.String() != strings.TrimSpace(input.ID.String()) {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart upload id must be canonical")
	}
	inputID := input.ID.String()
	input.ObjectKey = strings.TrimSpace(input.ObjectKey)
	input.ProviderUploadID = strings.TrimSpace(input.ProviderUploadID)
	if inputID == "" || input.ObjectKey == "" || !safeMetadata(input.ObjectKey, 2048) {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart upload id and safe object key are required")
	}
	if input.Existing && input.ProviderUploadID != "" || !input.Existing && (input.ProviderUploadID == "" || !safeMetadata(input.ProviderUploadID, 2048)) {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart provider upload id does not match existing state")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	row, err := q.GetManagedDataS3MultipartUpload(ctx, inputID)
	if err != nil {
		return manageddata.S3MultipartUpload{}, mapError(err)
	}
	current := mapS3MultipartUpload(row)
	if current.Status != manageddata.S3MultipartStatusCreating {
		if sameS3MultipartInitialization(current, input) {
			return current, nil
		}
		return manageddata.S3MultipartUpload{}, fmt.Errorf("%w: multipart upload is %s", manageddata.ErrConflict, current.Status)
	}
	var result sql.Result
	if input.Existing {
		result, err = q.InitializeExistingManagedDataS3MultipartUpload(ctx, platformdb.InitializeExistingManagedDataS3MultipartUploadParams{ObjectKey: input.ObjectKey, ID: inputID})
	} else {
		result, err = q.InitializeManagedDataS3MultipartUpload(ctx, platformdb.InitializeManagedDataS3MultipartUploadParams{ObjectKey: input.ObjectKey, ProviderUploadID: input.ProviderUploadID, ID: inputID})
	}
	if err := expectOne(result, err, "multipart upload changed while initializing"); err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	row, err = q.GetManagedDataS3MultipartUpload(ctx, inputID)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if err := tx.Commit(); err != nil {
		return manageddata.S3MultipartUpload{}, mapError(err)
	}
	return mapS3MultipartUpload(row), nil
}

func (r *Repository) ReserveS3MultipartPart(ctx context.Context, part manageddata.S3MultipartPart) (manageddata.S3MultipartPart, error) {
	if part.MultipartUploadID.String() != strings.TrimSpace(part.MultipartUploadID.String()) {
		return manageddata.S3MultipartPart{}, fmt.Errorf("multipart upload id must be canonical")
	}
	part.SHA256 = strings.TrimSpace(part.SHA256)
	if part.MultipartUploadID == "" || part.PartNumber < 1 || part.PartNumber > 10_000 || part.SizeBytes <= 0 {
		return manageddata.S3MultipartPart{}, fmt.Errorf("invalid multipart part reservation")
	}
	if part.SHA256 != "" {
		if err := validateHexIdentity("multipart part SHA-256", part.SHA256); err != nil {
			return manageddata.S3MultipartPart{}, err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	uploadRow, err := q.GetManagedDataS3MultipartUpload(ctx, part.MultipartUploadID.String())
	if err != nil {
		return manageddata.S3MultipartPart{}, mapError(err)
	}
	if uploadRow.Status != string(manageddata.S3MultipartStatusOpen) {
		return manageddata.S3MultipartPart{}, fmt.Errorf("%w: multipart upload is %s", manageddata.ErrConflict, uploadRow.Status)
	}
	existing, err := q.GetManagedDataS3MultipartPart(ctx, platformdb.GetManagedDataS3MultipartPartParams{
		MultipartUploadID: part.MultipartUploadID.String(), PartNumber: int64(part.PartNumber),
	})
	if err == nil {
		mapped := mapS3MultipartPart(existing)
		if mapped == part {
			return mapped, nil
		}
		return manageddata.S3MultipartPart{}, fmt.Errorf("%w: multipart part number was reused with different metadata", manageddata.ErrConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return manageddata.S3MultipartPart{}, err
	}
	total, err := q.SumManagedDataS3MultipartPartSizes(ctx, part.MultipartUploadID.String())
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	if total > uploadRow.SizeBytes-part.SizeBytes {
		return manageddata.S3MultipartPart{}, fmt.Errorf("%w: multipart part reservations exceed blob size", manageddata.ErrConflict)
	}
	if err := q.CreateManagedDataS3MultipartPart(ctx, platformdb.CreateManagedDataS3MultipartPartParams{
		MultipartUploadID: part.MultipartUploadID.String(), PartNumber: int64(part.PartNumber), SizeBytes: part.SizeBytes, Sha256: part.SHA256,
	}); err != nil {
		return manageddata.S3MultipartPart{}, mapError(err)
	}
	if err := tx.Commit(); err != nil {
		return manageddata.S3MultipartPart{}, mapError(err)
	}
	return part, nil
}

func (r *Repository) ListS3MultipartParts(ctx context.Context, id manageddata.MultipartUploadID) ([]manageddata.S3MultipartPart, error) {
	rows, err := r.q.ListManagedDataS3MultipartParts(ctx, id.String())
	if err != nil {
		return nil, err
	}
	parts := make([]manageddata.S3MultipartPart, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, mapS3MultipartPart(row))
	}
	return parts, nil
}

func (r *Repository) BeginS3MultipartCompletion(ctx context.Context, input manageddata.BeginS3MultipartCompletionInput) (manageddata.S3MultipartCompletion, error) {
	if input.ID.String() != strings.TrimSpace(input.ID.String()) {
		return manageddata.S3MultipartCompletion{}, fmt.Errorf("multipart upload id must be canonical")
	}
	inputID := input.ID.String()
	input.IdempotencyIdentity = strings.TrimSpace(input.IdempotencyIdentity)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	if inputID == "" {
		return manageddata.S3MultipartCompletion{}, fmt.Errorf("multipart upload id is required")
	}
	if err := validateHexIdentity("completion idempotency identity", input.IdempotencyIdentity); err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	if err := validateHexIdentity("completion request hash", input.RequestHash); err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	row, err := q.GetManagedDataS3MultipartUpload(ctx, inputID)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, mapError(err)
	}
	upload := mapS3MultipartUpload(row)
	execute := true
	switch upload.Status {
	case manageddata.S3MultipartStatusOpen:
		result, updateErr := q.BeginManagedDataS3MultipartCompletion(ctx, platformdb.BeginManagedDataS3MultipartCompletionParams{
			CompletionIdentity: input.IdempotencyIdentity, CompletionRequestHash: input.RequestHash, ID: inputID,
		})
		if err := expectOne(result, updateErr, "multipart upload changed while beginning completion"); err != nil {
			return manageddata.S3MultipartCompletion{}, err
		}
		row, err = q.GetManagedDataS3MultipartUpload(ctx, inputID)
		if err != nil {
			return manageddata.S3MultipartCompletion{}, err
		}
		upload = mapS3MultipartUpload(row)
	case manageddata.S3MultipartStatusCompleting:
		if upload.CompletionIdentity != input.IdempotencyIdentity || upload.CompletionRequestHash != input.RequestHash {
			return manageddata.S3MultipartCompletion{}, fmt.Errorf("%w: multipart completion identity conflicts", manageddata.ErrConflict)
		}
	case manageddata.S3MultipartStatusCompleted:
		if upload.CompletionIdentity != input.IdempotencyIdentity || upload.CompletionRequestHash != input.RequestHash {
			return manageddata.S3MultipartCompletion{}, fmt.Errorf("%w: multipart completion identity conflicts", manageddata.ErrConflict)
		}
		execute = false
	default:
		return manageddata.S3MultipartCompletion{}, fmt.Errorf("%w: multipart upload is %s", manageddata.ErrConflict, upload.Status)
	}
	partRows, err := q.ListManagedDataS3MultipartParts(ctx, inputID)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	parts := make([]manageddata.S3MultipartPart, 0, len(partRows))
	for _, part := range partRows {
		parts = append(parts, mapS3MultipartPart(part))
	}
	if err := tx.Commit(); err != nil {
		return manageddata.S3MultipartCompletion{}, mapError(err)
	}
	return manageddata.S3MultipartCompletion{Upload: upload, Parts: parts, Execute: execute}, nil
}

func (r *Repository) FinishS3MultipartCompletion(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	return r.finishS3Multipart(ctx, id.String(), manageddata.S3MultipartStatusCompleting, manageddata.S3MultipartStatusCompleted)
}

func (r *Repository) BeginS3MultipartAbort(ctx context.Context, input manageddata.BeginS3MultipartAbortInput) (manageddata.S3MultipartAbort, error) {
	if input.ID.String() != strings.TrimSpace(input.ID.String()) {
		return manageddata.S3MultipartAbort{}, fmt.Errorf("multipart upload id must be canonical")
	}
	inputID := input.ID.String()
	input.IdempotencyIdentity = strings.TrimSpace(input.IdempotencyIdentity)
	if inputID == "" {
		return manageddata.S3MultipartAbort{}, fmt.Errorf("multipart upload id is required")
	}
	if err := validateHexIdentity("abort idempotency identity", input.IdempotencyIdentity); err != nil {
		return manageddata.S3MultipartAbort{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.S3MultipartAbort{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	row, err := q.GetManagedDataS3MultipartUpload(ctx, inputID)
	if err != nil {
		return manageddata.S3MultipartAbort{}, mapError(err)
	}
	upload := mapS3MultipartUpload(row)
	execute := true
	switch upload.Status {
	case manageddata.S3MultipartStatusCreating, manageddata.S3MultipartStatusOpen, manageddata.S3MultipartStatusFailed:
		result, updateErr := q.BeginManagedDataS3MultipartAbort(ctx, platformdb.BeginManagedDataS3MultipartAbortParams{AbortIdentity: input.IdempotencyIdentity, ID: inputID})
		if err := expectOne(result, updateErr, "multipart upload changed while beginning abort"); err != nil {
			return manageddata.S3MultipartAbort{}, err
		}
		row, err = q.GetManagedDataS3MultipartUpload(ctx, inputID)
		if err != nil {
			return manageddata.S3MultipartAbort{}, err
		}
		upload = mapS3MultipartUpload(row)
	case manageddata.S3MultipartStatusAborting:
		if upload.AbortIdentity != input.IdempotencyIdentity {
			return manageddata.S3MultipartAbort{}, fmt.Errorf("%w: multipart abort identity conflicts", manageddata.ErrConflict)
		}
	case manageddata.S3MultipartStatusAborted:
		if upload.AbortIdentity != input.IdempotencyIdentity {
			return manageddata.S3MultipartAbort{}, fmt.Errorf("%w: multipart abort identity conflicts", manageddata.ErrConflict)
		}
		execute = false
	default:
		return manageddata.S3MultipartAbort{}, fmt.Errorf("%w: multipart upload is %s", manageddata.ErrConflict, upload.Status)
	}
	if err := tx.Commit(); err != nil {
		return manageddata.S3MultipartAbort{}, mapError(err)
	}
	return manageddata.S3MultipartAbort{Upload: upload, Execute: execute}, nil
}

func (r *Repository) FinishS3MultipartAbort(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	return r.finishS3Multipart(ctx, id.String(), manageddata.S3MultipartStatusAborting, manageddata.S3MultipartStatusAborted)
}

func (r *Repository) FailS3MultipartUpload(ctx context.Context, id manageddata.MultipartUploadID, message string) (manageddata.S3MultipartUpload, error) {
	idString := id.String()
	message = strings.TrimSpace(message)
	if idString == "" || message == "" || !safeMetadata(message, 2048) {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart upload id and safe terminal error are required")
	}
	current, err := r.S3MultipartUploadByID(ctx, id)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if current.Status == manageddata.S3MultipartStatusFailed {
		if current.Error == message {
			return current, nil
		}
		return manageddata.S3MultipartUpload{}, fmt.Errorf("%w: multipart terminal error conflicts", manageddata.ErrConflict)
	}
	result, err := r.q.FailManagedDataS3MultipartUpload(ctx, platformdb.FailManagedDataS3MultipartUploadParams{Error: message, ID: idString})
	if err := expectOne(result, err, "multipart upload cannot fail from its current state"); err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	return r.S3MultipartUploadByID(ctx, id)
}

func (r *Repository) ListRecoverableS3MultipartUploads(ctx context.Context, before time.Time, limit int64) ([]manageddata.S3MultipartUpload, error) {
	if before.IsZero() {
		before = time.Now()
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("multipart recovery limit must be between 1 and 1000")
	}
	rows, err := r.q.ListRecoverableManagedDataS3MultipartUploads(ctx, platformdb.ListRecoverableManagedDataS3MultipartUploadsParams{
		UpdatedCutoff: timestamp(before), ExpiryCutoff: timestamp(before), RowLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	uploads := make([]manageddata.S3MultipartUpload, 0, len(rows))
	for _, row := range rows {
		uploads = append(uploads, mapS3MultipartUpload(row))
	}
	return uploads, nil
}

func (r *Repository) ListS3MultipartProviderIDsByDigest(ctx context.Context, digest string) ([]string, error) {
	return r.q.ListManagedDataS3MultipartProviderIDsByDigest(ctx, strings.TrimSpace(digest))
}

func (r *Repository) ListCreatingS3MultipartIDsByDigest(ctx context.Context, digest string) ([]string, error) {
	return r.q.ListCreatingManagedDataS3MultipartIDsByDigest(ctx, strings.TrimSpace(digest))
}

func (r *Repository) ClaimS3MultipartDigest(ctx context.Context, digest, owner string, lease time.Time) (int64, bool, error) {
	generation, err := r.q.ClaimManagedDataMultipartDigest(ctx, platformdb.ClaimManagedDataMultipartDigestParams{
		Sha256: strings.TrimSpace(digest), OwnerID: strings.TrimSpace(owner), LeaseUntil: timestamp(lease),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return generation, err == nil, err
}

func (r *Repository) RenewS3MultipartDigest(ctx context.Context, digest, owner string, generation int64, lease time.Time) (bool, error) {
	count, err := r.q.RenewManagedDataMultipartDigest(ctx, platformdb.RenewManagedDataMultipartDigestParams{
		LeaseUntil: timestamp(lease), Sha256: strings.TrimSpace(digest),
		OwnerID: strings.TrimSpace(owner), LeaseGeneration: generation,
	})
	return count == 1, err
}

func (r *Repository) ReleaseS3MultipartDigest(ctx context.Context, digest, owner string, generation int64) error {
	_, err := r.q.ReleaseManagedDataMultipartDigest(ctx, platformdb.ReleaseManagedDataMultipartDigestParams{
		Sha256: strings.TrimSpace(digest), OwnerID: strings.TrimSpace(owner), LeaseGeneration: generation,
	})
	return err
}

func (r *Repository) finishS3Multipart(ctx context.Context, id string, from, to manageddata.S3MultipartStatus) (manageddata.S3MultipartUpload, error) {
	if id == "" {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("multipart upload id is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	row, err := q.GetManagedDataS3MultipartUpload(ctx, id)
	if err != nil {
		return manageddata.S3MultipartUpload{}, mapError(err)
	}
	current := mapS3MultipartUpload(row)
	if current.Status == to {
		return current, nil
	}
	if current.Status != from {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("%w: multipart upload is %s", manageddata.ErrConflict, current.Status)
	}
	var result sql.Result
	if to == manageddata.S3MultipartStatusCompleted {
		result, err = q.FinishManagedDataS3MultipartCompletion(ctx, id)
	} else {
		result, err = q.FinishManagedDataS3MultipartAbort(ctx, id)
	}
	if err := expectOne(result, err, "multipart upload changed while finishing transition"); err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	row, err = q.GetManagedDataS3MultipartUpload(ctx, id)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if err := tx.Commit(); err != nil {
		return manageddata.S3MultipartUpload{}, mapError(err)
	}
	return mapS3MultipartUpload(row), nil
}

func (r *Repository) CompleteUpload(ctx context.Context, input manageddata.CompleteUploadInput) (manageddata.Revision, error) {
	if input.SessionID.String() != strings.TrimSpace(input.SessionID.String()) || input.RevisionID.String() != strings.TrimSpace(input.RevisionID.String()) {
		return manageddata.Revision{}, fmt.Errorf("upload and revision identities must be canonical")
	}
	sessionID := input.SessionID.String()
	if input.SessionID == "" {
		return manageddata.Revision{}, fmt.Errorf("upload session id is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return manageddata.Revision{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	result, err := q.MarkManagedDataUploadCommitting(ctx, sessionID)
	if err != nil {
		return manageddata.Revision{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return manageddata.Revision{}, err
	}
	if affected != 1 {
		session, getErr := q.GetManagedDataUploadSession(ctx, sessionID)
		if getErr != nil {
			return manageddata.Revision{}, mapError(getErr)
		}
		if session.Status == string(manageddata.UploadStatusComplete) && session.RevisionID.Valid {
			row, getErr := q.GetManagedDataRevision(ctx, session.RevisionID.String)
			return mapRevision(row), mapError(getErr)
		}
		return manageddata.Revision{}, fmt.Errorf("%w: upload session is %s or expired", manageddata.ErrConflict, session.Status)
	}
	session, err := q.GetManagedDataUploadSession(ctx, sessionID)
	if err != nil {
		return manageddata.Revision{}, err
	}
	manifest, err := decodeManifest(session.ManifestJson)
	if err != nil {
		return manageddata.Revision{}, err
	}
	if err := validateStoredFiles(manifest, input.Files); err != nil {
		return manageddata.Revision{}, err
	}
	sequence, err := q.NextManagedDataRevisionSequence(ctx, session.CollectionID)
	if err != nil {
		return manageddata.Revision{}, err
	}
	revisionID := input.RevisionID.String()
	if revisionID == "" {
		revisionID, err = newID("revision")
		if err != nil {
			return manageddata.Revision{}, err
		}
	}
	if err := q.CreateReadyManagedDataRevision(ctx, platformdb.CreateReadyManagedDataRevisionParams{
		ID: revisionID, CollectionID: session.CollectionID, Sequence: sequence, Digest: manifest.RevisionID(),
		ManifestJson: session.ManifestJson, FileCount: session.ExpectedFileCount, SizeBytes: session.ExpectedSizeBytes, CreatedBy: session.CreatedBy,
	}); err != nil {
		return manageddata.Revision{}, mapError(err)
	}
	for _, file := range sortedStoredFiles(input.Files) {
		if err := q.CreateManagedDataRevisionFile(ctx, platformdb.CreateManagedDataRevisionFileParams{
			RevisionID: revisionID, LogicalPath: file.Path, SizeBytes: file.Size, Sha256: file.SHA256,
			StorageKey: file.StorageKey, MediaType: strings.TrimSpace(file.MediaType), Etag: strings.TrimSpace(file.ETag),
		}); err != nil {
			return manageddata.Revision{}, mapError(err)
		}
	}
	result, err = q.CompleteManagedDataUploadSession(ctx, platformdb.CompleteManagedDataUploadSessionParams{RevisionID: nullable(revisionID), ID: sessionID})
	if err != nil {
		return manageddata.Revision{}, err
	}
	if err := requireOne(result, "upload session changed while committing"); err != nil {
		return manageddata.Revision{}, err
	}
	if err := tx.Commit(); err != nil {
		return manageddata.Revision{}, mapError(err)
	}
	return r.RevisionByID(ctx, manageddata.RevisionID(revisionID))
}

func (r *Repository) RevisionByID(ctx context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	row, err := r.q.GetManagedDataRevision(ctx, id.String())
	if err != nil {
		return manageddata.Revision{}, mapError(err)
	}
	return mapRevision(row), nil
}

func (r *Repository) ListRevisions(ctx context.Context, collectionID projectgraph.ResourceID) ([]manageddata.Revision, error) {
	rows, err := r.q.ListManagedDataRevisions(ctx, collectionID.String())
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.Revision, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRevision(row))
	}
	return out, nil
}

func (r *Repository) UploadSessionIDByRevisionID(ctx context.Context, revisionID manageddata.RevisionID) (manageddata.UploadID, error) {
	id, err := r.q.GetManagedDataUploadSessionIDByRevision(ctx, nullable(revisionID.String()))
	if err != nil {
		return "", mapError(err)
	}
	return manageddata.UploadID(id), nil
}

func (r *Repository) ListRevisionFiles(ctx context.Context, revisionID manageddata.RevisionID) ([]manageddata.RevisionFile, error) {
	rows, err := r.q.ListManagedDataRevisionFiles(ctx, revisionID.String())
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.RevisionFile, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRevisionFile(row))
	}
	return out, nil
}

func (r *Repository) EnvironmentPointer(ctx context.Context, collectionID projectgraph.ResourceID, environment manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	normalized, err := manageddata.NormalizeEnvironment(string(environment))
	if err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	row, err := r.q.GetManagedDataEnvironmentPointer(ctx, platformdb.GetManagedDataEnvironmentPointerParams{CollectionID: collectionID.String(), Environment: string(normalized)})
	if err != nil {
		return manageddata.EnvironmentPointer{}, mapError(err)
	}
	return mapEnvironmentPointer(row), nil
}

// ActiveEnvironmentPointer derives the public active revision from the
// canonical delivery target and its immutable serving-state binding. The
// mutable environment pointer remains a planning input and is not evidence
// that a revision is serving.
func (r *Repository) ActiveEnvironmentPointer(ctx context.Context, collectionID projectgraph.ResourceID, environment manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	normalized, err := manageddata.NormalizeEnvironment(string(environment))
	if err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	row, err := r.q.GetActiveManagedDataServingPointer(ctx, platformdb.GetActiveManagedDataServingPointerParams{
		CollectionID: collectionID.String(), Environment: string(normalized),
	})
	if err != nil {
		return manageddata.EnvironmentPointer{}, mapError(err)
	}
	return manageddata.EnvironmentPointer{
		CollectionID: projectgraph.ResourceID(row.CollectionID), Environment: manageddata.Environment(row.Environment),
		RevisionID: manageddata.RevisionID(row.RevisionID), DeploymentID: row.DeploymentID,
		Generation: row.Generation, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (r *Repository) InstallServingStateBindings(ctx context.Context, identity projectgraph.ServingIdentity, bindings []manageddata.ServingStateBinding) error {
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("serving identity is required: %w", err)
	}
	normalized := make([]manageddata.ServingStateBinding, 0, len(bindings))
	seen := map[projectgraph.ResourceID]struct{}{}
	for _, binding := range bindings {
		if !binding.CollectionID.Valid() || binding.RevisionID.String() == "" || binding.RevisionID.String() != strings.TrimSpace(binding.RevisionID.String()) {
			return fmt.Errorf("binding collection and revision ids are required")
		}
		if binding.Identity != (projectgraph.ServingIdentity{}) && binding.Identity != identity {
			return fmt.Errorf("binding serving identity does not match replacement identity")
		}
		if _, exists := seen[binding.CollectionID]; exists {
			return fmt.Errorf("duplicate binding for collection %q", binding.CollectionID)
		}
		seen[binding.CollectionID] = struct{}{}
		binding.Identity = identity
		normalized = append(normalized, binding)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].CollectionID < normalized[j].CollectionID })
	digest, err := servingBindingDigest(normalized)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	markerResult, err := q.InstallManagedDataServingStateBindingSet(ctx, platformdb.InstallManagedDataServingStateBindingSetParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
		BindingDigest: digest, BindingCount: int64(len(normalized)),
	})
	if err != nil {
		return mapError(err)
	}
	markerAffected := markerResult
	if markerAffected == 0 {
		marker, getErr := q.GetManagedDataServingStateBindingSet(ctx, platformdb.GetManagedDataServingStateBindingSetParams{
			ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
		})
		if getErr != nil {
			return mapError(getErr)
		}
		if marker.BindingDigest != digest || marker.BindingCount != int64(len(normalized)) {
			return fmt.Errorf("%w: serving binding set conflicts with immutable generation evidence", manageddata.ErrConflict)
		}
	}
	if markerAffected == 1 {
		for _, binding := range normalized {
			if err := q.InstallManagedDataServingStateBinding(ctx, platformdb.InstallManagedDataServingStateBindingParams{
				ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
				CollectionID: binding.CollectionID.String(), RevisionID: binding.RevisionID.String(),
			}); err != nil {
				return mapError(err)
			}
		}
	}
	rows, err := q.ListManagedDataServingStateBindings(ctx, platformdb.ListManagedDataServingStateBindingsParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	})
	if err != nil {
		return mapError(err)
	}
	if !servingBindingRowsMatch(rows, normalized) {
		return fmt.Errorf("%w: serving binding rows do not match immutable generation evidence", manageddata.ErrConflict)
	}
	return tx.Commit()
}

func servingBindingDigest(bindings []manageddata.ServingStateBinding) (string, error) {
	type digestEntry struct {
		CollectionID string `json:"collection_id"`
		RevisionID   string `json:"revision_id"`
	}
	entries := make([]digestEntry, 0, len(bindings))
	for _, binding := range bindings {
		entries = append(entries, digestEntry{CollectionID: binding.CollectionID.String(), RevisionID: binding.RevisionID.String()})
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal serving binding set: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func servingBindingRowsMatch(rows []platformdb.ManagedDataServingStateBinding, want []manageddata.ServingStateBinding) bool {
	if len(rows) != len(want) {
		return false
	}
	for i, row := range rows {
		if row.CollectionID != want[i].CollectionID.String() || row.RevisionID != want[i].RevisionID.String() {
			return false
		}
	}
	return true
}

func (r *Repository) ListServingStateBindings(ctx context.Context, identity projectgraph.ServingIdentity) ([]manageddata.ServingStateBinding, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("serving identity is required: %w", err)
	}
	rows, err := r.q.ListManagedDataServingStateBindings(ctx, platformdb.ListManagedDataServingStateBindingsParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID})
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.ServingStateBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, manageddata.ServingStateBinding{Identity: identity, CollectionID: projectgraph.ResourceID(row.CollectionID), RevisionID: manageddata.RevisionID(row.RevisionID), BoundAt: row.BoundAt})
	}
	return out, nil
}

func validateStoredFiles(manifest manageddata.Manifest, files []manageddata.StoredFile) error {
	if len(files) != len(manifest.Files) {
		return fmt.Errorf("stored file count %d does not match manifest count %d", len(files), len(manifest.Files))
	}
	actual := manageddata.Manifest{Files: make([]manageddata.File, 0, len(files))}
	for _, file := range files {
		if strings.TrimSpace(file.StorageKey) == "" {
			return fmt.Errorf("stored file %q has no storage key", file.Path)
		}
		actual.Files = append(actual.Files, file.File)
	}
	wantJSON, err := manifest.CanonicalJSON()
	if err != nil {
		return err
	}
	actualJSON, err := actual.CanonicalJSON()
	if err != nil {
		return err
	}
	if !bytes.Equal(wantJSON, actualJSON) {
		return fmt.Errorf("stored files do not match upload manifest")
	}
	return nil
}

func decodeManifest(value string) (manageddata.Manifest, error) {
	var manifest manageddata.Manifest
	if err := json.Unmarshal([]byte(value), &manifest); err != nil {
		return manageddata.Manifest{}, fmt.Errorf("decode upload manifest: %w", err)
	}
	if err := manifest.Validate(manageddata.Limits{}); err != nil {
		return manageddata.Manifest{}, err
	}
	return manifest, nil
}

func manifestTotals(manifest manageddata.Manifest) (int64, int64) {
	var size int64
	for _, file := range manifest.Files {
		size += file.Size
	}
	return int64(len(manifest.Files)), size
}

func sortedStoredFiles(files []manageddata.StoredFile) []manageddata.StoredFile {
	out := append([]manageddata.StoredFile(nil), files...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func mapCollection(row platformdb.ManagedDataCollection) manageddata.Collection {
	return manageddata.Collection{ID: projectgraph.ResourceID(row.ID), ProjectID: projectgraph.ResourceID(row.ProjectID), ConnectionID: projectgraph.ResourceID(row.ConnectionID), Name: row.Name, Description: row.Description, Status: manageddata.CollectionStatus(row.Status), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt.String}
}

func mapRevision(row platformdb.ManagedDataRevision) manageddata.Revision {
	return manageddata.Revision{ID: manageddata.RevisionID(row.ID), CollectionID: projectgraph.ResourceID(row.CollectionID), Sequence: row.Sequence, Digest: row.Digest, Status: manageddata.RevisionStatus(row.Status), ManifestJSON: row.ManifestJson, FileCount: row.FileCount, SizeBytes: row.SizeBytes, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ReadyAt: row.ReadyAt.String, Error: row.Error}
}

func mapRevisionFile(row platformdb.ManagedDataRevisionFile) manageddata.RevisionFile {
	return manageddata.RevisionFile{RevisionID: manageddata.RevisionID(row.RevisionID), StoredFile: manageddata.StoredFile{File: manageddata.File{Path: row.LogicalPath, Size: row.SizeBytes, SHA256: row.Sha256}, StorageKey: row.StorageKey, MediaType: row.MediaType, ETag: row.Etag}, CreatedAt: row.CreatedAt}
}

func mapUploadSession(row platformdb.ManagedDataUploadSession) manageddata.UploadSession {
	return manageddata.UploadSession{ID: manageddata.UploadID(row.ID), CollectionID: projectgraph.ResourceID(row.CollectionID), BaseRevisionID: manageddata.RevisionID(row.BaseRevisionID.String), RevisionID: manageddata.RevisionID(row.RevisionID.String), Status: manageddata.UploadStatus(row.Status), ManifestJSON: row.ManifestJson, ExpectedFileCount: row.ExpectedFileCount, ExpectedSizeBytes: row.ExpectedSizeBytes, UploadedFileCount: row.UploadedFileCount, UploadedSizeBytes: row.UploadedSizeBytes, StorageBackend: row.StorageBackend, StagingPrefix: row.StagingPrefix, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ExpiresAt: row.ExpiresAt, CompletedAt: row.CompletedAt.String, Error: row.Error}
}

func mapS3MultipartUpload(row platformdb.ManagedDataS3MultipartUpload) manageddata.S3MultipartUpload {
	return manageddata.S3MultipartUpload{
		ID: manageddata.MultipartUploadID(row.ID), UploadSessionID: manageddata.UploadID(row.UploadSessionID), LogicalPath: row.LogicalPath, SHA256: row.Sha256, SizeBytes: row.SizeBytes,
		ObjectKey: row.ObjectKey, ProviderUploadID: row.ProviderUploadID, Status: manageddata.S3MultipartStatus(row.Status),
		Existing: row.Existing == 1, IdempotencyIdentity: row.IdempotencyIdentity,
		CompletionIdentity: row.CompletionIdentity, CompletionRequestHash: row.CompletionRequestHash,
		AbortIdentity: row.AbortIdentity, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		CompletedAt: row.CompletedAt.String, AbortedAt: row.AbortedAt.String, Error: row.Error,
	}
}

func mapS3MultipartPart(row platformdb.ManagedDataS3MultipartPart) manageddata.S3MultipartPart {
	return manageddata.S3MultipartPart{MultipartUploadID: manageddata.MultipartUploadID(row.MultipartUploadID), PartNumber: int32(row.PartNumber), SizeBytes: row.SizeBytes, SHA256: row.Sha256}
}

func idempotentS3MultipartUpload(existing manageddata.S3MultipartUpload, input manageddata.CreateS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	if existing.ID != input.ID || existing.UploadSessionID != input.UploadSessionID || existing.LogicalPath != input.LogicalPath || existing.SHA256 != input.SHA256 ||
		existing.SizeBytes != input.SizeBytes || existing.IdempotencyIdentity != input.IdempotencyIdentity {
		return manageddata.S3MultipartUpload{}, fmt.Errorf("%w: multipart identity was reused with different metadata", manageddata.ErrConflict)
	}
	return existing, nil
}

func sameS3MultipartInitialization(existing manageddata.S3MultipartUpload, input manageddata.InitializeS3MultipartUploadInput) bool {
	if existing.ObjectKey != input.ObjectKey || existing.Existing != input.Existing {
		return false
	}
	if input.Existing {
		return existing.Status == manageddata.S3MultipartStatusCompleted && existing.ProviderUploadID == ""
	}
	return existing.ProviderUploadID == input.ProviderUploadID && existing.Status != manageddata.S3MultipartStatusCreating
}

func validateHexIdentity(name, value string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return fmt.Errorf("%s must be 64 lowercase hexadecimal characters", name)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be 64 lowercase hexadecimal characters", name)
	}
	return nil
}

func safeMetadata(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validateIdentityPart(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > 255 {
		return fmt.Errorf("%s exceeds 255 characters", name)
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

func idempotentCollection(existing manageddata.Collection, input manageddata.CreateCollectionInput) (manageddata.Collection, error) {
	if input.ID != "" && existing.ID != input.ID || existing.Name != input.Name || existing.Description != strings.TrimSpace(input.Description) {
		return manageddata.Collection{}, fmt.Errorf("%w: collection %q/%q already exists with different identity or metadata", manageddata.ErrConflict, input.ProjectID, input.ConnectionID)
	}
	return existing, nil
}

func mapEnvironmentPointer(row platformdb.ManagedDataEnvironmentPointer) manageddata.EnvironmentPointer {
	return manageddata.EnvironmentPointer{CollectionID: projectgraph.ResourceID(row.CollectionID), Environment: manageddata.Environment(row.Environment), RevisionID: manageddata.RevisionID(row.RevisionID), DeploymentID: row.DeploymentID, Generation: row.Generation, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt}
}

func timestamp(value time.Time) string { return value.UTC().Format("2006-01-02 15:04:05.000000000") }

func nullable(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

func expectOne(result sql.Result, err error, message string) error {
	if err != nil {
		return mapError(err)
	}
	return requireOne(result, message)
}

func requireOne(result sql.Result, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", manageddata.ErrConflict, message)
	}
	return nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return manageddata.ErrNotFound
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint") || strings.Contains(message, "foreign key constraint") || strings.Contains(message, "constraint failed") {
		return fmt.Errorf("%w: %v", manageddata.ErrConflict, err)
	}
	return err
}

var _ manageddata.Repository = (*Repository)(nil)
