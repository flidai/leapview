package providerrestore

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/internal/recoveryset"
)

// PrimaryFence verifies that all original database writers are durably fenced
// for this exact recovery frontier. A controller lease is not a primary fence.
// Verification must observe the original host/provider, not trust a cached
// successful response or infer process death from a connection timeout.
type PrimaryFence interface {
	Verify(context.Context, recoveryset.RecoverySet) error
}

// NewManaged composes managed recovery over the existing coordinator and ledger.
// Unlike a provider-only component exercise, managed recovery requires an
// independently enrolled, durable original-primary fence.
func NewManaged(dependencies Dependencies, fence PrimaryFence) (*Coordinator, error) {
	if typednil.IsNil(fence) {
		return nil, fmt.Errorf("%w: managed recovery requires an original-primary fence", ErrInvalid)
	}
	coordinator, err := New(dependencies)
	if err != nil {
		return nil, err
	}
	coordinator.primaryFence = fence
	return coordinator, nil
}

func (coordinator *Coordinator) verifyPrimaryFence(ctx context.Context, request Request, set recoveryset.RecoverySet) error {
	if coordinator.primaryFence == nil {
		return nil
	}
	if set.ID != request.RecoverySetID || set.Delivery.TargetID != request.TargetID {
		return fmt.Errorf("%w: original-primary fence scope mismatch", ErrInconsistent)
	}
	digest, err := set.Digest()
	if err != nil || digest != set.FrontierDigest {
		return fmt.Errorf("%w: original-primary fence requires the exact recovery frontier", ErrInconsistent)
	}
	if err := coordinator.primaryFence.Verify(ctx, set); err != nil {
		return fmt.Errorf("%w: verify original-primary fence: %w", ErrIndeterminate, err)
	}
	return nil
}
