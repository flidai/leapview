package app

// This file is the application boundary between release candidate delivery,
// the PostgreSQL-backed identity ledger, and target-owned sealed delivery.
// It deliberately contains no store fallback: callers must supply the
// durable identity repository used by the running instance.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	identityledger "github.com/flidai/leapview/internal/project/identityledger/module"
	"github.com/flidai/leapview/internal/release"
)

var (
	// ErrIdentityLifecycleInvalid identifies a composition boundary that is
	// incomplete or whose immutable evidence does not bind together.
	ErrIdentityLifecycleInvalid = errors.New("identity lifecycle integration is invalid")
	// ErrIdentityLifecycleUnavailable identifies a missing durable identity
	// repository. There is intentionally no local or SQLite fallback.
	ErrIdentityLifecycleUnavailable = errors.New("identity lifecycle repository is unavailable")
)

// IdentityCandidateInput is the exact compiler-to-ledger identity hand-off
// used while a candidate is becoming ready. The graph is taken from the
// retained compiler evidence; callers must not reload or recompile it here.
type IdentityCandidateInput struct {
	InstanceID               string
	ExpectedBaseGenerationID string
	ActorID                  string
	Artifacts                release.CandidateArtifactSet
}

// BuildIdentityCandidate constructs the ledger candidate for the serving
// generation in artifacts. The project root is excluded by
// identityledger.CandidateFromGraph; all authored resource IDs and kinds are
// retained exactly and normalized by the ledger package.
func BuildIdentityCandidate(input IdentityCandidateInput) (identityledger.Candidate, error) {
	graph := input.Artifacts.Compiler.Graph
	if err := graph.Validate(); err != nil {
		return identityledger.Candidate{}, fmt.Errorf("%w: compiler graph: %w", ErrIdentityLifecycleInvalid, err)
	}
	if projectID := input.Artifacts.Generation.Identity.ProjectID; projectID != "" && projectID != graph.ProjectID() {
		return identityledger.Candidate{}, fmt.Errorf("%w: serving identity project %q does not match compiler graph project %q", ErrIdentityLifecycleInvalid, projectID, graph.ProjectID())
	}
	candidate, err := identityledger.CandidateFromGraph(
		input.InstanceID,
		input.Artifacts.Generation.Identity.GenerationID,
		input.ExpectedBaseGenerationID,
		input.ActorID,
		graph,
	)
	if err != nil {
		return identityledger.Candidate{}, fmt.Errorf("%w: ledger candidate: %w", ErrIdentityLifecycleInvalid, err)
	}
	return candidate, nil
}

// IdentityPublishPreparationInput contains the immutable identity evidence
// prepared at the ReadyCandidate boundary. CandidateID is Deployment's
// candidate identity; it is not inferred from graph metadata.
type IdentityPublishPreparationInput struct {
	IdentityCandidateInput
	CandidateID string
	Reason      string
}

// PlanIdentityCandidate validates the exact retained compiler graph against
// durable identity history without mutating the ledger. Candidate planning
// uses this admission check before expensive materialization begins.
func PlanIdentityCandidate(
	ctx context.Context,
	repository identityledger.TransitionRepository,
	input IdentityCandidateInput,
) (identityledger.Candidate, identityledger.Plan, error) {
	if repository == nil {
		return identityledger.Candidate{}, identityledger.Plan{}, fmt.Errorf("%w: %w", ErrIdentityLifecycleUnavailable, ErrIdentityLifecycleInvalid)
	}
	candidate, err := BuildIdentityCandidate(input)
	if err != nil {
		return identityledger.Candidate{}, identityledger.Plan{}, err
	}
	plan, err := repository.Plan(ctx, candidate)
	if err != nil {
		return identityledger.Candidate{}, identityledger.Plan{}, fmt.Errorf("plan identity candidate: %w", err)
	}
	if err := validateIdentityPlan(candidate, plan); err != nil {
		return identityledger.Candidate{}, identityledger.Plan{}, err
	}
	return candidate, plan, nil
}

