package module

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestActiveRuntimeResolverUsesReleasePinnedCredentialVersion(t *testing.T) {
	binding := activeTestBinding(t)
	evidence := binding.Evidence()
	versioned := &activeVersionedResolver{values: map[string]string{"token": "pinned-token"}}
	module := activeTestModule(binding, versioned, ActiveRuntimeBindingEvidence{
		BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID,
		ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision,
		ValidatedVersion: "secret-quack:v7", EndpointConfigHash: evidence.EndpointConfigHash,
	})
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}

	resolved, err := resolveTestConnection(resolver, context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.NoError(t, err)
	require.Equal(t, "secret-quack:v7", versioned.version)
	require.Equal(t, "pinned-token", resolved.Auth["token"])
	require.Equal(t, 1, module.connectionFactory.(*activePoolFactory).healthChecks)
}

func TestActiveRuntimeResolverRejectsBindingConfigurationChangedAfterValidation(t *testing.T) {
	binding := activeTestBinding(t)
	evidence := binding.Evidence()
	versioned := &activeVersionedResolver{values: map[string]string{"token": "must-not-be-read"}}
	module := activeTestModule(binding, versioned, ActiveRuntimeBindingEvidence{
		BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID,
		ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision,
		ValidatedVersion: "secret-quack:v7", EndpointConfigHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}

	_, err := resolveTestConnection(resolver, context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	require.Zero(t, versioned.calls)
}

func TestActiveRuntimeResolverKeepsReleaseVersionWhenBindingRotatesAfterPromotion(t *testing.T) {
	binding := activeTestBinding(t)
	evidence := binding.Evidence()
	binding.Revision++
	binding.ValidatedVersion = "secret-quack:v8"
	versioned := &activeVersionedResolver{values: map[string]string{"token": "release-v7-token"}}
	module := activeTestModule(binding, versioned, ActiveRuntimeBindingEvidence{
		BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID,
		ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision,
		ValidatedVersion: "secret-quack:v7", EndpointConfigHash: evidence.EndpointConfigHash,
	})
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}

	resolved, err := resolveTestConnection(resolver, context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.NoError(t, err)
	require.Equal(t, "secret-quack:v7", versioned.version)
	require.Equal(t, "release-v7-token", resolved.Auth["token"])
}

func TestActiveRuntimeResolverFailsClosedWhenReleaseBindingEvidenceIsMissing(t *testing.T) {
	binding := activeTestBinding(t)
	module := activeTestModule(binding, &activeVersionedResolver{}, ActiveRuntimeBindingEvidence{})
	module.activeRuntimeBindingEvidence = activeEvidenceSource{}
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}

	_, err := resolveTestConnection(resolver, context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrBindingNotFound)
}

func TestActiveRuntimeResolverFailsClosedWhenTargetBindingRuntimeIsUnconfigured(t *testing.T) {
	resolver := &activeRuntimeConnectionResolver{module: &Module{}}
	_, err := resolveTestConnection(resolver, t.Context(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
}

func TestActiveRuntimeResolverLeavesCredentialFreeAuthoredConnectionUnbound(t *testing.T) {
	source := &activeFlakyEvidenceSource{}
	module := &Module{activeRuntimeBindingEvidence: source}
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}
	logical := semanticmodel.Connection{Kind: "http", Scope: "https://example.test/public/", Auth: semanticmodel.ConnectionAuth{"token": "stale-runtime-value"}}

	resolved, err := resolveTestConnection(resolver, context.Background(), "public", logical)
	require.NoError(t, err)
	require.Equal(t, "stale-runtime-value", logical.Auth["token"])
	require.Nil(t, resolved.Auth)
	logical.Auth = nil
	require.Equal(t, logical, resolved)
	require.Zero(t, source.calls)
}

func TestActiveRuntimeResolverPublicTargetBindingSkipsCredentialResolver(t *testing.T) {
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_public_s3", TargetID: "production", ConnectionID: "public_files", ConnectorKind: "s3",
		AuthenticationMode: connectionbinding.AuthenticationNone,
		Scope:              connectionbinding.BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint:           connectionbinding.EndpointConfig{ObjectScope: "s3://public/"}, Enabled: true, Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	evidence := binding.Evidence()
	evidence.Access = semanticmodel.ConnectionAccessPublic
	evidence.ValidatedVersion = connectionbinding.NoAuthProviderVersion
	versioned := &activeVersionedResolver{}
	module := activeTestModule(binding, versioned, ActiveRuntimeBindingEvidence{
		BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID, ConnectorKind: evidence.ConnectorKind,
		Revision: evidence.BindingRevision, ValidatedVersion: evidence.ValidatedVersion,
		EndpointConfigHash: evidence.EndpointConfigHash, Access: semanticmodel.ConnectionAccessPublic,
	})
	resolver := &activeRuntimeConnectionResolver{module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod"}
	resolved, err := resolveTestConnection(resolver, context.Background(), "public_files", semanticmodel.Connection{Kind: "s3", Access: semanticmodel.ConnectionAccessPublic})
	require.NoError(t, err)
	require.Zero(t, versioned.calls)
	require.Equal(t, semanticmodel.ConnectionAccessPublic, resolved.Access)
}

func TestActiveRuntimeResolverRetriesTransientBindingEvidenceFailure(t *testing.T) {
	binding := activeTestBinding(t)
	evidence := binding.Evidence()
	source := &activeFlakyEvidenceSource{values: []ActiveRuntimeBindingEvidence{{
		BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID,
		ConnectorKind: evidence.ConnectorKind, Revision: evidence.BindingRevision,
		ValidatedVersion: "secret-quack:v7", EndpointConfigHash: evidence.EndpointConfigHash,
	}}}
	module := activeTestModule(binding, &activeVersionedResolver{values: map[string]string{"token": "pinned-token"}}, ActiveRuntimeBindingEvidence{})
	module.activeRuntimeBindingEvidence = source
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}

	_, err := resolveTestConnection(resolver, context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	resolved, err := resolveTestConnection(resolver, context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.NoError(t, err)
	require.Equal(t, "pinned-token", resolved.Auth["token"])
	require.Equal(t, 2, source.calls)
}

func activeTestBinding(t *testing.T) connectionbinding.TargetBinding {
	t.Helper()
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_quack", TargetID: "production", ConnectionID: "quack",
		ConnectorKind: "quack", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope:    connectionbinding.BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint: connectionbinding.EndpointConfig{Host: "quack.example.com", Port: 443, TLSMode: "require"},
		CredentialReference: connectionbinding.CredentialReference{
			ProjectID: "infisical-project", Environment: "prod", SecretPath: "/leapview", SecretKey: "quack",
		},
		Enabled: true, Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	return binding
}

func activeTestModule(
	binding connectionbinding.TargetBinding,
	resolver connectionbinding.CredentialResolver,
	evidence ActiveRuntimeBindingEvidence,
) *Module {
	return &Module{
		connectionBindings: activeBindingCatalog{binding: binding},
		targetResolvers:    connectionbinding.ResolverSet{Infisical: resolver},
		targetID:           "production", targetEnvironment: "prod", targetClass: connectionbinding.TargetProduction,
		connectionFactory:            &activePoolFactory{},
		activeRuntimeBindingEvidence: activeEvidenceSource{values: []ActiveRuntimeBindingEvidence{evidence}},
	}
}

type activeEvidenceSource struct {
	values []ActiveRuntimeBindingEvidence
}

type activeFlakyEvidenceSource struct {
	values []ActiveRuntimeBindingEvidence
	calls  int
}

func (source *activeFlakyEvidenceSource) BindingEvidence(context.Context, string, string) ([]ActiveRuntimeBindingEvidence, error) {
	source.calls++
	if source.calls == 1 {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	return append([]ActiveRuntimeBindingEvidence(nil), source.values...), nil
}

func (source activeEvidenceSource) BindingEvidence(context.Context, string, string) ([]ActiveRuntimeBindingEvidence, error) {
	return append([]ActiveRuntimeBindingEvidence(nil), source.values...), nil
}

type activeBindingCatalog struct {
	binding connectionbinding.TargetBinding
}

func (catalog activeBindingCatalog) Create(context.Context, connectionbinding.TargetBinding) error {
	return nil
}
func (catalog activeBindingCatalog) Binding(context.Context, connectionbinding.BindingScope, connectionbinding.TargetID, projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	return catalog.binding, nil
}
func (catalog activeBindingCatalog) List(context.Context, connectionbinding.BindingScope, connectionbinding.TargetID) ([]connectionbinding.TargetBinding, error) {
	return []connectionbinding.TargetBinding{catalog.binding}, nil
}
func (catalog activeBindingCatalog) Save(context.Context, connectionbinding.TargetBinding, int64) (connectionbinding.TargetBinding, error) {
	return connectionbinding.TargetBinding{}, errors.New("unexpected save")
}

type activeVersionedResolver struct {
	values  map[string]string
	version string
	calls   int
}

func (resolver *activeVersionedResolver) Resolve(context.Context, connectionbinding.CredentialReference) (connectionbinding.CredentialSnapshot, error) {
	return connectionbinding.CredentialSnapshot{}, errors.New("latest resolution must not be used")
}
func (resolver *activeVersionedResolver) ResolveVersion(_ context.Context, _ connectionbinding.CredentialReference, version string) (connectionbinding.CredentialSnapshot, error) {
	resolver.calls++
	resolver.version = version
	return connectionbinding.NewCredentialSnapshot(resolver.values, version, time.Now(), time.Now().Add(time.Hour))
}

type activePoolFactory struct {
	healthChecks int
	closes       int
	closeErr     error
	closePanic   bool
	prepareErr   error
	healthErr    error
	closed       bool
}

func (factory *activePoolFactory) Prepare(_ context.Context, _ connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	values := map[string]string{}
	if snapshot.ProviderVersion() == connectionbinding.NoAuthProviderVersion {
		return &activeRuntimePool{factory: factory, values: values}, factory.prepareErr
	}
	if err := snapshot.Use(func(source map[string]string) error {
		for key, value := range source {
			values[key] = value
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &activeRuntimePool{factory: factory, values: values}, factory.prepareErr
}

type activeRuntimePool struct {
	factory *activePoolFactory
	values  map[string]string
}

func (pool *activeRuntimePool) HealthCheck(context.Context) error {
	pool.factory.healthChecks++
	return pool.factory.healthErr
}
func (pool *activeRuntimePool) Close() error {
	pool.factory.closes++
	pool.factory.closed = true
	clear(pool.values)
	if pool.factory.closePanic {
		panic("private-driver-diagnostic")
	}
	return pool.factory.closeErr
}
func (pool *activeRuntimePool) WithConnection(_ context.Context, _ string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
	if len(pool.values) > 0 {
		logical.Auth = map[string]any{"token": pool.values["token"]}
	} else {
		logical.Auth = nil
	}
	defer clear(logical.Auth)
	return consume(logical)
}

// Value assertions deliberately copy inside the callback; production consumers
// must finish using authentication before WithConnection returns.
func resolveTestConnection(resolver analyticsruntime.ConnectionResolver, ctx context.Context, name string, logical semanticmodel.Connection) (resolved semanticmodel.Connection, err error) {
	err = resolver.WithConnection(ctx, name, logical, func(connection semanticmodel.Connection) error {
		resolved = connection
		resolved.Auth = maps.Clone(connection.Auth)
		return nil
	})
	return resolved, err
}

func TestActiveRuntimeConnectionLifetimeIncludesConsumerAndCleanup(t *testing.T) {
	for _, scenario := range []string{"success", "consumer error", "close error", "close panic", "partial prepare", "health failure", "consumer panic"} {
		t.Run(scenario, func(t *testing.T) {
			binding := activeTestBinding(t)
			evidence := binding.Evidence()
			module := activeTestModule(binding, &activeVersionedResolver{values: map[string]string{"token": "private-token"}}, ActiveRuntimeBindingEvidence{
				BindingID: evidence.BindingID, ConnectionID: evidence.ConnectionID, ConnectorKind: evidence.ConnectorKind,
				Revision: evidence.BindingRevision, ValidatedVersion: "secret-quack:v7", EndpointConfigHash: evidence.EndpointConfigHash,
			})
			factory := module.connectionFactory.(*activePoolFactory)
			privateErr := errors.New("private-driver-diagnostic")
			switch scenario {
			case "close error":
				factory.closeErr = privateErr
			case "close panic":
				factory.closePanic = true
			case "partial prepare":
				factory.prepareErr = privateErr
			case "health failure":
				factory.healthErr = privateErr
			}
			resolver := &activeRuntimeConnectionResolver{module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod"}
			called := false
			var auth semanticmodel.ConnectionAuth
			consumerErr := errors.New("consumer failed")
			err := resolver.WithConnection(t.Context(), "quack", semanticmodel.Connection{Kind: "quack"}, func(connection semanticmodel.Connection) error {
				called = true
				require.False(t, factory.closed, "connection owner closed before native consumer")
				require.Equal(t, "private-token", connection.Auth["token"])
				auth = connection.Auth
				if scenario == "consumer panic" {
					panic("private-driver-diagnostic")
				}
				if scenario == "consumer error" {
					return consumerErr
				}
				return nil
			})
			require.Equal(t, 1, factory.closes)
			require.Empty(t, auth, "callback auth must be cleared before returning")
			switch scenario {
			case "success":
				require.NoError(t, err)
			case "consumer error":
				require.ErrorIs(t, err, consumerErr)
			case "close error", "close panic", "consumer panic":
				require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
			default:
				require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
			}
			require.Equal(t, scenario != "partial prepare" && scenario != "health failure", called)
			if err != nil {
				require.NotContains(t, err.Error(), "private-driver-diagnostic")
			}
		})
	}
}

func TestActiveRuntimeConnectionRejectsMissingConsumerBeforeResolution(t *testing.T) {
	versioned := &activeVersionedResolver{}
	module := activeTestModule(activeTestBinding(t), versioned, ActiveRuntimeBindingEvidence{})
	resolver := &activeRuntimeConnectionResolver{module: module}
	require.ErrorIs(t, resolver.WithConnection(t.Context(), "quack", semanticmodel.Connection{Kind: "quack"}, nil), connectionbinding.ErrProviderUnavailable)
	require.Zero(t, versioned.calls)
	require.Zero(t, module.connectionFactory.(*activePoolFactory).healthChecks)
}
