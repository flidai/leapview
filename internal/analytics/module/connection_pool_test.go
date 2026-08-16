package module

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestConnectionAdministrationComposesTargetOwnedValidatedPoolDirectory(t *testing.T) {
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	binding := modulePoolBinding(t, now)
	repository := &moduleBindingCatalog{binding: binding}
	resolver := &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, now)}
	factory := &moduleRuntimePoolFactory{}
	module := &Module{
		connectionBindings: repository,
		targetResolvers:    connectionbinding.ResolverSet{Infisical: resolver},
		targetID:           binding.TargetID.String(),
		targetEnvironment:  binding.Scope.Environment,
		targetClass:        connectionbinding.TargetProduction,
		connectionFactory:  factory,
	}
	administration, err := module.NewConnectionAdministration(ConnectionAdministrationConfig{
		Authorize: func(context.Context, string, ConnectionAdministrationPermission, ConnectionTargetBinding) error {
			return nil
		},
		Dependencies:        moduleDependencyInspector{},
		Audit:               moduleRotationAuditNoop{},
		AdministrationAudit: moduleAdministrationAuditNoop{},
		Now:                 func() time.Time { return now },
		RefreshTimeout:      time.Second,
		MaxConcurrent:       1,
	})
	require.NoError(t, err)
	health, err := administration.Test(context.Background(), "operator-1", connectionbinding.BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
	})
	require.NoError(t, err)
	if resolver.calls != 1 || factory.calls != 1 || health.Health != connectionbinding.HealthHealthy ||
		health.ValidatedVersion != "secret:v2" || !health.HasActivePool {
		t.Fatalf("resolver=%d factory=%d health=%#v", resolver.calls, factory.calls, health)
	}
	if module.connectionPools == nil {
		t.Fatal("module did not retain the target-owned pool directory")
	}
	runtimeBindings, err := module.NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Authorize: func(context.Context, string, ConnectionTargetBinding) error {
			return nil
		},
		Audit:          moduleRotationAuditNoop{},
		Now:            func() time.Time { return now },
		RefreshTimeout: time.Second,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	leases, err := runtimeBindings.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "principal:author-1", Identity: projectgraph.ServingIdentity{ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment, GenerationID: "generation-1"}, TargetID: binding.TargetID,
		Requirements: []connectionbinding.Requirement{{
			ConnectionID:  binding.ConnectionID,
			ConnectorKind: binding.ConnectorKind,
		}},
	})
	require.NoError(t, err)
	evidence := leases.Evidence()
	if len(evidence) != 1 || evidence[0].ValidatedVersion != "secret:v2" ||
		resolver.calls != 1 || factory.calls != 1 {
		t.Fatalf(
			"runtime evidence=%#v resolver=%d factory=%d, want reused validated generation",
			evidence,
			resolver.calls,
			factory.calls,
		)
	}
	leases.Release()
	if err := module.Close(); err != nil {
		t.Fatal(err)
	}
	if !factory.pool.closed {
		t.Fatal("module close did not retire the active connection pool")
	}
}

func TestConnectionAdministrationRejectsBindingsForAnotherTarget(t *testing.T) {
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	binding := modulePoolBinding(t, now)
	repository := &moduleBindingCatalog{binding: binding}
	module := &Module{
		connectionBindings: repository,
		targetResolvers: connectionbinding.ResolverSet{
			Infisical: &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, now)},
		},
		targetID:          "this-target",
		targetEnvironment: binding.Scope.Environment,
		targetClass:       connectionbinding.TargetProduction,
		connectionFactory: &moduleRuntimePoolFactory{},
	}
	administration, err := module.NewConnectionAdministration(ConnectionAdministrationConfig{
		Authorize: func(context.Context, string, ConnectionAdministrationPermission, ConnectionTargetBinding) error {
			return nil
		},
		Dependencies:        moduleDependencyInspector{},
		Audit:               moduleRotationAuditNoop{},
		AdministrationAudit: moduleAdministrationAuditNoop{},
		Now:                 func() time.Time { return now },
		RefreshTimeout:      time.Second,
		MaxConcurrent:       1,
	})
	require.NoError(t, err)
	_, err = administration.Get(context.Background(), "operator-1", connectionbinding.BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
	})
	if !errors.Is(err, connectionbinding.ErrUnauthorizedBinding) {
		t.Fatalf("cross-target Get() error = %v", err)
	}
}

