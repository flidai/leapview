package l3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type fakeS3Object struct {
	body     []byte
	metadata map[string]string
	updated  time.Time
	etag     string
	encrypt  awss3types.ServerSideEncryption
	kmsKey   string
}

type fakeS3Client struct {
	objects    map[string]fakeS3Object
	lastDelete *awss3.DeleteObjectInput
	nilList    bool
}

func (f *fakeS3Client) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	if _, exists := f.objects[*in.Key]; exists {
		return nil, errors.New("precondition failed")
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	kmsKey := ""
	if in.SSEKMSKeyId != nil {
		kmsKey = *in.SSEKMSKeyId
	}
	f.objects[*in.Key] = fakeS3Object{body: body, metadata: in.Metadata, updated: time.Unix(100, 0).UTC(), etag: "etag", encrypt: in.ServerSideEncryption, kmsKey: kmsKey}
	return &awss3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) HeadObject(_ context.Context, in *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	object, ok := f.objects[*in.Key]
	if !ok {
		return nil, errors.New("not found")
	}
	return &awss3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(object.body))), Metadata: object.metadata, LastModified: aws.Time(object.updated), ETag: aws.String(object.etag), ServerSideEncryption: object.encrypt, SSEKMSKeyId: aws.String(object.kmsKey)}, nil
}

func (f *fakeS3Client) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	object, ok := f.objects[*in.Key]
	if !ok {
		return nil, errors.New("not found")
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(object.body))}, nil
}

func (f *fakeS3Client) DeleteObject(_ context.Context, in *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	f.lastDelete = in
	object, ok := f.objects[*in.Key]
	if in.IfMatch != nil && (!ok || *in.IfMatch != object.etag) {
		return nil, errors.New("precondition failed")
	}
	delete(f.objects, *in.Key)
	return &awss3.DeleteObjectOutput{}, nil
}

func (f *fakeS3Client) ListObjectsV2(_ context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	if f.nilList {
		return nil, nil
	}
	after := ""
	if in.StartAfter != nil {
		after = *in.StartAfter
	}
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, *in.Prefix) && (after == "" || key > after) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	limit := int(*in.MaxKeys)
	truncated := len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	contents := make([]awss3types.Object, 0, len(keys))
	for _, key := range keys {
		contents = append(contents, awss3types.Object{Key: aws.String(key)})
	}
	return &awss3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(truncated)}, nil
}

func TestS3ObjectStoreRejectsEmptyListResponse(t *testing.T) {
	client := &fakeS3Client{objects: make(map[string]fakeS3Object), nilList: true}
	store, err := NewS3ObjectStore(client, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.List(t.Context(), "cache/l3/sd/"+testDigest('d'), "", 1); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("empty LIST response error = %v, want object corrupt", err)
	}
}