// PrepareIdentityPublishTransition plans the exact candidate against the
// durable identity ledger, rejects blocking outcomes, and records the
// immutable publish transition evidence. Planning precedes preparation so a
// collision or restore-required result never leaves a prepared transition.
func PrepareIdentityPublishTransition(
	ctx context.Context,
	repository identityledger.TransitionRepository,
	input IdentityPublishPreparationInput,
) (identityledger.Transition, error) {
	if repository == nil {
		return identityledger.Transition{}, fmt.Errorf("%w: %w", ErrIdentityLifecycleUnavailable, ErrIdentityLifecycleInvalid)
	}
	if err := validateIdentityCandidateID(input.CandidateID); err != nil {
		return identityledger.Transition{}, err
	}
	candidate, _, err := PlanIdentityCandidate(ctx, repository, input.IdentityCandidateInput)
	if err != nil {
		return identityledger.Transition{}, err
	}
	references, err := ProjectIdentityReferences(candidate.InstanceID, input.Artifacts)
	if err != nil {
		return identityledger.Transition{}, err
	}
	transitionID, err := IdentityPublishTransitionID(input.CandidateID)
	if err != nil {
		return identityledger.Transition{}, err
	}
	transition := identityledger.Transition{
		TransitionID:     transitionID,
		Operation:        identityledger.OperationPublish,
		InstanceID:       candidate.InstanceID,
		CandidateID:      input.CandidateID,
		BundleID:         candidate.BundleID,
		ExpectedBundleID: candidate.ExpectedBundleID,
		ActorID:          candidate.ActorID,
		Reason:           input.Reason,
		Resources:        append([]identityledger.Resource(nil), candidate.Resources...),
		References:       references,
		GraphDigest:      input.Artifacts.Compiler.Graph.Digest(),
	}
	if err := identityledger.ValidateTransition(transition); err != nil {
		return identityledger.Transition{}, fmt.Errorf("%w: publish transition: %w", ErrIdentityLifecycleInvalid, err)
	}
	prepared, err := repository.PrepareTransition(ctx, transition)
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("prepare identity publish transition: %w", err)
	}
	if !identityledger.SameTransitionEvidence(prepared, transition) {
		return identityledger.Transition{}, fmt.Errorf("%w: prepared publish transition evidence changed", identityledger.ErrTransitionConflict)
	}
	return prepared, nil
}

// ProjectIdentityReferences projects only reviewed control relations from the
// exact compiled candidate evidence. It never reads mutable runtime models or
// derives target kinds from names.
func ProjectIdentityReferences(instanceID string, artifacts release.CandidateArtifactSet) ([]identityledger.DurableReference, error) {
	graph := artifacts.Compiler.Graph
	if err := graph.Validate(); err != nil {
		return nil, fmt.Errorf("%w: reference graph: %w", ErrIdentityLifecycleInvalid, err)
	}
	references := make([]identityledger.DurableReference, 0)
	for key, grant := range artifacts.Compiler.Manifest.Access.Grants {
		if grant.ID == "" || grant.ID != key {
			return nil, fmt.Errorf("%w: grant %q identity changed", ErrIdentityLifecycleInvalid, key)
		}
		if _, err := projectgraph.NewResourceID(grant.ID); err != nil {
			return nil, fmt.Errorf("%w: grant %q has invalid authored identity: %v", ErrIdentityLifecycleInvalid, grant.ID, err)
		}
		expectedKind := projectgraph.Kind(grant.Object.Kind)
		if expectedKind == projectgraph.KindProject {
			// Project-wide capability is instance administration, not a durable
			// reference to one authored analytics resource.
			continue
		}
		if !identityledger.IsAuthoredKind(expectedKind) {
			return nil, fmt.Errorf("%w: grant %q has unsupported target kind %q", ErrIdentityLifecycleInvalid, grant.ID, grant.Object.Kind)
		}
		targetID := projectgraph.ResourceID(grant.Object.ID)
		resource, ok := graph.Resource(targetID)
		if !ok || resource.Kind != expectedKind {
			return nil, fmt.Errorf("%w: grant %q target %q does not match graph kind %q", ErrIdentityLifecycleInvalid, grant.ID, targetID, expectedKind)
		}
		references = append(references, identityledger.DurableReference{
			InstanceID: instanceID, ReferenceID: "grant:" + grant.ID,
			OwnerAuthoredID: grant.ID, OwnerKind: identityledger.ReferenceOwnerKindGrant,
			TargetAuthoredID: targetID, ExpectedKind: expectedKind,
		})
	}
	for key, publication := range artifacts.Compiler.Manifest.Publications {
		if publication.Name == "" || publication.Name != key {
			return nil, fmt.Errorf("%w: dashboard publication %q identity changed", ErrIdentityLifecycleInvalid, key)
		}
		if _, err := projectgraph.NewResourceID(publication.Name); err != nil {
			return nil, fmt.Errorf("%w: dashboard publication %q has invalid authored identity: %v", ErrIdentityLifecycleInvalid, publication.Name, err)
		}
		for _, dependency := range publication.DependencyAssetIDs {
			targetID := projectgraph.ResourceID(dependency)
			resource, ok := graph.Resource(targetID)
			if !ok || !identityledger.IsAuthoredKind(resource.Kind) {
				return nil, fmt.Errorf("%w: dashboard publication %q dependency %q is not an authored graph resource", ErrIdentityLifecycleInvalid, key, dependency)
			}
			referenceID := "dashboard_publication:" + strconv.Itoa(len(key)) + ":" + key + ":" + strconv.Itoa(len(dependency)) + ":" + dependency
			references = append(references, identityledger.DurableReference{
				InstanceID: instanceID, ReferenceID: referenceID,
				OwnerAuthoredID: key, OwnerKind: identityledger.ReferenceOwnerKindDashboardPublication,
				TargetAuthoredID: targetID, ExpectedKind: resource.Kind,
			})
		}
	}
	normalized, err := identityledger.NormalizeReferences(instanceID, references)
	if err != nil {
		return nil, fmt.Errorf("%w: project reference evidence: %w", ErrIdentityLifecycleInvalid, err)
	}
	return normalized, nil
}

