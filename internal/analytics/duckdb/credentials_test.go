package duckdb

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/stretchr/testify/require"
)

func TestNonSecretCredentialResolverRejectsEnvironmentCredentials(t *testing.T) {
	t.Setenv("LEAPVIEW_TEST_PRODUCTION_CREDENTIAL", `{"password":"source-secret"}`)
	_, err := (NonSecretCredentialResolver{}).Resolve(context.Background(), "warehouse", semanticmodel.Connection{
		Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{
			Provider: "env", Secret: "LEAPVIEW_TEST_PRODUCTION_CREDENTIAL",
		},
	})
	if !errors.Is(err, ErrDevelopmentCredentialResolverRequired) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestDevelopmentEnvironmentCredentialResolverRequiresExplicitDevelopmentSelection(t *testing.T) {
	production, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-prod", ProjectID: "sales", Environment: "prod", TargetClass: connectionbinding.TargetProduction,
		Kind: connectionbinding.ResolverInfisical,
	})
	require.NoError(t, err)
	if _, err := NewDevelopmentEnvironmentCredentialResolver(production, nil); !errors.Is(err, connectionbinding.ErrInvalidBinding) {
		t.Fatalf("production selection error = %v", err)
	}

	development, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-dev", ProjectID: "sales", Environment: "dev", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := NewDevelopmentEnvironmentCredentialResolver(development, []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"})
	require.NoError(t, err)
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", `{"password":"source-secret"}`)
	auth, err := resolver.Resolve(context.Background(), "warehouse", semanticmodel.Connection{
		Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{
			Provider: "env", Secret: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
		},
	})
	require.NoError(t, err)
	if auth["password"] != "source-secret" {
		t.Fatalf("resolved auth = %#v", auth)
	}
}

func TestDevelopmentEnvironmentCredentialResolverAcceptsComposeSafeSelectedBundle(t *testing.T) {
	development, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-dev", ProjectID: "sales", Environment: "dev", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := NewDevelopmentEnvironmentCredentialResolver(development, []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"})
	require.NoError(t, err)
	raw := `{"password":"source $VALUE # comment"}`
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "leapview-base64-v1:"+base64.RawStdEncoding.EncodeToString([]byte(raw)))
	auth, err := resolver.Resolve(t.Context(), "warehouse", semanticmodel.Connection{
		Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{Provider: "env", Secret: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
	})
	require.NoError(t, err)
	require.Equal(t, "source $VALUE # comment", auth["password"])
}

func TestDevelopmentEnvironmentCredentialResolverRejectsUnselectedVariableBeforeLookup(t *testing.T) {
	development, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-dev", ProjectID: "sales", Environment: "dev", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := NewDevelopmentEnvironmentCredentialResolver(development, []string{"LEAPVIEW_DEV_CONNECTION_SELECTED"})
	require.NoError(t, err)
	t.Setenv("LEAPVIEW_DEV_CONNECTION_UNSELECTED", `{"password":"must-not-be-read"}`)
	_, err = resolver.Resolve(t.Context(), "warehouse", semanticmodel.Connection{
		Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{Provider: "env", Secret: "LEAPVIEW_DEV_CONNECTION_UNSELECTED"},
	})
	require.ErrorContains(t, err, "not selected")
	require.NotContains(t, err.Error(), "must-not-be-read")
}

func TestDefaultSourceRuntimeUsesFailClosedNonSecretResolver(t *testing.T) {
	runtime := NewSourceRuntime(nil)
	if _, ok := runtime.resolver.(NonSecretCredentialResolver); !ok {
		t.Fatalf("default resolver = %T", runtime.resolver)
	}
}

func TestNonSecretCredentialResolverLeavesPublicConnectionCredentialFree(t *testing.T) {
	auth, err := (NonSecretCredentialResolver{}).Resolve(context.Background(), "public_files", semanticmodel.Connection{
		Kind: "s3", Access: semanticmodel.ConnectionAccessPublic,
	})
	require.NoError(t, err)
	if len(auth) != 0 {
		t.Fatalf("public resolver returned auth keys: %#v", auth)
	}
}
