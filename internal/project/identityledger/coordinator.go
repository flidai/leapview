package identityledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	// ErrCoordinatorInvalid identifies a coordinator that was not configured
	// with a transition repository.
	ErrCoordinatorInvalid = errors.New("identity transition coordinator is invalid")
	// ErrDeliveryCommitRequired indicates that a non-terminal transition was
	// run without the delivery commit needed to finish it.
	ErrDeliveryCommitRequired = errors.New("identity transition delivery commit is required")
)

// TransitionRepository is the narrow control-plane port used by Coordinator.
// Implementations own durability, locking, and compare-and-set semantics;
// the coordinator only orders these calls and makes the recovery decisions
// that cannot be represented by one repository transaction.
type TransitionRepository interface {
	PrepareTransition(context.Context, Transition) (Transition, error)
	LoadTransition(context.Context, string, string) (Transition, error)
	AdvanceTransition(context.Context, string, string, TransitionPhase, TransitionPhase, string) (Transition, error)
	Plan(context.Context, Candidate) (Plan, error)
	Activate(context.Context, Candidate) (Plan, error)
	Rollback(context.Context, Rollback) (Plan, error)
}

// DeliveryCommit is the durable delivery-side commit for a transition. It is
// called once for each coordinator attempt that reaches identity_active. The
// operation must be idempotent because a failed attempt is retried while the
// journal remains identity_active.
type DeliveryCommit func(context.Context) error

// Coordinator advances one prepared transition through identity and delivery
// activation. Repository methods remain the source of truth for durable state;
// no process-local claims or compensation phases are introduced here.
type Coordinator struct {
	Repository TransitionRepository
}

// NewCoordinator constructs a transition coordinator over repository.
func NewCoordinator(repository TransitionRepository) *Coordinator {
	return &Coordinator{Repository: repository}
}

// Run prepares or exactly replays transition, then resumes its durable phase.
// The phase and error fields in transition are progress fields, not caller
// fencing tokens. The immutable transition evidence is prepared exactly; the
// current phase is loaded from the repository before any work is resumed.
func (c *Coordinator) Run(ctx context.Context, transition Transition, commit DeliveryCommit) (Transition, error) {
	repository, err := c.repository()
	if err != nil {
		return Transition{}, err
	}

	normalized, err := NormalizeTransition(transition)
	if err != nil {
		return Transition{}, err
	}
	// PrepareTransition accepts only the initial mutable state. Clearing the
	// mutable fields allows a caller to replay the same immutable request using
	// a previously loaded row without making that row's progress authoritative.
	seed := normalized
	seed.Phase = PhasePrepared
	seed.Error = ""
	prepared, err := repository.PrepareTransition(ctx, seed)
	if err != nil {
		return Transition{}, err
	}
	loaded, err := repository.LoadTransition(ctx, prepared.InstanceID, prepared.TransitionID)
	if err != nil {
		return Transition{}, err
	}
	if !sameTransitionEvidence(loaded, normalized) {
		return loaded, fmt.Errorf("%w: transition %q immutable evidence differs", ErrTransitionConflict, normalized.TransitionID)
	}
	if loaded.InstanceID != normalized.InstanceID {
		return loaded, fmt.Errorf("%w: transition %q belongs to another instance", ErrTransitionConflict, normalized.TransitionID)
	}
	return c.resume(ctx, repository, loaded, commit)
}

func (c *Coordinator) repository() (TransitionRepository, error) {
	if c == nil || c.Repository == nil {
		return nil, fmt.Errorf("%w: repository is required", ErrCoordinatorInvalid)
	}
	return c.Repository, nil
}