// IdentityPublishTransitionID derives a stable, transparent transition
// identity from Deployment's candidate ID. GraphDigest and Resources remain
// immutable transition evidence; they are never rehashed or replaced by this
// application boundary.
func IdentityPublishTransitionID(candidateID string) (string, error) {
	if err := validateIdentityCandidateID(candidateID); err != nil {
		return "", err
	}
	return "identity-publish:" + candidateID, nil
}

func validateIdentityCandidateID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return fmt.Errorf("%w: candidate id", ErrIdentityLifecycleInvalid)
	}
	return nil
}

func validateIdentityPlan(candidate identityledger.Candidate, plan identityledger.Plan) error {
	if plan.InstanceID != candidate.InstanceID || plan.CandidateBundleID != candidate.BundleID {
		return fmt.Errorf("%w: identity plan does not bind candidate %q", ErrIdentityLifecycleInvalid, candidate.BundleID)
	}
	if plan.ObservedBundleID != candidate.ExpectedBundleID {
		return fmt.Errorf("%w: candidate expected active bundle %q, found %q", identityledger.ErrActivationConflict, candidate.ExpectedBundleID, plan.ObservedBundleID)
	}
	for _, outcome := range plan.Outcomes {
		switch outcome.Outcome {
		case identityledger.OutcomeCollision:
			return fmt.Errorf("%w: identity candidate resource %q: %s", identityledger.ErrKindConflict, outcome.AuthoredID, outcome.Detail)
		case identityledger.OutcomeRestoreRequired:
			return fmt.Errorf("%w: identity candidate resource %q: %s", identityledger.ErrRestoreRequired, outcome.AuthoredID, outcome.Detail)
		}
	}
	return nil
}

// PublishedIdentityTransitionReader reads the original publish transition
// for one active bundle. Rollback uses this durable evidence verbatim.
type PublishedIdentityTransitionReader interface {
	PublishedBundleTransition(context.Context, string, string) (identityledger.Transition, error)
}

// PublishIdentityTransitionReader reads prepared or resuming publish evidence
// for the forward publication path.
type PublishIdentityTransitionReader interface {
	BundlePublishTransition(context.Context, string, string) (identityledger.Transition, error)
}

type identityReferenceReconciler interface {
	ReconcileReferences(context.Context, string, []identityledger.DurableReference) ([]identityledger.DurableReference, error)
}

