package identityledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

// TransitionOperation identifies the durable operation being fenced.
type TransitionOperation string

const (
	OperationPublish  TransitionOperation = "publish"
	OperationRollback TransitionOperation = "rollback"
)

// TransitionPhase is the monotonic state of an activation transition.
type TransitionPhase string

const (
	PhasePrepared        TransitionPhase = "prepared"
	PhaseIdentityPending TransitionPhase = "identity_pending"
	PhaseIdentityActive  TransitionPhase = "identity_active"
	PhaseDeliveryActive  TransitionPhase = "delivery_active"
	PhaseCompleted       TransitionPhase = "completed"
)

var (
	ErrTransitionNotFound = errors.New("activation transition not found")
	ErrTransitionConflict = errors.New("activation transition conflict")
	ErrPhaseConflict      = errors.New("activation transition phase conflict")
	ErrInvalidTransition  = errors.New("invalid activation transition")
)

// Transition is the immutable input and mutable progress record for one
// publish or rollback. Resources and GraphDigest are persisted together so
// a retry cannot silently change the authored evidence behind a transition.
type Transition struct {
	TransitionID     string
	Operation        TransitionOperation
	InstanceID       string
	CandidateID      string
	BundleID         string
	ExpectedBundleID string
	ActorID          string
	Reason           string
	Resources        []Resource
	GraphDigest      string
	Phase            TransitionPhase
	Error            string
	PreparedAt       time.Time
	PhaseAt          time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
}

// NormalizeTransition validates immutable transition input, stable-sorts its
// resources, and fills an omitted phase.
func NormalizeTransition(transition Transition) (Transition, error) {
	if err := validateToken("transition id", transition.TransitionID); err != nil {
		return Transition{}, err
	}
	if transition.Operation != OperationPublish && transition.Operation != OperationRollback {
		return Transition{}, fmt.Errorf("%w: operation %q", ErrInvalidInput, transition.Operation)
	}
	if err := validateToken("instance id", transition.InstanceID); err != nil {
		return Transition{}, err
	}
	if err := validateToken("candidate id", transition.CandidateID); err != nil {
		return Transition{}, err
	}
	if err := validateToken("bundle id", transition.BundleID); err != nil {
		return Transition{}, err
	}
	if transition.ExpectedBundleID != "" {
		if err := validateToken("expected bundle id", transition.ExpectedBundleID); err != nil {
			return Transition{}, err
		}
	}
	if err := validateToken("actor id", transition.ActorID); err != nil {
		return Transition{}, err
	}
	if strings.TrimSpace(transition.Reason) != transition.Reason || len(transition.Reason) > 2048 {
		return Transition{}, fmt.Errorf("%w: reason", ErrInvalidInput)
	}
	resources, err := NormalizeResources(transition.Resources)
	if err != nil {
		return Transition{}, err
	}
	if transition.Phase == "" {
		transition.Phase = PhasePrepared
	}
	if !validPhase(transition.Phase) {
		return Transition{}, fmt.Errorf("%w: phase %q", ErrInvalidInput, transition.Phase)
	}
	if transition.Error != "" && len(transition.Error) > 4096 {
		return Transition{}, fmt.Errorf("%w: phase error", ErrInvalidInput)
	}
	if err := platformdigest.ValidateSHA256Identity(transition.GraphDigest); err != nil {
		return Transition{}, fmt.Errorf("%w: graph digest: %v", ErrInvalidInput, err)
	}
	transition.Resources = resources
	return transition, nil
}

// ValidateTransition validates without changing the supplied value.
func ValidateTransition(transition Transition) error {
	_, err := NormalizeTransition(transition)
	return err
}

// ResourceEvidenceJSON returns the stable, normalized JSON representation
// persisted as transition evidence. It is not a hashing or canonicalization
// authority; GraphDigest binds the existing compiled-graph digest.
func ResourceEvidenceJSON(resources []Resource) ([]byte, error) {
	normalized, err := NormalizeResources(resources)
	if err != nil {
		return nil, err
	}
	type evidenceResource struct {
		AuthoredID string `json:"authored_id"`
		Kind       string `json:"kind"`
	}
	evidence := make([]evidenceResource, len(normalized))
	for i, resource := range normalized {
		evidence[i] = evidenceResource{AuthoredID: string(resource.AuthoredID), Kind: string(resource.Kind)}
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("marshal transition evidence: %w", err)
	}
	return canonical, nil
}

// CanAdvance reports whether a phase may move forward through the journal.
func CanAdvance(from, to TransitionPhase) bool {
	switch from {
	case PhasePrepared:
		return to == PhaseIdentityPending
	case PhaseIdentityPending:
		return to == PhaseIdentityActive
	case PhaseIdentityActive:
		return to == PhaseDeliveryActive
	case PhaseDeliveryActive:
		return to == PhaseCompleted
	default:
		return false
	}
}

func validPhase(phase TransitionPhase) bool {
	return phase == PhasePrepared || phase == PhaseIdentityPending || phase == PhaseIdentityActive || phase == PhaseDeliveryActive || phase == PhaseCompleted
}
