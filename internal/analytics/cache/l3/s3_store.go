package l3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

// S3Client is the minimum AWS/MinIO API needed by the L3 object adapter.
// Credentials and endpoint selection stay in the runtime composition root.
type S3Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
}

// S3ObjectStore is a domain-neutral adapter. Cache.New supplies the exact
// security-domain path suffix; this adapter only prepends the admitted pool
// prefix and never accepts an absolute or traversing key.
type S3ObjectStore struct {
	client           S3Client
	bucket           string
	prefix           string
	encryptionKeyRef string
	// providerEncryptionKey is resolved by the target-owned credential/key
	// capability.  The pool identity's EncryptionKeyRef is deliberately only
	// an opaque lookup reference and must never be sent to S3 as a key ID.
	providerEncryptionKey string
}

func NewS3ObjectStore(client S3Client, bucket, prefix string) (*S3ObjectStore, error) {
	return NewS3ObjectStoreWithEncryption(client, bucket, prefix, "")
}

// NewS3ObjectStoreWithEncryption is retained for callers that do not have a
// target key resolver. It supports the unencrypted-reference case (which
// still requires explicit SSE-S3); non-empty references are rejected so an
// opaque pool value cannot be sent as a provider KMS key ID. Use
// NewS3ObjectStoreWithResolvedEncryption for KMS-backed pools.
func NewS3ObjectStoreWithEncryption(client S3Client, bucket, prefix, encryptionKeyRef string) (*S3ObjectStore, error) {
	return NewS3ObjectStoreWithResolvedEncryption(client, bucket, prefix, encryptionKeyRef, "")
}

// NewS3ObjectStoreWithResolvedEncryption binds both the admitted opaque key
// reference and its target-resolved provider identity.  A non-empty reference
// without a resolved identity is rejected so an opaque value can never be
// accidentally sent as an AWS KMS key ID.
func NewS3ObjectStoreWithResolvedEncryption(client S3Client, bucket, prefix, encryptionKeyRef, providerEncryptionKey string) (*S3ObjectStore, error) {
	if client == nil || strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("%w: S3 client and bucket are required", ErrInvalid)
	}
	if strings.ContainsAny(encryptionKeyRef, "\x00\r\n") || strings.TrimSpace(encryptionKeyRef) != encryptionKeyRef {
		return nil, fmt.Errorf("%w: invalid S3 encryption key reference", ErrInvalid)
	}
	if strings.ContainsAny(providerEncryptionKey, "\x00\r\n") || strings.TrimSpace(providerEncryptionKey) != providerEncryptionKey {
		return nil, fmt.Errorf("%w: invalid resolved S3 encryption key identity", ErrInvalid)
	}
	if encryptionKeyRef != "" && providerEncryptionKey == "" {
		return nil, fmt.Errorf("%w: resolved S3 encryption key identity is required", ErrInvalid)
	}
	if encryptionKeyRef != "" && providerEncryptionKey == encryptionKeyRef {
		return nil, fmt.Errorf("%w: opaque encryption key reference was not resolved", ErrInvalid)
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix != "" {
		for _, segment := range strings.Split(prefix, "/") {
			if !safePrefixSegment(segment) {
				return nil, fmt.Errorf("%w: S3 prefix is not portable", ErrInvalid)
			}
		}
	}
	return &S3ObjectStore{client: client, bucket: strings.TrimSpace(bucket), prefix: prefix, encryptionKeyRef: encryptionKeyRef, providerEncryptionKey: providerEncryptionKey}, nil
}

func (s *S3ObjectStore) fullKey(key string) (string, error) {
	key = strings.Trim(key, "/")
	if key == "" || strings.ContainsAny(key, "\\\x00\r\n") {
		return "", fmt.Errorf("%w: invalid S3 object key", ErrInvalid)
	}
	for _, segment := range strings.Split(key, "/") {
		if !safePrefixSegment(segment) && platformdigest.ValidateSHA256Identity(segment) != nil {
			return "", fmt.Errorf("%w: invalid S3 object key", ErrInvalid)
		}
	}
	if s.prefix == "" {
		return key, nil
	}
	return s.prefix + "/" + key, nil
}

