package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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

	resolved, err := resolver.Resolve(context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
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

	_, err := resolver.Resolve(context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
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

	resolved, err := resolver.Resolve(context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
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

	_, err := resolver.Resolve(context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrBindingNotFound)
}

func TestActiveRuntimeResolverLeavesCredentialFreeAuthoredConnectionUnbound(t *testing.T) {
	source := &activeFlakyEvidenceSource{}
	module := &Module{activeRuntimeBindingEvidence: source}
	resolver := &activeRuntimeConnectionResolver{
		module: module, servingStateID: "state_sales", projectID: "sales", environment: "prod",
	}
	logical := semanticmodel.Connection{Kind: "http", Scope: "https://example.test/public/"}

	resolved, err := resolver.Resolve(context.Background(), "public", logical)
	require.NoError(t, err)
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
	resolved, err := resolver.Resolve(context.Background(), "public_files", semanticmodel.Connection{Kind: "s3", Access: semanticmodel.ConnectionAccessPublic})
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

	_, err := resolver.Resolve(context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	resolved, err := resolver.Resolve(context.Background(), "quack", semanticmodel.Connection{Kind: "quack"})
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

type activePoolFactory struct{ healthChecks int }

func (factory *activePoolFactory) Prepare(_ context.Context, _ connectionbinding.TargetBinding, snapshot connectionbinding.CredentialSnapshot) (connectionbinding.RuntimePool, error) {
	values := map[string]string{}
	if snapshot.ProviderVersion() == connectionbinding.NoAuthProviderVersion {
		return &activeRuntimePool{factory: factory, values: values}, nil
	}
	if err := snapshot.Use(func(source map[string]string) error {
		for key, value := range source {
			values[key] = value
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &activeRuntimePool{factory: factory, values: values}, nil
}

type activeRuntimePool struct {
	factory *activePoolFactory
	values  map[string]string
}

func (pool *activeRuntimePool) HealthCheck(context.Context) error {
	pool.factory.healthChecks++
	return nil
}
func (pool *activeRuntimePool) Close() error { return nil }
func (pool *activeRuntimePool) Resolve(_ context.Context, _ string, logical semanticmodel.Connection) (semanticmodel.Connection, error) {
	if len(pool.values) == 0 {
		logical.Auth = nil
		return logical, nil
	}
	logical.Auth = map[string]any{"token": pool.values["token"]}
	return logical, nil
}
