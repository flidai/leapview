package connectionbinding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdministrationRequiresDependencyPlanConfirmationForConfigurationChanges(t *testing.T) {
	now := time.Date(2026, 7, 29, 19, 0, 0, 0, time.UTC)
	binding := validTargetBinding(t)
	repository := &administrationRepository{binding: binding}
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository,
		Authorize:  allowAdministration,
		Dependencies: staticDependencyInspector{dependencies: []BindingDependency{
			{Kind: "candidate", ID: "candidate-1", Label: "Author preview"},
			{Kind: "serving_state", ID: "state-1", Label: "Active sales"},
		}},
		Audit: noOpAdministrationAudit{},
		Now:   func() time.Time { return now },
	})
	require.NoError(t, err)
	key := BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, LogicalConnectionID: binding.LogicalConnectionID,
	}
	configuration := binding.Configuration()
	configuration.Endpoint.Host = "warehouse-next.internal"
	plan, err := service.PlanConfigurationChange(context.Background(), "operator-1", key, configuration)
	require.NoError(t, err)
	if !plan.RequiresConfirmation || plan.ConfirmationToken == "" || len(plan.Dependencies) != 2 ||
		plan.ExpectedRevision != binding.Revision {
		t.Fatalf("change plan = %#v", plan)
	}
	if _, err := service.UpdateConfiguration(context.Background(), UpdateConfigurationRequest{
		ActorID: "operator-1", Key: key, Configuration: configuration,
		ExpectedRevision: binding.Revision,
	}); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("unconfirmed update error = %v", err)
	}
	updated, err := service.UpdateConfiguration(context.Background(), UpdateConfigurationRequest{
		ActorID: "operator-1", Key: key, Configuration: configuration,
		ExpectedRevision: binding.Revision, ConfirmationToken: plan.ConfirmationToken,
	})
	require.NoError(t, err)
	if updated.Endpoint.Host != "warehouse-next.internal" || repository.saves != 1 {
		t.Fatalf("updated=%#v saves=%d", updated, repository.saves)
	}
}

func TestAdministrationSeparatesMetadataAndRefreshAuthorization(t *testing.T) {
	binding := validTargetBinding(t)
	repository := &administrationRepository{binding: binding}
	pool := &administrationPool{}
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository,
		Authorize: func(_ context.Context, actor string, permission AdministrationPermission, _ TargetBinding) error {
			if actor != "metadata-operator" || permission != PermissionManageConnectionMetadata {
				return ErrUnauthorizedBinding
			}
			return nil
		},
		Dependencies: staticDependencyInspector{},
		Pools:        staticPoolDirectory{pool: pool},
		Audit:        noOpAdministrationAudit{},
		Now:          time.Now,
	})
	require.NoError(t, err)
	key := BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, LogicalConnectionID: binding.LogicalConnectionID,
	}
	if _, err := service.RefreshNow(context.Background(), "metadata-operator", key); !errors.Is(err, ErrUnauthorizedBinding) {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	if pool.refreshes != 0 {
		t.Fatalf("unauthorized refreshes = %d", pool.refreshes)
	}
}

func TestAdministrationTestUsesCandidateRuntimePathAndDistinctAuditOperation(t *testing.T) {
	binding := validTargetBinding(t)
	repository := &administrationRepository{binding: binding}
	pool := &administrationPool{}
	service, err := NewAdministration(AdministrationConfig{
		Repository:   repository,
		Authorize:    allowAdministration,
		Dependencies: staticDependencyInspector{},
		Pools:        staticPoolDirectory{pool: pool},
		Audit:        noOpAdministrationAudit{},
		Now:          time.Now,
	})
	require.NoError(t, err)
	key := BindingKey{
		Scope: binding.Scope, TargetID: binding.TargetID, LogicalConnectionID: binding.LogicalConnectionID,
	}

	if _, err := service.Test(context.Background(), "operator-1", key); err != nil {
		t.Fatal(err)
	}
	if len(pool.requests) != 1 {
		t.Fatalf("test refresh requests = %#v", pool.requests)
	}
	request := pool.requests[0]
	if request.Actor != "principal:operator-1" || request.Operation != RefreshTest {
		t.Fatalf("test refresh request = %#v", request)
	}
}

func TestAdministrationListsOnlyTheRequestedTargetScope(t *testing.T) {
	binding := validTargetBinding(t)
	second := binding
	second.ID = "binding_reporting"
	second.LogicalConnectionID = "reporting"
	repository := &administrationRepository{binding: binding, bindings: []TargetBinding{second, binding}}
	var authorized TargetBinding
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository,
		Authorize: func(_ context.Context, actor string, permission AdministrationPermission, binding TargetBinding) error {
			if actor != "operator-1" || permission != PermissionManageConnectionMetadata {
				return ErrUnauthorizedBinding
			}
			authorized = binding
			return nil
		},
		Dependencies: staticDependencyInspector{},
		Audit:        noOpAdministrationAudit{},
		Now:          time.Now,
	})
	require.NoError(t, err)

	bindings, err := service.List(context.Background(), "operator-1", binding.Scope, binding.TargetID)
	require.NoError(t, err)
	if authorized.TargetID != binding.TargetID || authorized.Scope != binding.Scope {
		t.Fatalf("authorized scope = %#v", authorized)
	}
	if len(bindings) != 2 ||
		bindings[0].LogicalConnectionID != second.LogicalConnectionID ||
		bindings[1].LogicalConnectionID != binding.LogicalConnectionID {
		t.Fatalf("listed bindings = %#v", bindings)
	}
}

