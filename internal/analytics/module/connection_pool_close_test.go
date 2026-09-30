package module

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/stretchr/testify/require"
)

func TestModuleCloseRetainsConnectionPoolCleanup(t *testing.T) {
	closeErr := errors.New("provider cleanup failed")
	factory := &closingModulePool{release: make(chan struct{}), err: closeErr}
	var release sync.Once
	finish := func() { release.Do(func() { close(factory.release) }) }
	defer finish()
	module, now := closeTestModule(t, factory)
	directory, err := module.ensureConnectionPools(now, moduleRotationAuditNoop{}, 20*time.Millisecond, 1)
	require.NoError(t, err)
	binding := modulePoolBinding(t, now())
	pool, err := directory.Pool(binding)
	require.NoError(t, err)
	require.NoError(t, pool.Refresh(context.Background(), connectionbinding.RefreshRequest{
		Actor: "principal:operator-1", Operation: connectionbinding.RefreshRequested,
	}))

	require.ErrorIs(t, module.Close(), context.DeadlineExceeded)
	require.ErrorIs(t, module.Close(), context.DeadlineExceeded)
	_, err = module.ensureConnectionPools(now, moduleRotationAuditNoop{}, time.Second, 1)
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	finish()
	require.ErrorIs(t, module.Close(), closeErr)
	require.ErrorIs(t, module.Close(), closeErr)
}

func TestModuleCloseBeforePoolInitializationFencesCreation(t *testing.T) {
	module, now := closeTestModule(t, &closingModulePool{})
	require.NoError(t, module.Close())
	_, err := module.ensureConnectionPools(now, moduleRotationAuditNoop{}, time.Second, 1)
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
}

func closeTestModule(t *testing.T, factory *closingModulePool) (*Module, func() time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	binding := modulePoolBinding(t, now)
	return &Module{
		connectionBindings: &moduleBindingCatalog{binding: binding},
		targetResolvers: connectionbinding.ResolverSet{
			Infisical: &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, now)},
		},
		targetID: binding.TargetID.String(), targetEnvironment: binding.Scope.Environment,
		targetClass: connectionbinding.TargetProduction, connectionFactory: factory,
	}, func() time.Time { return now }
}

type closingModulePool struct {
	release chan struct{}
	err     error
}

func (p *closingModulePool) Prepare(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	return p, nil
}

func (*closingModulePool) HealthCheck(context.Context) error { return nil }

func (p *closingModulePool) Close() error {
	<-p.release
	return p.err
}
