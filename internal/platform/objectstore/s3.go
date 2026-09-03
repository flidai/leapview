package objectstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	s3DomainMetadataKey    = "leapview-storage-security-domain"
	s3DigestMetadataKey    = "leapview-digest"
	s3SizeMetadataKey      = "leapview-size"
	s3MetadataDigestKey    = "leapview-metadata-digest"
	s3CreatedAtMetadataKey = "leapview-created-at"
	s3EncryptionRefKey     = "leapview-encryption-key-ref"
)

// S3Client is the small AWS/MinIO surface required by S3Store. Credentials,
// endpoint selection, and retries remain the responsibility of the caller.
type S3Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
}

// S3EncryptionMode identifies the explicit server-side encryption policy.
// S3EncryptionSSES3 is the AWS-managed SSE-S3 key; S3EncryptionSSEKMS binds a
// target-resolved provider KMS key to an opaque application reference.
type S3EncryptionMode string

const (
	S3EncryptionSSES3  S3EncryptionMode = "AES256"
	S3EncryptionSSEKMS S3EncryptionMode = "aws:kms"
)

// S3EncryptionConfig deliberately keeps OpaqueKeyRef and ProviderKey
// separate. The former is an application lookup reference and must never be
// sent to S3 as an SSE-KMS key ID.
type S3EncryptionConfig struct {
	Mode         S3EncryptionMode
	OpaqueKeyRef string
	ProviderKey  string
}

// S3StoreConfig configures an immutable S3 namespace. Prefix and domain are
// isolation boundaries and are therefore required to be exact, untrimmed
// values. A zero MaxObjectBytes uses MaxObjectBytes.
type S3StoreConfig struct {
	Bucket                string
	Prefix                string
	StorageSecurityDomain string
	MaxObjectBytes        int64
	Encryption            S3EncryptionConfig
	// TempDir bounds object buffering to an absolute, caller-owned directory.
	// An empty value uses the process temporary directory.
	TempDir             string
	ExpectedBucketOwner string
	ReconcileTimeout    time.Duration
	Now                 func() time.Time
}

// S3Store implements ImmutableStore using create-only S3 writes and verified
// reads. It never allows a caller to escape the configured bucket/prefix or
// storage security domain.
type S3Store struct {
	client                S3Client
	bucket                string
	prefix                string
	domain                string
	maxObjectBytes        int64
	encryptionMode        S3EncryptionMode
	encryptionKeyRef      string
	providerEncryptionKey string
	tempDir               string
	expectedBucketOwner   *string
	reconcileTimeout      time.Duration
	now                   func() time.Time
}

var errS3BodyTooLarge = errors.New("S3 body exceeds configured limit")

// NewS3Store constructs a production S3 immutable object store.
func NewS3Store(client S3Client, config S3StoreConfig) (*S3Store, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil S3 client", ErrInvalid)
	}
	if err := validateS3Bucket(config.Bucket); err != nil {
		return nil, err
	}
	if config.Prefix != "" {
		if err := validatePrefix(config.Prefix); err != nil {
			return nil, fmt.Errorf("%w: S3 prefix: %v", ErrInvalid, err)
		}
	}
	tempDir := config.TempDir
	if tempDir != "" {
		if !filepath.IsAbs(tempDir) || strings.TrimSpace(tempDir) != tempDir || strings.ContainsAny(tempDir, "\x00\r\n") {
			return nil, fmt.Errorf("%w: S3 TempDir must be an absolute path", ErrInvalid)
		}
		info, statErr := os.Stat(tempDir)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("%w: S3 TempDir must be an existing directory", ErrInvalid)
		}
	}
	if err := validateSecurityDomain(config.StorageSecurityDomain); err != nil {
		return nil, fmt.Errorf("%w: S3 security domain: %v", ErrInvalid, err)
	}
	maxBytes := config.MaxObjectBytes
	if maxBytes == 0 {
		maxBytes = MaxObjectBytes
	}
	if maxBytes < 1 || maxBytes > MaxObjectBytes {
		return nil, fmt.Errorf("%w: S3 object byte limit", ErrInvalid)
	}
	reconcileTimeout := config.ReconcileTimeout
	if reconcileTimeout == 0 {
		reconcileTimeout = 2 * time.Second
	}
	if reconcileTimeout < time.Millisecond || reconcileTimeout > time.Minute {
		return nil, fmt.Errorf("%w: invalid S3 reconcile timeout", ErrInvalid)
	}

	enc := config.Encryption
	mode, err := normalizeS3Encryption(enc.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateEncryptionValues(mode, enc.OpaqueKeyRef, enc.ProviderKey); err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	var expectedOwner *string
	if config.ExpectedBucketOwner != "" {
		if len(config.ExpectedBucketOwner) != 12 || !isDigits(config.ExpectedBucketOwner) {
			return nil, fmt.Errorf("%w: invalid expected S3 bucket owner", ErrInvalid)
		}
		expectedOwner = aws.String(config.ExpectedBucketOwner)
	}
	return &S3Store{
		client: client, bucket: config.Bucket, prefix: config.Prefix,
		domain: config.StorageSecurityDomain, maxObjectBytes: maxBytes,
		encryptionMode: mode, encryptionKeyRef: enc.OpaqueKeyRef,
		providerEncryptionKey: enc.ProviderKey, tempDir: tempDir,
		expectedBucketOwner: expectedOwner, reconcileTimeout: reconcileTimeout, now: now,
	}, nil
}

