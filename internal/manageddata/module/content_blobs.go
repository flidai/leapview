package module

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/manageddata/storage"
	managedfilesystem "github.com/flidai/leapview/internal/manageddata/storage/filesystem"
)

// ContentBlobStore is the module-level port for small content-addressed assets
// that share deployment-managed local/S3 connectivity without sharing keys or
// retention with managed data.
type ContentBlobStore interface {
	PutContent(context.Context, ContentBlob, io.Reader) (ContentBlob, error)
	OpenContent(context.Context, string) (io.ReadCloser, error)
}

type ContentBlob struct {
	SHA256 string
	Size   int64
}

func NewContentBlobStore(ctx context.Context, config ProductConfig, localDirectory, s3Prefix string) (ContentBlobStore, error) {
	var blobs storage.BlobStore
	var err error
	switch strings.TrimSpace(config.Backend) {
	case "", "local":
		blobs, err = managedfilesystem.New(filepath.Clean(localDirectory))
	case "s3":
		config.S3Prefix = strings.Trim(strings.TrimSpace(s3Prefix), "/")
		// Content assets share S3 connectivity but are not managed-data
		// revision members. Keep the managed-data observation profile and
		// recorder scoped to the revision store.
		blobs, err = newS3BlobStore(ctx, config, config.S3Prefix)
	default:
		return nil, fmt.Errorf("content-blob backend must be local or s3")
	}
	if err != nil {
		return nil, err
	}
	return contentBlobStore{blobs: blobs}, nil
}

type contentBlobStore struct{ blobs storage.BlobStore }

func (s contentBlobStore) PutContent(ctx context.Context, expected ContentBlob, body io.Reader) (ContentBlob, error) {
	stored, err := s.blobs.Put(ctx, storage.Blob{SHA256: expected.SHA256, Size: expected.Size}, body)
	return ContentBlob{SHA256: stored.SHA256, Size: stored.Size}, err
}

func (s contentBlobStore) OpenContent(ctx context.Context, digest string) (io.ReadCloser, error) {
	reader, err := s.blobs.Open(ctx, digest)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrContentBlobNotFound
	}
	return reader, err
}

var ErrContentBlobNotFound = errors.New("content blob not found")
