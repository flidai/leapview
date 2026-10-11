package composectl

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBundledPostgresMaterialSurvivesInterruptedBootstrapRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), bundledPostgresSecretDir)
	require.NoError(t, os.MkdirAll(root, 0o700))
	partialPasswordPath := filepath.Join(root, ".control-runtime-password-interrupted.tmp")
	require.NoError(t, os.WriteFile(partialPasswordPath, []byte("partial"), 0o600))
	require.NoError(t, ensureBundledPostgresSecrets(root))
	require.NoError(t, os.Remove(partialPasswordPath), "abandoned atomic-write temporary files must not block retries")
	first := readBundledPostgresMaterial(t, root)

	// Simulate a process stopping after the key was persisted but before its
	// certificate was written. The retry signs from the original key and keeps
	// every database password and trust root unchanged.
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(root), bundledPostgresReadyFile)))
	require.NoError(t, os.Remove(filepath.Join(root, "server.crt")))
	require.NoError(t, ensureBundledPostgresSecrets(root))
	second := readBundledPostgresMaterial(t, root)
	for name, contents := range first {
		if name == "server.crt" {
			continue
		}
		require.Equal(t, contents, second[name], "%s must not be replaced on retry", name)
	}
	serverCertificate := parseBundledCertificate(t, second["server.crt"])
	rootCertificate := parseBundledCertificate(t, second["ca.crt"])
	roots := x509.NewCertPool()
	roots.AddCert(rootCertificate)
	_, err := serverCertificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: "postgres"})
	require.NoError(t, err, "the certificate must authenticate the Compose service hostname")

	for _, name := range bundledPostgresRoleSecrets {
		info, statErr := os.Stat(filepath.Join(root, name))
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s is an operation or serving secret", name)
	}
	for _, name := range []string{"ca.key", "server.key"} {
		info, statErr := os.Stat(filepath.Join(root, name))
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s must stay root private", name)
	}
}

func TestBundledPostgresCompletedCredentialSetRejectsMissingSecret(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, bundledPostgresSecretDir)
	require.NoError(t, ensureBundledPostgresSecrets(root))
	missing := filepath.Join(root, "control-runtime-password")
	require.NoError(t, os.Remove(missing))
	require.ErrorContains(t, ensureBundledPostgresSecrets(root), "completed PostgreSQL credential set")
	_, err := os.Lstat(missing)
	require.True(t, os.IsNotExist(err), "a completed credential set must not replace a missing password")
	require.NoError(t, os.MkdirAll(root, 0o700))
	require.NoError(t, os.RemoveAll(root))
	require.ErrorContains(t, ensureBundledPostgresSecrets(root), "completed PostgreSQL credential set")
	_, err = os.Lstat(filepath.Join(root, "bootstrap-password"))
	require.True(t, os.IsNotExist(err), "a missing credential directory must not be regenerated after completion")
}

func TestBundledPostgresSecretsRejectSymlinkedCredentialDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(target, 0o755))
	secretRoot := filepath.Join(root, bundledPostgresSecretDir)
	require.NoError(t, os.Symlink(target, secretRoot))
	require.ErrorContains(t, ensureBundledPostgresSecrets(secretRoot), "real directory")
	info, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "a linked target must not be chmodded as private credentials")
}

func TestBundledPostgresReconcilesInitializedVolumeOnEveryRetry(t *testing.T) {
	root := t.TempDir()
	var calls [][]string
	controller, err := New(Options{Root: root})
	require.NoError(t, err)
	controller.composeOverride = func(_ context.Context, _ io.Reader, _, _ io.Writer, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	first, err := controller.ensureBundledPostgres(t.Context())
	require.NoError(t, err)
	second, err := controller.ensureBundledPostgres(t.Context())
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, [][]string{
		{"up", "-d", "--wait", "--wait-timeout", "120", "postgres"},
		{"exec", "-T", "postgres", "/bin/sh", "-ec", "LEAPVIEW_POSTGRES_BOOTSTRAP_HOST=postgres exec /bin/sh /run/leapview-postgres/bundled-init.sh"},
		{"up", "-d", "--wait", "--wait-timeout", "120", "postgres"},
		{"exec", "-T", "postgres", "/bin/sh", "-ec", "LEAPVIEW_POSTGRES_BOOTSTRAP_HOST=postgres exec /bin/sh /run/leapview-postgres/bundled-init.sh"},
	}, calls)
	for _, value := range []string{first.ControlMigratorURL, first.DuckLakeMigratorURL} {
		require.Contains(t, value, "sslmode=verify-full")
		require.Contains(t, value, "sslrootcert=%2Frun%2Fleapview%2Fpostgres%2Fca.crt")
		require.NotContains(t, strings.Join(calls[0], " "), value)
	}
	for _, name := range []string{"control-runtime-password", "control-migrator-password", "ducklake-runtime-password", "ducklake-migrator-password"} {
		for _, call := range calls {
			require.NotContains(t, strings.Join(call, " "), name)
		}
	}
}

