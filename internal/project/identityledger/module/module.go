// Package module is the application-facing composition surface for the
// instance identity ledger. The ledger model and PostgreSQL adapter remain
// behind this capability boundary; application composition consumes only the
// contracts and constructors declared here.
package module

import (
	"context"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	ledger "github.com/flidai/leapview/internal/project/identityledger"
	ledgerpostgres "github.com/flidai/leapview/internal/project/identityledger/postgres"
	"github.com/jackc/pgx/v5"
)

// Contract aliases keep the application boundary independent of the ledger
// implementation package while preserving one canonical set of evidence
// types and validation semantics.
type Candidate = ledger.Candidate
type ContractPublication = ledger.ContractPublication
type Coordinator = ledger.Coordinator
type DurableReference = ledger.DurableReference
type Identity = ledger.Identity
type LifecycleEvidence = ledger.LifecycleEvidence
type PolicyAffectedResource = ledger.PolicyAffectedResource
type PolicyApprovalState = ledger.PolicyApprovalState
type PolicyBaselineKind = ledger.PolicyBaselineKind
type PolicyContext = ledger.PolicyContext
type PolicyDecision = ledger.PolicyDecision
type PolicyEvidence = ledger.PolicyEvidence
type PolicyPublicationIdentity = ledger.PolicyPublicationIdentity
type Outcome = ledger.Outcome
type OutcomeKind = ledger.OutcomeKind
type Plan = ledger.Plan
type Resource = ledger.Resource
type Restore = ledger.Restore
type Rollback = ledger.Rollback
type Transition = ledger.Transition
type TransitionOperation = ledger.TransitionOperation
type TransitionPhase = ledger.TransitionPhase

const (
	LifecycleActive     = ledger.LifecycleActive
	LifecycleTombstoned = ledger.LifecycleTombstoned

	PolicyApprovalRequired      = ledger.PolicyApprovalRequired
	PolicyApprovalNotRequired   = ledger.PolicyApprovalNotRequired
	PolicyBaselineGenesis       = ledger.PolicyBaselineGenesis
	PolicyBaselineExisting      = ledger.PolicyBaselineExisting
	PolicyEvidenceVersion       = ledger.PolicyEvidenceVersion
	PolicyAffectedResourceScope = ledger.PolicyAffectedResourceScope

	OperationPublish  = ledger.OperationPublish
	OperationRollback = ledger.OperationRollback
	OperationRestore  = ledger.OperationRestore

	PhasePrepared        = ledger.PhasePrepared
	PhaseIdentityPending = ledger.PhaseIdentityPending
	PhaseIdentityActive  = ledger.PhaseIdentityActive
	PhaseDeliveryActive  = ledger.PhaseDeliveryActive
	PhaseCompleted       = ledger.PhaseCompleted

	OutcomeCollision       = ledger.OutcomeCollision
	OutcomeRestored        = ledger.OutcomeRestored
	OutcomeRestoreRequired = ledger.OutcomeRestoreRequired

	ReferenceOwnerKindGrant                = ledger.ReferenceOwnerKindGrant
	ReferenceOwnerKindDashboardPublication = ledger.ReferenceOwnerKindDashboardPublication
)

var (
	ErrActivationConflict     = ledger.ErrActivationConflict
	ErrDuplicateAuthoredID    = ledger.ErrDuplicateAuthoredID
	ErrInvalidInput           = ledger.ErrInvalidInput
	ErrKindConflict           = ledger.ErrKindConflict
	ErrPhaseConflict          = ledger.ErrPhaseConflict
	ErrRestoreRequired        = ledger.ErrRestoreRequired
	ErrTransitionConflict     = ledger.ErrTransitionConflict
	ErrPolicyEvidenceInvalid  = ledger.ErrPolicyEvidenceInvalid
	ErrPolicyEvidenceConflict = ledger.ErrPolicyEvidenceConflict
)

// TransitionRepository is the narrow durable transition contract consumed by
// the lifecycle coordinator. Implementations own locking and persistence.
type TransitionRepository interface {
	PrepareTransition(context.Context, Transition) (Transition, error)
	LoadTransition(context.Context, string, string) (Transition, error)
	AdvanceTransition(context.Context, string, string, TransitionPhase, TransitionPhase, string) (Transition, error)
	Plan(context.Context, Candidate) (Plan, error)
	Activate(context.Context, Candidate) (Plan, error)
	RestoreAndActivate(context.Context, Restore) (Plan, error)
	Rollback(context.Context, Rollback) (Plan, error)
}