func TestAdministrationAuthorizesBeforeEnsuringWorkspaceScopeAndCreatingBinding(t *testing.T) {
	binding := validTargetBinding(t)
	repository := &administrationRepository{}
	order := []string{}
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository,
		EnsureScope: func(_ context.Context, scope BindingScope) error {
			require.Equal(t, binding.Scope, scope)
			order = append(order, "ensure")
			return nil
		},
		Authorize: func(context.Context, string, AdministrationPermission, TargetBinding) error {
			order = append(order, "authorize")
			return nil
		},
		Dependencies: staticDependencyInspector{}, Audit: noOpAdministrationAudit{},
		Now: func() time.Time { return binding.CreatedAt },
	})
	require.NoError(t, err)
	_, err = service.Create(context.Background(), "operator-1", TargetBindingInput{
		ID: binding.ID, TargetID: binding.TargetID,
		LogicalConnectionID: binding.LogicalConnectionID.String(), ConnectorKind: binding.ConnectorKind,
		AuthenticationMode: binding.AuthenticationMode, Scope: binding.Scope, Endpoint: binding.Endpoint,
		CredentialReference: binding.CredentialReference, Enabled: binding.Enabled,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"authorize", "ensure"}, order)
	require.Equal(t, binding.Scope, repository.binding.Scope)
}

func TestAdministrationDoesNotEnsureWorkspaceScopeWhenCreateIsUnauthorized(t *testing.T) {
	binding := validTargetBinding(t)
	repository := &administrationRepository{}
	ensureCalls := 0
	service, err := NewAdministration(AdministrationConfig{
		Repository: repository,
		EnsureScope: func(context.Context, BindingScope) error {
			ensureCalls++
			return nil
		},
		Authorize: func(context.Context, string, AdministrationPermission, TargetBinding) error {
			return ErrUnauthorizedBinding
		},
		Dependencies: staticDependencyInspector{}, Audit: noOpAdministrationAudit{},
		Now: func() time.Time { return binding.CreatedAt },
	})
	require.NoError(t, err)

	_, err = service.Create(context.Background(), "operator-1", TargetBindingInput{
		ID: binding.ID, TargetID: binding.TargetID,
		LogicalConnectionID: binding.LogicalConnectionID.String(), ConnectorKind: binding.ConnectorKind,
		AuthenticationMode: binding.AuthenticationMode, Scope: binding.Scope, Endpoint: binding.Endpoint,
		CredentialReference: binding.CredentialReference, Enabled: binding.Enabled,
	})
	require.ErrorIs(t, err, ErrUnauthorizedBinding)
	require.Zero(t, ensureCalls)
	require.Empty(t, repository.binding.ID)
}

type administrationRepository struct {
	binding  TargetBinding
	bindings []TargetBinding
	saves    int
}

func (repository *administrationRepository) Create(_ context.Context, binding TargetBinding) error {
	repository.binding = binding
	return nil
}

func (repository *administrationRepository) Binding(
	context.Context,
	BindingScope,
	string,
	LogicalConnectionID,
) (TargetBinding, error) {
	return repository.binding, nil
}

func (repository *administrationRepository) List(
	context.Context,
	BindingScope,
	string,
) ([]TargetBinding, error) {
	return append([]TargetBinding(nil), repository.bindings...), nil
}

func (repository *administrationRepository) Save(
	_ context.Context,
	binding TargetBinding,
	expectedRevision int64,
) (TargetBinding, error) {
	if repository.binding.Revision != expectedRevision {
		return TargetBinding{}, ErrIncompatibleBinding
	}
	repository.binding = binding
	repository.saves++
	return binding, nil
}

func allowAdministration(context.Context, string, AdministrationPermission, TargetBinding) error {
	return nil
}

type staticDependencyInspector struct {
	dependencies []BindingDependency
}

func (inspector staticDependencyInspector) Dependents(context.Context, TargetBinding) ([]BindingDependency, error) {
	return append([]BindingDependency(nil), inspector.dependencies...), nil
}

type administrationPool struct {
	refreshes int
	requests  []RefreshRequest
}

func (pool *administrationPool) Refresh(_ context.Context, request RefreshRequest) error {
	pool.refreshes++
	pool.requests = append(pool.requests, request)
	return nil
}

func (pool *administrationPool) Disable(context.Context, time.Time) error { return nil }
func (pool *administrationPool) HealthStatus() BindingHealthStatus        { return BindingHealthStatus{} }

type staticPoolDirectory struct {
	pool AdministrationPool
}

func (directory staticPoolDirectory) Pool(TargetBinding) (AdministrationPool, error) {
	if directory.pool == nil {
		return nil, ErrBindingNotFound
	}
	return directory.pool, nil
}
