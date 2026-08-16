package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestUploadArtifactReplaysAlreadyRecordedContent(t *testing.T) {
	content := []byte("compiled project artifact")
	digest := sha256.Sum256(content)
	identity := projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_1"}
	repo := &serviceTestReleaseRepository{current: Release{
		ID: "release_1", ServingIdentity: identity, Status: StatusDraft,
		ArtifactDigest:     "sha256:" + fmt.Sprintf("%x", digest[:]),
		ArtifactUploadedAt: "2026-07-27T19:00:00Z", ActualDigest: "sha256:" + fmt.Sprintf("%x", digest[:]),
		ArtifactSizeBytes: int64(len(content)),
	}}
	store := &serviceTestArtifactStore{}
	service := &Service{releases: repo, artifacts: store}

	got, err := service.UploadArtifact(t.Context(), "project_a", "release_1", wireDigest(content), bytes.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), got.SizeBytes)
	require.Equal(t, 0, store.saveCalls)
	require.False(t, repo.recorded)
}

func TestUploadArtifactRejectsDifferentContentAfterArtifactWasRecorded(t *testing.T) {
	original := []byte("original artifact")
	replacement := []byte("different artifact")
	digest := sha256.Sum256(original)
	identity := projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_1"}
	repo := &serviceTestReleaseRepository{current: Release{ID: "release_1", ServingIdentity: identity, Status: StatusDraft, ArtifactDigest: "sha256:" + fmt.Sprintf("%x", digest[:]), ArtifactUploadedAt: "2026-07-27T19:00:00Z"}}
	service := &Service{releases: repo, artifacts: &serviceTestArtifactStore{}}
	_, err := service.UploadArtifact(t.Context(), "project_a", "release_1", wireDigest(replacement), bytes.NewReader(replacement))
	require.ErrorIs(t, err, ErrDigest)
}

func TestUploadArtifactRejectsMalformedExpectedDigestWithoutSaving(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_1"}
	store := &serviceTestArtifactStore{}
	repo := &serviceTestReleaseRepository{current: Release{ID: "release_1", ServingIdentity: identity, Status: StatusDraft, ArtifactDigest: "not-a-sha256-digest"}}
	service := &Service{releases: repo, artifacts: store}
	_, err := service.UploadArtifact(t.Context(), "project_a", "release_1", "sha-256=:invalid:", strings.NewReader("artifact"))
	require.ErrorIs(t, err, ErrInvalid)
	require.Equal(t, 0, store.saveCalls)
}

func TestValidateFinalizationRequiresEveryArtifactToMatchReleaseConnectionPins(t *testing.T) {
	pinErr := errors.New("artifact pins disagree with release manifest")
	identity := projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_1"}
	repo := &serviceTestReleaseRepository{current: Release{
		ID: "release_1", ServingIdentity: identity, ProjectDigest: "sha256:" + strings.Repeat("a", 64), ArtifactDigest: "sha256:" + strings.Repeat("b", 64), ActualDigest: "sha256:" + strings.Repeat("b", 64), ArtifactUploadedAt: "uploaded", Status: StatusValidating,
		Manifest: Manifest{Connections: []ConnectionPin{{ConnectionID: "orders", RevisionID: "sha256:" + strings.Repeat("c", 64)}}},
	}}
	pins := &serviceTestPinValidator{err: pinErr}
	service := &Service{releases: repo, finalization: repo, validator: serviceTestArtifactValidator{state: servingstate.State{ID: "generation_1", ProjectID: "project_a", Environment: "dev", Digest: repo.current.ArtifactDigest}}, pins: pins}
	got, err := service.ValidateFinalization(t.Context(), "project_a", "release_1")
	require.ErrorIs(t, err, pinErr)
	require.Equal(t, StatusFailed, got.Status)
	require.False(t, repo.completed)
	require.Equal(t, identity, pins.identity)
	require.Equal(t, map[projectgraph.ResourceID]string{"orders": "sha256:" + strings.Repeat("c", 64)}, pins.expected)
}

