// Package postgres implements the clean-slate managed-data control authority.
// PostgreSQL stores collection/revision/upload identity and serving evidence;
// bytes remain in object storage and analytical metadata remains in DuckLake.
package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata"
	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrInvalid      = errors.New("invalid managed-data PostgreSQL input")
	ErrConflict     = manageddata.ErrConflict
	ErrNotFound     = manageddata.ErrNotFound
	ErrStaleFence   = errors.New("managed-data lease fencing epoch is stale")
	ErrLeaseExpired = errors.New("managed-data lease is expired")
	ErrLeaseBusy    = errors.New("managed-data lease is owned by another worker")
)

const (
	maxManifestBytes = 1 << 20
	maxErrorBytes    = 4096
	maxLease         = 24 * time.Hour
)

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository is safe to construct over a pgx pool, connection or caller-owned
// transaction. WithTx is the preferred composition boundary for atomic work.
// WorkflowRecorder and AuditIntentRecorder are narrow app-composition ports.
// Their methods receive this capability's caller-owned pgx transaction and
// must not commit or roll it back.
type WorkflowRecorder interface {
	RecordWorkflow(context.Context, Tx, jobspkg.WorkflowIntent) error
}

type AuditIntentRecorder interface {
	RecordAuditIntent(context.Context, Tx, access.AuditIntent) error
}

type Options struct {
	Workflow WorkflowRecorder
	Audit    AuditIntentRecorder
}

type Repository struct {
	db       DBTX
	workflow WorkflowRecorder
	audit    AuditIntentRecorder
}

var _ manageddata.Repository = (*Repository)(nil)

