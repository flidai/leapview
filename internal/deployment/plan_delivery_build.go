package deployment

import (
	"fmt"
	"strings"
	"time"
)

// DeliveryBuildAttemptStatus describes durable control state around disposable
// private work. No pre-seal status implies a queryable candidate.
type DeliveryBuildAttemptStatus string

const (
	DeliveryBuildBuilding    DeliveryBuildAttemptStatus = "building"
	DeliveryBuildNormalizing DeliveryBuildAttemptStatus = "normalizing"
	DeliveryBuildValidating  DeliveryBuildAttemptStatus = "validating"
	DeliveryBuildSealing     DeliveryBuildAttemptStatus = "sealing"
	DeliveryBuildSealed      DeliveryBuildAttemptStatus = "sealed"
	DeliveryBuildFailed      DeliveryBuildAttemptStatus = "failed"
	DeliveryBuildAbandoned   DeliveryBuildAttemptStatus = "abandoned"
)

// DeliveryBuildAttempt binds canonical plan/execution inputs and one exact
// base before physical work begins. The fields that identify work are never
// changed by transitions.
type DeliveryBuildAttempt struct {
	ID     string `json:"id"`
	PlanID string `json:"planId"`
	// IdempotencyKey is the caller's build operation key. It is bound with the
	// attempt in the same control-plane transaction and is intentionally
	// distinct from the plan-creation key.
	IdempotencyKey        string                     `json:"idempotencyKey,omitempty"`
	PlanDigest            string                     `json:"planDigest"`
	SourceDigest          string                     `json:"sourceDigest"`
	ExecutionDigest       string                     `json:"executionDigest"`
	BaseGenerationID      string                     `json:"baseGenerationId,omitempty"`
	BaseCatalogDigest     string                     `json:"baseCatalogDigest,omitempty"`
	BasePhysicalPoolID    string                     `json:"basePhysicalPoolId,omitempty"`
	PhysicalPoolID        string                     `json:"physicalPoolId"`
	WriterLeaseID         string                     `json:"writerLeaseId"`
	ServingArtifactID     string                     `json:"servingArtifactId,omitempty"`
	ServingArtifactDigest string                     `json:"servingArtifactDigest,omitempty"`
	ServingStateID        string                     `json:"servingStateId,omitempty"`
	Status                DeliveryBuildAttemptStatus `json:"status"`
	SealID                string                     `json:"sealId,omitempty"`
	CandidateID           string                     `json:"candidateId,omitempty"`
	QualifiedSnapshotID   int64                      `json:"qualifiedSnapshotId,omitempty"`
	FailureCode           string                     `json:"failureCode,omitempty"`
	CreatedAt             time.Time                  `json:"createdAt"`
	UpdatedAt             time.Time                  `json:"updatedAt"`
	TerminalAt            time.Time                  `json:"terminalAt"`
	Revision              int64                      `json:"revision"`
}