func normalizeS3Encryption(mode S3EncryptionMode) (S3EncryptionMode, error) {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "", "aes256", "sse-s3", "s3":
		return S3EncryptionSSES3, nil
	case "aws:kms", "kms", "sse-kms":
		return S3EncryptionSSEKMS, nil
	default:
		return "", fmt.Errorf("%w: unsupported S3 encryption mode %q", ErrInvalid, mode)
	}
}

func validateEncryptionValues(mode S3EncryptionMode, ref, provider string) error {
	if strings.TrimSpace(ref) != ref || strings.ContainsAny(ref, "\x00\r\n") || len(ref) > 1024 {
		return fmt.Errorf("%w: invalid opaque S3 encryption key reference", ErrInvalid)
	}
	if strings.TrimSpace(provider) != provider || strings.ContainsAny(provider, "\x00\r\n") || len(provider) > 2048 {
		return fmt.Errorf("%w: invalid resolved S3 encryption key identity", ErrInvalid)
	}
	if mode == S3EncryptionSSES3 {
		if ref != "" || provider != "" {
			return fmt.Errorf("%w: SSE-S3 cannot carry KMS key references", ErrInvalid)
		}
		return nil
	}
	if ref == "" || provider == "" {
		return fmt.Errorf("%w: KMS requires opaque and resolved key identities", ErrInvalid)
	}
	if ref == provider {
		return fmt.Errorf("%w: opaque encryption key reference was not resolved", ErrInvalid)
	}
	return nil
}

func validateS3Bucket(bucket string) error {
	if len(bucket) < 3 || len(bucket) > 63 || strings.TrimSpace(bucket) != bucket || !isASCII(bucket) {
		return fmt.Errorf("%w: invalid S3 bucket", ErrInvalid)
	}
	if bucket[0] == '-' || bucket[0] == '.' || bucket[len(bucket)-1] == '-' || bucket[len(bucket)-1] == '.' || strings.Contains(bucket, "..") {
		return fmt.Errorf("%w: invalid S3 bucket", ErrInvalid)
	}
	for _, r := range bucket {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return fmt.Errorf("%w: invalid S3 bucket", ErrInvalid)
		}
	}
	// S3 disallows IPv4-looking bucket names because they are ambiguous with
	// virtual-host addresses.
	if net.ParseIP(bucket) != nil {
		return fmt.Errorf("%w: invalid S3 bucket", ErrInvalid)
	}
	return nil
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func isDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *S3Store) fullKey(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	if s.prefix == "" {
		return key, nil
	}
	return s.prefix + "/" + key, nil
}

func (s *S3Store) relativeKey(full string) (string, error) {
	if s.prefix == "" {
		if err := validateKey(full); err != nil {
			return "", err
		}
		return full, nil
	}
	base := s.prefix + "/"
	if !strings.HasPrefix(full, base) {
		return "", fmt.Errorf("%w: S3 object escaped configured prefix", ErrInvalid)
	}
	relative := strings.TrimPrefix(full, base)
	if err := validateKey(relative); err != nil {
		return "", err
	}
	return relative, nil
}

func (s *S3Store) listPrefix(prefix string) (string, error) {
	if err := validatePrefix(prefix); err != nil {
		return "", err
	}
	if prefix == "" {
		if s.prefix == "" {
			return "", nil
		}
		return s.prefix + "/", nil
	}
	if s.prefix == "" {
		return prefix, nil
	}
	return s.prefix + "/" + prefix, nil
}

type spooledObject struct {
	path   string
	size   int64
	digest string
}