func TestS3ObjectStoreNullVersionUsesETagDeletePrecondition(t *testing.T) {
	client := &fakeS3Client{objects: map[string]fakeS3Object{
		"pool/cache/object": {etag: "current-etag"},
	}}
	store, err := NewS3ObjectStore(client, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteExact(t.Context(), ObjectInfo{Key: "cache/object", VersionID: "null", ETag: "observed-etag"}); err == nil {
		t.Fatal("mutable null-version object was deleted without matching its observed ETag")
	}
	if client.lastDelete == nil || client.lastDelete.VersionId != nil || client.lastDelete.IfMatch == nil || *client.lastDelete.IfMatch != "observed-etag" {
		t.Fatalf("null-version delete condition = %#v, want ETag-only", client.lastDelete)
	}
}

func TestS3ObjectStoreCreateOnlyExactReadAndPage(t *testing.T) {
	client := &fakeS3Client{objects: make(map[string]fakeS3Object)}
	store, err := NewS3ObjectStore(client, "bucket", "pool")
	if err != nil {
		t.Fatal(err)
	}
	domain := testDigest('d')
	key := "objects/sd/" + domain + "/" + testDigest('a') + "/" + testDigest('b')
	metadata := ObjectMetadata{SecurityDomain: domain, Metadata: []byte(`{"z":1,"a":"x"}`)}
	info, err := store.PutImmutable(t.Context(), key, strings.NewReader("result"), metadata)
	if err != nil || info.Digest != digestBytes([]byte("result")) {
		t.Fatalf("put info=%+v err=%v", info, err)
	}
	if _, err := store.PutImmutable(t.Context(), key, strings.NewReader("result"), metadata); !errors.Is(err, ErrObjectAmbiguous) {
		t.Fatalf("duplicate put error=%v", err)
	}
	if got := client.objects["pool/"+key].metadata["leapview-metadata-digest"]; got != digestBytes([]byte(`{"a":"x","z":1}`)) {
		t.Fatalf("metadata digest header = %q", got)
	}
	if _, raw := client.objects["pool/"+key].metadata["leapview-metadata"]; raw {
		t.Fatal("raw metadata was copied into S3 user metadata")
	}
	object, err := store.Open(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(object.Body)
	_ = object.Body.Close()
	if err != nil || string(body) != "result" || object.Info.SecurityDomain != domain || object.Info.Digest != digestBytes([]byte("result")) {
		t.Fatalf("opened object info=%+v body=%q err=%v", object.Info, body, err)
	}
	objects, next, err := store.List(t.Context(), "objects/sd/"+domain, "", 1)
	if err != nil || len(objects) != 1 || next != "" {
		t.Fatalf("list objects=%+v next=%q err=%v", objects, next, err)
	}
	if err := store.DeleteExact(t.Context(), object.Info); err != nil {
		t.Fatal(err)
	}
	if client.lastDelete == nil || client.lastDelete.IfMatch == nil || *client.lastDelete.IfMatch != "etag" {
		t.Fatalf("delete precondition = %#v, want exact ETag", client.lastDelete)
	}
}

func TestS3ObjectStoreResolvedEncryptionBindsOpaqueReference(t *testing.T) {
	client := &fakeS3Client{objects: make(map[string]fakeS3Object)}
	domain := testDigest('d')
	store, err := NewS3ObjectStoreWithResolvedEncryption(client, "bucket", "pool", "kms:logical", "arn:aws:kms:us-east-1:123:key/abc")
	if err != nil {
		t.Fatal(err)
	}
	key := "objects/sd/" + domain + "/" + testDigest('a') + "/" + testDigest('b')
	if _, err := store.PutImmutable(t.Context(), key, strings.NewReader("result"), ObjectMetadata{SecurityDomain: domain, Metadata: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	obj := client.objects["pool/"+key]
	if obj.encrypt != awss3types.ServerSideEncryptionAwsKms || obj.kmsKey != "arn:aws:kms:us-east-1:123:key/abc" {
		t.Fatalf("put encryption = %v key=%q", obj.encrypt, obj.kmsKey)
	}
	if obj.metadata["leapview-encryption-key-ref"] != "kms:logical" {
		t.Fatalf("opaque key reference metadata = %q", obj.metadata["leapview-encryption-key-ref"])
	}
	if _, err := store.Open(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := NewS3ObjectStoreWithEncryption(client, "bucket", "pool", "kms:logical"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unresolved opaque key accepted: %v", err)
	}
	if _, err := NewS3ObjectStoreWithResolvedEncryption(client, "bucket", "pool", "kms:logical", "kms:logical"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("opaque key passed through resolver: %v", err)
	}
}

func TestClassifyS3ConditionalCreateErrors(t *testing.T) {
	response := func(status int) error {
		return &smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}}, Err: errors.New("s3")}
	}
	if !errors.Is(classifyPutError(response(412)), ErrObjectExists) || !errors.Is(classifyPutError(response(409)), ErrObjectExists) {
		t.Fatal("conditional responses must classify as object exists")
	}
	if errors.Is(classifyPutError(response(403)), ErrObjectAmbiguous) || errors.Is(classifyPutError(response(403)), ErrObjectExists) {
		t.Fatal("permission response must remain a provider error")
	}
	if !errors.Is(classifyPutError(response(503)), ErrObjectAmbiguous) {
		t.Fatal("server response must classify as ambiguous")
	}
	if !errors.Is(classifyPutError(&smithy.GenericAPIError{Code: "PreconditionFailed"}), ErrObjectExists) {
		t.Fatal("conditional API error must classify as object exists")
	}
}
