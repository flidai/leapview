package module

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/stretchr/testify/require"
)

const localRuntimeCredentialVersion = "93d623e3-8d75-41ea-b006-aed3375d0592"

func TestCheckLocalRuntimeCredentialChecksAndClosesTransientPool(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	pool := &localRuntimeCredentialPool{}
	factory := &localRuntimeCredentialFactory{pool: pool}
	catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}
	module := localRuntimeCredentialModule(binding, catalog, factory)
	readCalls := 0

	identity, err := module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
		func(_ context.Context, consume func(map[string]string) error) error {
			readCalls++
			return consume(map[string]string{"password": "local-secret"})
		})
	require.NoError(t, err)
	require.Equal(t, connectionbinding.CredentialIdentity{CredentialVersionID: localRuntimeCredentialVersion}, identity)
	require.Equal(t, 1, readCalls)
	require.Equal(t, 1, factory.localConnectionCalls)
	require.Zero(t, factory.prepareProviderCalls)
	require.Equal(t, localRuntimeCredentialVersion, factory.identity.CredentialVersionID)
	require.Equal(t, map[string]string{"password": "local-secret"}, factory.fields)
	require.Equal(t, 1, pool.healthCalls)
	require.Equal(t, 1, pool.closeCalls)
	require.Equal(t, 2, catalog.bindingCalls)
	require.Nil(t, module.connectionPools)
}

func TestCheckLocalRuntimeCredentialRequiresExactlyOnePasswordConsumerCall(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	for _, test := range []struct {
		name    string
		read    func(func(map[string]string) error) error
		wantErr error
	}{
		{name: "zero calls", read: func(func(map[string]string) error) error { return nil }, wantErr: ErrLocalRuntimeCredentialCheckFailed},
		{name: "multiple calls", read: func(consume func(map[string]string) error) error {
			if err := consume(map[string]string{"password": "one"}); err != nil {
				return err
			}
			return consume(map[string]string{"password": "two"})
		}, wantErr: ErrLocalRuntimeCredentialCheckFailed},
		{name: "extra field", read: func(consume func(map[string]string) error) error {
			return consume(map[string]string{"password": "one", "connection_string": "secret"})
		}, wantErr: connectionbinding.ErrInvalidCredentialBundle},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := &localRuntimeCredentialPool{}
			factory := &localRuntimeCredentialFactory{pool: pool}
			module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
			identity, err := module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
				func(_ context.Context, consume func(map[string]string) error) error { return test.read(consume) })
			require.ErrorIs(t, err, test.wantErr)
			require.Zero(t, identity)
			if test.name == "multiple calls" {
				require.Equal(t, 1, pool.closeCalls)
			} else {
				require.Zero(t, pool.closeCalls)
			}
		})
	}
}

func TestCheckLocalRuntimeCredentialPausedGateNeverReads(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	factory := &localRuntimeCredentialFactory{pool: &localRuntimeCredentialPool{}}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	pause, err := module.PauseSourceWork()
	require.NoError(t, err)
	defer func() { require.NoError(t, pause.Resume()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var readCalls atomic.Int32
	_, err = module.CheckLocalRuntimeCredential(ctx, binding, localRuntimeCredentialVersion,
		func(context.Context, func(map[string]string) error) error {
			readCalls.Add(1)
			return nil
		})
	require.ErrorIs(t, err, ErrLocalRuntimeCredentialCheckFailed)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, readCalls.Load())
	require.Zero(t, factory.localConnectionCalls)
}

func TestCheckLocalRuntimeCredentialHoldsLeaseThroughCloseAfterCancellation(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	pool := &localRuntimeCredentialPool{closeStarted: make(chan struct{}), closeRelease: make(chan struct{})}
	factory := &localRuntimeCredentialFactory{pool: pool}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := module.CheckLocalRuntimeCredential(ctx, binding, localRuntimeCredentialVersion,
			func(_ context.Context, consume func(map[string]string) error) error {
				return consume(map[string]string{"password": "local-secret"})
			})
		result <- err
	}()
	select {
	case <-pool.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("candidate close did not start")
	}
	pause, err := module.PauseSourceWork()
	require.NoError(t, err)
	cancel()
	drainCtx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, pause.WaitDrained(drainCtx), context.DeadlineExceeded)
	stop()
	close(pool.closeRelease)
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrLocalRuntimeCredentialCheckFailed)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("credential check did not return after pool close")
	}
	require.NoError(t, pause.WaitDrained(context.Background()))
	require.NoError(t, pause.Resume())
	require.Equal(t, 1, pool.closeCalls)
}

func TestCheckLocalRuntimeCredentialCleanupFailureQuarantinesLease(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	pool := &localRuntimeCredentialPool{closeErr: errors.New("local-secret close failure")}
	factory := &localRuntimeCredentialFactory{pool: pool}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	_, err := module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
		func(_ context.Context, consume func(map[string]string) error) error {
			_ = consume(map[string]string{"password": "local-secret"})
			return errors.New("reader masked local-secret cleanup failure")
		})
	require.ErrorIs(t, err, ErrLocalRuntimeCredentialCleanupFailed)
	require.NotContains(t, err.Error(), "local-secret")
	pause, pauseErr := module.PauseSourceWork()
	require.NoError(t, pauseErr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, pause.WaitDrained(ctx), context.DeadlineExceeded)
	cancel()
}