func (s *S3Store) spool(ctx context.Context, reader io.Reader) (spooledObject, error) {
	file, err := os.CreateTemp(s.tempDir, ".leapview-object-*")
	if err != nil {
		return spooledObject{}, fmt.Errorf("%w: create S3 temporary object: %v", ErrInvalid, err)
	}
	path := file.Name()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return spooledObject{}, fmt.Errorf("%w: secure S3 temporary object: %v", ErrInvalid, chmodErr)
	}
	remove := func() { _ = file.Close(); _ = os.Remove(path) }
	defer func() {
		if err != nil {
			remove()
		}
	}()
	hasher := sha256.New()
	chunk := make([]byte, 32*1024)
	var size int64
	zeroReads := 0
	for {
		if contextErr := contextErr(ctx); contextErr != nil {
			err = contextErr
			return spooledObject{}, err
		}
		read, readErr := reader.Read(chunk)
		if read > 0 {
			if size > s.maxObjectBytes-int64(read) {
				err = fmt.Errorf("%w: %w (%d bytes)", ErrInvalid, errS3BodyTooLarge, s.maxObjectBytes)
				return spooledObject{}, err
			}
			if _, err = file.Write(chunk[:read]); err != nil {
				return spooledObject{}, err
			}
			_, _ = hasher.Write(chunk[:read])
			size += int64(read)
			zeroReads = 0
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			err = readErr
			return spooledObject{}, err
		}
		if read == 0 {
			zeroReads++
			if zeroReads >= 100 {
				err = io.ErrNoProgress
				return spooledObject{}, err
			}
		}
	}
	if err = contextErr(ctx); err != nil {
		return spooledObject{}, err
	}
	if err = file.Sync(); err != nil {
		return spooledObject{}, err
	}
	if err = file.Close(); err != nil {
		return spooledObject{}, err
	}
	sum := hasher.Sum(nil)
	return spooledObject{path: path, size: size, digest: "sha256:" + fmt.Sprintf("%x", sum)}, nil
}

func (p *spooledObject) remove() {
	if p == nil || p.path == "" {
		return
	}
	_ = os.Remove(p.path)
	p.path = ""
}

func (s *S3Store) PutImmutable(ctx context.Context, key string, reader io.Reader, metadata ObjectMetadata) (ObjectInfo, error) {
	if s == nil {
		return ObjectInfo{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return ObjectInfo{}, err
	}
	full, err := s.fullKey(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if reader == nil {
		return ObjectInfo{}, fmt.Errorf("%w: nil object reader", ErrInvalid)
	}
	if err := s.validateMetadata(metadata); err != nil {
		return ObjectInfo{}, err
	}
	spooled, err := s.spool(ctx, reader)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer spooled.remove()
	digest := spooled.digest
	if digest != metadata.Digest {
		return ObjectInfo{}, fmt.Errorf("%w: digest got %s want %s", ErrInvalid, digest, metadata.Digest)
	}
	if spooled.size != metadata.SizeBytes {
		return ObjectInfo{}, fmt.Errorf("%w: size got %d want %d", ErrInvalid, spooled.size, metadata.SizeBytes)
	}
	createdAt := s.now().UTC()
	if createdAt.IsZero() {
		return ObjectInfo{}, fmt.Errorf("%w: creation time", ErrInvalid)
	}
	expected := ObjectInfo{Key: key, StorageSecurityDomain: metadata.StorageSecurityDomain, Digest: digest, SizeBytes: spooled.size, ContentType: metadata.ContentType, MetadataDigest: metadata.MetadataDigest, CreatedAt: createdAt}
	providerMetadata := s.providerMetadata(expected)
	upload, openErr := os.Open(spooled.path)
	if openErr != nil {
		return ObjectInfo{}, fmt.Errorf("%w: open S3 upload spool: %v", ErrInvalid, openErr)
	}
	defer upload.Close()
	input := &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(full), Body: upload, ContentLength: aws.Int64(spooled.size), ExpectedBucketOwner: s.expectedBucketOwner,
		ContentType: aws.String(metadata.ContentType), Metadata: providerMetadata, IfNoneMatch: aws.String("*"),
	}
	s.applyEncryption(input)
	putOutput, putErr := s.client.PutObject(ctx, input)
	if putErr == nil {
		// A successful PUT normally carries the provider ETag/version. Some
		// compatible S3 implementations omit one or both from PUT responses, so
		// obtain the authoritative identity from HEAD before returning. This
		// allows callers to retain an exact observed object for conditional
		// maintenance without duplicating provider-specific reads.
		if putOutput != nil {
			if putOutput.VersionId != nil {
				expected.VersionID = aws.ToString(putOutput.VersionId)
			}
			if putOutput.ETag != nil {
				expected.ETag = aws.ToString(putOutput.ETag)
			}
		}
		if expected.ETag == "" || expected.VersionID == "" {
			head, headErr := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner})
			if headErr != nil {
				if isS3NotFound(headErr) {
					return ObjectInfo{}, fmt.Errorf("%w: committed object is not yet visible", ErrAmbiguous)
				}
				return ObjectInfo{}, headErr
			}
			observed, infoErr := s.infoFromHead(key, head)
			if infoErr != nil {
				return ObjectInfo{}, infoErr
			}
			return observed, nil
		}
		return expected, nil
	}
	providerCanceled := ctx.Err() != nil || errors.Is(putErr, context.Canceled) || errors.Is(putErr, context.DeadlineExceeded)
	class := classifyS3Error(putErr)
	if class == s3PutConflict || class == s3PutAmbiguous || providerCanceled {
		reconcileCtx := ctx
		var cancel context.CancelFunc
		if providerCanceled {
			reconcileCtx, cancel = s.detachedReconcileContext()
			defer cancel()
		}
		reconciled, reconcileErr := s.reconcilePut(reconcileCtx, key, expected)
		if reconcileErr == nil {
			return reconciled, nil
		}
		if providerCanceled && errors.Is(reconcileErr, context.DeadlineExceeded) {
			return ObjectInfo{}, fmt.Errorf("%w: provider acknowledgement was canceled", ErrAmbiguous)
		}
		return ObjectInfo{}, reconcileErr
	}
	return ObjectInfo{}, putErr
}

