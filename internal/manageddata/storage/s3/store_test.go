package s3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
	"github.com/flidai/leapview/internal/manageddata/storage/storagetest"
)

func TestBlobStoreConformance(t *testing.T) {
	storagetest.BlobStoreConformance(t, func(t *testing.T) storage.BlobStore {
		client := newFakeClient()
		store, err := manageds3.New(client, &fakePresigner{}, manageds3.Config{Bucket: "private-data", Prefix: "managed"})
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestStoreUsesContentAddressedKeyAndStableURI(t *testing.T) {
	client := newFakeClient()
	store := newStore(t, client, &fakePresigner{})
	body := []byte("content addressed")
	blob := blobFor(body)
	stored, err := store.Put(t.Context(), blob, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	wantKey := "managed/blobs/sha256/" + blob.SHA256[:2] + "/" + blob.SHA256
	if client.lastPutKey != wantKey {
		t.Fatalf("PutObject key = %q, want %q", client.lastPutKey, wantKey)
	}
	if stored.URI != "s3://private-data/"+wantKey {
		t.Fatalf("URI = %q", stored.URI)
	}
	if client.lastPutIfNoneMatch != "*" {
		t.Fatalf("IfNoneMatch = %q", client.lastPutIfNoneMatch)
	}
}

func TestStoreCapturesAuthoritativePutResponseVersion(t *testing.T) {
	client := newFakeClient()
	capturedAt := time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC)
	store := newObservedStore(t, client, capturedAt)
	body := []byte("versioned content")
	expected := blobFor(body)
	stored, err := store.Put(t.Context(), expected, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	observation := stored.ProviderVersion
	if observation == nil {
		t.Fatal("Put() returned no provider-version observation")
	}
	if observation.VersionID != client.lastPutVersion || observation.ObjectKey != client.lastPutKey || observation.SHA256 != expected.SHA256 || observation.Size != expected.Size || observation.CapturedAt != capturedAt {
		t.Fatalf("provider observation = %#v", observation)
	}
	if client.lastHeadVersion != client.lastPutVersion || client.lastGetVersion != client.lastPutVersion {
		t.Fatalf("verification versions: head=%q get=%q write=%q", client.lastHeadVersion, client.lastGetVersion, client.lastPutVersion)
	}
	retried, err := store.Put(t.Context(), stored, bytes.NewReader(body))
	if err != nil || retried.ProviderVersion == nil || *retried.ProviderVersion != *stored.ProviderVersion {
		t.Fatalf("exact observed retry = %#v, %v", retried.ProviderVersion, err)
	}
	if _, err := store.Put(t.Context(), expected, bytes.NewReader(body)); !errors.Is(err, storage.ErrProviderVersion) {
		t.Fatalf("retry without prior observation error = %v", err)
	}
}

func TestStorePersistsVerifiedWriteObservation(t *testing.T) {
	client := newFakeClient()
	recorder := &providerObservationRecorder{}
	capturedAt := time.Date(2026, 9, 10, 9, 30, 0, 123000, time.UTC)
	store := newObservedStoreWithRecorder(t, client, capturedAt, recorder)
	body := []byte("durable observation")
	stored, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderVersion == nil || len(recorder.observations) != 1 || recorder.observations[0] != *stored.ProviderVersion {
		t.Fatalf("durable observations = %#v, stored=%#v", recorder.observations, stored.ProviderVersion)
	}
	if _, err := store.Put(t.Context(), stored, bytes.NewReader(body)); err != nil {
		t.Fatalf("durable exact retry: %v", err)
	}
	if len(recorder.observations) != 2 || recorder.observations[1] != recorder.observations[0] {
		t.Fatalf("durable retry observations = %#v", recorder.observations)
	}
}

func TestStorePersistenceFailureReturnsExactObservationForRetry(t *testing.T) {
	client := newFakeClient()
	recorder := &providerObservationRecorder{err: errors.New("database unavailable")}
	store := newObservedStoreWithRecorder(t, client, time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC), recorder)
	body := []byte("provider succeeds before database")
	stored, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if err == nil || stored.ProviderVersion == nil {
		t.Fatalf("persistence failure = %#v, %v", stored, err)
	}
	if stored.ProviderVersion.VersionID != client.lastPutVersion {
		t.Fatalf("failed handoff lost write VersionID: %#v", stored.ProviderVersion)
	}
	recorder.err = nil
	retried, err := store.Put(t.Context(), stored, bytes.NewReader(body))
	if err != nil || retried.ProviderVersion == nil || *retried.ProviderVersion != *stored.ProviderVersion {
		t.Fatalf("persistence repair retry = %#v, %v", retried, err)
	}
}

func TestStoreRejectsRecorderWithoutTrustedProfile(t *testing.T) {
	_, err := manageds3.New(newFakeClient(), &fakePresigner{}, manageds3.Config{
		Bucket: "private-data", Prefix: "managed", ObservationRecorder: &providerObservationRecorder{},
	})
	if !errors.Is(err, storage.ErrProviderVersion) {
		t.Fatalf("recorder without profile error = %v", err)
	}
}

func TestStoreRequiresWriteResponseVersionWhenCaptureEnabled(t *testing.T) {
	client := newFakeClient()
	client.omitPutVersion = true
	store := newObservedStore(t, client, time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC))
	body := []byte("unversioned response")
	stored, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if !errors.Is(err, storage.ErrProviderVersion) || stored.ProviderVersion != nil {
		t.Fatalf("Put() = %#v, %v", stored, err)
	}
	if _, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body)); !errors.Is(err, storage.ErrProviderVersion) {
		t.Fatalf("existing unobserved retry error = %v", err)
	}
}