func TestValidateFinalizationReplaysReadyRelease(t *testing.T) {
	repo := &serviceTestReleaseRepository{current: Release{ID: "release_1", ServingIdentity: projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_1"}, Status: StatusReady}}
	service := &Service{releases: repo, finalization: repo}
	got, err := service.ValidateFinalization(t.Context(), "project_a", "release_1")
	require.NoError(t, err)
	require.Equal(t, StatusReady, got.Status)
	require.False(t, repo.completed)
}

func TestPublishCandidatePromotesExactRetainedProvenanceWithoutRebuilding(t *testing.T) {
	provenance := candidateServiceTestProvenance(t)
	repo := &serviceTestReleaseRepository{}
	service := &Service{releases: repo, finalization: repo, validator: serviceTestArtifactValidator{state: servingstate.State{ID: "generation_candidate", ProjectID: "project_a", Environment: "dev", Digest: provenance.Artifact.ContentDigest}}, pins: &serviceTestPinValidator{}, candidateProvenance: serviceTestCandidateProvenanceRepository{provenance: provenance}}
	published, err := service.PublishCandidate(t.Context(), PublishCandidateInput{Scope: projectgraph.CandidateScope{ProjectID: "project_a", Environment: "dev", BaseGenerationID: "generation_0"}, CandidateID: provenance.Candidate.ID, CandidateRevision: provenance.Candidate.Revision, ProvenanceDigest: provenance.Digest, TargetID: provenance.Plan.TargetID, IdempotencyKey: "publish_1", CreatedBy: provenance.Candidate.OwnerID})
	require.NoError(t, err)
	require.Equal(t, StatusReady, published.Status)
	require.NotNil(t, published.Provenance)
	require.Equal(t, provenance.Digest, published.Provenance.Digest)
	require.Equal(t, provenance.Artifact.ContentDigest, repo.created.ArtifactDigest)
}

func TestPublishCandidateRejectsClientOrTargetDrift(t *testing.T) {
	provenance := candidateServiceTestProvenance(t)
	service := &Service{candidateProvenance: serviceTestCandidateProvenanceRepository{provenance: provenance}}
	base := PublishCandidateInput{Scope: projectgraph.CandidateScope{ProjectID: "project_a", Environment: "dev", BaseGenerationID: "generation_0"}, CandidateID: provenance.Candidate.ID, CandidateRevision: provenance.Candidate.Revision, ProvenanceDigest: provenance.Digest, TargetID: provenance.Plan.TargetID, IdempotencyKey: "publish_1", CreatedBy: provenance.Candidate.OwnerID}
	for name, mutate := range map[string]func(*PublishCandidateInput){
		"candidate revision": func(input *PublishCandidateInput) { input.CandidateRevision++ },
		"provenance digest":  func(input *PublishCandidateInput) { input.ProvenanceDigest = "sha256:" + strings.Repeat("f", 64) },
		"target":             func(input *PublishCandidateInput) { input.TargetID = "target_other" },
		"environment":        func(input *PublishCandidateInput) { input.Scope.Environment = "prod" },
		"owner":              func(input *PublishCandidateInput) { input.CreatedBy = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			_, err := service.PublishCandidate(t.Context(), input)
			require.ErrorIs(t, err, ErrConflict)
		})
	}
}

type serviceTestReleaseRepository struct {
	current   Release
	created   CreateInput
	completed bool
	recorded  bool
}

func (r *serviceTestReleaseRepository) Create(_ context.Context, input CreateInput) (Release, error) {
	r.created = input
	if r.current.ID != "" {
		return r.current, nil
	}
	r.current = Release{ID: input.ID, ServingIdentity: input.ServingIdentity, ProjectDigest: input.ProjectDigest, ArtifactDigest: input.ArtifactDigest, RequestDigest: input.RequestDigest, IdempotencyKey: input.IdempotencyKey, Status: StatusDraft, CreatedBy: input.CreatedBy, Manifest: Manifest{Connections: append([]ConnectionPin(nil), input.Connections...)}, Provenance: input.Provenance}
	return r.current, nil
}
func (r *serviceTestReleaseRepository) Get(_ context.Context, projectID projectgraph.ResourceID, releaseID string) (Release, error) {
	if r.current.ID != releaseID || r.current.ServingIdentity.ProjectID != projectID {
		return Release{}, ErrNotFound
	}
	return r.current, nil
}
func (r *serviceTestReleaseRepository) List(context.Context, projectgraph.ResourceID) ([]Release, error) {
	return []Release{r.current}, nil
}
func (r *serviceTestReleaseRepository) RecordArtifact(_ context.Context, artifact Artifact) error {
	r.recorded = true
	r.current.ArtifactUploadedAt = "2026-07-30T00:00:00Z"
	r.current.ActualDigest = artifact.ActualDigest
	r.current.ArtifactSizeBytes = artifact.SizeBytes
	return nil
}
func (r *serviceTestReleaseRepository) BeginFinalization(context.Context, string, string, jobs.WorkflowIntent) (Release, error) {
	if r.current.Status == StatusDraft {
		r.current.Status = StatusValidating
	}
	return r.current, nil
}
func (r *serviceTestReleaseRepository) CompleteFinalization(context.Context, string, string, string) (Release, error) {
	r.completed = true
	r.current.Status = StatusReady
	return r.current, nil
}
func (r *serviceTestReleaseRepository) FailFinalization(_ context.Context, _, _ string, cause error) (Release, error) {
	r.current.Status = StatusFailed
	r.current.Error = cause.Error()
	return r.current, nil
}

type serviceTestArtifactValidator struct{ state servingstate.State }

func (v serviceTestArtifactValidator) Validate(context.Context, servingstate.ID) (servingstate.State, error) {
	return v.state, nil
}

type serviceTestPinValidator struct {
	identity projectgraph.ServingIdentity
	expected map[projectgraph.ResourceID]string
	err      error
}

func (v *serviceTestPinValidator) ValidateServingStatePins(_ context.Context, identity projectgraph.ServingIdentity, expected map[projectgraph.ResourceID]string) error {
	v.identity = identity
	v.expected = make(map[projectgraph.ResourceID]string, len(expected))
	for key, value := range expected {
		v.expected[key] = value
	}
	return v.err
}

type serviceTestArtifactStore struct {
	saved     string
	saveCalls int
}

func (s *serviceTestArtifactStore) SaveUpload(_ context.Context, _ servingstate.ID, source io.Reader) (int64, error) {
	s.saveCalls++
	content, err := io.ReadAll(source)
	s.saved = string(content)
	return int64(len(content)), err
}

type serviceTestCandidateProvenanceRepository struct{ provenance Provenance }

func (repository serviceTestCandidateProvenanceRepository) RetainCandidateProvenance(context.Context, projectgraph.ResourceID, Provenance) (Provenance, error) {
	return repository.provenance, nil
}
func (repository serviceTestCandidateProvenanceRepository) CandidateProvenance(_ context.Context, _ projectgraph.ResourceID, candidateID string, candidateRevision int64) (Provenance, error) {
	if candidateID != repository.provenance.Candidate.ID || candidateRevision != repository.provenance.Candidate.Revision {
		return Provenance{}, ErrNotFound
	}
	return repository.provenance, nil
}

func wireDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
}

