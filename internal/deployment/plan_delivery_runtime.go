package deployment

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/project/graph"
)

// DeliveryGenerationStatus describes serving-state root lifecycle.
type DeliveryGenerationStatus string

const (
	DeliveryGenerationPrepared DeliveryGenerationStatus = "prepared"
	DeliveryGenerationActive   DeliveryGenerationStatus = "active"
	DeliveryGenerationRetired  DeliveryGenerationStatus = "retired"
)

type DeliveryRollbackClass string

const (
	DeliveryRollbackSafe  DeliveryRollbackClass = "rollback_safe"
	DeliveryServingSafe   DeliveryRollbackClass = "serving_safe"
	DeliveryNonReversible DeliveryRollbackClass = "non_reversible"
)

// DeliveryGeneration binds one exact candidate/catalog and is complete serving
// evidence. Activation changes lifecycle state only; it never mutates the
// catalog artifact.
type DeliveryGeneration struct {
	ID               string           `json:"id"`
	CandidateID      string           `json:"candidateId"`
	PlanID           string           `json:"planId"`
	PlanDigest       string           `json:"planDigest"`
	TargetID         string           `json:"targetId"`
	ProjectID        graph.ResourceID `json:"projectId"`
	Environment      string           `json:"environment"`
	CatalogDigest    string           `json:"catalogDigest"`
	CatalogObjectKey string           `json:"catalogObjectKey"`
	PhysicalPoolID   string           `json:"physicalPoolId"`
	// ServingArtifactID and ServingArtifactDigest are the exact immutable
	// serving artifact bound to this generation. Publication must carry the
	// candidate values through unchanged.
	ServingArtifactID       string                   `json:"servingArtifactId"`
	ServingArtifactDigest   string                   `json:"servingArtifactDigest"`
	ServingStateID          string                   `json:"servingStateId,omitempty"`
	CompatibilityDigest     string                   `json:"compatibilityDigest,omitempty"`
	RollbackClass           DeliveryRollbackClass    `json:"rollbackClass"`
	RollbackExternalEffects []string                 `json:"rollbackExternalEffects,omitempty"`
	Status                  DeliveryGenerationStatus `json:"status"`
	CreatedAt               time.Time                `json:"createdAt"`
	ActivatedAt             time.Time                `json:"activatedAt"`
	RetiredAt               time.Time                `json:"retiredAt"`
	RollbackUntil           time.Time                `json:"rollbackUntil"`
}

