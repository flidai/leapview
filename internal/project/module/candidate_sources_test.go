package module_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/stretchr/testify/require"
)

func TestCandidateSourceSynchronizerAuthorizesOnlyPlannedOwnerUploads(t *testing.T) {
	snapshot, err := (projectdevloop.FilesystemBuilder{
		ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml"),
	}).Build(t.Context())
	require.NoError(t, err)
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(t.TempDir())
	require.NoError(t, err)
	scope := project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: "principal_1"}
	request := synchronizationRequest(snapshot)
	request.SourceRevision = &project.CandidateSourceRevision{
		Revision: "commit-authorized", Repository: "https://example.invalid/repo",
		Ref: "refs/heads/main", ChangeID: "change-authorized",
	}

	plan, err := synchronizer.Plan(t.Context(), scope, request)
	if err != nil || len(plan.MissingDigests) != len(snapshot.Artifacts) {
		t.Fatalf("plan missing=%d error=%v", len(plan.MissingDigests), err)
	}
	if err := synchronizer.Upload(t.Context(), project.CandidateSourceScope{
		ProjectID: snapshot.ProjectID, OwnerID: "principal_2",
	}, plan.PlanID, plan.MissingDigests[0], bytes.NewReader(snapshot.Artifacts[0].Content)); err == nil {
		t.Fatal("foreign principal uploaded against another author's plan")
	}
	byDigest := make(map[string]projectdevloop.Artifact, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		byDigest[artifact.Digest] = artifact
	}
	for _, identity := range plan.MissingDigests {
		artifact := byDigest[identity]
		if err := synchronizer.Upload(t.Context(), scope, plan.PlanID, identity, bytes.NewReader(artifact.Content)); err != nil {
			t.Fatal(err)
		}
	}
	request.PlanID = plan.PlanID
	committed, err := synchronizer.Commit(t.Context(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	reader, ok := synchronizer.(project.CandidateSourceSnapshotReader)
	if !ok {
		t.Fatal("synchronizer does not expose retained snapshot reader")
	}
	resolved, err := reader.Snapshot(t.Context(), scope, snapshot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProjectID != committed.ProjectID || resolved.ArtifactDigest != committed.ArtifactDigest || resolved.ProjectDigest != committed.ProjectDigest {
		t.Fatalf("resolved snapshot = %#v, committed = %#v", resolved, committed)
	}
	if resolved.SourceAttestationDigest != "" || resolved.SourceRevision != nil {
		t.Fatalf("plain snapshot unexpectedly carries provenance: %#v", resolved)
	}
	attestationReader, ok := synchronizer.(project.CandidateSourceAttestationReader)
	if !ok {
		t.Fatal("synchronizer does not expose retained attestation reader")
	}
	firstAttestation, err := attestationReader.SnapshotAttestation(t.Context(), scope, snapshot.Digest, committed.SourceAttestationDigest)
	if err != nil {
		t.Fatalf("exact attestation lookup failed: %v", err)
	}
	if firstAttestation.SourceRevision == nil || firstAttestation.SourceRevision.Revision != request.SourceRevision.Revision {
		t.Fatalf("first attestation revision = %#v", firstAttestation.SourceRevision)
	}
	if _, err := attestationReader.SnapshotAttestation(t.Context(), scope, snapshot.Digest, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("attestation lookup accepted an unknown digest")
	}
	if _, err := reader.Snapshot(t.Context(), project.CandidateSourceScope{ProjectID: scope.ProjectID, OwnerID: "other"}, snapshot.Digest); err != nil {
		t.Fatalf("snapshot lookup should be owner-independent for retained bytes: %v", err)
	}
}

func TestCandidateSourceSynchronizerRetainsActivePlanAcrossRestart(t *testing.T) {
	root := t.TempDir()
	snapshot, err := (projectdevloop.FilesystemBuilder{
		ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml"),
	}).Build(t.Context())
	require.NoError(t, err)
	scope := project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: "principal_1"}
	request := synchronizationRequest(snapshot)
	first, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	plan, err := first.Plan(t.Context(), scope, request)
	if err != nil || len(plan.MissingDigests) == 0 {
		t.Fatalf("Plan() missing=%d error=%v", len(plan.MissingDigests), err)
	}

	restarted, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	byDigest := make(map[string]projectdevloop.Artifact, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		byDigest[artifact.Digest] = artifact
	}
	artifact := byDigest[plan.MissingDigests[0]]
	if err := restarted.Upload(
		t.Context(), scope, plan.PlanID, plan.MissingDigests[0], bytes.NewReader(artifact.Content),
	); err != nil {
		t.Fatalf("Upload() after restart error = %v", err)
	}
}

func TestCandidateSourceSnapshotRetainsRevisionAcrossRestart(t *testing.T) {
	root := t.TempDir()
	snapshot, err := (projectdevloop.FilesystemBuilder{
		ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml"),
	}).Build(t.Context())
	require.NoError(t, err)
	scope := project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: "principal_1"}
	request := synchronizationRequest(snapshot)
	request.SourceRevision = &project.CandidateSourceRevision{
		Revision: "commit-immutable", Repository: "https://example.invalid/repo",
		Ref: "refs/heads/main", ChangeID: "change-42",
	}
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	plan, err := synchronizer.Plan(t.Context(), scope, request)
	require.NoError(t, err)
	byDigest := make(map[string]projectdevloop.Artifact, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		byDigest[artifact.Digest] = artifact
	}
	for _, identity := range plan.MissingDigests {
		artifact := byDigest[identity]
		require.NoError(t, synchronizer.Upload(t.Context(), scope, plan.PlanID, identity, bytes.NewReader(artifact.Content)))
	}
	request.PlanID = plan.PlanID
	committed, err := synchronizer.Commit(t.Context(), scope, request)
	require.NoError(t, err)
	require.Equal(t, request.SourceRevision, committed.SourceRevision)
	replayed, err := synchronizer.Commit(t.Context(), scope, request)
	require.NoError(t, err, "an exact commit retry must survive a lost response")
	require.Equal(t, committed, replayed)

	restarted, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	reader, ok := restarted.(project.CandidateSourceSnapshotReader)
	require.True(t, ok)
	resolved, err := reader.Snapshot(t.Context(), project.CandidateSourceScope{ProjectID: snapshot.ProjectID}, snapshot.Digest)
	require.NoError(t, err)
	require.Nil(t, resolved.SourceRevision, "content-only lookup must not select provenance implicitly")
	require.Empty(t, resolved.SourceAttestationDigest)
	attestationReader, ok := restarted.(project.CandidateSourceAttestationReader)
	require.True(t, ok)
	resolvedAttestation, err := attestationReader.SnapshotAttestation(t.Context(), scope, snapshot.Digest, committed.SourceAttestationDigest)
	require.NoError(t, err)
	require.Equal(t, request.SourceRevision, resolvedAttestation.SourceRevision)

	conflicting := request
	conflicting.IdempotencyKey = "test-plan-revision-2"
	conflicting.SourceRevision = &project.CandidateSourceRevision{Revision: "commit-different"}
	secondPlan, err := restarted.Plan(t.Context(), scope, conflicting)
	require.NoError(t, err)
	conflicting.PlanID = secondPlan.PlanID
	second, err := restarted.Commit(t.Context(), scope, conflicting)
	require.NoError(t, err, "same bytes may carry another append-only provenance attestation")
	require.Equal(t, conflicting.SourceRevision, second.SourceRevision)
	secondReader, ok := restarted.(project.CandidateSourceAttestationReader)
	require.True(t, ok)
	secondAttestation, err := secondReader.SnapshotAttestation(t.Context(), scope, snapshot.Digest, second.SourceAttestationDigest)
	require.NoError(t, err)
	require.Equal(t, conflicting.SourceRevision, secondAttestation.SourceRevision)
}

