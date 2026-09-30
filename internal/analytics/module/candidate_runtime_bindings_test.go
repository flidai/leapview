package module

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

const candidateLocalCredentialVersion = "33333333-3333-4333-8333-333333333333"

func TestCandidateRuntimeBindsExactLocalConnectionAndRetainsSeparateVersionEvidence(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	module := candidateRuntimeTestModule(t, binding)
	local := candidateLocalConnectionFor(binding, candidateScopedResolverFunc(func(_ context.Context, name string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
		require.Equal(t, binding.ConnectionID.String(), name)
		require.Equal(t, "postgres", logical.Kind)
		return consume(semanticmodel.Connection{Kind: "postgres", Host: "local-host"})
	}))
	registration, err := module.BindCandidateRuntime("cand_local", binding.Scope.ProjectID, &RuntimeBindingLeases{}, nil, []CandidateLocalConnection{local})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registration.Close() })

	resolver, ok := module.candidateRuntimeConnectionResolver("cand_local", binding.Scope.ProjectID)
	require.True(t, ok)
	var resolved semanticmodel.Connection
	err = resolver.WithConnection(t.Context(), binding.ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, func(value semanticmodel.Connection) error {
		resolved = value
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "local-host", resolved.Host)

	evidence := registration.Evidence()
	require.Len(t, evidence, 1)
	require.Equal(t, binding.ConnectionID, evidence[0].Binding.ConnectionID)
	require.Empty(t, evidence[0].Binding.ValidatedVersion, "local version must not be stored as provider evidence")
	require.Equal(t, candidateLocalCredentialVersion, evidence[0].CredentialVersionID)
}

func TestCandidateRuntimeLocalDispatchDoesNotFallBackToProvider(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	module := candidateRuntimeTestModule(t, binding)
	want := errors.New("local credential callback denied")
	local := candidateLocalConnectionFor(binding, candidateScopedResolverFunc(func(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error {
		return want
	}))
	registration, err := module.BindCandidateRuntime("cand_local", binding.Scope.ProjectID, &RuntimeBindingLeases{}, nil, []CandidateLocalConnection{local})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registration.Close() })
	resolver, ok := module.candidateRuntimeConnectionResolver("cand_local", binding.Scope.ProjectID)
	require.True(t, ok)
	err = resolver.WithConnection(t.Context(), binding.ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		t.Fatal("consumer ran after local callback denied")
		return nil
	})
	require.ErrorIs(t, err, want, "local errors must be returned directly instead of trying a provider pool")
}

func TestCandidateRuntimeRejectsInvalidOrOverlappingLocalConnectionEvidence(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	for _, test := range []struct {
		name     string
		mutate   func(*CandidateLocalConnection)
		authored []CandidateAuthoredConnection
	}{
		{name: "wrong target", mutate: func(value *CandidateLocalConnection) { value.Evidence.TargetID = "another-target" }},
		{name: "wrong project", mutate: func(value *CandidateLocalConnection) { value.Evidence.Scope.ProjectID = "another-project" }},
		{name: "wrong environment", mutate: func(value *CandidateLocalConnection) { value.Evidence.Scope.Environment = "dev" }},
		{name: "provider version present", mutate: func(value *CandidateLocalConnection) { value.Evidence.ValidatedVersion = "provider:v2" }},
		{name: "wrong connector", mutate: func(value *CandidateLocalConnection) { value.Evidence.ConnectorKind = "mysql" }},
		{name: "public access", mutate: func(value *CandidateLocalConnection) { value.Evidence.Access = semanticmodel.ConnectionAccessPublic }},
		{name: "invalid local version", mutate: func(value *CandidateLocalConnection) { value.CredentialVersionID = "not-a-uuid" }},
		{name: "missing resolver", mutate: func(value *CandidateLocalConnection) { value.Resolver = nil }},
		{name: "overlaps authored", authored: []CandidateAuthoredConnection{{ConnectionID: binding.ConnectionID, ConnectorKind: "http"}}},
		{name: "duplicate local id", mutate: func(*CandidateLocalConnection) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := candidateRuntimeTestModule(t, binding)
			local := candidateLocalConnectionFor(binding, candidateScopedResolverFunc(func(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error {
				return nil
			}))
			if test.mutate != nil {
				test.mutate(&local)
			}
			locals := []CandidateLocalConnection{local}
			if test.name == "duplicate local id" {
				locals = append(locals, candidateLocalConnectionFor(binding, local.Resolver))
			}
			_, err := module.BindCandidateRuntime("cand_local", binding.Scope.ProjectID, &RuntimeBindingLeases{}, test.authored, locals)
			require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
			if _, ok := module.candidateRuntimeConnectionResolver("cand_local", binding.Scope.ProjectID); ok {
				t.Fatal("invalid local evidence registered a candidate resolver")
			}
		})
	}
}

