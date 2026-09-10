//go:build fai520qualification

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	recoverypg "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/recoveryset/s3reader"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	successorS3Image     = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
	successorS3Region    = "us-east-1"
	successorS3ProfileID = "22222222-2222-4222-8222-222222222222"
	successorS3Account   = "minio-successor-qualification"
)

// TestFAI520SuccessorS3ExactVersionQualification proves that the successor
// repository can use real versioned MinIO objects as its exact reader. The
// provider's mutable current object is overwritten and deleted before the
// repository is called; only the captured immutable versions remain usable.
func TestFAI520SuccessorS3ExactVersionQualification(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	provider := startSuccessorS3Provider(t)
	input, trust, _ := successorInputs(t, "minimal")
	configureSuccessorS3Input(t, provider, &input)
	sentinelKey := "unrelated/exact-version-sentinel"
	sentinelBody := []byte("unrelated-versioned-sentinel")
	sentinelVersion := putSuccessorS3Object(t, provider, sentinelKey, sentinelBody)

	pool, admin, reopenURL := successorDatabase(t)
	provisionSuccessorGeneration(t, admin, trust.Generation)
	reader := newSuccessorS3Reader(t, provider.client, provider.config)
	repo := recoverypg.NewSuccessorRepository(pool, recoverypg.SuccessorOptions{
		Reader: reader,
		Trust:  func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil },
	})

	overwriteAndDeleteCurrent(t, provider, input)
	if latest, err := provider.client.GetObject(t.Context(), &awss3.GetObjectInput{
		Bucket: aws.String(provider.bucket), Key: aws.String(input.Payloads.Manifest.Locator.Key),
	}); err == nil {
		_ = latest.Body.Close()
		t.Fatal("MinIO latest read unexpectedly survived the delete marker")
	}
	created, err := repo.CreateSet3(t.Context(), input)
	if err != nil {
		t.Fatalf("CreateSet3 against exact MinIO versions: %v", err)
	}
	wantCanonical, err := created.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}

	// A deterministic retry must preserve the immutable winner and its exact
	// canonical bytes, even though current provider objects are gone.
	retried, err := repo.CreateSet3(t.Context(), input)
	if err != nil {
		t.Fatalf("deterministic CreateSet3 retry: %v", err)
	}
	retryCanonical, err := retried.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retryCanonical, wantCanonical) {
		t.Fatal("CreateSet3 retry changed canonical recovery-set bytes")
	}
	assertSuccessorS3Version(t, provider, sentinelKey, sentinelVersion, sentinelBody)

	pool.Close()
	reopened, err := pgxpool.New(t.Context(), reopenURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	reopenedReader := newSuccessorS3Reader(t, provider.client, provider.config)
	reopenedRepo := recoverypg.NewSuccessorRepository(reopened, recoverypg.SuccessorOptions{
		Reader: reopenedReader,
		Trust:  func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil },
	})
	loaded, err := reopenedRepo.ReadSet3(t.Context(), input.Set.ID)
	if err != nil {
		t.Fatalf("ReadSet3 after PostgreSQL close/reopen: %v", err)
	}
	loadedCanonical, err := loaded.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loadedCanonical, wantCanonical) {
		t.Fatal("ReadSet3 after restart changed canonical recovery-set bytes")
	}
	assertSuccessorS3Version(t, provider, sentinelKey, sentinelVersion, sentinelBody)
	secondRead, err := reopenedRepo.ReadSet3(t.Context(), input.Set.ID)
	if err != nil {
		t.Fatalf("deterministic ReadSet3 retry: %v", err)
	}
	secondCanonical, err := secondRead.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secondCanonical, loadedCanonical) {
		t.Fatal("ReadSet3 retry changed canonical recovery-set bytes")
	}
	assertSuccessorS3Version(t, provider, sentinelKey, sentinelVersion, sentinelBody)
}