func TestCandidateSourceSynchronizerRejectsWhitespaceProjectIdentity(t *testing.T) {
	snapshot, err := (projectdevloop.FilesystemBuilder{
		ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml"),
	}).Build(t.Context())
	require.NoError(t, err)
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(t.TempDir())
	require.NoError(t, err)
	_, err = synchronizer.Plan(t.Context(), project.CandidateSourceScope{
		ProjectID: projectgraph.ResourceID(" " + snapshot.ProjectID.String()), OwnerID: "principal_1",
	}, synchronizationRequest(snapshot))
	if err == nil {
		t.Fatal("Plan() accepted whitespace-prefixed project identity")
	}
}

func TestCandidateSourceSynchronizerRequiresPlanIdentityAndSize(t *testing.T) {
	snapshot, err := (projectdevloop.FilesystemBuilder{ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")}).Build(t.Context())
	require.NoError(t, err)
	scope := project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: "principal_1"}
	request := synchronizationRequest(snapshot)
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(t.TempDir())
	require.NoError(t, err)
	plan, err := synchronizer.Plan(t.Context(), scope, request)
	require.NoError(t, err)
	artifact := snapshot.Artifacts[0]
	if err := synchronizer.Upload(t.Context(), scope, "wrong-plan", artifact.Digest, bytes.NewReader(artifact.Content)); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("wrong plan upload error = %v, want conflict", err)
	}
	if err := synchronizer.Upload(t.Context(), scope, plan.PlanID, artifact.Digest, bytes.NewReader(append(artifact.Content, 'x'))); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("wrong size upload error = %v, want conflict", err)
	}
	request.PlanID = "wrong-plan"
	if _, err := synchronizer.Commit(t.Context(), scope, request); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("wrong plan commit error = %v, want conflict", err)
	}
}

