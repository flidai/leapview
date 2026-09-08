// Package s3 implements content-addressed managed-data storage on Amazon S3.
package s3

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/manageddata/storage"
)

const defaultSignExpiry = 15 * time.Minute

type Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *awss3.DeleteObjectsInput, ...func(*awss3.Options)) (*awss3.DeleteObjectsOutput, error)
	CreateMultipartUpload(context.Context, *awss3.CreateMultipartUploadInput, ...func(*awss3.Options)) (*awss3.CreateMultipartUploadOutput, error)
	CompleteMultipartUpload(context.Context, *awss3.CompleteMultipartUploadInput, ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *awss3.AbortMultipartUploadInput, ...func(*awss3.Options)) (*awss3.AbortMultipartUploadOutput, error)
}

type PartPresigner interface {
	PresignUploadPart(context.Context, *awss3.UploadPartInput, ...func(*awss3.PresignOptions)) (*awsv4.PresignedHTTPRequest, error)
}

type Config struct {
	Bucket     string
	Prefix     string
	SignExpiry time.Duration
}

type Store struct {
	client     Client
	presigner  PartPresigner
	bucket     string
	prefix     string
	signExpiry time.Duration
}

func New(client Client, presigner PartPresigner, config Config) (*Store, error) {
	if client == nil || presigner == nil {
		return nil, fmt.Errorf("%w: S3 client and part presigner are required", storage.ErrInvalid)
	}
	bucket := strings.TrimSpace(config.Bucket)
	if bucket == "" || strings.ContainsAny(bucket, "/\x00\r\n") {
		return nil, fmt.Errorf("%w: S3 bucket is invalid", storage.ErrInvalid)
	}
	prefix := strings.Trim(config.Prefix, "/")
	if strings.ContainsAny(prefix, "\x00\r\n") {
		return nil, fmt.Errorf("%w: S3 prefix is invalid", storage.ErrInvalid)
	}
	expiry := config.SignExpiry
	if expiry == 0 {
		expiry = defaultSignExpiry
	}
	if expiry < time.Minute || expiry > 24*time.Hour {
		return nil, fmt.Errorf("%w: S3 part signing expiry must be between one minute and 24 hours", storage.ErrInvalid)
	}
	return &Store{client: client, presigner: presigner, bucket: bucket, prefix: prefix, signExpiry: expiry}, nil
}

func (s *Store) Put(ctx context.Context, expected storage.Blob, content io.Reader) (storage.Blob, error) {
	if err := storage.ValidateBlob(expected); err != nil {
		return storage.Blob{}, err
	}
	if content == nil {
		return storage.Blob{}, fmt.Errorf("%w: blob content is required", storage.ErrInvalid)
	}
	if existing, err := s.verify(ctx, expected); err == nil {
		return existing, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return storage.Blob{}, err
	}
	checksum, err := checksumBase64(expected.SHA256)
	if err != nil {
		return storage.Blob{}, err
	}
	key := s.blobKey(expected.SHA256)
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:         pointer(s.bucket),
		Key:            pointer(key),
		Body:           content,
		ContentLength:  pointer(expected.Size),
		ChecksumSHA256: pointer(checksum),
		IfNoneMatch:    pointer("*"),
		Metadata:       blobMetadata(expected),
	})
	if err != nil {
		if isCode(err, "PreconditionFailed", "ConditionalRequestConflict") {
			return s.verify(ctx, expected)
		}
		return storage.Blob{}, sanitizeError(ctx, "put S3 blob", err)
	}
	return s.verify(ctx, expected)
}

func (s *Store) Stat(ctx context.Context, digest string) (storage.Blob, error) {
	if err := storage.ValidateSHA256(digest); err != nil {
		return storage.Blob{}, err
	}
	return s.verify(ctx, storage.Blob{SHA256: digest, Size: -1})
}

