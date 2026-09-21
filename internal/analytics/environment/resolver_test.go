package environment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestResolverRequiresExplicitDevelopmentSelectionAndAllowedVariable(t *testing.T) {
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "local-target", ProjectID: projectgraph.ResourceID("sales"), Environment: "dev", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := NewResolver(Config{
		Selection: selection, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
		VersionKey: []byte("0123456789abcdef0123456789abcdef"),
		LookupEnv: func(name string) (string, bool) {
			if name != "LEAPVIEW_DEV_CONNECTION_WAREHOUSE" {
				t.Fatalf("looked up unexpected variable %q", name)
			}
			return `{"password":"development-source-secret"}`, true
		},
		Now: func() time.Time { return time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC) },
		TTL: time.Minute,
	})
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(context.Background(), connectionbinding.CredentialReference{
		ProjectID: projectgraph.ResourceID("sales"), Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	})
	require.NoError(t, err)
	defer snapshot.Destroy()
	if !strings.HasPrefix(snapshot.ProviderVersion(), "env:v1:") {
		t.Fatalf("provider version = %q", snapshot.ProviderVersion())
	}
	rawDigest := sha256.Sum256([]byte(`{"password":"development-source-secret"}`))
	if strings.Contains(snapshot.ProviderVersion(), hex.EncodeToString(rawDigest[:])) {
		t.Fatalf("provider version exposed raw credential hash")
	}
}

func TestResolverDecodesComposeSafeSelectedCredentialTransport(t *testing.T) {
	raw := `{"username":"dev","password":"contains $VALUE # comment and ' quote"}`
	encoded := developmentCredentialEncodingPrefix + base64.RawStdEncoding.EncodeToString([]byte(raw))
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "local-target", ProjectID: "sales", Environment: "dev",
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolver, err := NewResolver(Config{
		Selection: selection, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
		VersionKey: []byte("0123456789abcdef0123456789abcdef"),
		LookupEnv:  func(string) (string, bool) { return encoded, true },
		Now:        time.Now, TTL: time.Minute,
	})
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(t.Context(), connectionbinding.CredentialReference{
		ProjectID: "sales", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	})
	require.NoError(t, err)
	defer snapshot.Destroy()

	plainResolver, err := NewResolver(Config{
		Selection: selection, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
		VersionKey: []byte("0123456789abcdef0123456789abcdef"),
		LookupEnv:  func(string) (string, bool) { return raw, true },
		Now:        time.Now, TTL: time.Minute,
	})
	require.NoError(t, err)
	plain, err := plainResolver.Resolve(t.Context(), connectionbinding.CredentialReference{
		ProjectID: "sales", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	})
	require.NoError(t, err)
	defer plain.Destroy()
	require.Equal(t, plain.ProviderVersion(), snapshot.ProviderVersion())
}

func TestResolverRejectsInvalidSelectedCredentialTransport(t *testing.T) {
	for _, raw := range []string{developmentCredentialEncodingPrefix, developmentCredentialEncodingPrefix + "%%%"} {
		if _, err := decodeDevelopmentCredentialTransport(raw, defaultMaxBundleSize); !errors.Is(err, connectionbinding.ErrInvalidCredentialBundle) {
			t.Fatalf("decode transport error = %v", err)
		}
	}
}

func TestResolverRejectsProductionSelectionAndOutOfScopeReferenceBeforeLookup(t *testing.T) {
	production, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "target-prod", ProjectID: projectgraph.ResourceID("sales"), Environment: "prod", TargetClass: connectionbinding.TargetProduction,
		Kind: connectionbinding.ResolverInfisical,
	})
	require.NoError(t, err)
	if _, err := NewResolver(Config{Selection: production, VersionKey: []byte("0123456789abcdef0123456789abcdef"), LookupEnv: func(string) (string, bool) {
		return "", false
	}, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"}, Now: time.Now, TTL: time.Minute}); !errors.Is(err, connectionbinding.ErrInvalidBinding) {
		t.Fatalf("NewResolver(production) error = %v", err)
	}

	development, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "local-target", ProjectID: projectgraph.ResourceID("sales"), Environment: "dev", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	lookups := 0
	resolver, err := NewResolver(Config{
		Selection: development, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
		VersionKey: []byte("0123456789abcdef0123456789abcdef"),
		LookupEnv: func(string) (string, bool) {
			lookups++
			return "", false
		},
		Now: time.Now, TTL: time.Minute,
	})
	require.NoError(t, err)
	if _, err := resolver.Resolve(context.Background(), connectionbinding.CredentialReference{
		ProjectID: projectgraph.ResourceID("other-project"), Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
	}); !errors.Is(err, connectionbinding.ErrCredentialDenied) {
		t.Fatalf("Resolve(out of scope) error = %v", err)
	}
	if lookups != 0 {
		t.Fatalf("environment lookups = %d", lookups)
	}
}