func (s *S3Store) validateMetadata(metadata ObjectMetadata) error {
	if metadata.StorageSecurityDomain != s.domain {
		return fmt.Errorf("%w: got %q want %q", ErrDomainMismatch, metadata.StorageSecurityDomain, s.domain)
	}
	if !isSHA256Identity(metadata.Digest) || !isSHA256Identity(metadata.MetadataDigest) {
		return fmt.Errorf("%w: digest and metadata digest must be canonical sha256 identities", ErrInvalid)
	}
	if metadata.SizeBytes < 0 || metadata.SizeBytes > s.maxObjectBytes {
		return fmt.Errorf("%w: object size", ErrInvalid)
	}
	if len(metadata.ContentType) > MaxContentTypeBytes || !utf8.ValidString(metadata.ContentType) || hasControl(metadata.ContentType) {
		return fmt.Errorf("%w: content type", ErrInvalid)
	}
	return nil
}

func (s *S3Store) providerMetadata(info ObjectInfo) map[string]string {
	created := info.CreatedAt.UTC().Format(time.RFC3339Nano)
	return map[string]string{
		s3DomainMetadataKey:    info.StorageSecurityDomain,
		s3DigestMetadataKey:    info.Digest,
		s3SizeMetadataKey:      strconv.FormatInt(info.SizeBytes, 10),
		s3MetadataDigestKey:    info.MetadataDigest,
		s3CreatedAtMetadataKey: created,
		s3EncryptionRefKey:     s.encryptionKeyRef,
	}
}

func (s *S3Store) applyEncryption(input *awss3.PutObjectInput) {
	if s.encryptionMode == S3EncryptionSSEKMS {
		input.ServerSideEncryption = awss3types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(s.providerEncryptionKey)
		return
	}
	input.ServerSideEncryption = awss3types.ServerSideEncryptionAes256
}

func sameIdentity(a, b ObjectInfo) bool {
	return a.Key == b.Key && a.StorageSecurityDomain == b.StorageSecurityDomain && a.Digest == b.Digest && a.SizeBytes == b.SizeBytes && a.ContentType == b.ContentType && a.MetadataDigest == b.MetadataDigest
}

func (s *S3Store) detachedReconcileContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.reconcileTimeout)
}

func (s *S3Store) reconcilePut(ctx context.Context, key string, expected ObjectInfo) (ObjectInfo, error) {
	full, err := s.fullKey(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner})
	if err != nil {
		if isS3NotFound(err) {
			return ObjectInfo{}, fmt.Errorf("%w: conflicting object is not yet visible", ErrAmbiguous)
		}
		if contextErr(ctx) != nil {
			return ObjectInfo{}, contextErr(ctx)
		}
		if classifyS3Error(err) == s3PutAmbiguous {
			return ObjectInfo{}, fmt.Errorf("%w: reconcile HEAD: %v", ErrAmbiguous, err)
		}
		return ObjectInfo{}, err
	}
	info, err := s.infoFromHead(key, head)
	if err != nil {
		if errors.Is(err, ErrDomainMismatch) {
			return ObjectInfo{}, fmt.Errorf("%w: key %q already contains different bytes or metadata", ErrConflict, key)
		}
		return ObjectInfo{}, err
	}
	if !sameIdentity(info, expected) {
		return ObjectInfo{}, fmt.Errorf("%w: key %q already contains different bytes or metadata", ErrConflict, key)
	}
	opened, err := s.openWithHead(ctx, key, full, head, info)
	if err != nil {
		return ObjectInfo{}, err
	}
	opened.Body.Close()
	return info, nil
}

