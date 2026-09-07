package postgres

import (
	"errors"

	"github.com/flidai/leapview/internal/manageddata"
	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func collectionFromValues(id, project, connection, name, description, status, createdBy string, createdAt, updatedAt, archivedAt pgtype.Timestamptz) manageddata.Collection {
	c := manageddata.Collection{ID: projectgraph.ResourceID(id), ProjectID: projectgraph.ResourceID(project), ConnectionID: projectgraph.ResourceID(connection), Name: name, Description: description, Status: manageddata.CollectionStatus(status), CreatedBy: createdBy, CreatedAt: formatTime(createdAt.Time), UpdatedAt: formatTime(updatedAt.Time)}
	if archivedAt.Valid {
		c.ArchivedAt = formatTime(archivedAt.Time)
	}
	return c
}

func uploadFromValues(id, collection, base, revision, status, manifest, backend, prefix, createdBy string, expectedCount, expectedSize, uploadedCount, uploadedSize int64, createdAt, updatedAt, expiresAt, completedAt pgtype.Timestamptz, errText string) manageddata.UploadSession {
	s := manageddata.UploadSession{ID: manageddata.UploadID(id), CollectionID: projectgraph.ResourceID(collection), BaseRevisionID: manageddata.RevisionID(base), RevisionID: manageddata.RevisionID(revision), Status: manageddata.UploadStatus(status), ManifestJSON: manifest, ExpectedFileCount: expectedCount, ExpectedSizeBytes: expectedSize, UploadedFileCount: uploadedCount, UploadedSizeBytes: uploadedSize, StorageBackend: backend, StagingPrefix: prefix, CreatedBy: createdBy, CreatedAt: formatTime(createdAt.Time), UpdatedAt: formatTime(updatedAt.Time), ExpiresAt: formatTime(expiresAt.Time), Error: errText}
	if completedAt.Valid {
		s.CompletedAt = formatTime(completedAt.Time)
	}
	return s
}

func revisionFromValues(id, collection, digest, status, manifest, createdBy, errText string, sequence, fileCount, size int64, createdAt, readyAt pgtype.Timestamptz) manageddata.Revision {
	v := manageddata.Revision{ID: manageddata.RevisionID(id), CollectionID: projectgraph.ResourceID(collection), Sequence: sequence, Digest: digest, Status: manageddata.RevisionStatus(status), ManifestJSON: manifest, FileCount: fileCount, SizeBytes: size, CreatedBy: createdBy, CreatedAt: formatTime(createdAt.Time), Error: errText}
	if readyAt.Valid {
		v.ReadyAt = formatTime(readyAt.Time)
	}
	return v
}

func multipartFromRow(row manageddb.ManagedDataMultipartUpload) manageddata.S3MultipartUpload {
	u := manageddata.S3MultipartUpload{ID: manageddata.MultipartUploadID(row.MultipartID), UploadSessionID: manageddata.UploadID(row.UploadID), LogicalPath: row.LogicalPath, SHA256: row.Sha256, SizeBytes: row.SizeBytes, ObjectKey: row.ObjectKey, ProviderUploadID: row.ProviderUploadID, Status: manageddata.S3MultipartStatus(row.Status), Existing: row.Existing, IdempotencyIdentity: row.IdempotencyIdentity, CompletionIdentity: row.CompletionIdentity, CompletionRequestHash: row.CompletionRequestHash, AbortIdentity: row.AbortIdentity, CreatedAt: formatTime(row.CreatedAt.Time), UpdatedAt: formatTime(row.UpdatedAt.Time), Error: row.Error}
	if row.CompletedAt.Valid {
		u.CompletedAt = formatTime(row.CompletedAt.Time)
	}
	if row.AbortedAt.Valid {
		u.AbortedAt = formatTime(row.AbortedAt.Time)
	}
	return u
}

func scanNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
