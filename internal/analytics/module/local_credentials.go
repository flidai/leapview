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
)

// LocalCredentialReader consumes one exact server-proven version for a target
// binding. The application owns the encrypted credential authority.
type LocalCredentialReader func(context.Context, connectionbinding.TargetBinding, string, func(map[string]string) error) error

func (m *Module) ConfigureLocalCredentials(read LocalCredentialReader, admission analyticsduckdb.ProviderAdmission) error {
	if m == nil || read == nil || admission == nil || m.localCredentialReader != nil || m.connectionFactory == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	m.localCredentialReader, m.providerAdmission = read, admission
	return nil
}

// AcquireLocal is used only after the candidate leaser has authorized the
// connection and proved the immutable operation/generation's exact pin.
func (m *Module) AcquireLocal(ctx context.Context, binding connectionbinding.TargetBinding, version, _ string) (connectionbinding.ValidatedPoolLease, error) {
	if m == nil || m.localCredentialReader == nil || m.providerAdmission == nil || binding.ConnectorKind != "postgres" || binding.AuthenticationMode == connectionbinding.AuthenticationNone ||
		(connectionbinding.CredentialIdentity{CredentialVersionID: version}).Validate() != nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	ctx, release, err := m.providerAdmission.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var pool connectionbinding.RuntimePool
	cleanupUncertain := false
	err = m.localCredentialReader(ctx, binding, version, func(fields map[string]string) error {
		snapshot, err := connectionbinding.NewLocalCredentialSnapshot(fields, version, time.Now(), time.Time{})
		if err != nil {
			return err
		}
		defer snapshot.Destroy()
		pool, err = m.connectionFactory.Prepare(ctx, binding, snapshot)
		cleanupUncertain = errors.Is(err, connectionbinding.ErrProviderCleanupUncertain)
		if err != nil {
			return err
		}
		if pool == nil {
			return connectionbinding.ErrProviderUnavailable
		}
		return pool.HealthCheck(ctx)
	})
	if err != nil {
		// A failed close keeps the admission lease quarantined until restart.
		if !cleanupUncertain && (pool == nil || pool.Close() == nil) {
			release()
		}
		return nil, connectionbinding.ErrProviderUnavailable
	}
	evidence := binding.Evidence()
	evidence.ValidatedVersion, evidence.CredentialVersionID = "", version
	return &localCredentialLease{pool: pool, evidence: evidence, release: release}, nil
}

type localCredentialLease struct {
	pool     connectionbinding.RuntimePool
	evidence connectionbinding.BindingEvidence
	release  func()
	once     sync.Once
	closeErr error
}

func (l *localCredentialLease) Pool() connectionbinding.RuntimePool         { return l.pool }
func (l *localCredentialLease) Evidence() connectionbinding.BindingEvidence { return l.evidence }
func (l *localCredentialLease) Release()                                    { _ = l.close() }
func (l *localCredentialLease) close() error {
	l.once.Do(func() {
		l.closeErr = l.pool.Close()
		if l.closeErr == nil {
			l.release()
		}
	})
	return l.closeErr
}

func (r *activeRuntimeConnectionResolver) resolveLocal(ctx context.Context, binding connectionbinding.TargetBinding, evidence ActiveRuntimeBindingEvidence, name string, logical semanticmodel.Connection) (semanticmodel.Connection, error) {
	lease, err := r.module.AcquireLocal(ctx, binding, evidence.CredentialVersionID, "")
	if err != nil {
		return semanticmodel.Connection{}, err
	}
	defer lease.Release()
	target, ok := lease.Pool().(analyticsruntime.ConnectionResolver)
	if !ok {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	result, err := target.Resolve(ctx, name, logical)
	closeErr := lease.(*localCredentialLease).close()
	if err != nil || closeErr != nil {
		clear(result.Auth)
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	return result, nil
}

// Resolve returns a detached credential-bearing copy only after destruction of
// the probe pool has been acknowledged. SourceRuntime owns the enclosing lease.
func resolveActivePool(ctx context.Context, factory connectionbinding.RuntimePoolFactory, binding connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, name string, logical semanticmodel.Connection) (result semanticmodel.Connection, resultErr error) {
	pool, err := factory.Prepare(ctx, binding, snapshot)
	if err != nil || pool == nil {
		if errors.Is(err, connectionbinding.ErrProviderCleanupUncertain) {
			return result, connectionbinding.ErrProviderCleanupUncertain
		}
		return result, connectionbinding.ErrProviderUnavailable
	}
	defer func() {
		if pool.Close() != nil {
			clear(result.Auth)
			result = semanticmodel.Connection{}
			resultErr = connectionbinding.ErrProviderCleanupUncertain
		}
	}()
	if pool.HealthCheck(ctx) != nil {
		return result, connectionbinding.ErrProviderUnavailable
	}
	resolver, ok := pool.(analyticsruntime.ConnectionResolver)
	if !ok {
		return result, connectionbinding.ErrProviderUnavailable
	}
	return resolver.Resolve(ctx, name, logical)
}

// RetireCredentialPools destroys old provider-backed probe clients only after
// the shared admission barrier has acknowledged every active consumer.
func (m *Module) RetireCredentialPools(ctx context.Context) error {
	if m == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	m.connectionPoolsMu.Lock()
	pools := m.connectionPools
	m.connectionPoolsMu.Unlock()
	return pools.RetireAll(ctx)
}
