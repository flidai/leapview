package materialize

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

var (
	errSemanticCacheIdentityUnavailable = errors.New("semantic cache identity is unavailable")
	errSemanticCacheIdentityChanged     = errors.New("semantic cache authority changed")
)

// semanticCacheIdentity obtains a fresh cache identity from the request-bound
// consumer. The consumer is the authority for protected requests: this helper
// does not reconstruct identity from request fields or from the cache key.
// Public models intentionally return a nil identity and retain their existing
// cache representation.
func (r *Runtime) semanticCacheIdentity(ctx context.Context, request dataquery.Query, consumer *semanticquery.SemanticAccessConsumer) (*resultidentity.SemanticAccessIdentity, error) {
	state, err := r.requireSemanticProtectionState()
	if err != nil {
		return nil, err
	}
	if !state.protected {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if consumer == nil {
		return nil, fmt.Errorf("%w: request-bound consumer is required", errSemanticCacheIdentityUnavailable)
	}
	bound, ok := semanticquery.SemanticAccessConsumerContextFromContext(ctx)
	if !ok || bound.Consumer != consumer {
		return nil, fmt.Errorf("%w: request-bound consumer is invalid", errSemanticCacheIdentityUnavailable)
	}
	if err := bound.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", errSemanticCacheIdentityUnavailable, err)
	}
	identity, err := consumer.CacheIdentity()
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, fmt.Errorf("%w: protected consumer returned no identity", errSemanticCacheIdentityUnavailable)
	}
	return identity, nil
}

// semanticCacheGuard revalidates the exact admitted main and auxiliary count
// plans, and then obtains a fresh consumer identity before every cache
// boundary. A changed authority is an error: the caller must not silently
// fall back to a stale hit or publish a result under the old identity.
func (r *Runtime) semanticCacheGuard(request dataquery.Query, planned plannedArrowQuery) func(context.Context) error {
	if planned.consumer == nil || planned.semanticAccess == nil {
		return nil
	}
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		validatePlans := func() error {
			if err := planned.consumer.ValidatePlan(planned.plan); err != nil {
				return fmt.Errorf("validate admitted semantic cache plan: %w", err)
			}
			if planned.countPlan != nil {
				if err := planned.consumer.ValidatePlan(*planned.countPlan); err != nil {
					return fmt.Errorf("validate admitted semantic cache count plan: %w", err)
				}
			}
			return nil
		}
		// ValidatePlan owns plan-invalidation audit. Run it before CacheIdentity
		// and once more if the live identity read reports stale authority, so a
		// rejected cache boundary cannot silently skip that observation.
		if err := validatePlans(); err != nil {
			return err
		}
		current, err := r.semanticCacheIdentity(ctx, request, planned.consumer)
		if err != nil {
			if planErr := validatePlans(); planErr != nil {
				return planErr
			}
			return err
		}
		if !reflect.DeepEqual(current, planned.semanticAccess) {
			if planErr := validatePlans(); planErr != nil {
				return planErr
			}
			return fmt.Errorf("%w: cache identity no longer matches admitted request", errSemanticCacheIdentityChanged)
		}
		return nil
	}
}