// IdentitySealedCoordinatorConfig wires identity durability around the
// existing target-owned sealed-control coordinator. Transitions is required;
// no fake or SQLite identity ledger is created by this adapter.
type IdentitySealedCoordinatorConfig struct {
	InstanceID           string
	Transitions          identityledger.TransitionRepository
	PublishTransitions   PublishIdentityTransitionReader
	PublishedTransitions PublishedIdentityTransitionReader
	References           identityReferenceReconciler
	Sealed               deploymentmodule.SealedCoordinator
}

// IdentitySealedCoordinator serializes target publication and rollback behind
// the identityledger.Coordinator while leaving seal verification,
// authorization, approvals, and target CAS in sealedcontrol unchanged.
type IdentitySealedCoordinator struct {
	instanceID  string
	transitions identityledger.TransitionRepository
	publish     PublishIdentityTransitionReader
	published   PublishedIdentityTransitionReader
	references  identityReferenceReconciler
	identity    *identityledger.Coordinator
	sealed      deploymentmodule.SealedCoordinator
}

var _ deploymentmodule.SealedCoordinator = (*IdentitySealedCoordinator)(nil)

// NewIdentitySealedCoordinator rejects incomplete production composition.
// When the supplied transition repository also exposes published evidence, it
// is used automatically; otherwise rollback remains unavailable and fails
// closed rather than reconstructing graph evidence.
func NewIdentitySealedCoordinator(config IdentitySealedCoordinatorConfig) (*IdentitySealedCoordinator, error) {
	if strings.TrimSpace(config.InstanceID) == "" || config.Transitions == nil || config.Sealed == nil {
		return nil, fmt.Errorf("%w: instance, identity repository, and sealed coordinator are required", ErrIdentityLifecycleUnavailable)
	}
	publish := config.PublishTransitions
	if publish == nil {
		var ok bool
		publish, ok = config.Transitions.(PublishIdentityTransitionReader)
		if !ok {
			return nil, fmt.Errorf("%w: publish transition reader is required", ErrIdentityLifecycleUnavailable)
		}
	}
	published := config.PublishedTransitions
	if published == nil {
		var ok bool
		published, ok = config.Transitions.(PublishedIdentityTransitionReader)
		if !ok {
			return nil, fmt.Errorf("%w: published transition reader is required", ErrIdentityLifecycleUnavailable)
		}
	}
	references := config.References
	if references == nil {
		var ok bool
		references, ok = config.Transitions.(identityReferenceReconciler)
		if !ok {
			return nil, fmt.Errorf("%w: durable reference reconciler is required", ErrIdentityLifecycleUnavailable)
		}
	}
	return &IdentitySealedCoordinator{
		instanceID: config.InstanceID, transitions: config.Transitions, publish: publish, published: published,
		references: references, identity: identityledger.NewCoordinator(config.Transitions), sealed: config.Sealed,
	}, nil
}

// Publish runs identity activation first and invokes the existing sealed
// publication as the delivery commit. A completed identity retry calls the
// idempotent sealed operation once to recover its durable result.
func (c *IdentitySealedCoordinator) Publish(ctx context.Context, request sealedcontrol.PublishRequest) (deployment.PublicationIntent, error) {
	return c.PublishWithActivation(ctx, request, nil)
}

// PublishWithActivation preserves sealedcontrol's runtime activation hook for
// callers that use it, while identityledger still owns the delivery ordering.
func (c *IdentitySealedCoordinator) PublishWithActivation(ctx context.Context, request sealedcontrol.PublishRequest, activate sealedcontrol.PublicationActivation) (deployment.PublicationIntent, error) {
	if err := c.validate(); err != nil {
		return deployment.PublicationIntent{}, err
	}
	if err := request.Validate(); err != nil {
		return deployment.PublicationIntent{}, err
	}
	transition, err := c.publish.BundlePublishTransition(ctx, c.instanceID, request.Generation.ID)
	if err != nil {
		return deployment.PublicationIntent{}, fmt.Errorf("load identity publish evidence: %w", err)
	}
	if err := validatePublishBinding(c.instanceID, transition, request); err != nil {
		return deployment.PublicationIntent{}, err
	}
	var publication deployment.PublicationIntent
	_, runErr := c.identity.Run(ctx, transition, func(commitCtx context.Context) error {
		if err := c.registerReferences(commitCtx, transition.References); err != nil {
			return err
		}
		var err error
		publication, err = c.publishSealed(commitCtx, request, activate)
		return err
	})
	if runErr != nil {
		return publication, runErr
	}
	if publication.ID == "" {
		// Coordinator.Run intentionally does not invoke delivery for a completed
		// transition. Reconcile the existing sealed result by its own idempotent
		// request identity; this is still behind the completed identity fence.
		publication, err = c.publishSealed(ctx, request, activate)
		if err != nil {
			return publication, err
		}
	}
	return publication, nil
}