func (s *S3Store) Open(ctx context.Context, key string) (Object, error) {
	if s == nil {
		return Object{}, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return Object{}, err
	}
	full, err := s.fullKey(key)
	if err != nil {
		return Object{}, err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner})
	if err != nil {
		if isS3NotFound(err) {
			return Object{}, fmt.Errorf("%w: key %q", ErrNotFound, key)
		}
		return Object{}, err
	}
	info, err := s.infoFromHead(key, head)
	if err != nil {
		return Object{}, err
	}
	return s.openWithHead(ctx, key, full, head, info)
}

func (s *S3Store) openWithHead(ctx context.Context, key, full string, head *awss3.HeadObjectOutput, info ObjectInfo) (Object, error) {
	input := &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner}
	if head.VersionId != nil && aws.ToString(head.VersionId) != "" && !strings.EqualFold(aws.ToString(head.VersionId), "null") {
		input.VersionId = head.VersionId
	} else if head.ETag != nil && aws.ToString(head.ETag) != "" {
		input.IfMatch = head.ETag
	} else {
		return Object{}, fmt.Errorf("%w: key %q has no immutable version or ETag", ErrCorrupt, key)
	}
	out, err := s.client.GetObject(ctx, input)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Object{}, err
		}
		if s3GetDrift(err) {
			return Object{}, fmt.Errorf("%w: key %q GET version or precondition drift", ErrCorrupt, key)
		}
		return Object{}, err
	}
	if out == nil || out.Body == nil {
		return Object{}, fmt.Errorf("%w: key %q body is nil", ErrCorrupt, key)
	}
	spooled, spoolErr := s.spool(ctx, out.Body)
	closeErr := out.Body.Close()
	if spoolErr != nil {
		if errors.Is(spoolErr, errS3BodyTooLarge) {
			return Object{}, fmt.Errorf("%w: key %q body exceeds configured limit", ErrCorrupt, key)
		}
		if closeErr != nil && !errors.Is(spoolErr, context.Canceled) && !errors.Is(spoolErr, context.DeadlineExceeded) {
			return Object{}, spoolErr
		}
		return Object{}, spoolErr
	}
	if closeErr != nil {
		spooled.remove()
		return Object{}, closeErr
	}
	if spooled.size != info.SizeBytes || spooled.digest != info.Digest {
		spooled.remove()
		return Object{}, fmt.Errorf("%w: key %q digest or size evidence mismatch", ErrCorrupt, key)
	}
	file, openErr := os.Open(spooled.path)
	if openErr != nil {
		spooled.remove()
		return Object{}, openErr
	}
	return Object{Body: &tempObjectBody{File: file, path: spooled.path}, Info: info}, nil
}

type tempObjectBody struct {
	*os.File
	path   string
	mu     sync.Mutex
	closed bool
}

func (b *tempObjectBody) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	path := b.path
	b.path = ""
	b.mu.Unlock()
	closeErr := b.File.Close()
	removeErr := os.Remove(path)
	if closeErr != nil {
		return closeErr
	}
	if errors.Is(removeErr, os.ErrNotExist) {
		return nil
	}
	return removeErr
}

