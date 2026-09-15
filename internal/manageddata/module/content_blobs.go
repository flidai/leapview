package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Content assets are bounded by the avatar and product-logo upload limits.
// OpenContent validates the complete body before returning it so callers can
// safely attach a trusted digest ETag before writing any bytes to a response.
const maxContentBlobBytes int64 = 5 << 20

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
	blob := storage.Blob{SHA256: expected.SHA256, Size: expected.Size}
	if err := storage.ValidateBlob(blob); err != nil {
		return ContentBlob{}, err
	}
	if expected.Size > maxContentBlobBytes {
		return ContentBlob{}, fmt.Errorf("%w: content blob exceeds %d bytes", storage.ErrInvalid, maxContentBlobBytes)
	}
	stored, err := s.blobs.Put(ctx, blob, body)
	return ContentBlob{SHA256: stored.SHA256, Size: stored.Size}, err
}

func (s contentBlobStore) OpenContent(ctx context.Context, digest string) (io.ReadCloser, error) {
	reader, err := s.blobs.Open(ctx, digest)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrContentBlobNotFound
	}
	if err != nil {
		return nil, err
	}
	return verifyContentBlob(ctx, digest, reader)
}

func verifyContentBlob(ctx context.Context, digest string, reader io.ReadCloser) (io.ReadCloser, error) {
	if reader == nil {
		return nil, fmt.Errorf("%w: content blob reader is missing", storage.ErrIntegrity)
	}
	hash := sha256.New()
	var body bytes.Buffer
	limited := io.LimitReader(&contentBlobContextReader{ctx: ctx, reader: reader}, maxContentBlobBytes+1)
	written, readErr := io.Copy(io.MultiWriter(&body, hash), limited)
	closeErr := reader.Close()
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("read content blob: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close content blob: %w", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if written > maxContentBlobBytes || actualDigest != digest {
		return nil, fmt.Errorf("%w: content blob does not match its content address", storage.ErrIntegrity)
	}
	return io.NopCloser(bytes.NewReader(body.Bytes())), nil
}

type contentBlobContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contentBlobContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

var ErrContentBlobNotFound = errors.New("content blob not found")
