package identityledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// TransitionOperation identifies the durable operation being fenced.
type TransitionOperation string

const (
	OperationPublish  TransitionOperation = "publish"
	OperationRollback TransitionOperation = "rollback"
	OperationRestore  TransitionOperation = "restore"
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
// publish, explicit restore, or rollback. Resources, approved restore IDs, and
// GraphDigest are persisted together so a retry cannot silently change the
// authored evidence behind a transition.
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
	References       []DurableReference
	// ApprovedAuthoredIDs is immutable approval evidence for an explicit
	// restore. It must be empty for ordinary publish and rollback transitions.
	ApprovedAuthoredIDs []projectgraph.ResourceID
	GraphDigest         string
	Phase               TransitionPhase
	Error               string
	PreparedAt          time.Time
	PhaseAt             time.Time
	UpdatedAt           time.Time
	CompletedAt         *time.Time
}

// NormalizeTransition validates immutable transition input, stable-sorts its
// resources, and fills an omitted phase.
func NormalizeTransition(transition Transition) (Transition, error) {
	if err := validateToken("transition id", transition.TransitionID); err != nil {
		return Transition{}, err
	}
	if transition.Operation != OperationPublish && transition.Operation != OperationRollback && transition.Operation != OperationRestore {
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
	approvedAuthoredIDs, err := NormalizeApprovedAuthoredIDs(transition.ApprovedAuthoredIDs)
	if err != nil {
		return Transition{}, err
	}
	if transition.Operation == OperationRestore {
		if len(approvedAuthoredIDs) == 0 {
			return Transition{}, fmt.Errorf("%w: restore approved authored IDs are required", ErrInvalidInput)
		}
		if strings.TrimSpace(transition.Reason) == "" {
			return Transition{}, fmt.Errorf("%w: restore reason is required", ErrInvalidInput)
		}
	} else if len(approvedAuthoredIDs) > 0 {
		return Transition{}, fmt.Errorf("%w: approved authored IDs only valid for restore transitions", ErrInvalidTransition)
	}
	resources, err := NormalizeResources(transition.Resources)
	if err != nil {
		return Transition{}, err
	}
	references, err := NormalizeReferences(transition.InstanceID, transition.References)
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
	transition.References = references
	transition.ApprovedAuthoredIDs = approvedAuthoredIDs
	return transition, nil
}

// NormalizeApprovedAuthoredIDs validates and stable-sorts the exact identity
// IDs approved for an explicit restore. An empty input is valid here so the
// ordinary publish/rollback representation serializes deterministically as [].
func NormalizeApprovedAuthoredIDs(ids []projectgraph.ResourceID) ([]projectgraph.ResourceID, error) {
	result := append([]projectgraph.ResourceID(nil), ids...)
	seen := make(map[projectgraph.ResourceID]struct{}, len(result))
	for _, id := range result {
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("%w: restore authored id: %v", ErrInvalidInput, err)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%w %q", ErrDuplicateAuthoredID, id)
		}
		seen[id] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// ApprovedAuthoredIDsJSON returns the stable JSON representation persisted in
// the transition journal. Empty evidence is encoded as [] rather than null.
func ApprovedAuthoredIDsJSON(ids []projectgraph.ResourceID) ([]byte, error) {
	normalized, err := NormalizeApprovedAuthoredIDs(ids)
	if err != nil {
		return nil, err
	}
	if normalized == nil {
		normalized = []projectgraph.ResourceID{}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal approved restore authored IDs: %w", err)
	}
	return encoded, nil
}

// NormalizeReferences validates immutable reference bindings and returns a
// stable reference-ID ordering. Lifecycle timestamps belong to the live ledger
// row and are not admitted as candidate evidence.
func NormalizeReferences(instanceID string, references []DurableReference) ([]DurableReference, error) {
	if err := validateToken("instance id", instanceID); err != nil {
		return nil, err
	}
	result := append([]DurableReference(nil), references...)
	seen := make(map[string]struct{}, len(result))
	for index := range result {
		reference := &result[index]
		if reference.InstanceID != instanceID || reference.ReferenceID == "" || reference.ReferenceID != strings.TrimSpace(reference.ReferenceID) || len(reference.ReferenceID) > 255 ||
			reference.OwnerAuthoredID == "" || reference.OwnerAuthoredID != strings.TrimSpace(reference.OwnerAuthoredID) || len(reference.OwnerAuthoredID) > 255 ||
			reference.OwnerKind == "" || reference.OwnerKind != strings.TrimSpace(reference.OwnerKind) || len(reference.OwnerKind) > 255 {
			return nil, fmt.Errorf("%w: durable reference identity", ErrInvalidInput)
		}
		if _, duplicate := seen[reference.ReferenceID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate durable reference %q", ErrInvalidInput, reference.ReferenceID)
		}
		seen[reference.ReferenceID] = struct{}{}
		if err := reference.TargetAuthoredID.Validate(); err != nil || !IsAuthoredKind(reference.ExpectedKind) {
			return nil, fmt.Errorf("%w: durable reference %q target", ErrInvalidInput, reference.ReferenceID)
		}
		if reference.Lifecycle != "" || reference.SuspendedAt != nil || reference.ReactivatedAt != nil {
			return nil, fmt.Errorf("%w: durable reference %q contains mutable lifecycle state", ErrInvalidInput, reference.ReferenceID)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ReferenceID < result[j].ReferenceID })
	return result, nil
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

// ReferenceEvidenceJSON returns the stable JSON persisted with the transition.
// It encodes only immutable bindings; live lifecycle state remains authoritative
// in durable_resource_reference.
func ReferenceEvidenceJSON(instanceID string, references []DurableReference) ([]byte, error) {
	normalized, err := NormalizeReferences(instanceID, references)
	if err != nil {
		return nil, err
	}
	type evidenceReference struct {
		ReferenceID      string                  `json:"reference_id"`
		OwnerAuthoredID  string                  `json:"owner_authored_id"`
		OwnerKind        string                  `json:"owner_kind"`
		TargetAuthoredID projectgraph.ResourceID `json:"target_authored_id"`
		ExpectedKind     projectgraph.Kind       `json:"expected_kind"`
	}
	evidence := make([]evidenceReference, len(normalized))
	for index, reference := range normalized {
		evidence[index] = evidenceReference{
			ReferenceID: reference.ReferenceID, OwnerAuthoredID: reference.OwnerAuthoredID,
			OwnerKind: reference.OwnerKind, TargetAuthoredID: reference.TargetAuthoredID,
			ExpectedKind: reference.ExpectedKind,
		}
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("marshal transition reference evidence: %w", err)
	}
	return encoded, nil
}

// SameTransitionEvidence compares only the immutable request and authored
// evidence fields. Mutable journal progress and timestamps are deliberately
// excluded so callers share one exact-replay definition.
func SameTransitionEvidence(left, right Transition) bool {
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
	leftReferences, leftErr := ReferenceEvidenceJSON(left.InstanceID, left.References)
	rightReferences, rightErr := ReferenceEvidenceJSON(right.InstanceID, right.References)
	if leftErr != nil || rightErr != nil || string(leftReferences) != string(rightReferences) {
		return false
	}
	leftApproved, leftErr := ApprovedAuthoredIDsJSON(left.ApprovedAuthoredIDs)
	rightApproved, rightErr := ApprovedAuthoredIDsJSON(right.ApprovedAuthoredIDs)
	if leftErr != nil || rightErr != nil || string(leftApproved) != string(rightApproved) {
		return false
	}
	return true
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