func (s *S3ObjectStore) relative(full string) (string, error) {
	if s.prefix == "" {
		return full, nil
	}
	prefix := s.prefix + "/"
	if !strings.HasPrefix(full, prefix) {
		return "", fmt.Errorf("%w: S3 object escaped pool prefix", ErrSecurityDomain)
	}
	return strings.TrimPrefix(full, prefix), nil
}

func (s *S3ObjectStore) PutImmutable(ctx context.Context, key string, body io.Reader, metadata ObjectMetadata) (ObjectInfo, error) {
	if body == nil || platformdigest.ValidateSHA256Identity(metadata.SecurityDomain) != nil {
		return ObjectInfo{}, fmt.Errorf("%w: invalid S3 object metadata", ErrInvalid)
	}
	full, err := s.fullKey(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	canonical, err := canonicalMetadata(metadata.Metadata)
	if err != nil {
		return ObjectInfo{}, err
	}
	data, err := readBounded(body, MaxObjectBytesLimit)
	if err != nil {
		return ObjectInfo{}, err
	}
	digest := digestBytes(data)
	metadataDigest := digestBytes(canonical)
	if metadata.MetadataDigest != "" && metadata.MetadataDigest != metadataDigest {
		return ObjectInfo{}, fmt.Errorf("%w: metadata digest mismatch", ErrInvalid)
	}
	providerMetadata := map[string]string{
		"leapview-security-domain": metadata.SecurityDomain,
		"leapview-digest":          digest,
		"leapview-size":            strconv.FormatInt(int64(len(data)), 10),
		// Keep provider metadata bounded: the canonical JSON itself is not
		// copied into S3 user-metadata headers.
		"leapview-metadata-digest": metadataDigest,
	}
	putInput := &awss3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))), Metadata: providerMetadata, IfNoneMatch: aws.String("*")}
	if s.encryptionKeyRef != "" {
		putInput.ServerSideEncryption = awss3types.ServerSideEncryptionAwsKms
		putInput.SSEKMSKeyId = aws.String(s.providerEncryptionKey)
		providerMetadata["leapview-encryption-key-ref"] = s.encryptionKeyRef
	} else {
		putInput.ServerSideEncryption = awss3types.ServerSideEncryptionAes256
		providerMetadata["leapview-encryption-key-ref"] = ""
	}
	_, putErr := s.client.PutObject(ctx, putInput)
	if putErr != nil {
		return ObjectInfo{Key: key, SecurityDomain: metadata.SecurityDomain, Digest: digest, Size: int64(len(data)), Metadata: canonical, MetadataDigest: metadataDigest}, classifyPutError(putErr)
	}
	return ObjectInfo{Key: key, SecurityDomain: metadata.SecurityDomain, Digest: digest, Size: int64(len(data)), Metadata: canonical, MetadataDigest: metadataDigest, CreatedAt: time.Now().UTC()}, nil
}

func (s *S3ObjectStore) Open(ctx context.Context, key string) (Object, error) {
	full, err := s.fullKey(key)
	if err != nil {
		return Object{}, err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full)})
	if err != nil {
		return Object{}, err
	}
	if err := s.verifyEncryption(head); err != nil {
		return Object{}, err
	}
	info, err := s.infoFromHead(key, head)
	if err != nil {
		return Object{}, err
	}
	getInput := &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full)}
	if head.VersionId != nil && *head.VersionId != "" && !strings.EqualFold(*head.VersionId, "null") {
		getInput.VersionId = head.VersionId
	} else if head.ETag != nil && *head.ETag != "" {
		getInput.IfMatch = head.ETag
	} else {
		return Object{}, fmt.Errorf("%w: S3 object has no immutable version or ETag", ErrObjectCorrupt)
	}
	out, err := s.client.GetObject(ctx, getInput)
	if err != nil {
		return Object{}, err
	}
	if out.Body == nil {
		return Object{}, fmt.Errorf("%w: S3 object body is nil", ErrObjectCorrupt)
	}
	return Object{Body: out.Body, Info: info}, nil
}

