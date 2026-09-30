package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

const testCredentialProbeVersion = "22222222-2222-4222-8222-222222222222"

func TestProbeCredentialUsesTransientCandidateAndDoesNotMutateBinding(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}
	pool := &credentialProbeRuntimePool{}
	factory := &credentialProbeFactory{pool: pool}
	module := credentialProbeModule(binding, catalog, factory)
	version := testCredentialProbeVersion

	err := module.ProbeCredential(context.Background(), binding, version, map[string]string{"password": "draft-secret"})
	require.NoError(t, err)
	require.Equal(t, 1, factory.calls)
	require.Equal(t, binding.ID, factory.binding.ID)
	require.Equal(t, binding.Revision, factory.binding.Revision)
	require.Equal(t, "credential-draft:"+version, factory.version)
	require.Equal(t, map[string]string{"password": "draft-secret"}, factory.fields)
	require.Equal(t, 1, pool.healthCalls)
	require.Equal(t, 1, pool.closeCalls)
	require.NotZero(t, factory.deadline)
	require.NotZero(t, pool.healthDeadline)
	require.NotZero(t, pool.closeDeadline)
	require.True(t, factory.deadlineOK)
	deadline := factory.deadline
	require.True(t, time.Until(deadline) <= credentialProbeTimeout)
	require.Greater(t, time.Until(deadline), time.Duration(0))
	require.Equal(t, 2, catalog.bindingCalls)
	require.Zero(t, catalog.saveCalls)
	require.Nil(t, module.connectionPools)
	require.Equal(t, binding.Revision, catalog.bindings[len(catalog.bindings)-1].Revision)
}

func TestProbeCredentialRejectsUnsupportedBindingAndCredentialShape(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	for _, test := range []struct {
		name    string
		mutate  func(*connectionbinding.TargetBinding)
		fields  map[string]string
		wantErr error
	}{
		{name: "connection string", fields: map[string]string{"connection_string": "postgres://elsewhere"}, wantErr: connectionbinding.ErrInvalidCredentialBundle},
		{name: "extra field", fields: map[string]string{"password": "secret", "connection_string": "postgres://elsewhere"}, wantErr: connectionbinding.ErrInvalidCredentialBundle},
		{name: "mysql", mutate: func(binding *connectionbinding.TargetBinding) { binding.ConnectorKind = "mysql" }, fields: map[string]string{"password": "secret"}, wantErr: connectionbinding.ErrIncompatibleBinding},
		{name: "workload identity", mutate: func(binding *connectionbinding.TargetBinding) {
			binding.AuthenticationMode = connectionbinding.AuthenticationWorkload
			binding.CredentialReference = connectionbinding.CredentialReference{}
		}, fields: map[string]string{"password": "secret"}, wantErr: connectionbinding.ErrIncompatibleBinding},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := binding
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{candidate}}
			factory := &credentialProbeFactory{pool: &credentialProbeRuntimePool{}}
			module := credentialProbeModule(binding, catalog, factory)
			err := module.ProbeCredential(context.Background(), candidate, testCredentialProbeVersion, test.fields)
			require.ErrorIs(t, err, test.wantErr)
			require.Zero(t, factory.calls)
			require.Zero(t, catalog.bindingCalls)
		})
	}
}

func TestProbeCredentialRejectsBindingDriftAfterCandidateIsClosed(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	configuration := binding.Configuration()
	configuration.Endpoint.Host = "replacement.internal"
	changed, err := binding.UpdateConfiguration(configuration, now.Add(time.Second))
	require.NoError(t, err)
	catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding, changed}}
	pool := &credentialProbeRuntimePool{}
	factory := &credentialProbeFactory{pool: pool}
	module := credentialProbeModule(binding, catalog, factory)

	err = module.ProbeCredential(context.Background(), binding, testCredentialProbeVersion, map[string]string{"password": "draft-secret"})
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	require.Equal(t, 1, pool.healthCalls)
	require.Equal(t, 1, pool.closeCalls)
	require.Zero(t, catalog.saveCalls)
}

func TestProbeCredentialFailsClosedWhenCleanupIsUnconfirmed(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}
	pool := &credentialProbeRuntimePool{closeErr: errors.New("draft-secret leaked in driver error")}
	factory := &credentialProbeFactory{pool: pool}
	module := credentialProbeModule(binding, catalog, factory)

	err := module.ProbeCredential(context.Background(), binding, testCredentialProbeVersion, map[string]string{"password": "draft-secret"})
	require.ErrorIs(t, err, ErrCredentialProbeCleanupFailed)
	require.NotContains(t, err.Error(), "draft-secret")
	require.Equal(t, 1, pool.closeCalls)
	require.Equal(t, 1, catalog.bindingCalls)
	require.Zero(t, catalog.saveCalls)
}

func TestProbeCredentialFailsWhenParentContextExpiresDuringFinalBindingCheck(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	ctx, cancel := context.WithCancel(context.Background())
	catalog := &credentialProbeCatalog{
		bindings: []connectionbinding.TargetBinding{binding}, cancelOnBindingCall: 2, cancel: cancel,
	}
	pool := &credentialProbeRuntimePool{}
	factory := &credentialProbeFactory{pool: pool}
	module := credentialProbeModule(binding, catalog, factory)

	err := module.ProbeCredential(ctx, binding, testCredentialProbeVersion, map[string]string{"password": "draft-secret"})
	require.ErrorIs(t, err, ErrCredentialProbeFailed)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, pool.closeCalls)
	require.Zero(t, catalog.saveCalls)
}