func (attempt DeliveryBuildAttempt) Validate() error {
	for name, value := range map[string]string{
		"build attempt": attempt.ID, "plan": attempt.PlanID, "physical pool": attempt.PhysicalPoolID,
		"writer lease": attempt.WriterLeaseID,
	} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	for name, value := range map[string]string{
		"plan": attempt.PlanDigest, "source": attempt.SourceDigest, "execution": attempt.ExecutionDigest,
	} {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if attempt.ServingArtifactID != "" || attempt.ServingArtifactDigest != "" || attempt.ServingStateID != "" {
		if err := ValidateDeliveryID(attempt.ServingArtifactID); err != nil {
			return fmt.Errorf("serving artifact id: %w", err)
		}
		if err := ValidateDeliveryDigest(attempt.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
		if err := ValidateDeliveryID(attempt.ServingStateID); err != nil {
			return fmt.Errorf("serving state id: %w", err)
		}
	}
	if attempt.BaseGenerationID != "" {
		if err := ValidateDeliveryID(attempt.BaseGenerationID); err != nil {
			return fmt.Errorf("base generation: %w", err)
		}
		// BaseGenerationID is always the publication fence captured by the
		// plan. The catalog/pool pair is optional: a build may fence against
		// the active generation while deliberately doing a full refresh rather
		// than retaining that generation's sealed catalog.
		if (attempt.BaseCatalogDigest == "") != (attempt.BasePhysicalPoolID == "") {
			return fmt.Errorf("%w: base catalog and base physical pool must be supplied together", ErrDeliveryInvalid)
		}
		if attempt.BaseCatalogDigest != "" {
			if err := ValidateDeliveryDigest(attempt.BaseCatalogDigest); err != nil {
				return fmt.Errorf("base catalog: %w", err)
			}
			if err := ValidateDeliveryID(attempt.BasePhysicalPoolID); err != nil {
				return fmt.Errorf("base physical pool: %w", err)
			}
			if attempt.BasePhysicalPoolID != attempt.PhysicalPoolID {
				return fmt.Errorf("%w: base catalog reuse cannot cross physical pools", ErrDeliveryConflict)
			}
		}
	} else if attempt.BaseCatalogDigest != "" || attempt.BasePhysicalPoolID != "" {
		return fmt.Errorf("%w: base catalog requires base generation", ErrDeliveryInvalid)
	}
	if attempt.Revision < 1 {
		return fmt.Errorf("%w: build revision must be positive", ErrDeliveryInvalid)
	}
	if attempt.QualifiedSnapshotID < 0 {
		return fmt.Errorf("%w: qualified snapshot must not be negative", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("build created at", attempt.CreatedAt, true); err != nil {
		return err
	}
	if err := validateDeliveryTime("build updated at", attempt.UpdatedAt, true); err != nil {
		return err
	}
	if attempt.UpdatedAt.Before(attempt.CreatedAt) {
		return fmt.Errorf("%w: build updated before creation", ErrDeliveryInvalid)
	}
	if attempt.Status == DeliveryBuildSealed {
		if err := ValidateDeliveryID(attempt.SealID); err != nil {
			return fmt.Errorf("sealed attempt seal: %w", err)
		}
		if err := ValidateDeliveryID(attempt.CandidateID); err != nil {
			return fmt.Errorf("sealed attempt candidate: %w", err)
		}
		if attempt.TerminalAt.IsZero() {
			return fmt.Errorf("%w: sealed attempt requires terminal evidence", ErrDeliveryInvalid)
		}
	} else if attempt.Status != DeliveryBuildFailed && attempt.Status != DeliveryBuildAbandoned && (!attempt.TerminalAt.IsZero() || attempt.FailureCode != "" || attempt.SealID != "" || attempt.CandidateID != "") {
		return fmt.Errorf("%w: nonterminal build contains terminal evidence", ErrDeliveryInvalid)
	}
	if attempt.Status == DeliveryBuildFailed || attempt.Status == DeliveryBuildAbandoned {
		if !canonicalFailureCode(attempt.FailureCode) {
			return fmt.Errorf("%w: terminal build failure code is invalid", ErrDeliveryInvalid)
		}
		if attempt.TerminalAt.IsZero() || attempt.TerminalAt.Before(attempt.CreatedAt) {
			return fmt.Errorf("%w: terminal build timestamp is invalid", ErrDeliveryInvalid)
		}
	}
	switch attempt.Status {
	case DeliveryBuildBuilding, DeliveryBuildNormalizing, DeliveryBuildValidating, DeliveryBuildSealing,
		DeliveryBuildSealed, DeliveryBuildFailed, DeliveryBuildAbandoned:
	default:
		return fmt.Errorf("%w: unsupported build status %q", ErrDeliveryInvalid, attempt.Status)
	}
	return nil
}

func NewDeliveryBuildAttempt(attempt DeliveryBuildAttempt) (DeliveryBuildAttempt, error) {
	if err := validateDeliveryTime("build created at", attempt.CreatedAt, true); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	attempt.Status = DeliveryBuildBuilding
	attempt.CreatedAt = attempt.CreatedAt.UTC()
	attempt.UpdatedAt = attempt.CreatedAt
	attempt.Revision = 1
	attempt.TerminalAt = time.Time{}
	attempt.SealID, attempt.CandidateID, attempt.FailureCode = "", "", ""
	if err := attempt.Validate(); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return attempt, nil
}

func NewBuildAttempt(attempt DeliveryBuildAttempt) (DeliveryBuildAttempt, error) {
	return NewDeliveryBuildAttempt(attempt)
}

// ValidateWriterLeaseBinding enforces the cross-row ownership invariant that
// SQLite also protects with the composite writer-lease foreign key.
func (attempt DeliveryBuildAttempt) ValidateWriterLeaseBinding(lease DeliveryWriterLease) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	if err := lease.Validate(); err != nil {
		return err
	}
	if lease.ID != attempt.WriterLeaseID || lease.AttemptID != attempt.ID || lease.PhysicalPoolID != attempt.PhysicalPoolID {
		return fmt.Errorf("%w: writer lease does not belong to build attempt and pool", ErrDeliveryConflict)
	}
	return nil
}

var deliveryBuildTransitions = map[DeliveryBuildAttemptStatus]map[DeliveryBuildAttemptStatus]struct{}{
	DeliveryBuildBuilding:    {DeliveryBuildNormalizing: {}, DeliveryBuildValidating: {}, DeliveryBuildFailed: {}, DeliveryBuildAbandoned: {}},
	DeliveryBuildNormalizing: {DeliveryBuildValidating: {}, DeliveryBuildFailed: {}, DeliveryBuildAbandoned: {}},
	DeliveryBuildValidating:  {DeliveryBuildSealing: {}, DeliveryBuildFailed: {}, DeliveryBuildAbandoned: {}},
	DeliveryBuildSealing:     {DeliveryBuildSealed: {}, DeliveryBuildFailed: {}, DeliveryBuildAbandoned: {}},
	DeliveryBuildSealed:      {}, DeliveryBuildFailed: {}, DeliveryBuildAbandoned: {},
}

func (attempt DeliveryBuildAttempt) Transition(next DeliveryBuildAttemptStatus, now time.Time) (DeliveryBuildAttempt, error) {
	if next == attempt.Status {
		if err := attempt.Validate(); err != nil {
			return DeliveryBuildAttempt{}, err
		}
		return attempt, nil
	}
	if _, ok := deliveryBuildTransitions[attempt.Status][next]; !ok {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: build %s -> %s", ErrDeliveryTransition, attempt.Status, next)
	}
	if err := validateDeliveryTime("build transition time", now, true); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if now.Before(attempt.UpdatedAt) {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: build transition time regressed", ErrDeliveryInvalid)
	}
	if (next == DeliveryBuildFailed || next == DeliveryBuildAbandoned) && !canonicalFailureCode(attempt.FailureCode) {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: terminal build transition requires failure evidence", ErrDeliveryInvalid)
	}
	if next == DeliveryBuildSealed && (attempt.SealID == "" || attempt.CandidateID == "") {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: sealed build transition requires seal and candidate identities", ErrDeliveryInvalid)
	}
	validationAttempt := attempt
	if next == DeliveryBuildFailed || next == DeliveryBuildAbandoned || next == DeliveryBuildSealed {
		validationAttempt.Status = next
		validationAttempt.TerminalAt = now.UTC()
	}
	if err := validationAttempt.Validate(); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	attempt.Status = next
	attempt.UpdatedAt = now.UTC()
	attempt.Revision++
	if next == DeliveryBuildFailed || next == DeliveryBuildAbandoned || next == DeliveryBuildSealed {
		attempt.TerminalAt = attempt.UpdatedAt
	}
	return attempt, nil
}

func (attempt DeliveryBuildAttempt) BeginNormalize(now time.Time) (DeliveryBuildAttempt, error) {
	return attempt.Transition(DeliveryBuildNormalizing, now)
}

func (attempt DeliveryBuildAttempt) BeginValidation(now time.Time) (DeliveryBuildAttempt, error) {
	return attempt.Transition(DeliveryBuildValidating, now)
}

func (attempt DeliveryBuildAttempt) PrepareSeal(now time.Time) (DeliveryBuildAttempt, error) {
	return attempt.Transition(DeliveryBuildSealing, now)
}

func (attempt DeliveryBuildAttempt) MarkFailed(code string, now time.Time) (DeliveryBuildAttempt, error) {
	if !canonicalFailureCode(code) {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: failure code must contain uppercase letters, digits, or underscores", ErrDeliveryInvalid)
	}
	attempt.FailureCode = code
	next, err := attempt.Transition(DeliveryBuildFailed, now)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return next, next.Validate()
}

func (attempt DeliveryBuildAttempt) Abandon(code string, now time.Time) (DeliveryBuildAttempt, error) {
	if !canonicalFailureCode(code) {
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: abandonment code is invalid", ErrDeliveryInvalid)
	}
	attempt.FailureCode = code
	next, err := attempt.Transition(DeliveryBuildAbandoned, now)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return next, next.Validate()
}

// SealCandidate binds the exact successful seal. Repeating an identical seal
// is idempotent; changing either identity is a conflict.
func (attempt DeliveryBuildAttempt) SealCandidate(sealID, candidateID string, now time.Time) (DeliveryBuildAttempt, error) {
	if err := ValidateDeliveryID(sealID); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if err := ValidateDeliveryID(candidateID); err != nil {
		return DeliveryBuildAttempt{}, err
	}
	if attempt.Status == DeliveryBuildSealed {
		if attempt.SealID == sealID && attempt.CandidateID == candidateID {
			return attempt, nil
		}
		return DeliveryBuildAttempt{}, fmt.Errorf("%w: build already sealed", ErrDeliveryConflict)
	}
	attempt.SealID, attempt.CandidateID = sealID, candidateID
	next, err := attempt.Transition(DeliveryBuildSealed, now)
	if err != nil {
		return DeliveryBuildAttempt{}, err
	}
	return next, next.Validate()
}

// CatalogSealStatus is the cross-store seal boundary. Before verified, no
// candidate is queryable.
type CatalogSealStatus string

const (
	CatalogSealPreparing CatalogSealStatus = "preparing"
	CatalogSealUploaded  CatalogSealStatus = "uploaded"
	CatalogSealVerified  CatalogSealStatus = "verified"
	CatalogSealFailed    CatalogSealStatus = "failed"
)

type CatalogSeal struct {
	ID                    string            `json:"id"`
	AttemptID             string            `json:"attemptId"`
	PlanID                string            `json:"planId"`
	PlanDigest            string            `json:"planDigest"`
	ExecutionDigest       string            `json:"executionDigest"`
	PhysicalPoolID        string            `json:"physicalPoolId"`
	CatalogDigest         string            `json:"catalogDigest"`
	BaseCatalogDigest     string            `json:"baseCatalogDigest,omitempty"`
	BasePhysicalPoolID    string            `json:"basePhysicalPoolId,omitempty"`
	CompatibilityDigest   string            `json:"compatibilityDigest"`
	ServingArtifactID     string            `json:"servingArtifactId"`
	ServingArtifactDigest string            `json:"servingArtifactDigest"`
	ServingStateID        string            `json:"servingStateId,omitempty"`
	ObjectKey             string            `json:"objectKey"`
	ObjectSize            int64             `json:"objectSize"`
	ClosureDigest         string            `json:"closureDigest,omitempty"`
	QualificationDigest   string            `json:"qualificationDigest,omitempty"`
	Status                CatalogSealStatus `json:"status"`
	CreatedAt             time.Time         `json:"createdAt"`
	VerifiedAt            time.Time         `json:"verifiedAt"`
	FailureCode           string            `json:"failureCode,omitempty"`
}

func (seal CatalogSeal) Validate() error {
	for name, value := range map[string]string{
		"seal": seal.ID, "attempt": seal.AttemptID, "plan": seal.PlanID, "pool": seal.PhysicalPoolID, "object key": seal.ObjectKey,
	} {
		if err := validateDeliveryText(name, value, true); err != nil {
			return err
		}
		if name == "object key" {
			if err := validateCatalogObjectKey(name, value); err != nil {
				return err
			}
		} else {
			if err := ValidateDeliveryID(value); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if err := ValidateDeliveryID(seal.ServingArtifactID); err != nil {
		return fmt.Errorf("serving artifact id: %w", err)
	}
	if err := ValidateDeliveryID(seal.ServingStateID); err != nil {
		return fmt.Errorf("serving state id: %w", err)
	}
	for name, value := range map[string]string{
		"plan": seal.PlanDigest, "execution": seal.ExecutionDigest, "catalog": seal.CatalogDigest,
		"compatibility": seal.CompatibilityDigest, "serving artifact": seal.ServingArtifactDigest,
	} {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if seal.ObjectSize <= 0 {
		return fmt.Errorf("%w: catalog object size must be positive", ErrDeliveryInvalid)
	}
	if (seal.BaseCatalogDigest == "") != (seal.BasePhysicalPoolID == "") {
		return fmt.Errorf("%w: base catalog and base physical pool must be supplied together", ErrDeliveryInvalid)
	}
	if seal.BaseCatalogDigest != "" {
		if err := ValidateDeliveryDigest(seal.BaseCatalogDigest); err != nil {
			return fmt.Errorf("base catalog digest: %w", err)
		}
		if err := ValidateDeliveryID(seal.BasePhysicalPoolID); err != nil {
			return fmt.Errorf("base physical pool: %w", err)
		}
		if seal.BasePhysicalPoolID != seal.PhysicalPoolID {
			return fmt.Errorf("%w: base catalog reuse cannot cross physical pools", ErrDeliveryConflict)
		}
	}
	if err := validateDeliveryTime("seal created at", seal.CreatedAt, true); err != nil {
		return err
	}
	switch seal.Status {
	case CatalogSealPreparing, CatalogSealUploaded:
		if seal.ClosureDigest != "" || seal.QualificationDigest != "" || !seal.VerifiedAt.IsZero() || seal.FailureCode != "" {
			return fmt.Errorf("%w: unverified seal contains terminal evidence", ErrDeliveryInvalid)
		}
	case CatalogSealVerified:
		for name, value := range map[string]string{"closure": seal.ClosureDigest, "qualification": seal.QualificationDigest} {
			if err := ValidateDeliveryDigest(value); err != nil {
				return fmt.Errorf("%s digest: %w", name, err)
			}
		}
		if err := validateDeliveryTime("seal verified at", seal.VerifiedAt, true); err != nil {
			return err
		}
		if seal.FailureCode != "" {
			return fmt.Errorf("%w: verified seal cannot contain failure evidence", ErrDeliveryInvalid)
		}
	case CatalogSealFailed:
		if seal.ClosureDigest != "" || seal.QualificationDigest != "" || !seal.VerifiedAt.IsZero() || !canonicalFailureCode(seal.FailureCode) {
			return fmt.Errorf("%w: failed seal evidence is incoherent", ErrDeliveryInvalid)
		}
	}
	if seal.Status == CatalogSealFailed && !canonicalFailureCode(seal.FailureCode) {
		return fmt.Errorf("%w: seal failure code is invalid", ErrDeliveryInvalid)
	}
	switch seal.Status {
	case CatalogSealPreparing, CatalogSealUploaded, CatalogSealVerified, CatalogSealFailed:
	default:
		return fmt.Errorf("%w: unsupported seal status %q", ErrDeliveryInvalid, seal.Status)
	}
	return nil
}

func NewCatalogSeal(seal CatalogSeal) (CatalogSeal, error) {
	if err := validateDeliveryTime("seal created at", seal.CreatedAt, true); err != nil {
		return CatalogSeal{}, err
	}
	seal.Status = CatalogSealPreparing
	seal.CreatedAt = seal.CreatedAt.UTC()
	seal.VerifiedAt = time.Time{}
	seal.ClosureDigest, seal.QualificationDigest, seal.FailureCode = "", "", ""
	if err := seal.Validate(); err != nil {
		return CatalogSeal{}, err
	}
	return seal, nil
}

func (seal CatalogSeal) ValidateBuildBinding(attempt DeliveryBuildAttempt) error {
	if err := seal.Validate(); err != nil {
		return err
	}
	if err := attempt.Validate(); err != nil {
		return err
	}
	if seal.AttemptID != attempt.ID || seal.PlanID != attempt.PlanID || seal.PlanDigest != attempt.PlanDigest || seal.ExecutionDigest != attempt.ExecutionDigest || seal.PhysicalPoolID != attempt.PhysicalPoolID || seal.BaseCatalogDigest != attempt.BaseCatalogDigest || seal.BasePhysicalPoolID != attempt.BasePhysicalPoolID {
		return fmt.Errorf("%w: catalog seal does not match build attempt binding", ErrDeliveryConflict)
	}
	return nil
}

func (seal CatalogSeal) MarkUploaded() (CatalogSeal, error) {
	if err := seal.Validate(); err != nil {
		return CatalogSeal{}, err
	}
	switch seal.Status {
	case CatalogSealPreparing:
		seal.Status = CatalogSealUploaded
		return seal, nil
	case CatalogSealUploaded:
		return seal, nil
	default:
		return CatalogSeal{}, fmt.Errorf("%w: seal is %s", ErrDeliveryTransition, seal.Status)
	}
}

func (seal CatalogSeal) MarkVerified(closureDigest, qualificationDigest string, now time.Time) (CatalogSeal, error) {
	if err := ValidateDeliveryDigest(closureDigest); err != nil {
		return CatalogSeal{}, err
	}
	if err := ValidateDeliveryDigest(qualificationDigest); err != nil {
		return CatalogSeal{}, err
	}
	if err := validateDeliveryTime("seal verification time", now, true); err != nil {
		return CatalogSeal{}, err
	}
	if err := seal.Validate(); err != nil {
		return CatalogSeal{}, err
	}
	if now.Before(seal.CreatedAt) {
		return CatalogSeal{}, fmt.Errorf("%w: seal verification time regressed", ErrDeliveryInvalid)
	}
	if seal.Status == CatalogSealVerified {
		if seal.ClosureDigest == closureDigest && seal.QualificationDigest == qualificationDigest {
			return seal, nil
		}
		return CatalogSeal{}, fmt.Errorf("%w: seal verification evidence changed", ErrDeliveryConflict)
	}
	if seal.Status != CatalogSealUploaded {
		return CatalogSeal{}, fmt.Errorf("%w: seal is %s", ErrDeliveryTransition, seal.Status)
	}
	seal.Status = CatalogSealVerified
	seal.ClosureDigest, seal.QualificationDigest, seal.VerifiedAt = closureDigest, qualificationDigest, now.UTC()
	return seal, seal.Validate()
}

func (seal CatalogSeal) MarkFailed(code string) (CatalogSeal, error) {
	if !canonicalFailureCode(code) {
		return CatalogSeal{}, fmt.Errorf("%w: seal failure code is invalid", ErrDeliveryInvalid)
	}
	if err := seal.Validate(); err != nil {
		return CatalogSeal{}, err
	}
	if seal.Status == CatalogSealFailed && seal.FailureCode == code {
		return seal, nil
	}
	if seal.Status == CatalogSealVerified {
		return CatalogSeal{}, fmt.Errorf("%w: verified seal cannot fail", ErrDeliveryConflict)
	}
	seal.Status, seal.FailureCode = CatalogSealFailed, code
	return seal, nil
}

func canonicalFailureCode(value string) bool {
	if value == "" || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}