func TestCandidateRuntimeRejectsLocalEvidenceOverlappingProviderLease(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	module := candidateRuntimeTestModule(t, binding)
	leaser, err := module.NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Authorize: func(context.Context, string, ConnectionTargetBinding) error { return nil },
		Audit:     moduleRotationAuditNoop{}, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor:        "author_1",
		Identity:     projectgraph.ServingIdentity{ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment, GenerationID: "generation-1"},
		TargetID:     binding.TargetID,
		Requirements: []ConnectionRequirement{{ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind}},
	})
	require.NoError(t, err)
	local := candidateLocalConnectionFor(binding, candidateScopedResolverFunc(func(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error {
		return nil
	}))
	_, err = module.BindCandidateRuntime("cand_local", binding.Scope.ProjectID, leases, nil, []CandidateLocalConnection{local})
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	leases.Release()
}

func TestCandidateRuntimeCloseRetiresRetainedLocalResolverAfterInFlightCleanup(t *testing.T) {
	binding := modulePoolBinding(t, time.Now().UTC())
	module := candidateRuntimeTestModule(t, binding)
	entered := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	local := candidateLocalConnectionFor(binding, candidateScopedResolverFunc(func(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error {
		calls.Add(1)
		close(entered)
		<-finish
		return nil
	}))
	registration, err := module.BindCandidateRuntime("cand_local", binding.Scope.ProjectID, &RuntimeBindingLeases{}, nil, []CandidateLocalConnection{local})
	require.NoError(t, err)
	resolver, ok := module.candidateRuntimeConnectionResolver("cand_local", binding.Scope.ProjectID)
	require.True(t, ok)
	used := make(chan error, 1)
	go func() {
		used <- resolver.WithConnection(t.Context(), binding.ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error { return nil })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("local callback did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- registration.Close() }()
	require.Eventually(t, func() bool {
		_, exists := module.candidateRuntimeConnectionResolver("cand_local", binding.Scope.ProjectID)
		return !exists
	}, time.Second, time.Millisecond)
	select {
	case err := <-closed:
		t.Fatalf("registration closed before local cleanup finished: %v", err)
	default:
	}
	close(finish)
	require.NoError(t, <-used)
	require.NoError(t, <-closed)

	err = resolver.WithConnection(t.Context(), binding.ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		t.Fatal("retained resolver ran a callback after registration close")
		return nil
	})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	require.EqualValues(t, 1, calls.Load())
}

func TestWithLocalRuntimeConnectionValidatesCurrentBindingAndDelegates(t *testing.T) {
	now := time.Now().UTC()
	binding := modulePoolBinding(t, now)
	factory := &candidateLocalRuntimeFactory{}
	module := candidateRuntimeTestModule(t, binding)
	module.connectionFactory = factory
	snapshot, err := connectionbinding.NewLocalCredentialSnapshot(map[string]string{"password": "local-secret"}, candidateLocalCredentialVersion, now, time.Time{})
	require.NoError(t, err)
	defer snapshot.Destroy()
	consumeCalled := false
	err = module.WithLocalRuntimeConnection(t.Context(), binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		consumeCalled = true
		return nil
	})
	require.NoError(t, err)
	require.True(t, consumeCalled)
	require.Equal(t, 1, factory.calls)
	require.Equal(t, binding, factory.binding)
	require.Equal(t, snapshot.Identity(), factory.identity)

	drifted := binding
	drifted.Revision++
	err = module.WithLocalRuntimeConnection(t.Context(), drifted, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error { return nil })
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	require.Equal(t, 1, factory.calls, "binding drift must be rejected before local pool acquisition")
}

func candidateRuntimeTestModule(t *testing.T, binding connectionbinding.TargetBinding) *Module {
	t.Helper()
	return &Module{
		connectionBindings: &moduleBindingCatalog{binding: binding},
		targetResolvers: connectionbinding.ResolverSet{
			Infisical: &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, time.Now().UTC())},
		},
		targetID: binding.TargetID.String(), targetEnvironment: binding.Scope.Environment,
		targetClass: connectionbinding.TargetProduction, connectionFactory: &moduleRuntimePoolFactory{},
	}
}

func candidateLocalConnectionFor(binding connectionbinding.TargetBinding, resolver analyticsruntime.ConnectionResolver) CandidateLocalConnection {
	evidence := binding.Evidence()
	evidence.ValidatedVersion = ""
	return CandidateLocalConnection{Evidence: evidence, CredentialVersionID: candidateLocalCredentialVersion, Resolver: resolver}
}

type candidateScopedResolverFunc func(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error

func (resolver candidateScopedResolverFunc) WithConnection(ctx context.Context, name string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
	return resolver(ctx, name, logical, consume)
}

type candidateLocalRuntimeFactory struct {
	calls    int
	binding  connectionbinding.TargetBinding
	identity connectionbinding.CredentialIdentity
}

func (factory *candidateLocalRuntimeFactory) Prepare(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	return nil, connectionbinding.ErrProviderUnavailable
}

func (factory *candidateLocalRuntimeFactory) WithLocalConnection(_ context.Context, binding connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
	factory.calls++
	factory.binding = binding
	factory.identity = snapshot.Identity()
	return consume(semanticmodel.Connection{Kind: logical.Kind})
}

var _ analyticsruntime.ConnectionResolver = candidateScopedResolverFunc(nil)
var _ localRuntimeConnectionConsumer = (*candidateLocalRuntimeFactory)(nil)
