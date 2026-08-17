package authoring

import (
	"fmt"
	"math"
)

// SemanticModelUsage is the project-scoped authoring asset count for one
// semantic model. Counts include every non-archived dashboard identity,
// regardless of whether its lifecycle is currently a draft or published.
type SemanticModelUsage struct {
	SemanticModel string `json:"semanticModel"`
	Private       uint64 `json:"private"`
	Organization  uint64 `json:"organization"`
	Total         uint64 `json:"total"`
}

// NewSemanticModelUsage constructs a usage projection deterministically from
// its two visibility buckets. Total is always derived from those buckets.
func NewSemanticModelUsage(semanticModel string, private, organization uint64) (SemanticModelUsage, error) {
	if err := validateRequiredLifecycleValue("semantic model", semanticModel); err != nil {
		return SemanticModelUsage{}, err
	}
	if private > math.MaxUint64-organization {
		return SemanticModelUsage{}, fmt.Errorf("%w: semantic model usage count overflows total", ErrInvalidAuthoring)
	}
	return SemanticModelUsage{SemanticModel: semanticModel, Private: private, Organization: organization, Total: private + organization}, nil
}

// Validate verifies a usage projection loaded from a persistence boundary.
func (u SemanticModelUsage) Validate() error {
	if err := validateRequiredLifecycleValue("semantic model", u.SemanticModel); err != nil {
		return err
	}
	if u.Private > math.MaxUint64-u.Organization {
		return fmt.Errorf("%w: semantic model usage count overflows total", ErrInvalidAuthoring)
	}
	if u.Total != u.Private+u.Organization {
		return fmt.Errorf("%w: semantic model usage total must equal private plus organization", ErrInvalidAuthoring)
	}
	return nil
}