func (s *S3ObjectStore) infoFromHead(key string, head *awss3.HeadObjectOutput) (ObjectInfo, error) {
	if head == nil {
		return ObjectInfo{}, fmt.Errorf("%w: S3 HEAD response is empty", ErrObjectCorrupt)
	}
	size := int64(0)
	if head.ContentLength == nil || *head.ContentLength < 0 {
		return ObjectInfo{}, fmt.Errorf("%w: S3 object content length is invalid", ErrObjectCorrupt)
	}
	size = *head.ContentLength
	if rawSize := head.Metadata["leapview-size"]; rawSize != "" {
		parsed, parseErr := strconv.ParseInt(rawSize, 10, 64)
		if parseErr != nil || parsed < 0 {
			return ObjectInfo{}, fmt.Errorf("%w: S3 object size metadata is invalid", ErrObjectCorrupt)
		}
		if parsed != size {
			return ObjectInfo{}, fmt.Errorf("%w: S3 object size evidence mismatch", ErrObjectCorrupt)
		}
	}
	domain := head.Metadata["leapview-security-domain"]
	digest := head.Metadata["leapview-digest"]
	metadataDigest := head.Metadata["leapview-metadata-digest"]
	if platformdigest.ValidateSHA256Identity(domain) != nil || platformdigest.ValidateSHA256Identity(digest) != nil || platformdigest.ValidateSHA256Identity(metadataDigest) != nil {
		return ObjectInfo{}, fmt.Errorf("%w: S3 object digest metadata is invalid", ErrObjectCorrupt)
	}
	createdAt := time.Time{}
	if head.LastModified != nil {
		createdAt = head.LastModified.UTC()
	}
	versionID, etag := "", ""
	if head.VersionId != nil {
		versionID = *head.VersionId
	}
	if head.ETag != nil {
		etag = *head.ETag
	}
	return ObjectInfo{Key: key, SecurityDomain: domain, Digest: digest, Size: size, MetadataDigest: metadataDigest, VersionID: versionID, ETag: etag, CreatedAt: createdAt}, nil
}

func (s *S3ObjectStore) DeleteExact(ctx context.Context, object ObjectInfo) error {
	full, err := s.fullKey(object.Key)
	if err != nil {
		return err
	}
	input := &awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full)}
	// S3 uses the literal "null" version ID for an unversioned object and for
	// the mutable null version in a versioning-suspended bucket. It is not an
	// immutable incarnation identifier, so deletion must use the observed ETag.
	if object.VersionID != "" && !strings.EqualFold(object.VersionID, "null") {
		input.VersionId = aws.String(object.VersionID)
	} else if object.ETag != "" {
		input.IfMatch = aws.String(object.ETag)
	} else {
		return fmt.Errorf("%w: S3 delete requires an exact version or ETag", ErrObjectCorrupt)
	}
	_, err = s.client.DeleteObject(ctx, input)
	return err
}

func classifyPutError(err error) error {
	if err == nil {
		return nil
	}
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil && responseErr.Response != nil && responseErr.Response.Response != nil {
		status := responseErr.HTTPStatusCode()
		switch status {
		case 409, 412:
			return ErrObjectExists
		case 401, 403, 404:
			return err
		}
		if status >= 400 && status < 500 {
			return err
		}
		return ErrObjectAmbiguous
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := strings.ToLower(apiErr.ErrorCode())
		switch code {
		case "preconditionfailed", "conditionalrequestconflict", "entityalreadyexists", "objectalreadyinactive", "objectalreadyexists":
			return ErrObjectExists
		case "accessdenied", "invalidaccesskeyid", "signaturedoesnotmatch", "nosuchbucket", "invalidbucketname":
			return err
		}
		if strings.Contains(code, "precondition") || strings.Contains(code, "conditional") || strings.Contains(code, "alreadyexists") || strings.Contains(code, "already-exists") || strings.Contains(code, "conflict") {
			return ErrObjectExists
		}
		if strings.Contains(code, "access") || strings.Contains(code, "auth") || strings.Contains(code, "credential") || strings.Contains(code, "bucket") {
			return err
		}
		return ErrObjectAmbiguous
	}
	// A transport failure leaves the provider's commit state unknown.
	return ErrObjectAmbiguous
}

