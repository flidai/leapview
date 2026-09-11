//go:build fai520qualification

package s3_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	"github.com/flidai/leapview/internal/recoveryset/capture"
)

var _ capture.ObservationVerifier = (*manageds3.Store)(nil)

// This validates recovery evidence binding. It does not prove successful physical disaster recovery.
func TestFAI520ManagedS3WriteResponseVersionCapture(t *testing.T) {
	ctx, client, _, bucket, endpoint := historicalProvider(t)
	profile := storage.ProviderProfileIdentity{
		ProfileID: "managed-source-write-qualification", Implementation: "s3", AccountIdentity: "qualification",
		Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Namespace: "project-a",
	}
	store, err := manageds3.New(client, awss3.NewPresignClient(client), manageds3.Config{
		Bucket: bucket, Prefix: profile.Namespace, ObservationProfile: &profile,
	})
	if err != nil {
		t.Fatal(err)
	}

	firstBody := []byte("managed object revision one\n")
	first, err := store.Put(ctx, blobFor(firstBody), bytes.NewReader(firstBody))
	if err != nil {
		t.Fatal(err)
	}
	requireExactWriteObservation(t, store, client, first, profile, firstBody)

	secondBody := []byte("managed object revision two\n")
	second, err := store.Put(ctx, blobFor(secondBody), bytes.NewReader(secondBody))
	if err != nil {
		t.Fatal(err)
	}
	requireExactWriteObservation(t, store, client, second, profile, secondBody)
	if first.ProviderVersion.VersionID == second.ProviderVersion.VersionID || first.ProviderVersion.ObjectKey == second.ProviderVersion.ObjectKey {
		t.Fatal("successive managed revisions did not retain distinct provider observations")
	}

	multipartBody := []byte("managed multipart object\n")
	upload, err := store.CreateMultipart(ctx, blobFor(multipartBody))
	if err != nil {
		t.Fatal(err)
	}
	partNumber := int32(1)
	part, err := client.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: &bucket, Key: &upload.Key, UploadId: &upload.UploadID,
		PartNumber: &partNumber, Body: bytes.NewReader(multipartBody), ContentLength: aws.Int64(int64(len(multipartBody))),
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteMultipart(ctx, upload, []storage.CompletedMultipartPart{{Number: partNumber, ETag: aws.ToString(part.ETag)}})
	if err != nil {
		t.Fatal(err)
	}
	requireExactWriteObservation(t, store, client, completed, profile, multipartBody)

	var observations storage.ProviderVersionObservationSet
	for _, blob := range []storage.Blob{first, second, completed} {
		if err := observations.Add(*blob.ProviderVersion); err != nil {
			t.Fatal(err)
		}
		if err := observations.Add(*blob.ProviderVersion); err != nil {
			t.Fatalf("identical observation retry: %v", err)
		}
	}
	if got := observations.Snapshot(); len(got) != 3 {
		t.Fatalf("captured observations = %d, want 3", len(got))
	}
}

func requireExactWriteObservation(t *testing.T, store *manageds3.Store, client *awss3.Client, blob storage.Blob, profile storage.ProviderProfileIdentity, want []byte) {
	t.Helper()
	if blob.ProviderVersion == nil {
		t.Fatal("managed write omitted provider-version observation")
	}
	observation := *blob.ProviderVersion
	if observation.Profile != profile || observation.VersionID == "" || observation.VersionID == "null" || observation.SHA256 != blob.SHA256 || observation.Size != blob.Size {
		t.Fatalf("provider-version observation = %#v", observation)
	}
	if err := store.VerifyExact(t.Context(), observation); err != nil {
		t.Fatalf("production exact-version verifier: %v", err)
	}
	result, err := client.GetObject(t.Context(), &awss3.GetObjectInput{
		Bucket: &profile.Bucket, Key: &observation.ObjectKey, VersionId: &observation.VersionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(result.Body)
	closeErr := result.Body.Close()
	if readErr != nil || closeErr != nil || aws.ToString(result.VersionId) != observation.VersionID || !bytes.Equal(got, want) {
		t.Fatalf("exact observed provider version did not reproduce write bytes: read=%v close=%v", readErr, closeErr)
	}
}