func candidateServiceTestProvenance(t *testing.T) Provenance {
	t.Helper()
	identity := projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_candidate"}
	provenance, err := NewProvenance(ProvenanceInput{
		Artifact:  ProjectArtifactProvenance{SourceDigest: "sha256:" + strings.Repeat("1", 64), ProjectDigest: "sha256:" + strings.Repeat("2", 64), ContentDigest: "sha256:" + strings.Repeat("3", 64), CompilerVersion: "leapview:test", SchemaVersion: 3},
		Candidate: CandidateProvenance{ID: "candidate_1", Revision: 4, OwnerID: "publisher"},
		Plan:      GenerationPlanProvenance{Identity: identity, BaseIdentity: &projectgraph.ServingIdentity{ProjectID: "project_a", Environment: "dev", GenerationID: "generation_0"}, TargetID: "target_dev", RuntimeVersion: "runtime:test", PolicyDigest: "sha256:" + strings.Repeat("4", 64), DataRevision: "snapshot:17", DataMode: GenerationDataReuseSnapshot},
	})
	require.NoError(t, err)
	return provenance
}

var _ Repository = (*serviceTestReleaseRepository)(nil)
var _ FinalizationUnitOfWork = (*serviceTestReleaseRepository)(nil)
var _ CandidateProvenanceRepository = serviceTestCandidateProvenanceRepository{}
var _ ArtifactValidator = serviceTestArtifactValidator{}
var _ ArtifactStore = (*serviceTestArtifactStore)(nil)