func TestStoreFailedPutProducesNoObservation(t *testing.T) {
	client := newFakeClient()
	client.putFailure = errors.New("provider write failed")
	recorder := &providerObservationRecorder{}
	store := newObservedStoreWithRecorder(t, client, time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC), recorder)
	body := []byte("failed write")
	stored, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if !errors.Is(err, storage.ErrBackend) || stored.ProviderVersion != nil {
		t.Fatalf("Put() = %#v, %v", stored, err)
	}
	if len(recorder.observations) != 0 {
		t.Fatalf("failed provider write persisted observations: %#v", recorder.observations)
	}
}

func TestStoreRejectsSubstitutedPutResponseVersion(t *testing.T) {
	client := newFakeClient()
	client.putOutputVersion = "substituted-version"
	store := newObservedStore(t, client, time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC))
	body := []byte("substituted version")
	stored, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if err == nil || stored.ProviderVersion != nil {
		t.Fatalf("Put() = %#v, %v", stored, err)
	}
}

func TestStorePreservesUnversionedBehaviorWithoutObservationProfile(t *testing.T) {
	client := newFakeClient()
	client.omitPutVersion = true
	store := newStore(t, client, &fakePresigner{})
	body := []byte("legacy unversioned store")
	stored, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if err != nil || stored.ProviderVersion != nil {
		t.Fatalf("Put() = %#v, %v", stored, err)
	}
}

func TestLegacyStoreRejectsSuppliedProviderVersionBeforeS3Calls(t *testing.T) {
	client := newFakeClient()
	store := newStore(t, client, &fakePresigner{})
	body := []byte("legacy provider version")
	expected := blobFor(body)
	expected.ProviderVersion = &storage.ProviderVersionObservation{VersionID: "provider-version-1"}

	stored, err := store.Put(t.Context(), expected, bytes.NewReader(body))
	if !errors.Is(err, storage.ErrProviderVersion) || stored.ProviderVersion != nil {
		t.Fatalf("Put() = %#v, %v", stored, err)
	}
	if len(client.headVersions) != 0 || len(client.getVersions) != 0 || client.putCalls != 0 {
		t.Fatalf("legacy store contacted S3 for untrusted observation: head=%v get=%v puts=%d", client.headVersions, client.getVersions, client.putCalls)
	}
}

func TestStoreRejectsObservationProfileOutsideConfiguredNamespace(t *testing.T) {
	client := newFakeClient()
	profile := storage.ProviderProfileIdentity{
		ProfileID: "managed-source-profile", Implementation: "s3", AccountIdentity: "qualification",
		Endpoint: "https://s3.example.test", Region: "test-region-1", Bucket: "other-bucket", Namespace: "managed",
	}
	_, err := manageds3.New(client, &fakePresigner{}, manageds3.Config{Bucket: "private-data", Prefix: "managed", ObservationProfile: &profile})
	if !errors.Is(err, storage.ErrProviderVersion) {
		t.Fatalf("New() error = %v", err)
	}
}