func TestBundledPostgresProfileSelectionIsPrivateAndImmutable(t *testing.T) {
	root := t.TempDir()
	partialPath := filepath.Join(root, ".host-postgres-profile-interrupted.tmp")
	require.NoError(t, os.WriteFile(partialPath, []byte("partial"), 0o600))
	selected, err := bundledPostgresSelected(root)
	require.NoError(t, err)
	require.False(t, selected)
	require.NoError(t, selectBundledPostgres(root))
	require.NoError(t, os.Remove(partialPath), "abandoned profile-write temporary files must not block retries")
	selected, err = bundledPostgresSelected(root)
	require.NoError(t, err)
	require.True(t, selected)
	require.NoError(t, selectBundledPostgres(root), "interrupted installation retries keep the selected profile")
	require.ErrorContains(t, rejectBundledPostgresSelection(root), "profile is bundled")
	require.NoError(t, os.MkdirAll(filepath.Join(root, bundledPostgresSecretDir), 0o700))
	require.NoError(t, os.Remove(filepath.Join(root, bundledPostgresProfileFile)))
	_, err = bundledPostgresSelected(root)
	require.ErrorContains(t, err, "marker is missing but bundled credentials remain")

	externalRoot := t.TempDir()
	require.NoError(t, selectPostgresProfile(externalRoot, FirstInstallPostgresExternal))
	require.ErrorContains(t, selectPostgresProfile(externalRoot, FirstInstallPostgresBundled), "cannot be changed")
	info, err := os.Stat(filepath.Join(root, bundledPostgresProfileFile))
	if !os.IsNotExist(err) {
		t.Fatalf("deleted bundled profile marker stat = %v", err)
	}
	info, err = os.Stat(filepath.Join(externalRoot, bundledPostgresProfileFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestComposeArgumentsSelectBundledPostgresOverlayFromInstallationProfile(t *testing.T) {
	for _, test := range []struct {
		name       string
		profile    string
		wantBundle bool
	}{
		{name: "external", profile: FirstInstallPostgresExternal},
		{name: "bundled", profile: FirstInstallPostgresBundled, wantBundle: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(
				"COMPOSE_PROJECT_NAME=profile-selection\nCOMPOSE_HTTPS=1\n",
			), 0o600))
			require.NoError(t, selectPostgresProfile(root, test.profile))

			arguments, err := composeArguments(root, "up", "-d")
			require.NoError(t, err)
			joined := strings.Join(arguments, " ")
			if test.wantBundle {
				require.Contains(t, joined, "--file "+filepath.Join(root, "compose.postgres.yaml"))
				require.Less(t,
					strings.Index(joined, "compose.postgres.yaml"),
					strings.Index(joined, "compose.https.yaml"),
					"the stable database topology must precede the HTTPS overlay",
				)
			} else {
				require.NotContains(t, joined, "compose.postgres.yaml")
			}
			require.Contains(t, joined, "--file "+filepath.Join(root, "compose.https.yaml"))
		})
	}
}

