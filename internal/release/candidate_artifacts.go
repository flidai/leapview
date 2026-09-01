package release

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/pkg/jobs"
)

var (
	ErrCandidateArtifactInvalid     = errors.New("candidate artifact invalid")
	ErrCandidateArtifactUnavailable = errors.New("candidate artifact preparation unavailable")
)

type CandidateConnectionRequirement struct {
	ConnectionID  projectgraph.ResourceID
	ConnectorKind string
	Access        semanticmodel.ConnectionAccess
}

type CandidateAuthoredConnection struct {
	ConnectionID  projectgraph.ResourceID
	ConnectorKind string
	Access        semanticmodel.ConnectionAccess
}

// NativeArtifactObjectEvidence is the value-only immutable object metadata
// retained for a native serving artifact. Locator is the provider object key
// (and the serving-admission locator); it is never a host filesystem path.
// Created-at and other provider/runtime handles intentionally do not cross
// the release boundary.
type NativeArtifactObjectEvidence struct {
	Locator               string
	StorageSecurityDomain string
	ContentType           string
	MetadataDigest        string
	SizeBytes             int64
}

type CandidateRestriction struct {
	ID             string
	ObjectID       projectgraph.ResourceID
	ObjectKind     projectgraph.Kind
	Subject        *access.SubjectRef
	PolicyType     string
	ExpressionJSON string
}

// CandidateGenerationArtifact contains the one project-generation artifact
// prepared for a candidate. A candidate never carries a partial target
// collection: the serving identity and generation artifact are the unit of
// preparation and publication.
type CandidateGenerationArtifact struct {
	Identity projectgraph.ServingIdentity
	// ServingArtifactID is the immutable artifact row identity. It is
	// intentionally distinct from Identity.GenerationID/ServingStateID.
	ServingArtifactID string
	ArtifactDigest    string
	// BundleManifestJSON is the canonical project-bundle container manifest
	// retained with the immutable serving artifact. It is populated by native
	// materialization/hydration from the bundle itself so serving admission can
	// persist the exact manifest without re-deriving it from compiler evidence.
	BundleManifestJSON string
	// NativeArtifact is populated only by native materialization/hydration from
	// the exact immutable object metadata returned by put/open. Legacy
	// filesystem artifacts leave it zero-valued.
	NativeArtifact NativeArtifactObjectEvidence
	// These canonical policy documents are snapshots from the immutable
	// compiled manifest. Native serving admission persists them verbatim;
	// legacy artifact paths leave them zero-valued.
	AccessPolicyJSON          string
	DashboardPublicationsJSON string
	DashboardAppearancesJSON  string
	DataRevision              string
	DataMode                  GenerationDataMode
	// Deterministic is an explicit compiler/runtime declaration. Zero means
	// unknown and therefore fail-closed for physical reuse.
	Deterministic       bool
	EquivalenceToken    string
	ManagedDataPins     []ManagedDataPin
	Connections         []CandidateConnectionRequirement
	AuthoredConnections []CandidateAuthoredConnection
	Restrictions        []CandidateRestriction
	// BaseGateEvidence is the sealed base's source/check evidence. Reuse paths
	// may use its observed schema/timestamps only after current identity checks
	// and must still re-evaluate freshness at candidate evaluation time.
	BaseGateEvidence *GateEvidence
}

type CandidateArtifactRequest struct {
	CandidateID string
	// GenerationID is the caller-owned serving-generation identity for native
	// materialization/hydration. Read-only inspection may omit it; native
	// materialization and hydration require a canonical UUIDv7 value.
	GenerationID   string
	Scope          projectgraph.CandidateScope
	OwnerID        string
	ArtifactDigest string
	Source         project.CandidateSourceSnapshot
}

// CandidateArtifactIdentity is the immutable serving artifact identity
// produced while preparing one candidate. It lives with the release artifact
// contract so the release module does not depend on deployment persistence.
type CandidateArtifactIdentity struct {
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
}

// CandidateArtifactRecoveryRequest identifies one immutable serving artifact
// that is being reloaded after a native physical-build outcome was lost. It
// carries no source snapshot or target/base selector: recovery is value-only
// and must use the exact serving identity and content-addressed artifact
// identity supplied by the caller.
type CandidateArtifactRecoveryRequest struct {
	CandidateID     string
	ServingIdentity projectgraph.ServingIdentity
	SourceDigest    string
	// ManagedDataPins is the exact immutable revision ledger selected by the
	// durable delivery plan. Managed activations are not embedded in serving
	// bundles, so native recovery must carry these pins explicitly rather than
	// dropping managed-data bindings or consulting mutable source state.
	ManagedDataPins []ManagedDataPin
	Artifact        CandidateArtifactIdentity
}

type CandidateArtifactSet struct {
	Artifact ProjectArtifactProvenance
	// Extensions is target-side, non-secret evidence for exact extension
	// artifacts admitted during bounded candidate preparation.
	Extensions               []extension.Evidence
	AuthorizationFingerprint string
	Generation               CandidateGenerationArtifact
	// Compiler is the exact immutable compiler evidence used to produce the
	// serving artifact. Keeping the graph, manifest, and plan alongside the
	// artifact prevents production delivery from reloading or recompiling a
	// moving worktree while constructing a private candidate catalog.
	Compiler CandidateCompilerEvidence
}

