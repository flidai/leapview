package objectstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type fakeS3Object struct {
	body              []byte
	meta              map[string]string
	contentType       string
	created           time.Time
	etag              string
	version           string
	encryption        awss3types.ServerSideEncryption
	kmsKey            string
	customerAlgorithm string
	customerKeyMD5    string
}

type fakeS3Client struct {
	mu                   sync.Mutex
	objects              map[string]fakeS3Object
	putErr               error
	putErrAfterCommit    bool
	headErr              error
	getErr               error
	listErr              error
	deleteErr            error
	deleteErrAfterDelete bool
	deleteRejectIfMatch  bool
	puts                 []*awss3.PutObjectInput
	heads                []*awss3.HeadObjectInput
	deletes              []*awss3.DeleteObjectInput
	gets                 []*awss3.GetObjectInput
	lists                []*awss3.ListObjectsV2Input
}

func newFakeS3() *fakeS3Client { return &fakeS3Client{objects: make(map[string]fakeS3Object)} }

func (f *fakeS3Client) PutObject(ctx context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, in)
	putErr := f.putErr
	key := aws.ToString(in.Key)
	if _, ok := f.objects[key]; ok {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"}
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if putErr != nil && !f.putErrAfterCommit {
		return nil, putErr
	}
	f.objects[key] = fakeS3Object{body: body, meta: cloneMap(in.Metadata), contentType: aws.ToString(in.ContentType), created: time.Now().UTC(), etag: "\"etag-" + strconv.Itoa(len(f.objects)) + "\"", version: "v1", encryption: in.ServerSideEncryption, kmsKey: aws.ToString(in.SSEKMSKeyId), customerAlgorithm: aws.ToString(in.SSECustomerAlgorithm), customerKeyMD5: aws.ToString(in.SSECustomerKeyMD5)}
	if putErr != nil {
		return nil, putErr
	}
	return &awss3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) HeadObject(ctx context.Context, in *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads = append(f.heads, in)
	if f.headErr != nil {
		return nil, f.headErr
	}
	o, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
	}
	if in.VersionId != nil && aws.ToString(in.VersionId) != o.version {
		return nil, &smithy.GenericAPIError{Code: "NoSuchVersion", Message: "version missing"}
	}
	return &awss3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(o.body))), Metadata: cloneMap(o.meta), ContentType: aws.String(o.contentType), LastModified: aws.Time(o.created), ETag: aws.String(o.etag), VersionId: aws.String(o.version), ServerSideEncryption: o.encryption, SSEKMSKeyId: aws.String(o.kmsKey), SSECustomerAlgorithm: aws.String(o.customerAlgorithm), SSECustomerKeyMD5: aws.String(o.customerKeyMD5)}, nil
}

func (f *fakeS3Client) GetObject(ctx context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, in)
	if f.getErr != nil {
		return nil, f.getErr
	}
	o, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(append([]byte(nil), o.body...)))}, nil
}

