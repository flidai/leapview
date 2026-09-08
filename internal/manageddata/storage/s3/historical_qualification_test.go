//go:build fai520qualification

package s3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
)

const historicalProviderImage = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"

// This is a provider experiment, not an alternate recovery-admission validator.
// This validates historical managed-object retrieval. It does not prove successful physical disaster recovery.
func TestFAI520HistoricalManagedObjectRetrieval(t *testing.T) {
	ctx, client, clientFor, bucket, endpoint := historicalProvider(t)
	store, err := manageds3.New(client, awss3.NewPresignClient(client), manageds3.Config{Bucket: bucket, Prefix: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	qualifyHistoricalObject(t, ctx, client, clientFor, bucket, endpoint, store)
}

func historicalProvider(t *testing.T) (context.Context, *awss3.Client, func(string) *awss3.Client, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	user, secret := "q"+strings.ReplaceAll(uuid.NewString(), "-", ""), uuid.NewString()
	// Liveness alone can pass before S3 initialization completes.
	container, err := tcminio.Run(ctx, historicalProviderImage, tcminio.WithUsername(user), tcminio.WithPassword(secret),
		testcontainers.WithTmpfs(map[string]string{"/data": "rw,size=1g"}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000").WithStartupTimeout(time.Minute)))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	} // Explicit qualification must never silently skip.
	address, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + strings.TrimRight(address, "/")
	clientFor := func(password string) *awss3.Client {
		return awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(user, password, ""), RetryMaxAttempts: 1, HTTPClient: historicalHTTPClient{t: t}})
	}
	client := clientFor(secret)
	bucket := "qualification-" + uuid.NewString()
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{Bucket: &bucket, VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	return ctx, client, clientFor, bucket, endpoint
}

// Keep setup failures diagnosable without printing credentials, URLs, or bodies.
type historicalHTTPClient struct{ t *testing.T }

func (c historicalHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Log("provider HTTP transport failure")
	}
	if response != nil && response.StatusCode >= 400 {
		c.t.Logf("provider HTTP %s status %d", request.Method, response.StatusCode)
	}
	return response, err
}

func qualifyHistoricalObject(t *testing.T, ctx context.Context, client *awss3.Client, clientFor func(string) *awss3.Client, bucket, endpoint string, store storage.BlobStore) {
	put := func(key string, body []byte) string {
		t.Helper()
		out, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: &bucket, Key: &key, Body: bytes.NewReader(body)})
		if err != nil {
			t.Fatal(err)
		}
		id := aws.ToString(out.VersionId)
		if id == "" || id == "null" {
			t.Fatal("provider did not return an immutable version")
		}
		return id
	}
	baseline := func(key, version string, body []byte) {
		t.Helper()
		selected := historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: key, Version: version}
		digest, reason := observeHistoricalBytes(ctx, client, selected, selected, blobFor(body))
		if reason != "verified" {
			t.Fatalf("baseline bytes: %s", reason)
		}
		record, err := json.Marshal(struct {
			Selection historicalSelection
			Size      int
			SHA256    string
			Observed  time.Time
		}{selected, len(body), digest, time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("baseline", string(record))
	}
	a, b := []byte("managed revision A\n"), []byte("managed revision B\n")
	blobA, err := store.Put(ctx, blobFor(a), bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	blobB, err := store.Put(ctx, blobFor(b), bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimPrefix(blobA.URI, "s3://"+bucket+"/")
	head, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		t.Fatal(err)
	}
	versionA := aws.ToString(head.VersionId)
	if versionA == "" || versionA == "null" {
		t.Fatal("unversioned baseline")
	}
	baseline(key, versionA, a)
	keyB := strings.TrimPrefix(blobB.URI, "s3://"+bucket+"/")
	headB, err := client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &bucket, Key: &keyB})
	if err != nil {
		t.Fatal(err)
	}
	baseline(keyB, aws.ToString(headB.VersionId), b)
	// Production writes use distinct content keys. Raw provider replacement at
	// A's key deliberately models corruption, not a supported managed write.
	versionB := put(key, b)
	corrupt := put(key, bytes.Repeat([]byte("x"), len(a)))
	sentinelKey := "project-b/sentinel"
	sentinelBody := []byte("unrelated immutable sentinel")
	sentinelVersion := put(sentinelKey, sentinelBody)
	baseline(sentinelKey, sentinelVersion, sentinelBody)
	sentinelLatestBody := []byte("unrelated later version")
	sentinelLatestVersion := put(sentinelKey, sentinelLatestBody)
	baseline(sentinelKey, sentinelLatestVersion, sentinelLatestBody)
	if _, err := client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &bucket, Key: &key}); err != nil {
		t.Fatal(err)
	}
	if latest, err := client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &bucket, Key: &key}); err == nil {
		_ = latest.Body.Close()
		t.Fatal("latest unexpectedly survived delete marker")
	} else {
		requireHistoricalCode(t, err, "NoSuchKey")
	}
	inventory := func() *awss3.ListObjectVersionsOutput {
		t.Helper()
		out, err := client.ListObjectVersions(ctx, &awss3.ListObjectVersionsInput{Bucket: &bucket})
		if err != nil || aws.ToBool(out.IsTruncated) {
			t.Fatalf("bounded inventory: %v", err)
		}
		return out
	}
	before := inventory()
	selection := historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: key, Version: versionA}
	// Emit safe observation records in go test -json output, never SDK errors
	// or credentials. These records are not a recovery-set evidence format.
	retrieve := func(t *testing.T, name string, c *awss3.Client, selected historicalSelection, expected storage.Blob, want string) {
		t.Helper()
		started := time.Now().UTC()
		digest, reason := observeHistoricalBytes(ctx, c, selection, selected, expected)
		record, err := json.Marshal(struct {
			Case               string
			Selection          historicalSelection
			SHA256             string
			ExpectedSize       int64
			ExpectedSHA256     string
			Started, Completed time.Time
			Result             string
		}{name, selected, digest, expected.Size, expected.SHA256, started, time.Now().UTC(), reason})
		if err != nil {
			t.Fatal(err)
		}
		t.Log(string(record))
		if reason != want {
			t.Fatalf("%s: got %s, want %s", name, reason, want)
		}
	}
	retrieve(t, "exact historical A", client, selection, blobA, "verified")
	for _, tc := range []struct{ name, version, want string }{{"missing version", uuid.NewString(), "NoSuchVersion"}, {"empty version", "", "missing_version"}, {"null version", "null", "missing_version"}, {"wrong version", versionB, "integrity_mismatch"}, {"corrupted bytes", corrupt, "integrity_mismatch"}} {
		t.Run(tc.name, func(t *testing.T) {
			selected := selection
			selected.Version = tc.version
			retrieve(t, tc.name, client, selected, blobA, tc.want)
		})
	}
	for _, field := range []string{"endpoint", "region", "bucket", "prefix"} {
		t.Run("substituted "+field, func(t *testing.T) {
			selected := selection
			switch field {
			case "endpoint":
				selected.Endpoint = "http://untrusted.invalid"
			case "region":
				selected.Region = "other"
			case "bucket":
				selected.Bucket = "other"
			case "prefix":
				selected.Key = sentinelKey
			}
			retrieve(t, field, client, selected, blobA, "namespace_mismatch")
		})
	}
	retrieve(t, "credential failure", clientFor(uuid.NewString()), selection, blobA, "SignatureDoesNotMatch")
	retrieve(t, "credential repaired retry 1", client, selection, blobA, "verified")
	retrieve(t, "credential repaired retry 2", client, selection, blobA, "verified")
	// Check another genuine managed revision through its production read path.
	reader, err := store.Open(ctx, blobB.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || !bytes.Equal(got, b) {
		t.Fatal("unrelated managed revision changed")
	}
	sentinel, err := client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &bucket, Key: &sentinelKey, VersionId: &sentinelVersion})
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(sentinel.Body)
	closeErr = sentinel.Body.Close()
	if err != nil || closeErr != nil || !bytes.Equal(got, sentinelBody) {
		t.Fatal("sentinel changed")
	}
	baseline(sentinelKey, sentinelLatestVersion, sentinelLatestBody)
	after := inventory()
	if !reflect.DeepEqual(before.Versions, after.Versions) || !reflect.DeepEqual(before.DeleteMarkers, after.DeleteMarkers) {
		t.Fatal("read/retry modified provider versions")
	}
}