// CandidateSourcesDataRevision returns the canonical provenance identity for
// a candidate that refreshes source data. The source artifact digest and the
// complete managed-data pin set are the only inputs; pins are sorted before
// hashing so map/loader order can never change the resulting identity.
func CandidateSourcesDataRevision(artifactDigest string, pins []ManagedDataPin) (string, error) {
	if artifactDigest != strings.TrimSpace(artifactDigest) || platformdigest.ValidateSHA256Identity(artifactDigest) != nil {
		return "", fmt.Errorf("candidate source artifact digest is not a canonical SHA-256 identity")
	}
	canonicalPins := append([]ManagedDataPin(nil), pins...)
	sort.Slice(canonicalPins, func(i, j int) bool {
		if canonicalPins[i].ConnectionID != canonicalPins[j].ConnectionID {
			return canonicalPins[i].ConnectionID < canonicalPins[j].ConnectionID
		}
		return canonicalPins[i].RevisionID < canonicalPins[j].RevisionID
	})
	for i, pin := range canonicalPins {
		if pin.ConnectionID == "" || pin.ConnectionID != strings.TrimSpace(pin.ConnectionID) || pin.RevisionID == "" || pin.RevisionID != strings.TrimSpace(pin.RevisionID) {
			return "", fmt.Errorf("candidate managed-data pin is incomplete")
		}
		if i > 0 && canonicalPins[i-1].ConnectionID == pin.ConnectionID {
			return "", fmt.Errorf("candidate managed-data pins contain duplicate connection %q", pin.ConnectionID)
		}
	}
	payload := struct {
		ArtifactDigest  string           `json:"artifactDigest"`
		ManagedDataPins []ManagedDataPin `json:"managedDataPins"`
	}{ArtifactDigest: artifactDigest, ManagedDataPins: canonicalPins}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode candidate source data revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sources:sha256:" + fmt.Sprintf("%x", digest[:]), nil
}

type CandidateCompilerEvidence struct {
	Graph    projectgraph.ProjectGraph
	Manifest projectmanifest.Project
	Plan     projectcompiler.ProjectPlan
	// RelationExecution and BaseRelationExecution are per-materialization
	// identities. They let delivery retain unchanged sealed relation refs while
	// rebuilding only changed/removed relations from the same base catalog.
	RelationExecution     map[string]string
	BaseRelationExecution map[string]string
	// Artifact is the decoded portable artifact whose digest is bound by
	// Artifact.ProjectDigest. It is retained for model projections used by
	// candidate materialization.
	Artifact projectartifact.Project
}

type CandidateArtifactPreparer interface {
	PrepareCandidateArtifacts(context.Context, CandidateArtifactRequest) (CandidateArtifactSet, error)
	RetainCandidateProvenance(
		context.Context,
		projectgraph.ResourceID,
		Provenance,
	) (Provenance, error)
	CandidateProvenance(
		context.Context,
		projectgraph.ResourceID,
		string,
		int64,
	) (Provenance, error)
}

// CandidateArtifactRecovery is the value-only native recovery surface. It is
// intentionally separate from CandidateArtifactPreparer so legacy/source
// preparation implementations are not granted a recovery capability they
// cannot safely provide.
type CandidateArtifactRecovery interface {
	RecoverCandidateArtifacts(context.Context, CandidateArtifactRecoveryRequest) (CandidateArtifactSet, error)
}

type PublishCandidateInput struct {
	Scope             projectgraph.CandidateScope
	CandidateID       string
	CandidateRevision int64
	ProvenanceDigest  string
	TargetID          string
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
	if s == nil || input.Scope.Validate() != nil || input.CandidateID != strings.TrimSpace(input.CandidateID) || input.ProvenanceDigest != strings.TrimSpace(input.ProvenanceDigest) || input.TargetID != strings.TrimSpace(input.TargetID) || input.IdempotencyKey != strings.TrimSpace(input.IdempotencyKey) || input.CreatedBy != strings.TrimSpace(input.CreatedBy) || input.CandidateID == "" || input.CandidateRevision < 1 || input.ProvenanceDigest == "" || input.TargetID == "" || input.IdempotencyKey == "" || input.CreatedBy == "" {
		return Release{}, ErrInvalid
	}
	provenance, err := s.CandidateProvenance(
		ctx,
		input.Scope.ProjectID,
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
	if provenance.Digest != input.ProvenanceDigest || provenance.Candidate.ID != input.CandidateID || provenance.Candidate.Revision != input.CandidateRevision || provenance.Candidate.OwnerID != input.CreatedBy || provenance.Plan.TargetID != input.TargetID || provenance.Plan.Identity.ProjectID != input.Scope.ProjectID || provenance.Plan.Identity.Environment != input.Scope.Environment {
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
			input.Scope.ProjectID.String(),
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
	return s.ValidateFinalization(ctx, input.Scope.ProjectID.String(), created.ID)
}
