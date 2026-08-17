package duckdb

import (
	"context"
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
	if _, err := NewDevelopmentEnvironmentCredentialResolver(production); !errors.Is(err, connectionbinding.ErrInvalidBinding) {
		t.Fatalf("production selection error = %v", err)
	}

	development, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-dev", ProjectID: "sales", Environment: "dev", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := NewDevelopmentEnvironmentCredentialResolver(development)
	require.NoError(t, err)
	t.Setenv("LEAPVIEW_TEST_DEVELOPMENT_CREDENTIAL", `{"password":"source-secret"}`)
	auth, err := resolver.Resolve(context.Background(), "warehouse", semanticmodel.Connection{
		Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{
			Provider: "env", Secret: "LEAPVIEW_TEST_DEVELOPMENT_CREDENTIAL",
		},
	})
	require.NoError(t, err)
	if auth["password"] != "source-secret" {
		t.Fatalf("resolved auth = %#v", auth)
	}
}

func TestDefaultSourceRuntimeUsesFailClosedNonSecretResolver(t *testing.T) {
	runtime := NewSourceRuntime(nil)
	if _, ok := runtime.resolver.(NonSecretCredentialResolver); !ok {
		t.Fatalf("default resolver = %T", runtime.resolver)
	}
}
