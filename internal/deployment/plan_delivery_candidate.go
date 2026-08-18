package deployment

import (
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/project/graph"
)

type DeliveryCandidateStatus string

const (
	DeliveryCandidatePreparing DeliveryCandidateStatus = "preparing"
	DeliveryCandidateReady     DeliveryCandidateStatus = "ready"
	DeliveryCandidateFailed    DeliveryCandidateStatus = "failed"
	DeliveryCandidateRetired   DeliveryCandidateStatus = "retired"
)

// DeliveryCandidate is the immutable, target-private result of a successful
// catalog seal. Staleness is derived from the plan fence; it is not a mutable
// candidate revision or a second lifecycle object.
type DeliveryCandidate struct {
	ID                  string           `json:"id"`
	PlanID              string           `json:"planId"`
	PlanDigest          string           `json:"planDigest"`
	TargetID            string           `json:"targetId"`
	ProjectID           graph.ResourceID `json:"projectId"`
	Environment         string           `json:"environment"`
	SourceDigest        string           `json:"sourceDigest"`
	ExecutionDigest     string           `json:"executionDigest"`
	BaseGenerationID    string           `json:"baseGenerationId,omitempty"`
	BaseTargetRevision  int64            `json:"baseTargetRevision"`
	SealID              string           `json:"sealId"`
	CatalogDigest       string           `json:"catalogDigest"`
	BaseCatalogDigest   string           `json:"baseCatalogDigest,omitempty"`
	BasePhysicalPoolID  string           `json:"basePhysicalPoolId,omitempty"`
	CompatibilityDigest string           `json:"compatibilityDigest"`
	CatalogObjectKey    string           `json:"catalogObjectKey"`
	PhysicalPoolID      string           `json:"physicalPoolId"`
	// ServingArtifactID and ServingArtifactDigest bind the candidate to the
	// exact compiled serving artifact that produced the sealed catalog. They
	// are immutable evidence, not a lookup hint or a mutable deployment
	// pointer.
	ServingArtifactID     string                      `json:"servingArtifactId"`
	ServingArtifactDigest string                      `json:"servingArtifactDigest"`
	ServingStateID        string                      `json:"servingStateId,omitempty"`
	QualificationDigest   string                      `json:"qualificationDigest"`
	ResolvedInputs        DeliveryResolvedBuildInputs `json:"resolvedInputs"`
	Status                DeliveryCandidateStatus     `json:"status"`
	FailureCode           string                      `json:"failureCode,omitempty"`
	CreatedAt             time.Time                   `json:"createdAt"`
	ReadyAt               time.Time                   `json:"readyAt"`
	RetiredAt             time.Time                   `json:"retiredAt"`
}