// TestFAI520SuccessorS3ExactVersionFailures exercises provider failures at the
// adapter boundary and through CreateSet3. Every failed submission must leave
// all successor association tables empty.
func TestFAI520SuccessorS3ExactVersionFailures(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	provider := startSuccessorS3Provider(t)
	input, trust, _ := successorInputs(t, "minimal")
	configureSuccessorS3Input(t, provider, &input)

	pool, admin, _ := successorDatabase(t)
	provisionSuccessorGeneration(t, admin, trust.Generation)
	reader := newSuccessorS3Reader(t, provider.client, provider.config)
	baseRepo := func(r recoverypg.ExactVersionReader) *recoverypg.SuccessorRepository {
		return recoverypg.NewSuccessorRepository(pool, recoverypg.SuccessorOptions{
			Reader: r,
			Trust:  func(context.Context, string) (recoverypg.TrustInput, error) { return trust, nil },
		})
	}

	manifestKey := input.Payloads.Manifest.Locator.Key
	wrongVersion := putSuccessorS3Object(t, provider, manifestKey, []byte("a different exact provider version"))
	corrupted := bytes.Clone(input.Payloads.Manifest.CanonicalBytes)
	corrupted[len(corrupted)-1] ^= 1
	corruptedVersion := putSuccessorS3Object(t, provider, manifestKey, corrupted)
	deletedVersion := putSuccessorS3Object(t, provider, manifestKey, input.Payloads.Manifest.CanonicalBytes)
	if _, err := provider.client.DeleteObject(t.Context(), &awss3.DeleteObjectInput{
		Bucket: aws.String(provider.bucket), Key: aws.String(manifestKey), VersionId: aws.String(deletedVersion),
	}); err != nil {
		t.Fatalf("delete explicit MinIO version: %v", err)
	}

	cases := []struct {
		name       string
		reader     recoverypg.ExactVersionReader
		mutate     func(*recoverypg.Set3Input)
		exactFails bool
		wantExact  error
	}{
		{
			name: "missing version has no latest fallback",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.VersionID = "missing-version-id"
			},
			exactFails: true,
		},
		{
			name: "latest version selector is rejected",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.VersionID = "latest"
			},
			exactFails: true,
			wantExact:  s3reader.ErrMissingVersion,
		},
		{
			name: "wrong version bytes are rejected",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.VersionID = wrongVersion
			},
			exactFails: true,
			wantExact:  s3reader.ErrIntegrity,
		},
		{
			name: "deleted version is rejected",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.VersionID = deletedVersion
			},
			exactFails: true,
			wantExact:  s3reader.ErrMissingVersion,
		},
		{
			name: "corrupted bytes are rejected",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.VersionID = corruptedVersion
			},
			exactFails: true,
			wantExact:  s3reader.ErrIntegrity,
		},
		{
			name: "access denied",
			reader: func() recoverypg.ExactVersionReader {
				return newSuccessorS3Reader(t, provider.clientFor("wrong-secret"), provider.config)
			}(),
			exactFails: true,
			wantExact:  s3reader.ErrAccessDenied,
		},
		{
			name: "wrong bucket substitution",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.Bucket = "wrong-successor-bucket"
			},
			exactFails: true,
			wantExact:  s3reader.ErrProfileMismatch,
		},
		{
			name: "wrong profile substitution",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.StorageProfileID = "33333333-3333-4333-8333-333333333333"
			},
			exactFails: true,
			wantExact:  s3reader.ErrProfileMismatch,
		},
		{
			name: "wrong endpoint substitution",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.Endpoint = "https://localhost:1"
			},
			exactFails: true,
			wantExact:  s3reader.ErrProfileMismatch,
		},
		{
			name: "wrong namespace substitution",
			mutate: func(candidate *recoverypg.Set3Input) {
				candidate.Payloads.Manifest.Locator.Namespace = "other-evidence"
			},
			exactFails: true,
			wantExact:  s3reader.ErrProfileMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := input
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			caseReader := tc.reader
			if caseReader == nil {
				caseReader = reader
			}
			if tc.exactFails {
				requireSuccessorExactReadError(t, caseReader, candidate.Payloads.Manifest.Locator, tc.wantExact)
			}
			if _, err := baseRepo(caseReader).CreateSet3(t.Context(), candidate); err == nil {
				t.Fatal("CreateSet3 accepted a provider failure or locator substitution")
			}
			assertNoSuccessorAssociation(t, pool, candidate.Set.ID)
		})
	}
}