func (generation DeliveryGeneration) Validate() error {
	for name, value := range map[string]string{
		"generation": generation.ID, "candidate": generation.CandidateID, "plan": generation.PlanID,
		"target": generation.TargetID, "pool": generation.PhysicalPoolID,
	} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if err := validateDeliveryScope(generation.ProjectID, generation.Environment); err != nil {
		return err
	}
	for name, value := range map[string]string{"plan": generation.PlanDigest, "catalog": generation.CatalogDigest} {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if err := validateCatalogObjectKey("generation object key", generation.CatalogObjectKey); err != nil {
		return err
	}
	if err := ValidateDeliveryID(generation.ServingStateID); err != nil {
		return fmt.Errorf("serving state id: %w", err)
	}
	if err := ValidateDeliveryDigest(generation.CompatibilityDigest); err != nil {
		return fmt.Errorf("compatibility digest: %w", err)
	}
	for _, effect := range generation.RollbackExternalEffects {
		if effect == "" || effect != strings.TrimSpace(effect) || strings.ContainsAny(effect, "\r\n") {
			return fmt.Errorf("%w: rollback external effect is not canonical", ErrDeliveryInvalid)
		}
	}
	switch generation.RollbackClass {
	case DeliveryRollbackSafe, DeliveryServingSafe, DeliveryNonReversible:
	default:
		return fmt.Errorf("%w: unsupported rollback class %q", ErrDeliveryInvalid, generation.RollbackClass)
	}
	switch generation.Status {
	case DeliveryGenerationPrepared:
		if err := ValidateDeliveryID(generation.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryDigest(generation.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
		if !generation.ActivatedAt.IsZero() || !generation.RetiredAt.IsZero() {
			return fmt.Errorf("%w: prepared generation contains lifecycle timestamps", ErrDeliveryInvalid)
		}
	case DeliveryGenerationActive:
		if err := ValidateDeliveryID(generation.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryDigest(generation.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
		if err := validateDeliveryTime("generation activated at", generation.ActivatedAt, true); err != nil {
			return err
		}
		if generation.ActivatedAt.Before(generation.CreatedAt) || !generation.RetiredAt.IsZero() {
			return fmt.Errorf("%w: active generation timestamps are incoherent", ErrDeliveryInvalid)
		}
	case DeliveryGenerationRetired:
		if err := ValidateDeliveryID(generation.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryDigest(generation.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
		if err := validateDeliveryTime("generation retired at", generation.RetiredAt, true); err != nil {
			return err
		}
		if generation.ActivatedAt.Before(generation.CreatedAt) || generation.RetiredAt.Before(generation.ActivatedAt) {
			return fmt.Errorf("%w: retired generation timestamps are incoherent", ErrDeliveryInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported generation status %q", ErrDeliveryInvalid, generation.Status)
	}
	if err := validateDeliveryTime("generation created at", generation.CreatedAt, true); err != nil {
		return err
	}
	if !generation.RollbackUntil.IsZero() && generation.RollbackUntil.Before(generation.CreatedAt) {
		return fmt.Errorf("%w: rollback window precedes generation", ErrDeliveryInvalid)
	}
	return nil
}

func NewDeliveryGeneration(generation DeliveryGeneration) (DeliveryGeneration, error) {
	if err := validateDeliveryTime("generation created at", generation.CreatedAt, true); err != nil {
		return DeliveryGeneration{}, err
	}
	generation.Status = DeliveryGenerationPrepared
	generation.RollbackExternalEffects = append([]string(nil), generation.RollbackExternalEffects...)
	if generation.RollbackExternalEffects == nil {
		generation.RollbackExternalEffects = []string{}
	}
	sort.Strings(generation.RollbackExternalEffects)
	generation.CreatedAt = generation.CreatedAt.UTC()
	generation.ActivatedAt, generation.RetiredAt = time.Time{}, time.Time{}
	if err := generation.Validate(); err != nil {
		return DeliveryGeneration{}, err
	}
	return generation, nil
}

func (generation DeliveryGeneration) Activate(now time.Time) (DeliveryGeneration, error) {
	if err := generation.Validate(); err != nil {
		return DeliveryGeneration{}, err
	}
	if generation.Status == DeliveryGenerationActive {
		return generation, nil
	}
	if generation.Status != DeliveryGenerationPrepared {
		return DeliveryGeneration{}, fmt.Errorf("%w: only prepared generations may activate", ErrDeliveryTransition)
	}
	if err := validateDeliveryTime("generation activation time", now, true); err != nil {
		return DeliveryGeneration{}, err
	}
	if now.Before(generation.CreatedAt) {
		return DeliveryGeneration{}, fmt.Errorf("%w: activation time regressed", ErrDeliveryInvalid)
	}
	generation.Status, generation.ActivatedAt = DeliveryGenerationActive, now.UTC()
	return generation, nil
}

func (generation DeliveryGeneration) Retire(now time.Time) (DeliveryGeneration, error) {
	if err := generation.Validate(); err != nil {
		return DeliveryGeneration{}, err
	}
	if generation.Status == DeliveryGenerationRetired {
		return generation, nil
	}
	if generation.Status != DeliveryGenerationActive {
		return DeliveryGeneration{}, fmt.Errorf("%w: only active generations may retire", ErrDeliveryTransition)
	}
	if err := validateDeliveryTime("generation retirement time", now, true); err != nil {
		return DeliveryGeneration{}, err
	}
	if now.Before(generation.ActivatedAt) {
		return DeliveryGeneration{}, fmt.Errorf("%w: retirement time regressed", ErrDeliveryInvalid)
	}
	generation.Status, generation.RetiredAt = DeliveryGenerationRetired, now.UTC()
	return generation, nil
}

// Rollback re-selects a retained generation without rebuilding or mutating its
// catalog. The control transaction that invokes this transition must fence the
// target revision separately.
func (generation DeliveryGeneration) Rollback(now time.Time) (DeliveryGeneration, error) {
	if err := generation.Validate(); err != nil {
		return DeliveryGeneration{}, err
	}
	if generation.Status != DeliveryGenerationRetired {
		return DeliveryGeneration{}, fmt.Errorf("%w: only retired generations may be selected for rollback", ErrDeliveryTransition)
	}
	if err := validateDeliveryTime("generation rollback time", now, true); err != nil {
		return DeliveryGeneration{}, err
	}
	if generation.RollbackUntil.IsZero() || now.UTC().After(generation.RollbackUntil) {
		return DeliveryGeneration{}, fmt.Errorf("%w: generation rollback window has expired", ErrDeliveryStale)
	}
	generation.Status, generation.ActivatedAt, generation.RetiredAt = DeliveryGenerationActive, now.UTC(), time.Time{}
	return generation, nil
}

type DeliveryPublicationStatus string

const (
	DeliveryPublicationPending       DeliveryPublicationStatus = "pending"
	DeliveryPublicationCommitted     DeliveryPublicationStatus = "committed"
	DeliveryPublicationRejected      DeliveryPublicationStatus = "rejected"
	DeliveryPublicationIndeterminate DeliveryPublicationStatus = "indeterminate"
)

type RefreshPublicationFence struct {
	RunID          string
	LeaseOwner     string
	LeaseRevision  int64
	TargetRevision int64
}

func (fence RefreshPublicationFence) Validate() error {
	if err := ValidateDeliveryID(fence.RunID); err != nil {
		return fmt.Errorf("refresh run id: %w", err)
	}
	if fence.LeaseOwner == "" || fence.LeaseOwner != strings.TrimSpace(fence.LeaseOwner) {
		return fmt.Errorf("%w: refresh lease owner is not canonical", ErrDeliveryInvalid)
	}
	if fence.LeaseRevision <= 0 || fence.TargetRevision <= 0 {
		return fmt.Errorf("%w: refresh publication revisions must be positive", ErrDeliveryInvalid)
	}
	return nil
}

// DeliveryPublication records the exact candidate and both sides of the
// target CAS. Publication never rebuilds or mutates a candidate.
type DeliveryPublication struct {
	ID string `json:"id"`
	// ActorID is authenticated command evidence retained in the event ledger;
	// it is not part of publication CAS identity.
	ActorID                  string           `json:"actorId,omitempty"`
	RequestDigest            string           `json:"requestDigest"`
	TargetID                 string           `json:"targetId"`
	ProjectID                graph.ResourceID `json:"projectId"`
	Environment              string           `json:"environment"`
	PlanID                   string           `json:"planId"`
	PlanDigest               string           `json:"planDigest"`
	CandidateID              string           `json:"candidateId"`
	GenerationID             string           `json:"generationId"`
	ExpectedBaseGenerationID string           `json:"expectedBaseGenerationId,omitempty"`
	ExpectedTargetRevision   int64            `json:"expectedTargetRevision"`
	// Refresh publication authority is optional for ordinary deployments. A
	// canonical refresh persists the exact worker lease on the publication so
	// the delivery target CAS can reject superseded or reclaimed work atomically.
	RefreshRunID          string                    `json:"-"`
	RefreshLeaseOwner     string                    `json:"-"`
	RefreshLeaseRevision  int64                     `json:"-"`
	RefreshTargetRevision int64                     `json:"-"`
	ResultTargetRevision  int64                     `json:"resultTargetRevision"`
	Status                DeliveryPublicationStatus `json:"status"`
	Reason                string                    `json:"reason,omitempty"`
	CreatedAt             time.Time                 `json:"createdAt"`
	CompletedAt           time.Time                 `json:"completedAt"`
}

func (publication DeliveryPublication) Validate() error {
	if publication.ActorID != "" {
		if err := ValidateDeliveryID(publication.ActorID); err != nil {
			return fmt.Errorf("publication actor id: %w", err)
		}
	}
	for name, value := range map[string]string{
		"publication": publication.ID, "request": publication.RequestDigest, "target": publication.TargetID,
		"plan": publication.PlanID, "candidate": publication.CandidateID, "generation": publication.GenerationID,
	} {
		if name == "request" {
			if err := ValidateDeliveryDigest(value); err != nil {
				return fmt.Errorf("request digest: %w", err)
			}
			continue
		}
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if err := validateDeliveryScope(publication.ProjectID, publication.Environment); err != nil {
		return err
	}
	if err := ValidateDeliveryDigest(publication.PlanDigest); err != nil {
		return fmt.Errorf("plan digest: %w", err)
	}
	if publication.ExpectedBaseGenerationID != "" {
		if err := ValidateDeliveryID(publication.ExpectedBaseGenerationID); err != nil {
			return fmt.Errorf("expected base generation: %w", err)
		}
	}
	if publication.ExpectedTargetRevision < 0 || publication.ResultTargetRevision < 0 {
		return fmt.Errorf("%w: target revision cannot be negative", ErrDeliveryInvalid)
	}
	hasRefreshFence := publication.RefreshRunID != "" || publication.RefreshLeaseOwner != "" || publication.RefreshLeaseRevision != 0 || publication.RefreshTargetRevision != 0
	if hasRefreshFence {
		if err := (RefreshPublicationFence{
			RunID: publication.RefreshRunID, LeaseOwner: publication.RefreshLeaseOwner,
			LeaseRevision: publication.RefreshLeaseRevision, TargetRevision: publication.RefreshTargetRevision,
		}).Validate(); err != nil {
			return err
		}
	}
	if publication.Status == DeliveryPublicationCommitted && publication.ResultTargetRevision != publication.ExpectedTargetRevision+1 {
		return fmt.Errorf("%w: committed publication must advance target revision exactly once", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("publication created at", publication.CreatedAt, true); err != nil {
		return err
	}
	if publication.Status != DeliveryPublicationPending {
		if err := validateDeliveryTime("publication completed at", publication.CompletedAt, true); err != nil {
			return err
		}
	}
	switch publication.Status {
	case DeliveryPublicationPending:
		if !publication.CompletedAt.IsZero() || publication.ResultTargetRevision != 0 || publication.Reason != "" {
			return fmt.Errorf("%w: pending publication contains terminal evidence", ErrDeliveryInvalid)
		}
	case DeliveryPublicationIndeterminate:
		if publication.CompletedAt.IsZero() || publication.ResultTargetRevision != 0 {
			return fmt.Errorf("%w: indeterminate publication evidence is incoherent", ErrDeliveryInvalid)
		}
	case DeliveryPublicationCommitted:
		if publication.CompletedAt.IsZero() || publication.ResultTargetRevision != publication.ExpectedTargetRevision+1 || publication.Reason != "" {
			return fmt.Errorf("%w: committed publication evidence is incoherent", ErrDeliveryInvalid)
		}
	case DeliveryPublicationRejected:
		if publication.CompletedAt.IsZero() || publication.ResultTargetRevision != 0 || publication.Reason == "" {
			return fmt.Errorf("%w: rejected publication evidence is incoherent", ErrDeliveryInvalid)
		}
	}
	switch publication.Status {
	case DeliveryPublicationPending, DeliveryPublicationCommitted, DeliveryPublicationRejected, DeliveryPublicationIndeterminate:
	default:
		return fmt.Errorf("%w: unsupported publication status %q", ErrDeliveryInvalid, publication.Status)
	}
	return nil
}

func NewDeliveryPublication(publication DeliveryPublication) (DeliveryPublication, error) {
	if err := validateDeliveryTime("publication created at", publication.CreatedAt, true); err != nil {
		return DeliveryPublication{}, err
	}
	publication.Status = DeliveryPublicationPending
	publication.CreatedAt = publication.CreatedAt.UTC()
	publication.CompletedAt = time.Time{}
	publication.ResultTargetRevision = 0
	publication.Reason = ""
	if err := publication.Validate(); err != nil {
		return DeliveryPublication{}, err
	}
	return publication, nil
}

func (publication DeliveryPublication) Commit(activeGenerationID string, targetRevision int64, now time.Time) (DeliveryPublication, error) {
	if err := publication.Validate(); err != nil {
		return DeliveryPublication{}, err
	}
	if publication.Status == DeliveryPublicationCommitted {
		return publication, nil
	}
	if publication.Status != DeliveryPublicationPending && publication.Status != DeliveryPublicationIndeterminate {
		return DeliveryPublication{}, fmt.Errorf("%w: publication is %s", ErrDeliveryTransition, publication.Status)
	}
	if activeGenerationID != publication.ExpectedBaseGenerationID || targetRevision != publication.ExpectedTargetRevision {
		return DeliveryPublication{}, fmt.Errorf("%w: publication CAS fence changed", ErrDeliveryStale)
	}
	if err := validateDeliveryTime("publication completion time", now, true); err != nil {
		return DeliveryPublication{}, err
	}
	if now.Before(publication.CreatedAt) {
		return DeliveryPublication{}, fmt.Errorf("%w: publication completion time regressed", ErrDeliveryInvalid)
	}
	publication.Status, publication.CompletedAt = DeliveryPublicationCommitted, now.UTC()
	publication.ResultTargetRevision = targetRevision + 1
	return publication, publication.Validate()
}

func (publication DeliveryPublication) Reject(reason string, now time.Time) (DeliveryPublication, error) {
	if reason == "" || reason != trim(reason) {
		return DeliveryPublication{}, fmt.Errorf("%w: publication rejection reason is required", ErrDeliveryInvalid)
	}
	if err := publication.Validate(); err != nil {
		return DeliveryPublication{}, err
	}
	if publication.Status == DeliveryPublicationRejected && publication.Reason == reason {
		return publication, nil
	}
	if publication.Status != DeliveryPublicationPending && publication.Status != DeliveryPublicationIndeterminate {
		return DeliveryPublication{}, fmt.Errorf("%w: publication is %s", ErrDeliveryTransition, publication.Status)
	}
	if err := validateDeliveryTime("publication completion time", now, true); err != nil {
		return DeliveryPublication{}, err
	}
	if now.Before(publication.CreatedAt) {
		return DeliveryPublication{}, fmt.Errorf("%w: publication completion time regressed", ErrDeliveryInvalid)
	}
	publication.Status, publication.Reason, publication.CompletedAt = DeliveryPublicationRejected, reason, now.UTC()
	return publication, nil
}

func (publication DeliveryPublication) MarkIndeterminate(now time.Time) (DeliveryPublication, error) {
	if err := publication.Validate(); err != nil {
		return DeliveryPublication{}, err
	}
	if publication.Status == DeliveryPublicationIndeterminate {
		return publication, nil
	}
	if publication.Status != DeliveryPublicationPending {
		return DeliveryPublication{}, fmt.Errorf("%w: publication is %s", ErrDeliveryTransition, publication.Status)
	}
	if err := validateDeliveryTime("publication indeterminate time", now, true); err != nil {
		return DeliveryPublication{}, err
	}
	if now.Before(publication.CreatedAt) {
		return DeliveryPublication{}, fmt.Errorf("%w: publication completion time regressed", ErrDeliveryInvalid)
	}
	publication.Status, publication.CompletedAt = DeliveryPublicationIndeterminate, now.UTC()
	return publication, nil
}