func (candidate DeliveryCandidate) Validate() error {
	for name, value := range map[string]string{
		"candidate": candidate.ID, "plan": candidate.PlanID, "target": candidate.TargetID,
		"seal": candidate.SealID, "pool": candidate.PhysicalPoolID,
	} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if err := validateDeliveryScope(candidate.ProjectID, candidate.Environment); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"plan": candidate.PlanDigest, "source": candidate.SourceDigest, "execution": candidate.ExecutionDigest,
		"catalog": candidate.CatalogDigest, "compatibility": candidate.CompatibilityDigest,
	} {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if candidate.Status == DeliveryCandidateReady {
		if err := ValidateDeliveryID(candidate.ServingStateID); err != nil {
			return fmt.Errorf("serving state id: %w", err)
		}
		if err := ValidateDeliveryID(candidate.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryDigest(candidate.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
		if err := ValidateDeliveryDigest(candidate.QualificationDigest); err != nil {
			return fmt.Errorf("qualification digest: %w", err)
		}
		if err := candidate.ResolvedInputs.Validate(); err != nil {
			return fmt.Errorf("resolved inputs: %w", err)
		}
		if candidate.ResolvedInputs.EvidenceDigest != "" {
			digest, err := candidate.ResolvedInputs.Digest()
			if err != nil || digest != candidate.ResolvedInputs.EvidenceDigest {
				return fmt.Errorf("%w: resolved input evidence digest does not match canonical inputs", ErrDeliveryConflict)
			}
		}
	}
	if candidate.Status == DeliveryCandidateRetired {
		if err := ValidateDeliveryID(candidate.ServingStateID); err != nil {
			return fmt.Errorf("serving state id: %w", err)
		}
		if err := ValidateDeliveryID(candidate.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryDigest(candidate.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
	}
	if err := validateCatalogObjectKey("candidate catalog object key", candidate.CatalogObjectKey); err != nil {
		return err
	}
	if candidate.BaseGenerationID != "" {
		if err := ValidateDeliveryID(candidate.BaseGenerationID); err != nil {
			return fmt.Errorf("base generation: %w", err)
		}
	} else if candidate.BaseTargetRevision != 0 {
		return fmt.Errorf("%w: base target revision requires a base generation", ErrDeliveryInvalid)
	}
	if (candidate.BaseCatalogDigest == "") != (candidate.BasePhysicalPoolID == "") {
		return fmt.Errorf("%w: base catalog and base physical pool must be supplied together", ErrDeliveryInvalid)
	}
	if candidate.BaseCatalogDigest != "" {
		if err := ValidateDeliveryDigest(candidate.BaseCatalogDigest); err != nil {
			return fmt.Errorf("base catalog digest: %w", err)
		}
		if err := ValidateDeliveryID(candidate.BasePhysicalPoolID); err != nil {
			return fmt.Errorf("base physical pool: %w", err)
		}
		if candidate.BasePhysicalPoolID != candidate.PhysicalPoolID {
			return fmt.Errorf("%w: base catalog reuse cannot cross physical pools", ErrDeliveryConflict)
		}
	}
	if candidate.BaseTargetRevision < 0 {
		return fmt.Errorf("%w: base target revision cannot be negative", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("candidate created at", candidate.CreatedAt, true); err != nil {
		return err
	}
	if candidate.Status == DeliveryCandidateReady {
		if err := validateDeliveryTime("candidate ready at", candidate.ReadyAt, true); err != nil {
			return err
		}
		if candidate.ReadyAt.Before(candidate.CreatedAt) || candidate.FailureCode != "" {
			return fmt.Errorf("%w: ready candidate evidence is incoherent", ErrDeliveryInvalid)
		}
	}
	if candidate.Status == DeliveryCandidatePreparing && (candidate.QualificationDigest != "" || !candidate.ReadyAt.IsZero() || !candidate.RetiredAt.IsZero() || candidate.FailureCode != "") {
		return fmt.Errorf("%w: preparing candidate contains terminal evidence", ErrDeliveryInvalid)
	}
	if candidate.Status == DeliveryCandidateFailed && (!candidate.ReadyAt.IsZero() || !candidate.RetiredAt.IsZero() || candidate.QualificationDigest != "" || !canonicalFailureCode(candidate.FailureCode)) {
		return fmt.Errorf("%w: failed candidate evidence is incoherent", ErrDeliveryInvalid)
	}
	if candidate.Status == DeliveryCandidateRetired && candidate.FailureCode != "" {
		return fmt.Errorf("%w: retired candidate contains failure evidence", ErrDeliveryInvalid)
	}
	if candidate.Status == DeliveryCandidateRetired {
		if err := validateDeliveryTime("candidate retired at", candidate.RetiredAt, true); err != nil {
			return err
		}
		if candidate.RetiredAt.Before(candidate.CreatedAt) || candidate.RetiredAt.Before(candidate.ReadyAt) {
			return fmt.Errorf("%w: candidate retirement precedes readiness", ErrDeliveryInvalid)
		}
	}
	if candidate.Status == DeliveryCandidateFailed && !canonicalFailureCode(candidate.FailureCode) {
		return fmt.Errorf("%w: candidate failure code is invalid", ErrDeliveryInvalid)
	}
	switch candidate.Status {
	case DeliveryCandidatePreparing, DeliveryCandidateReady, DeliveryCandidateFailed, DeliveryCandidateRetired:
	default:
		return fmt.Errorf("%w: unsupported candidate status %q", ErrDeliveryInvalid, candidate.Status)
	}
	return nil
}

func trim(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\n' || value[0] == '\r') {
		value = value[1:]
	}
	for len(value) > 0 && (value[len(value)-1] == ' ' || value[len(value)-1] == '\t' || value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}

func NewDeliveryCandidate(candidate DeliveryCandidate) (DeliveryCandidate, error) {
	if err := validateDeliveryTime("candidate created at", candidate.CreatedAt, true); err != nil {
		return DeliveryCandidate{}, err
	}
	candidate.Status = DeliveryCandidatePreparing
	var resolvedErr error
	candidate.ResolvedInputs, resolvedErr = NewDeliveryResolvedBuildInputs(candidate.ResolvedInputs)
	if resolvedErr != nil {
		return DeliveryCandidate{}, resolvedErr
	}
	candidate.ReadyAt, candidate.RetiredAt, candidate.FailureCode = time.Time{}, time.Time{}, ""
	if err := candidate.Validate(); err != nil {
		return DeliveryCandidate{}, err
	}
	return candidate, nil
}

func NewCandidateRecord(candidate DeliveryCandidate) (DeliveryCandidate, error) {
	return NewDeliveryCandidate(candidate)
}

func (candidate DeliveryCandidate) MarkReady(seal CatalogSeal, now time.Time) (DeliveryCandidate, error) {
	if err := candidate.Validate(); err != nil {
		return DeliveryCandidate{}, err
	}
	if err := seal.Validate(); err != nil {
		return DeliveryCandidate{}, err
	}
	if seal.Status != CatalogSealVerified {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate requires a verified catalog seal", ErrDeliveryTransition)
	}
	if candidate.Status == DeliveryCandidateReady {
		if candidate.SealID == seal.ID && candidate.CatalogDigest == seal.CatalogDigest && candidate.CompatibilityDigest == seal.CompatibilityDigest && candidate.ServingArtifactID == seal.ServingArtifactID && candidate.ServingArtifactDigest == seal.ServingArtifactDigest && candidate.ServingStateID == seal.ServingStateID && candidate.QualificationDigest == seal.QualificationDigest {
			return candidate, nil
		}
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate seal changed", ErrDeliveryConflict)
	}
	if candidate.Status != DeliveryCandidatePreparing {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate is %s", ErrDeliveryTransition, candidate.Status)
	}
	if candidate.SealID != seal.ID || candidate.CatalogDigest != seal.CatalogDigest || candidate.ServingArtifactID != seal.ServingArtifactID || candidate.ServingArtifactDigest != seal.ServingArtifactDigest || candidate.ServingStateID != seal.ServingStateID || candidate.PhysicalPoolID != seal.PhysicalPoolID || candidate.CompatibilityDigest != seal.CompatibilityDigest || candidate.BaseCatalogDigest != seal.BaseCatalogDigest || candidate.BasePhysicalPoolID != seal.BasePhysicalPoolID {
		return DeliveryCandidate{}, fmt.Errorf("%w: seal does not match candidate binding", ErrDeliveryConflict)
	}
	if err := validateDeliveryTime("candidate ready time", now, true); err != nil {
		return DeliveryCandidate{}, err
	}
	if now.Before(candidate.CreatedAt) {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate ready time regressed", ErrDeliveryInvalid)
	}
	candidate.QualificationDigest = seal.QualificationDigest
	candidate.Status, candidate.ReadyAt = DeliveryCandidateReady, now.UTC()
	return candidate, candidate.Validate()
}

func (candidate DeliveryCandidate) MarkFailed(code string) (DeliveryCandidate, error) {
	if !canonicalFailureCode(code) {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate failure code is invalid", ErrDeliveryInvalid)
	}
	if err := candidate.Validate(); err != nil {
		return DeliveryCandidate{}, err
	}
	if candidate.Status == DeliveryCandidateReady {
		return DeliveryCandidate{}, fmt.Errorf("%w: ready candidate cannot fail", ErrDeliveryConflict)
	}
	if candidate.Status == DeliveryCandidateFailed && candidate.FailureCode == code {
		return candidate, nil
	}
	candidate.Status, candidate.FailureCode = DeliveryCandidateFailed, code
	return candidate, nil
}

func (candidate DeliveryCandidate) Retire(now time.Time) (DeliveryCandidate, error) {
	if err := candidate.Validate(); err != nil {
		return DeliveryCandidate{}, err
	}
	if candidate.Status == DeliveryCandidateRetired {
		return candidate, nil
	}
	if candidate.Status != DeliveryCandidateReady {
		return DeliveryCandidate{}, fmt.Errorf("%w: only ready candidates may retire", ErrDeliveryTransition)
	}
	if err := validateDeliveryTime("candidate retirement time", now, true); err != nil {
		return DeliveryCandidate{}, err
	}
	if now.Before(candidate.ReadyAt) {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate retirement time regressed", ErrDeliveryInvalid)
	}
	candidate.Status, candidate.RetiredAt = DeliveryCandidateRetired, now.UTC()
	return candidate, nil
}

// Stale is derived from the exact plan fence and is monotonic because target
// revisions are monotonic.
func (candidate DeliveryCandidate) Stale(activeGenerationID string, targetRevision int64) bool {
	return activeGenerationID != candidate.BaseGenerationID || targetRevision != candidate.BaseTargetRevision
}

func (candidate DeliveryCandidate) PublicationEligible(plan DeliveryPlan, activeGenerationID string, targetRevision int64, now time.Time) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if candidate.Status != DeliveryCandidateReady {
		return fmt.Errorf("%w: candidate is not ready", ErrDeliveryStale)
	}
	if candidate.PlanID != plan.ID || candidate.PlanDigest != plan.Digest {
		return fmt.Errorf("%w: candidate is bound to a different plan", ErrDeliveryConflict)
	}
	if candidate.TargetID != plan.TargetID || candidate.ProjectID != plan.ProjectID || candidate.Environment != plan.Environment {
		return fmt.Errorf("%w: candidate scope differs from plan scope", ErrDeliveryConflict)
	}
	if err := plan.PublicationEligible(activeGenerationID, targetRevision, now); err != nil {
		return err
	}
	if candidate.Stale(activeGenerationID, targetRevision) {
		return fmt.Errorf("%w: candidate base changed", ErrDeliveryStale)
	}
	return nil
}
