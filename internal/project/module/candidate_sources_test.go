package module_test

import (
	"bytes"
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

	missing, err := synchronizer.Plan(t.Context(), scope, request)
	if err != nil || len(missing) != len(snapshot.Artifacts) {
		t.Fatalf("plan missing=%d error=%v", len(missing), err)
	}
	if err := synchronizer.Upload(t.Context(), project.CandidateSourceScope{
		ProjectID: snapshot.ProjectID, OwnerID: "principal_2",
	}, missing[0], bytes.NewReader(snapshot.Artifacts[0].Content)); err == nil {
		t.Fatal("foreign principal uploaded against another author's plan")
	}
	byDigest := make(map[string]projectdevloop.Artifact, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		byDigest[artifact.Digest] = artifact
	}
	for _, identity := range missing {
		artifact := byDigest[identity]
		if err := synchronizer.Upload(t.Context(), scope, identity, bytes.NewReader(artifact.Content)); err != nil {
			t.Fatal(err)
		}
	}
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
	if resolved.SourceAttestationDigest == "" || resolved.SourceAttestationDigest != committed.SourceAttestationDigest {
		t.Fatalf("resolved attestation digest = %q, committed = %q", resolved.SourceAttestationDigest, committed.SourceAttestationDigest)
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
	missing, err := first.Plan(t.Context(), scope, request)
	if err != nil || len(missing) == 0 {
		t.Fatalf("Plan() missing=%d error=%v", len(missing), err)
	}

	restarted, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	byDigest := make(map[string]projectdevloop.Artifact, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		byDigest[artifact.Digest] = artifact
	}
	artifact := byDigest[missing[0]]
	if err := restarted.Upload(
		t.Context(), scope, missing[0], bytes.NewReader(artifact.Content),
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
	missing, err := synchronizer.Plan(t.Context(), scope, request)
	require.NoError(t, err)
	byDigest := make(map[string]projectdevloop.Artifact, len(snapshot.Artifacts))
	for _, artifact := range snapshot.Artifacts {
		byDigest[artifact.Digest] = artifact
	}
	for _, identity := range missing {
		artifact := byDigest[identity]
		require.NoError(t, synchronizer.Upload(t.Context(), scope, identity, bytes.NewReader(artifact.Content)))
	}
	committed, err := synchronizer.Commit(t.Context(), scope, request)
	require.NoError(t, err)
	require.Equal(t, request.SourceRevision, committed.SourceRevision)

	restarted, err := projectmodule.NewCandidateSourceSynchronizer(root)
	require.NoError(t, err)
	reader, ok := restarted.(project.CandidateSourceSnapshotReader)
	require.True(t, ok)
	resolved, err := reader.Snapshot(t.Context(), project.CandidateSourceScope{ProjectID: snapshot.ProjectID}, snapshot.Digest)
	require.NoError(t, err)
	require.Equal(t, request.SourceRevision, resolved.SourceRevision)

	conflicting := request
	conflicting.SourceRevision = &project.CandidateSourceRevision{Revision: "commit-different"}
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

func synchronizationRequest(snapshot projectdevloop.Snapshot) project.CandidateSynchronizationRequest {
	request := project.CandidateSynchronizationRequest{
		ProjectFile: snapshot.ProjectFile, ArtifactDigest: snapshot.Digest,
		Artifacts: make([]project.CandidateSourceArtifact, len(snapshot.Artifacts)),
	}
	for index, artifact := range snapshot.Artifacts {
		request.Artifacts[index] = project.CandidateSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest,
		}
	}
	return request
}
