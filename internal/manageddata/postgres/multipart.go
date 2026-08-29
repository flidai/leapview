package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

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
	err = manageddb.New(db).InsertMultipartUpload(contextOrBackground(ctx), manageddb.InsertMultipartUploadParams{MultipartID: id, UploadID: in.UploadSessionID.String(), LogicalPath: in.LogicalPath, Sha256: in.SHA256, SizeBytes: in.SizeBytes, IdempotencyIdentity: in.IdempotencyIdentity})
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	u, err := r.S3MultipartUploadByID(ctx, manageddata.MultipartUploadID(id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			var existingID string
			existingID, lookupErr := manageddb.New(db).GetMultipartByIdentity(contextOrBackground(ctx), manageddb.GetMultipartByIdentityParams{UploadID: in.UploadSessionID.String(), IdempotencyIdentity: in.IdempotencyIdentity})
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
	row, err := manageddb.New(db).GetMultipartByID(contextOrBackground(ctx), id.String())
	if err != nil {
		return manageddata.S3MultipartUpload{}, scanNotFound(err)
	}
	return multipartFromRow(row), nil
}
func (r *Repository) InitializeS3MultipartUpload(ctx context.Context, in manageddata.InitializeS3MultipartUploadInput) (manageddata.S3MultipartUpload, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartUpload{}, err
	}
	if in.ID == "" || in.ObjectKey == "" || (!in.Existing && in.ProviderUploadID == "") {
		return manageddata.S3MultipartUpload{}, ErrInvalid
	}
	tag, err := manageddb.New(db).InitializeMultipart(contextOrBackground(ctx), manageddb.InitializeMultipartParams{MultipartID: in.ID.String(), ObjectKey: in.ObjectKey, ProviderUploadID: in.ProviderUploadID, Existing: in.Existing})
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
	err = manageddb.New(db).InsertMultipartPart(contextOrBackground(ctx), manageddb.InsertMultipartPartParams{MultipartID: part.MultipartUploadID.String(), PartNumber: int32(part.PartNumber), SizeBytes: part.SizeBytes, Sha256: part.SHA256})
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	row, err := manageddb.New(db).GetMultipartPart(contextOrBackground(ctx), manageddb.GetMultipartPartParams{MultipartID: part.MultipartUploadID.String(), PartNumber: int32(part.PartNumber)})
	if err != nil {
		return manageddata.S3MultipartPart{}, err
	}
	out := manageddata.S3MultipartPart{MultipartUploadID: part.MultipartUploadID, PartNumber: row.PartNumber, SizeBytes: row.SizeBytes, SHA256: row.Sha256}
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
	rows, err := manageddb.New(db).ListMultipartParts(contextOrBackground(ctx), id.String())
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.S3MultipartPart, 0, len(rows))
	for _, row := range rows {
		out = append(out, manageddata.S3MultipartPart{MultipartUploadID: id, PartNumber: row.PartNumber, SizeBytes: row.SizeBytes, SHA256: row.Sha256})
	}
	return out, nil
}
func (r *Repository) BeginS3MultipartCompletion(ctx context.Context, in manageddata.BeginS3MultipartCompletionInput) (manageddata.S3MultipartCompletion, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.S3MultipartCompletion{}, err
	}
	if in.ID == "" || in.IdempotencyIdentity == "" || len(in.RequestHash) != 64 {
		return manageddata.S3MultipartCompletion{}, ErrInvalid
	}
	tag, err := manageddb.New(db).BeginMultipartCompletion(contextOrBackground(ctx), manageddb.BeginMultipartCompletionParams{MultipartID: in.ID.String(), CompletionIdentity: in.IdempotencyIdentity, CompletionRequestHash: in.RequestHash})
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
	tag, err := manageddb.New(db).BeginMultipartAbort(contextOrBackground(ctx), manageddb.BeginMultipartAbortParams{MultipartID: in.ID.String(), AbortIdentity: in.IdempotencyIdentity})
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
	tag, err := manageddb.New(db).FinishMultipart(contextOrBackground(ctx), manageddb.FinishMultipartParams{MultipartID: id.String(), FromStatus: from, ToStatus: to})
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
	tag, err := manageddb.New(db).FailMultipart(contextOrBackground(ctx), manageddb.FailMultipartParams{MultipartID: id.String(), Error: msg})
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
	cutoff := pgtype.Timestamptz{}
	if !updatedCutoff.IsZero() {
		cutoff = pgtype.Timestamptz{Time: updatedCutoff.UTC(), Valid: true}
	}
	rows, err := manageddb.New(db).ListRecoverableMultipart(contextOrBackground(ctx), manageddb.ListRecoverableMultipartParams{Cutoff: cutoff, PLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]manageddata.S3MultipartUpload, 0, len(rows))
	for _, row := range rows {
		out = append(out, multipartFromRow(row))
	}
	return out, nil
}
func (r *Repository) ListS3MultipartProviderIDsByDigest(ctx context.Context, digest string) ([]string, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := manageddb.New(db).ListProviderIDsByDigest(contextOrBackground(ctx), digest)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
func (r *Repository) ListCreatingS3MultipartIDsByDigest(ctx context.Context, digest string) ([]string, error) {
	db, err := requireDB(r)
	if err != nil {
		return nil, err
	}
	rows, err := manageddb.New(db).ListCreatingIDsByDigest(contextOrBackground(ctx), digest)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
func (r *Repository) ClaimS3MultipartDigest(ctx context.Context, digest, owner string, until time.Time) (int64, bool, error) {
	db, err := requireDB(r)
	if err != nil {
		return 0, false, err
	}
	if len(digest) != 64 || digest != strings.ToLower(digest) || canonicalText(owner, 255) != nil || until.IsZero() {
		return 0, false, ErrInvalid
	}
	epoch, err := manageddb.New(db).ClaimDigestLease(contextOrBackground(ctx), manageddb.ClaimDigestLeaseParams{Sha256: digest, OwnerID: owner, LeaseUntil: pgtype.Timestamptz{Time: until.UTC(), Valid: true}})
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
	tag, err := manageddb.New(db).RenewDigestLease(contextOrBackground(ctx), manageddb.RenewDigestLeaseParams{Sha256: digest, OwnerID: owner, FencingEpoch: epoch, LeaseUntil: pgtype.Timestamptz{Time: until.UTC(), Valid: true}})
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
	tag, err := manageddb.New(db).ReleaseDigestLease(contextOrBackground(ctx), manageddb.ReleaseDigestLeaseParams{Sha256: digest, OwnerID: owner, FencingEpoch: epoch})
	if err == nil && tag.RowsAffected() != 1 {
		return ErrStaleFence
	}
	return err
}
