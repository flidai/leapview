package gcstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

type fakeS3 struct {
	body            []byte
	digest, version string
	etag            string
	getIfMatch      string
	deleteIfMatch   string
	deleted         bool
	markerMetadata  map[string]string
	leaseMetadata   map[string]string
	leaseETag       string
	leaseHeadErrors []error
	putEncryption   types.ServerSideEncryption
	putKMSKey       string
}

func (f *fakeS3) HeadObject(_ context.Context, input *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	if f.deleted {
		return nil, errors.New("not found")
	}
	metadata := map[string]string{}
	if input.Key != nil && strings.HasSuffix(*input.Key, s3DeletionLeaseKey) {
		if len(f.leaseHeadErrors) > 0 {
			err := f.leaseHeadErrors[0]
			f.leaseHeadErrors = f.leaseHeadErrors[1:]
			if err != nil {
				return nil, err
			}
		}
		if f.leaseMetadata == nil {
			return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "lease marker does not exist"}
		}
		for k, v := range f.leaseMetadata {
			metadata[k] = v
		}
		return &awss3.HeadObjectOutput{ContentLength: aws.Int64(0), LastModified: aws.Time(time.Now().UTC()), Metadata: metadata, ETag: aws.String(f.leaseETag)}, nil
	}
	for k, v := range f.markerMetadata {
		metadata[k] = v
	}
	output := &awss3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(f.body))), LastModified: aws.Time(time.Now().UTC()), Metadata: metadata}
	if f.version != "" {
		output.VersionId = aws.String(f.version)
	}
	if f.digest != "" {
		output.Metadata["sha256"] = f.digest
	}
	if f.etag != "" {
		output.ETag = aws.String(f.etag)
	}
	return output, nil
}

func (f *fakeS3) PutObject(_ context.Context, input *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	f.putEncryption = input.ServerSideEncryption
	f.putKMSKey = aws.ToString(input.SSEKMSKeyId)
	if input.Key != nil && strings.HasSuffix(*input.Key, s3DeletionLeaseKey) {
		if input.IfNoneMatch != nil && f.leaseMetadata != nil {
			return nil, errors.New("precondition failed")
		}
		if input.IfMatch != nil && *input.IfMatch != f.leaseETag {
			return nil, errors.New("precondition failed")
		}
		f.leaseMetadata = map[string]string{}
		for k, v := range input.Metadata {
			f.leaseMetadata[k] = v
		}
		f.leaseETag = "lease-v1"
		return &awss3.PutObjectOutput{}, nil
	}
	if f.markerMetadata != nil {
		return nil, errors.New("precondition failed")
	}
	f.markerMetadata = map[string]string{}
	for k, v := range input.Metadata {
		f.markerMetadata[k] = v
	}
	return &awss3.PutObjectOutput{}, nil
}

