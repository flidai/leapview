package gcstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment/gc"
)

type conditionalS3Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
}

type S3Client interface {
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
}

type S3Store struct {
	client         S3Client
	bucket, prefix string
}

const s3OwnershipMarkerKey = ".leapview-pool-owner.json"
const s3DeletionLeaseKey = ".leapview-pool-gc-lease.json"

func (s *S3Store) AcquireNamespaceOwnership(ctx context.Context, claim physicalpool.OwnershipClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	writer, ok := s.client.(conditionalS3Client)
	if !ok {
		return fmt.Errorf("S3 client does not support conditional ownership markers")
	}
	encoded, err := json.Marshal(markerFor(claim))
	if err != nil {
		return err
	}
	key, err := s.key(s3OwnershipMarkerKey)
	if err != nil {
		return err
	}
	_, err = writer.PutObject(ctx, &awss3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(encoded), IfNoneMatch: aws.String("*"), Metadata: map[string]string{
		"leapview-pool-id": string(claim.PoolID), "leapview-compatibility-digest": claim.CompatibilityDigest,
		"leapview-evidence-digest": claim.EvidenceDigest, "leapview-owner-id": claim.OwnerID,
	}})
	if err == nil {
		return nil
	}
	return s.VerifyNamespaceOwnership(ctx, claim)
}

func (s *S3Store) VerifyNamespaceOwnership(ctx context.Context, claim physicalpool.OwnershipClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	key, err := s.key(s3OwnershipMarkerKey)
	if err != nil {
		return err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return fmt.Errorf("verify physical-pool ownership marker: %w", err)
	}
	marker := ownershipMarker{PoolID: physicalpool.PoolID(head.Metadata["leapview-pool-id"]), CompatibilityDigest: head.Metadata["leapview-compatibility-digest"], EvidenceDigest: head.Metadata["leapview-evidence-digest"], OwnerID: head.Metadata["leapview-owner-id"]}
	want := markerFor(claim)
	// Admission evidence may change when a compatible runtime/extension is
	// re-admitted. Namespace ownership remains stable across those upgrades;
	// only the pool identity and durable metadata-database owner fence it.
	if marker.PoolID != want.PoolID || marker.OwnerID != want.OwnerID {
		return physicalpool.ErrOwnershipConflict
	}
	return nil
}

func (s *S3Store) AcquireNamespaceDeletionLease(ctx context.Context, ownerID string, ttl time.Duration) (string, error) {
	if ownerID == "" || ttl <= 0 {
		return "", physicalpool.ErrDeletionLeaseConflict
	}
	writer, ok := s.client.(conditionalS3Client)
	if !ok {
		return "", fmt.Errorf("S3 client does not support conditional deletion leases")
	}
	tokenBytes := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", ownerID, time.Now().UnixNano())))
	lease := deletionLease{OwnerID: ownerID, Token: hex.EncodeToString(tokenBytes[:]), ExpiresAt: time.Now().UTC().Add(ttl)}
	encoded, err := json.Marshal(lease)
	if err != nil {
		return "", err
	}
	key, err := s.key(s3DeletionLeaseKey)
	if err != nil {
		return "", err
	}
	metadata := map[string]string{"leapview-lease-owner": lease.OwnerID, "leapview-lease-token": lease.Token, "leapview-lease-expires": lease.ExpiresAt.Format(time.RFC3339Nano)}
	_, putErr := writer.PutObject(ctx, &awss3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(encoded), IfNoneMatch: aws.String("*"), Metadata: metadata})
	if putErr == nil {
		return lease.Token, nil
	}
	// A held lease can only be replaced after its expiry, and only with the
	// exact provider ETag observed by this caller.
	head, headErr := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if headErr != nil || head.Metadata["leapview-lease-expires"] == "" {
		return "", physicalpool.ErrDeletionLeaseConflict
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, head.Metadata["leapview-lease-expires"])
	if parseErr != nil || expires.After(time.Now().UTC()) || head.ETag == nil || *head.ETag == "" {
		return "", physicalpool.ErrDeletionLeaseConflict
	}
	input := &awss3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(encoded), IfMatch: head.ETag, Metadata: metadata}
	if _, err := writer.PutObject(ctx, input); err != nil {
		return "", physicalpool.ErrDeletionLeaseConflict
	}
	return lease.Token, nil
}