func (s *S3Store) infoFromHead(key string, head *awss3.HeadObjectOutput) (ObjectInfo, error) {
	if head == nil {
		return ObjectInfo{}, fmt.Errorf("%w: key %q has invalid HEAD content length", ErrCorrupt, key)
	}
	meta := head.Metadata
	domain := meta[s3DomainMetadataKey]
	if domain == "" {
		return ObjectInfo{}, fmt.Errorf("%w: key %q storage domain metadata is invalid", ErrCorrupt, key)
	}
	if domain != s.domain {
		return ObjectInfo{}, fmt.Errorf("%w: key %q", ErrDomainMismatch, key)
	}
	if err := s.verifyEncryption(head); err != nil {
		return ObjectInfo{}, err
	}
	if head.ContentLength == nil || *head.ContentLength < 0 {
		return ObjectInfo{}, fmt.Errorf("%w: key %q has invalid HEAD content length", ErrCorrupt, key)
	}
	digest := meta[s3DigestMetadataKey]
	metadataDigest := meta[s3MetadataDigestKey]
	if !isSHA256Identity(digest) || !isSHA256Identity(metadataDigest) {
		return ObjectInfo{}, fmt.Errorf("%w: key %q digest metadata is invalid", ErrCorrupt, key)
	}
	rawSize, ok := meta[s3SizeMetadataKey]
	if !ok {
		return ObjectInfo{}, fmt.Errorf("%w: key %q size metadata is missing", ErrCorrupt, key)
	}
	size, err := strconv.ParseInt(rawSize, 10, 64)
	if err != nil || size < 0 || size != *head.ContentLength {
		return ObjectInfo{}, fmt.Errorf("%w: key %q size evidence mismatch", ErrCorrupt, key)
	}
	if size > s.maxObjectBytes {
		return ObjectInfo{}, fmt.Errorf("%w: key %q exceeds configured object limit", ErrCorrupt, key)
	}
	contentType := aws.ToString(head.ContentType)
	if len(contentType) > MaxContentTypeBytes || !utf8.ValidString(contentType) || hasControl(contentType) {
		return ObjectInfo{}, fmt.Errorf("%w: key %q content type metadata is invalid", ErrCorrupt, key)
	}
	createdRaw := meta[s3CreatedAtMetadataKey]
	createdAt, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil || createdAt.IsZero() {
		return ObjectInfo{}, fmt.Errorf("%w: key %q creation metadata is invalid", ErrCorrupt, key)
	}
	versionID := ""
	if head.VersionId != nil {
		versionID = aws.ToString(head.VersionId)
	}
	etag := ""
	if head.ETag != nil {
		etag = aws.ToString(head.ETag)
	}
	return ObjectInfo{Key: key, StorageSecurityDomain: domain, Digest: digest, SizeBytes: size, ContentType: contentType, MetadataDigest: metadataDigest, CreatedAt: createdAt.UTC(), VersionID: versionID, ETag: etag}, nil
}

func (s *S3Store) verifyEncryption(head *awss3.HeadObjectOutput) error {
	if head == nil {
		return fmt.Errorf("%w: nil S3 HEAD response", ErrCorrupt)
	}
	if s.encryptionMode == S3EncryptionSSEKMS {
		if head.ServerSideEncryption != awss3types.ServerSideEncryptionAwsKms || aws.ToString(head.SSEKMSKeyId) != s.providerEncryptionKey || head.Metadata[s3EncryptionRefKey] != s.encryptionKeyRef {
			return fmt.Errorf("%w: KMS encryption evidence mismatch", ErrCorrupt)
		}
		return nil
	}
	if head.ServerSideEncryption != awss3types.ServerSideEncryptionAes256 || head.Metadata[s3EncryptionRefKey] != "" {
		return fmt.Errorf("%w: SSE-S3 encryption evidence mismatch", ErrCorrupt)
	}
	return nil
}