func TestResolverRequiresProtectedVersionKey(t *testing.T) {
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "local-target", ProjectID: projectgraph.ResourceID("sales"), Environment: "dev",
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	_, err = NewResolver(Config{
		Selection: selection, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
		LookupEnv: func(string) (string, bool) { return `{}`, true }, Now: time.Now, TTL: time.Minute,
	})
	if !errors.Is(err, connectionbinding.ErrInvalidBinding) {
		t.Fatalf("NewResolver(without version key) error = %v", err)
	}
}

func TestResolverVersionIsStableOnlyWithinOneProtectedAuthority(t *testing.T) {
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "local-target", ProjectID: projectgraph.ResourceID("sales"), Environment: "dev",
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	resolveWithKey := func(key string) string {
		t.Helper()
		resolver, buildErr := NewResolver(Config{
			Selection: selection, AllowedVariables: []string{"LEAPVIEW_DEV_CONNECTION_WAREHOUSE"}, VersionKey: []byte(key),
			LookupEnv: func(string) (string, bool) { return `{"password":"development-source-secret"}`, true },
			Now:       time.Now, TTL: time.Minute,
		})
		require.NoError(t, buildErr)
		snapshot, resolveErr := resolver.Resolve(t.Context(), connectionbinding.CredentialReference{
			ProjectID: "sales", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_WAREHOUSE",
		})
		require.NoError(t, resolveErr)
		defer snapshot.Destroy()
		return snapshot.ProviderVersion()
	}
	first := resolveWithKey("0123456789abcdef0123456789abcdef")
	require.Equal(t, first, resolveWithKey("0123456789abcdef0123456789abcdef"))
	require.NotEqual(t, first, resolveWithKey("fedcba9876543210fedcba9876543210"))
}

func TestValidateCredentialBundleRejectsDuplicatesAndNonStringValues(t *testing.T) {
	for _, raw := range []string{
		`{"password":"first","password":"second"}`,
		`{"password":1}`,
		`[]`,
		`{"password":""}`,
		`{"bad key":"value"}`,
		`{"password":"value"} trailing`,
	} {
		if err := ValidateCredentialBundle(raw); !errors.Is(err, connectionbinding.ErrInvalidCredentialBundle) {
			t.Fatalf("ValidateCredentialBundle(%q) error = %v", raw, err)
		}
	}
	require.NoError(t, ValidateCredentialBundle(`{"username":"dev","password":"local-only"}`))
}

func TestResolverRejectsInvalidAndDuplicateAllowlistEntries(t *testing.T) {
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "local-target", ProjectID: projectgraph.ResourceID("sales"), Environment: "dev",
		TargetClass: connectionbinding.TargetDevelopment, Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	for _, allowed := range [][]string{
		{"DATABASE_URL"},
		{"LEAPVIEW_DEV_CONNECTION_lower"},
		{"LEAPVIEW_DEV_CONNECTION_DUPLICATE", "LEAPVIEW_DEV_CONNECTION_DUPLICATE"},
	} {
		_, buildErr := NewResolver(Config{
			Selection: selection, AllowedVariables: allowed,
			VersionKey: []byte("0123456789abcdef0123456789abcdef"),
			LookupEnv:  func(string) (string, bool) { return `{"password":"must-not-be-read"}`, true },
			Now:        time.Now, TTL: time.Minute,
		})
		if !errors.Is(buildErr, connectionbinding.ErrInvalidBinding) || strings.Contains(buildErr.Error(), "must-not-be-read") {
			t.Fatalf("allowlist error = %v", buildErr)
		}
	}
}