func TestProbeCredentialFailsWhenHealthAndCleanupOutliveParentDeadline(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	catalog := &credentialProbeCatalog{bindings: []connectionbinding.TargetBinding{binding}}
	pool := &credentialProbeRuntimePool{waitForHealthDeadline: true, closeWithContextError: true}
	factory := &credentialProbeFactory{pool: pool}
	module := credentialProbeModule(binding, catalog, factory)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := module.ProbeCredential(ctx, binding, testCredentialProbeVersion, map[string]string{"password": "draft-secret"})
	require.ErrorIs(t, err, ErrCredentialProbeCleanupFailed)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotContains(t, err.Error(), "draft-secret")
	require.Equal(t, 1, pool.healthCalls)
	require.Equal(t, 1, pool.closeCalls)
	require.Equal(t, 1, catalog.bindingCalls)
	require.Zero(t, catalog.saveCalls)
}

func TestCredentialProbePolicyIdentityNamesTargetAndOutboundPolicy(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	module := credentialProbeModule(binding, &credentialProbeCatalog{}, &credentialProbeFactory{})
	module.production = true
	require.Equal(t,
		"target-postgres-password-read-only-probe-v1/target-production/explicit-private-target-egress-v1",
		module.CredentialProbePolicyIdentity(),
	)
	module.targetClass = connectionbinding.TargetDevelopment
	require.Equal(t,
		"target-postgres-password-read-only-probe-v1/target-development/explicit-private-target-egress-v1",
		module.CredentialProbePolicyIdentity(),
	)
	module.targetClass = "unknown"
	require.Empty(t, module.CredentialProbePolicyIdentity())
}

func credentialProbeModule(
	binding connectionbinding.TargetBinding,
	catalog *credentialProbeCatalog,
	factory *credentialProbeFactory,
) *Module {
	return &Module{
		connectionBindings: catalog,
		connectionFactory:  factory,
		targetID:           binding.TargetID.String(),
		targetEnvironment:  binding.Scope.Environment,
		targetClass:        connectionbinding.TargetProduction,
	}
}

type credentialProbeCatalog struct {
	bindings            []connectionbinding.TargetBinding
	bindingCalls        int
	saveCalls           int
	cancelOnBindingCall int
	cancel              context.CancelFunc
}

func (catalog *credentialProbeCatalog) Create(context.Context, connectionbinding.TargetBinding) error {
	return errors.New("unexpected binding create")
}

func (catalog *credentialProbeCatalog) Binding(
	_ context.Context,
	_ connectionbinding.BindingScope,
	_ connectionbinding.TargetID,
	_ projectgraph.ResourceID,
) (connectionbinding.TargetBinding, error) {
	if len(catalog.bindings) == 0 {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	index := min(catalog.bindingCalls, len(catalog.bindings)-1)
	catalog.bindingCalls++
	binding := catalog.bindings[index]
	if catalog.cancel != nil && catalog.bindingCalls == catalog.cancelOnBindingCall {
		catalog.cancel()
	}
	return binding, nil
}

func (catalog *credentialProbeCatalog) Save(context.Context, connectionbinding.TargetBinding, int64) (connectionbinding.TargetBinding, error) {
	catalog.saveCalls++
	return connectionbinding.TargetBinding{}, errors.New("unexpected binding save")
}

func (catalog *credentialProbeCatalog) List(context.Context, connectionbinding.BindingScope, connectionbinding.TargetID) ([]connectionbinding.TargetBinding, error) {
	return append([]connectionbinding.TargetBinding(nil), catalog.bindings...), nil
}

type credentialProbeFactory struct {
	calls        int
	binding      connectionbinding.TargetBinding
	version      string
	fields       map[string]string
	deadline     time.Time
	deadlineOK   bool
	pool         connectionbinding.RuntimePool
	prepareError error
}

func (factory *credentialProbeFactory) Prepare(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (connectionbinding.RuntimePool, error) {
	factory.calls++
	factory.binding = binding
	factory.version = snapshot.ProviderVersion()
	factory.deadline, factory.deadlineOK = ctx.Deadline()
	factory.fields = map[string]string{}
	if err := snapshot.Use(func(values map[string]string) error {
		for key, value := range values {
			factory.fields[key] = value
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return factory.pool, factory.prepareError
}

type credentialProbeRuntimePool struct {
	healthCalls           int
	closeCalls            int
	healthDeadline        time.Time
	closeDeadline         time.Time
	healthErr             error
	closeErr              error
	waitForHealthDeadline bool
	closeWithContextError bool
}

func (pool *credentialProbeRuntimePool) HealthCheck(ctx context.Context) error {
	pool.healthCalls++
	pool.healthDeadline, _ = ctx.Deadline()
	if pool.waitForHealthDeadline {
		<-ctx.Done()
		return ctx.Err()
	}
	return pool.healthErr
}

func (pool *credentialProbeRuntimePool) Close() error {
	pool.closeCalls++
	return pool.closeErr
}

func (pool *credentialProbeRuntimePool) CloseContext(ctx context.Context) error {
	pool.closeCalls++
	pool.closeDeadline, _ = ctx.Deadline()
	if pool.closeWithContextError {
		return ctx.Err()
	}
	return pool.closeErr
}
