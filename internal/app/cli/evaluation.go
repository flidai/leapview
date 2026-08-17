package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	appadminoffline "github.com/flidai/leapview/internal/app/adminoffline"
	"github.com/flidai/leapview/internal/app/config"
	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

const (
	evaluationEnvironment           = "evaluation"
	evaluationDefaultPort           = 8080
	evaluationAdminEmail            = "admin@localhost"
	evaluationRuntimeConfigFileName = ".evaluation-runtime.json"
	evaluationFirstLoginFileName    = ".evaluation-first-login.json"
	evaluationBootstrapFileName     = ".evaluation-bootstrap.json"
	evaluationCompleteFileName      = ".evaluation-complete.json"
	evaluationAuthoringFileName     = ".evaluation-authoring.json"
	evaluationFirstLoginLockName    = ".evaluation-first-login.lock"
	evaluationProjectRelativePath   = "project/leapview.yaml"
	evaluationDataRelativePath      = "data"
	evaluationConnection            = "sample"
	evaluationProjectID             = "project:leapview-evaluation"
	evaluationDashboardID           = "dashboard:sales-overview"
)

type evaluationOptions struct {
	Port uint16
}

type evaluationTarget struct {
	ListenAddress string
	PublicURL     string
	ServerOrigin  string
}

type evaluationRuntimeConfig struct {
	CSRFKey      string `json:"csrfKey"`
	MetricsToken string `json:"metricsToken"`
}

type evaluationBootstrapCredentials struct {
	PublisherToken string `json:"publisherToken"`
}

type evaluationCompletion struct {
	ProjectID  string `json:"projectId"`
	Dashboard  string `json:"dashboard"`
	RevisionID string `json:"revisionId"`
}

func evaluationCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	options := evaluationOptions{Port: evaluationDefaultPort}
	command := &cobra.Command{
		Use:   "evaluate",
		Short: "Run the self-contained local evaluation server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEvaluation(ctx, opts, options, cmd.OutOrStdout())
		},
	}
	command.Flags().Uint16Var(
		&options.Port,
		"port",
		options.Port,
		"loopback port for this evaluation target",
	)
	command.AddCommand(&cobra.Command{
		Use:   "first-login",
		Short: "Print and consume the one-time local evaluation credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := configuredEvaluationHome()
			if err != nil {
				return err
			}
			return consumeEvaluationFirstLogin(home, cmd.OutOrStdout())
		},
	})
	return command
}