func TestCandidateSourceSynchronizerPlanReplayAndIndependentPlans(t *testing.T) {
	snapshot, err := (projectdevloop.FilesystemBuilder{ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")}).Build(t.Context())
	require.NoError(t, err)
	scope := project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: "principal_1"}
	request := synchronizationRequest(snapshot)
	request.IdempotencyKey = "idem-1"
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(t.TempDir())
	require.NoError(t, err)
	first, err := synchronizer.Plan(t.Context(), scope, request)
	require.NoError(t, err)
	replay, err := synchronizer.Plan(t.Context(), scope, request)
	require.NoError(t, err)
	if replay.PlanID != first.PlanID {
		t.Fatalf("exact plan replay changed plan ID: %q -> %q", first.PlanID, replay.PlanID)
	}
	drift := request
	drift.SourceRevision = &project.CandidateSourceRevision{Revision: "different"}
	if _, err := synchronizer.Plan(t.Context(), scope, drift); !errors.Is(err, project.ErrCandidateSourceConflict) {
		t.Fatalf("idempotency drift error = %v, want conflict", err)
	}
	independent := request
	independent.IdempotencyKey = "idem-2"
	second, err := synchronizer.Plan(t.Context(), scope, independent)
	require.NoError(t, err)
	if second.PlanID == first.PlanID {
		t.Fatal("independent plans reused opaque plan ID")
	}
}

func TestCandidateSourceSynchronizerRejectsMissingIdempotencyKey(t *testing.T) {
	snapshot, err := (projectdevloop.FilesystemBuilder{ProjectPath: filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")}).Build(t.Context())
	require.NoError(t, err)
	request := synchronizationRequest(snapshot)
	request.IdempotencyKey = ""
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(t.TempDir())
	require.NoError(t, err)
	_, err = synchronizer.Plan(t.Context(), project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: "principal_1"}, request)
	if !errors.Is(err, project.ErrCandidateSourceInvalid) {
		t.Fatalf("missing idempotency key error = %v, want invalid", err)
	}
}

func TestCandidateSourceSynchronizerRejectsMalformedPersistedPlan(t *testing.T) {
	root := t.TempDir()
	_, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	zeroDigest := "sha256:" + strings.Repeat("0", 64)
	record := map[string]any{
		"version": 2, "projectId": "project_demo", "ownerId": "principal_1",
		"idempotencyKey": "retry-1", "artifactDigest": zeroDigest,
		"planId": "NOT-A-PLAN-ID", "requestDigest": zeroDigest,
		"expiresAt": "2099-01-01T00:00:00Z", "missing": []string{zeroDigest},
		"sizes": map[string]int64{zeroDigest: 1},
	}
	content, err := json.Marshal(record)
	require.NoError(t, err)
	planDir := filepath.Join(root, ".synchronization-plans")
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "invalid.json"), content, 0o600))
	if _, err := projectmodule.NewCandidateSourceSynchronizer(root); err == nil || !strings.Contains(err.Error(), "plan id") {
		t.Fatalf("malformed persisted plan error = %v, want plan-id validation", err)
	}
}

func synchronizationRequest(snapshot projectdevloop.Snapshot) project.CandidateSynchronizationRequest {
	request := project.CandidateSynchronizationRequest{
		ProjectFile: snapshot.ProjectFile, ArtifactDigest: snapshot.Digest,
		IdempotencyKey: "test-plan",
		Artifacts:      make([]project.CandidateSourceArtifact, len(snapshot.Artifacts)),
	}
	for index, artifact := range snapshot.Artifacts {
		request.Artifacts[index] = project.CandidateSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
		}
	}
	return request
}