func (c *Coordinator) resume(ctx context.Context, repository TransitionRepository, transition Transition, commit DeliveryCommit) (Transition, error) {
	for {
		var err error
		switch transition.Phase {
		case PhasePrepared:
			// Reject deterministic admission failures before claiming the one
			// instance cutover slot. The same checks are repeated after the CAS
			// because this preview is not itself a mutation fence.
			candidate := candidateFromTransition(transition)
			plan, planErr := repository.Plan(ctx, candidate)
			if planErr != nil {
				return transition, planErr
			}
			if transition.Operation == OperationPublish {
				if planErr := rejectBlockingOutcomes(plan); planErr != nil {
					return transition, planErr
				}
			}
			if plan.ObservedBundleID != transition.ExpectedBundleID && plan.ObservedBundleID != transition.BundleID {
				return transition, fmt.Errorf("%w: transition %q expected observed bundle %q, found %q", ErrActivationConflict, transition.TransitionID, transition.ExpectedBundleID, plan.ObservedBundleID)
			}
			// Claim the instance cutover owner before observing or mutating
			// identity state. PostgreSQL fences this phase with the partial
			// unique index, so two prepared transitions cannot race between
			// Plan and Activate.
			transition, err = advance(ctx, repository, transition, PhasePrepared, PhaseIdentityPending, "")
			if err != nil {
				return transition, err
			}
			continue

		case PhaseIdentityPending:
			candidate := candidateFromTransition(transition)
			plan, err := repository.Plan(ctx, candidate)
			if err != nil {
				return transition, err
			}
			if transition.Operation == OperationPublish {
				if err := rejectBlockingOutcomes(plan); err != nil {
					return transition, err
				}
			}
			if plan.ObservedBundleID == transition.BundleID {
				// The identity commit may have succeeded before its phase CAS
				// was acknowledged. Only the exact target bundle proves that
				// fact; every other observed bundle remains a conflict only when
				// it is neither the expected base nor the exact target.
				transition, err = advance(ctx, repository, transition, PhaseIdentityPending, PhaseIdentityActive, "")
				if err != nil {
					return transition, err
				}
				continue
			}
			if plan.ObservedBundleID != transition.ExpectedBundleID {
				return transition, fmt.Errorf("%w: transition %q expected observed bundle %q, found %q", ErrActivationConflict, transition.TransitionID, transition.ExpectedBundleID, plan.ObservedBundleID)
			}
			if transition.Operation == OperationPublish {
				if _, err := repository.Activate(ctx, candidate); err != nil {
					return transition, err
				}
			} else {
				if _, err := repository.Rollback(ctx, Rollback{
					InstanceID:       transition.InstanceID,
					BundleID:         transition.BundleID,
					ExpectedBundleID: transition.ExpectedBundleID,
					ActorID:          transition.ActorID,
					Reason:           transition.Reason,
				}); err != nil {
					return transition, err
				}
			}
			transition, err = advance(ctx, repository, transition, PhaseIdentityPending, PhaseIdentityActive, "")
			if err != nil {
				return transition, err
			}

		case PhaseIdentityActive:
			if commit == nil {
				return transition, fmt.Errorf("%w: transition %q", ErrDeliveryCommitRequired, transition.TransitionID)
			}
			plan, err := repository.Plan(ctx, candidateFromTransition(transition))
			if err != nil {
				return transition, err
			}
			if plan.ObservedBundleID != transition.BundleID {
				return transition, fmt.Errorf("%w: transition %q expected active bundle %q before delivery, found %q", ErrActivationConflict, transition.TransitionID, transition.BundleID, plan.ObservedBundleID)
			}
			if err := commit(ctx); err != nil {
				message := phaseError(err)
				recorded, recordErr := advance(ctx, repository, transition, PhaseIdentityActive, PhaseIdentityActive, message)
				if recordErr != nil {
					return recorded, errors.Join(err, recordErr)
				}
				return recorded, err
			}
			transition, err = advance(ctx, repository, transition, PhaseIdentityActive, PhaseDeliveryActive, "")
			if err != nil {
				return transition, err
			}

		case PhaseDeliveryActive:
			transition, err = advance(ctx, repository, transition, PhaseDeliveryActive, PhaseCompleted, "")
			if err != nil {
				return transition, err
			}

		case PhaseCompleted:
			return transition, nil

		default:
			return transition, fmt.Errorf("%w: unknown phase %q", ErrInvalidTransition, transition.Phase)
		}
	}
}

func candidateFromTransition(transition Transition) Candidate {
	return Candidate{
		InstanceID:       transition.InstanceID,
		BundleID:         transition.BundleID,
		ExpectedBundleID: transition.ExpectedBundleID,
		ActorID:          transition.ActorID,
		Resources:        append([]Resource(nil), transition.Resources...),
	}
}

func advance(ctx context.Context, repository TransitionRepository, transition Transition, expected, next TransitionPhase, phaseError string) (Transition, error) {
	updated, err := repository.AdvanceTransition(ctx, transition.InstanceID, transition.TransitionID, expected, next, phaseError)
	if err != nil {
		return updated, err
	}
	return updated, nil
}

func rejectBlockingOutcomes(plan Plan) error {
	for _, outcome := range plan.Outcomes {
		switch outcome.Outcome {
		case OutcomeCollision:
			return fmt.Errorf("%w: transition candidate resource %q: %s", ErrKindConflict, outcome.AuthoredID, outcome.Detail)
		case OutcomeRestoreRequired:
			return fmt.Errorf("%w: transition candidate resource %q: %s", ErrRestoreRequired, outcome.AuthoredID, outcome.Detail)
		}
	}
	return nil
}

func phaseError(err error) string {
	message := strings.TrimSpace(strings.ToValidUTF8(err.Error(), "�"))
	if message == "" {
		message = "delivery commit failed"
	}
	for len(message) > 4096 {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return message
}

func sameTransitionEvidence(left, right Transition) bool {
	if left.TransitionID != right.TransitionID || left.Operation != right.Operation ||
		left.InstanceID != right.InstanceID || left.CandidateID != right.CandidateID ||
		left.BundleID != right.BundleID || left.ExpectedBundleID != right.ExpectedBundleID ||
		left.ActorID != right.ActorID || left.Reason != right.Reason || left.GraphDigest != right.GraphDigest {
		return false
	}
	leftResources, leftErr := NormalizeResources(left.Resources)
	rightResources, rightErr := NormalizeResources(right.Resources)
	if leftErr != nil || rightErr != nil || len(leftResources) != len(rightResources) {
		return false
	}
	for index := range leftResources {
		if leftResources[index] != rightResources[index] {
			return false
		}
	}
	return true
}