func (s *S3Store) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > MaxListLimit {
		return nil, "", fmt.Errorf("%w: list limit must be between 1 and %d", ErrInvalid, MaxListLimit)
	}
	fullPrefix, err := s.listPrefix(prefix)
	if err != nil {
		return nil, "", err
	}
	if cursor != "" {
		if err := validateKey(cursor); err != nil {
			return nil, "", fmt.Errorf("%w: cursor: %v", ErrInvalid, err)
		}
		if !prefixMatch(cursor, prefix) {
			return nil, "", fmt.Errorf("%w: cursor outside listing prefix", ErrInvalid)
		}
	}
	var token *string
	startAfter := ""
	if cursor != "" {
		fullCursor, keyErr := s.fullKey(cursor)
		if keyErr != nil {
			return nil, "", keyErr
		}
		startAfter = fullCursor
	}
	infos := make([]ObjectInfo, 0, limit)
	for {
		if err := contextErr(ctx); err != nil {
			return nil, "", err
		}
		remaining := limit - len(infos)
		if remaining < 1 {
			return infos, infos[len(infos)-1].Key, nil
		}
		maxKeys := int32(remaining)
		if maxKeys > MaxListLimit {
			maxKeys = MaxListLimit
		}
		input := &awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(fullPrefix), MaxKeys: aws.Int32(maxKeys), ExpectedBucketOwner: s.expectedBucketOwner}
		if token != nil {
			input.ContinuationToken = token
		} else if startAfter != "" {
			input.StartAfter = aws.String(startAfter)
		}
		out, listErr := s.client.ListObjectsV2(ctx, input)
		if listErr != nil {
			return nil, "", listErr
		}
		if out == nil {
			return nil, "", fmt.Errorf("%w: nil S3 list response", ErrCorrupt)
		}
		for _, entry := range out.Contents {
			if entry.Key == nil {
				continue
			}
			fullKey := aws.ToString(entry.Key)
			relative, relErr := s.relativeKey(fullKey)
			if relErr != nil {
				return nil, "", fmt.Errorf("%w: listed key %q", ErrCorrupt, fullKey)
			}
			if !prefixMatch(relative, prefix) || (cursor != "" && relative <= cursor) {
				continue
			}
			head, headErr := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: entry.Key, ExpectedBucketOwner: s.expectedBucketOwner})
			if headErr != nil {
				if isS3NotFound(headErr) {
					return nil, "", fmt.Errorf("%w: key %q", ErrNotFound, relative)
				}
				return nil, "", headErr
			}
			info, infoErr := s.infoFromHead(relative, head)
			if infoErr != nil {
				if errors.Is(infoErr, ErrDomainMismatch) {
					// Foreign-domain objects share the physical bucket but are
					// intentionally invisible to this store.
					continue
				}
				return nil, "", infoErr
			}
			infos = append(infos, info)
			if len(infos) == limit {
				if out.IsTruncated != nil && *out.IsTruncated {
					return infos, infos[len(infos)-1].Key, nil
				}
				return infos, "", nil
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return infos, "", nil
		}
		if out.NextContinuationToken == nil || aws.ToString(out.NextContinuationToken) == "" {
			return nil, "", fmt.Errorf("%w: truncated S3 page has no continuation token", ErrCorrupt)
		}
		token = out.NextContinuationToken
		startAfter = ""
	}
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	full, err := s.fullKey(key)
	if err != nil {
		return err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner})
	if err != nil {
		if isS3NotFound(err) {
			return fmt.Errorf("%w: key %q", ErrNotFound, key)
		}
		return err
	}
	original, infoErr := s.infoFromHead(key, head)
	if infoErr != nil {
		return infoErr
	}
	if head.ETag == nil || aws.ToString(head.ETag) == "" {
		return fmt.Errorf("%w: key %q has no ETag fence", ErrCorrupt, key)
	}
	_, err = s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), IfMatch: head.ETag, ExpectedBucketOwner: s.expectedBucketOwner})
	if err != nil {
		providerCanceled := ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		if providerCanceled {
			reconcileCtx, cancel := s.detachedReconcileContext()
			reconcileErr := s.reconcileDelete(reconcileCtx, key, full, original)
			cancel()
			if errors.Is(reconcileErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: provider delete acknowledgement was canceled", ErrAmbiguous)
			}
			return reconcileErr
		}
		if isS3NotFound(err) {
			return fmt.Errorf("%w: key %q", ErrNotFound, key)
		}
		if status := s3HTTPStatus(err); status == 412 || s3APIErrorCode(err, "preconditionfailed") {
			return fmt.Errorf("%w: key %q delete ETag precondition failed", ErrConflict, key)
		}
		if status := s3HTTPStatus(err); status == 409 || s3APIErrorCode(err, "conditionalrequestconflict") || classifyS3Error(err) == s3PutAmbiguous {
			return s.reconcileDelete(ctx, key, full, original)
		}
		return err
	}
	return nil
}

// DeleteExact removes one object only when the provider identity observed by
// the caller still names that exact immutable incarnation. A non-null
// VersionID takes precedence; the literal S3 "null" version is mutable and
// therefore falls back to the observed ETag precondition.
func (s *S3Store) DeleteExact(ctx context.Context, observed ObjectInfo) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	full, err := s.fullKey(observed.Key)
	if err != nil {
		return err
	}
	if observed.StorageSecurityDomain != s.domain {
		return fmt.Errorf("%w: key %q", ErrDomainMismatch, observed.Key)
	}
	input := &awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner}
	if observed.VersionID != "" && !strings.EqualFold(observed.VersionID, "null") {
		input.VersionId = aws.String(observed.VersionID)
	} else if observed.ETag != "" {
		input.IfMatch = aws.String(observed.ETag)
	} else {
		return fmt.Errorf("%w: key %q has no immutable version or ETag", ErrCorrupt, observed.Key)
	}
	_, deleteErr := s.client.DeleteObject(ctx, input)
	if deleteErr == nil {
		return nil
	}
	providerCanceled := ctx.Err() != nil || errors.Is(deleteErr, context.Canceled) || errors.Is(deleteErr, context.DeadlineExceeded)
	if providerCanceled {
		reconcileCtx, cancel := s.detachedReconcileContext()
		reconcileErr := s.reconcileDeleteExact(reconcileCtx, observed.Key, full, observed)
		cancel()
		if errors.Is(reconcileErr, context.DeadlineExceeded) {
			return fmt.Errorf("%w: provider delete acknowledgement was canceled", ErrAmbiguous)
		}
		return reconcileErr
	}
	if isS3NotFound(deleteErr) {
		return fmt.Errorf("%w: key %q", ErrNotFound, observed.Key)
	}
	if status := s3HTTPStatus(deleteErr); status == 412 || s3APIErrorCode(deleteErr, "preconditionfailed") {
		return fmt.Errorf("%w: key %q delete identity precondition failed", ErrConflict, observed.Key)
	}
	if status := s3HTTPStatus(deleteErr); status == 409 || s3APIErrorCode(deleteErr, "conditionalrequestconflict") || classifyS3Error(deleteErr) == s3PutAmbiguous {
		return s.reconcileDeleteExact(ctx, observed.Key, full, observed)
	}
	return deleteErr
}