// PublishTransitionReader returns prepared or resuming publish evidence.
type PublishTransitionReader interface {
	BundlePublishTransition(context.Context, string, string) (Transition, error)
}

// PublishedTransitionReader returns completed publish evidence for rollback.
type PublishedTransitionReader interface {
	PublishedBundleTransition(context.Context, string, string) (Transition, error)
}

// ReferenceReconciler atomically validates the immutable control-plane
// reference bindings carried by transition evidence. Implementations must not
// infer control-plane deletion from an omitted binding; omitted rows remain
// durable and return only the supplied desired bindings after target lifecycle
// validation.
type ReferenceReconciler interface {
	ReconcileReferences(context.Context, string, []DurableReference) ([]DurableReference, error)
}

// InFlightTransitionReader is used by production readiness checks.
type InFlightTransitionReader interface {
	ListInFlightTransitions(context.Context, string) ([]Transition, error)
}

// LifecycleEvidenceReader is an optional capability for bounded cache
// admission. It is intentionally not part of Repository so existing
// composition fakes and non-cache lifecycle users do not acquire a second
// identity or publication authority.
type LifecycleEvidenceReader interface {
	ReadLifecycleEvidence(context.Context, string, projectgraph.ResourceID, projectgraph.Kind, string) (LifecycleEvidence, error)
}

// Repository is the complete PostgreSQL identity authority contract required
// by application composition.
type Repository interface {
	TransitionRepository
	PublishTransitionReader
	PublishedTransitionReader
	ReferenceReconciler
	InFlightTransitionReader
}

// ControlPool is the minimum PostgreSQL pool surface needed by the identity
// adapter. It keeps unit tests independent of a live control database.
type ControlPool interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Close()
}

// ControlOpener is injectable so composition tests can validate configuration
// and cleanup without opening a network connection.
type ControlOpener func(context.Context, platformpostgres.Config) (ControlPool, error)

// OpenControl opens the identity ledger's PostgreSQL control authority. Pool
// setup remains owned by the capability-neutral platform PostgreSQL boundary.
func OpenControl(ctx context.Context, config platformpostgres.Config) (ControlPool, error) {
	return platformpostgres.OpenControl(ctx, config)
}

// NewPostgresRepository constructs the durable identity authority adapter.
// PostgreSQL schema and transaction behavior remain private to the adapter.
func NewPostgresRepository(pool ControlPool) (Repository, error) {
	return ledgerpostgres.New(pool)
}

// CandidateFromGraph maps exact compiled graph evidence into a ledger
// candidate without exposing the ledger implementation package to application
// composition.
func CandidateFromGraph(instanceID, bundleID, expectedBundleID, actorID string, graph projectgraph.ProjectGraph) (Candidate, error) {
	return ledger.CandidateFromGraph(instanceID, bundleID, expectedBundleID, actorID, graph)
}

// IsAuthoredKind reports whether a graph kind participates in identity
// authority evidence.
func IsAuthoredKind(kind projectgraph.Kind) bool {
	return ledger.IsAuthoredKind(kind)
}

// NormalizeReferences validates and deterministically orders immutable
// reference evidence before it crosses the application boundary.
func NormalizeReferences(instanceID string, references []DurableReference) ([]DurableReference, error) {
	return ledger.NormalizeReferences(instanceID, references)
}

// IsReviewedReferenceOwnerKind reports whether a binding belongs to the
// compiler-projected grant/publication reference set.
func IsReviewedReferenceOwnerKind(kind string) bool {
	return ledger.IsReviewedReferenceOwnerKind(kind)
}

// SameTransitionEvidence compares the immutable transition evidence used for
// idempotent preparation and replay.
func SameTransitionEvidence(left, right Transition) bool {
	return ledger.SameTransitionEvidence(left, right)
}

// ValidateTransition validates a transition without mutating its value.
func ValidateTransition(transition Transition) error {
	return ledger.ValidateTransition(transition)
}

// NewCoordinator constructs the durable transition coordinator.
func NewCoordinator(repository TransitionRepository) *Coordinator {
	return ledger.NewCoordinator(repository)
}
