package connectionbinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestConnectionIDIsEnvironmentNeutralAndCanonical(t *testing.T) {
	id, err := ParseConnectionID("warehouse_primary")
	require.NoError(t, err)
	if id.String() != "warehouse_primary" {
		t.Fatalf("logical connection id = %q", id)
	}
	for _, invalid := range []string{"", " prod", "prod/db"} {
		if _, err := ParseConnectionID(invalid); !errors.Is(err, ErrInvalidBinding) {
			t.Fatalf("ParseConnectionID(%q) error = %v", invalid, err)
		}
	}
}

func TestTargetBindingSeparatesTargetReferenceFromLogicalRequirement(t *testing.T) {
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	binding, err := NewTargetBinding(TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: AuthenticationExternalBundle,
		Scope: BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint: EndpointConfig{
			Host: "warehouse.internal", Port: 5432, Database: "analytics", SourceIdentity: "leapview_runtime", TLSMode: "verify-full",
		},
		CredentialReference: CredentialReference{
			ProjectID: "infisical-project", Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse",
		},
		Enabled: true, Now: now,
	})
	require.NoError(t, err)
	if binding.Revision != 1 || binding.Health != HealthPending || binding.ConnectionID.String() != "warehouse" {
		t.Fatalf("binding = %#v", binding)
	}
	evidence := binding.Evidence()
	if evidence.BindingID != binding.ID || evidence.BindingRevision != 1 {
		t.Fatalf("redacted evidence = %#v", evidence)
	}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	for _, forbidden := range []string{"infisical-project", "/leapview/sales", "secretPath", "secretKey", "credentialReference"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("redacted evidence = %s, contains %q", raw, forbidden)
		}
	}
}

func TestTargetBindingRejectsUnknownConnectorKindAtAdministrationBoundary(t *testing.T) {
	input := validTargetBindingInput()
	input.ConnectorKind = "operator_supplied_plugin"
	if _, err := NewTargetBinding(input); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("unknown connector error = %v", err)
	}
}

func TestSameLogicalRequirementBindsIndependentlyAcrossTargets(t *testing.T) {
	production := validTargetBinding(t)
	developmentInput := validTargetBindingInput()
	developmentInput.ID = "binding_dev_warehouse"
	developmentInput.TargetID = "lvinst_dev"
	developmentInput.Scope.Environment = "dev"
	developmentInput.CredentialReference.Environment = "dev"
	developmentInput.CredentialReference.SecretPath = "/leapview/dev"
	development, err := NewTargetBinding(developmentInput)
	require.NoError(t, err)
	requirement := Requirement{ConnectionID: production.ConnectionID, ConnectorKind: "postgres"}
	productionEvidence, err := production.CompatibleEvidence(requirement, true)
	require.NoError(t, err)
	developmentEvidence, err := development.CompatibleEvidence(requirement, true)
	require.NoError(t, err)
	if productionEvidence.TargetID == developmentEvidence.TargetID ||
		productionEvidence.ConnectionID != developmentEvidence.ConnectionID {
		t.Fatalf("production=%#v development=%#v", productionEvidence, developmentEvidence)
	}
}

