package devloop

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestTransportRemoteUsesNativeSynchronizationWhenAvailable(t *testing.T) {
	transport := &nativeSyncTransport{}
	remote, err := NewTransportRemote(transport, 3)
	require.NoError(t, err)
	snapshot := testSnapshot("native")
	candidate, err := remote.Synchronize(t.Context(), SyncRequest{Snapshot: snapshot})
	require.NoError(t, err)
	if candidate.ID != "native-candidate" || transport.nativeCalls != 1 {
		t.Fatalf("candidate=%#v native calls=%d", candidate, transport.nativeCalls)
	}
	if transport.commits != 0 {
		t.Fatalf("native synchronization invoked legacy commit %d times", transport.commits)
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

func TestTransportRemoteRetainSourceBoundsUploads(t *testing.T) {
	artifacts := []Artifact{contentArtifact("leapview.yaml", []byte("project"))}
	for index := range 7 {
		artifacts = append(artifacts, contentArtifact(
			"models/model-"+string(rune('a'+index))+".yaml",
			[]byte{byte(index + 1)},
		))
	}
	snapshot := testSnapshotWithArtifacts("source-only-bounded", artifacts)
	missing := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		missing = append(missing, artifact.Digest)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	transport := &recordingSyncTransport{
		missing:       missing,
		uploadStarted: make(chan struct{}, len(artifacts)),
		uploadRelease: release,
	}
	remote, err := NewTransportRemote(transport, 3)
	require.NoError(t, err)

	type result struct{ err error }
	completed := make(chan result, 1)
	go func() {
		_, err := remote.RetainSource(t.Context(), snapshot)
		completed <- result{err: err}
	}()

	for index := 0; index < 3; index++ {
		select {
		case <-transport.uploadStarted:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for bounded source upload %d", index+1)
		}
	}
	transport.mu.Lock()
	active, maxActive, uploaded := transport.active, transport.maxActive, len(transport.uploaded)
	transport.mu.Unlock()
	if active != 3 || maxActive != 3 || uploaded != 3 {
		t.Fatalf("source uploads active=%d max=%d started=%d, want 3 each", active, maxActive, uploaded)
	}
	select {
	case <-transport.uploadStarted:
		t.Fatal("source retention started an upload beyond the configured limit")
	default:
	}

	releaseAll()
	select {
	case result := <-completed:
		require.NoError(t, result.err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source retention")
	}
	transport.mu.Lock()
	maxActive = transport.maxActive
	retained := transport.retained
	transport.mu.Unlock()
	require.Equal(t, 3, maxActive)
	require.Equal(t, 1, retained)
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
	mu            sync.Mutex
	plan          SynchronizationPlanRequest
	missing       []string
	uploaded      []Artifact
	uploadStarted chan struct{}
	uploadRelease <-chan struct{}
	active        int
	maxActive     int
	uploadErr     error
	commits       int
	retained      int
}

type nativeSyncTransport struct {
	nativeCalls int
	commits     int
}

func (transport *nativeSyncTransport) Plan(context.Context, SynchronizationPlanRequest) (SynchronizationPlan, error) {
	return SynchronizationPlan{PlanID: "legacy-plan"}, nil
}

func (*nativeSyncTransport) Upload(context.Context, SynchronizationPlanRequest, Artifact) error {
	return nil
}

func (transport *nativeSyncTransport) Commit(context.Context, SynchronizationPlanRequest) (Candidate, error) {
	transport.commits++
	return Candidate{}, errors.New("legacy commit must not be called")
}

func (transport *nativeSyncTransport) SynchronizeNative(_ context.Context, request SyncRequest, maxParallelUploads int) (Candidate, error) {
	transport.nativeCalls++
	if maxParallelUploads != 3 {
		return Candidate{}, errors.New("native upload limit was not propagated")
	}
	return Candidate{ID: "native-candidate", ProjectID: request.Snapshot.ProjectID, ArtifactDigest: request.Snapshot.Digest}, nil
}

func (transport *recordingSyncTransport) Plan(
	_ context.Context,
	request SynchronizationPlanRequest,
) (SynchronizationPlan, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.plan = request
	return SynchronizationPlan{PlanID: "plan-transport", MissingDigests: append([]string(nil), transport.missing...)}, nil
}

func (transport *recordingSyncTransport) Upload(
	ctx context.Context,
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
	transport.mu.Unlock()
	if transport.uploadStarted != nil {
		transport.uploadStarted <- struct{}{}
	}
	if transport.uploadRelease != nil {
		select {
		case <-transport.uploadRelease:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	transport.mu.Lock()
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