func TestStorePutUsesHistoricalObservationAfterCurrentReplacement(t *testing.T) {
	client := newFakeClient()
	store := newObservedStore(t, client, time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC))
	firstBody := []byte("managed object V1")
	first, err := store.Put(t.Context(), blobFor(firstBody), bytes.NewReader(firstBody))
	if err != nil {
		t.Fatal(err)
	}

	key := first.ProviderVersion.ObjectKey
	replacementBody := []byte("managed object V2")
	if _, err := client.PutObject(t.Context(), &awss3.PutObjectInput{Bucket: testPointer("private-data"), Key: &key, Body: bytes.NewReader(replacementBody)}); err != nil {
		t.Fatal(err)
	}
	client.headVersions, client.getVersions = nil, nil
	putsBeforeRetry, versionsBeforeRetry := client.putCalls, client.nextVersion

	retried, err := store.Put(t.Context(), first, bytes.NewReader(firstBody))
	if err != nil {
		t.Fatal(err)
	}
	if retried.ProviderVersion == nil || *retried.ProviderVersion != *first.ProviderVersion {
		t.Fatalf("historical observation changed: got=%#v want=%#v", retried.ProviderVersion, first.ProviderVersion)
	}
	if client.putCalls != putsBeforeRetry || client.nextVersion != versionsBeforeRetry {
		t.Fatalf("historical retry issued a new PUT/version: puts=%d/%d versions=%d/%d", client.putCalls, putsBeforeRetry, client.nextVersion, versionsBeforeRetry)
	}
	if client.lastHeadVersion != first.ProviderVersion.VersionID || client.lastGetVersion != first.ProviderVersion.VersionID {
		t.Fatalf("historical retry versions: head=%q get=%q want=%q", client.lastHeadVersion, client.lastGetVersion, first.ProviderVersion.VersionID)
	}
	if len(client.headVersions) == 0 || client.headVersions[0] != first.ProviderVersion.VersionID || len(client.getVersions) == 0 || client.getVersions[0] != first.ProviderVersion.VersionID {
		t.Fatalf("historical retry did not start with exact version: head=%v get=%v", client.headVersions, client.getVersions)
	}
}

func TestStorePutUsesHistoricalObservationAfterDeleteMarker(t *testing.T) {
	client := newFakeClient()
	store := newObservedStore(t, client, time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC))
	body := []byte("managed object V1")
	first, err := store.Put(t.Context(), blobFor(body), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	key := first.ProviderVersion.ObjectKey
	// An unversioned delete in a versioned bucket creates a delete marker: the
	// fake removes mutable latest state while retaining historical versions.
	if _, err := client.DeleteObjects(t.Context(), &awss3.DeleteObjectsInput{
		Bucket: testPointer("private-data"),
		Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: &key}}},
	}); err != nil {
		t.Fatal(err)
	}
	client.headVersions, client.getVersions = nil, nil
	putsBeforeRetry, versionsBeforeRetry := client.putCalls, client.nextVersion

	retried, err := store.Put(t.Context(), first, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if retried.ProviderVersion == nil || *retried.ProviderVersion != *first.ProviderVersion {
		t.Fatalf("historical observation changed: got=%#v want=%#v", retried.ProviderVersion, first.ProviderVersion)
	}
	if client.putCalls != putsBeforeRetry || client.nextVersion != versionsBeforeRetry {
		t.Fatalf("historical retry issued a new PUT/version: puts=%d/%d versions=%d/%d", client.putCalls, putsBeforeRetry, client.nextVersion, versionsBeforeRetry)
	}
	if client.lastHeadVersion != first.ProviderVersion.VersionID || client.lastGetVersion != first.ProviderVersion.VersionID {
		t.Fatalf("historical retry versions: head=%q get=%q want=%q", client.lastHeadVersion, client.lastGetVersion, first.ProviderVersion.VersionID)
	}
	if len(client.headVersions) == 0 || client.headVersions[0] != first.ProviderVersion.VersionID || len(client.getVersions) == 0 || client.getVersions[0] != first.ProviderVersion.VersionID {
		t.Fatalf("historical retry did not start with exact version: head=%v get=%v", client.headVersions, client.getVersions)
	}
}