func TestS3NamespaceMarkersUseResolvedKMSKey(t *testing.T) {
	f := &fakeS3{}
	store, err := NewS3WithEncryption(f, "bucket", "pool", "arn:aws:kms:us-east-1:123:key/real")
	if err != nil {
		t.Fatal(err)
	}
	claim := physicalpool.OwnershipClaim{PoolID: physicalpool.PoolID("sha256:" + strings.Repeat("a", 64)), CompatibilityDigest: "sha256:" + strings.Repeat("b", 64), EvidenceDigest: "sha256:" + strings.Repeat("c", 64), OwnerID: "instance-a"}
	if err := store.AcquireNamespaceOwnership(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if f.putEncryption != types.ServerSideEncryptionAwsKms || f.putKMSKey != "arn:aws:kms:us-east-1:123:key/real" {
		t.Fatalf("marker encryption = %q/%q, want SSE-KMS resolved key", f.putEncryption, f.putKMSKey)
	}
}

func TestS3NamespaceOwnershipUsesConditionalMarker(t *testing.T) {
	f := &fakeS3{}
	store, err := NewS3(f, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	claim := physicalpool.OwnershipClaim{PoolID: physicalpool.PoolID("sha256:" + strings.Repeat("a", 64)), CompatibilityDigest: "sha256:" + strings.Repeat("b", 64), EvidenceDigest: "sha256:" + strings.Repeat("c", 64), OwnerID: "instance-a"}
	if err := store.AcquireNamespaceOwnership(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireNamespaceOwnership(context.Background(), claim); err != nil {
		t.Fatalf("same-owner retry: %v", err)
	}
	upgraded := claim
	upgraded.CompatibilityDigest = "sha256:" + strings.Repeat("d", 64)
	upgraded.EvidenceDigest = "sha256:" + strings.Repeat("e", 64)
	if err := store.AcquireNamespaceOwnership(context.Background(), upgraded); err != nil {
		t.Fatalf("same durable DB owner must survive tuple upgrade: %v", err)
	}
	conflict := claim
	conflict.OwnerID = "lvinst_other_database"
	if err := store.AcquireNamespaceOwnership(context.Background(), conflict); !errors.Is(err, physicalpool.ErrOwnershipConflict) {
		t.Fatalf("conflicting owner error=%v", err)
	}
}

func TestS3DeletionLeaseFencesClonedMetadataDatabases(t *testing.T) {
	f := &fakeS3{}
	store, err := NewS3(f, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.AcquireNamespaceDeletionLease(context.Background(), "lvinst_a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireNamespaceDeletionLease(context.Background(), "lvinst_a", time.Minute); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("second metadata DB unexpectedly acquired lease: %v", err)
	}
	if err := store.VerifyNamespaceDeletionLease(context.Background(), "lvinst_a", token); err != nil {
		t.Fatalf("lease verification: %v", err)
	}
	if err := store.ReleaseNamespaceDeletionLease(context.Background(), "lvinst_a", token); err != nil {
		t.Fatalf("lease release: %v", err)
	}
	if _, err := store.AcquireNamespaceDeletionLease(context.Background(), "lvinst_b", time.Minute); err != nil {
		t.Fatalf("lease after release: %v", err)
	}
}

func TestS3DeletionLeaseReleasePropagatesVerifyAndHeadFailures(t *testing.T) {
	validExpiry := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	newFake := func() *fakeS3 {
		return &fakeS3{leaseMetadata: map[string]string{
			"leapview-lease-owner": "owner-a", "leapview-lease-token": "token-a", "leapview-lease-expires": validExpiry,
		}, leaseETag: "lease-v1"}
	}
	for _, test := range []struct {
		name   string
		errors []error
	}{
		{name: "verify", errors: []error{errors.New("provider unavailable")}},
		{name: "release head", errors: []error{nil, errors.New("provider unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFake()
			f.leaseHeadErrors = test.errors
			store, err := NewS3(f, "bucket", "pool")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ReleaseNamespaceDeletionLease(context.Background(), "owner-a", "token-a"); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
				t.Fatalf("release error = %v, want deletion lease conflict", err)
			}
		})
	}
	missing := &fakeS3{}
	store, err := NewS3(missing, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseNamespaceDeletionLease(context.Background(), "owner-a", "token-a"); err != nil {
		t.Fatalf("missing lease release = %v, want nil", err)
	}
}

func TestS3StoreUnversionedObjectUsesETagTokenAndByteDigest(t *testing.T) {
	f := &fakeS3{body: []byte("unversioned"), etag: `"etag-1"`}
	store, err := NewS3(f, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Stat(context.Background(), "pool", "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if object.Version != `etag:"etag-1"` || object.Digest == "" {
		t.Fatalf("object identity = %#v, want ETag token and computed digest", object)
	}
	if _, err := store.DeleteConditional(context.Background(), DeleteRequest{PhysicalPoolID: "pool", Key: "orphan", Digest: object.Digest, Version: object.Version}); err != nil {
		t.Fatalf("unversioned conditional delete: %v", err)
	}
	if f.getIfMatch != `"etag-1"` || f.deleteIfMatch != `"etag-1"` {
		t.Fatalf("conditional ETags GET=%q DELETE=%q, want exact quoted ETag", f.getIfMatch, f.deleteIfMatch)
	}
}
func (f *fakeS3) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	if input.IfMatch != nil {
		f.getIfMatch = *input.IfMatch
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.body))}, nil
}
func (f *fakeS3) ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	return &awss3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("pool/orphan")}}}, nil
}
func (f *fakeS3) DeleteObject(_ context.Context, input *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	if input.Key != nil && strings.HasSuffix(*input.Key, s3DeletionLeaseKey) {
		if f.leaseMetadata == nil {
			return nil, errors.New("not found")
		}
		if input.IfMatch == nil || *input.IfMatch != f.leaseETag {
			return nil, errors.New("precondition failed")
		}
		f.leaseMetadata = nil
		return &awss3.DeleteObjectOutput{}, nil
	}
	if input.IfMatch != nil {
		f.deleteIfMatch = *input.IfMatch
	}
	f.deleted = true
	return &awss3.DeleteObjectOutput{}, nil
}

func TestS3StoreConditionalVersionAndPoolPrefix(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	f := &fakeS3{body: []byte("x"), digest: "sha256:" + hex.EncodeToString(sum[:]), version: "v1"}
	store, err := NewS3(f, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	objects, err := store.ListPoolObjects(context.Background(), "pool")
	if err != nil || len(objects) != 1 {
		t.Fatalf("objects=%v err=%v", objects, err)
	}
	if _, err := store.DeleteConditional(context.Background(), DeleteRequest{PhysicalPoolID: "pool", Key: "orphan", Digest: f.digest, Version: "v2"}); err == nil {
		t.Fatal("wrong S3 version deleted")
	}
	if _, err := store.DeleteConditional(context.Background(), DeleteRequest{PhysicalPoolID: "pool", Key: "orphan", Digest: f.digest, Version: "v1"}); err != nil || !f.deleted {
		t.Fatalf("delete err=%v deleted=%v", err, f.deleted)
	}
}