func (s *Store) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	if _, err := s.Stat(ctx, digest); err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: pointer(s.bucket), Key: pointer(s.blobKey(digest))})
	if err != nil {
		return nil, sanitizeError(ctx, "open S3 blob", err)
	}
	return result.Body, nil
}

func (s *Store) WalkBlobs(ctx context.Context, visit func(storage.BlobMetadata) error) error {
	if visit == nil {
		return fmt.Errorf("%w: blob visitor is required", storage.ErrInvalid)
	}
	prefix := s.blobPrefix()
	var continuation *string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket:            pointer(s.bucket),
			Prefix:            pointer(prefix),
			ContinuationToken: continuation,
			MaxKeys:           pointer(int32(1000)),
		})
		if err != nil {
			return sanitizeError(ctx, "list S3 blobs", err)
		}
		if result == nil {
			return fmt.Errorf("%w: list S3 blobs", storage.ErrBackend)
		}
		for _, object := range result.Contents {
			if object.Key == nil || object.Size == nil || object.LastModified == nil {
				return fmt.Errorf("%w: S3 blob inventory metadata is incomplete", storage.ErrIntegrity)
			}
			digest, valid := parseBlobKey(prefix, *object.Key)
			if !valid || *object.Size < 0 || object.LastModified.IsZero() {
				return fmt.Errorf("%w: S3 blob inventory is noncanonical", storage.ErrIntegrity)
			}
			if err := visit(storage.BlobMetadata{SHA256: digest, Size: *object.Size, LastModified: object.LastModified.UTC()}); err != nil {
				return err
			}
		}
		if result.IsTruncated == nil || !*result.IsTruncated {
			return nil
		}
		if result.NextContinuationToken == nil || *result.NextContinuationToken == "" || continuation != nil && *result.NextContinuationToken == *continuation {
			return fmt.Errorf("%w: S3 blob listing pagination is invalid", storage.ErrBackend)
		}
		continuation = result.NextContinuationToken
	}
}

func (s *Store) DeleteBlobs(ctx context.Context, digests []string) error {
	if len(digests) > 1000 {
		return fmt.Errorf("%w: blob deletion batch exceeds 1000 entries", storage.ErrInvalid)
	}
	for _, digest := range digests {
		if err := storage.ValidateSHA256(digest); err != nil {
			return err
		}
	}
	if len(digests) == 0 {
		return nil
	}
	objects := make([]types.ObjectIdentifier, len(digests))
	for index, digest := range digests {
		objects[index] = types.ObjectIdentifier{Key: pointer(s.blobKey(digest))}
	}
	result, err := s.client.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
		Bucket: pointer(s.bucket),
		Delete: &types.Delete{Objects: objects, Quiet: pointer(true)},
	})
	if err != nil {
		return sanitizeError(ctx, "delete S3 blobs", err)
	}
	if result == nil || len(result.Errors) != 0 {
		return fmt.Errorf("%w: delete S3 blobs", storage.ErrBackend)
	}
	return nil
}

func (s *Store) CreateMultipart(ctx context.Context, expected storage.Blob) (storage.MultipartUpload, error) {
	if err := storage.ValidateBlob(expected); err != nil {
		return storage.MultipartUpload{}, err
	}
	key := s.blobKey(expected.SHA256)
	if _, err := s.verify(ctx, expected); err == nil {
		return storage.MultipartUpload{SHA256: expected.SHA256, Size: expected.Size, Key: key, Existing: true}, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return storage.MultipartUpload{}, err
	}
	result, err := s.client.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket:   pointer(s.bucket),
		Key:      pointer(key),
		Metadata: blobMetadata(expected),
	})
	if err != nil {
		return storage.MultipartUpload{}, sanitizeError(ctx, "create S3 multipart upload", err)
	}
	if result.UploadId == nil || *result.UploadId == "" {
		return storage.MultipartUpload{}, fmt.Errorf("%w: S3 returned an empty multipart upload ID", storage.ErrBackend)
	}
	return storage.MultipartUpload{UploadID: *result.UploadId, SHA256: expected.SHA256, Size: expected.Size, Key: key}, nil
}

