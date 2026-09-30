package duckdb

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/flidai/leapview/internal/platform/typednil"
)

// WithLocalConnection exposes a locally managed credential only while the
// synchronous consumer runs. The caller owns source-work admission and the
// runtime credential reader lease around this call. The snapshot remains
// caller-owned and must be destroyed by its owner.
func (factory *TargetRuntimePoolFactory) WithLocalConnection(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
	logical semanticmodel.Connection,
	consume func(semanticmodel.Connection) error,
) (resultErr error) {
	if ctx == nil || factory == nil || consume == nil {
		return connectionbinding.ErrIncompatibleBinding
	}
	identity := snapshot.Identity()
	if identity.Validate() != nil || identity.CredentialVersionID == "" || binding.Validate() != nil || !binding.Enabled ||
		binding.ConnectorKind != "postgres" || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle ||
		logical.Kind != binding.ConnectorKind || logical.Access != "" {
		return connectionbinding.ErrIncompatibleBinding
	}
	if err := sourcework.Revalidate(ctx); err != nil {
		return err
	}

	var candidate connectionbinding.RuntimePool
	safeBoundaryError := false
	defer func() {
		if recover() != nil {
			resultErr = errors.Join(ErrTargetPoolCleanupFailed, analyticsruntime.ErrConnectionCleanupFailed)
		}
		var closeErr error
		if cleanupErr := closeLocalTargetPool(candidate); cleanupErr != nil {
			closeErr = cleanupErr
		}
		primaryErr := safeLocalTargetRuntimeError(resultErr)
		if safeBoundaryError {
			primaryErr = resultErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			primaryErr = errors.Join(primaryErr, ctxErr)
		}
		resultErr = errors.Join(primaryErr, closeErr)
	}()

	var prepareErr error
	candidate, prepareErr = factory.PrepareLocal(ctx, binding, snapshot)
	if prepareErr != nil {
		return safeLocalTargetRuntimeError(prepareErr)
	}
	target, ok := candidate.(*targetRuntimePool)
	if !ok || target == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	if target.CredentialIdentity() != identity {
		return connectionbinding.ErrProviderUnavailable
	}

	if err := sourcework.Revalidate(ctx); err != nil {
		safeBoundaryError = true
		return err
	}
	if err := target.HealthCheck(ctx); err != nil {
		return safeLocalTargetRuntimeError(err)
	}
	if err := sourcework.Revalidate(ctx); err != nil {
		safeBoundaryError = true
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return safeLocalTargetRuntimeError(target.withLocalConnection(
		ctx, binding.ConnectionID.String(), logical, identity, consume,
	))
}

func closeLocalTargetPool(pool connectionbinding.RuntimePool) (resultErr error) {
	if typednil.IsNil(pool) {
		return nil
	}
	defer func() {
		if recover() != nil {
			resultErr = errors.Join(ErrTargetPoolCleanupFailed, analyticsruntime.ErrConnectionCleanupFailed)
		}
	}()
	if err := pool.Close(); err != nil {
		return errors.Join(ErrTargetPoolCleanupFailed, analyticsruntime.ErrConnectionCleanupFailed)
	}
	return nil
}

func safeLocalTargetRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var safe []error
	if errors.Is(err, ErrTargetPoolCleanupFailed) {
		safe = append(safe, ErrTargetPoolCleanupFailed)
	}
	if errors.Is(err, analyticsruntime.ErrConnectionCleanupFailed) {
		safe = append(safe, analyticsruntime.ErrConnectionCleanupFailed)
	}
	if errors.Is(err, connectionbinding.ErrInvalidCredentialBundle) {
		safe = append(safe, connectionbinding.ErrInvalidCredentialBundle)
	}
	if errors.Is(err, connectionbinding.ErrIncompatibleBinding) {
		safe = append(safe, connectionbinding.ErrIncompatibleBinding)
	}
	if errors.Is(err, connectionbinding.ErrInvalidBinding) {
		safe = append(safe, connectionbinding.ErrInvalidBinding)
	}
	if errors.Is(err, context.Canceled) {
		safe = append(safe, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		safe = append(safe, context.DeadlineExceeded)
	}
	if len(safe) == 0 {
		return connectionbinding.ErrProviderUnavailable
	}
	if errors.Is(err, ErrTargetPoolCleanupFailed) {
		foundRuntimeCleanup := false
		for _, value := range safe {
			foundRuntimeCleanup = foundRuntimeCleanup || errors.Is(value, analyticsruntime.ErrConnectionCleanupFailed)
		}
		if !foundRuntimeCleanup {
			safe = append(safe, analyticsruntime.ErrConnectionCleanupFailed)
		}
	}
	return errors.Join(safe...)
}
