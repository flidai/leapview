package module

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestConnectionBindingAPICreatesOnlyNonSecretMetadata(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	repository := &apiBindingRepository{}
	administration := newTestConnectionAdministration(t, repository, nil, now)
	handler := connectionBindingAPIHandler{config: ConnectionBindingAPIGenConfig{
		Administration: administration,
		Environment:    "prod",
		CurrentPrincipal: func(*http.Request) (string, bool) {
			return "operator-1", true
		},
	}}
	body := `{
		"id":"binding_prod_warehouse",
		"logicalConnection":"warehouse",
		"configuration":{
			"connectorKind":"postgres",
			"authenticationMode":"external_bundle",
			"endpoint":{"host":"warehouse.internal","port":5432,"database":"analytics","tlsMode":"verify-full"},
			"credentialReference":{"projectId":"project-1","environment":"prod","secretPath":"/leapview/sales","secretKey":"warehouse"}
		},
		"enabled":true
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/sales/targets/target-1/connection-bindings", strings.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.Create(recorder, request, "sales", "target-1")

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.binding.ID != "binding_prod_warehouse" ||
		repository.binding.CredentialReference.SecretPath != "/leapview/sales" {
		t.Fatalf("persisted binding = %#v", repository.binding)
	}
	for _, forbidden := range []string{"secretValue", "source-secret", "password", "token", "credentialReference", "project-1", "/leapview/sales", "secretKey", "secretPath"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response exposed forbidden material %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestConnectionBindingAPINeverReturnsRawProviderErrors(t *testing.T) {
	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	binding := testAPIBinding(t, now)
	repository := &apiBindingRepository{binding: binding}
	pool := &apiAdministrationPool{
		err: fmt.Errorf("source-secret from provider: %w", connectionbinding.ErrProviderUnavailable),
		status: connectionbinding.BindingHealthStatus{
			BindingID: binding.ID, TargetID: binding.TargetID,
			ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind,
			Scope: binding.Scope, BindingRevision: binding.Revision,
			Health: connectionbinding.HealthDegraded, DiagnosticCode: "PROVIDER_UNAVAILABLE",
		},
	}
	administration := newTestConnectionAdministration(t, repository, pool, now)
	handler := connectionBindingAPIHandler{config: ConnectionBindingAPIGenConfig{
		Administration: administration,
		Environment:    "prod",
		CurrentPrincipal: func(*http.Request) (string, bool) {
			return "operator-1", true
		},
	}}
	request := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(nil))
	recorder := httptest.NewRecorder()

	handler.Test(recorder, request, "sales", "target-1", "warehouse")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "source-secret") ||
		strings.Contains(recorder.Body.String(), "provider:") {
		t.Fatalf("response exposed raw provider error: %s", recorder.Body.String())
	}
}

func newTestConnectionAdministration(
	t *testing.T,
	repository *apiBindingRepository,
	pool connectionbinding.AdministrationPool,
	now time.Time,
) *connectionbinding.Administration {
	t.Helper()
	var pools connectionbinding.AdministrationPoolDirectory
	if pool != nil {
		pools = apiPoolDirectory{pool: pool}
	}
	administration, err := connectionbinding.NewAdministration(connectionbinding.AdministrationConfig{
		Repository: repository,
		Authorize: func(context.Context, string, connectionbinding.AdministrationPermission, connectionbinding.TargetBinding) error {
			return nil
		},
		Dependencies: apiDependencyInspector{},
		Pools:        pools,
		Audit:        moduleAdministrationAuditNoop{},
		Now:          func() time.Time { return now },
	})
	require.NoError(t, err)
	return administration
}

type apiBindingRepository struct {
	binding connectionbinding.TargetBinding
}

func (repository *apiBindingRepository) Create(_ context.Context, binding connectionbinding.TargetBinding) error {
	repository.binding = binding
	return nil
}

func (repository *apiBindingRepository) Binding(
	_ context.Context,
	_ connectionbinding.BindingScope,
	_ connectionbinding.TargetID,
	_ projectgraph.ResourceID,
) (connectionbinding.TargetBinding, error) {
	if repository.binding.ID == "" {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrBindingNotFound
	}
	return repository.binding, nil
}

func (repository *apiBindingRepository) List(
	context.Context,
	connectionbinding.BindingScope,
	connectionbinding.TargetID,
) ([]connectionbinding.TargetBinding, error) {
	if repository.binding.ID == "" {
		return nil, nil
	}
	return []connectionbinding.TargetBinding{repository.binding}, nil
}

func (repository *apiBindingRepository) Save(
	_ context.Context,
	binding connectionbinding.TargetBinding,
	expectedRevision int64,
) (connectionbinding.TargetBinding, error) {
	if repository.binding.Revision != expectedRevision {
		return connectionbinding.TargetBinding{}, connectionbinding.ErrIncompatibleBinding
	}
	repository.binding = binding
	return binding, nil
}

type apiDependencyInspector struct{}

func (apiDependencyInspector) Dependents(
	context.Context,
	connectionbinding.TargetBinding,
) ([]connectionbinding.BindingDependency, error) {
	return nil, nil
}

type apiAdministrationPool struct {
	err    error
	status connectionbinding.BindingHealthStatus
}

func (pool *apiAdministrationPool) Refresh(context.Context, connectionbinding.RefreshRequest) error {
	return pool.err
}

func (pool *apiAdministrationPool) Disable(context.Context, time.Time) error { return nil }
func (pool *apiAdministrationPool) HealthStatus() connectionbinding.BindingHealthStatus {
	return pool.status
}

type apiPoolDirectory struct {
	pool connectionbinding.AdministrationPool
}

func (directory apiPoolDirectory) Pool(connectionbinding.TargetBinding) (connectionbinding.AdministrationPool, error) {
	return directory.pool, nil
}

func testAPIBinding(t *testing.T, now time.Time) connectionbinding.TargetBinding {
	t.Helper()
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "target-1", ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope: connectionbinding.BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint: connectionbinding.EndpointConfig{
			Host: "warehouse.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full",
		},
		CredentialReference: connectionbinding.CredentialReference{
			ProjectID: "project-1", Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse",
		},
		Enabled: true, Now: now,
	})
	require.NoError(t, err)
	return binding
}