func TestCandidateRuntimeBindingRegistrationMakesOnlyItsValidatedGenerationAvailable(t *testing.T) {
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	binding := modulePoolBinding(t, now)
	module := &Module{
		connectionBindings: &moduleBindingCatalog{binding: binding},
		targetResolvers: connectionbinding.ResolverSet{
			Infisical: &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, now)},
		},
		targetID: binding.TargetID.String(), targetEnvironment: binding.Scope.Environment,
		targetClass:       connectionbinding.TargetProduction,
		connectionFactory: &moduleRuntimePoolFactory{},
	}
	t.Cleanup(func() { _ = module.Close() })
	leaser, err := module.NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Authorize: func(context.Context, string, ConnectionTargetBinding) error {
			return nil
		},
		Audit: moduleRotationAuditNoop{},
		Now:   func() time.Time { return now },
	})
	require.NoError(t, err)
	leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
		Actor: "author_1", Identity: projectgraph.ServingIdentity{ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment, GenerationID: "generation-1"}, TargetID: binding.TargetID,
		Requirements: []ConnectionRequirement{{
			ConnectionID:  binding.ConnectionID,
			ConnectorKind: binding.ConnectorKind,
		}},
	})
	require.NoError(t, err)
	registration, err := module.BindCandidateRuntime(
		"cand_1",
		binding.Scope.ProjectID,
		leases,
		nil,
	)
	require.NoError(t, err)
	resolver, ok := module.candidateRuntimeConnectionResolver(
		"cand_1",
		binding.Scope.ProjectID,
	)
	if !ok {
		t.Fatal("candidate resolver was not registered")
	}
	resolved, err := resolver.Resolve(
		t.Context(),
		binding.ConnectionID.String(),
		semanticmodel.Connection{Kind: binding.ConnectorKind},
	)
	require.NoError(t, err)
	if resolved.Host != binding.Endpoint.Host ||
		resolved.Auth["password"] != "source-secret" {
		t.Fatalf("resolved candidate connection = %#v", resolved)
	}
	clear(resolved.Auth)
	if err := registration.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := module.candidateRuntimeConnectionResolver(
		"cand_1",
		binding.Scope.ProjectID,
	); ok {
		t.Fatal("candidate resolver remained registered after lifetime close")
	}
}

func TestCandidateRuntimeBindingReplacementRemovalIsGenerationSafe(t *testing.T) {
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	binding := modulePoolBinding(t, now)
	module := &Module{
		connectionBindings: &moduleBindingCatalog{binding: binding},
		targetResolvers: connectionbinding.ResolverSet{
			Infisical: &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, now)},
		},
		targetID: binding.TargetID.String(), targetEnvironment: binding.Scope.Environment,
		targetClass:       connectionbinding.TargetProduction,
		connectionFactory: &moduleRuntimePoolFactory{},
	}
	t.Cleanup(func() { _ = module.Close() })
	leaser, err := module.NewRuntimeBindingLeaser(RuntimeBindingLeaserConfig{
		Authorize: func(context.Context, string, ConnectionTargetBinding) error {
			return nil
		},
		Audit: moduleRotationAuditNoop{},
		Now:   func() time.Time { return now },
	})
	require.NoError(t, err)
	acquire := func() *RuntimeBindingLeases {
		leases, err := leaser.Acquire(t.Context(), RuntimeBindingRequest{
			Actor: "author_1", Identity: projectgraph.ServingIdentity{ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment, GenerationID: "generation-1"}, TargetID: binding.TargetID,
		})
		require.NoError(t, err)
		return leases
	}
	first, err := module.BindCandidateRuntime(
		"cand_1",
		binding.Scope.ProjectID,
		acquire(),
		nil,
	)
	require.NoError(t, err)
	second, err := module.BindCandidateRuntime(
		"cand_1",
		binding.Scope.ProjectID,
		acquire(),
		nil,
	)
	require.NoError(t, err)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := module.candidateRuntimeConnectionResolver(
		"cand_1",
		binding.Scope.ProjectID,
	); !ok {
		t.Fatal("retiring the replaced registration removed the current generation")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := module.candidateRuntimeConnectionResolver(
		"cand_1",
		binding.Scope.ProjectID,
	); ok {
		t.Fatal("current registration remained after close")
	}
}

func TestCandidateRuntimeResolverPassesThroughOnlyDeclaredAuthoredConnections(t *testing.T) {
	resolver := runtimeBindingConnectionResolver{
		authored: map[string]string{"public_http": "http"},
	}
	logical := semanticmodel.Connection{
		Kind: "http", Scope: "https://example.test/public/",
	}
	resolved, err := resolver.Resolve(t.Context(), "public_http", logical)
	require.NoError(t, err)
	require.Equal(t, logical, resolved)

	_, err = resolver.Resolve(t.Context(), "undeclared", logical)
	require.ErrorIs(t, err, connectionbinding.ErrBindingNotFound)
	_, err = resolver.Resolve(
		t.Context(), "public_http", semanticmodel.Connection{Kind: "quack"},
	)
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
}