type successorS3Provider struct {
	client    *awss3.Client
	clientFor func(string) *awss3.Client
	bucket    string
	endpoint  string
	config    s3reader.Config
}

func startSuccessorS3Provider(t *testing.T) successorS3Provider {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	user := "q" + strings.ReplaceAll(uuid.NewString(), "-", "")
	secret := uuid.NewString()
	container, err := tcminio.Run(ctx, successorS3Image,
		tcminio.WithUsername(user), tcminio.WithPassword(secret),
		testcontainers.WithTmpfs(map[string]string{"/data": "rw,size=1g"}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/ready").WithPort("9000").WithStartupTimeout(time.Minute)))
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	address, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transportEndpoint := "http://" + strings.TrimRight(address, "/")
	// Successor locators intentionally require a canonical HTTPS DNS endpoint.
	// The client still talks plain HTTP to the disposable MinIO mapping; the
	// HTTPS localhost value is the trusted endpoint identity recorded in each
	// locator and profile tuple.
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse MinIO connection address: %v", err)
	}
	identityEndpoint := "https://localhost:" + port
	bucket := "successor-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	clientFor := func(password string) *awss3.Client {
		return awss3.New(awss3.Options{
			Region:           successorS3Region,
			BaseEndpoint:     aws.String(transportEndpoint),
			UsePathStyle:     true,
			Credentials:      credentials.NewStaticCredentialsProvider(user, password, ""),
			RetryMaxAttempts: 1,
		})
	}
	client := clientFor(secret)
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create MinIO successor bucket: %v", err)
	}
	if _, err := client.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatalf("enable MinIO bucket versioning: %v", err)
	}
	return successorS3Provider{
		client: client, clientFor: clientFor, bucket: bucket, endpoint: identityEndpoint,
		config: s3reader.Config{Profile: s3reader.ProfileTuple{
			Bucket: bucket, Endpoint: identityEndpoint, Region: successorS3Region,
			StorageProfileID: successorS3ProfileID, StorageProfileRevision: 1,
			AccountIdentity: successorS3Account, Namespace: "evidence",
		}},
	}
}

func configureSuccessorS3Input(t *testing.T, provider successorS3Provider, input *recoverypg.Set3Input) {
	t.Helper()
	values := []*recoverypg.PayloadReference{
		&input.Payloads.Set, &input.Payloads.Manifest, &input.Payloads.Anchor,
		&input.Payloads.Profiles, &input.Payloads.Receipt, &input.Payloads.Authority,
	}
	for _, ref := range values {
		if ref.Locator.Key == "" || len(ref.CanonicalBytes) == 0 {
			t.Fatal("successor fixture omitted a canonical S3 payload")
		}
		version := putSuccessorS3Object(t, provider, ref.Locator.Key, ref.CanonicalBytes)
		ref.Locator.Endpoint = provider.endpoint
		ref.Locator.Region = successorS3Region
		ref.Locator.Bucket = provider.bucket
		ref.Locator.StorageProfileID = successorS3ProfileID
		ref.Locator.StorageProfileRevision = 1
		ref.Locator.AccountIdentity = successorS3Account
		ref.Locator.VersionID = version
	}
}

func putSuccessorS3Object(t *testing.T, provider successorS3Provider, key string, body []byte) string {
	t.Helper()
	out, err := provider.client.PutObject(t.Context(), &awss3.PutObjectInput{
		Bucket: aws.String(provider.bucket), Key: aws.String(key), Body: bytes.NewReader(body),
	})
	if err != nil {
		t.Fatalf("put MinIO successor object %q: %v", key, err)
	}
	version := aws.ToString(out.VersionId)
	if version == "" || version == "null" {
		t.Fatalf("MinIO successor object %q did not return an immutable version", key)
	}
	return version
}

