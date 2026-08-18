package deployment

import (
	"fmt"
	"time"
)

type DeliveryRetentionExceptionStatus string

const (
	DeliveryRetentionActive   DeliveryRetentionExceptionStatus = "active"
	DeliveryRetentionReleased DeliveryRetentionExceptionStatus = "released"
)

// DeliveryRetentionException is an explicit durable root for a catalog that
// must survive ordinary candidate TTL/GC policy (for example, a rollback
// window or an incident hold). It roots one complete candidate or generation,
// never an individual table/file.
type DeliveryRetentionException struct {
	ID             string                           `json:"id"`
	PhysicalPoolID string                           `json:"physicalPoolId"`
	CandidateID    string                           `json:"candidateId,omitempty"`
	GenerationID   string                           `json:"generationId,omitempty"`
	CatalogDigest  string                           `json:"catalogDigest"`
	Reason         string                           `json:"reason"`
	ExpiresAt      time.Time                        `json:"expiresAt"`
	CreatedAt      time.Time                        `json:"createdAt"`
	ReleasedAt     time.Time                        `json:"releasedAt"`
	Status         DeliveryRetentionExceptionStatus `json:"status"`
}

func (root DeliveryRetentionException) Validate() error {
	for name, value := range map[string]string{"retention exception": root.ID, "pool": root.PhysicalPoolID} {
		if err := ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if (root.CandidateID == "") == (root.GenerationID == "") {
		return fmt.Errorf("%w: retention exception must root exactly one candidate or generation", ErrDeliveryInvalid)
	}
	if root.CandidateID != "" {
		if err := ValidateDeliveryID(root.CandidateID); err != nil {
			return fmt.Errorf("candidate id: %w", err)
		}
	}
	if root.GenerationID != "" {
		if err := ValidateDeliveryID(root.GenerationID); err != nil {
			return fmt.Errorf("generation id: %w", err)
		}
	}
	if err := ValidateDeliveryDigest(root.CatalogDigest); err != nil {
		return fmt.Errorf("catalog digest: %w", err)
	}
	if root.Reason == "" || root.Reason != trim(root.Reason) {
		return fmt.Errorf("%w: retention reason is required", ErrDeliveryInvalid)
	}
	if err := validateDeliveryTime("retention created at", root.CreatedAt, true); err != nil {
		return err
	}
	if err := validateDeliveryTime("retention expiry", root.ExpiresAt, true); err != nil {
		return err
	}
	if !root.ExpiresAt.After(root.CreatedAt) {
		return fmt.Errorf("%w: retention expiry must be after creation", ErrDeliveryInvalid)
	}
	if root.Status == DeliveryRetentionReleased {
		if err := validateDeliveryTime("retention release", root.ReleasedAt, true); err != nil {
			return err
		}
	} else if root.Status != DeliveryRetentionActive {
		return fmt.Errorf("%w: unsupported retention status %q", ErrDeliveryInvalid, root.Status)
	} else if !root.ReleasedAt.IsZero() {
		return fmt.Errorf("%w: active retention exception contains release evidence", ErrDeliveryInvalid)
	}
	return nil
}

func NewDeliveryRetentionException(root DeliveryRetentionException) (DeliveryRetentionException, error) {
	if err := validateDeliveryTime("retention created at", root.CreatedAt, true); err != nil {
		return DeliveryRetentionException{}, err
	}
	if err := validateDeliveryTime("retention expiry", root.ExpiresAt, true); err != nil {
		return DeliveryRetentionException{}, err
	}
	root.Status = DeliveryRetentionActive
	root.CreatedAt, root.ExpiresAt = root.CreatedAt.UTC(), root.ExpiresAt.UTC()
	root.ReleasedAt = time.Time{}
	if err := root.Validate(); err != nil {
		return DeliveryRetentionException{}, err
	}
	return root, nil
}

func (root DeliveryRetentionException) Release(now time.Time) (DeliveryRetentionException, error) {
	if err := root.Validate(); err != nil {
		return DeliveryRetentionException{}, err
	}
	if root.Status == DeliveryRetentionReleased {
		return root, nil
	}
	if now.IsZero() {
		return DeliveryRetentionException{}, fmt.Errorf("%w: retention release time is required", ErrDeliveryInvalid)
	}
	root.Status, root.ReleasedAt = DeliveryRetentionReleased, now.UTC()
	return root, nil
}