func TestBlobInventoryPaginatesAndDeletesInOneBatch(t *testing.T) {
	client := newFakeClient()
	client.listPageSize = 1
	store := newStore(t, client, &fakePresigner{})
	first := blobFor([]byte("first"))
	second := blobFor([]byte("second"))
	for _, item := range []struct {
		blob storage.Blob
		body []byte
	}{{first, []byte("first")}, {second, []byte("second")}} {
		if _, err := store.Put(t.Context(), item.blob, bytes.NewReader(item.body)); err != nil {
			t.Fatal(err)
		}
	}
	var metadata []storage.BlobMetadata
	if err := store.WalkBlobs(t.Context(), func(item storage.BlobMetadata) error {
		metadata = append(metadata, item)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || client.listCalls != 2 {
		t.Fatalf("WalkBlobs() = %#v, list calls = %d", metadata, client.listCalls)
	}
	for _, item := range metadata {
		if item.LastModified.IsZero() || item.Size <= 0 {
			t.Fatalf("invalid metadata = %#v", item)
		}
	}
	if err := store.DeleteBlobs(t.Context(), []string{first.SHA256, second.SHA256}); err != nil {
		t.Fatal(err)
	}
	if len(client.deleteBatches) != 1 || len(client.deleteBatches[0]) != 2 {
		t.Fatalf("delete batches = %#v", client.deleteBatches)
	}
	if err := store.DeleteBlobs(t.Context(), []string{first.SHA256, second.SHA256}); err != nil {
		t.Fatalf("idempotent DeleteBlobs() = %v", err)
	}
}

func TestBlobInventoryRejectsMalformedProviderKeys(t *testing.T) {
	client := newFakeClient()
	client.objects["managed/blobs/sha256/not-canonical"] = fakeObject{body: []byte("x"), modified: time.Now()}
	store := newStore(t, client, &fakePresigner{})
	if err := store.WalkBlobs(t.Context(), func(storage.BlobMetadata) error { return nil }); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("WalkBlobs() error = %v", err)
	}
}

func TestMultipartCreateSignCompleteAndAbort(t *testing.T) {
	client := newFakeClient()
	presigner := &fakePresigner{}
	store := newStore(t, client, presigner)
	body := []byte("multipart body")
	expected := blobFor(body)

	upload, err := store.CreateMultipart(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if upload.UploadID == "" || upload.Key == "" || upload.Existing {
		t.Fatalf("CreateMultipart() = %#v", upload)
	}
	partBody := body[:5]
	partDigest := blobFor(partBody).SHA256
	signed, err := store.SignPart(t.Context(), upload, storage.MultipartPartRequest{Number: 1, Size: int64(len(partBody)), SHA256: partDigest})
	if err != nil {
		t.Fatal(err)
	}
	if signed.URL != "https://uploads.example/part/1" || http.Header(signed.Headers).Get("X-Test") != "signed" {
		t.Fatalf("SignPart() = %#v", signed)
	}
	if presigner.lastChecksum != base64.StdEncoding.EncodeToString(mustDecodeHex(t, partDigest)) {
		t.Fatalf("signed checksum = %q", presigner.lastChecksum)
	}

	client.setMultipartBody(upload.UploadID, body)
	completed, err := store.CompleteMultipart(t.Context(), upload, []storage.CompletedMultipartPart{{Number: 1, ETag: "etag-1", SHA256: partDigest}})
	if err != nil {
		t.Fatal(err)
	}
	if completed.SHA256 != expected.SHA256 || completed.Size != expected.Size {
		t.Fatalf("CompleteMultipart() = %#v", completed)
	}
	completedAgain, err := store.CompleteMultipart(t.Context(), upload, []storage.CompletedMultipartPart{{Number: 1, ETag: "etag-1", SHA256: partDigest}})
	if err != nil || completedAgain != completed {
		t.Fatalf("idempotent CompleteMultipart() = %#v, %v", completedAgain, err)
	}
	if err := store.AbortMultipart(t.Context(), upload); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortMultipart(t.Context(), upload); err != nil {
		t.Fatalf("idempotent AbortMultipart() = %v", err)
	}
}

func TestMultipartCompletionCapturesAuthoritativeResponseVersion(t *testing.T) {
	client := newFakeClient()
	capturedAt := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	store := newObservedStore(t, client, capturedAt)
	body := []byte("multipart observed body")
	expected := blobFor(body)
	upload, err := store.CreateMultipart(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	client.setMultipartBody(upload.UploadID, body)
	completed, err := store.CompleteMultipart(t.Context(), upload, []storage.CompletedMultipartPart{{Number: 1, ETag: "etag-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if completed.ProviderVersion == nil || completed.ProviderVersion.VersionID != client.lastMultipartVersion {
		t.Fatalf("multipart provider observation = %#v", completed.ProviderVersion)
	}
	if client.lastHeadVersion != client.lastMultipartVersion || client.lastGetVersion != client.lastMultipartVersion {
		t.Fatalf("multipart verification versions: head=%q get=%q write=%q", client.lastHeadVersion, client.lastGetVersion, client.lastMultipartVersion)
	}
}

func TestMultipartCompletionRequiresWriteResponseVersionWhenCaptureEnabled(t *testing.T) {
	client := newFakeClient()
	client.omitMultipartVersion = true
	store := newObservedStore(t, client, time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC))
	body := []byte("multipart without version")
	upload, err := store.CreateMultipart(t.Context(), blobFor(body))
	if err != nil {
		t.Fatal(err)
	}
	client.setMultipartBody(upload.UploadID, body)
	completed, err := store.CompleteMultipart(t.Context(), upload, []storage.CompletedMultipartPart{{Number: 1, ETag: "etag-1"}})
	if !errors.Is(err, storage.ErrProviderVersion) || completed.ProviderVersion != nil {
		t.Fatalf("CompleteMultipart() = %#v, %v", completed, err)
	}
	if _, err := store.CompleteMultipart(t.Context(), upload, []storage.CompletedMultipartPart{{Number: 1, ETag: "etag-1"}}); !errors.Is(err, storage.ErrProviderVersion) {
		t.Fatalf("unobserved multipart retry error = %v", err)
	}
}

func TestMultipartCompletionDeletesContentThatFailsStreamVerification(t *testing.T) {
	client := newFakeClient()
	store := newStore(t, client, &fakePresigner{})
	expected := blobFor([]byte("expected"))
	upload, err := store.CreateMultipart(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	client.setMultipartBody(upload.UploadID, []byte("tampered"))
	_, err = store.CompleteMultipart(t.Context(), upload, []storage.CompletedMultipartPart{{Number: 1, ETag: "etag"}})
	if !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("CompleteMultipart() error = %v", err)
	}
	if _, exists := client.object(upload.Key); exists {
		t.Fatal("failed multipart object was not deleted")
	}
}

func TestListMultipartUploadsPaginatesAndFiltersExactContentKey(t *testing.T) {
	client := newFakeClient()
	client.multipartListPageSize = 1
	store := newStore(t, client, &fakePresigner{})
	expected := blobFor([]byte("multipart-list"))
	first, err := store.CreateMultipart(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateMultipart(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.multipart["other"] = fakeMultipart{key: expectedKeyWithSuffix(store, expected.SHA256)}
	client.mu.Unlock()
	found, err := store.ListMultipartUploads(t.Context(), expected)
	if err != nil || len(found) != 2 || client.multipartListCalls < 2 {
		t.Fatalf("found=%#v calls=%d err=%v", found, client.multipartListCalls, err)
	}
	if found[0].UploadID != first.UploadID || found[1].UploadID != second.UploadID {
		t.Fatalf("found uploads=%#v", found)
	}
}

func expectedKeyWithSuffix(store *manageds3.Store, digest string) string {
	return "managed/blobs/" + digest + "-suffix"
}

func TestS3ErrorsDoNotExposeCredentials(t *testing.T) {
	client := newFakeClient()
	client.failure = errors.New("request failed: X-Amz-Credential=AKIA_SECRET&X-Amz-Signature=SECRET")
	store := newStore(t, client, &fakePresigner{})
	_, err := store.Stat(t.Context(), blobFor([]byte("missing")).SHA256)
	if err == nil || strings.Contains(err.Error(), "SECRET") || !errors.Is(err, storage.ErrBackend) {
		t.Fatalf("sanitized error = %v", err)
	}
}

func newStore(t *testing.T, client *fakeClient, presigner *fakePresigner) *manageds3.Store {
	t.Helper()
	store, err := manageds3.New(client, presigner, manageds3.Config{Bucket: "private-data", Prefix: "/managed/", SignExpiry: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newObservedStore(t *testing.T, client *fakeClient, capturedAt time.Time) *manageds3.Store {
	return newObservedStoreWithRecorder(t, client, capturedAt, nil)
}

func newObservedStoreWithRecorder(t *testing.T, client *fakeClient, capturedAt time.Time, recorder storage.ProviderVersionObservationRecorder) *manageds3.Store {
	t.Helper()
	profile := storage.ProviderProfileIdentity{
		ProfileID: "managed-source-profile", Implementation: "s3", AccountIdentity: "qualification",
		Endpoint: "https://s3.example.test", Region: "test-region-1", Bucket: "private-data", Namespace: "managed",
	}
	store, err := manageds3.New(client, &fakePresigner{}, manageds3.Config{
		Bucket: "private-data", Prefix: "/managed/", ObservationProfile: &profile,
		ObservationRecorder: recorder, Clock: func() time.Time { return capturedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type providerObservationRecorder struct {
	observations []storage.ProviderVersionObservation
	err          error
}

func (r *providerObservationRecorder) RecordProviderVersionObservation(_ context.Context, observation storage.ProviderVersionObservation) (storage.ProviderVersionObservation, error) {
	if r.err != nil {
		return storage.ProviderVersionObservation{}, r.err
	}
	r.observations = append(r.observations, observation)
	return observation, nil
}

type fakeClient struct {
	mu                    sync.Mutex
	objects               map[string]fakeObject
	versions              map[string]map[string]fakeObject
	multipart             map[string]fakeMultipart
	nextUpload            int
	failure               error
	lastPutKey            string
	lastPutIfNoneMatch    string
	listPageSize          int
	listCalls             int
	multipartListPageSize int
	multipartListCalls    int
	deleteBatches         [][]string
	putFailure            error
	omitPutVersion        bool
	putOutputVersion      string
	omitMultipartVersion  bool
	lastPutVersion        string
	lastMultipartVersion  string
	lastHeadVersion       string
	lastGetVersion        string
	headVersions          []string
	getVersions           []string
	nextVersion           int
	putCalls              int
}

type fakeObject struct {
	body     []byte
	metadata map[string]string
	modified time.Time
	version  string
}

type fakeMultipart struct {
	key      string
	metadata map[string]string
	body     []byte
}

func newFakeClient() *fakeClient {
	return &fakeClient{objects: map[string]fakeObject{}, versions: map[string]map[string]fakeObject{}, multipart: map[string]fakeMultipart{}}
}

func (c *fakeClient) PutObject(_ context.Context, input *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	if c.putFailure != nil {
		return nil, c.putFailure
	}
	key := dereference(input.Key)
	c.putCalls++
	c.lastPutKey = key
	c.lastPutIfNoneMatch = dereference(input.IfNoneMatch)
	if _, exists := c.objects[key]; exists && dereference(input.IfNoneMatch) == "*" {
		return nil, fakeAPIError{code: "PreconditionFailed"}
	}
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	if input.ContentLength != nil && *input.ContentLength != int64(len(body)) {
		return nil, fakeAPIError{code: "IncompleteBody"}
	}
	sum := sha256.Sum256(body)
	if input.ChecksumSHA256 != nil && *input.ChecksumSHA256 != base64.StdEncoding.EncodeToString(sum[:]) {
		return nil, fakeAPIError{code: "BadDigest"}
	}
	version := c.newVersion()
	c.lastPutVersion = version
	object := fakeObject{body: append([]byte(nil), body...), metadata: clone(input.Metadata), modified: time.Now().UTC(), version: version}
	c.objects[key] = object
	if c.versions[key] == nil {
		c.versions[key] = make(map[string]fakeObject)
	}
	c.versions[key][version] = object
	if c.omitPutVersion {
		return &awss3.PutObjectOutput{}, nil
	}
	if c.putOutputVersion != "" {
		return &awss3.PutObjectOutput{VersionId: &c.putOutputVersion}, nil
	}
	return &awss3.PutObjectOutput{VersionId: &version}, nil
}

func (c *fakeClient) HeadObject(_ context.Context, input *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	key := dereference(input.Key)
	c.lastHeadVersion = dereference(input.VersionId)
	c.headVersions = append(c.headVersions, c.lastHeadVersion)
	var object fakeObject
	var exists bool
	if c.lastHeadVersion != "" {
		object, exists = c.versions[key][c.lastHeadVersion]
		if !exists {
			return nil, fakeAPIError{code: "NoSuchVersion"}
		}
	} else {
		object, exists = c.objects[key]
		if !exists {
			return nil, fakeAPIError{code: "NotFound"}
		}
	}
	length := int64(len(object.body))
	return &awss3.HeadObjectOutput{ContentLength: &length, Metadata: clone(object.metadata), VersionId: &object.version}, nil
}

func (c *fakeClient) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	key := dereference(input.Key)
	c.lastGetVersion = dereference(input.VersionId)
	c.getVersions = append(c.getVersions, c.lastGetVersion)
	var object fakeObject
	var exists bool
	if c.lastGetVersion != "" {
		object, exists = c.versions[key][c.lastGetVersion]
		if !exists {
			return nil, fakeAPIError{code: "NoSuchVersion"}
		}
	} else {
		object, exists = c.objects[key]
		if !exists {
			return nil, fakeAPIError{code: "NoSuchKey"}
		}
	}
	length := int64(len(object.body))
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(object.body)), ContentLength: &length, VersionId: &object.version}, nil
}

func (c *fakeClient) ListObjectsV2(_ context.Context, input *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	c.listCalls++
	var keys []string
	for key := range c.objects {
		if strings.HasPrefix(key, dereference(input.Prefix)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if token := dereference(input.ContinuationToken); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil {
			return nil, err
		}
		start = parsed
	}
	pageSize := c.listPageSize
	if pageSize <= 0 {
		pageSize = len(keys)
	}
	end := min(start+pageSize, len(keys))
	result := &awss3.ListObjectsV2Output{}
	for _, key := range keys[start:end] {
		object := c.objects[key]
		size := int64(len(object.body))
		modified := object.modified
		result.Contents = append(result.Contents, types.Object{Key: testPointer(key), Size: &size, LastModified: &modified})
	}
	if end < len(keys) {
		result.IsTruncated = testPointer(true)
		result.NextContinuationToken = testPointer(strconv.Itoa(end))
	}
	return result, nil
}

func (c *fakeClient) DeleteObjects(_ context.Context, input *awss3.DeleteObjectsInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectsOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	batch := make([]string, 0, len(input.Delete.Objects))
	for _, object := range input.Delete.Objects {
		key := dereference(object.Key)
		batch = append(batch, key)
		delete(c.objects, key)
	}
	c.deleteBatches = append(c.deleteBatches, batch)
	return &awss3.DeleteObjectsOutput{}, nil
}

func (c *fakeClient) CreateMultipartUpload(_ context.Context, input *awss3.CreateMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.CreateMultipartUploadOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	c.nextUpload++
	id := fmt.Sprintf("upload-%d", c.nextUpload)
	c.multipart[id] = fakeMultipart{key: dereference(input.Key), metadata: clone(input.Metadata)}
	return &awss3.CreateMultipartUploadOutput{UploadId: &id}, nil
}

func (c *fakeClient) ListMultipartUploads(_ context.Context, input *awss3.ListMultipartUploadsInput, _ ...func(*awss3.Options)) (*awss3.ListMultipartUploadsOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.multipartListCalls++
	prefix := dereference(input.Prefix)
	keys := make([]string, 0, len(c.multipart))
	for id, upload := range c.multipart {
		if strings.HasPrefix(upload.key, prefix) {
			keys = append(keys, upload.key+"\x00"+id)
		}
	}
	sort.Strings(keys)
	start := 0
	if marker := dereference(input.UploadIdMarker); marker != "" {
		for index, value := range keys {
			if strings.HasSuffix(value, "\x00"+marker) {
				start = index + 1
				break
			}
		}
	}
	pageSize := c.multipartListPageSize
	if pageSize <= 0 {
		pageSize = len(keys)
	}
	end := min(start+pageSize, len(keys))
	result := &awss3.ListMultipartUploadsOutput{IsTruncated: testPointer(end < len(keys))}
	for _, value := range keys[start:end] {
		parts := strings.SplitN(value, "\x00", 2)
		key, id := parts[0], parts[1]
		result.Uploads = append(result.Uploads, types.MultipartUpload{Key: testPointer(key), UploadId: testPointer(id)})
	}
	if end < len(keys) {
		parts := strings.SplitN(keys[end-1], "\x00", 2)
		result.NextKeyMarker, result.NextUploadIdMarker = testPointer(parts[0]), testPointer(parts[1])
	}
	return result, nil
}

func (c *fakeClient) CompleteMultipartUpload(_ context.Context, input *awss3.CompleteMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	id := dereference(input.UploadId)
	upload, exists := c.multipart[id]
	if !exists {
		return nil, fakeAPIError{code: "NoSuchUpload"}
	}
	if _, exists := c.objects[upload.key]; exists && dereference(input.IfNoneMatch) == "*" {
		return nil, fakeAPIError{code: "PreconditionFailed"}
	}
	version := c.newVersion()
	c.lastMultipartVersion = version
	object := fakeObject{body: append([]byte(nil), upload.body...), metadata: clone(upload.metadata), modified: time.Now().UTC(), version: version}
	c.objects[upload.key] = object
	if c.versions[upload.key] == nil {
		c.versions[upload.key] = make(map[string]fakeObject)
	}
	c.versions[upload.key][version] = object
	delete(c.multipart, id)
	if c.omitMultipartVersion {
		return &awss3.CompleteMultipartUploadOutput{}, nil
	}
	return &awss3.CompleteMultipartUploadOutput{VersionId: &version}, nil
}

func (c *fakeClient) newVersion() string {
	c.nextVersion++
	return fmt.Sprintf("provider-version-%d", c.nextVersion)
}

func (c *fakeClient) AbortMultipartUpload(_ context.Context, input *awss3.AbortMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.AbortMultipartUploadOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	id := dereference(input.UploadId)
	if _, exists := c.multipart[id]; !exists {
		return nil, fakeAPIError{code: "NoSuchUpload"}
	}
	delete(c.multipart, id)
	return &awss3.AbortMultipartUploadOutput{}, nil
}

func (c *fakeClient) setMultipartBody(uploadID string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	upload := c.multipart[uploadID]
	upload.body = append([]byte(nil), body...)
	c.multipart[uploadID] = upload
}

func (c *fakeClient) object(key string) (fakeObject, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	object, exists := c.objects[key]
	return object, exists
}

type fakePresigner struct {
	lastChecksum string
}

func (p *fakePresigner) PresignUploadPart(_ context.Context, input *awss3.UploadPartInput, _ ...func(*awss3.PresignOptions)) (*awsv4.PresignedHTTPRequest, error) {
	p.lastChecksum = dereference(input.ChecksumSHA256)
	headers := http.Header{}
	headers.Set("X-Test", "signed")
	return &awsv4.PresignedHTTPRequest{URL: fmt.Sprintf("https://uploads.example/part/%d", dereference(input.PartNumber)), SignedHeader: headers}, nil
}

type fakeAPIError struct{ code string }

func (e fakeAPIError) Error() string     { return e.code }
func (e fakeAPIError) ErrorCode() string { return e.code }

func blobFor(body []byte) storage.Blob {
	sum := sha256.Sum256(body)
	return storage.Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func dereference[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

func clone(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func testPointer[T any](value T) *T { return &value }

var _ manageds3.Client = (*fakeClient)(nil)
var _ manageds3.PartPresigner = (*fakePresigner)(nil)
var _ manageds3.Client = (*awss3.Client)(nil)
var _ manageds3.PartPresigner = (*awss3.PresignClient)(nil)