func assertSuccessorS3Version(t *testing.T, provider successorS3Provider, key, version string, want []byte) {
	t.Helper()
	out, err := provider.client.GetObject(t.Context(), &awss3.GetObjectInput{
		Bucket: aws.String(provider.bucket), Key: aws.String(key), VersionId: aws.String(version),
	})
	if err != nil {
		t.Fatalf("get sentinel version %q: %v", version, err)
	}
	if out == nil || out.Body == nil {
		t.Fatal("sentinel version returned no body")
	}
	got, readErr := io.ReadAll(out.Body)
	closeErr := out.Body.Close()
	if readErr != nil {
		t.Fatalf("read sentinel version %q: %v", version, readErr)
	}
	if closeErr != nil {
		t.Fatalf("close sentinel version %q: %v", version, closeErr)
	}
	if gotVersion := aws.ToString(out.VersionId); gotVersion != version {
		t.Fatalf("sentinel version = %q, want %q", gotVersion, version)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("sentinel bytes = %q, want %q", got, want)
	}
}

func overwriteAndDeleteCurrent(t *testing.T, provider successorS3Provider, input recoverypg.Set3Input) {
	t.Helper()
	refs := []*recoverypg.PayloadReference{
		&input.Payloads.Set, &input.Payloads.Manifest, &input.Payloads.Anchor,
		&input.Payloads.Profiles, &input.Payloads.Receipt, &input.Payloads.Authority,
	}
	for _, ref := range refs {
		putSuccessorS3Object(t, provider, ref.Locator.Key, []byte("mutable current replacement"))
		if _, err := provider.client.DeleteObject(t.Context(), &awss3.DeleteObjectInput{
			Bucket: aws.String(provider.bucket), Key: aws.String(ref.Locator.Key),
		}); err != nil {
			t.Fatalf("delete current MinIO successor object %q: %v", ref.Locator.Key, err)
		}
	}
}

func newSuccessorS3Reader(t *testing.T, client *awss3.Client, config s3reader.Config) recoverypg.ExactVersionReader {
	t.Helper()
	reader, err := s3reader.New(client, config)
	if err != nil {
		t.Fatalf("construct successor S3 reader: %v", err)
	}
	if reader == nil {
		t.Fatal("successor S3 reader is nil")
	}
	return reader
}

func requireSuccessorExactReadError(t *testing.T, reader recoverypg.ExactVersionReader, locator recoverypg.ValidatedLocator, want error) {
	t.Helper()
	body, err := reader.ReadExact(t.Context(), locator)
	if err == nil {
		if body != nil {
			_ = body.Close()
		}
		t.Fatal("exact-version read unexpectedly succeeded; adapter may have fallen back to latest")
	}
	if body != nil {
		_ = body.Close()
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("exact-version read error = %v, want %v", err, want)
	}
}

func assertNoSuccessorAssociation(t *testing.T, pool *pgxpool.Pool, setID string) {
	t.Helper()
	for _, table := range []string{
		"recovery_set_v3", "recovery_set_v3_root", "successor_manifest_binding", "successor_evidence_v2", "successor_evidence_locator_v2", "set_identity_registry",
	} {
		query := fmt.Sprintf("SELECT count(*) FROM recovery.%s", table)
		var args []any
		if table == "recovery_set_v3" || table == "recovery_set_v3_root" || table == "set_identity_registry" {
			query += " WHERE set_id = $1::uuid"
			args = append(args, setID)
		} else if table == "successor_manifest_binding" {
			query += " WHERE set_id = $1::uuid"
			args = append(args, setID)
		}
		var count int
		if err := pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
			t.Fatalf("count failed successor rows in %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("failed submission left %d rows in %s", count, table)
		}
	}
}