type historicalSelection struct{ Endpoint, Region, Bucket, Key, Version string }

// Provider observations only: no mutation, fallback, or product admission.
func observeHistoricalBytes(ctx context.Context, client *awss3.Client, boundary, selected historicalSelection, expected storage.Blob) (string, string) {
	options := client.Options()
	if selected.Endpoint != boundary.Endpoint || selected.Region != boundary.Region || selected.Bucket != boundary.Bucket || selected.Key != boundary.Key || aws.ToString(options.BaseEndpoint) != boundary.Endpoint || options.Region != boundary.Region {
		return "", "namespace_mismatch"
	}
	if selected.Version == "" || selected.Version == "null" {
		return "", "missing_version"
	}
	out, err := client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &selected.Bucket, Key: &selected.Key, VersionId: &selected.Version})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) {
			switch api.ErrorCode() {
			case "NoSuchVersion", "NoSuchKey", "AccessDenied", "SignatureDoesNotMatch", "InvalidAccessKeyId":
				return "", api.ErrorCode()
			}
		}
		return "", "provider_failure"
	}
	defer out.Body.Close()
	if aws.ToString(out.VersionId) != selected.Version {
		return "", "version_mismatch"
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(out.Body, expected.Size+1))
	if err != nil {
		return "", "read_failure"
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if n != expected.Size || digest != expected.SHA256 {
		return digest, "integrity_mismatch"
	}
	return digest, "verified"
}

func requireHistoricalCode(t *testing.T, err error, code string) {
	t.Helper()
	var api smithy.APIError
	if !errors.As(err, &api) || api.ErrorCode() != code {
		t.Fatalf("provider did not return expected %s", code)
	}
}
