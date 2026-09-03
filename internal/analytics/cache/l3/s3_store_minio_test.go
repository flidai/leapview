//go:build ducklake_minio

package l3

import (
	"context"
	"encoding/base64"
	"io"
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

// TestS3ObjectStoreMinIOAdapterSmoke exercises only the L3-specific metadata
// and capability translation. The platform package owns the exhaustive S3
// protocol/error conformance tests; this lane verifies the real adapter path.
func TestS3ObjectStoreMinIOAdapterSmoke(t *testing.T) {
	ctx := context.Background()
	endpoint := startL3MinIO(t, ctx)
	client := l3MinIOClient(t, ctx, endpoint)
	const bucket = "leapview-l3-conformance"
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	prefix := "l3-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	domain := testDigest('d')
	store, err := NewS3ObjectStore(client, bucket, prefix, domain)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("adapter-smoke")
	key := "objects/sd/" + domain + "/" + testDigest('a')
	info, err := store.PutImmutable(ctx, key, strings.NewReader(string(body)), ObjectMetadata{SecurityDomain: domain, Metadata: []byte(`{"rows":1}`), Digest: digestBytes(body), Size: int64(len(body))})
	if err != nil {
		l3MinIOSkipOrFail(t, "MinIO does not support explicit SSE-S3", err)
	}
	opened, err := store.Open(ctx, key)
	if err != nil {
		l3MinIOSkipOrFail(t, "MinIO adapter object read", err)
	}
	readBody, readErr := io.ReadAll(opened.Body)
	opened.Body.Close()
	if readErr != nil || string(readBody) != string(body) || opened.Info.MetadataDigest != digestBytes([]byte(`{"rows":1}`)) {
		t.Fatalf("opened body/info = %q/%+v err=%v", readBody, opened.Info, readErr)
	}
	page, next, err := store.List(ctx, "objects/sd/"+domain, "", 1)
	if err != nil || len(page) != 1 || next != "" {
		t.Fatalf("list = %+v next=%q err=%v", page, next, err)
	}
	if err := store.DeleteExact(ctx, info); err != nil {
		t.Fatal(err)
	}
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
