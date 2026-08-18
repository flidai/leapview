package devloop

import (
	"context"
	"errors"
	"sync"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestTransportRemoteUploadsOnlyMissingContentBeforeCommit(t *testing.T) {
	snapshot := testSnapshotWithArtifacts("transport", []Artifact{
		contentArtifact("leapview.yaml", []byte("project")),
		contentArtifact("models/orders.yaml", []byte("orders")),
		contentArtifact("models/customers.yaml", []byte("customers")),
	})
	transport := &recordingSyncTransport{
		missing: []string{snapshot.Artifacts[0].Digest, snapshot.Artifacts[2].Digest},
	}
	remote, err := NewTransportRemote(transport, 2)
	require.NoError(t, err)

	candidate, err := remote.Synchronize(t.Context(), SyncRequest{
		Snapshot: snapshot, ExpectedCandidateID: "cand_existing",
		ExpectedArtifactDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	require.NoError(t, err)
	if candidate.ArtifactDigest != snapshot.Digest || len(transport.uploaded) != 2 || transport.commits != 1 {
		t.Fatalf("candidate=%#v uploads=%#v commits=%d", candidate, transport.uploaded, transport.commits)
	}
	if transport.plan.ExpectedArtifactDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("plan expected digest = %q", transport.plan.ExpectedArtifactDigest)
	}
	if transport.plan.ExpectedCandidateID != "cand_existing" {
		t.Fatalf("plan expected candidate = %q", transport.plan.ExpectedCandidateID)
	}
	if len(transport.plan.Artifacts) != len(snapshot.Artifacts) {
		t.Fatalf("plan artifact references = %d, want %d", len(transport.plan.Artifacts), len(snapshot.Artifacts))
	}
}

func TestTransportRemoteRetainSourceDoesNotPrepareCandidate(t *testing.T) {
	snapshot := testSnapshotWithArtifacts("source-only", []Artifact{
		contentArtifact("leapview.yaml", []byte("project")),
		contentArtifact("models/orders.yaml", []byte("orders")),
	})
	transport := &recordingSyncTransport{missing: []string{snapshot.Artifacts[0].Digest, snapshot.Artifacts[1].Digest}}
	remote, err := NewTransportRemote(transport, 2)
	require.NoError(t, err)

	retained, err := remote.RetainSource(t.Context(), snapshot)
	require.NoError(t, err)
	require.Equal(t, snapshot.ProjectID, retained.ProjectID)
	require.Equal(t, snapshot.Digest, retained.SourceDigest)
	require.Equal(t, 1, transport.retained)
	require.Equal(t, 0, transport.commits, "source retention must not create a candidate")
	require.True(t, transport.plan.SourceOnly)
	require.Len(t, transport.uploaded, 2)
}

func TestTransportRemoteRejectsUnknownMissingDigestWithoutUploadingOrCommit(t *testing.T) {
	transport := &recordingSyncTransport{missing: []string{
		"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}}
	remote, err := NewTransportRemote(transport, 2)
	require.NoError(t, err)

	if _, err := remote.Synchronize(t.Context(), SyncRequest{Snapshot: testSnapshot("unknown")}); err == nil {
		t.Fatal("remote accepted a missing digest outside the planned snapshot")
	}
	if len(transport.uploaded) != 0 || transport.commits != 0 {
		t.Fatalf("uploads=%d commits=%d, want 0, 0", len(transport.uploaded), transport.commits)
	}
}

func TestTransportRemoteBoundsUploadsAndDoesNotCommitPartialFailure(t *testing.T) {
	artifacts := []Artifact{contentArtifact("leapview.yaml", []byte("project"))}
	for index := range 12 {
		artifacts = append(artifacts, contentArtifact(
			"models/model-"+string(rune('a'+index))+".yaml",
			[]byte{byte(index + 1)},
		))
	}
	snapshot := testSnapshotWithArtifacts("bounded", artifacts)
	missing := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		missing = append(missing, artifact.Digest)
	}
	injected := errors.New("upload failed")
	transport := &recordingSyncTransport{missing: missing, uploadErr: injected}
	remote, err := NewTransportRemote(transport, 3)
	require.NoError(t, err)

	if _, err := remote.Synchronize(t.Context(), SyncRequest{Snapshot: snapshot}); !errors.Is(err, injected) {
		t.Fatalf("synchronize error = %v, want injected upload failure", err)
	}
	if transport.maxActive > 3 {
		t.Fatalf("maximum concurrent uploads = %d, want <= 3", transport.maxActive)
	}
	if transport.commits != 0 {
		t.Fatalf("partial upload committed snapshot %d times", transport.commits)
	}
}

type recordingSyncTransport struct {
	mu        sync.Mutex
	plan      SynchronizationPlanRequest
	missing   []string
	uploaded  []Artifact
	active    int
	maxActive int
	uploadErr error
	commits   int
	retained  int
}

func (transport *recordingSyncTransport) Plan(
	_ context.Context,
	request SynchronizationPlanRequest,
) (SynchronizationPlan, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.plan = request
	return SynchronizationPlan{MissingDigests: append([]string(nil), transport.missing...)}, nil
}

func (transport *recordingSyncTransport) Upload(
	_ context.Context,
	_ SynchronizationPlanRequest,
	artifact Artifact,
) error {
	transport.mu.Lock()
	transport.active++
	if transport.active > transport.maxActive {
		transport.maxActive = transport.active
	}
	transport.uploaded = append(transport.uploaded, artifact)
	err := transport.uploadErr
	transport.active--
	transport.mu.Unlock()
	return err
}

func (transport *recordingSyncTransport) Commit(
	_ context.Context,
	request SynchronizationPlanRequest,
) (Candidate, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.commits++
	return Candidate{
		ID: "cand_transport", ProjectID: request.ProjectID,
		ArtifactDigest: request.ArtifactDigest,
		PreviewURL:     "https://target.example/candidates/cand_transport",
	}, nil
}

func (transport *recordingSyncTransport) RetainSource(
	_ context.Context,
	request SynchronizationPlanRequest,
) (RetainedSource, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.retained++
	return RetainedSource{
		ProjectID: request.ProjectID, SourceDigest: request.ArtifactDigest,
		ProjectDigest: request.ArtifactDigest, TargetID: "target_source_only",
		Environment: "test",
	}, nil
}

func testSnapshotWithArtifacts(projectID string, artifacts []Artifact) Snapshot {
	return Snapshot{
		ProjectID: projectgraph.ResourceID(projectID), ProjectFile: "leapview.yaml",
		Digest: candidateSetDigest(projectgraph.ResourceID(projectID), "leapview.yaml", artifacts), Artifacts: artifacts,
	}
}
