package l3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
)

// S3Client aliases the shared provider surface so existing runtime and test
// constructors remain source-compatible while all buffering, reconciliation,
// encryption, owner fencing, and provider error handling live in the platform
// object-store implementation.
type S3Client = platformobjectstore.S3Client

// S3ObjectStore adapts the provider-neutral platform S3Store to L3's
// capability split. The platform store owns the physical bucket/prefix and
// provider protocol; this adapter only translates L3 metadata and sentinels.
type S3ObjectStore struct {
	store *platformobjectstore.S3Store
}

// NewS3ObjectStore constructs an L3 adapter using the platform S3Store's
// default SSE-S3 policy.
func NewS3ObjectStore(client S3Client, bucket, prefix, securityDomain string) (*S3ObjectStore, error) {
	return NewS3ObjectStoreWithResolvedEncryption(client, bucket, prefix, "", "", securityDomain)
}

// NewS3ObjectStoreWithEncryption is retained for callers that supply only an
// opaque reference. KMS references must be resolved before construction and
// are rejected by the shared platform store when no provider identity exists.
func NewS3ObjectStoreWithEncryption(client S3Client, bucket, prefix, encryptionKeyRef, securityDomain string) (*S3ObjectStore, error) {
	return NewS3ObjectStoreWithResolvedEncryption(client, bucket, prefix, encryptionKeyRef, "", securityDomain)
}

// NewS3ObjectStoreWithResolvedEncryption binds the admitted opaque key
// reference to a target-resolved provider identity, then delegates all S3
// policy validation to platform/objectstore.
func NewS3ObjectStoreWithResolvedEncryption(client S3Client, bucket, prefix, encryptionKeyRef, providerEncryptionKey, securityDomain string) (*S3ObjectStore, error) {
	if securityDomain == "" {
		return nil, fmt.Errorf("%w: S3 storage security domain is required", ErrInvalid)
	}
	inner, err := platformobjectstore.NewS3Store(client, platformobjectstore.S3StoreConfig{
		Bucket:                bucket,
		Prefix:                strings.Trim(strings.TrimSpace(prefix), "/"),
		StorageSecurityDomain: securityDomain,
		Encryption: platformobjectstore.S3EncryptionConfig{
			OpaqueKeyRef: encryptionKeyRef,
			ProviderKey:  providerEncryptionKey,
		},
	})
	if err != nil {
		return nil, mapPlatformError(err)
	}
	return &S3ObjectStore{store: inner}, nil
}

func (s *S3ObjectStore) PutImmutable(ctx context.Context, key string, body io.Reader, metadata ObjectMetadata) (ObjectInfo, error) {
	if s == nil || s.store == nil {
		return ObjectInfo{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	canonical, err := canonicalMetadata(metadata.Metadata)
	if err != nil {
		return ObjectInfo{}, err
	}
	metadataDigest := digestBytes(canonical)
	if metadata.MetadataDigest != "" && metadata.MetadataDigest != metadataDigest {
		return ObjectInfo{}, fmt.Errorf("%w: metadata digest mismatch", ErrInvalid)
	}
	if metadata.Digest == "" || metadata.Size < 0 {
		return ObjectInfo{}, fmt.Errorf("%w: body digest and size evidence are required", ErrInvalid)
	}
	info, putErr := s.store.PutImmutable(ctx, key, body, platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: metadata.SecurityDomain,
		Digest:                metadata.Digest,
		SizeBytes:             metadata.Size,
		MetadataDigest:        metadataDigest,
	})
	if putErr != nil {
		return ObjectInfo{}, mapPlatformError(putErr)
	}
	return fromPlatformInfo(info, canonical), nil
}

func (s *S3ObjectStore) Open(ctx context.Context, key string) (Object, error) {
	if s == nil || s.store == nil {
		return Object{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	object, err := s.store.Open(ctx, key)
	if err != nil {
		return Object{}, mapPlatformError(err)
	}
	return Object{Body: object.Body, Info: fromPlatformInfo(object.Info, nil)}, nil
}

func (s *S3ObjectStore) DeleteExact(ctx context.Context, object ObjectInfo) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	return mapPlatformError(s.store.DeleteExact(ctx, platformobjectstore.ObjectInfo{
		Key:                   object.Key,
		StorageSecurityDomain: object.SecurityDomain,
		VersionID:             object.VersionID,
		ETag:                  object.ETag,
	}))
}

func (s *S3ObjectStore) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if s == nil || s.store == nil {
		return nil, "", fmt.Errorf("%w: nil store", ErrInvalid)
	}
	// L3 uses a trailing slash in its collector boundary so prefix checks cannot
	// admit a sibling domain with the same textual prefix. The shared platform
	// store models prefixes as slash-free keys and adds that boundary itself.
	objects, next, err := s.store.List(ctx, strings.TrimSuffix(prefix, "/"), cursor, limit)
	if err != nil {
		return nil, "", mapPlatformError(err)
	}
	out := make([]ObjectInfo, 0, len(objects))
	for _, object := range objects {
		out = append(out, fromPlatformInfo(object, nil))
	}
	return out, next, nil
}

func fromPlatformInfo(info platformobjectstore.ObjectInfo, metadata json.RawMessage) ObjectInfo {
	return ObjectInfo{
		Key:            info.Key,
		SecurityDomain: info.StorageSecurityDomain,
		Digest:         info.Digest,
		Size:           info.SizeBytes,
		Metadata:       append(json.RawMessage(nil), metadata...),
		MetadataDigest: info.MetadataDigest,
		VersionID:      info.VersionID,
		ETag:           info.ETag,
		CreatedAt:      info.CreatedAt,
	}
}

func mapPlatformError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, platformobjectstore.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, platformobjectstore.ErrConflict):
		return fmt.Errorf("%w: %v", ErrObjectExists, err)
	case errors.Is(err, platformobjectstore.ErrAmbiguous):
		return fmt.Errorf("%w: %v", ErrObjectAmbiguous, err)
	case errors.Is(err, platformobjectstore.ErrCorrupt):
		return fmt.Errorf("%w: %v", ErrObjectCorrupt, err)
	case errors.Is(err, platformobjectstore.ErrDomainMismatch):
		return fmt.Errorf("%w: %v", ErrSecurityDomain, err)
	default:
		return err
	}
}
