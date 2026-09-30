package module

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/stretchr/testify/require"
)

func TestModuleSourcePauseFencesSharedRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		close(release)
		module, now := closeTestModule(t, &closingModulePool{release: release})
		defer module.Close()
		pause, err := module.PauseSourceWork()
		require.NoError(t, err)
		directory, err := module.ensureConnectionPools(now, moduleRotationAuditNoop{}, time.Minute, 1)
		require.NoError(t, err)
		pool, err := directory.Pool(modulePoolBinding(t, now()))
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			done <- pool.Refresh(context.Background(), connectionbinding.RefreshRequest{
				Actor: "principal:operator-1", Operation: connectionbinding.RefreshRequested,
			})
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("shared refresh passed paused module: %v", err)
		default:
		}
		require.NoError(t, pause.WaitDrained(context.Background()))
		require.NoError(t, pause.Resume())
		require.NoError(t, <-done)
	})
}

func TestModuleSourcePauseKeepsIsolatedCredentialProbeAvailable(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}
	factory := &credentialProbeFactory{pool: &credentialProbeRuntimePool{}}
	module := credentialProbeModule(binding, catalog, factory)
	pause, err := module.PauseSourceWork()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, module.ProbeCredential(ctx, binding, testCredentialProbeVersion, map[string]string{"password": "draft-secret"}))
	require.Equal(t, 1, factory.calls)
	require.NoError(t, pause.WaitDrained(ctx))
	require.NoError(t, pause.Resume())
}

func TestModuleClosePermanentlyFencesSourceWork(t *testing.T) {
	module := &Module{}
	pause, err := module.PauseSourceWork()
	require.NoError(t, err)
	require.NoError(t, module.Close())
	require.ErrorIs(t, pause.Resume(), sourcework.ErrClosed)
	_, err = module.sourceWorkGate().Acquire(context.Background())
	require.ErrorIs(t, err, sourcework.ErrClosed)
	_, err = module.PauseSourceWork()
	require.ErrorIs(t, err, sourcework.ErrClosed)
}