func New(db DBTX) *Repository           { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository { return New(db) }
func NewWithOptions(db DBTX, options Options) *Repository {
	return &Repository{db: db, workflow: options.Workflow, audit: options.Audit}
}

// TransitionCapabilitiesConfigured reports whether this repository can carry
// workflow/event and Access audit side effects in the same native transaction
// as upload lifecycle mutations.
func (r *Repository) TransitionCapabilitiesConfigured() bool {
	return r != nil && r.workflow != nil && r.audit != nil
}
func (r *Repository) WithTx(tx Tx) *Repository {
	if r == nil {
		return New(tx)
	}
	return &Repository{db: tx, workflow: r.workflow, audit: r.audit}
}

// DB exposes the already-configured native PostgreSQL handle to capability
// adapters that need a caller-owned transaction (for example, the bounded
// reachability maintenance source). It does not open connections or transfer
// transaction ownership.
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func requireDB(r *Repository) (DBTX, error) {
	if r == nil || r.db == nil {
		return nil, ErrInvalid
	}
	return r.db, nil
}

func uuidID(prefix string) string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return prefix + "_" + strings.ReplaceAll(id.String(), "-", "")
}
func canonicalText(v string, max int) error {
	if v != strings.TrimSpace(v) || v == "" || len(v) > max {
		return ErrInvalid
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return ErrInvalid
		}
	}
	return nil
}
func validID(v string, name string) error {
	if err := canonicalText(v, 255); err != nil {
		return fmt.Errorf("%s: %w", name, ErrInvalid)
	}
	return nil
}
func digestBytes(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func canonicalManifest(m manageddata.Manifest) ([]byte, int64, int64, error) {
	b, err := m.CanonicalJSON()
	if err != nil {
		return nil, 0, 0, err
	}
	if len(b) > maxManifestBytes {
		return nil, 0, 0, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	var size int64
	for _, f := range m.Files {
		size += f.Size
	}
	return b, int64(len(m.Files)), size, nil
}
func parseManifest(raw []byte) (manageddata.Manifest, error) {
	var m manageddata.Manifest
	if err := strictjson.DecodeWithOptions(raw, &m, strictjson.Options{MaxBytes: maxManifestBytes, MaxDepth: 32, AllowUnknownFields: false}); err != nil {
		return m, err
	}
	if _, _, _, err := canonicalManifest(m); err != nil {
		return m, err
	}
	return m, nil
}
func digestFor(parts ...string) string { return digestBytes([]byte(strings.Join(parts, "\x00"))) }

func completionDigest(in manageddata.CompleteUploadInput) string {
	type fileIdentity struct {
		Path, SHA256, StorageKey, MediaType, ETag string
		Size                                      int64
	}
	files := append([]manageddata.StoredFile(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	entries := make([]fileIdentity, 0, len(files))
	for _, f := range files {
		entries = append(entries, fileIdentity{Path: f.Path, Size: f.Size, SHA256: f.SHA256, StorageKey: f.StorageKey, MediaType: f.MediaType, ETag: f.ETag})
	}
	b, _ := json.Marshal(struct {
		RevisionID string         `json:"revision_id"`
		Files      []fileIdentity `json:"files"`
	}{RevisionID: in.RevisionID.String(), Files: entries})
	return digestBytes(b)
}

func (r *Repository) CreateCollection(ctx context.Context, in manageddata.CreateCollectionInput) (manageddata.Collection, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Collection{}, err
	}
	if !in.ProjectID.Valid() || !in.ConnectionID.Valid() {
		return manageddata.Collection{}, ErrInvalid
	}
	if in.ID != "" {
		if err := manageddata.ValidateCollectionID(in.ID.String()); err != nil {
			return manageddata.Collection{}, err
		}
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = in.ConnectionID.String()
	}
	if err := canonicalText(in.Name, 255); err != nil || len(in.Description) > 4096 {
		return manageddata.Collection{}, ErrInvalid
	}
	in.Description = strings.TrimSpace(in.Description)
	if in.Description != strings.TrimSpace(in.Description) {
		return manageddata.Collection{}, ErrInvalid
	}
	suppliedID := in.ID != ""
	id := in.ID.String()
	if id == "" {
		id = uuidID("collection")
	}
	d := digestFor(in.ProjectID.String(), in.ConnectionID.String(), in.Name, in.Description, strings.TrimSpace(in.CreatedBy))
	err = manageddb.New(db).InsertCollection(contextOrBackground(ctx), manageddb.InsertCollectionParams{CollectionID: id, ProjectID: in.ProjectID.String(), ConnectionID: in.ConnectionID.String(), Name: in.Name, Description: in.Description, CreatedBy: strings.TrimSpace(in.CreatedBy), RequestDigest: d})
	if err != nil {
		return manageddata.Collection{}, err
	}
	c, err := r.CollectionByProjectConnection(ctx, in.ProjectID, in.ConnectionID)
	if err != nil {
		return manageddata.Collection{}, err
	}
	var storedDigest string
	if storedDigest, err = manageddb.New(db).GetCollectionRequestDigest(contextOrBackground(ctx), c.ID.String()); err != nil {
		return manageddata.Collection{}, err
	}
	if (suppliedID && c.ID.String() != id) || c.Name != in.Name || c.Description != in.Description || storedDigest != d {
		return manageddata.Collection{}, fmt.Errorf("%w: collection identity replay differs", ErrConflict)
	}
	return c, nil
}

func (r *Repository) CollectionByID(ctx context.Context, id projectgraph.ResourceID) (manageddata.Collection, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Collection{}, err
	}
	if !id.Valid() {
		return manageddata.Collection{}, ErrInvalid
	}
	row, err := manageddb.New(db).GetCollectionByID(contextOrBackground(ctx), id.String())
	if err != nil {
		return manageddata.Collection{}, scanNotFound(err)
	}
	return collectionFromValues(row.CollectionID, row.ProjectID, row.ConnectionID, row.Name, row.Description, row.Status, row.CreatedBy, row.CreatedAt, row.UpdatedAt, row.ArchivedAt), nil
}
func (r *Repository) CollectionByProjectConnection(ctx context.Context, projectID, connectionID projectgraph.ResourceID) (manageddata.Collection, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Collection{}, err
	}
	if !projectID.Valid() || !connectionID.Valid() {
		return manageddata.Collection{}, ErrInvalid
	}
	row, err := manageddb.New(db).GetCollectionByProjectConnection(contextOrBackground(ctx), manageddb.GetCollectionByProjectConnectionParams{ProjectID: projectID.String(), ConnectionID: connectionID.String()})
	if err != nil {
		return manageddata.Collection{}, scanNotFound(err)
	}
	return collectionFromValues(row.CollectionID, row.ProjectID, row.ConnectionID, row.Name, row.Description, row.Status, row.CreatedBy, row.CreatedAt, row.UpdatedAt, row.ArchivedAt), nil
}
func (r *Repository) ListCollections(ctx context.Context, includeArchived bool) ([]manageddata.Collection, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	if includeArchived {
		rows, err := manageddb.New(db).ListCollections(contextOrBackground(ctx))
		if err != nil {
			return nil, err
		}
		out := make([]manageddata.Collection, 0, len(rows))
		for _, row := range rows {
			out = append(out, collectionFromValues(row.CollectionID, row.ProjectID, row.ConnectionID, row.Name, row.Description, row.Status, row.CreatedBy, row.CreatedAt, row.UpdatedAt, row.ArchivedAt))
		}
		return out, nil
	}
	rows, err := manageddb.New(db).ListActiveCollections(contextOrBackground(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.Collection, 0, len(rows))
	for _, row := range rows {
		out = append(out, collectionFromValues(row.CollectionID, row.ProjectID, row.ConnectionID, row.Name, row.Description, row.Status, row.CreatedBy, row.CreatedAt, row.UpdatedAt, row.ArchivedAt))
	}
	return out, nil
}
func (r *Repository) ArchiveCollection(ctx context.Context, id projectgraph.ResourceID) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	if !id.Valid() {
		return ErrInvalid
	}
	tag, err := manageddb.New(db).ArchiveCollection(contextOrBackground(ctx), id.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) CreateUploadSession(ctx context.Context, in manageddata.CreateUploadSessionInput) (result manageddata.UploadSession, returnErr error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if !in.CollectionID.Valid() || in.ExpiresAt.IsZero() {
		return manageddata.UploadSession{}, ErrInvalid
	}
	b, count, size, err := canonicalManifest(in.Manifest)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	suppliedID := in.ID != ""
	id := in.ID.String()
	if !suppliedID {
		return manageddata.UploadSession{}, fmt.Errorf("upload id is required for exact replay")
	}
	if err := validID(id, "upload id"); err != nil {
		return manageddata.UploadSession{}, err
	}
	if in.BaseRevisionID != "" {
		if err := validID(in.BaseRevisionID.String(), "base revision id"); err != nil {
			return manageddata.UploadSession{}, err
		}
	}
	if err := canonicalText(strings.TrimSpace(in.StorageBackend), 255); err != nil || len(in.StagingPrefix) == 0 || len(in.StagingPrefix) > 2048 {
		return manageddata.UploadSession{}, ErrInvalid
	}
	d := digestFor(id, in.CollectionID.String(), in.BaseRevisionID.String(), string(b), strings.TrimSpace(in.StorageBackend), in.StagingPrefix, strings.TrimSpace(in.CreatedBy), in.ExpiresAt.UTC().Format(time.RFC3339Nano))
	manifestDigest := in.Manifest.RevisionID()
	baseRevisionID := in.BaseRevisionID.String()
	var baseRevisionPtr *string
	if baseRevisionID != "" {
		baseRevisionPtr = &baseRevisionID
	}
	if in.AuditIntent == nil {
		return r.createUploadSessionOn(ctx, db, in, id, baseRevisionPtr, b, count, size, d, manifestDigest)
	}
	if r.audit == nil {
		return manageddata.UploadSession{}, fmt.Errorf("%w: managed-data PostgreSQL audit recorder is required", ErrInvalid)
	}
	tx, owned, err := r.beginTransition(ctx)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if owned {
		defer func() {
			if returnErr != nil {
				_ = tx.Rollback(context.Background())
			}
		}()
	}
	s, err := r.createUploadSessionOn(ctx, tx, in, id, baseRevisionPtr, b, count, size, d, manifestDigest)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if err := r.audit.RecordAuditIntent(contextOrBackground(ctx), tx, *in.AuditIntent); err != nil {
		return manageddata.UploadSession{}, err
	}
	if owned {
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return manageddata.UploadSession{}, err
		}
	}
	return s, nil
}

// createUploadSessionOn performs the SQL and exact-replay validation on the
// supplied handle. Keeping reads on the same handle is required when the
// caller owns a transaction so the audit intent and row mutation commit (or
// roll back) atomically.
func (r *Repository) createUploadSessionOn(ctx context.Context, db DBTX, in manageddata.CreateUploadSessionInput, id string, baseRevisionPtr *string, b []byte, count, size int64, requestDigest, manifestDigest string) (manageddata.UploadSession, error) {
	queries := manageddb.New(db)
	err := queries.InsertUploadSession(contextOrBackground(ctx), manageddb.InsertUploadSessionParams{UploadID: id, CollectionID: in.CollectionID.String(), BaseRevisionID: baseRevisionPtr, Manifest: b, ExpectedFileCount: count, ExpectedSizeBytes: size, StorageBackend: strings.TrimSpace(in.StorageBackend), StagingPrefix: in.StagingPrefix, CreatedBy: strings.TrimSpace(in.CreatedBy), ExpiresAt: pgtype.Timestamptz{Time: in.ExpiresAt.UTC(), Valid: true}, RequestDigest: requestDigest, ManifestDigest: manifestDigest})
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	row, err := queries.GetUploadSessionByID(contextOrBackground(ctx), id)
	if err != nil {
		return manageddata.UploadSession{}, scanNotFound(err)
	}
	s := uploadFromValues(row.UploadID, row.CollectionID, row.BaseRevisionID, row.RevisionID, row.Status, row.Manifest, row.StorageBackend, row.StagingPrefix, row.CreatedBy, row.ExpectedFileCount, row.ExpectedSizeBytes, row.UploadedFileCount, row.UploadedSizeBytes, row.CreatedAt, row.UpdatedAt, row.ExpiresAt, row.CompletedAt, row.Error)
	storedDigest, err := queries.GetUploadRequestDigest(contextOrBackground(ctx), id)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	storedManifest, storedErr := parseManifest([]byte(s.ManifestJSON))
	storedJSON, _, _, canonicalErr := canonicalManifest(storedManifest)
	if storedErr != nil || canonicalErr != nil || s.CollectionID != in.CollectionID || !bytes.Equal(storedJSON, b) || s.ExpectedFileCount != count || s.ExpectedSizeBytes != size || storedDigest != requestDigest {
		return manageddata.UploadSession{}, ErrConflict
	}
	return s, nil
}
func (r *Repository) UploadSessionByID(ctx context.Context, id manageddata.UploadID) (manageddata.UploadSession, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if err := validID(id.String(), "upload id"); err != nil {
		return manageddata.UploadSession{}, err
	}
	row, err := manageddb.New(db).GetUploadSessionByID(contextOrBackground(ctx), id.String())
	if err != nil {
		return manageddata.UploadSession{}, scanNotFound(err)
	}
	return uploadFromValues(row.UploadID, row.CollectionID, row.BaseRevisionID, row.RevisionID, row.Status, row.Manifest, row.StorageBackend, row.StagingPrefix, row.CreatedBy, row.ExpectedFileCount, row.ExpectedSizeBytes, row.UploadedFileCount, row.UploadedSizeBytes, row.CreatedAt, row.UpdatedAt, row.ExpiresAt, row.CompletedAt, row.Error), nil
}
func (r *Repository) ListUploadSessions(ctx context.Context, collectionID projectgraph.ResourceID) ([]manageddata.UploadSession, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	if !collectionID.Valid() {
		return nil, ErrInvalid
	}
	rows, err := manageddb.New(db).ListUploadSessionsByCollection(contextOrBackground(ctx), collectionID.String())
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.UploadSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, uploadFromValues(row.UploadID, row.CollectionID, row.BaseRevisionID, row.RevisionID, row.Status, row.Manifest, row.StorageBackend, row.StagingPrefix, row.CreatedBy, row.ExpectedFileCount, row.ExpectedSizeBytes, row.UploadedFileCount, row.UploadedSizeBytes, row.CreatedAt, row.UpdatedAt, row.ExpiresAt, row.CompletedAt, row.Error))
	}
	return out, nil
}
func (r *Repository) ListUploadSessionsForCleanup(ctx context.Context, limit int64) ([]manageddata.UploadSession, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := manageddb.New(db).ListUploadSessionsForCleanup(contextOrBackground(ctx), int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.UploadSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, uploadFromValues(row.UploadID, row.CollectionID, row.BaseRevisionID, row.RevisionID, row.Status, row.Manifest, row.StorageBackend, row.StagingPrefix, row.CreatedBy, row.ExpectedFileCount, row.ExpectedSizeBytes, row.UploadedFileCount, row.UploadedSizeBytes, row.CreatedAt, row.UpdatedAt, row.ExpiresAt, row.CompletedAt, row.Error))
	}
	return out, nil
}
func (r *Repository) MarkUploadCleanupComplete(ctx context.Context, id manageddata.UploadID) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	var marked bool
	marked, err = manageddb.New(db).MarkUploadCleanup(contextOrBackground(ctx), id.String())
	if err != nil {
		return err
	}
	if !marked {
		return ErrConflict
	}
	return nil
}
func (r *Repository) UpdateUploadProgress(ctx context.Context, id manageddata.UploadID, p manageddata.UploadProgress) error {
	if p.UploadedFileCount < 0 || p.UploadedSizeBytes < 0 {
		return ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	tag, err := manageddb.New(db).UpdateUploadProgress(contextOrBackground(ctx), manageddb.UpdateUploadProgressParams{UploadID: id.String(), UploadedFileCount: p.UploadedFileCount, UploadedSizeBytes: p.UploadedSizeBytes})
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) BeginUploadFinalization(ctx context.Context, id manageddata.UploadID, _ jobspkg.WorkflowIntent) (manageddata.UploadSession, error) {
	return r.BeginUploadFinalizationTransition(ctx, id, manageddata.UploadTransition{})
}

func (r *Repository) BeginUploadFinalizationTransition(ctx context.Context, id manageddata.UploadID, transition manageddata.UploadTransition) (result manageddata.UploadSession, returnErr error) {
	if err := r.requireTransitionPorts(transition); err != nil {
		return manageddata.UploadSession{}, err
	}
	tx, owned, err := r.beginTransition(ctx)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if owned {
		defer func() {
			if returnErr != nil {
				_ = tx.Rollback(context.Background())
			}
		}()
	}
	queries := manageddb.New(tx)
	tag, err := queries.BeginUploadFinalization(contextOrBackground(ctx), id.String())
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if tag.RowsAffected() != 1 {
		row, lookupErr := queries.GetUploadSessionByID(contextOrBackground(ctx), id.String())
		if lookupErr != nil {
			return manageddata.UploadSession{}, scanNotFound(lookupErr)
		}
		if row.Status != string(manageddata.UploadStatusCommitting) {
			return manageddata.UploadSession{}, ErrConflict
		}
	}
	if err := r.recordTransition(contextOrBackground(ctx), tx, transition); err != nil {
		return manageddata.UploadSession{}, err
	}
	row, err := queries.GetUploadSessionByID(contextOrBackground(ctx), id.String())
	if err != nil {
		return manageddata.UploadSession{}, scanNotFound(err)
	}
	result = uploadFromValues(row.UploadID, row.CollectionID, row.BaseRevisionID, row.RevisionID, row.Status, row.Manifest, row.StorageBackend, row.StagingPrefix, row.CreatedBy, row.ExpectedFileCount, row.ExpectedSizeBytes, row.UploadedFileCount, row.UploadedSizeBytes, row.CreatedAt, row.UpdatedAt, row.ExpiresAt, row.CompletedAt, row.Error)
	if owned {
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return manageddata.UploadSession{}, err
		}
	}
	return result, nil
}
func (r *Repository) FailUploadFinalization(ctx context.Context, id manageddata.UploadID, msg string) (manageddata.UploadSession, error) {
	if msg == "" || len(msg) > maxErrorBytes {
		return manageddata.UploadSession{}, ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	tag, err := manageddb.New(db).FailUploadFinalization(contextOrBackground(ctx), manageddb.FailUploadFinalizationParams{UploadID: id.String(), Error: msg})
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if tag.RowsAffected() != 1 {
		return manageddata.UploadSession{}, ErrConflict
	}
	return r.UploadSessionByID(ctx, id)
}
func (r *Repository) AbortUploadSession(ctx context.Context, id manageddata.UploadID) error {
	return r.abortUploadSession(ctx, id)
}
func (r *Repository) AbortUploadSessionTransition(ctx context.Context, id manageddata.UploadID, transition manageddata.UploadTransition) (returnErr error) {
	if err := r.requireTransitionPorts(transition); err != nil {
		return err
	}
	tx, owned, err := r.beginTransition(ctx)
	if err != nil {
		return err
	}
	if owned {
		defer func() {
			if returnErr != nil {
				_ = tx.Rollback(context.Background())
			}
		}()
	}
	queries := manageddb.New(tx)
	tag, err := queries.AbortUploadSession(contextOrBackground(ctx), id.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		row, lookupErr := queries.GetUploadSessionByID(contextOrBackground(ctx), id.String())
		if lookupErr != nil {
			return scanNotFound(lookupErr)
		}
		if row.Status != string(manageddata.UploadStatusAborted) {
			return ErrConflict
		}
	}
	if err := r.recordTransition(contextOrBackground(ctx), tx, transition); err != nil {
		return err
	}
	if owned {
		returnErr = tx.Commit(contextOrBackground(ctx))
		return returnErr
	}
	return nil
}

func (r *Repository) requireTransitionPorts(transition manageddata.UploadTransition) error {
	if manageddata.WorkflowIntentPresent(transition.Workflow) && r.workflow == nil {
		return fmt.Errorf("%w: managed-data PostgreSQL workflow recorder is required", ErrInvalid)
	}
	if transition.AuditIntent != nil && r.audit == nil {
		return fmt.Errorf("%w: managed-data PostgreSQL audit recorder is required", ErrInvalid)
	}
	return nil
}

func (r *Repository) recordTransition(ctx context.Context, tx Tx, transition manageddata.UploadTransition) error {
	if manageddata.WorkflowIntentPresent(transition.Workflow) {
		if err := r.workflow.RecordWorkflow(ctx, tx, transition.Workflow); err != nil {
			return err
		}
	}
	if transition.AuditIntent != nil {
		if err := r.audit.RecordAuditIntent(ctx, tx, *transition.AuditIntent); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) beginTransition(ctx context.Context) (Tx, bool, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, false, err
	}
	if tx, ok := db.(Tx); ok {
		return tx, false, nil
	}
	b, ok := db.(beginner)
	if !ok {
		return nil, false, fmt.Errorf("%w: PostgreSQL transition requires a transaction-capable database", ErrInvalid)
	}
	tx, err := b.Begin(contextOrBackground(ctx))
	if err != nil {
		return nil, false, err
	}
	return tx, true, nil
}
func (r *Repository) abortUploadSession(ctx context.Context, id manageddata.UploadID) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	tag, err := manageddb.New(db).AbortUploadSession(contextOrBackground(ctx), id.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (r *Repository) ExpireUploadSessions(ctx context.Context, now time.Time) (int64, error) {
	db, err := requireDB(r)
	if err != nil {
		return 0, err
	}
	cutoff := pgtype.Timestamptz{}
	if !now.IsZero() {
		cutoff = pgtype.Timestamptz{Time: now.UTC(), Valid: true}
	}
	tag, err := manageddb.New(db).ExpireUploadSessions(contextOrBackground(ctx), cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) CompleteUpload(ctx context.Context, in manageddata.CompleteUploadInput) (manageddata.Revision, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Revision{}, err
	}
	if b, ok := db.(beginner); ok {
		if _, isTx := db.(pgx.Tx); !isTx {
			tx, e := b.Begin(contextOrBackground(ctx))
			if e != nil {
				return manageddata.Revision{}, e
			}
			rev, e := completeUploadTx(ctx, tx, in)
			if e != nil {
				_ = tx.Rollback(ctx)
				return manageddata.Revision{}, e
			}
			if e = tx.Commit(ctx); e != nil {
				return manageddata.Revision{}, e
			}
			return rev, nil
		}
	}
	return completeUploadTx(ctx, db, in)
}
func (r *Repository) CompleteUploadTx(ctx context.Context, tx Tx, in manageddata.CompleteUploadInput) (manageddata.Revision, error) {
	if tx == nil {
		return manageddata.Revision{}, ErrInvalid
	}
	return completeUploadTx(ctx, tx, in)
}
func completeUploadTx(ctx context.Context, db DBTX, in manageddata.CompleteUploadInput) (manageddata.Revision, error) {
	if in.SessionID == "" {
		return manageddata.Revision{}, ErrInvalid
	}
	row, err := manageddb.New(db).LockUploadSessionForCompletion(contextOrBackground(ctx), in.SessionID.String())
	if errors.Is(err, pgx.ErrNoRows) {
		return manageddata.Revision{}, ErrNotFound
	}
	if err != nil {
		return manageddata.Revision{}, err
	}
	if row.Status == string(manageddata.UploadStatusComplete) {
		if row.RevisionID == "" {
			return manageddata.Revision{}, ErrConflict
		}
		if row.CompletionDigest != completionDigest(in) || in.RevisionID != "" && in.RevisionID.String() != row.RevisionID {
			return manageddata.Revision{}, ErrConflict
		}
		r, e := manageddb.New(db).GetRevisionByID(contextOrBackground(ctx), row.RevisionID)
		if e != nil {
			return manageddata.Revision{}, scanNotFound(e)
		}
		return revisionFromValues(r.RevisionID, r.CollectionID, r.Digest, r.Status, r.Manifest, r.CreatedBy, r.Error, r.Sequence, r.FileCount, r.SizeBytes, r.CreatedAt, r.ReadyAt), nil
	}
	if row.Status != "committing" && row.Status != "open" {
		return manageddata.Revision{}, ErrConflict
	}
	if row.Status == "open" {
		tag, e := manageddb.New(db).BeginUploadFinalization(contextOrBackground(ctx), in.SessionID.String())
		if e != nil {
			return manageddata.Revision{}, e
		}
		if tag.RowsAffected() != 1 {
			return manageddata.Revision{}, ErrConflict
		}
	}
	m, err := parseManifest([]byte(row.Manifest))
	if err != nil {
		return manageddata.Revision{}, err
	}
	if err := validateStoredFiles(m, in.Files); err != nil {
		return manageddata.Revision{}, err
	}
	digest := m.RevisionID()
	revisionID := in.RevisionID.String()
	if revisionID == "" {
		revisionID = uuidID("revision")
	}
	if err := validID(revisionID, "revision id"); err != nil {
		return manageddata.Revision{}, err
	}
	collection, err := manageddb.New(db).LockCollection(contextOrBackground(ctx), row.CollectionID)
	if err != nil {
		return manageddata.Revision{}, err
	}
	sequence, err := manageddb.New(db).NextRevisionSequence(contextOrBackground(ctx), collection)
	if err != nil {
		return manageddata.Revision{}, err
	}
	seq := int64(sequence)
	err = manageddb.New(db).InsertRevisionFromUpload(contextOrBackground(ctx), manageddb.InsertRevisionFromUploadParams{RevisionID: revisionID, CollectionID: collection, Sequence: seq, Digest: digest, Manifest: []byte(row.Manifest), FileCount: row.ExpectedFileCount, SizeBytes: row.ExpectedSizeBytes, UploadID: in.SessionID.String()})
	if err != nil {
		return manageddata.Revision{}, err
	}
	files := append([]manageddata.StoredFile(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		if err = manageddb.New(db).InsertRevisionFile(contextOrBackground(ctx), manageddb.InsertRevisionFileParams{RevisionID: revisionID, LogicalPath: f.Path, SizeBytes: f.Size, Sha256: f.SHA256, StorageKey: f.StorageKey, MediaType: strings.TrimSpace(f.MediaType), Etag: strings.TrimSpace(f.ETag)}); err != nil {
			return manageddata.Revision{}, err
		}
	}
	readyTag, err := manageddb.New(db).MarkRevisionReady(contextOrBackground(ctx), revisionID)
	if err != nil {
		return manageddata.Revision{}, err
	}
	if readyTag.RowsAffected() != 1 {
		return manageddata.Revision{}, ErrConflict
	}
	tag, err := manageddb.New(db).CompleteUploadSession(contextOrBackground(ctx), manageddb.CompleteUploadSessionParams{UploadID: in.SessionID.String(), RevisionID: &revisionID, CompletionDigest: completionDigest(in)})
	if err != nil {
		return manageddata.Revision{}, err
	}
	if tag.RowsAffected() != 1 {
		return manageddata.Revision{}, ErrConflict
	}
	r, err := manageddb.New(db).GetRevisionByID(contextOrBackground(ctx), revisionID)
	if err != nil {
		return manageddata.Revision{}, scanNotFound(err)
	}
	return revisionFromValues(r.RevisionID, r.CollectionID, r.Digest, r.Status, r.Manifest, r.CreatedBy, r.Error, r.Sequence, r.FileCount, r.SizeBytes, r.CreatedAt, r.ReadyAt), nil
}

func validateStoredFiles(m manageddata.Manifest, files []manageddata.StoredFile) error {
	if len(m.Files) != len(files) {
		return ErrConflict
	}
	actual := make([]manageddata.File, 0, len(files))
	for _, f := range files {
		if f.StorageKey == "" {
			return ErrInvalid
		}
		actual = append(actual, f.File)
	}
	a, _ := manageddata.Manifest{Files: actual}.CanonicalJSON()
	w, _ := m.CanonicalJSON()
	if !bytes.Equal(a, w) {
		return ErrConflict
	}
	return nil
}

func (r *Repository) RevisionByID(ctx context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Revision{}, err
	}
	if err := validID(id.String(), "revision id"); err != nil {
		return manageddata.Revision{}, err
	}
	row, err := manageddb.New(db).GetRevisionByID(contextOrBackground(ctx), id.String())
	if err != nil {
		return manageddata.Revision{}, scanNotFound(err)
	}
	return revisionFromValues(row.RevisionID, row.CollectionID, row.Digest, row.Status, row.Manifest, row.CreatedBy, row.Error, row.Sequence, row.FileCount, row.SizeBytes, row.CreatedAt, row.ReadyAt), nil
}
func (r *Repository) ListRevisions(ctx context.Context, c projectgraph.ResourceID) ([]manageddata.Revision, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := manageddb.New(db).ListRevisionsByCollection(contextOrBackground(ctx), c.String())
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.Revision, 0, len(rows))
	for _, row := range rows {
		out = append(out, revisionFromValues(row.RevisionID, row.CollectionID, row.Digest, row.Status, row.Manifest, row.CreatedBy, row.Error, row.Sequence, row.FileCount, row.SizeBytes, row.CreatedAt, row.ReadyAt))
	}
	return out, nil
}
func (r *Repository) UploadSessionIDByRevisionID(ctx context.Context, id manageddata.RevisionID) (manageddata.UploadID, error) {
	db, err := requireDB(r)
	if err != nil {
		return "", err
	}
	revisionID := id.String()
	upload, err := manageddb.New(db).GetUploadIDByRevision(contextOrBackground(ctx), &revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return manageddata.UploadID(upload), err
}
func (r *Repository) ListRevisionFiles(ctx context.Context, id manageddata.RevisionID) ([]manageddata.RevisionFile, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := manageddb.New(db).ListRevisionFiles(contextOrBackground(ctx), id.String())
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.RevisionFile, 0, len(rows))
	for _, row := range rows {
		out = append(out, manageddata.RevisionFile{RevisionID: manageddata.RevisionID(row.RevisionID), StoredFile: manageddata.StoredFile{File: manageddata.File{Path: row.LogicalPath, Size: row.SizeBytes, SHA256: row.Sha256}, StorageKey: row.StorageKey, MediaType: row.MediaType, ETag: row.Etag}, CreatedAt: formatTime(row.CreatedAt.Time)})
	}
	return out, nil
}

func (r *Repository) EnvironmentPointer(ctx context.Context, c projectgraph.ResourceID, e manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	if _, err := manageddata.NormalizeEnvironment(string(e)); err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	row, err := manageddb.New(db).GetEnvironmentPointer(contextOrBackground(ctx), manageddb.GetEnvironmentPointerParams{CollectionID: c.String(), Environment: string(e)})
	if errors.Is(err, pgx.ErrNoRows) {
		return manageddata.EnvironmentPointer{}, ErrNotFound
	}
	if err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	p := manageddata.EnvironmentPointer{CollectionID: projectgraph.ResourceID(row.CollectionID), Environment: manageddata.Environment(row.Environment), RevisionID: manageddata.RevisionID(row.RevisionID), RevisionDigest: row.RevisionDigest, DeploymentID: row.DeploymentID, Generation: row.Generation, UpdatedBy: row.UpdatedBy, UpdatedAt: formatTime(row.UpdatedAt.Time)}
	return p, nil
}

// ActiveEnvironmentPointer returns the durable environment pointer used by
// the managed-data API. Production serving additionally validates immutable
// generation bindings through the resolver; keeping this method on the
// native repository preserves the capability-owned API adapter contract.
func (r *Repository) ActiveEnvironmentPointer(ctx context.Context, c projectgraph.ResourceID, e manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	return r.EnvironmentPointer(ctx, c, e)
}
func (r *Repository) InstallEnvironmentPointerTx(ctx context.Context, tx Tx, p manageddata.EnvironmentPointer) error {
	if tx == nil || !p.CollectionID.Valid() || p.RevisionID == "" || p.DeploymentID == "" || p.Generation < 1 {
		return ErrInvalid
	}
	if _, err := manageddata.NormalizeEnvironment(string(p.Environment)); err != nil {
		return err
	}
	if err := manageddata.ValidateRevisionID(p.RevisionDigest); err != nil {
		return err
	}
	tag, err := manageddb.New(tx).UpsertEnvironmentPointer(contextOrBackground(ctx), manageddb.UpsertEnvironmentPointerParams{CollectionID: p.CollectionID.String(), Environment: string(p.Environment), RevisionID: p.RevisionID.String(), RevisionDigest: p.RevisionDigest, DeploymentID: p.DeploymentID, Generation: p.Generation, UpdatedBy: p.UpdatedBy})
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	row, err := manageddb.New(tx).GetEnvironmentPointer(contextOrBackground(ctx), manageddb.GetEnvironmentPointerParams{CollectionID: p.CollectionID.String(), Environment: string(p.Environment)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if row.Generation == p.Generation && row.Environment == string(p.Environment) && row.RevisionID == p.RevisionID.String() && row.RevisionDigest == p.RevisionDigest && row.DeploymentID == p.DeploymentID && row.UpdatedBy == p.UpdatedBy {
		return nil
	}
	return ErrConflict
}

func bindingDigest(bindings []manageddata.ServingStateBinding) string {
	sorted := append([]manageddata.ServingStateBinding(nil), bindings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CollectionID < sorted[j].CollectionID })
	var payload strings.Builder
	for _, b := range sorted {
		payload.WriteString(b.CollectionID.String())
		payload.WriteByte(31)
		payload.WriteString(b.RevisionID.String())
		payload.WriteByte(31)
	}
	return digestBytes([]byte(payload.String()))
}
func (r *Repository) InstallServingStateBindings(ctx context.Context, identity projectgraph.ServingIdentity, bindings []manageddata.ServingStateBinding) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	if len(bindings) > 10000 {
		return ErrInvalid
	}
	normalized := append([]manageddata.ServingStateBinding(nil), bindings...)
	seen := map[string]bool{}
	for i := range normalized {
		if !normalized[i].CollectionID.Valid() || normalized[i].RevisionID == "" || seen[normalized[i].CollectionID.String()] {
			return ErrInvalid
		}
		seen[normalized[i].CollectionID.String()] = true
		normalized[i].Identity = identity
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].CollectionID < normalized[j].CollectionID })
	digest := bindingDigest(normalized)
	tx, ok := db.(pgx.Tx)
	if !ok {
		b, yes := db.(beginner)
		if !yes {
			return ErrInvalid
		}
		tx, e := b.Begin(contextOrBackground(ctx))
		if e != nil {
			return e
		}
		if e = installBindingsTx(ctx, tx, identity, normalized, digest); e != nil {
			_ = tx.Rollback(ctx)
			return e
		}
		return tx.Commit(ctx)
	}
	return installBindingsTx(ctx, tx, identity, normalized, digest)
}
func installBindingsTx(ctx context.Context, tx Tx, identity projectgraph.ServingIdentity, b []manageddata.ServingStateBinding, digest string) error {
	type entry struct {
		CollectionID string `json:"collection_id"`
		RevisionID   string `json:"revision_id"`
	}
	entries := make([]entry, 0, len(b))
	for _, v := range b {
		entries = append(entries, entry{v.CollectionID.String(), v.RevisionID.String()})
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	err = manageddb.New(tx).PublishBindingSet(contextOrBackground(ctx), manageddb.PublishBindingSetParams{ProjectID: identity.ProjectID.String(), Environment: string(identity.Environment), GenerationID: identity.GenerationID, BindingDigest: digest, BindingCount: int64(len(b)), Bindings: payload})
	if err != nil && strings.Contains(err.Error(), "binding set conflicts") {
		return ErrConflict
	}
	return err
}
func (r *Repository) ListServingStateBindings(ctx context.Context, identity projectgraph.ServingIdentity) ([]manageddata.ServingStateBinding, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	var markerDigest string
	var markerCount int64
	marker, err := manageddb.New(db).GetBindingSetMarker(contextOrBackground(ctx), manageddb.GetBindingSetMarkerParams{ProjectID: identity.ProjectID.String(), Environment: string(identity.Environment), GenerationID: identity.GenerationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	markerDigest, markerCount = marker.BindingDigest, marker.BindingCount
	rows, err := manageddb.New(db).ListBindings(contextOrBackground(ctx), manageddb.ListBindingsParams{ProjectID: identity.ProjectID.String(), Environment: string(identity.Environment), GenerationID: identity.GenerationID})
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.ServingStateBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, manageddata.ServingStateBinding{Identity: identity, CollectionID: projectgraph.ResourceID(row.CollectionID), RevisionID: manageddata.RevisionID(row.RevisionID), BoundAt: formatTime(row.BoundAt.Time)})
	}
	if int64(len(out)) != markerCount || bindingDigest(out) != markerDigest {
		return nil, ErrConflict
	}
	return out, nil
}

// Lease is a database-fenced lease. A new acquisition always increments the
// fencing epoch, preventing stale workers from committing physical effects.
type Lease struct {
	Key, Owner   string
	FencingEpoch int64
	ExpiresAt    time.Time
}

func (r *Repository) AcquireLease(ctx context.Context, key, owner string, duration time.Duration) (Lease, error) {
	db, err := requireDB(r)
	if err != nil {
		return Lease{}, err
	}
	if err := canonicalText(key, 255); err != nil || canonicalText(owner, 255) != nil || duration < time.Microsecond || duration > maxLease {
		return Lease{}, ErrInvalid
	}
	row, err := manageddb.New(db).AcquireLease(contextOrBackground(ctx), manageddb.AcquireLeaseParams{LeaseKey: key, OwnerID: owner, DurationMicros: duration.Microseconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrLeaseBusy
	}
	if err != nil {
		return Lease{}, err
	}
	return Lease{Key: row.LeaseKey, Owner: row.OwnerID, FencingEpoch: row.FencingEpoch, ExpiresAt: row.ExpiresAt.Time}, nil
}
func (r *Repository) RenewLease(ctx context.Context, key, owner string, epoch int64, duration time.Duration) (Lease, error) {
	db, err := requireDB(r)
	if err != nil {
		return Lease{}, err
	}
	if epoch < 1 || duration < time.Microsecond || duration > maxLease {
		return Lease{}, ErrInvalid
	}
	row, err := manageddb.New(db).RenewLease(contextOrBackground(ctx), manageddb.RenewLeaseParams{LeaseKey: key, OwnerID: owner, FencingEpoch: epoch, DurationMicros: duration.Microseconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrStaleFence
	}
	if err != nil {
		return Lease{}, err
	}
	return Lease{Key: row.LeaseKey, Owner: row.OwnerID, FencingEpoch: row.FencingEpoch, ExpiresAt: row.ExpiresAt.Time}, nil
}
func (r *Repository) ReleaseLease(ctx context.Context, key, owner string, epoch int64) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	tag, err := manageddb.New(db).ReleaseLease(contextOrBackground(ctx), manageddb.ReleaseLeaseParams{LeaseKey: key, OwnerID: owner, FencingEpoch: epoch})
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
