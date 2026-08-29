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

	"github.com/flidai/leapview/internal/manageddata"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
type Repository struct{ db DBTX }

var _ manageddata.Repository = (*Repository)(nil)

func New(db DBTX) *Repository                  { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository        { return New(db) }
func (r *Repository) WithTx(tx Tx) *Repository { return New(tx) }

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
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.collection(collection_id,project_id,connection_id,name,description,created_by,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, id, in.ProjectID.String(), in.ConnectionID.String(), in.Name, in.Description, strings.TrimSpace(in.CreatedBy), d)
	if err != nil {
		return manageddata.Collection{}, err
	}
	c, err := r.CollectionByProjectConnection(ctx, in.ProjectID, in.ConnectionID)
	if err != nil {
		return manageddata.Collection{}, err
	}
	var storedDigest string
	if err := db.QueryRow(contextOrBackground(ctx), `SELECT request_digest FROM managed_data.collection WHERE collection_id=$1`, c.ID.String()).Scan(&storedDigest); err != nil {
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
	return scanCollection(db.QueryRow(contextOrBackground(ctx), `SELECT collection_id,project_id,connection_id,name,description,status,created_by,created_at,updated_at,archived_at FROM managed_data.collection WHERE collection_id=$1`, id.String()))
}
func (r *Repository) CollectionByProjectConnection(ctx context.Context, projectID, connectionID projectgraph.ResourceID) (manageddata.Collection, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Collection{}, err
	}
	if !projectID.Valid() || !connectionID.Valid() {
		return manageddata.Collection{}, ErrInvalid
	}
	return scanCollection(db.QueryRow(contextOrBackground(ctx), `SELECT collection_id,project_id,connection_id,name,description,status,created_by,created_at,updated_at,archived_at FROM managed_data.collection WHERE project_id=$1 AND connection_id=$2`, projectID.String(), connectionID.String()))
}
func (r *Repository) ListCollections(ctx context.Context, includeArchived bool) ([]manageddata.Collection, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	q := `SELECT collection_id,project_id,connection_id,name,description,status,created_by,created_at,updated_at,archived_at FROM managed_data.collection`
	if !includeArchived {
		q += ` WHERE status='active'`
	}
	q += ` ORDER BY project_id,connection_id,collection_id`
	rows, err := db.Query(contextOrBackground(ctx), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]manageddata.Collection, 0)
	for rows.Next() {
		c, e := scanCollection(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (r *Repository) ArchiveCollection(ctx context.Context, id projectgraph.ResourceID) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	if !id.Valid() {
		return ErrInvalid
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.collection SET status='archived',archived_at=clock_timestamp(),updated_at=clock_timestamp() WHERE collection_id=$1 AND status='active'`, id.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) CreateUploadSession(ctx context.Context, in manageddata.CreateUploadSessionInput) (manageddata.UploadSession, error) {
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
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.upload_session(upload_id,collection_id,base_revision_id,manifest,expected_file_count,expected_size_bytes,storage_backend,staging_prefix,created_by,expires_at,request_digest,manifest_digest) VALUES($1,$2,NULLIF($3,'')::text,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(upload_id) DO NOTHING`, id, in.CollectionID.String(), in.BaseRevisionID.String(), b, count, size, strings.TrimSpace(in.StorageBackend), in.StagingPrefix, strings.TrimSpace(in.CreatedBy), in.ExpiresAt.UTC(), d, manifestDigest)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	s, err := r.UploadSessionByID(ctx, manageddata.UploadID(id))
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	var storedDigest string
	if err := db.QueryRow(contextOrBackground(ctx), `SELECT request_digest FROM managed_data.upload_session WHERE upload_id=$1`, id).Scan(&storedDigest); err != nil {
		return manageddata.UploadSession{}, err
	}
	storedManifest, storedErr := parseManifest([]byte(s.ManifestJSON))
	storedJSON, _, _, canonicalErr := canonicalManifest(storedManifest)
	if storedErr != nil || canonicalErr != nil || s.CollectionID != in.CollectionID || !bytes.Equal(storedJSON, b) || s.ExpectedFileCount != count || s.ExpectedSizeBytes != size || (suppliedID && storedDigest != d) {
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
	return scanUpload(db.QueryRow(contextOrBackground(ctx), `SELECT upload_id,collection_id,COALESCE(base_revision_id,''),COALESCE(revision_id,''),status,manifest::text,expected_file_count,expected_size_bytes,uploaded_file_count,uploaded_size_bytes,storage_backend,staging_prefix,created_by,created_at,updated_at,expires_at,completed_at,error FROM managed_data.upload_session WHERE upload_id=$1`, id.String()))
}
func (r *Repository) ListUploadSessions(ctx context.Context, collectionID projectgraph.ResourceID) ([]manageddata.UploadSession, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	if !collectionID.Valid() {
		return nil, ErrInvalid
	}
	rows, err := db.Query(contextOrBackground(ctx), `SELECT upload_id,collection_id,COALESCE(base_revision_id,''),COALESCE(revision_id,''),status,manifest::text,expected_file_count,expected_size_bytes,uploaded_file_count,uploaded_size_bytes,storage_backend,staging_prefix,created_by,created_at,updated_at,expires_at,completed_at,error FROM managed_data.upload_session WHERE collection_id=$1 ORDER BY created_at DESC,upload_id DESC`, collectionID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.UploadSession{}
	for rows.Next() {
		s, e := scanUpload(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (r *Repository) ListUploadSessionsForCleanup(ctx context.Context, limit int64) ([]manageddata.UploadSession, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(contextOrBackground(ctx), `SELECT upload_id,collection_id,COALESCE(base_revision_id,''),COALESCE(revision_id,''),status,manifest::text,expected_file_count,expected_size_bytes,uploaded_file_count,uploaded_size_bytes,storage_backend,staging_prefix,created_by,created_at,updated_at,expires_at,completed_at,error FROM managed_data.upload_session WHERE status IN ('complete','aborted','expired','failed') AND cleanup_completed_at IS NULL ORDER BY updated_at,upload_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.UploadSession{}
	for rows.Next() {
		s, e := scanUpload(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (r *Repository) MarkUploadCleanupComplete(ctx context.Context, id manageddata.UploadID) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	var marked bool
	err = db.QueryRow(contextOrBackground(ctx), `SELECT managed_data.mark_upload_cleanup($1)`, id.String()).Scan(&marked)
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
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET uploaded_file_count=$2,uploaded_size_bytes=$3,updated_at=clock_timestamp() WHERE upload_id=$1 AND status='open' AND $2<=expected_file_count AND $3<=expected_size_bytes`, id.String(), p.UploadedFileCount, p.UploadedSizeBytes)
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
func (r *Repository) BeginUploadFinalizationTransition(ctx context.Context, id manageddata.UploadID, _ manageddata.UploadTransition) (manageddata.UploadSession, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET status='committing',updated_at=clock_timestamp() WHERE upload_id=$1 AND status='open' AND expires_at>clock_timestamp()`, id.String())
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	if tag.RowsAffected() != 1 {
		return manageddata.UploadSession{}, ErrConflict
	}
	return r.UploadSessionByID(ctx, id)
}
func (r *Repository) FailUploadFinalization(ctx context.Context, id manageddata.UploadID, msg string) (manageddata.UploadSession, error) {
	if msg == "" || len(msg) > maxErrorBytes {
		return manageddata.UploadSession{}, ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return manageddata.UploadSession{}, err
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET status='failed',error=$2,updated_at=clock_timestamp() WHERE upload_id=$1 AND status='committing'`, id.String(), msg)
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
func (r *Repository) AbortUploadSessionTransition(ctx context.Context, id manageddata.UploadID, _ manageddata.UploadTransition) error {
	return r.abortUploadSession(ctx, id)
}
func (r *Repository) abortUploadSession(ctx context.Context, id manageddata.UploadID) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET status='aborted',updated_at=clock_timestamp() WHERE upload_id=$1 AND status='open'`, id.String())
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
	var cutoff any
	if !now.IsZero() {
		cutoff = now.UTC()
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET status='expired',updated_at=clock_timestamp() WHERE status='open' AND expires_at<=LEAST(COALESCE($1::timestamptz,clock_timestamp()),clock_timestamp())`, cutoff)
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
	var status, collection, manifest, existingCompletionDigest string
	var expectedCount, expectedSize int64
	var existingID string
	err := db.QueryRow(contextOrBackground(ctx), `SELECT status,collection_id,manifest::text,expected_file_count,expected_size_bytes,COALESCE(revision_id,''),completion_digest FROM managed_data.upload_session WHERE upload_id=$1 FOR UPDATE`, in.SessionID.String()).Scan(&status, &collection, &manifest, &expectedCount, &expectedSize, &existingID, &existingCompletionDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return manageddata.Revision{}, ErrNotFound
	}
	if err != nil {
		return manageddata.Revision{}, err
	}
	if status == string(manageddata.UploadStatusComplete) {
		if existingID == "" {
			return manageddata.Revision{}, ErrConflict
		}
		if existingCompletionDigest != completionDigest(in) || in.RevisionID != "" && in.RevisionID.String() != existingID {
			return manageddata.Revision{}, ErrConflict
		}
		return scanRevision(db.QueryRow(contextOrBackground(ctx), revisionSelect+` WHERE revision_id=$1`, existingID))
	}
	if status != "committing" && status != "open" {
		return manageddata.Revision{}, ErrConflict
	}
	if status == "open" {
		tag, e := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET status='committing',updated_at=clock_timestamp() WHERE upload_id=$1 AND status='open' AND expires_at>clock_timestamp()`, in.SessionID.String())
		if e != nil {
			return manageddata.Revision{}, e
		}
		if tag.RowsAffected() != 1 {
			return manageddata.Revision{}, ErrConflict
		}
	}
	m, err := parseManifest([]byte(manifest))
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
	var seq int64
	if err := db.QueryRow(contextOrBackground(ctx), `SELECT collection_id FROM managed_data.collection WHERE collection_id=$1 FOR UPDATE`, collection).Scan(new(string)); err != nil {
		return manageddata.Revision{}, err
	}
	if err := db.QueryRow(contextOrBackground(ctx), `SELECT COALESCE(MAX(sequence),0)+1 FROM managed_data.revision WHERE collection_id=$1`, collection).Scan(&seq); err != nil {
		return manageddata.Revision{}, err
	}
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.revision(revision_id,collection_id,sequence,digest,status,manifest,file_count,size_bytes,created_by) SELECT $1,$2,$3,$4,'pending',$5::jsonb,$6,$7,created_by FROM managed_data.upload_session WHERE upload_id=$8`, revisionID, collection, seq, digest, manifest, expectedCount, expectedSize, in.SessionID.String())
	if err != nil {
		return manageddata.Revision{}, err
	}
	files := append([]manageddata.StoredFile(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		if _, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.revision_file(revision_id,logical_path,size_bytes,sha256,storage_key,media_type,etag) VALUES($1,$2,$3,$4,$5,$6,$7)`, revisionID, f.Path, f.Size, f.SHA256, f.StorageKey, strings.TrimSpace(f.MediaType), strings.TrimSpace(f.ETag)); err != nil {
			return manageddata.Revision{}, err
		}
	}
	readyTag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.revision SET status='ready',ready_at=clock_timestamp() WHERE revision_id=$1 AND status='pending'`, revisionID)
	if err != nil {
		return manageddata.Revision{}, err
	}
	if readyTag.RowsAffected() != 1 {
		return manageddata.Revision{}, ErrConflict
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.upload_session SET status='complete',revision_id=$2,completion_digest=$3,uploaded_file_count=expected_file_count,uploaded_size_bytes=expected_size_bytes,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE upload_id=$1 AND status='committing'`, in.SessionID.String(), revisionID, completionDigest(in))
	if err != nil {
		return manageddata.Revision{}, err
	}
	if tag.RowsAffected() != 1 {
		return manageddata.Revision{}, ErrConflict
	}
	return scanRevision(db.QueryRow(contextOrBackground(ctx), revisionSelect+` WHERE revision_id=$1`, revisionID))
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

const revisionSelect = `SELECT revision_id,collection_id,sequence,digest,status,manifest::text,file_count,size_bytes,created_by,created_at,ready_at,error FROM managed_data.revision`

func (r *Repository) RevisionByID(ctx context.Context, id manageddata.RevisionID) (manageddata.Revision, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.Revision{}, err
	}
	if err := validID(id.String(), "revision id"); err != nil {
		return manageddata.Revision{}, err
	}
	return scanRevision(db.QueryRow(contextOrBackground(ctx), revisionSelect+` WHERE revision_id=$1`, id.String()))
}
func (r *Repository) ListRevisions(ctx context.Context, c projectgraph.ResourceID) ([]manageddata.Revision, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(contextOrBackground(ctx), revisionSelect+` WHERE collection_id=$1 ORDER BY sequence DESC`, c.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.Revision{}
	for rows.Next() {
		v, e := scanRevision(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) UploadSessionIDByRevisionID(ctx context.Context, id manageddata.RevisionID) (manageddata.UploadID, error) {
	db, err := requireDB(r)
	if err != nil {
		return "", err
	}
	var upload string
	err = db.QueryRow(contextOrBackground(ctx), `SELECT upload_id FROM managed_data.upload_session WHERE revision_id=$1 AND status='complete'`, id.String()).Scan(&upload)
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
	rows, err := db.Query(contextOrBackground(ctx), `SELECT revision_id,logical_path,size_bytes,sha256,storage_key,media_type,etag,created_at FROM managed_data.revision_file WHERE revision_id=$1 ORDER BY logical_path`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.RevisionFile{}
	for rows.Next() {
		var rid, path, sha, key, media, etag string
		var size int64
		var created time.Time
		if err := rows.Scan(&rid, &path, &size, &sha, &key, &media, &etag, &created); err != nil {
			return nil, err
		}
		out = append(out, manageddata.RevisionFile{RevisionID: manageddata.RevisionID(rid), StoredFile: manageddata.StoredFile{File: manageddata.File{Path: path, Size: size, SHA256: sha}, StorageKey: key, MediaType: media, ETag: etag}, CreatedAt: formatTime(created)})
	}
	return out, rows.Err()
}

func (r *Repository) EnvironmentPointer(ctx context.Context, c projectgraph.ResourceID, e manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	if _, err := manageddata.NormalizeEnvironment(string(e)); err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	var p manageddata.EnvironmentPointer
	var rev, digest, env string
	var generation int64
	var at time.Time
	err = db.QueryRow(contextOrBackground(ctx), `SELECT collection_id,environment,revision_id,revision_digest,deployment_id,generation,updated_by,updated_at FROM managed_data.environment_pointer WHERE collection_id=$1 AND environment=$2`, c.String(), string(e)).Scan(&p.CollectionID, &env, &rev, &digest, &p.DeploymentID, &generation, &p.UpdatedBy, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return manageddata.EnvironmentPointer{}, ErrNotFound
	}
	if err != nil {
		return manageddata.EnvironmentPointer{}, err
	}
	p.Environment = manageddata.Environment(env)
	p.RevisionID = manageddata.RevisionID(rev)
	p.RevisionDigest = digest
	p.Generation = generation
	p.UpdatedAt = formatTime(at)
	return p, nil
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
	tag, err := tx.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.environment_pointer(collection_id,environment,revision_id,revision_digest,deployment_id,generation,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(collection_id,environment) DO UPDATE SET revision_id=EXCLUDED.revision_id,revision_digest=EXCLUDED.revision_digest,deployment_id=EXCLUDED.deployment_id,generation=EXCLUDED.generation,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp() WHERE managed_data.environment_pointer.generation<EXCLUDED.generation`, p.CollectionID.String(), string(p.Environment), p.RevisionID.String(), p.RevisionDigest, p.DeploymentID, p.Generation, p.UpdatedBy)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	var existing manageddata.EnvironmentPointer
	var env, rev, digest string
	var generation int64
	var updated time.Time
	err = tx.QueryRow(contextOrBackground(ctx), `SELECT environment,revision_id,revision_digest,deployment_id,generation,updated_by,updated_at FROM managed_data.environment_pointer WHERE collection_id=$1 AND environment=$2`, p.CollectionID.String(), string(p.Environment)).Scan(&env, &rev, &digest, &existing.DeploymentID, &generation, &existing.UpdatedBy, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if generation == p.Generation && env == string(p.Environment) && rev == p.RevisionID.String() && digest == p.RevisionDigest && existing.DeploymentID == p.DeploymentID && existing.UpdatedBy == p.UpdatedBy {
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
	_, err = tx.Exec(contextOrBackground(ctx), `SELECT managed_data.publish_binding_set($1,$2,$3,$4,$5,$6::jsonb)`, identity.ProjectID.String(), identity.Environment, identity.GenerationID, digest, len(b), payload)
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
	if err := db.QueryRow(contextOrBackground(ctx), `SELECT binding_digest,binding_count FROM managed_data.binding_set WHERE project_id=$1 AND environment=$2 AND generation_id=$3`, identity.ProjectID.String(), identity.Environment, identity.GenerationID).Scan(&markerDigest, &markerCount); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := db.Query(contextOrBackground(ctx), `SELECT collection_id,revision_id,bound_at FROM managed_data.binding WHERE project_id=$1 AND environment=$2 AND generation_id=$3 ORDER BY collection_id`, identity.ProjectID.String(), identity.Environment, identity.GenerationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.ServingStateBinding{}
	for rows.Next() {
		var c, r string
		var t time.Time
		if err := rows.Scan(&c, &r, &t); err != nil {
			return nil, err
		}
		out = append(out, manageddata.ServingStateBinding{Identity: identity, CollectionID: projectgraph.ResourceID(c), RevisionID: manageddata.RevisionID(r), BoundAt: formatTime(t)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	var l Lease
	err = db.QueryRow(contextOrBackground(ctx), `INSERT INTO managed_data.lease(lease_key,owner_id,fencing_epoch,expires_at) SELECT $1,$2,1,clock_timestamp()+($3::bigint * interval '1 microsecond') WHERE $3::bigint>0 AND $3::bigint<=86400000000 ON CONFLICT(lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id,fencing_epoch=managed_data.lease.fencing_epoch+1,expires_at=EXCLUDED.expires_at,state='held',released_at=NULL WHERE managed_data.lease.expires_at<=clock_timestamp() OR managed_data.lease.owner_id=$2 RETURNING lease_key,owner_id,fencing_epoch,expires_at`, key, owner, duration.Microseconds()).Scan(&l.Key, &l.Owner, &l.FencingEpoch, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrLeaseBusy
	}
	return l, err
}
func (r *Repository) RenewLease(ctx context.Context, key, owner string, epoch int64, duration time.Duration) (Lease, error) {
	db, err := requireDB(r)
	if err != nil {
		return Lease{}, err
	}
	if epoch < 1 || duration < time.Microsecond || duration > maxLease {
		return Lease{}, ErrInvalid
	}
	var l Lease
	err = db.QueryRow(contextOrBackground(ctx), `UPDATE managed_data.lease SET expires_at=clock_timestamp()+($4::bigint * interval '1 microsecond') WHERE lease_key=$1 AND owner_id=$2 AND fencing_epoch=$3 AND state='held' AND expires_at>clock_timestamp() AND $4::bigint>0 AND $4::bigint<=86400000000 AND clock_timestamp()+($4::bigint * interval '1 microsecond')>expires_at RETURNING lease_key,owner_id,fencing_epoch,expires_at`, key, owner, epoch, duration.Microseconds()).Scan(&l.Key, &l.Owner, &l.FencingEpoch, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrStaleFence
	}
	return l, err
}
func (r *Repository) ReleaseLease(ctx context.Context, key, owner string, epoch int64) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.lease SET state='released',released_at=clock_timestamp(),expires_at=clock_timestamp() WHERE lease_key=$1 AND owner_id=$2 AND fencing_epoch=$3 AND state='held'`, key, owner, epoch)
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
func scanCollection(row interface{ Scan(...any) error }) (manageddata.Collection, error) {
	var c manageddata.Collection
	var id, p, conn, status, created, updated string
	var archived *time.Time
	var ca, ua time.Time
	err := row.Scan(&id, &p, &conn, &c.Name, &c.Description, &status, &c.CreatedBy, &ca, &ua, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.ID = projectgraph.ResourceID(id)
	c.ProjectID = projectgraph.ResourceID(p)
	c.ConnectionID = projectgraph.ResourceID(conn)
	c.Status = manageddata.CollectionStatus(status)
	created, updated = formatTime(ca), formatTime(ua)
	c.CreatedAt, c.UpdatedAt = created, updated
	if archived != nil {
		c.ArchivedAt = formatTime(*archived)
	}
	return c, nil
}
func scanUpload(row interface{ Scan(...any) error }) (manageddata.UploadSession, error) {
	var s manageddata.UploadSession
	var id, c, base, rev, status, manifest, backend, prefix, by string
	var expected, size, upc, ups int64
	var created, updated, expires time.Time
	var completed *time.Time
	var errstr string
	err := row.Scan(&id, &c, &base, &rev, &status, &manifest, &expected, &size, &upc, &ups, &backend, &prefix, &by, &created, &updated, &expires, &completed, &errstr)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	s.ID = manageddata.UploadID(id)
	s.CollectionID = projectgraph.ResourceID(c)
	s.BaseRevisionID = manageddata.RevisionID(base)
	s.RevisionID = manageddata.RevisionID(rev)
	s.Status = manageddata.UploadStatus(status)
	s.ManifestJSON = manifest
	s.ExpectedFileCount, s.ExpectedSizeBytes = expected, size
	s.UploadedFileCount, s.UploadedSizeBytes = upc, ups
	s.StorageBackend, s.StagingPrefix, s.CreatedBy = backend, prefix, by
	s.CreatedAt, s.UpdatedAt, s.ExpiresAt = formatTime(created), formatTime(updated), formatTime(expires)
	if completed != nil {
		s.CompletedAt = formatTime(*completed)
	}
	s.Error = errstr
	return s, nil
}
func scanRevision(row interface{ Scan(...any) error }) (manageddata.Revision, error) {
	var v manageddata.Revision
	var id, c, digest, status, manifest, by, errstr string
	var seq, fc, size int64
	var created time.Time
	var ready *time.Time
	err := row.Scan(&id, &c, &seq, &digest, &status, &manifest, &fc, &size, &by, &created, &ready, &errstr)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.ID = manageddata.RevisionID(id)
	v.CollectionID = projectgraph.ResourceID(c)
	v.Sequence = seq
	v.Digest = digest
	v.Status = manageddata.RevisionStatus(status)
	v.ManifestJSON = manifest
	v.FileCount, v.SizeBytes = fc, size
	v.CreatedBy = by
	v.CreatedAt = formatTime(created)
	if ready != nil {
		v.ReadyAt = formatTime(*ready)
	}
	v.Error = errstr
	return v, nil
}
