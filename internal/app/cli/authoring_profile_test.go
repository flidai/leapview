package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestPrepareLocalDevelopmentProfileCapturesOnlySelectedCredential(t *testing.T) {
	checkout, sourceRoot := writeLocalProfileProject(t)
	t.Chdir(checkout)
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", `{"password":"selected","username":"developer"}`)
	t.Setenv("LEAPVIEW_DEV_CONNECTION_OTHER", `{"password":"unselected"}`)
	t.Setenv("DATABASE_URL", "postgres://production.example.invalid")

	selected, err := prepareLocalDevelopmentProfile(localProfileCommand(), nil)
	require.NoError(t, err)
	require.Equal(t, checkout, selected.CheckoutRoot)
	require.Equal(t, sourceRoot, selected.SourceRoot)
	require.NoError(t, platformdigest.ValidateSHA256Identity(selected.GraphDigest))
	require.NoError(t, platformdigest.ValidateSHA256Identity(selected.Profile.ProfileDigest))
	require.Equal(t, "local", selected.Profile.ProfileName)
	require.Equal(t, map[string]string{
		"LEAPVIEW_DEV_CONNECTION_WAREHOUSE": `{"password":"selected","username":"developer"}`,
	}, selected.Credentials)
}

func TestPrepareLocalDevelopmentProfileRejectsMissingAndInvalidSelectedCredential(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		set   bool
	}{
		{name: "missing"},
		{name: "duplicate key", value: `{"password":"one","password":"two"}`, set: true},
		{name: "non string", value: `{"password":12}`, set: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout, _ := writeLocalProfileProject(t)
			t.Chdir(checkout)
			if test.set {
				t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", test.value)
			} else {
				require.NoError(t, os.Unsetenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE"))
			}
			_, err := prepareLocalDevelopmentProfile(localProfileCommand(), nil)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "WAREHOUSE")
			require.NotContains(t, err.Error(), "password")
			require.NotContains(t, err.Error(), "one")
		})
	}
}

func TestDiscoverLocalCheckoutWithoutGitReturnsCanonicalStart(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))
	resolved, err := discoverLocalCheckout(nested)
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(nested)
	require.NoError(t, err)
	require.Equal(t, want, resolved)
}

func localProfileCommand() *cobra.Command {
	command := &cobra.Command{Use: "dev [source-root]"}
	command.Flags().String("source-root", "dashboards", "source")
	command.Flags().String("profile-file", "", "profile file")
	command.Flags().String("profile", "", "profile")
	return command
}

func writeLocalProfileProject(t *testing.T) (string, string) {
	t.Helper()
	checkout := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(checkout, ".git"), 0o755))
	sourceRoot := filepath.Join(checkout, "dashboards")
	require.NoError(t, os.MkdirAll(filepath.Join(sourceRoot, "connections"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceRoot, "connections", "warehouse.yaml"), []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: postgres}
`), 0o644))
	profileDirectory := filepath.Join(checkout, ".leapview")
	require.NoError(t, os.Mkdir(profileDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(profileDirectory, "profiles.local.yaml"), []byte(`version: 1
profiles:
  local:
    connections:
      warehouse:
        endpoint:
          host: 127.0.0.1
          port: 5432
          database: analytics
          tlsMode: disable
        credentials:
          env: LEAPVIEW_DEV_CONNECTION_WAREHOUSE
`), 0o600))
	canonical, err := filepath.EvalSymlinks(checkout)
	require.NoError(t, err)
	return canonical, filepath.Join(canonical, "dashboards")
}

func TestLocalProfileErrorDoesNotEchoCredentialContent(t *testing.T) {
	checkout, _ := writeLocalProfileProject(t)
	t.Chdir(checkout)
	secret := strings.Repeat("secret-value", 4)
	t.Setenv("LEAPVIEW_DEV_CONNECTION_WAREHOUSE", secret)
	_, err := prepareLocalDevelopmentProfile(localProfileCommand(), nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}
