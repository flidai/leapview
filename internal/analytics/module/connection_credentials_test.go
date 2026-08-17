package module

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestBuildDevelopmentTargetResolverAllowsOnlyDedicatedConnectionVariables(t *testing.T) {
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	values := map[string]string{
		"LEAPVIEW_DEV_CONNECTION_WAREHOUSE": `{"password":"source-secret"}`,
		"DATABASE_URL":                      "must-not-be-readable",
	}
	resolver, err := buildDevelopmentTargetResolver(
		projectgraph.ResourceID("sales"),
		"lvinst_local",
		"dev",
		[]string{
			"LEAPVIEW_DEV_CONNECTION_WAREHOUSE=redacted",
			"DATABASE_URL=redacted",
		},
		func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		},
		func() time.Time { return now },
	)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(context.Background(), connectionbinding.CredentialReference{
		ProjectID: projectgraph.ResourceID("sales"), Environment: "dev",
		SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	})
	require.NoError(t, err)
	if snapshot.ProviderVersion() == "" {
		t.Fatal("development snapshot has no version")
	}
	snapshot.Destroy()
	_, err = resolver.Resolve(context.Background(), connectionbinding.CredentialReference{
		ProjectID: projectgraph.ResourceID("sales"), Environment: "dev",
		SecretPath: "/", SecretKey: "DATABASE_URL",
	})
	if !errors.Is(err, connectionbinding.ErrCredentialDenied) {
		t.Fatalf("unscoped environment variable error = %v", err)
	}
}

func TestBuildCredentialResolverAllowsUnboundDevelopmentStartup(t *testing.T) {
	resolver, err := buildCredentialResolver(Config{
		CredentialMode:        CredentialModeDevelopmentEnvironment,
		CredentialTargetID:    "target-local",
		CredentialProjectID:   "",
		CredentialEnvironment: "dev",
	})
	require.NoError(t, err)
	t.Setenv("LEAPVIEW_TEST_UNBOUND_CREDENTIAL", `{"password":"source-secret"}`)
	auth, err := resolver.Resolve(context.Background(), "warehouse", semanticmodel.Connection{
		Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{
			Provider: "env", Secret: "LEAPVIEW_TEST_UNBOUND_CREDENTIAL",
		},
	})
	require.NoError(t, err)
	if auth["password"] != "source-secret" {
		t.Fatalf("resolved auth = %#v", auth)
	}
}

func TestTargetCredentialResolverBindsUnboundDevelopmentScopeAtSelectionTime(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", `{"password":"source-secret"}`)
	module := &Module{targetClass: connectionbinding.TargetDevelopment, targetID: "target-local", targetEnvironment: "dev", targetResolvers: connectionbinding.ResolverSet{
		Environment: unboundProcessDevelopmentTargetResolver{targetID: "target-local", environment: "dev"},
	}}
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-local", ProjectID: "project:active", Environment: "dev",
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := module.TargetCredentialResolver(selection, module.targetResolvers.Environment)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(context.Background(), connectionbinding.CredentialReference{
		ProjectID: "project:active", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	})
	require.NoError(t, err)
	snapshot.Destroy()
}

func TestUnboundProcessDevelopmentResolverUsesReferenceProject(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", `{"password":"source-secret"}`)
	resolver := unboundProcessDevelopmentTargetResolver{targetID: "target-local", environment: "dev"}
	snapshot, err := resolver.Resolve(context.Background(), connectionbinding.CredentialReference{
		ProjectID: "project:active", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	})
	require.NoError(t, err)
	snapshot.Destroy()
}

func TestBuildTargetResolversComposesOnlyTheConfiguredInfisicalAuthority(t *testing.T) {
	resolvers, err := buildTargetResolvers(TargetCredentialConfig{
		InfisicalBaseURL:               "https://infisical.example.com",
		InfisicalUniversalClientID:     "machine-client",
		InfisicalUniversalClientSecret: "bootstrap-secret",
		InfisicalAllowedScopes:         `[{"projectId":"project-1","environment":"prod","secretPathPrefix":"/leapview"}]`,
	})
	require.NoError(t, err)
	if resolvers.Infisical == nil || resolvers.Environment != nil {
		t.Fatalf("resolver set = %#v", resolvers)
	}
	module := &Module{targetResolvers: resolvers}
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-prod", ProjectID: "project-1", Environment: "prod",
		TargetClass: connectionbinding.TargetProduction, Kind: connectionbinding.ResolverInfisical,
	})
	require.NoError(t, err)
	if _, err := module.TargetCredentialResolver(selection, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBuildTargetResolversRejectsPartialOrMalformedConfigurationWithoutDisclosure(t *testing.T) {
	for _, config := range []TargetCredentialConfig{
		{InfisicalBaseURL: "https://infisical.example.com"},
		{
			InfisicalBaseURL:               "https://infisical.example.com",
			InfisicalUniversalClientID:     "machine-client",
			InfisicalUniversalClientSecret: "bootstrap-secret",
			InfisicalAllowedScopes:         "not-json",
		},
	} {
		_, err := buildTargetResolvers(config)
		if !errors.Is(err, connectionbinding.ErrInvalidBinding) ||
			strings.Contains(err.Error(), "bootstrap-secret") {
			t.Fatalf("buildTargetResolvers() error = %v", err)
		}
	}
}

func TestTargetCredentialConfigExcludesBootstrapSecretFromSerializationAndFormatting(t *testing.T) {
	config := TargetCredentialConfig{
		InfisicalBaseURL:               "https://infisical.example.com",
		InfisicalUniversalClientSecret: "bootstrap-secret",
	}
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	for _, rendered := range []string{string(encoded), config.String(), config.GoString()} {
		if strings.Contains(rendered, "bootstrap-secret") {
			t.Fatalf("target credential config disclosed secret: %s", rendered)
		}
	}
}
