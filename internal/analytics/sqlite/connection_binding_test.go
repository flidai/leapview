package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestConnectionBindingRepositoryPersistsOnlyNonSecretTargetStateAcrossRestart(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repository := NewConnectionBindingRepository(store.SQLDB())
	binding := testTargetBinding(t)
	invalid := binding
	invalid.Endpoint.Options = map[string]string{"password": "must-not-persist"}
	if err := repository.Create(ctx, invalid); !errors.Is(err, connectionbinding.ErrInvalidBinding) {
		t.Fatalf("invalid binding create error = %v", err)
	}
	if err := repository.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}

	restarted := NewConnectionBindingRepository(store.SQLDB())
	loaded, err := restarted.Binding(ctx, connectionbinding.BindingScope{
		ProjectID: "sales", Environment: "prod",
	}, "lvinst_prod", binding.ConnectionID)
	require.NoError(t, err)
	if loaded.ID != binding.ID || loaded.CredentialReference != binding.CredentialReference ||
		loaded.Endpoint.Host != "warehouse.internal" || loaded.Revision != 1 {
		t.Fatalf("loaded binding = %#v", loaded)
	}

	var schema string
	if err := store.SQLDB().QueryRowContext(ctx,
		`SELECT group_concat(name, ',') FROM pragma_table_info('target_connection_bindings')`,
	).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password", "token", "credential_value", "snapshot"} {
		if strings.Contains(strings.ToLower(schema), forbidden) {
			t.Fatalf("binding persistence contains secret column %q: %s", forbidden, schema)
		}
	}
}

func TestConnectionBindingRepositoryUsesOptimisticRevisionAndUniqueTargetScope(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repository := NewConnectionBindingRepository(store.SQLDB())
	binding := testTargetBinding(t)
	if err := repository.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	duplicate := binding
	duplicate.ID = "binding_duplicate"
	if err := repository.Create(ctx, duplicate); !errors.Is(err, connectionbinding.ErrIncompatibleBinding) {
		t.Fatalf("duplicate create error = %v", err)
	}

	validated, err := binding.MarkValidated("provider-version-8", binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	saved, err := repository.Save(ctx, validated, binding.Revision)
	if err != nil || saved.ValidatedVersion != "provider-version-8" {
		t.Fatalf("Save() = %#v, %v", saved, err)
	}
	if _, err := repository.Save(ctx, validated, binding.Revision); !errors.Is(err, connectionbinding.ErrIncompatibleBinding) {
		t.Fatalf("stale save error = %v", err)
	}

	immutableDrift := validated
	immutableDrift.TargetID = "lvinst_other"
	immutableDrift, err = immutableDrift.MarkDegraded("BINDING_DRIFT", validated.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	if _, err := repository.Save(ctx, immutableDrift, validated.Revision); !errors.Is(err, connectionbinding.ErrIncompatibleBinding) {
		t.Fatalf("immutable drift save error = %v", err)
	}
}

func TestConnectionBindingRepositoryListsOnlyRequestedTargetScope(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repository := NewConnectionBindingRepository(store.SQLDB())
	first := testTargetBinding(t)
	second := first
	second.ID = "binding_reporting"
	second.ConnectionID = "reporting"
	for _, binding := range []connectionbinding.TargetBinding{first, second} {
		if err := repository.Create(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}

	bindings, err := repository.List(ctx, first.Scope, first.TargetID)
	require.NoError(t, err)
	if len(bindings) != 2 ||
		bindings[0].ConnectionID != second.ConnectionID ||
		bindings[1].ConnectionID != first.ConnectionID {
		t.Fatalf("listed bindings = %#v", bindings)
	}
	other, err := repository.List(
		ctx,
		connectionbinding.BindingScope{ProjectID: "sales", Environment: "dev"},
		first.TargetID,
	)
	require.NoError(t, err)
	if len(other) != 0 {
		t.Fatalf("other-scope bindings = %#v", other)
	}
}

func testTargetBinding(t *testing.T) connectionbinding.TargetBinding {
	t.Helper()
	binding, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "lvinst_prod", ConnectionID: "warehouse",
		ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope: connectionbinding.BindingScope{ProjectID: "sales", Environment: "prod"},
		Endpoint: connectionbinding.EndpointConfig{
			Host: "warehouse.internal", Port: 5432, Database: "analytics", SourceIdentity: "leapview_runtime", TLSMode: "verify-full",
		},
		CredentialReference: connectionbinding.CredentialReference{
			ProjectID: "infisical-project", Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse",
		},
		Enabled: true, Now: time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	return binding
}
