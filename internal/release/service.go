package release

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	ocidigest "github.com/opencontainers/go-digest"
)

type Repository interface {
	Create(context.Context, CreateInput) (Release, error)
	Get(context.Context, projectgraph.ResourceID, string) (Release, error)
	List(context.Context, projectgraph.ResourceID) ([]Release, error)
	RecordArtifact(context.Context, Artifact) error
}

type ServingStateProvenanceRepository interface {
	ProvenanceForServingState(context.Context, projectgraph.ServingIdentity) (Provenance, error)
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
	ValidateServingStatePins(context.Context, projectgraph.ServingIdentity, map[projectgraph.ResourceID]string) error
}
type CandidateProvenanceRepository interface {
	RetainCandidateProvenance(context.Context, projectgraph.ResourceID, Provenance) (Provenance, error)
	CandidateProvenance(context.Context, projectgraph.ResourceID, string, int64) (Provenance, error)
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
	if options.Environment == "" || string(options.Environment) != strings.TrimSpace(string(options.Environment)) {
		return nil, fmt.Errorf("release environment must be canonical")
	}
	if err := servingstate.ValidateEnvironment(options.Environment); err != nil {
		return nil, err
	}
	return &Service{releases: options.Releases, finalization: options.Finalization, artifacts: options.Artifacts, validator: options.Validator, pins: options.Pins, candidateProvenance: options.CandidateProvenance, environment: options.Environment}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Release, error) {
	identity, err := input.Identity()
	if input.ProjectDigest != strings.TrimSpace(input.ProjectDigest) || input.ArtifactDigest != strings.TrimSpace(input.ArtifactDigest) || input.IdempotencyKey != strings.TrimSpace(input.IdempotencyKey) || input.CreatedBy != strings.TrimSpace(input.CreatedBy) || input.ProjectDigest == "" || input.ArtifactDigest == "" || input.IdempotencyKey == "" || input.CreatedBy == "" {
		return Release{}, ErrInvalid
	}
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if digest.ValidateSHA256Identity(input.ProjectDigest) != nil || digest.ValidateSHA256Identity(input.ArtifactDigest) != nil {
		return Release{}, ErrInvalid
	}
	input.Connections = append([]ConnectionPin(nil), input.Connections...)
	for i := range input.Connections {
		rawConnection, rawRevision := input.Connections[i].ConnectionID, input.Connections[i].RevisionID
		input.Connections[i].ConnectionID, input.Connections[i].RevisionID = strings.TrimSpace(rawConnection), strings.TrimSpace(rawRevision)
		if rawConnection != input.Connections[i].ConnectionID || rawRevision != input.Connections[i].RevisionID {
			return Release{}, ErrInvalid
		}
		if input.Connections[i].ConnectionID == "" || input.Connections[i].RevisionID == "" {
			return Release{}, ErrInvalid
		}
	}
	sort.Slice(input.Connections, func(i, j int) bool { return input.Connections[i].ConnectionID < input.Connections[j].ConnectionID })
	for i := 1; i < len(input.Connections); i++ {
		if input.Connections[i-1].ConnectionID == input.Connections[i].ConnectionID {
			return Release{}, ErrInvalid
		}
	}
	if input.Provenance == nil {
		return Release{}, fmt.Errorf("%w: provenance is required", ErrInvalid)
	}
	provenance, err := cloneProvenance(*input.Provenance)
	if err != nil {
		return Release{}, fmt.Errorf("%w: provenance clone: %v", ErrInvalid, err)
	}
	input.Provenance = &provenance
	if err := input.Provenance.Validate(); err != nil {
		return Release{}, fmt.Errorf("%w: provenance: %v", ErrInvalid, err)
	}
	if input.Provenance.Artifact.ProjectDigest != input.ProjectDigest || input.Provenance.Artifact.ContentDigest != input.ArtifactDigest || input.Provenance.Plan.Identity != identity {
		return Release{}, fmt.Errorf("%w: provenance does not match project generation", ErrInvalid)
	}
	input.ID = stableID("rel", identity.ProjectID.String(), input.IdempotencyKey)
	encoded, err := json.Marshal(struct {
		ProjectID, Environment, GenerationID, ProjectDigest, ArtifactDigest string
		Connections                                                         []ConnectionPin
	}{identity.ProjectID.String(), identity.Environment, identity.GenerationID, input.ProjectDigest, input.ArtifactDigest, input.Connections})
	if err != nil {
		return Release{}, err
	}
	expectedRequestDigest := ocidigest.FromBytes(encoded).String()
	if input.RequestDigest != "" && (digest.ValidateSHA256Identity(input.RequestDigest) != nil || input.RequestDigest != expectedRequestDigest) {
		return Release{}, fmt.Errorf("%w: request digest mismatch", ErrInvalid)
	}
	input.RequestDigest = expectedRequestDigest
	return s.releases.Create(ctx, input)
}

func (s *Service) Get(ctx context.Context, projectID projectgraph.ResourceID, releaseID string) (Release, error) {
	if projectID.Validate() != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return Release{}, ErrInvalid
	}
	return s.releases.Get(ctx, projectID, releaseID)
}
func (s *Service) List(ctx context.Context, projectID projectgraph.ResourceID) ([]Release, error) {
	if projectID.Validate() != nil {
		return nil, ErrInvalid
	}
	return s.releases.List(ctx, projectID)
}