func (s *S3Store) reconcileDeleteExact(ctx context.Context, key, full string, observed ObjectInfo) error {
	headInput := &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner}
	if observed.VersionID != "" && !strings.EqualFold(observed.VersionID, "null") {
		headInput.VersionId = aws.String(observed.VersionID)
	}
	head, err := s.client.HeadObject(ctx, headInput)
	if err != nil {
		if isS3NotFound(err) {
			return nil
		}
		if contextErr(ctx) != nil {
			return contextErr(ctx)
		}
		if classifyS3Error(err) == s3PutAmbiguous {
			return fmt.Errorf("%w: reconcile exact delete HEAD: %v", ErrAmbiguous, err)
		}
		return err
	}
	current, infoErr := s.infoFromHead(key, head)
	if infoErr != nil {
		if errors.Is(infoErr, ErrDomainMismatch) {
			return fmt.Errorf("%w: key %q changed while deleting", ErrConflict, key)
		}
		return infoErr
	}
	if observed.VersionID != "" && !strings.EqualFold(observed.VersionID, "null") {
		if current.VersionID != observed.VersionID {
			return fmt.Errorf("%w: key %q changed while deleting", ErrConflict, key)
		}
	} else if current.ETag != observed.ETag {
		return fmt.Errorf("%w: key %q changed while deleting", ErrConflict, key)
	}
	return fmt.Errorf("%w: key %q remains after ambiguous delete", ErrAmbiguous, key)
}

func (s *S3Store) reconcileDelete(ctx context.Context, key, full string, original ObjectInfo) error {
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(full), ExpectedBucketOwner: s.expectedBucketOwner})
	if err != nil {
		if isS3NotFound(err) {
			return nil
		}
		if contextErr(ctx) != nil {
			return contextErr(ctx)
		}
		if classifyS3Error(err) == s3PutAmbiguous {
			return fmt.Errorf("%w: reconcile delete HEAD: %v", ErrAmbiguous, err)
		}
		return err
	}
	info, infoErr := s.infoFromHead(key, head)
	if infoErr != nil {
		if errors.Is(infoErr, ErrDomainMismatch) {
			return fmt.Errorf("%w: key %q changed while deleting", ErrConflict, key)
		}
		return infoErr
	}
	if !sameIdentity(info, original) {
		return fmt.Errorf("%w: key %q changed while deleting", ErrConflict, key)
	}
	return fmt.Errorf("%w: key %q remains after ambiguous delete", ErrAmbiguous, key)
}

type s3PutClass uint8

const (
	s3PutOther s3PutClass = iota
	s3PutConflict
	s3PutAmbiguous
)

func classifyS3Error(err error) s3PutClass {
	if err == nil {
		return s3PutOther
	}
	if status := s3HTTPStatus(err); status != 0 {
		switch {
		case status == 409 || status == 412:
			return s3PutConflict
		case status >= 500:
			return s3PutAmbiguous
		case status >= 400:
			return s3PutOther
		}
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := strings.ToLower(apiErr.ErrorCode())
		if code == "preconditionfailed" || code == "conditionalrequestconflict" || code == "conflict" || strings.Contains(code, "alreadyexist") {
			return s3PutConflict
		}
		if strings.Contains(code, "accessdenied") || strings.Contains(code, "invalidaccess") || strings.Contains(code, "signature") || strings.Contains(code, "authorization") || strings.Contains(code, "credential") || strings.Contains(code, "nosuchbucket") || strings.Contains(code, "invalidbucket") {
			return s3PutOther
		}
		return s3PutAmbiguous
	}
	return s3PutAmbiguous
}

func s3HTTPStatus(err error) int {
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) && responseErr != nil {
		return responseErr.HTTPStatusCode()
	}
	return 0
}

func s3GetDrift(err error) bool {
	if status := s3HTTPStatus(err); status == 404 || status == 412 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "nosuchkey", "nosuchversion", "preconditionfailed", "invalidversionid", "notmodified":
			return true
		}
	}
	return false
}

func s3APIErrorCode(err error, want string) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.EqualFold(apiErr.ErrorCode(), want)
}

func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	if status := s3HTTPStatus(err); status == 404 {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "notfound", "nosuchkey", "nosuchobject", "nosuchversion":
			return true
		}
	}
	return false
}
