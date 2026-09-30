package module

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/google/uuid"
)

var (
	ErrLocalRuntimeCredentialCheckFailed   = errors.New("local runtime credential check failed")
	ErrLocalRuntimeCredentialCleanupFailed = errors.New("local runtime credential cleanup failed")
)

type localRuntimeConnectionConsumer interface {
	WithLocalConnection(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot, semanticmodel.Connection, func(semanticmodel.Connection) error) error
}

// CheckLocalRuntimeCredential synchronously checks one locally managed
// PostgreSQL credential against the exact serving target binding. The read
// capability is supplied by the application and is invoked only after source
// work admission succeeds. No candidate pool or secret-bearing error escapes.
func (m *Module) CheckLocalRuntimeCredential(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	versionID string,
	read func(context.Context, func(map[string]string) error) error,
) (connectionbinding.CredentialIdentity, error) {
	if ctx == nil || m == nil || m.connectionBindings == nil || m.connectionFactory == nil || read == nil {
		return connectionbinding.CredentialIdentity{}, connectionbinding.ErrProviderUnavailable
	}
	localFactory, ok := m.connectionFactory.(localRuntimeConnectionConsumer)
	if !ok {
		return connectionbinding.CredentialIdentity{}, connectionbinding.ErrProviderUnavailable
	}
	if err := validateLocalRuntimeCredentialInput(m, binding, versionID); err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}

	lease, err := m.sourceWork.Acquire(ctx)
	if err != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(err)
	}
	quarantineLease := false
	defer func() {
		if !quarantineLease {
			lease.Release()
		}
	}()

	current, err := m.currentCredentialProbeBinding(ctx, binding)
	if err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}

	var mu sync.Mutex
	var calls int
	var readReturned bool
	var consumerErr error
	var identity connectionbinding.CredentialIdentity
	consumer := func(fields map[string]string) error {
		mu.Lock()
		defer mu.Unlock()
		if readReturned {
			return ErrLocalRuntimeCredentialCheckFailed
		}
		calls++
		if calls != 1 {
			return ErrLocalRuntimeCredentialCheckFailed
		}
		checkedIdentity, checkErr := checkLocalRuntimeCandidate(ctx, localFactory, current, versionID, fields)
		consumerErr = checkErr
		if checkErr == nil {
			identity = checkedIdentity
		}
		return checkErr
	}

	// If a reader or factory panics after partially preparing a candidate, keep
	// the source-work lease until a normal return proves cleanup completed.
	quarantineLease = true
	readErr := read(ctx, consumer)
	mu.Lock()
	readReturned = true
	count := calls
	checkErr := consumerErr
	resultIdentity := identity
	mu.Unlock()
	if errors.Is(checkErr, ErrLocalRuntimeCredentialCleanupFailed) || errors.Is(checkErr, analyticsduckdb.ErrTargetPoolCleanupFailed) {
		return connectionbinding.CredentialIdentity{}, checkErr
	}
	quarantineLease = false
	if errors.Is(checkErr, connectionbinding.ErrInvalidCredentialBundle) {
		return connectionbinding.CredentialIdentity{}, connectionbinding.ErrInvalidCredentialBundle
	}
	if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(readErr)
	}
	if checkErr != nil {
		return connectionbinding.CredentialIdentity{}, checkErr
	}
	if readErr != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(readErr)
	}
	if count != 1 {
		return connectionbinding.CredentialIdentity{}, ErrLocalRuntimeCredentialCheckFailed
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(err)
	}
	if _, err := m.currentCredentialProbeBinding(ctx, current); err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(err)
	}
	return resultIdentity, nil
}

func validateLocalRuntimeCredentialInput(module *Module, binding connectionbinding.TargetBinding, versionID string) error {
	parsed, err := uuid.Parse(versionID)
	if err != nil || parsed == uuid.Nil || parsed.String() != versionID {
		return connectionbinding.ErrInvalidCredentialBundle
	}
	if binding.Validate() != nil || !binding.Enabled || binding.TargetID.String() != module.targetID ||
		module.targetID == "" || module.targetEnvironment == "" || binding.Scope.Environment != module.targetEnvironment ||
		binding.ConnectorKind != "postgres" || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
		return connectionbinding.ErrIncompatibleBinding
	}
	return nil
}

func checkLocalRuntimeCandidate(
	ctx context.Context,
	factory localRuntimeConnectionConsumer,
	binding connectionbinding.TargetBinding,
	versionID string,
	fields map[string]string,
) (connectionbinding.CredentialIdentity, error) {
	if err := ctx.Err(); err != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(err)
	}
	password, ok := fields["password"]
	if len(fields) != 1 || !ok || password == "" || len(password) > 16<<10 {
		return connectionbinding.CredentialIdentity{}, connectionbinding.ErrInvalidCredentialBundle
	}
	snapshot, err := connectionbinding.NewLocalCredentialSnapshot(fields, versionID, time.Now().UTC(), time.Time{})
	if err != nil {
		return connectionbinding.CredentialIdentity{}, connectionbinding.ErrInvalidCredentialBundle
	}
	defer snapshot.Destroy()

	// The factory owns the complete transient pool lifetime. A health check
	// uses the same scoped connection path as a source consumer, with no source
	// work to perform after the factory has established health.
	err = factory.WithLocalConnection(ctx, binding, snapshot, semanticmodel.Connection{Kind: binding.ConnectorKind},
		func(semanticmodel.Connection) error { return nil })
	if errors.Is(err, analyticsduckdb.ErrTargetPoolCleanupFailed) || errors.Is(err, analyticsruntime.ErrConnectionCleanupFailed) {
		return connectionbinding.CredentialIdentity{}, localRuntimeCleanupError(errors.Join(err, ctx.Err()))
	}
	if err != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(errors.Join(err, ctx.Err()))
	}
	if err := ctx.Err(); err != nil {
		return connectionbinding.CredentialIdentity{}, localRuntimeCheckError(err)
	}
	return connectionbinding.CredentialIdentity{CredentialVersionID: versionID}, nil
}

func localRuntimeCheckError(cause error) error {
	if errors.Is(cause, context.Canceled) {
		return errors.Join(ErrLocalRuntimeCredentialCheckFailed, context.Canceled)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(ErrLocalRuntimeCredentialCheckFailed, context.DeadlineExceeded)
	}
	return ErrLocalRuntimeCredentialCheckFailed
}

func localRuntimeCleanupError(cause error) error {
	if errors.Is(cause, context.Canceled) {
		return errors.Join(ErrLocalRuntimeCredentialCleanupFailed, context.Canceled)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return errors.Join(ErrLocalRuntimeCredentialCleanupFailed, context.DeadlineExceeded)
	}
	return ErrLocalRuntimeCredentialCleanupFailed
}