// UploadArtifact streams the immutable generation artifact and records the
// resulting size only after the canonical digest verifier succeeds.
func (s *Service) UploadArtifact(ctx context.Context, projectID, releaseID, contentDigest string, source io.Reader) (Artifact, error) {
	project, err := canonicalProjectID(projectID)
	if err != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return Artifact{}, ErrInvalid
	}
	current, err := s.releases.Get(ctx, project, releaseID)
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
		return Artifact{ReleaseID: current.ID, ServingIdentity: current.ServingIdentity, ExpectedDigest: current.ArtifactDigest, ActualDigest: current.ActualDigest, SizeBytes: current.ArtifactSizeBytes, UploadedAt: current.ArtifactUploadedAt}, nil
	}
	verifier := expected.Verifier()
	size, err := s.artifacts.SaveUpload(ctx, servingstate.ID(current.ServingIdentity.GenerationID), io.TeeReader(source, verifier))
	if err != nil {
		return Artifact{}, err
	}
	if !verifier.Verified() {
		return Artifact{}, ErrDigest
	}
	item := Artifact{ReleaseID: current.ID, ServingIdentity: current.ServingIdentity, ExpectedDigest: current.ArtifactDigest, ActualDigest: current.ArtifactDigest, SizeBytes: size}
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
	project, err := canonicalProjectID(projectID)
	if err != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return Release{}, ErrInvalid
	}
	current, err := s.releases.Get(ctx, project, releaseID)
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
	if current.ArtifactUploadedAt == "" || current.ActualDigest == "" || current.ActualDigest != current.ArtifactDigest {
		return s.failFinalization(ctx, current, ErrIncomplete)
	}
	state, err := s.validator.Validate(ctx, servingstate.ID(current.ServingIdentity.GenerationID))
	if err != nil {
		return s.failFinalization(ctx, current, err)
	}
	identity := current.ServingIdentity
	if state.ProjectID != identity.ProjectID || state.Environment != servingstate.Environment(identity.Environment) || state.Digest != current.ArtifactDigest {
		return s.failFinalization(ctx, current, fmt.Errorf("%w: generation identity or artifact digest mismatch", ErrConflict))
	}
	if len(current.Manifest.Connections) > 0 && s.pins == nil {
		return s.failFinalization(ctx, current, fmt.Errorf("%w: managed-data pin validation is unavailable", ErrConflict))
	}
	if s.pins != nil {
		pins := make(map[projectgraph.ResourceID]string, len(current.Manifest.Connections))
		for _, pin := range current.Manifest.Connections {
			connectionID, parseErr := projectgraph.NewResourceID(pin.ConnectionID)
			if parseErr != nil {
				return s.failFinalization(ctx, current, fmt.Errorf("%w: invalid managed-data connection identity", ErrConflict))
			}
			pins[connectionID] = pin.RevisionID
		}
		if err := s.pins.ValidateServingStatePins(ctx, identity, pins); err != nil {
			return s.failFinalization(ctx, current, err)
		}
	}
	return s.finalization.CompleteFinalization(ctx, projectID, releaseID, state.Digest)
}

func (s *Service) failFinalization(ctx context.Context, current Release, cause error) (Release, error) {
	failed, err := s.finalization.FailFinalization(ctx, current.ServingIdentity.ProjectID.String(), current.ID, cause)
	if err != nil {
		return Release{}, errorsJoin(cause, err)
	}
	return failed, cause
}

func (s *Service) RetainCandidateProvenance(ctx context.Context, projectID projectgraph.ResourceID, provenance Provenance) (Provenance, error) {
	if s == nil || s.candidateProvenance == nil {
		return Provenance{}, ErrCandidateArtifactUnavailable
	}
	if err := provenance.Validate(); err != nil {
		return Provenance{}, err
	}
	if projectID.Validate() != nil {
		return Provenance{}, ErrInvalid
	}
	return s.candidateProvenance.RetainCandidateProvenance(ctx, projectID, provenance)
}
func (s *Service) CandidateProvenance(ctx context.Context, projectID projectgraph.ResourceID, candidateID string, revision int64) (Provenance, error) {
	if s == nil || s.candidateProvenance == nil {
		return Provenance{}, ErrCandidateArtifactUnavailable
	}
	if projectID.Validate() != nil || candidateID == "" || candidateID != strings.TrimSpace(candidateID) {
		return Provenance{}, ErrInvalid
	}
	return s.candidateProvenance.CandidateProvenance(ctx, projectID, candidateID, revision)
}

func canonicalProjectID(value string) (projectgraph.ResourceID, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", ErrInvalid
	}
	return projectgraph.NewResourceID(value)
}

func stableID(prefix string, values ...string) string {
	h := ocidigest.FromBytes([]byte(strings.Join(values, "\x00"))).String()
	return prefix + "_" + strings.TrimPrefix(h, "sha256:")[:24]
}

func cloneProvenance(value Provenance) (Provenance, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Provenance{}, err
	}
	var clone Provenance
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return Provenance{}, err
	}
	return clone, nil
}
func errorsJoin(primary, secondary error) error {
	return fmt.Errorf("%v; persist failure: %w", primary, secondary)
}