func (f *fakeS3Client) ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.lists = append(f.lists, in)
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if len(aws.ToString(in.Prefix)) == 0 || bytes.HasPrefix([]byte(key), []byte(aws.ToString(in.Prefix))) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if in.ContinuationToken != nil {
		start, _ = strconv.Atoi(aws.ToString(in.ContinuationToken))
	}
	if in.StartAfter != nil {
		for start < len(keys) && keys[start] <= aws.ToString(in.StartAfter) {
			start++
		}
	}
	max := int(aws.ToInt32(in.MaxKeys))
	if max <= 0 {
		max = 1000
	}
	end := start + max
	if end > len(keys) {
		end = len(keys)
	}
	contents := make([]awss3types.Object, 0, end-start)
	for _, key := range keys[start:end] {
		contents = append(contents, awss3types.Object{Key: aws.String(key)})
	}
	truncated := end < len(keys)
	out := &awss3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(truncated)}
	if truncated {
		out.NextContinuationToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

func (f *fakeS3Client) DeleteObject(ctx context.Context, in *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, in)
	deleteErr := f.deleteErr
	key := aws.ToString(in.Key)
	o, ok := f.objects[key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	if f.deleteRejectIfMatch {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "delete rejected"}
	}
	if in.VersionId != nil {
		if aws.ToString(in.VersionId) != o.version {
			return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "version mismatch"}
		}
	} else if in.IfMatch == nil || aws.ToString(in.IfMatch) != o.etag {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "etag mismatch"}
	}
	delete(f.objects, key)
	if deleteErr != nil {
		if !f.deleteErrAfterDelete {
			f.objects[key] = o
		}
		return nil, deleteErr
	}
	return &awss3.DeleteObjectOutput{}, nil
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func s3TestStore(t *testing.T, client *fakeS3Client, enc S3EncryptionConfig) *S3Store {
	t.Helper()
	store, err := NewS3Store(client, S3StoreConfig{Bucket: "bucket-a", Prefix: "objects", StorageSecurityDomain: "domain-a", Encryption: enc, Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestS3StorePutOpenConflictAndLostAck(t *testing.T) {
	client := newFakeS3()
	store := s3TestStore(t, client, S3EncryptionConfig{Mode: S3EncryptionSSES3})
	body := []byte("payload")
	metadata := metadataFor(body)
	info, err := store.PutImmutable(context.Background(), "a/item", bytes.NewReader(body), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.puts) != 1 || aws.ToString(client.puts[0].IfNoneMatch) != "*" || client.puts[0].ServerSideEncryption != awss3types.ServerSideEncryptionAes256 {
		t.Fatalf("put precondition/encryption missing: %#v", client.puts[0])
	}
	opened, err := store.Open(context.Background(), "a/item")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(opened.Body)
	opened.Body.Close()
	if err != nil || !bytes.Equal(got, body) || opened.Info != info {
		t.Fatalf("open = %q, %+v, %v", got, opened.Info, err)
	}
	replay, err := store.PutImmutable(context.Background(), "a/item", bytes.NewReader(body), metadata)
	if err != nil || !sameIdentity(replay, info) {
		t.Fatalf("exact replay = %+v, %v", replay, err)
	}
	changed := metadata
	changed.ContentType = "text/plain"
	if _, err := store.PutImmutable(context.Background(), "a/item", bytes.NewReader(body), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
	client.putErr = errors.New("connection reset")
	if _, err := store.PutImmutable(context.Background(), "a/lost", bytes.NewReader(body), metadata); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("transport error = %v", err)
	}
	client.putErr = errors.New("gateway timeout")
	client.putErrAfterCommit = true
	reconciled, err := store.PutImmutable(context.Background(), "a/committed", bytes.NewReader(body), metadata)
	if err != nil || reconciled.Digest != metadata.Digest {
		t.Fatalf("ambiguous committed put = %+v, %v", reconciled, err)
	}
	client.putErr = context.Canceled
	client.putErrAfterCommit = true
	reconciled, err = store.PutImmutable(context.Background(), "a/canceled-committed", bytes.NewReader(body), metadata)
	if err != nil || reconciled.Digest != metadata.Digest {
		t.Fatalf("canceled committed put = %+v, %v", reconciled, err)
	}
	client.putErr = nil
	client.putErrAfterCommit = false
}

func TestS3StoreExactIdentityUsesVersionOrETag(t *testing.T) {
	client := newFakeS3()
	store := s3TestStore(t, client, S3EncryptionConfig{Mode: S3EncryptionSSES3})
	body := []byte("exact")
	info, err := store.PutImmutable(context.Background(), "exact/versioned", bytes.NewReader(body), metadataFor(body))
	if err != nil {
		t.Fatal(err)
	}
	if info.VersionID != "v1" || info.ETag == "" {
		t.Fatalf("put identity = %+v", info)
	}
	if err := store.DeleteExact(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	if got := client.deletes[len(client.deletes)-1]; got.VersionId == nil || aws.ToString(got.VersionId) != "v1" || got.IfMatch != nil {
		t.Fatalf("versioned delete input = %#v", got)
	}

	// A mutable null version must be fenced with the observed ETag, including
	// when the object was opened rather than listed.
	body = []byte("null-version")
	if _, err := store.PutImmutable(context.Background(), "exact/null", bytes.NewReader(body), metadataFor(body)); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	nullObject := client.objects["objects/exact/null"]
	nullObject.version = "null"
	client.objects["objects/exact/null"] = nullObject
	client.mu.Unlock()
	opened, err := store.Open(context.Background(), "exact/null")
	if err != nil {
		t.Fatal(err)
	}
	if got := client.gets[len(client.gets)-1]; got.VersionId != nil || aws.ToString(got.IfMatch) != nullObject.etag {
		t.Fatalf("null-version open input = %#v", got)
	}
	if err := store.DeleteExact(context.Background(), opened.Info); err != nil {
		t.Fatal(err)
	}
	if got := client.deletes[len(client.deletes)-1]; got.VersionId != nil || aws.ToString(got.IfMatch) != nullObject.etag {
		t.Fatalf("null-version delete input = %#v", got)
	}
}

func TestS3StoreExactDeleteReconcilesObservedVersion(t *testing.T) {
	client := newFakeS3()
	store := s3TestStore(t, client, S3EncryptionConfig{Mode: S3EncryptionSSES3})
	body := []byte("ambiguous-exact")
	info, err := store.PutImmutable(context.Background(), "exact/ambiguous", bytes.NewReader(body), metadataFor(body))
	if err != nil {
		t.Fatal(err)
	}
	client.deleteErr = &smithy.GenericAPIError{Code: "InternalError", Message: "uncertain"}
	client.deleteErrAfterDelete = true
	if err := store.DeleteExact(context.Background(), info); err != nil {
		t.Fatalf("ambiguous versioned delete = %v", err)
	}
	if got := client.deletes[len(client.deletes)-1]; got.VersionId == nil || aws.ToString(got.VersionId) != info.VersionID {
		t.Fatalf("delete input = %#v", got)
	}
}

func TestS3StoreSpoolsWithoutExposingUnverifiedBytes(t *testing.T) {
	tempDir := t.TempDir()
	client := newFakeS3()
	store, err := NewS3Store(client, S3StoreConfig{Bucket: "bucket-a", Prefix: "objects", StorageSecurityDomain: "domain-a", TempDir: tempDir, Encryption: S3EncryptionConfig{Mode: S3EncryptionSSES3}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("spooled")
	if _, err := store.PutImmutable(context.Background(), "spooled", bytes.NewReader(body), metadataFor(body)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("put spool cleanup entries=%d err=%v", len(entries), err)
	}
	opened, err := store.Open(context.Background(), "spooled")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(tempDir)
	if len(entries) != 1 {
		t.Fatalf("verified open should retain one reader spool, entries=%d", len(entries))
	}
	if err := opened.Body.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(tempDir)
	if len(entries) != 0 {
		t.Fatalf("reader close did not remove spool: %d", len(entries))
	}
}

func TestS3StoreKMSAndStrictConfiguration(t *testing.T) {
	client := newFakeS3()
	store := s3TestStore(t, client, S3EncryptionConfig{Mode: S3EncryptionSSEKMS, OpaqueKeyRef: "kms:logical", ProviderKey: "arn:aws:kms:us-east-1:123:key/real"})
	body := []byte("x")
	if _, err := store.PutImmutable(context.Background(), "item", bytes.NewReader(body), metadataFor(body)); err != nil {
		t.Fatal(err)
	}
	put := client.puts[0]
	if put.ServerSideEncryption != awss3types.ServerSideEncryptionAwsKms || aws.ToString(put.SSEKMSKeyId) != "arn:aws:kms:us-east-1:123:key/real" || put.Metadata[s3EncryptionRefKey] != "kms:logical" {
		t.Fatalf("KMS binding = %#v", put)
	}
	for _, cfg := range []S3StoreConfig{{Bucket: "bad/name", StorageSecurityDomain: "domain-a"}, {Bucket: "bucket-a", Prefix: "../x", StorageSecurityDomain: "domain-a"}, {Bucket: "bucket-a", StorageSecurityDomain: "domain-a", Encryption: S3EncryptionConfig{Mode: S3EncryptionSSEKMS, OpaqueKeyRef: "opaque"}}} {
		if _, err := NewS3Store(client, cfg); !errors.Is(err, ErrInvalid) {
			t.Errorf("config %#v error = %v", cfg, err)
		}
	}
}

func TestS3StoreSSECustomerEncryptionHeadersAndEvidence(t *testing.T) {
	client := newFakeS3()
	keyBytes := bytes.Repeat([]byte{0x42}, 32)
	customerKey := base64.StdEncoding.EncodeToString(keyBytes)
	keyMD5Sum := md5.Sum(keyBytes)
	keyMD5 := base64.StdEncoding.EncodeToString(keyMD5Sum[:])
	store := s3TestStore(t, client, S3EncryptionConfig{Mode: S3EncryptionSSEC, OpaqueKeyRef: "customer-epoch-1", CustomerKey: customerKey})
	body := []byte("sse-c")
	if _, err := store.PutImmutable(context.Background(), "customer/item", bytes.NewReader(body), metadataFor(body)); err != nil {
		t.Fatal(err)
	}
	put := client.puts[0]
	if put.ServerSideEncryption != "" || aws.ToString(put.SSECustomerAlgorithm) != "AES256" || aws.ToString(put.SSECustomerKey) != customerKey || aws.ToString(put.SSECustomerKeyMD5) != keyMD5 {
		t.Fatal("SSE-C PUT encryption headers were not applied")
	}
	if put.Metadata[s3EncryptionRefKey] != "customer-epoch-1" || strings.Contains(fmt.Sprint(put.Metadata), customerKey) || strings.Contains(fmt.Sprint(put.Metadata), keyMD5) {
		t.Fatalf("SSE-C metadata exposed key material: %#v", put.Metadata)
	}
	assertSSECustomerHead := func() {
		t.Helper()
		if len(client.heads) == 0 {
			t.Fatal("SSE-C operation did not issue HEAD")
		}
		head := client.heads[len(client.heads)-1]
		if aws.ToString(head.SSECustomerAlgorithm) != "AES256" || aws.ToString(head.SSECustomerKey) != customerKey || aws.ToString(head.SSECustomerKeyMD5) != keyMD5 {
			t.Fatal("SSE-C HEAD encryption headers were not applied")
		}
	}
	assertSSECustomerHead()
	opened, err := store.Open(context.Background(), "customer/item")
	if err != nil {
		t.Fatal(err)
	}
	opened.Body.Close()
	assertSSECustomerHead()
	get := client.gets[len(client.gets)-1]
	if aws.ToString(get.SSECustomerAlgorithm) != "AES256" || aws.ToString(get.SSECustomerKey) != customerKey || aws.ToString(get.SSECustomerKeyMD5) != keyMD5 {
		t.Fatal("SSE-C GET encryption headers were not applied")
	}
	if _, _, err := store.List(context.Background(), "customer", "", 10); err != nil {
		t.Fatal(err)
	}
	assertSSECustomerHead()
	if err := store.Delete(context.Background(), "customer/item"); err != nil {
		t.Fatal(err)
	}
	assertSSECustomerHead()
}

func TestS3StoreSSECustomerKeyValidationAndMutualExclusion(t *testing.T) {
	client := newFakeS3()
	valid := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x7f}, 32))
	for name, encryption := range map[string]S3EncryptionConfig{
		"invalid base64": {Mode: S3EncryptionSSEC, OpaqueKeyRef: "epoch", CustomerKey: "not-base64"},
		"wrong length":   {Mode: S3EncryptionSSEC, OpaqueKeyRef: "epoch", CustomerKey: base64.StdEncoding.EncodeToString([]byte("short"))},
		"provider key":   {Mode: S3EncryptionSSEC, OpaqueKeyRef: "epoch", ProviderKey: "provider", CustomerKey: valid},
		"missing epoch":  {Mode: S3EncryptionSSEC, CustomerKey: valid},
		"AES256 key":     {Mode: S3EncryptionSSES3, CustomerKey: valid},
		"KMS key":        {Mode: S3EncryptionSSEKMS, OpaqueKeyRef: "epoch", ProviderKey: "provider", CustomerKey: valid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewS3Store(client, S3StoreConfig{Bucket: "bucket-a", StorageSecurityDomain: "domain-a", Encryption: encryption})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), valid) {
				t.Fatalf("error exposed customer key: %v", err)
			}
		})
	}
}