// ListMultipartUploads returns in-flight uploads for one deterministic object
// key. It is used by recovery to discover provider success before the control
// plane has committed the upload identifier.
func (s *Store) ListMultipartUploads(ctx context.Context, expected storage.Blob) ([]storage.MultipartUpload, error) {
	key := s.blobKey(expected.SHA256)
	lister, ok := s.client.(interface {
		ListMultipartUploads(context.Context, *awss3.ListMultipartUploadsInput, ...func(*awss3.Options)) (*awss3.ListMultipartUploadsOutput, error)
	})
	if !ok {
		return nil, fmt.Errorf("%w: multipart listing is unavailable", storage.ErrBackend)
	}
	uploads := make([]storage.MultipartUpload, 0)
	var keyMarker, uploadIDMarker *string
	for {
		result, err := lister.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{
			Bucket: pointer(s.bucket), Prefix: pointer(strings.TrimSpace(key)),
			KeyMarker: keyMarker, UploadIdMarker: uploadIDMarker,
		})
		if err != nil {
			return nil, sanitizeError(ctx, "list S3 multipart uploads", err)
		}
		if result == nil {
			return nil, fmt.Errorf("%w: empty multipart listing", storage.ErrBackend)
		}
		for _, item := range result.Uploads {
			if item.Key == nil || item.UploadId == nil || *item.Key != key || *item.UploadId == "" {
				continue
			}
			uploads = append(uploads, storage.MultipartUpload{UploadID: *item.UploadId, Key: *item.Key, SHA256: expected.SHA256, Size: expected.Size})
		}
		if result.IsTruncated == nil || !*result.IsTruncated {
			return uploads, nil
		}
		if result.NextKeyMarker == nil || *result.NextKeyMarker == "" ||
			(keyMarker != nil && result.NextKeyMarker != nil && *result.NextKeyMarker == *keyMarker &&
				(result.NextUploadIdMarker == nil || uploadIDMarker != nil && *result.NextUploadIdMarker == *uploadIDMarker)) {
			return nil, fmt.Errorf("%w: multipart listing pagination is invalid", storage.ErrBackend)
		}
		keyMarker = result.NextKeyMarker
		uploadIDMarker = result.NextUploadIdMarker
	}
}

func (s *Store) SignPart(ctx context.Context, upload storage.MultipartUpload, part storage.MultipartPartRequest) (storage.SignedMultipartPart, error) {
	if err := s.validateMultipart(upload); err != nil {
		return storage.SignedMultipartPart{}, err
	}
	if upload.Existing || upload.UploadID == "" {
		return storage.SignedMultipartPart{}, fmt.Errorf("%w: existing blobs do not accept multipart parts", storage.ErrInvalid)
	}
	if part.Number < 1 || part.Number > 10_000 || part.Size <= 0 {
		return storage.SignedMultipartPart{}, fmt.Errorf("%w: S3 multipart part number or size is invalid", storage.ErrInvalid)
	}
	input := &awss3.UploadPartInput{
		Bucket:        pointer(s.bucket),
		Key:           pointer(upload.Key),
		UploadId:      pointer(upload.UploadID),
		PartNumber:    pointer(part.Number),
		ContentLength: pointer(part.Size),
	}
	if part.SHA256 != "" {
		checksum, err := checksumBase64(part.SHA256)
		if err != nil {
			return storage.SignedMultipartPart{}, err
		}
		input.ChecksumSHA256 = pointer(checksum)
	}
	result, err := s.presigner.PresignUploadPart(ctx, input, func(options *awss3.PresignOptions) {
		options.Expires = s.signExpiry
	})
	if err != nil {
		return storage.SignedMultipartPart{}, sanitizeError(ctx, "sign S3 multipart part", err)
	}
	return storage.SignedMultipartPart{Number: part.Number, URL: result.URL, Headers: cloneHeaders(result.SignedHeader)}, nil
}