func TestBundledPostgresDockerResumesProvisioningAndPreservesVolume(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER") != "1" {
		t.Skip("set LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1 to run the isolated Docker integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	project := fmt.Sprintf("lvpgtest-%x", time.Now().UnixNano())
	root, secretRoot, controller := newBundledPostgresDockerProject(t, repository, project)
	require.NoError(t, os.WriteFile(filepath.Join(root, appEnvName), []byte("LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n"), 0o600))

	// Simulate a crash after initdb has established PGDATA but before any
	// provisioning SQL ran. The normal overlay is restored on the retry below;
	// official initdb.d hooks will then be skipped, so only reconciliation can
	// finish the role and database setup.
	initialOverlay, err := os.ReadFile(filepath.Join(root, "compose.postgres.yaml"))
	require.NoError(t, err)
	withoutInitHook := regexp.MustCompile(`(?m)^      - type: bind\n        source: ./postgres/bundled-init\.sh\n        target: /docker-entrypoint-initdb\.d/10-leapview-roles\.sh\n        read_only: true\n`).ReplaceAll(initialOverlay, nil)
	require.NotEqual(t, initialOverlay, withoutInitHook, "initial stage must omit the initdb provisioning hook")
	initialPath := filepath.Join(root, "compose.postgres.initial.yaml")
	require.NoError(t, os.WriteFile(initialPath, withoutInitHook, 0o600))
	require.NoError(t, runBundledPostgresCompose(ctx, root, initialPath, "up", "-d", "--wait", "--wait-timeout", "120", "postgres"))
	roleCount, err := bundledPostgresQuery(ctx, root, "postgres", "SELECT count(*) FROM pg_roles WHERE rolname = 'leapview_control_runtime'")
	require.NoError(t, err)
	require.Equal(t, "0", roleCount, "the interrupted stage must leave roles unprovisioned")

	first, err := controller.ensureBundledPostgres(ctx)
	require.NoError(t, err, "retry must reconcile an initialized volume despite initdb.d being skipped")
	assertBundledPostgresControlBaselineRoles(t, ctx, root)
	persistedSecrets := make(map[string][]byte, len(bundledPostgresRoleSecrets))
	for _, name := range bundledPostgresRoleSecrets {
		before, readErr := os.ReadFile(filepath.Join(secretRoot, name))
		require.NoError(t, readErr)
		persistedSecrets[name] = before
	}
	require.NoError(t, controller.compose(ctx, nil, io.Discard, io.Discard, "exec", "-T", "postgres", "psql", "--no-psqlrc", "--username", "leapview_bootstrap", "--dbname", "leapview_control", "--command", "CREATE TABLE interruption_persistence (id integer PRIMARY KEY)"))
	require.NoError(t, controller.compose(ctx, nil, io.Discard, io.Discard, "stop", "postgres"))
	second, err := controller.ensureBundledPostgres(ctx)
	require.NoError(t, err, "restart must keep reconciling the persistent PostgreSQL volume")
	assertBundledPostgresControlBaselineRoles(t, ctx, root)
	require.Equal(t, first, second, "serving and migration URLs must retain the generated credentials")
	for _, name := range bundledPostgresRoleSecrets {
		after, readErr := os.ReadFile(filepath.Join(secretRoot, name))
		require.NoError(t, readErr)
		require.Equal(t, persistedSecrets[name], after, "%s must not be regenerated on restart", name)
	}
	marker, err := bundledPostgresQuery(ctx, root, "leapview_control", "SELECT to_regclass('public.interruption_persistence') IS NOT NULL")
	require.NoError(t, err)
	require.Equal(t, "t", marker, "database state must survive the service restart")
	require.NoError(t, controller.compose(ctx, nil, io.Discard, io.Discard, "down", "--volumes", "--remove-orphans"))

	// A second isolated project starts with an empty volume and the unmodified
	// overlay. This exercises the official initdb.d hook on fresh-cluster setup.
	freshProject := fmt.Sprintf("lvpgfresh-%x", time.Now().UnixNano())
	freshRoot, _, freshController := newBundledPostgresDockerProject(t, repository, freshProject)
	require.NoError(t, os.WriteFile(filepath.Join(freshRoot, appEnvName), []byte("LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n"), 0o600))
	require.NoError(t, freshController.compose(ctx, nil, io.Discard, io.Discard, "up", "-d", "--wait", "--wait-timeout", "120", "postgres"))
	freshHookRoles, err := bundledPostgresQuery(ctx, freshRoot, "postgres", "SELECT count(*) FROM pg_roles WHERE rolname = 'leapview_control_runtime'")
	require.NoError(t, err)
	require.Equal(t, "1", freshHookRoles, "the official initdb.d script must provision roles on a fresh volume")
	assertBundledPostgresControlBaselineRoles(t, ctx, freshRoot)
	if _, err := freshController.ensureBundledPostgres(ctx); err != nil {
		t.Fatalf("fresh-volume retryable reconciliation failed after initdb hook: %v", err)
	}
}

func assertBundledPostgresControlBaselineRoles(t *testing.T, ctx context.Context, root string) {
	t.Helper()
	count, err := bundledPostgresQuery(ctx, root, "postgres", `
		SELECT count(*) FROM pg_roles WHERE rolname IN ('leapview_control_readonly', 'leapview_control_backup')
		AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit`)
	require.NoError(t, err)
	require.Equal(t, "2", count, "fresh and reconciled clusters must include restricted baseline authorities")
}

func TestBundledPostgresPrepareSeedsAppEnvBeforeCompose(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER") != "1" {
		t.Skip("set LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1 to run the isolated Compose config check")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	project := fmt.Sprintf("lvpgprepare-%x", time.Now().UnixNano())
	root, _, controller := newBundledPostgresDockerProject(t, repository, project)
	require.NoError(t, os.Remove(filepath.Join(root, deploymentEnvName)))
	image := "docker.io/library/busybox:latest"
	require.NoError(t, os.WriteFile(filepath.Join(root, "deployment.env.example"), []byte(
		"COMPOSE_PROJECT_NAME="+project+"\nCOMPOSE_HTTPS=0\nLEAPVIEW_IMAGE="+image+"\n",
	), 0o600))
	appEnvironmentExample, err := os.ReadFile(filepath.Join(repository, "deploy", "compose", "leapview.env.example"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env.example"), appEnvironmentExample, 0o600))
	options := firstInstallOptionsFixture(t)
	options.Profile = FirstInstallPostgresBundled
	options.Postgres = FirstInstallPostgres{}
	controller.qualificationExecutor = &recordingQualificationExecutor{
		output: qualificationNativePoolBootstrapOutput(
			options.PhysicalPool.Pool,
			options.PhysicalPool.Evidence.Evidence,
			false,
		),
	}
	var composeCalls int
	controller.composeOverride = func(_ context.Context, _ io.Reader, _, _ io.Writer, args ...string) error {
		composeCalls++
		require.FileExists(t, filepath.Join(root, deploymentEnvName))
		require.FileExists(t, filepath.Join(root, appEnvName), "Compose requires leapview.env even when starting only postgres")
		if composeCalls == 1 {
			require.Equal(t, "up", args[0])
		}
		return nil
	}
	require.NoError(t, controller.PrepareFirstInstall(ctx, options))
	require.Equal(t, 2, composeCalls, "prepare must start then reconcile PostgreSQL")
	require.NoError(t, runBundledPostgresCompose(
		ctx, root, filepath.Join(root, "compose.postgres.yaml"), "config", "--quiet",
	), "the actual Compose configuration must resolve from a fresh install after the app env is seeded")
}

func newBundledPostgresDockerProject(t *testing.T, repository, project string) (string, string, *Controller) {
	t.Helper()
	return newBundledPostgresDockerProjectWithDiagnostics(t, repository, project, os.Stderr)
}

func newBundledPostgresDockerProjectWithDiagnostics(t *testing.T, repository, project string, diagnostics io.Writer) (string, string, *Controller) {
	t.Helper()
	root := t.TempDir()
	compose := filepath.Join(repository, "deploy", "compose")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "postgres"), 0o700))
	for _, name := range []string{"compose.yaml", "compose.postgres.yaml"} {
		contents, err := os.ReadFile(filepath.Join(compose, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, name), contents, 0o600))
	}
	for _, name := range []string{"bundled-entrypoint.sh", "bundled-init.sh"} {
		contents, err := os.ReadFile(filepath.Join(compose, "postgres", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "postgres", name), contents, 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "deployment.env"), []byte(
		"COMPOSE_PROJECT_NAME="+project+"\nCOMPOSE_HTTPS=0\nLEAPVIEW_IMAGE=docker.io/library/busybox:latest\n",
	), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env.example"), []byte("LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n"), 0o600))
	secretRoot := filepath.Join(root, bundledPostgresSecretDir)
	require.NoError(t, selectBundledPostgres(root))
	require.NoError(t, ensureBundledPostgresSecrets(secretRoot))
	controller, err := New(Options{Root: root, Stdout: io.Discard, Stderr: io.Discard})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if t.Failed() {
			if err := controller.compose(cleanup, nil, diagnostics, diagnostics, "logs", "--no-color", "postgres"); err != nil {
				t.Errorf("read isolated PostgreSQL startup logs: %v", err)
			}
		}
		if err := controller.compose(cleanup, nil, io.Discard, io.Discard, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Errorf("remove isolated PostgreSQL Compose project: %v", err)
		}
	})
	return root, secretRoot, controller
}

