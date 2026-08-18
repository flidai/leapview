package deployment

import (
	"fmt"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// VerifiedSeal is the immutable evidence a serving or publication adapter
// must bind before it can expose a catalog. It intentionally contains no
// physical table/file manifest; that remains in the DuckLake artifact.
type VerifiedSeal struct {
	SealID                string
	CatalogDigest         string
	CatalogObjectKey      string
	ObjectSize            int64
	PhysicalPoolID        string
	CompatibilityDigest   string
	ClosureDigest         string
	QualificationDigest   string
	ServingArtifactID     string
	ServingArtifactDigest string
}

func (s VerifiedSeal) Validate() error {
	if err := ValidateDeliveryID(s.SealID); err != nil {
		return fmt.Errorf("seal id: %w", err)
	}
	for name, value := range map[string]string{"catalog": s.CatalogDigest, "compatibility": s.CompatibilityDigest, "closure": s.ClosureDigest, "qualification": s.QualificationDigest, "serving artifact digest": s.ServingArtifactDigest, "pool": s.PhysicalPoolID} {
		if name == "pool" {
			if err := ValidateDeliveryID(value); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := ValidateDeliveryID(s.ServingArtifactID); err != nil {
		return fmt.Errorf("serving artifact id: %w", err)
	}
	if err := validateCatalogObjectKey("catalog object key", s.CatalogObjectKey); err != nil {
		return err
	}
	if s.ObjectSize <= 0 {
		return fmt.Errorf("%w: sealed catalog object size must be positive", ErrDeliveryInvalid)
	}
	return nil
}

// RollbackRequest is a target-fenced selection of one retained generation.
// Implementations must perform the swap in their durable control-plane
// transaction and must not touch DuckLake metadata or object storage.
type RollbackRequest struct {
	ID                       string
	ActorID                  string
	RequestDigest            string
	TargetID                 string
	ProjectID                projectgraph.ResourceID
	Environment              string
	GenerationID             string
	CandidateID              string
	ExpectedBaseGenerationID string
	ExpectedTargetRevision   int64
	VerifiedSeal             VerifiedSeal
	CreatedAt                time.Time
}

func (r RollbackRequest) Validate() error {
	if r.ActorID != "" {
		if err := ValidateDeliveryID(r.ActorID); err != nil {
			return fmt.Errorf("rollback actor id: %w", err)
		}
	}
	for name, value := range map[string]string{"rollback": r.ID, "target": r.TargetID, "generation": r.GenerationID, "candidate": r.CandidateID} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := ValidateDeliveryDigest(r.RequestDigest); err != nil {
		return fmt.Errorf("request digest: %w", err)
	}
	if err := validateDeliveryScope(r.ProjectID, r.Environment); err != nil {
		return err
	}
	if r.ExpectedTargetRevision < 0 {
		return fmt.Errorf("%w: target revision cannot be negative", ErrDeliveryInvalid)
	}
	if r.ExpectedBaseGenerationID != "" {
		if err := ValidateDeliveryID(r.ExpectedBaseGenerationID); err != nil {
			return fmt.Errorf("expected base generation: %w", err)
		}
	}
	if err := validateDeliveryTime("rollback created at", r.CreatedAt, true); err != nil {
		return err
	}
	return r.VerifiedSeal.Validate()
}

// RollbackResult is the durable result of a rollback CAS. Repeating an
// identical request returns the same result.
type RollbackResult struct {
	RequestDigest    string
	TargetID         string
	GenerationID     string
	TargetRevision   int64
	CatalogDigest    string
	CatalogObjectKey string
	Status           string
	CompletedAt      time.Time
}

func (r RollbackResult) Validate() error {
	if err := ValidateDeliveryDigest(r.RequestDigest); err != nil {
		return err
	}
	if err := ValidateDeliveryID(r.TargetID); err != nil {
		return err
	}
	if err := ValidateDeliveryID(r.GenerationID); err != nil {
		return err
	}
	if err := ValidateDeliveryDigest(r.CatalogDigest); err != nil {
		return err
	}
	if err := validateCatalogObjectKey("rollback catalog object key", r.CatalogObjectKey); err != nil {
		return err
	}
	if r.TargetRevision <= 0 || r.Status != string(DeliveryPublicationCommitted) {
		return fmt.Errorf("%w: rollback result is incomplete", ErrDeliveryInvalid)
	}
	return validateDeliveryTime("rollback completed at", r.CompletedAt, true)
}
