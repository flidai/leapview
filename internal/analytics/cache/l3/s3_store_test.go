package l3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
)

type adapterS3Object struct {
	body        []byte
	meta        map[string]string
	etag        string
	version     string
	created     time.Time
	contentType string
	encryption  awss3types.ServerSideEncryption
}

type adapterS3Client struct {
	objects    map[string]adapterS3Object
	lastDelete *awss3.DeleteObjectInput
}

func (f *adapterS3Client) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	key := aws.ToString(in.Key)
	if _, exists := f.objects[key]; exists {
		return nil, errors.New("precondition failed")
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	now := time.Unix(100, 0).UTC()
	f.objects[key] = adapterS3Object{body: body, meta: cloneAdapterMap(in.Metadata), etag: "etag-1", version: "v1", created: now, contentType: aws.ToString(in.ContentType), encryption: in.ServerSideEncryption}
	return &awss3.PutObjectOutput{ETag: aws.String("etag-1"), VersionId: aws.String("v1")}, nil
}

func (f *adapterS3Client) HeadObject(_ context.Context, in *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	o, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return &awss3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(o.body))), Metadata: cloneAdapterMap(o.meta), ContentType: aws.String(o.contentType), LastModified: aws.Time(o.created), ETag: aws.String(o.etag), VersionId: aws.String(o.version), ServerSideEncryption: o.encryption}, nil
}

func (f *adapterS3Client) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	o, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.body))}, nil
}

func (f *adapterS3Client) DeleteObject(_ context.Context, in *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	f.lastDelete = in
	key := aws.ToString(in.Key)
	o, ok := f.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	if in.VersionId != nil && aws.ToString(in.VersionId) != o.version || in.IfMatch != nil && aws.ToString(in.IfMatch) != o.etag {
		return nil, errors.New("precondition failed")
	}
	delete(f.objects, key)
	return &awss3.DeleteObjectOutput{}, nil
}

func (f *adapterS3Client) ListObjectsV2(_ context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if len(aws.ToString(in.Prefix)) == 0 || len(key) >= len(aws.ToString(in.Prefix)) && key[:len(aws.ToString(in.Prefix))] == aws.ToString(in.Prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	contents := make([]awss3types.Object, 0, len(keys))
	for _, key := range keys {
		contents = append(contents, awss3types.Object{Key: aws.String(key)})
	}
	return &awss3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(false)}, nil
}

func cloneAdapterMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func TestS3ObjectStoreAdapterDelegatesCanonicalMetadataAndIdentity(t *testing.T) {
	client := &adapterS3Client{objects: make(map[string]adapterS3Object)}
	domain := testDigest('d')
	store, err := NewS3ObjectStore(client, "bucket", "pool", domain)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("result")
	info, err := store.PutImmutable(t.Context(), "objects/"+domain, bytes.NewReader(body), ObjectMetadata{SecurityDomain: domain, Metadata: []byte(`{"z":1,"a":"x"}`), Digest: digestBytes(body), Size: int64(len(body))})
	if err != nil {
		t.Fatal(err)
	}
	if info.MetadataDigest != digestBytes([]byte(`{"a":"x","z":1}`)) || info.VersionID != "v1" || info.ETag != "etag-1" {
		t.Fatalf("put info = %+v", info)
	}
	opened, err := store.Open(t.Context(), info.Key)
	if err != nil {
		t.Fatal(err)
	}
	opened.Body.Close()
	page, next, err := store.List(t.Context(), "objects/", "", 10)
	if err != nil || len(page) != 1 || next != "" || page[0].VersionID != "v1" {
		t.Fatalf("list = %+v next=%q err=%v", page, next, err)
	}
	if err := store.DeleteExact(t.Context(), page[0]); err != nil {
		t.Fatal(err)
	}
	if client.lastDelete == nil || aws.ToString(client.lastDelete.VersionId) != "v1" || client.lastDelete.IfMatch != nil {
		t.Fatalf("delete input = %#v", client.lastDelete)
	}
}

func TestS3ObjectStoreAdapterRequiresPrecommittedBodyEvidence(t *testing.T) {
	client := &adapterS3Client{objects: make(map[string]adapterS3Object)}
	domain := testDigest('d')
	store, err := NewS3ObjectStore(client, "bucket", "pool", domain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutImmutable(t.Context(), "objects/"+domain, bytes.NewReader([]byte("result")), ObjectMetadata{SecurityDomain: domain, Metadata: []byte(`{}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing body evidence error = %v", err)
	}
}

var _ platformobjectstore.S3Client = (*adapterS3Client)(nil)
