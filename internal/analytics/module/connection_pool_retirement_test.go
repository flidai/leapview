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

func TestModulePoolRetirementDoesNotCreateMissingPools(t *testing.T) {
	module, now := closeTestModule(t, &closingModulePool{})
	binding := modulePoolBinding(t, now())
	handle, err := module.RetireConnectionPool(binding)
	require.ErrorIs(t, err, connectionbinding.ErrBindingNotFound)
	require.Nil(t, handle)
	require.Nil(t, module.connectionPools)

	foreign := binding
	foreign.TargetID = "target_other"
	_, err = module.RetireConnectionPool(foreign)
	require.ErrorIs(t, err, connectionbinding.ErrUnauthorizedBinding)
	var missing *Module
	_, err = missing.RetireConnectionPool(binding)
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
}

func TestModulePoolRetirementRetainsCleanupFailureThroughShutdown(t *testing.T) {
	closeErr := errors.New("pool cleanup failed")
	pool := &closingModulePool{release: make(chan struct{}), err: closeErr}
	var closeOnce sync.Once
	finish := func() { closeOnce.Do(func() { close(pool.release) }) }
	module, now := closeTestModule(t, pool)
	t.Cleanup(func() {
		finish()
		_ = module.Close()
	})
	directory, err := module.ensureConnectionPools(now, moduleRotationAuditNoop{}, 20*time.Millisecond, 1)
	require.NoError(t, err)
	binding := modulePoolBinding(t, now())
	administration, err := directory.Pool(binding)
	require.NoError(t, err)
	require.NoError(t, administration.Refresh(t.Context(), connectionbinding.RefreshRequest{
		Actor: "principal:operator-1", Operation: connectionbinding.RefreshRequested,
	}))
	// Refresh persists a newer binding revision; retirement must use that exact tuple.
	binding = module.connectionBindings.(*moduleBindingCatalog).binding
	handle, err := module.RetireConnectionPool(binding)
	require.NoError(t, err)
	finish()
	require.ErrorIs(t, handle.Wait(context.Background(), time.Now().Add(time.Second)), closeErr)
	require.ErrorIs(t, module.Close(), closeErr)
	require.ErrorIs(t, handle.Wait(context.Background(), time.Now().Add(time.Second)), closeErr)
	_, err = module.RetireConnectionPool(binding)
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
}
