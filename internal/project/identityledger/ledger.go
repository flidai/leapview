// Package identityledger defines the durable identity and activation contract
// for source-authored analytics resources.
//
// A resource has no generated platform UID. Its complete identity is the pair
// (instance ID, authored metadata.id), and its kind is immutable after the
// first successful activation.
package identityledger

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalidInput        = errors.New("invalid identity ledger input")
	ErrDuplicateAuthoredID = errors.New("duplicate authored resource id")
	ErrKindConflict        = errors.New("resource identity kind conflict")
	ErrRestoreRequired     = errors.New("explicit resource restore required")
	ErrActivationConflict  = errors.New("active source bundle changed")
	ErrBundleNotFound      = errors.New("source bundle not found")
	ErrReferenceConflict   = errors.New("durable reference conflict")
)

// Lifecycle is the durable state of an authored resource identity.
type Lifecycle string

const (
	LifecycleActive     Lifecycle = "active"
	LifecycleTombstoned Lifecycle = "tombstoned"
)

// ReferenceLifecycle follows the availability of its target identity.
type ReferenceLifecycle string

const (
	ReferenceActive    ReferenceLifecycle = "active"
	ReferenceSuspended ReferenceLifecycle = "suspended"
)

// ReviewedReferenceOwnerKind identifies the control-plane owners whose
// compiler-projected bindings may be validated during publication. Other
// durable reference owners remain outside that set. Omission from a candidate
// never implies suspension; target removal is handled by resource reconcile.
const (
	ReferenceOwnerKindGrant                = "grant"
	ReferenceOwnerKindDashboardPublication = "dashboard_publication"
)

func IsReviewedReferenceOwnerKind(kind string) bool {
	return kind == ReferenceOwnerKindGrant || kind == ReferenceOwnerKindDashboardPublication
}

// Resource is one source-authored identity in a compiled bundle.
type Resource struct {
	AuthoredID projectgraph.ResourceID
	Kind       projectgraph.Kind
}

// Identity is the durable ledger view of a resource.
type Identity struct {
	InstanceID      string
	AuthoredID      projectgraph.ResourceID
	Kind            projectgraph.Kind
	Lifecycle       Lifecycle
	ActiveBundleID  string
	TombstoneReason string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	TombstonedAt    *time.Time
	RestoredAt      *time.Time
}

// Candidate describes one compiled replacement bundle. ExpectedBundleID is
// an optimistic concurrency token; empty means the instance must have no
// active bundle.
type Candidate struct {
	InstanceID       string
	BundleID         string
	ExpectedBundleID string
	ActorID          string
	Resources        []Resource
}

// Restore is an explicit, audited activation of a tombstoned identity.
type Restore struct {
	Candidate   Candidate
	AuthoredIDs []projectgraph.ResourceID
	Reason      string
}

// Rollback atomically makes a historical bundle active again.
type Rollback struct {
	InstanceID       string
	BundleID         string
	ExpectedBundleID string
	ActorID          string
	Reason           string
}

// DurableReference stores the instance-qualified authored target and expected
// kind needed to prevent silent rebinding.
type DurableReference struct {
	InstanceID       string
	ReferenceID      string
	OwnerAuthoredID  string
	OwnerKind        string
	TargetAuthoredID projectgraph.ResourceID
	ExpectedKind     projectgraph.Kind
	Lifecycle        ReferenceLifecycle
	SuspendedAt      *time.Time
	ReactivatedAt    *time.Time
}

// OutcomeKind is one candidate-planning classification.
type OutcomeKind string

const (
	OutcomeCreated         OutcomeKind = "created"
	OutcomeUpdated         OutcomeKind = "updated"
	OutcomeTombstoned      OutcomeKind = "tombstoned"
	OutcomeRestored        OutcomeKind = "restored"
	OutcomeCollision       OutcomeKind = "collision"
	OutcomeRestoreRequired OutcomeKind = "restore_required"
)

// Outcome is deterministic candidate evidence sorted by authored ID.
type Outcome struct {
	AuthoredID projectgraph.ResourceID
	Kind       projectgraph.Kind
	Outcome    OutcomeKind
	Detail     string
}

// Plan is a read-only preview against one observed active bundle.
type Plan struct {
	InstanceID        string
	ObservedBundleID  string
	CandidateBundleID string
	Outcomes          []Outcome
}

// NormalizeResources validates candidate-wide uniqueness and returns a stable
// authored-ID ordering for persistence and diagnostics.
func NormalizeResources(resources []Resource) ([]Resource, error) {
	result := append([]Resource(nil), resources...)
	seen := make(map[projectgraph.ResourceID]projectgraph.Kind, len(result))
	for _, resource := range result {
		if err := resource.AuthoredID.Validate(); err != nil {
			return nil, fmt.Errorf("%w: authored id: %v", ErrInvalidInput, err)
		}
		if !IsAuthoredKind(resource.Kind) {
			return nil, fmt.Errorf("%w: resource %q has unsupported kind %q", ErrInvalidInput, resource.AuthoredID, resource.Kind)
		}
		if prior, ok := seen[resource.AuthoredID]; ok {
			return nil, fmt.Errorf("%w %q (%s and %s)", ErrDuplicateAuthoredID, resource.AuthoredID, prior, resource.Kind)
		}
		seen[resource.AuthoredID] = resource.Kind
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AuthoredID < result[j].AuthoredID })
	return result, nil
}

// IsAuthoredKind admits exactly the six analytics resource directories.
func IsAuthoredKind(kind projectgraph.Kind) bool {
	switch kind {
	case projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel,
		projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard:
		return true
	default:
		return false
	}
}

// ValidateCandidate checks the transaction-independent activation boundary.
func ValidateCandidate(candidate Candidate) error {
	if err := validateToken("instance id", candidate.InstanceID); err != nil {
		return err
	}
	if err := validateToken("bundle id", candidate.BundleID); err != nil {
		return err
	}
	if candidate.ExpectedBundleID != "" {
		if err := validateToken("expected bundle id", candidate.ExpectedBundleID); err != nil {
			return err
		}
	}
	if err := validateToken("actor id", candidate.ActorID); err != nil {
		return err
	}
	_, err := NormalizeResources(candidate.Resources)
	return err
}

func validateToken(label, value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 255 {
		return fmt.Errorf("%w: %s", ErrInvalidInput, label)
	}
	return nil
}