// Rollback loads the original published transition and reuses its graph
// digest/resources as immutable evidence for the rollback transition.
func (c *IdentitySealedCoordinator) Rollback(ctx context.Context, request sealedcontrol.RollbackRequest) (deployment.RollbackResult, error) {
	return c.RollbackWithActivation(ctx, request, nil)
}

// RollbackWithActivation preserves sealedcontrol's runtime activation hook
// while preventing rollback from rebuilding or rehashing graph evidence.
func (c *IdentitySealedCoordinator) RollbackWithActivation(ctx context.Context, request sealedcontrol.RollbackRequest, activate sealedcontrol.PublicationActivation) (deployment.RollbackResult, error) {
	if err := c.validate(); err != nil {
		return deployment.RollbackResult{}, err
	}
	if err := request.Validate(); err != nil {
		return deployment.RollbackResult{}, err
	}
	published, err := c.published.PublishedBundleTransition(ctx, c.instanceID, request.Request.GenerationID)
	if err != nil {
		return deployment.RollbackResult{}, fmt.Errorf("load original identity publish evidence: %w", err)
	}
	transition, err := IdentityRollbackTransition(request, published)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	var result deployment.RollbackResult
	_, runErr := c.identity.Run(ctx, transition, func(commitCtx context.Context) error {
		if err := c.registerReferences(commitCtx, transition.References); err != nil {
			return err
		}
		var err error
		result, err = c.rollbackSealed(commitCtx, request, activate)
		return err
	})
	if runErr != nil {
		return result, runErr
	}
	if result.RequestDigest == "" {
		result, err = c.rollbackSealed(ctx, request, activate)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// IdentityRollbackTransition creates a rollback transition without touching
// compiler graph data. The published transition is the sole source for
// CandidateID, BundleID, GraphDigest, and authored-resource evidence.
func IdentityRollbackTransition(request sealedcontrol.RollbackRequest, published identityledger.Transition) (identityledger.Transition, error) {
	if published.Operation != identityledger.OperationPublish || published.Phase != identityledger.PhaseCompleted {
		return identityledger.Transition{}, fmt.Errorf("%w: rollback source is not a completed publish transition", identityledger.ErrTransitionConflict)
	}
	if published.InstanceID == "" || published.BundleID == "" || published.CandidateID == "" {
		return identityledger.Transition{}, fmt.Errorf("%w: original publish transition is incomplete", ErrIdentityLifecycleInvalid)
	}
	if request.Request.CandidateID != published.CandidateID || request.Request.GenerationID != published.BundleID {
		return identityledger.Transition{}, fmt.Errorf("%w: rollback request does not bind original publish evidence", identityledger.ErrTransitionConflict)
	}
	actor := request.ActorID
	if actor == "" {
		actor = request.Request.ActorID
	}
	if actor == "" {
		return identityledger.Transition{}, fmt.Errorf("%w: rollback actor is required", ErrIdentityLifecycleInvalid)
	}
	transitionID, err := identityRollbackTransitionID(request.Request.ID)
	if err != nil {
		return identityledger.Transition{}, err
	}
	transition := identityledger.Transition{
		TransitionID:     transitionID,
		Operation:        identityledger.OperationRollback,
		InstanceID:       published.InstanceID,
		CandidateID:      published.CandidateID,
		BundleID:         published.BundleID,
		ExpectedBundleID: request.Request.ExpectedBaseGenerationID,
		ActorID:          actor,
		Reason:           "rollback:" + request.Request.ID,
		Resources:        append([]identityledger.Resource(nil), published.Resources...),
		References:       append([]identityledger.DurableReference(nil), published.References...),
		GraphDigest:      published.GraphDigest,
	}
	if err := identityledger.ValidateTransition(transition); err != nil {
		return identityledger.Transition{}, fmt.Errorf("%w: rollback transition: %w", ErrIdentityLifecycleInvalid, err)
	}
	return transition, nil
}

func identityRollbackTransitionID(requestID string) (string, error) {
	if requestID == "" || requestID != strings.TrimSpace(requestID) || len(requestID) > 128 {
		return "", fmt.Errorf("%w: rollback request id", ErrIdentityLifecycleInvalid)
	}
	return "identity-rollback:" + requestID, nil
}

func (c *IdentitySealedCoordinator) validate() error {
	if c == nil || c.identity == nil || c.transitions == nil || c.publish == nil || c.published == nil || c.references == nil || c.sealed == nil || strings.TrimSpace(c.instanceID) == "" {
		return fmt.Errorf("%w: coordinator is incomplete", ErrIdentityLifecycleUnavailable)
	}
	return nil
}

func (c *IdentitySealedCoordinator) registerReferences(ctx context.Context, references []identityledger.DurableReference) error {
	stored, err := c.references.ReconcileReferences(ctx, c.instanceID, references)
	if err != nil {
		return fmt.Errorf("reconcile durable identity references: %w", err)
	}
	want, err := identityledger.NormalizeReferences(c.instanceID, references)
	if err != nil {
		return fmt.Errorf("normalize durable identity references: %w", err)
	}
	if len(stored) != len(want) {
		return fmt.Errorf("%w: durable reference reconciler returned %d bindings, want %d", ErrIdentityLifecycleInvalid, len(stored), len(want))
	}
	for index := range want {
		if stored[index].InstanceID != want[index].InstanceID || stored[index].ReferenceID != want[index].ReferenceID ||
			stored[index].OwnerAuthoredID != want[index].OwnerAuthoredID || stored[index].OwnerKind != want[index].OwnerKind ||
			stored[index].TargetAuthoredID != want[index].TargetAuthoredID || stored[index].ExpectedKind != want[index].ExpectedKind {
			return fmt.Errorf("%w: durable reference reconciler changed binding for %q", ErrIdentityLifecycleInvalid, want[index].ReferenceID)
		}
	}
	return nil
}

func (c *IdentitySealedCoordinator) publishSealed(ctx context.Context, request sealedcontrol.PublishRequest, activate sealedcontrol.PublicationActivation) (deployment.PublicationIntent, error) {
	if activate != nil {
		if coordinator, ok := c.sealed.(interface {
			PublishWithActivation(context.Context, sealedcontrol.PublishRequest, sealedcontrol.PublicationActivation) (deployment.PublicationIntent, error)
		}); ok {
			return coordinator.PublishWithActivation(ctx, request, activate)
		}
		return deployment.PublicationIntent{}, fmt.Errorf("%w: sealed coordinator does not support activation", ErrIdentityLifecycleInvalid)
	}
	return c.sealed.Publish(ctx, request)
}

func (c *IdentitySealedCoordinator) rollbackSealed(ctx context.Context, request sealedcontrol.RollbackRequest, activate sealedcontrol.PublicationActivation) (deployment.RollbackResult, error) {
	if activate != nil {
		if coordinator, ok := c.sealed.(interface {
			RollbackWithActivation(context.Context, sealedcontrol.RollbackRequest, sealedcontrol.PublicationActivation) (deployment.RollbackResult, error)
		}); ok {
			return coordinator.RollbackWithActivation(ctx, request, activate)
		}
		return deployment.RollbackResult{}, fmt.Errorf("%w: sealed coordinator does not support activation", ErrIdentityLifecycleInvalid)
	}
	return c.sealed.Rollback(ctx, request)
}

func validatePublishBinding(instanceID string, transition identityledger.Transition, request sealedcontrol.PublishRequest) error {
	if transition.Operation != identityledger.OperationPublish || transition.BundleID != request.Generation.ID || transition.CandidateID != request.Generation.CandidateID || transition.InstanceID != instanceID {
		return fmt.Errorf("%w: sealed publication does not bind original identity transition", identityledger.ErrTransitionConflict)
	}
	if request.Publication.ExpectedBaseGenerationID != transition.ExpectedBundleID {
		return fmt.Errorf("%w: sealed publication base differs from identity transition", identityledger.ErrTransitionConflict)
	}
	return nil
}