func cloneHeaders(headers map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(headers))
	for name, values := range headers {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func (s *Store) CompleteMultipart(ctx context.Context, upload storage.MultipartUpload, parts []storage.CompletedMultipartPart) (storage.Blob, error) {
	if err := s.validateMultipart(upload); err != nil {
		return storage.Blob{}, err
	}
	expected := storage.Blob{SHA256: upload.SHA256, Size: upload.Size}
	if existing, err := s.verify(ctx, expected); err == nil {
		_ = s.AbortMultipart(ctx, upload)
		return existing, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return storage.Blob{}, err
	}
	if upload.Existing || upload.UploadID == "" {
		return storage.Blob{}, storage.ErrNotFound
	}
	completed, err := completedParts(parts)
	if err != nil {
		return storage.Blob{}, err
	}
	_, err = s.client.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket:          pointer(s.bucket),
		Key:             pointer(upload.Key),
		UploadId:        pointer(upload.UploadID),
		IfNoneMatch:     pointer("*"),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		if isCode(err, "PreconditionFailed", "ConditionalRequestConflict") {
			_ = s.AbortMultipart(ctx, upload)
			return s.verify(ctx, expected)
		}
		if isCode(err, "NoSuchUpload") {
			return s.verify(ctx, expected)
		}
		return storage.Blob{}, sanitizeError(ctx, "complete S3 multipart upload", err)
	}
	blob, verifyErr := s.verify(ctx, expected)
	if verifyErr == nil {
		return blob, nil
	}
	if deleteErr := s.DeleteBlobs(ctx, []string{expected.SHA256}); deleteErr != nil {
		return storage.Blob{}, deleteErr
	}
	return storage.Blob{}, verifyErr
}

func (s *Store) AbortMultipart(ctx context.Context, upload storage.MultipartUpload) error {
	if err := s.validateMultipart(upload); err != nil {
		return err
	}
	if upload.Existing || upload.UploadID == "" {
		return nil
	}
	_, err := s.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket:   pointer(s.bucket),
		Key:      pointer(upload.Key),
		UploadId: pointer(upload.UploadID),
	})
	if err != nil && !isCode(err, "NoSuchUpload") {
		return sanitizeError(ctx, "abort S3 multipart upload", err)
	}
	return nil
}

func (s *Store) verify(ctx context.Context, expected storage.Blob) (storage.Blob, error) {
	key := s.blobKey(expected.SHA256)
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: pointer(s.bucket), Key: pointer(key)})
	if err != nil {
		return storage.Blob{}, sanitizeError(ctx, "head S3 blob", err)
	}
	if head.ContentLength == nil {
		return storage.Blob{}, fmt.Errorf("%w: S3 blob has no content length", storage.ErrIntegrity)
	}
	if expected.Size >= 0 && *head.ContentLength != expected.Size {
		return storage.Blob{}, fmt.Errorf("%w: S3 blob size does not match", storage.ErrIntegrity)
	}
	if digest := head.Metadata["sha256"]; digest != "" && digest != expected.SHA256 {
		return storage.Blob{}, fmt.Errorf("%w: S3 blob metadata does not match", storage.ErrIntegrity)
	}
	result, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: pointer(s.bucket), Key: pointer(key)})
	if err != nil {
		return storage.Blob{}, sanitizeError(ctx, "stream S3 blob for verification", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: result.Body})
	closeErr := result.Body.Close()
	if copyErr != nil {
		return storage.Blob{}, sanitizeError(ctx, "stream S3 blob for verification", copyErr)
	}
	if closeErr != nil {
		return storage.Blob{}, sanitizeError(ctx, "close S3 verification stream", closeErr)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if written != *head.ContentLength || digest != expected.SHA256 {
		return storage.Blob{}, fmt.Errorf("%w: S3 blob does not match its content address", storage.ErrIntegrity)
	}
	return storage.Blob{SHA256: digest, Size: written, URI: s.blobURI(key)}, nil
}

