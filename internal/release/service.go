package release

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	ocidigest "github.com/opencontainers/go-digest"
)

type Repository interface {
	Create(context.Context, CreateInput) (Release, error)
	Get(context.Context, string, string) (Release, error)
	List(context.Context, string) ([]Release, error)
	RecordArtifact(context.Context, Artifact) error
}

type ServingStateProvenanceRepository interface {
	ProvenanceForServingState(context.Context, string) (Provenance, error)
}
type FinalizationUnitOfWork interface {
	BeginFinalization(context.Context, string, string, jobs.WorkflowIntent) (Release, error)
	CompleteFinalization(context.Context, string, string, string) (Release, error)
	FailFinalization(context.Context, string, string, error) (Release, error)
}
type ArtifactStore interface {
	SaveUpload(context.Context, servingstate.ID, io.Reader) (int64, error)
}
type ArtifactValidator interface {
	Validate(context.Context, servingstate.ID) (servingstate.State, error)
}
type PinValidator interface {
	ValidateServingStatePins(context.Context, servingstate.ID, string, map[string]string) error
}
type CandidateProvenanceRepository interface {
	RetainCandidateProvenance(context.Context, string, Provenance) (Provenance, error)
	CandidateProvenance(context.Context, string, string, int64) (Provenance, error)
}

type Service struct {
	releases            Repository
	finalization        FinalizationUnitOfWork
	artifacts           ArtifactStore
	validator           ArtifactValidator
	pins                PinValidator
	candidateProvenance CandidateProvenanceRepository
	environment         servingstate.Environment
}

type ServiceOptions struct {
	Releases            Repository
	Finalization        FinalizationUnitOfWork
	Artifacts           ArtifactStore
	Validator           ArtifactValidator
	Pins                PinValidator
	CandidateProvenance CandidateProvenanceRepository
	Environment         servingstate.Environment
}

func NewService(options ServiceOptions) (*Service, error) {
	if options.Releases == nil || options.Finalization == nil || options.Artifacts == nil || options.Validator == nil {
		return nil, fmt.Errorf("release repository, finalization unit of work, artifact store, and validator are required")
	}
	return &Service{releases: options.Releases, finalization: options.Finalization, artifacts: options.Artifacts, validator: options.Validator, pins: options.Pins, candidateProvenance: options.CandidateProvenance, environment: servingstate.NormalizeEnvironment(options.Environment)}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Release, error) {
	input.ProjectID, input.Environment, input.GenerationID, input.ProjectDigest, input.ArtifactDigest = strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.Environment), strings.TrimSpace(input.GenerationID), strings.TrimSpace(input.ProjectDigest), strings.TrimSpace(input.ArtifactDigest)
	input.Environment = string(servingstate.NormalizeEnvironment(servingstate.Environment(input.Environment)))
	input.IdempotencyKey, input.CreatedBy = strings.TrimSpace(input.IdempotencyKey), strings.TrimSpace(input.CreatedBy)
	if input.ProjectID == "" || input.GenerationID == "" || input.Environment == "" || input.ProjectDigest == "" || input.ArtifactDigest == "" || input.IdempotencyKey == "" || input.CreatedBy == "" {
		return Release{}, ErrInvalid
	}
	if _, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(input.ProjectID), input.Environment, input.GenerationID); err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if digest.ValidateSHA256Identity(input.ProjectDigest) != nil || digest.ValidateSHA256Identity(input.ArtifactDigest) != nil {
		return Release{}, ErrInvalid
	}
	for i := range input.Connections {
		input.Connections[i].ConnectionID, input.Connections[i].RevisionID = strings.TrimSpace(input.Connections[i].ConnectionID), strings.TrimSpace(input.Connections[i].RevisionID)
		if input.Connections[i].ConnectionID == "" || input.Connections[i].RevisionID == "" {
			return Release{}, ErrInvalid
		}
	}
	input.ID = stableID("rel", input.ProjectID, input.IdempotencyKey)
	encoded, err := json.Marshal(struct {
		ProjectID, Environment, GenerationID, ProjectDigest, ArtifactDigest string
		Connections                                                         []ConnectionPin
	}{input.ProjectID, input.Environment, input.GenerationID, input.ProjectDigest, input.ArtifactDigest, input.Connections})
	if err != nil {
		return Release{}, err
	}
	input.RequestDigest = digest.SHA256Identity(encoded)
	return s.releases.Create(ctx, input)
}

func (s *Service) Get(ctx context.Context, projectID, releaseID string) (Release, error) {
	return s.releases.Get(ctx, strings.TrimSpace(projectID), strings.TrimSpace(releaseID))
}
func (s *Service) List(ctx context.Context, projectID string) ([]Release, error) {
	return s.releases.List(ctx, strings.TrimSpace(projectID))
}