func runEvaluation(
	ctx context.Context,
	_ *rootOptions,
	options evaluationOptions,
	out io.Writer,
) error {
	home, err := configuredEvaluationHome()
	if err != nil {
		return err
	}
	target, err := newEvaluationTarget(options.Port)
	if err != nil {
		return err
	}
	if err := configureEvaluationEnvironment(home, target); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(config.ProfileServe); err != nil {
		return fmt.Errorf("validate evaluation configuration: %w", err)
	}
	if _, err := readEvaluationCompletion(home); err == nil {
		return runCompletedEvaluation(ctx, target, out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	token, err := prepareEvaluationCredentials(ctx, home)
	if err != nil {
		return err
	}
	assets, err := evaluationAssetsRoot()
	if err != nil {
		return err
	}
	projectPath := filepath.Join(assets, evaluationProjectRelativePath)
	dataPath := filepath.Join(assets, evaluationDataRelativePath)
	planner := localplan.NewService(loadManagedDataPlanProject)
	plan, err := planner.Plan(ctx, localplan.Request{
		ProjectPath: projectPath,
		Connection:  evaluationConnection,
		From:        dataPath,
	})
	if err != nil {
		return fmt.Errorf("plan evaluation data: %w", err)
	}

	serverCtx, stopServer := context.WithCancel(ctx)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- runServe(serverCtx, &rootOptions{production: true, environment: evaluationEnvironment})
	}()
	bootstrapFailed := true
	defer func() {
		if bootstrapFailed {
			stopServer()
			<-serverErr
		}
	}()
	if err := waitForEvaluationEndpoint(ctx, serverErr, target.ServerOrigin+"/healthz", 45*time.Second); err != nil {
		return err
	}
	if err := manageddatacli.RunSync(ctx, manageddatacli.SyncRequest{
		ProjectPath: projectPath,
		ProjectID:   evaluationProjectID,
		Connection:  evaluationConnection,
		Root:        plan.Root,
		Target:      target.ServerOrigin,
		Token:       token,
		Plan:        plan,
		Out:         out,
		HTTPClient:  http.DefaultClient,
	}); err != nil {
		return fmt.Errorf("stage evaluation data: %w", err)
	}
	revisionID := plan.Manifest.RevisionID()
	if err := publishEvaluationProject(
		ctx,
		home,
		projectPath,
		target.ServerOrigin,
		token,
		out,
	); err != nil {
		return err
	}
	if err := waitForEvaluationEndpoint(ctx, serverErr, target.ServerOrigin+"/readyz", 2*time.Minute); err != nil {
		return err
	}
	completion := evaluationCompletion{
		ProjectID: evaluationProjectID, Dashboard: evaluationDashboardID, RevisionID: revisionID,
	}
	if err := writeEvaluationCompletion(home, completion); err != nil {
		return err
	}
	if err := os.Remove(evaluationBootstrapPath(home)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove evaluation bootstrap credential: %w", err)
	}
	if err := syncEvaluationDirectory(home); err != nil {
		return err
	}
	bootstrapFailed = false
	writeEvaluationReadyMessage(out, target)
	return <-serverErr
}

func runCompletedEvaluation(
	ctx context.Context,
	target evaluationTarget,
	out io.Writer,
) error {
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- runServe(
			serverCtx,
			&rootOptions{
				production:  true,
				environment: evaluationEnvironment,
			},
		)
	}()
	if err := waitForEvaluationEndpoint(
		ctx,
		serverErr,
		target.ServerOrigin+"/readyz",
		2*time.Minute,
	); err != nil {
		stopServer()
		<-serverErr
		return err
	}
	writeEvaluationReadyMessage(out, target)
	return <-serverErr
}

func publishEvaluationProject(
	ctx context.Context,
	home,
	projectPath,
	targetOrigin,
	token string,
	out io.Writer,
) error {
	client := capabilityAPIClient{
		httpClient:        authoringRefreshingHTTPClient(http.DefaultClient),
		validateAuthoring: true,
	}
	credentials := cliapi.Credentials{
		Target: targetOrigin,
		Token:  token,
	}
	checkpoints := projectcli.NewCandidateCheckpointStore(
		filepath.Join(home, evaluationAuthoringFileName),
	)
	if err := projectcli.RunDev(
		ctx,
		client,
		checkpoints,
		projectDevRemoteFactory{client: client},
		projectcli.DevOptions{
			ProjectPath:       projectPath,
			Credentials:       credentials,
			UploadConcurrency: 4,
			Once:              true,
		},
		nil,
		out,
		out,
	); err != nil {
		return fmt.Errorf("synchronize evaluation candidate: %w", err)
	}
	if err := projectcli.RunPublish(
		ctx,
		client,
		checkpoints,
		projectPublishOperations{
			client:        client,
			requireActive: true,
		},
		projectcli.PublishOptions{
			ProjectPath: projectPath,
			Credentials: credentials,
		},
		out,
	); err != nil {
		return fmt.Errorf("publish evaluation candidate: %w", err)
	}
	return nil
}

