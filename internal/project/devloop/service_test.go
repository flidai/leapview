package devloop

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReconcilePreservesLastValidCandidateWhenNextBuildFails(t *testing.T) {
	builder := &scriptedBuilder{steps: []buildStep{
		{snapshot: testSnapshot("first")},
		{err: errors.New("models/orders.yaml:12: unknown source")},
	}}
	remote := &recordingRemote{}
	service, err := New(builder, remote)
	require.NoError(t, err)

	first, err := service.Reconcile(t.Context())
	if err != nil || first.Status != StatusSynchronized {
		t.Fatalf("first reconcile = %#v, %v", first, err)
	}
	next, err := service.Reconcile(t.Context())
	if err == nil || next.Status != StatusInvalid {
		t.Fatalf("invalid reconcile = %#v, %v", next, err)
	}
	if len(remote.requests) != 1 {
		t.Fatalf("remote requests = %d, want only last valid build", len(remote.requests))
	}
	if next.Candidate.ID != first.Candidate.ID ||
		next.Candidate.ArtifactDigest != first.Candidate.ArtifactDigest {
		t.Fatalf("invalid build replaced last valid candidate: first=%#v next=%#v", first, next)
	}
}

func TestReconcileIsIdempotentAndRetriesFailedSynchronization(t *testing.T) {
	snapshot := testSnapshot("retry")
	builder := &scriptedBuilder{steps: []buildStep{
		{snapshot: snapshot},
		{snapshot: snapshot},
		{snapshot: snapshot},
	}}
	remote := &recordingRemote{errors: []error{errors.New("temporary disconnect"), nil}}
	service, err := New(builder, remote)
	require.NoError(t, err)

	if result, err := service.Reconcile(t.Context()); err == nil || result.Status != StatusRetryable {
		t.Fatalf("first reconcile = %#v, %v, want retryable", result, err)
	}
	synchronized, err := service.Reconcile(t.Context())
	if err != nil || synchronized.Status != StatusSynchronized {
		t.Fatalf("second reconcile = %#v, %v", synchronized, err)
	}
	unchanged, err := service.Reconcile(t.Context())
	if err != nil || unchanged.Status != StatusUnchanged {
		t.Fatalf("third reconcile = %#v, %v", unchanged, err)
	}
	if len(remote.requests) != 2 {
		t.Fatalf("remote requests = %d, want retry plus success", len(remote.requests))
	}
}

func TestConcurrentReconcileSerializesOneDigestSynchronization(t *testing.T) {
	builder := &constantBuilder{snapshot: testSnapshot("concurrent")}
	remote := &recordingRemote{}
	service, err := New(builder, remote)
	require.NoError(t, err)

	var wait sync.WaitGroup
	results := make(chan Result, 8)
	errors := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.Reconcile(t.Context())
			results <- result
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	synchronized := 0
	for result := range results {
		if result.Status == StatusSynchronized {
			synchronized++
		}
	}
	if synchronized != 1 || len(remote.requests) != 1 {
		t.Fatalf("synchronized results = %d, remote requests = %d; want 1, 1", synchronized, len(remote.requests))
	}
}

func TestReconcileSynchronizesChangedSnapshot(t *testing.T) {
	first := testSnapshot("first")
	second := testSnapshot("second")
	builder := &scriptedBuilder{steps: []buildStep{{snapshot: first}, {snapshot: second}}}
	remote := &recordingRemote{}
	service, err := New(builder, remote)
	require.NoError(t, err)
	if _, err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(remote.requests) != 2 || remote.requests[1].Snapshot.Digest == remote.requests[0].Snapshot.Digest {
		t.Fatalf("changed snapshot was not synchronized: %#v", remote.requests)
	}
}

func TestReconcileSynchronizesChangedSourceRevisionForSameContent(t *testing.T) {
	first := testSnapshot("same")
	first.SourceRevision = &SourceRevision{
		Revision: "commit-a", Repository: "https://code.example/acme/analytics",
		Ref: "refs/pull/42/head", ChangeID: "pull/42",
	}
	second := first
	second.SourceRevision = &SourceRevision{
		Revision: "commit-b", Repository: first.SourceRevision.Repository,
		Ref: first.SourceRevision.Ref, ChangeID: first.SourceRevision.ChangeID,
	}
	remote := &recordingRemote{}
	service, err := New(
		&scriptedBuilder{steps: []buildStep{{snapshot: first}, {snapshot: second}}},
		remote,
	)
	require.NoError(t, err)
	if _, err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(remote.requests) != 2 {
		t.Fatalf("remote requests = %d, want new synchronization for changed revision", len(remote.requests))
	}
	if remote.requests[1].Snapshot.Digest != remote.requests[0].Snapshot.Digest {
		t.Fatalf("source revision changed content digest: %#v", remote.requests)
	}
	if remote.requests[1].Snapshot.SourceRevision == nil ||
		remote.requests[1].Snapshot.SourceRevision.Revision != "commit-b" {
		t.Fatalf("second source revision = %#v", remote.requests[1].Snapshot.SourceRevision)
	}
}

type buildStep struct {
	snapshot Snapshot
	err      error
}

type scriptedBuilder struct {
	steps []buildStep
	index int
}

func (builder *scriptedBuilder) Build(context.Context) (Snapshot, error) {
	step := builder.steps[builder.index]
	builder.index++
	return step.snapshot, step.err
}

type constantBuilder struct {
	snapshot Snapshot
}

func (builder *constantBuilder) Build(context.Context) (Snapshot, error) {
	return builder.snapshot, nil
}

type recordingRemote struct {
	mu       sync.Mutex
	requests []SyncRequest
	errors   []error
}

func (remote *recordingRemote) Synchronize(_ context.Context, request SyncRequest) (Candidate, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.requests = append(remote.requests, request)
	if len(remote.errors) > 0 {
		err := remote.errors[0]
		remote.errors = remote.errors[1:]
		if err != nil {
			return Candidate{}, err
		}
	}
	return Candidate{
		ID: "cand_1", ProjectID: request.Snapshot.ProjectID,
		OwnerID:          "principal_1",
		ArtifactDigest:   request.Snapshot.Digest,
		PreviewURL:       "https://target.example/candidates/cand_1",
		TargetID:         "target_1",
		Environment:      "development",
		ProvenanceDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Revision:         1,
	}, nil
}

func testSnapshot(content string) Snapshot {
	artifacts := []Artifact{contentArtifact("leapview.yaml", []byte(content))}
	return Snapshot{
		ProjectID: "sales_project", ProjectFile: "leapview.yaml",
		Digest: candidateSetDigest("sales_project", "leapview.yaml", artifacts), Artifacts: artifacts,
	}
}