func TestResolverSelectionIsExplicitAndEnvironmentCannotBackstopProduction(t *testing.T) {
	if _, err := NewResolverSelection(ResolverSelectionInput{
		TargetID: "lvinst_prod", ProjectID: "project_sales", Environment: "prod", TargetClass: TargetProduction, Kind: ResolverEnvironment,
	}); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("production environment resolver error = %v", err)
	}
	if _, err := NewResolverSelection(ResolverSelectionInput{
		TargetID: "lvinst_local", ProjectID: "project_sales", Environment: "dev", TargetClass: TargetDevelopment, Kind: ResolverEnvironment,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolverSelection(ResolverSelectionInput{
		TargetID: "lvinst_prod", ProjectID: "project_sales", Environment: "prod", TargetClass: TargetProduction, Kind: ResolverInfisical,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolverSelection(ResolverSelectionInput{
		TargetID: "lvinst_prod", ProjectID: "project_sales", Environment: "prod", TargetClass: TargetProduction,
	}); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("implicit resolver error = %v", err)
	}
}

func TestTargetBindingFailsClosedForMissingDisabledUnauthorizedAndDriftedBindings(t *testing.T) {
	binding := validTargetBinding(t)
	requirement := Requirement{
		ConnectionID:  binding.ConnectionID,
		ConnectorKind: binding.ConnectorKind,
	}
	if _, err := binding.CompatibleEvidence(requirement, true); err != nil {
		t.Fatal(err)
	}
	if _, err := binding.CompatibleEvidence(requirement, false); !errors.Is(err, ErrUnauthorizedBinding) {
		t.Fatalf("unauthorized error = %v", err)
	}
	disabled := binding
	disabled.Enabled = false
	if _, err := disabled.CompatibleEvidence(requirement, true); !errors.Is(err, ErrDisabledBinding) {
		t.Fatalf("disabled error = %v", err)
	}
	drifted := requirement
	drifted.ConnectorKind = "mysql"
	if _, err := binding.CompatibleEvidence(drifted, true); !errors.Is(err, ErrIncompatibleBinding) {
		t.Fatalf("drift error = %v", err)
	}
}

func TestCredentialSnapshotCannotBeSerializedOrFormattedWithValues(t *testing.T) {
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	snapshot, err := NewCredentialSnapshot(
		map[string]string{"username": "source-user", "password": "source-secret"},
		"version-7",
		now,
		now.Add(time.Hour),
	)
	require.NoError(t, err)
	if _, err := json.Marshal(snapshot); !errors.Is(err, ErrCredentialSerialization) {
		t.Fatalf("json marshal error = %v", err)
	}
	if _, err := yaml.Marshal(snapshot); !errors.Is(err, ErrCredentialSerialization) {
		t.Fatalf("yaml marshal error = %v", err)
	}
	formatted := fmt.Sprintf("%v %#v", snapshot, snapshot)
	for _, secret := range []string{"source-user", "source-secret"} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("formatted snapshot leaked %q: %s", secret, formatted)
		}
	}
	observedPassword := ""
	if err := snapshot.Use(func(values map[string]string) error {
		observedPassword = values["password"]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if observedPassword != "source-secret" || snapshot.ProviderVersion() != "version-7" {
		t.Fatalf("snapshot password observed=%t version=%q", observedPassword != "", snapshot.ProviderVersion())
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("resolved", "snapshot", snapshot)
	for _, secret := range []string{"source-user", "source-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("structured log leaked %q: %s", secret, logs.String())
		}
	}
}

func TestEndpointConfigRejectsSecretShapedOptions(t *testing.T) {
	for _, key := range []string{"password", "api_token", "credentialValue", "secret_key"} {
		input := validTargetBindingInput()
		input.Endpoint.Options = map[string]string{key: "must-not-persist"}
		if _, err := NewTargetBinding(input); !errors.Is(err, ErrInvalidBinding) {
			t.Fatalf("option %q error = %v", key, err)
		}
	}
}

func TestTargetBindingValidationEvidenceAdvancesOptimistically(t *testing.T) {
	binding := validTargetBinding(t)
	now := binding.UpdatedAt.Add(time.Minute)
	validated, err := binding.MarkValidated("provider-version-8", now)
	require.NoError(t, err)
	if validated.Health != HealthHealthy || validated.ValidatedVersion != "provider-version-8" ||
		validated.LastValidatedAt != now || validated.Revision != binding.Revision+1 {
		t.Fatalf("validated binding = %#v", validated)
	}
	degraded, err := validated.MarkDegraded("PROVIDER_UNAVAILABLE", now.Add(time.Minute))
	require.NoError(t, err)
	if degraded.Health != HealthDegraded || degraded.ValidatedVersion != validated.ValidatedVersion {
		t.Fatalf("degraded binding = %#v", degraded)
	}
	if _, err := validated.MarkDegraded("password=source-secret", now); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("unsafe diagnostic error = %v", err)
	}
}

func TestTargetBindingConfigurationUpdateInvalidatesPriorRuntimeEvidence(t *testing.T) {
	binding := validTargetBinding(t)
	validated, err := binding.MarkValidated("secret:v1", binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	updated, err := validated.UpdateConfiguration(TargetBindingConfiguration{
		ConnectorKind:      validated.ConnectorKind,
		AuthenticationMode: validated.AuthenticationMode,
		Endpoint: EndpointConfig{
			Host: "warehouse-next.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full",
		},
		CredentialReference: validated.CredentialReference,
	}, validated.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	if updated.Revision != validated.Revision+1 || updated.Health != HealthPending ||
		updated.ValidatedVersion != "" || !updated.LastValidatedAt.IsZero() ||
		updated.Endpoint.Host != "warehouse-next.internal" {
		t.Fatalf("updated binding = %#v", updated)
	}
}

func TestTargetBindingConfigurationUpdateIsIdempotentAndValidated(t *testing.T) {
	binding := validTargetBinding(t)
	config := binding.Configuration()
	unchanged, err := binding.UpdateConfiguration(config, binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	if unchanged.Revision != binding.Revision || !unchanged.UpdatedAt.Equal(binding.UpdatedAt) {
		t.Fatalf("idempotent update changed binding: %#v", unchanged)
	}

	config.ConnectorKind = ""
	if _, err := binding.UpdateConfiguration(config, binding.UpdatedAt.Add(time.Minute)); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("invalid configuration error = %v", err)
	}
}

func TestTargetBindingEnableReturnsToPendingWithoutRestoringOldEvidence(t *testing.T) {
	binding := validTargetBinding(t)
	disabled, err := binding.Disable(binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	enabled, err := disabled.Enable(disabled.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	if !enabled.Enabled || enabled.Health != HealthPending || enabled.ValidatedVersion != "" ||
		!enabled.LastValidatedAt.IsZero() || enabled.Revision != disabled.Revision+1 {
		t.Fatalf("enabled binding = %#v", enabled)
	}
}

func validTargetBinding(t *testing.T) TargetBinding {
	t.Helper()
	binding, err := NewTargetBinding(validTargetBindingInput())
	require.NoError(t, err)
	return binding
}

func validTargetBindingInput() TargetBindingInput {
	return TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: AuthenticationExternalBundle,
		Scope:    BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint: EndpointConfig{Host: "warehouse.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full"},
		CredentialReference: CredentialReference{
			ProjectID: "infisical-project", Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse",
		},
		Enabled: true, Now: time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC),
	}
}

func servingIdentity(projectID, environment, generationID string) projectgraph.ServingIdentity {
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), environment, generationID)
	if err != nil {
		panic(err)
	}
	return identity
}