func (s *S3Store) VerifyNamespaceDeletionLease(ctx context.Context, ownerID, token string) error {
	if ownerID == "" || token == "" {
		return physicalpool.ErrDeletionLeaseConflict
	}
	key, err := s.key(s3DeletionLeaseKey)
	if err != nil {
		return err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return physicalpool.ErrDeletionLeaseConflict
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, head.Metadata["leapview-lease-expires"])
	if parseErr != nil || head.Metadata["leapview-lease-owner"] != ownerID || head.Metadata["leapview-lease-token"] != token || !expires.After(time.Now().UTC()) {
		return physicalpool.ErrDeletionLeaseConflict
	}
	return nil
}

func (s *S3Store) ReleaseNamespaceDeletionLease(ctx context.Context, ownerID, token string) error {
	if err := s.VerifyNamespaceDeletionLease(ctx, ownerID, token); err != nil {
		return nil
	}
	key, err := s.key(s3DeletionLeaseKey)
	if err != nil {
		return err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil
	}
	input := &awss3.DeleteObjectInput{Bucket: &s.bucket, Key: &key}
	if head.ETag == nil || *head.ETag == "" {
		return physicalpool.ErrDeletionLeaseConflict
	}
	input.IfMatch = head.ETag
	_, err = s.client.DeleteObject(ctx, input)
	return err
}

func NewS3(client S3Client, bucket, prefix string) (*S3Store, error) {
	if client == nil || strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("S3 GC client and bucket are required")
	}
	prefix = strings.Trim(prefix, "/")
	if strings.ContainsAny(prefix, "\x00\r\n") {
		return nil, fmt.Errorf("S3 GC prefix is invalid")
	}
	return &S3Store{client: client, bucket: bucket, prefix: prefix}, nil
}
func (s *S3Store) key(key string) (string, error) {
	key = strings.Trim(strings.ReplaceAll(key, "\\", "/"), "/")
	if key == "" || strings.Contains(key, "..") {
		return "", gc.ErrObjectOutsidePool
	}
	if s.prefix == "" {
		return key, nil
	}
	return s.prefix + "/" + key, nil
}
func (s *S3Store) relative(key string) (string, error) {
	if s.prefix == "" {
		return key, nil
	}
	prefix := s.prefix + "/"
	if !strings.HasPrefix(key, prefix) {
		return "", gc.ErrObjectOutsidePool
	}
	return strings.TrimPrefix(key, prefix), nil
}