func waitForEvaluationEndpoint(ctx context.Context, serverErr chan error, endpoint string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(waitCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case err := <-serverErr:
			serverErr <- err
			if err == nil {
				return fmt.Errorf("evaluation server stopped before bootstrap completed")
			}
			return fmt.Errorf("evaluation server failed during bootstrap: %w", err)
		case <-waitCtx.Done():
			return fmt.Errorf("wait for evaluation endpoint %s: %w", endpoint, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func writeEvaluationReadyMessage(out io.Writer, target evaluationTarget) {
	_, _ = fmt.Fprintf(
		out,
		"LeapView evaluation is ready at %s/dashboards/sales-overview\n",
		target.PublicURL,
	)
	_, _ = fmt.Fprintln(out, "Run `leapview evaluate first-login` with the same LEAPVIEW_HOME once to retrieve the required sign-in credentials.")
	_, _ = fmt.Fprintln(out, "Evaluation mode is disposable and loopback-only; use the installation guide before connecting real data.")
}

func evaluationAssetsRoot() (string, error) {
	if evaluationAssetsExist("/app/evaluation") {
		return "/app/evaluation", nil
	}
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(current, "evaluation")
		if evaluationAssetsExist(candidate) {
			return candidate, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("bundled evaluation assets were not found")
		}
		current = parent
	}
}

func evaluationAssetsExist(root string) bool {
	for _, relative := range []string{evaluationProjectRelativePath, filepath.Join(evaluationDataRelativePath, "orders.csv")} {
		info, err := os.Stat(filepath.Join(root, relative))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func writeEvaluationCompletion(home string, completion evaluationCompletion) error {
	if completion.ProjectID != evaluationProjectID || completion.Dashboard != evaluationDashboardID ||
		!canonicalManagedRevisionID(completion.RevisionID) {
		return fmt.Errorf("evaluation completion is invalid")
	}
	contents, err := json.Marshal(completion)
	if err != nil {
		return err
	}
	if err := securefs.WritePrivateFileAtomic(evaluationCompletePath(home), append(contents, '\n')); err != nil {
		return fmt.Errorf("write evaluation completion: %w", err)
	}
	return nil
}

func readEvaluationCompletion(home string) (evaluationCompletion, error) {
	contents, err := readPrivateRegularFile(evaluationCompletePath(home))
	if err != nil {
		return evaluationCompletion{}, err
	}
	var completion evaluationCompletion
	if err := json.Unmarshal(contents, &completion); err != nil {
		return evaluationCompletion{}, fmt.Errorf("decode evaluation completion: %w", err)
	}
	if completion.ProjectID != evaluationProjectID || completion.Dashboard != evaluationDashboardID ||
		!canonicalManagedRevisionID(completion.RevisionID) {
		return evaluationCompletion{}, fmt.Errorf("evaluation completion is invalid")
	}
	return completion, nil
}

func configuredEvaluationHome() (string, error) {
	home := strings.TrimSpace(os.Getenv("LEAPVIEW_HOME"))
	if home == "" {
		home = ".leapview"
	}
	absolute, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve evaluation home: %w", err)
	}
	return absolute, nil
}

func newEvaluationTarget(port uint16) (evaluationTarget, error) {
	if port == 0 {
		return evaluationTarget{}, fmt.Errorf(
			"evaluation port must be between 1 and 65535",
		)
	}
	return evaluationTarget{
		ListenAddress: fmt.Sprintf(":%d", port),
		PublicURL:     fmt.Sprintf("http://localhost:%d", port),
		ServerOrigin:  fmt.Sprintf("http://127.0.0.1:%d", port),
	}, nil
}

func configureEvaluationEnvironment(
	home string,
	target evaluationTarget,
) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return fmt.Errorf("evaluation home is required")
	}
	if strings.TrimSpace(target.ListenAddress) == "" ||
		strings.TrimSpace(target.PublicURL) == "" ||
		strings.TrimSpace(target.ServerOrigin) == "" {
		return fmt.Errorf("evaluation target is required")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create evaluation home: %w", err)
	}
	runtime, err := readEvaluationRuntimeConfig(home)
	if errors.Is(err, os.ErrNotExist) {
		runtime, err = newEvaluationRuntimeConfig()
		if err == nil {
			encoded, encodeErr := json.Marshal(runtime)
			if encodeErr != nil {
				return encodeErr
			}
			encoded = append(encoded, '\n')
			err = securefs.WritePrivateFileAtomic(evaluationRuntimeConfigPath(home), encoded)
		}
	}
	if err != nil {
		return err
	}
	settings := map[string]string{
		"LEAPVIEW_ADDR":                        target.ListenAddress,
		"LEAPVIEW_ALLOWED_HOSTS":               "localhost,127.0.0.1",
		"LEAPVIEW_API_TOKEN_ONLY_AUTH":         "false",
		"LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL":       evaluationAdminEmail,
		"LEAPVIEW_COOKIE_SECURE":               "false",
		"LEAPVIEW_CSRF_KEY":                    runtime.CSRFKey,
		"LEAPVIEW_DEV_AUTH_BYPASS":             "false",
		"LEAPVIEW_ENVIRONMENT":                 evaluationEnvironment,
		"LEAPVIEW_EVALUATION_MODE":             "true",
		"LEAPVIEW_HOME":                        home,
		"LEAPVIEW_LOCAL_AUTH":                  "true",
		"LEAPVIEW_MANAGED_DATA_BACKEND":        "local",
		"LEAPVIEW_MANAGED_DATA_DIR":            filepath.Join(home, "managed-data"),
		"LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES": "67108864",
		"LEAPVIEW_METRICS_BEARER_TOKEN":        runtime.MetricsToken,
		"LEAPVIEW_PRODUCTION":                  "true",
		"LEAPVIEW_PUBLIC_URL":                  target.PublicURL,
		"LEAPVIEW_TRUST_PROXY_HEADERS":         "false",
	}
	for name, value := range settings {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	for _, name := range []string{
		"LEAPVIEW_AZURE_CALLBACK_URL", "LEAPVIEW_AZURE_CLIENT_ID", "LEAPVIEW_AZURE_CLIENT_SECRET", "LEAPVIEW_AZURE_TENANT",
		"LEAPVIEW_MCP_OAUTH_ISSUER_URL", "LEAPVIEW_OIDC_CALLBACK_URL", "LEAPVIEW_OIDC_CLIENT_ID",
		"LEAPVIEW_OIDC_CLIENT_SECRET", "LEAPVIEW_OIDC_ISSUER_URL", "LEAPVIEW_OIDC_SCOPES",
	} {
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("clear %s: %w", name, err)
		}
	}
	return nil
}

func newEvaluationRuntimeConfig() (evaluationRuntimeConfig, error) {
	csrf, err := evaluationRandomHex(32)
	if err != nil {
		return evaluationRuntimeConfig{}, err
	}
	metrics, err := evaluationRandomHex(32)
	if err != nil {
		return evaluationRuntimeConfig{}, err
	}
	return evaluationRuntimeConfig{CSRFKey: csrf, MetricsToken: metrics}, nil
}

func evaluationRandomHex(size int) (string, error) {
	contents := make([]byte, size)
	if _, err := rand.Read(contents); err != nil {
		return "", fmt.Errorf("generate evaluation secret: %w", err)
	}
	return hex.EncodeToString(contents), nil
}

func readEvaluationRuntimeConfig(home string) (evaluationRuntimeConfig, error) {
	contents, err := readPrivateRegularFile(evaluationRuntimeConfigPath(home))
	if err != nil {
		return evaluationRuntimeConfig{}, err
	}
	var runtime evaluationRuntimeConfig
	if err := json.Unmarshal(contents, &runtime); err != nil {
		return evaluationRuntimeConfig{}, fmt.Errorf("decode evaluation runtime configuration: %w", err)
	}
	if len(runtime.CSRFKey) < 32 || len(runtime.MetricsToken) < 32 {
		return evaluationRuntimeConfig{}, fmt.Errorf("evaluation runtime configuration is invalid")
	}
	return runtime, nil
}

func prepareEvaluationCredentials(ctx context.Context, home string) (string, error) {
	token, err := readEvaluationBootstrapToken(home)
	if err == nil {
		if _, statErr := os.Stat(filepath.Join(home, adminoffline.CredentialRecoveryFileName)); statErr == nil {
			if ackErr := (appadminoffline.Operations{}).AcknowledgeInitialCredentials(ctx); ackErr != nil {
				return "", ackErr
			}
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var output bytes.Buffer
	if err := (appadminoffline.Operations{}).Initialize(ctx, adminoffline.InitializeRequest{Format: "json"}, &output); err != nil {
		return "", fmt.Errorf("initialize evaluation administrator: %w", err)
	}
	var credentials adminoffline.InitialCredentials
	if err := json.Unmarshal(output.Bytes(), &credentials); err != nil || credentials.PublisherToken == "" {
		return "", fmt.Errorf("evaluation initialization returned invalid credentials")
	}
	if err := securefs.WritePrivateFileAtomic(evaluationFirstLoginPath(home), output.Bytes()); err != nil {
		return "", fmt.Errorf("store evaluation first-login credentials: %w", err)
	}
	bootstrap, err := json.Marshal(evaluationBootstrapCredentials{PublisherToken: credentials.PublisherToken})
	if err != nil {
		return "", err
	}
	if err := securefs.WritePrivateFileAtomic(evaluationBootstrapPath(home), append(bootstrap, '\n')); err != nil {
		return "", fmt.Errorf("store evaluation bootstrap credential: %w", err)
	}
	if err := (appadminoffline.Operations{}).AcknowledgeInitialCredentials(ctx); err != nil {
		return "", fmt.Errorf("acknowledge evaluation initialization credentials: %w", err)
	}
	return credentials.PublisherToken, nil
}

func readEvaluationBootstrapToken(home string) (string, error) {
	contents, err := readPrivateRegularFile(evaluationBootstrapPath(home))
	if err != nil {
		return "", err
	}
	var credentials evaluationBootstrapCredentials
	if err := json.Unmarshal(contents, &credentials); err != nil || strings.TrimSpace(credentials.PublisherToken) == "" {
		return "", fmt.Errorf("evaluation bootstrap credential is invalid")
	}
	return credentials.PublisherToken, nil
}

func consumeEvaluationFirstLogin(home string, out io.Writer) error {
	lock, err := instancelock.AcquireNamed(home, evaluationFirstLoginLockName)
	if err != nil {
		return fmt.Errorf("acquire evaluation first-login lock: %w", err)
	}
	defer lock.Release()
	contents, err := securefs.ReadPrivateFile(evaluationFirstLoginPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("evaluation first-login credentials have already been consumed or were never created")
		}
		return err
	}
	if _, err := adminoffline.DecodeInitialCredentials(contents); err != nil {
		return err
	}
	written, err := out.Write(contents)
	if err == nil && written != len(contents) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return err
	}
	if err := os.Remove(evaluationFirstLoginPath(home)); err != nil {
		return fmt.Errorf("consume evaluation first-login credentials: %w", err)
	}
	return syncEvaluationDirectory(home)
}

func readPrivateRegularFile(path string) ([]byte, error) {
	return securefs.ReadPrivateFile(path)
}

func syncEvaluationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func evaluationRuntimeConfigPath(home string) string {
	return filepath.Join(home, evaluationRuntimeConfigFileName)
}

func evaluationFirstLoginPath(home string) string {
	return filepath.Join(home, evaluationFirstLoginFileName)
}

func evaluationBootstrapPath(home string) string {
	return filepath.Join(home, evaluationBootstrapFileName)
}

func evaluationCompletePath(home string) string {
	return filepath.Join(home, evaluationCompleteFileName)
}
