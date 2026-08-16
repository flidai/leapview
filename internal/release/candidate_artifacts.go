package release

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/project"
)

var (
	ErrCandidateArtifactInvalid     = errors.New("candidate artifact invalid")
	ErrCandidateArtifactUnavailable = errors.New("candidate artifact preparation unavailable")
)

type CandidateConnectionRequirement struct {
	LogicalConnectionID string
	ConnectorKind       string
}

type CandidateAuthoredConnection struct {
	LogicalConnectionID string
	ConnectorKind       string
}

type CandidateRestriction struct {
	ID             string
	WorkspaceID    string
	ObjectID       string
	PolicyType     string
	ExpressionJSON string
}

type CandidateArtifactWorkspace struct {
	WorkspaceID         string
	ServingStateID      string
	ArtifactDigest      string
	DataRevision        string
	DataMode            string
	ManagedDataPins     []ManagedDataPin
	Connections         []CandidateConnectionRequirement
	AuthoredConnections []CandidateAuthoredConnection
	Restrictions        []CandidateRestriction
}

type CandidateArtifactRequest struct {
	CandidateID    string
	ProjectID      string
	OwnerID        string
	Environment    string
	ArtifactDigest string
	Source         project.CandidateSourceSnapshot
}

type CandidateArtifactSet struct {
	Artifact                 ProjectArtifactProvenance
	AuthorizationFingerprint string
	Workspaces               []CandidateArtifactWorkspace
}

type CandidateArtifactPreparer interface {
	PrepareCandidateArtifacts(context.Context, CandidateArtifactRequest) (CandidateArtifactSet, error)
	RetainCandidateProvenance(
		context.Context,
		string,
		Provenance,
	) (Provenance, error)
	CandidateProvenance(
		context.Context,
		string,
		string,
		int64,
	) (Provenance, error)
}

type PublishCandidateInput struct {
	ProjectID         string
	CandidateID       string
	CandidateRevision int64
	ProvenanceDigest  string
	TargetID          string
	Environment       string
	IdempotencyKey    string
	CreatedBy         string
}

// PublishCandidate promotes the exact target-retained candidate artifact into
// a ready release. It reuses the candidate serving states and never recompiles
// client source or accepts client-supplied target evidence.
func (s *Service) PublishCandidate(
	ctx context.Context,
	input PublishCandidateInput,
) (Release, error) {
	if s == nil || input.ProjectID != strings.TrimSpace(input.ProjectID) || input.CandidateID != strings.TrimSpace(input.CandidateID) || input.ProvenanceDigest != strings.TrimSpace(input.ProvenanceDigest) || input.TargetID != strings.TrimSpace(input.TargetID) || input.Environment != strings.TrimSpace(input.Environment) || input.IdempotencyKey != strings.TrimSpace(input.IdempotencyKey) || input.CreatedBy != strings.TrimSpace(input.CreatedBy) || input.ProjectID == "" || input.CandidateID == "" || input.CandidateRevision < 1 || input.ProvenanceDigest == "" || input.TargetID == "" || input.Environment == "" || input.IdempotencyKey == "" || input.CreatedBy == "" {
		return Release{}, ErrInvalid
	}
	provenance, err := s.CandidateProvenance(
		ctx,
		input.ProjectID,
		input.CandidateID,
		input.CandidateRevision,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Release{}, fmt.Errorf(
				"%w: candidate publication evidence was not retained",
				ErrConflict,
			)
		}
		return Release{}, err
	}
	if provenance.Digest != input.ProvenanceDigest || provenance.Candidate.ID != input.CandidateID || provenance.Candidate.Revision != input.CandidateRevision || provenance.Candidate.OwnerID != input.CreatedBy || provenance.Plan.TargetID != input.TargetID || provenance.Plan.Identity.ProjectID.String() != input.ProjectID || provenance.Plan.Identity.Environment != input.Environment {
		return Release{}, fmt.Errorf(
			"%w: candidate publication evidence drifted",
			ErrConflict,
		)
	}
	pins := make(map[string]string)
	for _, pin := range provenance.Plan.ManagedDataPins {
		if current, exists := pins[pin.ConnectionID]; exists && current != pin.RevisionID {
			return Release{}, fmt.Errorf(
				"%w: candidate managed-data pins disagree",
				ErrConflict,
			)
		}
		pins[pin.ConnectionID] = pin.RevisionID
	}
	connectionIDs := make([]string, 0, len(pins))
	for connectionID := range pins {
		connectionIDs = append(connectionIDs, connectionID)
	}
	sort.Strings(connectionIDs)
	connections := make([]ConnectionPin, len(connectionIDs))
	for index, connectionID := range connectionIDs {
		connections[index] = ConnectionPin{
			ConnectionID: connectionID,
			RevisionID:   pins[connectionID],
		}
	}
	created, err := s.Create(ctx, CreateInput{
		ServingIdentity: provenance.Plan.Identity,
		ProjectDigest:   provenance.Artifact.ProjectDigest,
		ArtifactDigest:  provenance.Artifact.ContentDigest,
		IdempotencyKey:  input.IdempotencyKey,
		CreatedBy:       input.CreatedBy,
		Connections:     connections,
		Provenance:      &provenance,
	})
	if err != nil {
		return Release{}, err
	}
	if created.Provenance == nil ||
		created.Provenance.Digest != provenance.Digest {
		return Release{}, fmt.Errorf(
			"%w: published release provenance changed",
			ErrConflict,
		)
	}
	if created.Status == StatusReady {
		return created, nil
	}
	if created.Status == StatusFailed {
		return Release{}, fmt.Errorf("%w: %s", ErrConflict, created.Error)
	}
	if created.Status == StatusDraft {
		if err := s.releases.RecordArtifact(ctx, Artifact{ReleaseID: created.ID, ServingIdentity: created.ServingIdentity, ExpectedDigest: created.ArtifactDigest, ActualDigest: created.ArtifactDigest}); err != nil {
			return Release{}, err
		}
		created, err = s.BeginFinalization(
			ctx,
			input.ProjectID,
			created.ID,
			jobs.WorkflowIntent{},
		)
		if err != nil {
			return Release{}, err
		}
	}
	if created.Status != StatusValidating {
		return Release{}, fmt.Errorf(
			"%w: candidate release is %s",
			ErrConflict,
			created.Status,
		)
	}
	return s.ValidateFinalization(ctx, input.ProjectID, created.ID)
}