func TestS3StoreListIsolationTamperDeleteAndCancellation(t *testing.T) {
	client := newFakeS3()
	store := s3TestStore(t, client, S3EncryptionConfig{Mode: S3EncryptionSSES3})
	for _, key := range []string{"a/c", "a/a", "a/b", "other/x"} {
		body := []byte(key)
		if _, err := store.PutImmutable(context.Background(), key, bytes.NewReader(body), metadataFor(body)); err != nil {
			t.Fatal(err)
		}
	}
	foreignBody := []byte("a/foreign")
	foreignMeta := metadataFor(foreignBody)
	foreignMeta.StorageSecurityDomain = "domain-b"
	client.mu.Lock()
	client.objects["objects/a/aa-foreign"] = fakeS3Object{body: foreignBody, meta: map[string]string{s3DomainMetadataKey: "domain-b", s3DigestMetadataKey: identity(foreignBody), s3SizeMetadataKey: strconv.Itoa(len(foreignBody)), s3MetadataDigestKey: identity([]byte("foreign")), s3CreatedAtMetadataKey: time.Now().UTC().Format(time.RFC3339Nano)}, contentType: "application/octet-stream", created: time.Now().UTC(), etag: "\"foreign\"", version: "v1", encryption: awss3types.ServerSideEncryptionAes256}
	client.mu.Unlock()
	page, cursor, err := store.List(context.Background(), "a", "", 2)
	if err != nil || len(page) != 2 || page[0].Key != "a/a" || page[1].Key != "a/b" || cursor != "a/b" {
		t.Fatalf("first page = %#v %q %v", page, cursor, err)
	}
	page, cursor, err = store.List(context.Background(), "a", cursor, 2)
	if err != nil || len(page) != 1 || page[0].Key != "a/c" || cursor != "" {
		t.Fatalf("second page = %#v %q %v", page, cursor, err)
	}
	client.mu.Lock()
	client.objects["objects/a/c"] = fakeS3Object{body: []byte("tampered"), meta: cloneMap(client.objects["objects/a/c"].meta), contentType: "application/octet-stream", created: time.Now().UTC(), etag: "\"tampered\"", version: "v2", encryption: awss3types.ServerSideEncryptionAes256}
	client.mu.Unlock()
	if _, err := store.Open(context.Background(), "a/c"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered open = %v", err)
	}
	encryptionBody := []byte("encryption")
	if _, err := store.PutImmutable(context.Background(), "a/encryption", bytes.NewReader(encryptionBody), metadataFor(encryptionBody)); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	encryptionObject := client.objects["objects/a/encryption"]
	encryptionObject.encryption = ""
	client.objects["objects/a/encryption"] = encryptionObject
	client.mu.Unlock()
	if _, err := store.Open(context.Background(), "a/encryption"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("same-domain encryption tamper = %v", err)
	}
	if _, _, err := store.List(context.Background(), "a", "", 10); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("same-domain encryption list tamper = %v", err)
	}
	deleteBody := []byte("delete")
	if _, err := store.PutImmutable(context.Background(), "delete/me", bytes.NewReader(deleteBody), metadataFor(deleteBody)); err != nil {
		t.Fatal(err)
	}
	client.deleteRejectIfMatch = true
	if err := store.Delete(context.Background(), "delete/me"); !errors.Is(err, ErrConflict) {
		t.Fatalf("failing delete fence = %v", err)
	}
	client.deleteRejectIfMatch = false
	client.deleteErr = &smithy.GenericAPIError{Code: "InternalError", Message: "uncertain"}
	if err := store.Delete(context.Background(), "delete/me"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous delete = %v", err)
	}
	if len(client.deletes) == 0 || aws.ToString(client.deletes[len(client.deletes)-1].IfMatch) == "" {
		t.Fatalf("delete did not carry ETag fence: %#v", client.deletes)
	}
	client.deleteErr = nil
	if err := store.Delete(context.Background(), "delete/me"); err != nil {
		t.Fatalf("delete retry = %v", err)
	}
	deleteCanceledBody := []byte("delete-canceled")
	if _, err := store.PutImmutable(context.Background(), "delete/canceled", bytes.NewReader(deleteCanceledBody), metadataFor(deleteCanceledBody)); err != nil {
		t.Fatal(err)
	}
	client.deleteErr = context.Canceled
	client.deleteErrAfterDelete = true
	if err := store.Delete(context.Background(), "delete/canceled"); err != nil {
		t.Fatalf("canceled committed delete = %v", err)
	}
	if err := store.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.List(canceled, "a", "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list = %v", err)
	}
}