// UploadArtifact streams the immutable generation artifact and records the
// resulting size only after the canonical digest verifier succeeds.
func (s *Service) UploadArtifact(ctx context.Context, projectID, releaseID, contentDigest string, source io.Reader) (Artifact, error) {
	current, err := s.releases.Get(ctx, projectID, releaseID)
	if err != nil {
		return Artifact{}, err
	}
	if current.Status != StatusDraft {
		return Artifact{}, ErrImmutable
	}
	expected := ocidigest.NewDigestFromEncoded(ocidigest.SHA256, strings.TrimPrefix(current.ArtifactDigest, "sha256:"))
	if err := expected.Validate(); err != nil {
		return Artifact{}, ErrInvalid
	}
	b, err := hex.DecodeString(expected.Encoded())
	if err != nil || strings.TrimSpace(contentDigest) != "sha-256=:"+base64.StdEncoding.EncodeToString(b)+":" {
		return Artifact{}, ErrDigest
	}
	if current.ArtifactUploadedAt != "" {
		return Artifact{ReleaseID: current.ID, ProjectID: current.ProjectID, Environment: current.Environment, GenerationID: current.GenerationID, ExpectedDigest: current.ArtifactDigest, ActualDigest: current.ActualDigest, SizeBytes: current.ArtifactSizeBytes, UploadedAt: current.ArtifactUploadedAt}, nil
	}
	verifier := expected.Verifier()
	size, err := s.artifacts.SaveUpload(ctx, servingstate.ID(current.GenerationID), io.TeeReader(source, verifier))
	if err != nil {
		return Artifact{}, err
	}
	if !verifier.Verified() {
		return Artifact{}, ErrDigest
	}
	item := Artifact{ReleaseID: current.ID, ProjectID: current.ProjectID, Environment: current.Environment, GenerationID: current.GenerationID, ExpectedDigest: current.ArtifactDigest, SizeBytes: size}
	if err := s.releases.RecordArtifact(ctx, item); err != nil {
		return Artifact{}, err
	}
	return item, nil
}

func (s *Service) Finalize(ctx context.Context, projectID, releaseID string) (Release, error) {
	if _, err := s.BeginFinalization(ctx, projectID, releaseID, jobs.WorkflowIntent{}); err != nil {
		return Release{}, err
	}
	return s.ValidateFinalization(ctx, projectID, releaseID)
}
func (s *Service) BeginFinalization(ctx context.Context, projectID, releaseID string, workflow jobs.WorkflowIntent) (Release, error) {
	return s.finalization.BeginFinalization(ctx, projectID, releaseID, workflow)
}

func (s *Service) ValidateFinalization(ctx context.Context, projectID, releaseID string) (Release, error) {
	current, err := s.releases.Get(ctx, projectID, releaseID)
	if err != nil {
		return Release{}, err
	}
	if current.Status == StatusReady {
		return current, nil
	}
	if current.Status == StatusFailed {
		return Release{}, fmt.Errorf("%w: %s", ErrConflict, current.Error)
	}
	if current.Status != StatusValidating {
		return Release{}, ErrConflict
	}
	state, err := s.validator.Validate(ctx, servingstate.ID(current.GenerationID))
	if err != nil {
		return s.failFinalization(ctx, current, err)
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(current.ProjectID), current.Environment, current.GenerationID)
	if err != nil {
		return s.failFinalization(ctx, current, err)
	}
	if state.ProjectID != identity.ProjectID || servingstate.NormalizeEnvironment(state.Environment) != servingstate.Environment(identity.Environment) || state.Digest != current.ArtifactDigest {
		return s.failFinalization(ctx, current, fmt.Errorf("%w: generation identity or artifact digest mismatch", ErrConflict))
	}
	if s.pins != nil {
		pins := make(map[string]string, len(current.Manifest.Connections))
		for _, pin := range current.Manifest.Connections {
			pins[pin.ConnectionID] = pin.RevisionID
		}
		if err := s.pins.ValidateServingStatePins(ctx, servingstate.ID(current.GenerationID), current.ProjectID, pins); err != nil {
			return s.failFinalization(ctx, current, err)
		}
	}
	return s.finalization.CompleteFinalization(ctx, projectID, releaseID, state.Digest)
}

func (s *Service) failFinalization(ctx context.Context, current Release, cause error) (Release, error) {
	failed, err := s.finalization.FailFinalization(ctx, current.ProjectID, current.ID, cause)
	if err != nil {
		return Release{}, errorsJoin(cause, err)
	}
	return failed, cause
}

func (s *Service) RetainCandidateProvenance(ctx context.Context, projectID string, provenance Provenance) (Provenance, error) {
	if s == nil || s.candidateProvenance == nil {
		return Provenance{}, ErrCandidateArtifactUnavailable
	}
	if err := provenance.Validate(); err != nil {
		return Provenance{}, err
	}
	return s.candidateProvenance.RetainCandidateProvenance(ctx, strings.TrimSpace(projectID), provenance)
}
func (s *Service) CandidateProvenance(ctx context.Context, projectID, candidateID string, revision int64) (Provenance, error) {
	if s == nil || s.candidateProvenance == nil {
		return Provenance{}, ErrCandidateArtifactUnavailable
	}
	return s.candidateProvenance.CandidateProvenance(ctx, strings.TrimSpace(projectID), strings.TrimSpace(candidateID), revision)
}

func stableID(prefix string, values ...string) string {
	h := digest.SHA256Identity([]byte(strings.Join(values, "\x00")))
	return prefix + "_" + strings.TrimPrefix(h, "sha256:")[:24]
}
func errorsJoin(primary, secondary error) error {
	return fmt.Errorf("%v; persist failure: %w", primary, secondary)
}
