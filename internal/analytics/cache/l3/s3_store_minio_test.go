//go:build ducklake_minio

package l3

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

const (
	l3MinIOImage  = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	l3MinIOUser   = "leapview"
	l3MinIOSecret = "leapview-conformance-secret"
)

// TestS3ObjectStoreMinIOConformance owns its disposable MinIO service and
// exercises the same immutable object protocol used by the target runtime.
// The lane is opt-in locally and required by CI's external-service task.
func TestS3ObjectStoreMinIOConformance(t *testing.T) {
	ctx := context.Background()
	endpoint := startL3MinIO(t, ctx)
	client := l3MinIOClient(t, ctx, endpoint)
	const bucket = "leapview-l3-conformance"
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	prefix := "l3-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	store, err := NewS3ObjectStore(client, bucket, prefix)
	if err != nil {
		t.Fatal(err)
	}
	domain := testDigest('d')
	key1 := "objects/sd/" + domain + "/" + testDigest('a') + "/" + testDigest('b')
	key2 := "objects/sd/" + domain + "/" + testDigest('a') + "/" + testDigest('c')
	metadata := ObjectMetadata{SecurityDomain: domain, Metadata: []byte(`{"rows":1}`)}
	if _, err := store.PutImmutable(ctx, key1, strings.NewReader("one"), metadata); err != nil {
		l3MinIOSkipOrFail(t, "MinIO does not support explicit SSE-S3", err)
	}
	if _, err := store.PutImmutable(ctx, key2, strings.NewReader("two"), metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutImmutable(ctx, key1, strings.NewReader("one"), metadata); !errors.Is(err, ErrObjectExists) {
		t.Fatalf("duplicate immutable put error = %v, want ErrObjectExists", err)
	}
	obj, err := store.Open(ctx, key1)
	if err != nil {
		l3MinIOSkipOrFail(t, "MinIO did not return explicit SSE-S3 HEAD evidence", err)
	}
	if got := obj.Info.MetadataDigest; got != digestBytes([]byte(`{"rows":1}`)) {
		t.Fatalf("metadata digest = %q", got)
	}
	_ = obj.Body.Close()

	first, next, err := store.List(ctx, "objects/sd/"+domain, "", 1)
	if err != nil || len(first) != 1 || next == "" {
		t.Fatalf("first page objects=%+v next=%q err=%v", first, next, err)
	}
	second, next2, err := store.List(ctx, "objects/sd/"+domain, next, 1)
	if err != nil || len(second) != 1 || next2 != "" {
		t.Fatalf("second page objects=%+v next=%q err=%v", second, next2, err)
	}
	if err := store.Delete(ctx, key1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, key1); err == nil {
		t.Fatal("deleted MinIO object reopened")
	}

	// A successful provider write followed by a lost acknowledgement must be
	// reconciled by reopening the exact immutable key.
	lostStore, err := NewS3ObjectStore(&l3LostAckClient{S3Client: client}, bucket, prefix)
	if err != nil {
		t.Fatal(err)
	}
	key3 := "objects/sd/" + domain + "/" + testDigest('a') + "/" + testDigest('e')
	if _, err := lostStore.PutImmutable(ctx, key3, strings.NewReader("three"), metadata); !errors.Is(err, ErrObjectAmbiguous) {
		t.Fatalf("lost acknowledgement error = %v, want ErrObjectAmbiguous", err)
	}
	opened, err := lostStore.Open(ctx, key3)
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Body.Close()

}

type l3LostAckClient struct {
	S3Client
	acked bool
}

func (c *l3LostAckClient) PutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	out, err := c.S3Client.PutObject(ctx, in, opts...)
	if err != nil || c.acked {
		return out, err
	}
	c.acked = true
	return nil, errors.New("lost MinIO PUT acknowledgement")
}

func startL3MinIO(t *testing.T, ctx context.Context) string {
	t.Helper()
	if !l3MinIORequired() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	testKMSKey := "leapview-sse-key:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("0", 32)))
	container, err := tcminio.Run(ctx, l3MinIOImage, tcminio.WithUsername(l3MinIOUser), tcminio.WithPassword(l3MinIOSecret), testcontainers.WithEnv(map[string]string{"MINIO_KMS_SECRET_KEY": testKMSKey}), testcontainers.WithLogger(log.TestLogger(t)))
	if err != nil {
		l3MinIOSkipOrFail(t, "start MinIO conformance container", err)
	}
	testcontainers.CleanupContainer(t, container)
	connection, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return "http://" + strings.TrimRight(connection, "/")
}

func l3MinIOClient(t *testing.T, ctx context.Context, endpoint string) *awss3.Client {
	t.Helper()
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(l3MinIOUser, l3MinIOSecret, "")))
	if err != nil {
		t.Fatal(err)
	}
	return awss3.NewFromConfig(config, func(options *awss3.Options) { options.BaseEndpoint = aws.String(endpoint); options.UsePathStyle = true })
}

func l3MinIORequired() bool {
	return strings.TrimSpace(os.Getenv("LEAPVIEW_MINIO_CONFORMANCE_REQUIRED")) != "" || strings.TrimSpace(os.Getenv("CI")) != ""
}

func l3MinIOSkipOrFail(t *testing.T, reason string, err error) {
	t.Helper()
	if l3MinIORequired() {
		t.Fatalf("%s: %v", reason, err)
	}
	t.Skipf("%s: %v", reason, err)
}