func runBundledPostgresCompose(ctx context.Context, root, overlay string, args ...string) error {
	project, err := envFileValue(filepath.Join(root, "deployment.env"), "COMPOSE_PROJECT_NAME")
	if err != nil {
		return err
	}
	commandArgs := []string{
		"compose", "--project-name", project,
		"--project-directory", root,
		"--env-file", filepath.Join(root, "deployment.env"),
		"--file", filepath.Join(root, "compose.yaml"), "--file", overlay,
	}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	environment, err := composeProcessEnvironment(root, nil)
	if err != nil {
		return err
	}
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(commandArgs, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func bundledPostgresQuery(ctx context.Context, root, database, query string) (string, error) {
	var output strings.Builder
	controller, err := New(Options{Root: root, Stdout: &output, Stderr: &output})
	if err != nil {
		return "", err
	}
	err = controller.compose(ctx, nil, &output, &output,
		"exec", "-T", "postgres", "psql", "--no-psqlrc", "--tuples-only", "--no-align",
		"--username", "leapview_bootstrap", "--dbname", database, "--command", query,
	)
	return strings.TrimSpace(output.String()), err
}

func readBundledPostgresMaterial(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := append(append([]string(nil), bundledPostgresRoleSecrets...), "ca.key", "ca.crt", "server.key", "server.crt")
	contents := make(map[string][]byte, len(files))
	for _, name := range files {
		value, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		contents[name] = value
	}
	return contents
}

func parseBundledCertificate(t *testing.T, contents []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(contents)
	require.NotNil(t, block)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return certificate
}
