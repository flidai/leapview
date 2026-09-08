// Package storage defines managed-data blob persistence contracts.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrInvalid   = errors.New("invalid storage request")
	ErrNotFound  = errors.New("blob not found")
	ErrIntegrity = errors.New("blob integrity check failed")
	ErrOffset    = errors.New("upload offset mismatch")
	ErrBackend   = errors.New("storage backend operation failed")
)

// Blob identifies immutable content. SHA256 is lowercase hexadecimal without a prefix.
type Blob struct {
	SHA256 string
	Size   int64
	URI    string
}

type MultipartUpload struct {
	UploadID string
	SHA256   string
	Size     int64
	Key      string
	Existing bool
}

type MultipartPartRequest struct {
	Number int32
	Size   int64
	SHA256 string
}

type SignedMultipartPart struct {
	Number  int32
	URL     string
	Headers map[string][]string
}

type CompletedMultipartPart struct {
	Number int32
	ETag   string
	SHA256 string
}

// BlobMetadata describes an immutable backend object without reading its body.
type BlobMetadata struct {
	SHA256       string
	Size         int64
	LastModified time.Time
}

// BlobInventory exposes bounded administrative operations used by garbage
// collection. Implementations must enumerate only canonical managed-data blobs
// in nondecreasing SHA256 order (adjacent duplicate metadata must match) and make
// deletion idempotent. The ordering is part of the bounded GC contract: the
// collector can reject duplicate metadata without retaining a history-sized
// seen set.
type BlobInventory interface {
	WalkBlobs(ctx context.Context, visit func(BlobMetadata) error) error
	DeleteBlobs(ctx context.Context, sha256s []string) error
}

// BlobStore persists and verifies immutable, content-addressed blobs.
type BlobStore interface {
	Put(ctx context.Context, expected Blob, content io.Reader) (Blob, error)
	Stat(ctx context.Context, sha256 string) (Blob, error)
	Open(ctx context.Context, sha256 string) (io.ReadCloser, error)
}