func (s *S3ObjectStore) verifyEncryption(head *awss3.HeadObjectOutput) error {
	if head == nil {
		return fmt.Errorf("%w: S3 HEAD response is empty", ErrObjectCorrupt)
	}
	if s.encryptionKeyRef != "" {
		if head.ServerSideEncryption != awss3types.ServerSideEncryptionAwsKms || head.SSEKMSKeyId == nil || *head.SSEKMSKeyId != s.providerEncryptionKey || head.Metadata["leapview-encryption-key-ref"] != s.encryptionKeyRef {
			return fmt.Errorf("%w: S3 KMS encryption evidence mismatch", ErrSecurityDomain)
		}
		return nil
	}
	if head.ServerSideEncryption != awss3types.ServerSideEncryptionAes256 || head.Metadata["leapview-encryption-key-ref"] != "" {
		return fmt.Errorf("%w: S3 AES256 encryption evidence mismatch", ErrSecurityDomain)
	}
	return nil
}

func (s *S3ObjectStore) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, string, error) {
	if limit < 1 || limit > MaxGCBatchSize {
		return nil, "", fmt.Errorf("%w: invalid S3 list limit", ErrInvalid)
	}
	fullPrefix, err := s.fullPrefix(prefix)
	if err != nil {
		return nil, "", err
	}
	input := &awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(fullPrefix), MaxKeys: aws.Int32(int32(limit))}
	if after != "" {
		fullAfter, keyErr := s.fullKey(after)
		if keyErr != nil {
			return nil, "", keyErr
		}
		input.StartAfter = aws.String(fullAfter)
	}
	out, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, "", err
	}
	if out == nil {
		return nil, "", fmt.Errorf("%w: S3 LIST response is empty", ErrObjectCorrupt)
	}
	objects := make([]ObjectInfo, 0, len(out.Contents))
	for _, entry := range out.Contents {
		if entry.Key == nil {
			continue
		}
		relative, relErr := s.relative(*entry.Key)
		if relErr != nil {
			return nil, "", relErr
		}
		head, headErr := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: entry.Key})
		if headErr != nil {
			return nil, "", headErr
		}
		if encryptionErr := s.verifyEncryption(head); encryptionErr != nil {
			return nil, "", encryptionErr
		}
		info, infoErr := s.infoFromHead(relative, head)
		if infoErr != nil {
			return nil, "", infoErr
		}
		objects = append(objects, info)
	}
	next := ""
	if out.IsTruncated != nil && *out.IsTruncated {
		if len(objects) == 0 {
			return nil, "", fmt.Errorf("%w: truncated S3 page has no resumable cursor", ErrInvalid)
		}
		next = objects[len(objects)-1].Key
	}
	return objects, next, nil
}

func (s *S3ObjectStore) fullPrefix(prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return "", fmt.Errorf("%w: empty S3 listing prefix", ErrInvalid)
	}
	for _, segment := range strings.Split(prefix, "/") {
		if !safePrefixSegment(segment) && platformdigest.ValidateSHA256Identity(segment) != nil {
			return "", fmt.Errorf("%w: invalid S3 listing prefix", ErrInvalid)
		}
	}
	if s.prefix == "" {
		return prefix, nil
	}
	return s.prefix + "/" + prefix, nil
}