func TestConnectionAdministrationUsesExplicitEnvironmentResolverOnlyForDevelopmentTarget(t *testing.T) {
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	binding := modulePoolBinding(t, now)
	binding.TargetID = "lvinst_local"
	binding.Scope.Environment = "dev"
	binding.CredentialReference = connectionbinding.CredentialReference{
		ProjectID: "lvinst_local", Environment: "dev",
		SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	}
	repository := &moduleBindingCatalog{binding: binding}
	resolver := &moduleCredentialResolver{snapshot: modulePoolSnapshot(t, now)}
	module := &Module{
		connectionBindings: repository,
		targetResolvers:    connectionbinding.ResolverSet{Environment: resolver},
		targetID:           binding.TargetID.String(),
		targetEnvironment:  binding.Scope.Environment,
		targetClass:        connectionbinding.TargetDevelopment,
		connectionFactory:  &moduleRuntimePoolFactory{},
	}
	administration, err := module.NewConnectionAdministration(ConnectionAdministrationConfig{
		Authorize: func(context.Context, string, ConnectionAdministrationPermission, ConnectionTargetBinding) error {
			return nil
		},
		Dependencies:        moduleDependencyInspector{},
		Audit:               moduleRotationAuditNoop{},
		AdministrationAudit: moduleAdministrationAuditNoop{},
		Now:                 func() time.Time { return now },
		RefreshTimeout:      time.Second,
		MaxConcurrent:       1,
	})
	require.NoError(t, err)
	_, err = administration.Test(context.Background(), "operator-1", connectionbinding.BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
	})
	require.NoError(t, err)
	if resolver.calls != 1 {
		t.Fatalf("development environment resolver calls = %d", resolver.calls)
	}
}

func modulePoolBinding(t *testing.T, now time.Time) connectionbinding.TargetBinding {
	t.Helper()
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope: connectionbinding.BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint: connectionbinding.EndpointConfig{
			Host: "warehouse.internal", Port: 5432, Database: "analytics",
			SourceIdentity: "runtime", TLSMode: "verify-full",
		},
		CredentialReference: connectionbinding.CredentialReference{
			ProjectID: "project-1", Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse",
		},
		Enabled: true, Now: now,
	})
	require.NoError(t, err)
	return binding
}

func modulePoolSnapshot(t *testing.T, now time.Time) connectionbinding.CredentialSnapshot {
	t.Helper()
	snapshot, err := connectionbinding.NewCredentialSnapshot(
		map[string]string{"password": "source-secret"}, "secret:v2", now, now.Add(time.Hour),
	)
	require.NoError(t, err)
	return snapshot
}

type moduleBindingCatalog struct {
	binding connectionbinding.TargetBinding
}

func (catalog *moduleBindingCatalog) Create(_ context.Context, binding connectionbinding.TargetBinding) error {
	catalog.binding = binding
	return nil
}

func (catalog *moduleBindingCatalog) Binding(
	context.Context,
	connectionbinding.BindingScope,
	connectionbinding.TargetID,
	projectgraph.ResourceID,
) (connectionbinding.TargetBinding, error) {
	return catalog.binding, nil
}

func (catalog *moduleBindingCatalog) List(
	context.Context,
	connectionbinding.BindingScope,
	connectionbinding.TargetID,
) ([]connectionbinding.TargetBinding, error) {
	return []connectionbinding.TargetBinding{catalog.binding}, nil
}

func (catalog *moduleBindingCatalog) Save(
	_ context.Context,
	binding connectionbinding.TargetBinding,
	expected int64,
) (connectionbinding.TargetBinding, error) {
	if catalog.binding.Revision != expected {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrIncompatibleBinding
	}
	catalog.binding = binding
	return binding, nil
}

type moduleCredentialResolver struct {
	snapshot connectionbinding.CredentialSnapshot
	calls    int
}

func (resolver *moduleCredentialResolver) Resolve(
	context.Context,
	connectionbinding.CredentialReference,
) (connectionbinding.CredentialSnapshot, error) {
	resolver.calls++
	return resolver.snapshot, nil
}

type moduleRuntimePoolFactory struct {
	calls int
	pool  *moduleRuntimePool
}

func (factory *moduleRuntimePoolFactory) Prepare(
	_ context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (connectionbinding.RuntimePool, error) {
	factory.calls++
	factory.pool = &moduleRuntimePool{
		connection: semanticmodel.Connection{
			Kind: binding.ConnectorKind, Host: binding.Endpoint.Host,
			Port: binding.Endpoint.Port, Database: binding.Endpoint.Database,
		},
	}
	if err := snapshot.Use(func(values map[string]string) error {
		factory.pool.connection.Auth = make(semanticmodel.ConnectionAuth, len(values))
		for key, value := range values {
			factory.pool.connection.Auth[key] = value
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return factory.pool, nil
}

type moduleRuntimePool struct {
	closed     bool
	connection semanticmodel.Connection
}

func (*moduleRuntimePool) HealthCheck(context.Context) error { return nil }
func (pool *moduleRuntimePool) Resolve(
	_ context.Context,
	_ string,
	logical semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	resolved := pool.connection
	resolved.Path = logical.Path
	resolved.Auth = maps.Clone(pool.connection.Auth)
	return resolved, nil
}
func (pool *moduleRuntimePool) Close() error {
	clear(pool.connection.Auth)
	pool.closed = true
	return nil
}

type moduleDependencyInspector struct{}

func (moduleDependencyInspector) Dependents(
	context.Context,
	ConnectionTargetBinding,
) ([]ConnectionBindingDependency, error) {
	return nil, nil
}