func TestCheckLocalRuntimeCredentialCleanupFailurePreservesCancellation(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	pool := &localRuntimeCredentialPool{
		closeErr: errors.New("local-secret close failure"), closeStarted: make(chan struct{}), closeRelease: make(chan struct{}),
	}
	factory := &localRuntimeCredentialFactory{pool: pool}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := module.CheckLocalRuntimeCredential(ctx, binding, localRuntimeCredentialVersion,
			func(_ context.Context, consume func(map[string]string) error) error {
				return consume(map[string]string{"password": "local-secret"})
			})
		result <- err
	}()
	select {
	case <-pool.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("candidate close did not start")
	}
	cancel()
	close(pool.closeRelease)
	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrLocalRuntimeCredentialCleanupFailed)
		require.ErrorIs(t, err, context.Canceled)
		require.NotContains(t, err.Error(), "local-secret")
	case <-time.After(time.Second):
		t.Fatal("credential check did not return after pool close")
	}
}

func TestCheckLocalRuntimeCredentialFactoryCleanupSentinelQuarantinesLease(t *testing.T) {
	for _, marker := range []error{analyticsduckdb.ErrTargetPoolCleanupFailed, analyticsruntime.ErrConnectionCleanupFailed} {
		t.Run(marker.Error(), func(t *testing.T) {
			binding := modulePoolBinding(t, time.Now().UTC())
			pool := &localRuntimeCredentialPool{}
			factory := &localRuntimeCredentialFactory{pool: pool, prepareErr: marker}
			module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
			_, err := module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
				func(_ context.Context, consume func(map[string]string) error) error {
					return consume(map[string]string{"password": "local-secret"})
				})
			require.ErrorIs(t, err, ErrLocalRuntimeCredentialCleanupFailed)
			require.Equal(t, 1, pool.closeCalls)
			pause, pauseErr := module.PauseSourceWork()
			require.NoError(t, pauseErr)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, pause.WaitDrained(ctx), context.DeadlineExceeded)
		})
	}
}

func TestCheckLocalRuntimeCredentialReaderPanicQuarantinesLease(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	factory := &localRuntimeCredentialFactory{pool: &localRuntimeCredentialPool{}}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	require.Panics(t, func() {
		_, _ = module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
			func(context.Context, func(map[string]string) error) error { panic("reader panic") })
	})
	pause, err := module.PauseSourceWork()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, pause.WaitDrained(ctx), context.DeadlineExceeded)
	cancel()
}

func TestCheckLocalRuntimeCredentialPreservesCleanupFailureOnPrepareError(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	pool := &localRuntimeCredentialPool{closeErr: errors.New("local-secret close failure")}
	factory := &localRuntimeCredentialFactory{pool: pool, prepareErr: errors.New("local-secret initialization failure")}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	_, err := module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
		func(_ context.Context, consume func(map[string]string) error) error {
			return consume(map[string]string{"password": "local-secret"})
		})
	require.ErrorIs(t, err, ErrLocalRuntimeCredentialCleanupFailed)
	require.NotContains(t, err.Error(), "local-secret")
	require.Equal(t, 1, pool.closeCalls)
}

func TestCheckLocalRuntimeCredentialRedactsReaderFailure(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	factory := &localRuntimeCredentialFactory{pool: &localRuntimeCredentialPool{}}
	module := localRuntimeCredentialModule(binding, &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}, factory)
	_, err := module.CheckLocalRuntimeCredential(context.Background(), binding, localRuntimeCredentialVersion,
		func(context.Context, func(map[string]string) error) error {
			return errors.New("reader leaked local-secret")
		})
	require.ErrorIs(t, err, ErrLocalRuntimeCredentialCheckFailed)
	require.NotContains(t, err.Error(), "local-secret")
}

type localRuntimeCredentialFactory struct {
	pool                 connectionbinding.RuntimePool
	prepareErr           error
	localConnectionCalls int
	prepareProviderCalls int
	identity             connectionbinding.CredentialIdentity
	fields               map[string]string
}

func (factory *localRuntimeCredentialFactory) Prepare(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	factory.prepareProviderCalls++
	return nil, errors.New("provider prepare must not run")
}

func (factory *localRuntimeCredentialFactory) WithLocalConnection(ctx context.Context, _ connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) (resultErr error) {
	factory.localConnectionCalls++
	factory.identity = snapshot.Identity()
	factory.fields = map[string]string{}
	if err := snapshot.Use(func(values map[string]string) error {
		for key, value := range values {
			factory.fields[key] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if factory.pool != nil {
		defer func() {
			if err := factory.pool.Close(); err != nil {
				resultErr = errors.Join(resultErr, analyticsduckdb.ErrTargetPoolCleanupFailed)
			}
		}()
	}
	if factory.prepareErr != nil {
		return factory.prepareErr
	}
	if factory.pool == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	if err := factory.pool.HealthCheck(ctx); err != nil {
		return err
	}
	return consume(logical)
}

func localRuntimeCredentialModule(
	binding connectionbinding.TargetBinding,
	catalog *credentialProbeCatalog,
	factory *localRuntimeCredentialFactory,
) *Module {
	return &Module{
		connectionBindings: catalog,
		connectionFactory:  factory,
		targetID:           binding.TargetID.String(),
		targetEnvironment:  binding.Scope.Environment,
		targetClass:        connectionbinding.TargetProduction,
	}
}

type localRuntimeCredentialPool struct {
	healthCalls  int
	closeCalls   int
	closeErr     error
	closeStarted chan struct{}
	closeRelease chan struct{}
}

func (pool *localRuntimeCredentialPool) HealthCheck(context.Context) error {
	pool.healthCalls++
	return nil
}

func (pool *localRuntimeCredentialPool) Close() error {
	pool.closeCalls++
	if pool.closeStarted != nil {
		close(pool.closeStarted)
		<-pool.closeRelease
	}
	return pool.closeErr
}