func (s *Store) validateMultipart(upload storage.MultipartUpload) error {
	expected := storage.Blob{SHA256: upload.SHA256, Size: upload.Size}
	if err := storage.ValidateBlob(expected); err != nil {
		return err
	}
	if upload.Key != s.blobKey(upload.SHA256) {
		return fmt.Errorf("%w: S3 multipart key does not match its content address", storage.ErrInvalid)
	}
	return nil
}

func (s *Store) blobKey(digest string) string {
	key := "blobs/sha256/" + digest[:2] + "/" + digest
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *Store) blobPrefix() string {
	if s.prefix == "" {
		return "blobs/sha256/"
	}
	return s.prefix + "/blobs/sha256/"
}

func parseBlobKey(prefix, key string) (string, bool) {
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	relative := strings.TrimPrefix(key, prefix)
	parts := strings.Split(relative, "/")
	if len(parts) != 2 || len(parts[0]) != 2 || storage.ValidateSHA256(parts[1]) != nil || parts[0] != parts[1][:2] {
		return "", false
	}
	return parts[1], true
}

func (s *Store) blobURI(key string) string {
	return (&url.URL{Scheme: "s3", Host: s.bucket, Path: "/" + key}).String()
}

func completedParts(parts []storage.CompletedMultipartPart) ([]types.CompletedPart, error) {
	if len(parts) == 0 || len(parts) > 10_000 {
		return nil, fmt.Errorf("%w: S3 multipart completion requires parts", storage.ErrInvalid)
	}
	ordered := append([]storage.CompletedMultipartPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	result := make([]types.CompletedPart, len(ordered))
	for index, part := range ordered {
		if part.Number < 1 || part.Number > 10_000 || strings.TrimSpace(part.ETag) == "" || index > 0 && ordered[index-1].Number == part.Number {
			return nil, fmt.Errorf("%w: S3 completed part is invalid", storage.ErrInvalid)
		}
		result[index] = types.CompletedPart{PartNumber: pointer(part.Number), ETag: pointer(part.ETag)}
		if part.SHA256 != "" {
			checksum, err := checksumBase64(part.SHA256)
			if err != nil {
				return nil, err
			}
			result[index].ChecksumSHA256 = pointer(checksum)
		}
	}
	return result, nil
}

func blobMetadata(blob storage.Blob) map[string]string {
	return map[string]string{"sha256": blob.SHA256, "size": strconv.FormatInt(blob.Size, 10)}
}

func checksumBase64(digest string) (string, error) {
	if err := storage.ValidateSHA256(digest); err != nil {
		return "", err
	}
	decoded, _ := hex.DecodeString(digest)
	return base64.StdEncoding.EncodeToString(decoded), nil
}

type codedError interface {
	ErrorCode() string
}

func isCode(err error, codes ...string) bool {
	var apiError codedError
	if !errors.As(err, &apiError) {
		return false
	}
	for _, code := range codes {
		if apiError.ErrorCode() == code {
			return true
		}
	}
	return false
}

func sanitizeError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isCode(err, "NoSuchKey", "NotFound", "404") {
		return fmt.Errorf("%w: %s", storage.ErrNotFound, operation)
	}
	if isCode(err, "BadDigest", "InvalidDigest", "IncompleteBody") {
		return fmt.Errorf("%w: %s", storage.ErrIntegrity, operation)
	}
	return fmt.Errorf("%w: %s", storage.ErrBackend, operation)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func pointer[T any](value T) *T { return &value }

var _ storage.BlobStore = (*Store)(nil)
var _ storage.BlobInventory = (*Store)(nil)
