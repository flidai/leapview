package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/jackc/pgx/v5"
)

const multipartSelect = `SELECT multipart_id,upload_id,logical_path,sha256,size_bytes,object_key,provider_upload_id,status,existing,idempotency_identity,completion_identity,completion_request_hash,abort_identity,created_at,updated_at,completed_at,aborted_at,error FROM managed_data.multipart_upload`
const multipartSelectQualified = `SELECT m.multipart_id,m.upload_id,m.logical_path,m.sha256,m.size_bytes,m.object_key,m.provider_upload_id,m.status,m.existing,m.idempotency_identity,m.completion_identity,m.completion_request_hash,m.abort_identity,m.created_at,m.updated_at,m.completed_at,m.aborted_at,m.error FROM managed_data.multipart_upload m`

func (r *Repository) CreateS3MultipartUpload(ctx context.Context, in manageddata.CreateS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	id := in.ID.String()
	if id == "" {
		id = uuidID("multipart")
	}
	if err := validID(id, "multipart id"); err != nil || in.UploadSessionID == "" || in.LogicalPath == "" || in.SHA256 == "" || len(in.SHA256) != 64 || in.SizeBytes < 0 || in.IdempotencyIdentity == "" {
		return manageddata.S3MultipartUpload{}, ErrInvalid
	}
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.multipart_upload(multipart_id,upload_id,logical_path,sha256,size_bytes,idempotency_identity) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, id, in.UploadSessionID.String(), in.LogicalPath, in.SHA256, in.SizeBytes, in.IdempotencyIdentity)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	u, err := r.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			var existingID string
			lookupErr := db.QueryRow(contextOrBackground(ctx), `SELECT multipart_id FROM managed_data.multipart_upload WHERE upload_id=$1 AND idempotency_identity=$2`, in.UploadSessionID.String(), in.IdempotencyIdentity).Scan(&existingID)
			if lookupErr == nil && existingID != id {
				return manageddata.S3MultipartUpload{}, ErrConflict
			}
		}
		return u, err
	}
	if u.UploadSessionID != in.UploadSessionID || u.LogicalPath != in.LogicalPath || u.SHA256 != in.SHA256 || u.SizeBytes != in.SizeBytes || u.IdempotencyIdentity != in.IdempotencyIdentity {
		return manageddata.S3MultipartUpload{}, ErrConflict
	}
	return u, nil
}
func (r *Repository) S3MultipartUploadByID(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	return scanMultipart(db.QueryRow(contextOrBackground(ctx), multipartSelect+` WHERE multipart_id=$1`, id.String()))
}
func (r *Repository) InitializeS3MultipartUpload(ctx context.Context, in manageddata.InitializeS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if in.ID == "" || in.ObjectKey == "" || (!in.Existing && in.ProviderUploadID == "") {
		return manageddata.S3MultipartUpload{}, ErrInvalid
	}
	status := "open"
	if in.Existing {
		status = "completed"
	}
	completed := ""
	if in.Existing {
		completed = ",completed_at=clock_timestamp()"
	}
	q := `UPDATE managed_data.multipart_upload SET object_key=$2,provider_upload_id=$3,status=$4,existing=$5,updated_at=clock_timestamp()` + completed + ` WHERE multipart_id=$1 AND status='creating'`
	tag, err := db.Exec(contextOrBackground(ctx), q, in.ID.String(), in.ObjectKey, in.ProviderUploadID, status, in.Existing)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if tag.RowsAffected() == 0 {
		u, e := r.S3MultipartUploadByID(ctx, in.ID)
		if e != nil {
			return u, e
		}
		if u.ObjectKey != in.ObjectKey || u.Existing != in.Existing || (!in.Existing && u.ProviderUploadID != in.ProviderUploadID) {
			return manageddata.S3MultipartUpload{}, ErrConflict
		}
		return u, nil
	}
	return r.S3MultipartUploadByID(ctx, in.ID)
}
func (r *Repository) ReserveS3MultipartPart(ctx context.Context, part manageddata.S3MultipartPart) (manageddata.S3MultipartPart, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	if part.MultipartUploadID == "" || part.PartNumber < 1 || part.PartNumber > 10000 || part.SizeBytes <= 0 {
		return manageddata.S3MultipartPart{}, ErrInvalid
	}
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.multipart_part(multipart_id,part_number,size_bytes,sha256) VALUES($1,$2,$3,$4) ON CONFLICT(multipart_id,part_number) DO NOTHING`, part.MultipartUploadID.String(), part.PartNumber, part.SizeBytes, part.SHA256)
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	var out manageddata.S3MultipartPart
	var storedID string
	err = db.QueryRow(contextOrBackground(ctx), `SELECT multipart_id,part_number,size_bytes,sha256 FROM managed_data.multipart_part WHERE multipart_id=$1 AND part_number=$2`, part.MultipartUploadID.String(), part.PartNumber).Scan(&storedID, &out.PartNumber, &out.SizeBytes, &out.SHA256)
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	out.MultipartUploadID = part.MultipartUploadID
	out.PartNumber = part.PartNumber
	if out.SizeBytes != part.SizeBytes || out.SHA256 != part.SHA256 {
		return manageddata.S3MultipartPart{}, ErrConflict
	}
	return out, nil
}
func (r *Repository) ListS3MultipartParts(ctx context.Context, id manageddata.MultipartUploadID) ([]manageddata.S3MultipartPart, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(contextOrBackground(ctx), `SELECT multipart_id,part_number,size_bytes,sha256 FROM managed_data.multipart_part WHERE multipart_id=$1 ORDER BY part_number`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.S3MultipartPart{}
	for rows.Next() {
		var p manageddata.S3MultipartPart
		if err := rows.Scan(new(string), &p.PartNumber, &p.SizeBytes, &p.SHA256); err != nil {
			return nil, err
		}
		p.MultipartUploadID = id
		out = append(out, p)
	}
	return out, rows.Err()
}
func (r *Repository) BeginS3MultipartCompletion(ctx context.Context, in manageddata.BeginS3MultipartCompletionInput) (manageddata.S3MultipartCompletion, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	if in.ID == "" || in.IdempotencyIdentity == "" || len(in.RequestHash) != 64 {
		return manageddata.S3MultipartCompletion{}, ErrInvalid
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.multipart_upload SET status='completing',completion_identity=$2,completion_request_hash=$3,updated_at=clock_timestamp() WHERE multipart_id=$1 AND status='open'`, in.ID.String(), in.IdempotencyIdentity, in.RequestHash)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	u, err := r.S3MultipartUploadByID(ctx, in.ID)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	if tag.RowsAffected() == 0 {
		if u.Status != "completing" || u.CompletionIdentity != in.IdempotencyIdentity || u.CompletionRequestHash != in.RequestHash {
			return manageddata.S3MultipartCompletion{}, ErrConflict
		}
	}
	parts, err := r.ListS3MultipartParts(ctx, in.ID)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	return manageddata.S3MultipartCompletion{Upload: u, Parts: parts, Execute: tag.RowsAffected() == 1}, nil
}
func (r *Repository) FinishS3MultipartCompletion(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	return r.finishMultipart(ctx, id, "completing", "completed")
}
func (r *Repository) BeginS3MultipartAbort(ctx context.Context, in manageddata.BeginS3MultipartAbortInput) (manageddata.S3MultipartAbort, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartAbort{}, err
	}
	if in.ID == "" || in.IdempotencyIdentity == "" {
		return manageddata.S3MultipartAbort{}, ErrInvalid
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.multipart_upload SET status='aborting',abort_identity=$2,updated_at=clock_timestamp() WHERE multipart_id=$1 AND status IN ('creating','open','failed')`, in.ID.String(), in.IdempotencyIdentity)
	if err != nil {
		return manageddata.S3MultipartAbort{}, err
	}
	u, err := r.S3MultipartUploadByID(ctx, in.ID)
	if err != nil {
		return manageddata.S3MultipartAbort{}, err
	}
	if tag.RowsAffected() == 0 && (u.Status != "aborting" || u.AbortIdentity != in.IdempotencyIdentity) {
		return manageddata.S3MultipartAbort{}, ErrConflict
	}
	return manageddata.S3MultipartAbort{Upload: u, Execute: tag.RowsAffected() == 1}, nil
}
func (r *Repository) FinishS3MultipartAbort(ctx context.Context, id manageddata.MultipartUploadID) (manageddata.S3MultipartUpload, error) {
	return r.finishMultipart(ctx, id, "aborting", "aborted")
}
func (r *Repository) finishMultipart(ctx context.Context, id manageddata.MultipartUploadID, from, to string) (manageddata.S3MultipartUpload, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.multipart_upload SET status=$3,completed_at=CASE WHEN $3='completed' THEN clock_timestamp() ELSE completed_at END,aborted_at=CASE WHEN $3='aborted' THEN clock_timestamp() ELSE aborted_at END,updated_at=clock_timestamp() WHERE multipart_id=$1 AND status=$2`, id.String(), from, to)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if tag.RowsAffected() == 0 {
		u, e := r.S3MultipartUploadByID(ctx, id)
		if e != nil {
			return u, e
		}
		if string(u.Status) != to {
			return manageddata.S3MultipartUpload{}, ErrConflict
		}
		return u, nil
	}
	return r.S3MultipartUploadByID(ctx, id)
}
func (r *Repository) FailS3MultipartUpload(ctx context.Context, id manageddata.MultipartUploadID, msg string) (manageddata.S3MultipartUpload, error) {
	if msg == "" || len(msg) > maxErrorBytes {
		return manageddata.S3MultipartUpload{}, ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.multipart_upload SET status='failed',error=$2,updated_at=clock_timestamp() WHERE multipart_id=$1 AND status IN ('creating','open','completing')`, id.String(), msg)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if tag.RowsAffected() == 0 {
		return manageddata.S3MultipartUpload{}, ErrConflict
	}
	return r.S3MultipartUploadByID(ctx, id)
}
func (r *Repository) ListRecoverableS3MultipartUploads(ctx context.Context, updatedCutoff time.Time, limit int64) ([]manageddata.S3MultipartUpload, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	var cutoff any
	if !updatedCutoff.IsZero() {
		cutoff = updatedCutoff.UTC()
	}
	rows, err := db.Query(contextOrBackground(ctx), multipartSelectQualified+` JOIN managed_data.upload_session s ON s.upload_id=m.upload_id WHERE m.updated_at<=LEAST(COALESCE($1::timestamptz,clock_timestamp()),clock_timestamp()) AND (m.status IN ('aborting','failed','creating','completing') OR (m.status='open' AND (s.status IN ('complete','aborted','expired','failed') OR (s.status='open' AND s.expires_at<=clock_timestamp())))) ORDER BY m.updated_at,m.multipart_id LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []manageddata.S3MultipartUpload{}
	for rows.Next() {
		u, e := scanMultipart(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (r *Repository) ListS3MultipartProviderIDsByDigest(ctx context.Context, digest string) ([]string, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(contextOrBackground(ctx), `SELECT provider_upload_id FROM managed_data.multipart_upload WHERE sha256=$1 AND provider_upload_id<>'' ORDER BY provider_upload_id`, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (r *Repository) ListCreatingS3MultipartIDsByDigest(ctx context.Context, digest string) ([]string, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(contextOrBackground(ctx), `SELECT multipart_id FROM managed_data.multipart_upload WHERE sha256=$1 AND status='creating' ORDER BY multipart_id`, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (r *Repository) ClaimS3MultipartDigest(ctx context.Context, digest, owner string, until time.Time) (int64, bool, error) {
	db, err := requireDB(r)
	if err != nil {
		return 0, false, err
	}
	if len(digest) != 64 || digest != strings.ToLower(digest) || canonicalText(owner, 255) != nil || until.IsZero() {
		return 0, false, ErrInvalid
	}
	var epoch int64
	err = db.QueryRow(contextOrBackground(ctx), `INSERT INTO managed_data.multipart_digest_lease(sha256,owner_id,fencing_epoch,state,lease_until) SELECT $1,$2,1,'held',$3 WHERE $3::timestamptz>clock_timestamp() AND $3::timestamptz<=clock_timestamp()+interval '24 hours' ON CONFLICT(sha256) DO UPDATE SET owner_id=EXCLUDED.owner_id,fencing_epoch=managed_data.multipart_digest_lease.fencing_epoch+1,state='held',lease_until=EXCLUDED.lease_until WHERE managed_data.multipart_digest_lease.lease_until<=clock_timestamp() OR managed_data.multipart_digest_lease.owner_id=$2 RETURNING fencing_epoch`, digest, owner, until.UTC()).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return epoch, err == nil, err
}
func (r *Repository) RenewS3MultipartDigest(ctx context.Context, digest, owner string, epoch int64, until time.Time) (bool, error) {
	db, err := requireDB(r)
	if err != nil {
		return false, err
	}
	if len(digest) != 64 || digest != strings.ToLower(digest) || canonicalText(owner, 255) != nil || epoch < 1 || until.IsZero() {
		return false, ErrInvalid
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.multipart_digest_lease SET lease_until=$4 WHERE sha256=$1 AND owner_id=$2 AND fencing_epoch=$3 AND state='held' AND lease_until>clock_timestamp() AND $4::timestamptz>lease_until AND $4::timestamptz<=clock_timestamp()+interval '24 hours'`, digest, owner, epoch, until.UTC())
	return tag.RowsAffected() == 1, err
}
func (r *Repository) ReleaseS3MultipartDigest(ctx context.Context, digest, owner string, epoch int64) error {
	db, err := requireDB(r)
	if err != nil {
		return err
	}
	if len(digest) != 64 || digest != strings.ToLower(digest) || canonicalText(owner, 255) != nil || epoch < 1 {
		return ErrInvalid
	}
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.multipart_digest_lease SET state='released',lease_until=clock_timestamp() WHERE sha256=$1 AND owner_id=$2 AND fencing_epoch=$3 AND state='held'`, digest, owner, epoch)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	return err
}

func scanMultipart(row interface{ Scan(...any) error }) (manageddata.S3MultipartUpload, error) {
	var u manageddata.S3MultipartUpload
	var id, upload, path, sha, obj, provider, status, idemp, comp, hash, abort, errstr string
	var size int64
	var existing bool
	var created, updated time.Time
	var completed, aborted *time.Time
	err := row.Scan(&id, &upload, &path, &sha, &size, &obj, &provider, &status, &existing, &idemp, &comp, &hash, &abort, &created, &updated, &completed, &aborted, &errstr)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.ID = manageddata.MultipartUploadID(id)
	u.UploadSessionID = manageddata.UploadID(upload)
	u.LogicalPath = path
	u.SHA256 = sha
	u.SizeBytes = size
	u.ObjectKey = obj
	u.ProviderUploadID = provider
	u.Status = manageddata.S3MultipartStatus(status)
	u.Existing = existing
	u.IdempotencyIdentity = idemp
	u.CompletionIdentity = comp
	u.CompletionRequestHash = hash
	u.AbortIdentity = abort
	u.CreatedAt = formatTime(created)
	u.UpdatedAt = formatTime(updated)
	if completed != nil {
		u.CompletedAt = formatTime(*completed)
	}
	if aborted != nil {
		u.AbortedAt = formatTime(*aborted)
	}
	u.Error = errstr
	return u, nil
}
