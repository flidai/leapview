package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/stretchr/testify/require"
)

func TestEvaluationCommandExposesServerAndOneTimeFirstLogin(t *testing.T) {
	command := evaluationCommand(context.Background(), &rootOptions{})
	if command.Name() != "evaluate" || !command.Runnable() {
		t.Fatalf("evaluation command = %#v, want runnable evaluate command", command)
	}
	if command.Flags().Lookup("port") == nil {
		t.Fatal("evaluation command is missing its isolated loopback --port")
	}
	for _, forbidden := range []string{"project", "target", "token"} {
		if command.Flags().Lookup(forbidden) != nil {
			t.Fatalf("evaluation command exposes authoring flag --%s", forbidden)
		}
	}
	firstLogin, _, err := command.Find([]string{"first-login"})
	if err != nil || firstLogin == nil || firstLogin.Name() != "first-login" {
		t.Fatalf("first-login command = %#v, err=%v", firstLogin, err)
	}
}

func TestEvaluationTargetDerivesOneOrdinaryLoopbackIdentity(t *testing.T) {
	target, err := newEvaluationTarget(8181)
	require.NoError(t, err)
	if target.ListenAddress != ":8181" ||
		target.PublicURL != "http://localhost:8181" ||
		target.ServerOrigin != "http://127.0.0.1:8181" {
		t.Fatalf("target = %#v", target)
	}
	if _, err := newEvaluationTarget(0); err == nil {
		t.Fatal("port zero created an ambiguous evaluation target")
	}
}

func TestConfigureEvaluationEnvironmentPersistsPrivateRuntimeSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_PUBLIC_URL", "https://unsafe.example.com")
	t.Setenv("LEAPVIEW_TRUST_PROXY_HEADERS", "true")

	target, err := newEvaluationTarget(8181)
	require.NoError(t, err)
	if err := configureEvaluationEnvironment(home, target); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	if !cfg.EvaluationMode || !cfg.Production || cfg.Environment != evaluationEnvironment ||
		!cfg.LocalAuth || cfg.PublicURL != target.PublicURL ||
		cfg.ListenAddr() != target.ListenAddress || cfg.TrustProxyHeaders {
		t.Fatalf("evaluation configuration = %#v", cfg)
	}
	if err := cfg.Validate(config.ProfileServe); err != nil {
		t.Fatalf("evaluation configuration validation: %v", err)
	}
	path := evaluationRuntimeConfigPath(home)
	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime config mode = %o, want 600", info.Mode().Perm())
	}
	firstCSRF := cfg.CSRFKey
	if err := configureEvaluationEnvironment(home, target); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	require.NoError(t, err)
	if cfg.CSRFKey != firstCSRF {
		t.Fatal("evaluation runtime secret changed across restart")
	}
}

func TestEvaluationCredentialHandoffIsPrivateRecoverableAndOneTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	target, err := newEvaluationTarget(8080)
	require.NoError(t, err)
	if err := configureEvaluationEnvironment(home, target); err != nil {
		t.Fatal(err)
	}
	token, err := prepareEvaluationCredentials(context.Background(), home)
	require.NoError(t, err)
	if strings.TrimSpace(token) == "" {
		t.Fatal("evaluation bootstrap token is empty")
	}
	if _, err := os.Stat(filepath.Join(home, adminoffline.CredentialRecoveryFileName)); !os.IsNotExist(err) {
		t.Fatalf("platform recovery bundle still exists: %v", err)
	}
	info, err := os.Stat(evaluationFirstLoginPath(home))
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("first-login mode = %o, want 600", info.Mode().Perm())
	}

	var out bytes.Buffer
	if err := consumeEvaluationFirstLogin(home, &out); err != nil {
		t.Fatal(err)
	}
	var credentials adminoffline.InitialCredentials
	if err := json.Unmarshal(out.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials.Email != evaluationAdminEmail || credentials.TemporaryPassword == "" {
		t.Fatalf("first-login credentials = %#v", credentials)
	}
	if _, err := os.Stat(evaluationFirstLoginPath(home)); !os.IsNotExist(err) {
		t.Fatalf("first-login file remains after successful delivery: %v", err)
	}
	if got, err := readEvaluationBootstrapToken(home); err != nil || got != token {
		t.Fatalf("bootstrap token after first-login = %q, %v", got, err)
	}
	if err := consumeEvaluationFirstLogin(home, &out); err == nil {
		t.Fatal("second first-login delivery succeeded")
	}
}

func TestEvaluationBootstrapUsesExactProjectCandidatePipeline(t *testing.T) {
	source, err := os.ReadFile("evaluation.go")
	require.NoError(t, err)
	body := string(source)
	for _, required := range []string{
		"projectcli.RunDev",
		"projectcli.RunPublish",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("evaluation bootstrap is missing %s", required)
		}
	}
	if strings.Contains(body, "runDeploy(") {
		t.Fatal("evaluation bootstrap bypasses the candidate publication pipeline")
	}
}

func TestEvaluationFirstLoginRetainedWhenDeliveryFails(t *testing.T) {
	home := t.TempDir()
	contents := []byte(`{"email":"admin@localhost","temporaryPassword":"temporary","publisherToken":"publisher","publisherTokenExpiresAt":"2026-07-28T00:00:00Z"}` + "\n")
	if err := securefs.WritePrivateFileAtomic(evaluationFirstLoginPath(home), contents); err != nil {
		t.Fatal(err)
	}
	if err := consumeEvaluationFirstLogin(home, evaluationErrorWriter{}); err == nil {
		t.Fatal("first-login output failure = nil")
	}
	if _, err := os.Stat(filepath.Join(home, evaluationFirstLoginFileName)); err != nil {
		t.Fatalf("first-login credentials not retained: %v", err)
	}
}

type evaluationErrorWriter struct{}

func (evaluationErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("credential destination failed")
}

func TestBundledEvaluationProjectCompilesAndPlansOneSmallManagedFile(t *testing.T) {
	root, err := evaluationAssetsRoot()
	require.NoError(t, err)
	projectPath := filepath.Join(root, evaluationProjectRelativePath)
	compiled, err := projectcompiler.CompileProject(projectPath)
	require.NoError(t, err)
	if compiled.ProjectID().String() != evaluationProjectID {
		t.Fatalf("compiled evaluation project = %q, want %q", compiled.ProjectID(), evaluationProjectID)
	}
	plan, err := localplan.NewService(loadManagedDataPlanProject).Plan(context.Background(), localplan.Request{
		ProjectPath: projectPath,
		Connection:  evaluationConnection,
		From:        filepath.Join(root, evaluationDataRelativePath),
	})
	require.NoError(t, err)
	if len(plan.Manifest.Files) != 1 || plan.Manifest.Files[0].Path != "orders.csv" || plan.Manifest.Files[0].Size > 16<<10 {
		t.Fatalf("evaluation manifest = %#v", plan.Manifest)
	}
}

func TestEvaluationCompletionMarkerIsStrictAndPrivate(t *testing.T) {
	home := t.TempDir()
	completion := evaluationCompletion{
		ProjectID:  evaluationProjectID,
		Dashboard:  evaluationDashboardID,
		RevisionID: "sha256:" + strings.Repeat("a", 64),
	}
	if err := writeEvaluationCompletion(home, completion); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(evaluationCompletePath(home))
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("completion mode = %o, want 600", info.Mode().Perm())
	}
	got, err := readEvaluationCompletion(home)
	if err != nil || got != completion {
		t.Fatalf("completion = %#v, %v", got, err)
	}
}
