//go:build fai520qualification

package s3_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
	manageds3 "github.com/flidai/leapview/internal/manageddata/storage/s3"
)

type providerObservationArtifact struct {
	RevisionID         manageddata.RevisionID      `json:"revisionID"`
	ManifestRevisionID string                      `json:"manifestRevisionID"`
	Endpoint           string                      `json:"endpoint"`
	Region             string                      `json:"region"`
	Bucket             string                      `json:"bucket"`
	Objects            []providerObjectObservation `json:"objects"`
}

type providerObjectObservation struct {
	Path     string `json:"path"`
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Version  string `json:"versionID"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

func TestFAI520ManagedProviderBindingObservationReplay(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "1")
	ctx, client, _, bucket, endpoint := historicalProvider(t)
	repo := closureRepository(t)
	profile := storage.ProviderProfileIdentity{
		ProfileID: "managed-source-minio-qualification", Implementation: "s3", AccountIdentity: "qualification",
		Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Namespace: "project-a",
	}
	store, err := manageds3.New(client, awss3.NewPresignClient(client), manageds3.Config{Bucket: bucket, Prefix: "project-a", ObservationProfile: &profile, ObservationRecorder: repo})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := repo.CreateCollection(ctx, manageddata.CreateCollectionInput{ProjectID: "project_a", ConnectionID: "connection_a", Name: "provider-binding"})
	if err != nil {
		t.Fatal(err)
	}

	objects := []struct {
		path string
		body []byte
		mime string
	}{
		{path: "orders.csv", body: []byte("order_id,amount\n1,10\n"), mime: "text/csv"},
		{path: "customers.csv", body: []byte("customer_id,name\n1,Ada\n"), mime: "text/csv"},
		{path: "metadata.json", body: []byte(`{"dataset":"orders","format":"qualification"}`), mime: "application/json"},
	}
	manifest := manageddata.Manifest{}
	stored := make([]manageddata.StoredFile, 0, len(objects))
	selections := make(map[string]historicalSelection, len(objects))
	observed := make([]providerObjectObservation, 0, len(objects))
	var writeObservations storage.ProviderVersionObservationSet
	for _, object := range objects {
		blob, putErr := store.Put(ctx, blobFor(object.body), bytes.NewReader(object.body))
		if putErr != nil {
			t.Fatal(putErr)
		}
		file := manageddata.File{Path: object.path, SHA256: blob.SHA256, Size: blob.Size}
		manifest.Files = append(manifest.Files, file)
		stored = append(stored, manageddata.StoredFile{File: file, StorageKey: blob.URI, MediaType: object.mime})
		if blob.ProviderVersion == nil {
			t.Fatalf("managed write did not return provider-version observation for %s", object.path)
		}
		if err := writeObservations.Add(*blob.ProviderVersion); err != nil {
			t.Fatalf("record write observation for %s: %v", object.path, err)
		}
		key := strings.TrimPrefix(blob.URI, "s3://"+bucket+"/")
		version := blob.ProviderVersion.VersionID
		if blob.ProviderVersion.Profile != profile || blob.ProviderVersion.ObjectKey != key || blob.ProviderVersion.SHA256 != blob.SHA256 || blob.ProviderVersion.Size != blob.Size {
			t.Fatalf("managed write observation for %s = %#v", object.path, blob.ProviderVersion)
		}
		selection := historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: key, Version: version}
		selections[object.path] = selection
		observed = append(observed, providerObjectObservation{Path: object.path, Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: key, Version: version, SHA256: blob.SHA256, Size: blob.Size})
	}
	if captured := writeObservations.Snapshot(); len(captured) != len(objects) {
		t.Fatalf("captured write observations = %d, want %d", len(captured), len(objects))
	} else {
		for _, observation := range captured {
			persisted, err := repo.ProviderVersionObservation(ctx, observation.Profile.ProfileID, observation.ObjectKey)
			if err != nil || persisted != observation {
				t.Fatalf("durable write observation = %#v, %v; want %#v", persisted, err, observation)
			}
		}
	}
	upload, err := repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{ID: "upload_provider_binding", CollectionID: collection.ID, Manifest: manifest, StorageBackend: "s3", StagingPrefix: "staging/provider-binding", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: upload.ID, RevisionID: "revision_provider_binding", Files: stored})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Digest != manifest.RevisionID() || revision.FileCount != int64(len(objects)) {
		t.Fatalf("persisted managed revision = %#v; manifest = %s", revision, manifest.RevisionID())
	}
	if revision.ID == "" || revision.ManifestJSON == "" {
		t.Fatalf("persisted managed revision omitted identity or metadata: %#v", revision)
	}
	sort.Slice(observed, func(i, j int) bool { return observed[i].Path < observed[j].Path })

	// This is a test observation artifact, not a recovery-set or authoritative
	// frontier binding. It records the exact provider reads needed to replay the
	// managed revision closure without inventing a product admission contract.
	artifact := providerObservationArtifact{RevisionID: revision.ID, ManifestRevisionID: manifest.RevisionID(), Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Objects: observed}
	artifactPath := filepath.Join(t.TempDir(), "provider-observations.json")
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	replayedBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var replay providerObservationArtifact
	if err := json.Unmarshal(replayedBytes, &replay); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay, artifact) {
		t.Fatalf("observation artifact changed across persistence round-trip:\n%#v\n%#v", replay, artifact)
	}
	if replay.RevisionID != revision.ID || replay.ManifestRevisionID != manifest.RevisionID() || len(replay.Objects) != len(manifest.Files) {
		t.Fatalf("observation artifact identity = %#v", replay)
	}
	replayedSelections := make(map[string]historicalSelection, len(replay.Objects))
	for _, object := range replay.Objects {
		if object.Endpoint != endpoint || object.Region != "us-east-1" || object.Bucket != bucket || object.Key == "" || object.Version == "" || object.SHA256 == "" || object.Size <= 0 {
			t.Fatalf("incomplete provider observation = %#v", object)
		}
		replayedSelections[object.Path] = historicalSelection{Endpoint: object.Endpoint, Region: object.Region, Bucket: object.Bucket, Key: object.Key, Version: object.Version}
	}

	first, reason := observeHistoricalClosure(t, ctx, repo, revision.ID, endpoint, bucket, selections, func(int) *awss3.Client { return client })
	if reason != "verified" || len(first) != len(objects) {
		t.Fatalf("initial managed closure = %s, %d members", reason, len(first))
	}
	data := objects[0]
	dataBlob := blobFor(data.body)
	dataSelection := selections[data.path]
	boundary := historicalSelection{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Key: dataSelection.Key}
	if _, result := observeHistoricalBytes(ctx, client, boundary, dataSelection, storage.Blob{SHA256: strings.Repeat("0", 64), Size: dataBlob.Size}); result != "integrity_mismatch" {
		t.Fatalf("digest mismatch result = %s", result)
	}
	wrongBody := bytes.Repeat([]byte("x"), len(data.body))
	wrongVersionOutput, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(dataSelection.Key), Body: bytes.NewReader(wrongBody)})
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion := aws.ToString(wrongVersionOutput.VersionId)
	if wrongVersion == "" || wrongVersion == "null" {
		t.Fatal("provider did not return wrong-byte version")
	}
	if _, result := observeHistoricalBytes(ctx, client, boundary, dataSelection, dataBlob); result != "verified" {
		t.Fatalf("exact historical version after current overwrite = %s, want verified", result)
	}
	wrongSelection := dataSelection
	wrongSelection.Version = wrongVersion
	if _, result := observeHistoricalBytes(ctx, client, boundary, wrongSelection, dataBlob); result != "integrity_mismatch" {
		t.Fatalf("wrong-version bytes result = %s", result)
	}
	for attempt := 0; attempt < 2; attempt++ {
		replayDigests, replayReason := observeHistoricalClosure(t, ctx, repo, replay.RevisionID, replay.Endpoint, replay.Bucket, replayedSelections, func(int) *awss3.Client { return client })
		if replayReason != "verified" || !reflect.DeepEqual(replayDigests, first) {
			t.Fatalf("observation replay %d = %s, %#v; want verified %#v", attempt, replayReason, replayDigests, first)
		}
	}
	endpointSelection := dataSelection
	endpointSelection.Endpoint = "http://untrusted.invalid"
	if _, result := observeHistoricalBytes(ctx, client, boundary, endpointSelection, dataBlob); result != "namespace_mismatch" {
		t.Fatalf("provider endpoint mismatch result = %s", result)
	}

	missing := maps.Clone(selections)
	delete(missing, "metadata.json")
	missingDigests, missingReason := observeHistoricalClosure(t, ctx, repo, revision.ID, endpoint, bucket, missing, func(int) *awss3.Client { return client })
	if missingReason != "missing_dependency" || len(missingDigests) != 1 {
		t.Fatalf("missing observation = %s, %d members; want missing_dependency, 1", missingReason, len(missingDigests))
	}
	conflictArtifact := artifact
	conflict := conflictArtifact.Objects[0]
	conflict.Version = wrongVersion
	conflictArtifact.Objects = append(append([]providerObjectObservation(nil), conflictArtifact.Objects...), conflict)
	conflictEncoded, err := json.Marshal(conflictArtifact)
	if err != nil {
		t.Fatal(err)
	}
	var conflictReplay providerObservationArtifact
	if err := json.Unmarshal(conflictEncoded, &conflictReplay); err != nil {
		t.Fatal(err)
	}
	if len(conflictReplay.Objects) != len(artifact.Objects)+1 || conflictReplay.Objects[0].Path != conflictReplay.Objects[len(conflictReplay.Objects)-1].Path || conflictReplay.Objects[len(conflictReplay.Objects)-1].Version != wrongVersion {
		t.Fatalf("conflicting duplicate observation was not preserved for characterization: %#v", conflictReplay.Objects)
	}
	captured := writeObservations.Snapshot()
	conflictingWrite := captured[0]
	conflictingWrite.SHA256 = strings.Repeat("0", 64)
	if err := writeObservations.Add(conflictingWrite); !errors.Is(err, storage.ErrObservationConflict) {
		t.Fatalf("conflicting write observation error = %v", err)
	}
	t.Log("remaining gap: durable write observations are not yet selected by an authoritative capture or signed into Manifest v2")
}