func (s *S3Store) Open(ctx context.Context, key string) (gc.CatalogObject, error) {
	full, err := s.key(key)
	if err != nil {
		return gc.CatalogObject{}, err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &full})
	if err != nil {
		return gc.CatalogObject{}, err
	}
	getInput := &awss3.GetObjectInput{Bucket: &s.bucket, Key: &full}
	if head.VersionId != nil && *head.VersionId != "" {
		getInput.VersionId = head.VersionId
	} else if head.ETag != nil && *head.ETag != "" {
		getInput.IfMatch = head.ETag
	} else {
		return gc.CatalogObject{}, fmt.Errorf("S3 object has no immutable provider version or ETag")
	}
	out, err := s.client.GetObject(ctx, getInput)
	if err != nil {
		return gc.CatalogObject{}, err
	}
	metadata := map[string]string{}
	for k, v := range head.Metadata {
		metadata[k] = v
	}
	if head.VersionId != nil {
		metadata["version"] = *head.VersionId
	}
	size := int64(0)
	if head.ContentLength != nil {
		size = *head.ContentLength
	}
	return gc.CatalogObject{Body: out.Body, Size: size, Metadata: metadata}, nil
}
func (s *S3Store) Stat(ctx context.Context, _ string, key string) (gc.Object, error) {
	full, err := s.key(key)
	if err != nil {
		return gc.Object{}, err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.bucket, Key: &full})
	if err != nil {
		return gc.Object{}, err
	}
	relative, _ := s.relative(full)
	digest := head.Metadata["sha256"]
	// Provider metadata is an optimization, not a coverage requirement.  Read
	// the exact object bytes when absent (and verify supplied metadata against
	// those bytes) so MinIO/S3 objects without custom metadata remain eligible
	// for safe conditional deletion.
	{
		getInput := &awss3.GetObjectInput{Bucket: &s.bucket, Key: &full}
		if head.VersionId != nil && *head.VersionId != "" {
			getInput.VersionId = head.VersionId
		} else if head.ETag != nil && *head.ETag != "" {
			getInput.IfMatch = head.ETag
		} else {
			return gc.Object{}, fmt.Errorf("S3 object has no immutable provider version or ETag")
		}
		out, getErr := s.client.GetObject(ctx, getInput)
		if getErr != nil {
			return gc.Object{}, getErr
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, out.Body)
		closeErr := out.Body.Close()
		if readErr != nil {
			return gc.Object{}, readErr
		}
		if closeErr != nil {
			return gc.Object{}, closeErr
		}
		computed := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if digest != "" && digest != computed {
			return gc.Object{}, fmt.Errorf("S3 object digest metadata mismatch")
		}
		digest = computed
		if head.ContentLength != nil && n != *head.ContentLength {
			return gc.Object{}, fmt.Errorf("S3 object size changed during digest read")
		}
	}
	version := ""
	if head.VersionId != nil {
		version = *head.VersionId
	}
	if version == "" && head.ETag != nil && *head.ETag != "" {
		version = "etag:" + *head.ETag
	}
	modified := time.Time{}
	if head.LastModified != nil {
		modified = head.LastModified.UTC()
	}
	size := int64(0)
	if head.ContentLength != nil {
		size = *head.ContentLength
	}
	return gc.Object{Key: relative, Digest: digest, Version: version, Size: size, CreatedAt: modified, LastModified: modified, Metadata: head.Metadata}, nil
}
func (s *S3Store) ListPoolObjects(ctx context.Context, _ string) ([]gc.Object, error) {
	var result []gc.Object
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &s.prefix, ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, entry := range out.Contents {
			if entry.Key == nil {
				continue
			}
			key, err := s.relative(*entry.Key)
			if err != nil {
				return nil, err
			}
			if key == s3OwnershipMarkerKey || key == s3DeletionLeaseKey {
				continue
			}
			object, err := s.Stat(ctx, "", key)
			if err != nil {
				return nil, err
			}
			result = append(result, object)
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
		if token == nil {
			return nil, fmt.Errorf("S3 listing pagination is incomplete")
		}
	}
	return result, nil
}
func (s *S3Store) DeleteConditional(ctx context.Context, req gc.DeleteRequest) (gc.DeleteResponse, error) {
	object, err := s.Stat(ctx, req.PhysicalPoolID, req.Key)
	if err != nil {
		return gc.DeleteResponse{}, err
	}
	if object.Digest != req.Digest || req.Version == "" || object.Version != req.Version {
		return gc.DeleteResponse{}, fmt.Errorf("S3 conditional identity mismatch")
	}
	full, _ := s.key(req.Key)
	deleteInput := &awss3.DeleteObjectInput{Bucket: &s.bucket, Key: &full}
	if strings.HasPrefix(req.Version, "etag:") {
		etag := strings.TrimPrefix(req.Version, "etag:")
		if etag == "" {
			return gc.DeleteResponse{}, fmt.Errorf("S3 conditional identity has empty ETag")
		}
		deleteInput.IfMatch = &etag
	} else {
		deleteInput.VersionId = &req.Version
	}
	_, err = s.client.DeleteObject(ctx, deleteInput)
	if err != nil {
		return gc.DeleteResponse{}, err
	}
	return gc.DeleteResponse{Deleted: true}, nil
}
