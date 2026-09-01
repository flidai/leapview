package composectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type qualificationInstalledReport struct {
	SchemaVersion  int                          `json:"schemaVersion"`
	Result         string                       `json:"result"`
	Image          string                       `json:"image"`
	Architecture   string                       `json:"architecture"`
	StartedAt      string                       `json:"startedAt"`
	CompletedAt    string                       `json:"completedAt"`
	ElapsedSeconds int64                        `json:"elapsedSeconds"`
	Phases         []qualificationPhaseEvidence `json:"phases"`
	Assertions     struct {
		OneTimeCredentials   bool `json:"oneTimeCredentials"`
		BrowserJourney       bool `json:"browserJourney"`
		PerformanceBudgets   bool `json:"performanceBudgets"`
		GovernedQuery        bool `json:"governedQuery"`
		AuditedDenial        bool `json:"auditedDenial"`
		InterruptionRecovery bool `json:"interruptionRecovery"`
		RestartPersistence   bool `json:"restartPersistence"`
	} `json:"assertions"`
}

func (c *Controller) QualifyInstalledCandidate(
	ctx context.Context,
	options QualificationInstalledOptions,
) (runErr error) {
	if bundle := strings.TrimSpace(options.Bundle); bundle != "" {
		bundleRoot, err := filepath.Abs(bundle)
		if err != nil {
			return err
		}
		options.Bundle = ""
		if bundleRoot != c.root {
			bundleController, err := c.scoped(bundleRoot, c.stdout)
			if err != nil {
				return err
			}
			return bundleController.QualifyInstalledCandidate(ctx, options)
		}
	}
	rootContext := ctx
	started := c.now()
	phases := newQualificationPhaseTracker(c.now)
	report := qualificationInstalledReport{
		SchemaVersion: qualificationEvidenceSchema,
		Result:        "failure",
		Architecture:  runtime.GOARCH,
		StartedAt:     qualificationStartedAt(started),
	}
	evidenceDir := strings.TrimSpace(options.EvidenceDir)
	if evidenceDir == "" {
		evidenceDir = c.path("qualification-evidence")
	}
	var err error
	evidenceDir, err = filepath.Abs(evidenceDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "preflight", 15*time.Minute)
	for _, pattern := range []string{
		"authoring-browser-failure.json",
		"authoring-browser-failure.png",
		"authoring-report.json",
		"browser-failure.png",
		"compose.log",
		"decision.json",
		"performance-report.json",
		"policy-validation.json",
		"qualification-report.json",
		"recovery-events.json",
		"recovery-report.json",
		"runtime-identity.json",
	} {
		_ = os.Remove(filepath.Join(evidenceDir, pattern))
	}
	coldResults, _ := filepath.Glob(filepath.Join(evidenceDir, "performance-cold-*.json"))
	for _, path := range coldResults {
		_ = os.Remove(path)
	}

	cleanup := qualificationCleanup{}
	var primaryStarted bool
	var primaryProject string
	var browserContainer string
	credentialsPath := filepath.Join(c.root, ".qualification-credentials.json")
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		runErr = joinQualificationError(runErr, cleanup.Run(cleanupCtx))
		runErr = phases.Finish(runErr)
		if runErr != nil {
			report.Result = "failure"
		}
		report.Phases = phases.Evidence()
		if runErr != nil {
			report.CompletedAt = qualificationStartedAt(c.now())
			report.ElapsedSeconds = int64(c.now().Sub(started).Seconds())
			_ = writeQualificationJSON(
				filepath.Join(evidenceDir, "qualification-report.json"),
				report,
			)
		}
	}()
	cleanup.Add(func(context.Context) error {
		for _, path := range []string{
			c.path(deploymentEnvName),
			c.path(appEnvName),
			c.path(credentialsName),
			credentialsPath,
		} {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
		return nil
	})
	cleanup.Add(func(cleanupCtx context.Context) error {
		if browserContainer == "" {
			return nil
		}
		_, err := c.qualificationContainers.Existing(browserContainer).Remove(cleanupCtx)
		return ignoreQualificationNotFound(err)
	})
	cleanup.Add(func(cleanupCtx context.Context) error {
		if !primaryStarted {
			return nil
		}
		logs, _ := c.qualificationCompose(
			cleanupCtx, c.root, "logs", "--no-color", "--tail", "500",
		)
		_ = os.WriteFile(
			filepath.Join(evidenceDir, "compose.log"),
			redactQualificationLog(logs, 500),
			0o600,
		)
		_, err := c.qualificationCompose(
			cleanupCtx, c.root, "down", "--volumes", "--remove-orphans",
		)
		return ignoreQualificationNotFound(err)
	})

	for _, path := range []string{
		c.path(deploymentEnvName),
		c.path(appEnvName),
		c.path(credentialsName),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			return fmt.Errorf("qualification requires a fresh extracted bundle; found %s", filepath.Base(path))
		}
	}
	if err := verifyQualificationChecksums(c.root); err != nil {
		return err
	}
	imageReferenceBytes, err := os.ReadFile(c.path("image-reference.txt"))
	if err != nil {
		return err
	}
	imageReference := strings.TrimSpace(string(imageReferenceBytes))
	report.Image = imageReference
	if !options.AllowLocal &&
		(!strings.HasPrefix(imageReference, "ghcr.io/flidai/leapview@sha256:") ||
			len(strings.TrimPrefix(imageReference, "ghcr.io/flidai/leapview@sha256:")) != 64) {
		return fmt.Errorf("qualification requires an immutable LeapView GHCR digest")
	}
	if options.MinFreeBytes > 0 && !options.AllowLocal {
		return fmt.Errorf("minimum-free-bytes is a local-image qualification override")
	}
	if options.AllowLocal {
		if _, err := c.qualificationDocker(ctx, nil, "image", "inspect", imageReference); err != nil {
			return err
		}
	} else {
		_, _ = c.qualificationDocker(ctx, nil, "logout", "ghcr.io")
		if _, err := c.qualificationDocker(ctx, nil, "pull", imageReference); err != nil {
			return err
		}
	}
	if err := c.verifyQualificationRuntimeIdentity(ctx, imageReference, evidenceDir); err != nil {
		return err
	}
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "target bootstrap", 20*time.Minute)

	runSuffix := normalizedQualificationName(
		fmt.Sprintf(
			"%s-%s-%d",
			os.Getenv("GITHUB_RUN_ID"),
			runtime.GOARCH,
			os.Getpid(),
		),
	)
	primaryProject = "leapview-qualification-" + runSuffix
	if err := copyQualificationFile(
		c.path("deployment.env.example"),
		c.path(deploymentEnvName),
		0o600,
	); err != nil {
		return err
	}
	if err := updateEnvFile(c.path(deploymentEnvName), map[string]string{
		"COMPOSE_PROJECT_NAME": primaryProject,
		"LEAPVIEW_IMAGE":       imageReference,
		"CADDY_DOMAIN":         "localhost",
	}); err != nil {
		return err
	}
	if err := c.Initialize(ctx, InitOptions{
		AdminEmail:  "admin@localhost",
		Domain:      "localhost",
		Environment: "evaluation",
		Image:       imageReference,
	}); err != nil {
		return err
	}
	if options.MinFreeBytes > 0 {
		if err := appendOrReplaceQualificationEnv(
			c.path(appEnvName),
			"LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES",
			strconv.FormatInt(options.MinFreeBytes, 10),
		); err != nil {
			return err
		}
	}
	if err := appendOrReplaceQualificationEnv(
		c.path(appEnvName),
		"LEAPVIEW_REFRESH_JOB_LEASE_TIMEOUT",
		"15s",
	); err != nil {
		return err
	}
	if err := c.bootstrapQualificationLocalPhysicalPool(ctx); err != nil {
		return err
	}
	if !primaryStarted {
		primaryStarted = true
		if err := c.startQualificationBootstrap(ctx); err != nil {
			return err
		}
	}
	var credentialsOutput bytes.Buffer
	originalOutput := c.stdout
	c.stdout = &credentialsOutput
	if err := c.FirstLogin(); err != nil {
		c.stdout = originalOutput
		return err
	}
	c.stdout = io.Discard
	if err := c.FirstLogin(); err == nil {
		c.stdout = originalOutput
		return fmt.Errorf("one-time credentials were delivered more than once")
	}
	c.stdout = originalOutput
	var credentials qualificationCredentials
	if err := json.Unmarshal(credentialsOutput.Bytes(), &credentials); err != nil {
		return err
	}
	if credentials.Email == "" || credentials.TemporaryPassword == "" ||
		credentials.PublisherToken == "" || credentials.PublisherTokenExpires == "" {
		return fmt.Errorf("initial credential contract is incomplete")
	}
	credentials.QualificationPassword, err = randomHex(24)
	if err != nil {
		return err
	}
	if err := writeQualificationJSON(credentialsPath, credentials); err != nil {
		return err
	}
	report.Assertions.OneTimeCredentials = true

	containerID, err := c.containerID(ctx)
	if err != nil {
		return err
	}
	syncOutput, err := c.qualificationContainers.Existing(containerID).Exec(
		ctx, nil,
		"env",
		"LEAPVIEW_API_TOKEN="+credentials.PublisherToken,
		"LEAPVIEW_TARGET=http://localhost:8080",
		"leapview", "data", "sync",
		"--project", "/app/evaluation/project/leapview.yaml",
		"--connection", "sample",
		"--from", "/app/evaluation/data",
		"--format", "json",
	)
	if err != nil {
		return err
	}
	sourceRevision, err := parseStagedQualificationRevision(string(syncOutput))
	if err != nil {
		return err
	}
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "enterprise authoring", 30*time.Minute)
	if _, err := c.runQualificationAuthoring(ctx, qualificationAuthoringOptions{
		BundleRoot:      c.root,
		Image:           imageReference,
		CredentialsFile: credentialsPath,
		ComposeProject:  primaryProject,
		EvidenceDir:     evidenceDir,
		SourceRevision:  sourceRevision,
	}); err != nil {
		return err
	}
	if err := c.waitQualificationReadiness(ctx); err != nil {
		return fmt.Errorf("installed candidate did not become ready after sealed publication: %w", err)
	}
	if err := readQualificationJSON(credentialsPath, &credentials); err != nil {
		return err
	}
	workloadToken, err := credentials.workloadToken()
	if err != nil {
		return err
	}
	projectDataToken, err := credentials.projectDataToken()
	if err != nil {
		return err
	}
	recoveryControlToken, err := credentials.recoveryControlToken()
	if err != nil {
		return err
	}
	report.Assertions.BrowserJourney = true
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "performance", 45*time.Minute)

	metricsToken, err := envFileValue(c.path(appEnvName), "LEAPVIEW_METRICS_BEARER_TOKEN")
	if err != nil {
		return err
	}
	browserContainer, err = c.startQualificationPerformanceBrowser(
		ctx,
		primaryProject,
		credentialsPath,
		evidenceDir,
	)
	if err != nil {
		return err
	}
	if err := c.runQualificationPerformance(
		ctx,
		browserContainer,
		containerID,
		evidenceDir,
		imageReference,
		metricsToken,
	); err != nil {
		return err
	}
	report.Assertions.PerformanceBudgets = true
	_, _ = c.qualificationContainers.Existing(browserContainer).Remove(ctx)
	browserContainer = ""
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "governance", 10*time.Minute)

	queryBody := `{"dimensions":[{"field":"state"}],"metrics":[{"field":"order_count"},{"field":"revenue"}],"limit":10}`
	queryOutput, err := c.qualificationContainers.Existing(containerID).Exec(
		ctx, nil,
		"env",
		"LEAPVIEW_API_TOKEN="+workloadToken,
		"LEAPVIEW_TARGET=http://localhost:8080",
		"leapview", "api", "call", "querySemanticModel",
		"--path", "model=semantic-model:sales",
		"--body-json", queryBody,
	)
	if err != nil {
		return err
	}
	var queryResult struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(queryOutput, &queryResult); err != nil || len(queryResult.Rows) != 4 {
		return fmt.Errorf("governed qualification query failed")
	}
	report.Assertions.GovernedQuery = true
	if err := verifyQualificationDenialsAndMetrics(
		ctx, queryBody, metricsToken,
	); err != nil {
		return err
	}
	report.Assertions.AuditedDenial = true
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "interruption recovery", 60*time.Minute)

	recoveryReport, err := c.runQualificationRecovery(ctx, qualificationRecoveryOptions{
		BundleRoot:           c.root,
		EvidenceDir:          evidenceDir,
		PublisherToken:       credentials.PublisherToken,
		WorkloadToken:        workloadToken,
		ProjectDataToken:     projectDataToken,
		RecoveryControlToken: recoveryControlToken,
		MetricsToken:         metricsToken,
		ContainerID:          containerID,
		ComposeProject:       primaryProject,
		ProjectID:            "project:leapview-evaluation",
		Image:                imageReference,
	})
	if err != nil {
		return err
	}
	if recoveryReport.Result != "success" {
		return fmt.Errorf("recovery qualification report is incomplete")
	}
	report.Assertions.InterruptionRecovery = true
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "restart persistence", 15*time.Minute)
	containerID, err = c.containerID(ctx)
	if err != nil {
		return err
	}
	if _, err := c.qualificationContainers.Existing(containerID).Restart(ctx); err != nil {
		return err
	}
	if err := c.waitQualificationHealthy(ctx, containerID, "restart persistence"); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var instance json.RawMessage
	if err := qualificationAPI(
		ctx, client, http.MethodGet,
		"http://127.0.0.1:8080/api/v1/instance",
		credentials.PublisherToken, nil, "", &instance,
	); err != nil {
		return err
	}
	report.Assertions.RestartPersistence = true
	if err := phases.Finish(nil); err != nil {
		return err
	}
	report.Phases = phases.Evidence()

	report.Result = "success"
	report.CompletedAt = qualificationStartedAt(c.now())
	report.ElapsedSeconds = int64(c.now().Sub(started).Seconds())
	if err := writeQualificationJSON(
		filepath.Join(evidenceDir, "qualification-report.json"),
		report,
	); err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		c.stdout,
		"installed-candidate qualification passed in %d seconds\n",
		report.ElapsedSeconds,
	)
	return err
}

func isQualificationLowerHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func (c *Controller) bootstrapQualificationLocalPhysicalPool(ctx context.Context) error {
	output, err := c.qualificationCompose(
		ctx,
		c.root,
		"run", "--rm", "--no-deps", "leapview",
		"admin", "delivery", "pool", "qualify", "--apply",
	)
	if err != nil {
		return fmt.Errorf("bootstrap installed-candidate physical pool: %w", err)
	}
	poolID, compatibilityDigest, err := parseQualificationPoolBootstrapResult(output)
	if err != nil {
		return err
	}
	for _, entry := range []struct {
		key   string
		value string
	}{
		{key: "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID", value: poolID},
		{key: "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST", value: compatibilityDigest},
	} {
		if err := appendOrReplaceQualificationEnv(c.path(appEnvName), entry.key, entry.value); err != nil {
			return err
		}
	}
	return nil
}

func parseQualificationPoolBootstrapResult(output []byte) (string, string, error) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	poolID := values["pool_id"]
	compatibilityDigest := values["compatibility_digest"]
	validDigest := func(value string) bool {
		raw := strings.TrimPrefix(value, "sha256:")
		if len(raw) != 64 || raw == value {
			return false
		}
		_, err := hex.DecodeString(raw)
		return err == nil
	}
	if values["applied"] != "true" || !validDigest(poolID) || !validDigest(compatibilityDigest) {
		return "", "", fmt.Errorf("qualification physical-pool bootstrap returned incomplete durable evidence")
	}
	return poolID, compatibilityDigest, nil
}

func (c *Controller) startQualificationBootstrap(ctx context.Context) error {
	if _, err := c.qualificationCompose(ctx, c.root, "up", "-d", "leapview"); err != nil {
		return err
	}
	if err := c.waitQualificationBootstrapLiveness(ctx); err != nil {
		return err
	}
	if _, err := c.qualificationCompose(ctx, c.root, "up", "-d", "--no-deps", "caddy"); err != nil {
		return fmt.Errorf("start qualification HTTPS proxy during target bootstrap: %w", err)
	}
	return nil
}

func (c *Controller) waitQualificationBootstrapLiveness(ctx context.Context) error {
	container, err := c.qualificationApplicationContainer(ctx)
	if err != nil {
		return err
	}
	if err := waitQualificationHealthcheck(
		ctx,
		container,
		"http://127.0.0.1:8080/healthz",
		2*time.Minute,
	); err != nil {
		return fmt.Errorf("wait for qualification bootstrap liveness: %w", err)
	}
	return nil
}

func (c *Controller) waitQualificationReadiness(ctx context.Context) error {
	container, err := c.qualificationApplicationContainer(ctx)
	if err != nil {
		return err
	}
	if err := waitQualificationHealthcheck(
		ctx,
		container,
		"http://127.0.0.1:8080/readyz",
		3*time.Minute,
	); err != nil {
		return fmt.Errorf("wait for qualification readiness: %w", err)
	}
	if err := waitQualificationContainerValue(
		ctx,
		container,
		"{{.State.Health.Status}}",
		"healthy",
		time.Minute,
	); err != nil {
		err = qualificationContainerOperationError(
			ctx,
			container,
			"wait for container state healthy",
			err,
		)
		return fmt.Errorf("wait for Docker qualification health: %w", err)
	}
	return nil
}

// qualificationApplicationContainer resolves the service container created by
// the qualification Compose project through the injected container runtime.
// Keeping the lookup and container handle together lets qualification health
// checks use the same runtime seam as all other container operations.
func (c *Controller) qualificationApplicationContainer(ctx context.Context) (qualificationContainer, error) {
	containerOutput, err := c.qualificationCompose(ctx, c.root, "ps", "--quiet", "leapview")
	if err != nil {
		return nil, err
	}
	containerID := strings.TrimSpace(string(containerOutput))
	if containerID == "" {
		return nil, fmt.Errorf("qualification application container is missing")
	}
	container := c.qualificationContainers.Existing(containerID)
	if container == nil {
		return nil, fmt.Errorf("qualification application container is missing")
	}
	return container, nil
}

// waitQualificationHealthcheck retries the in-container healthcheck command
// until it succeeds or the supplied qualification timeout expires. The
// command intentionally retains the explicit URL and five-second request
// timeout used by qualification's bootstrap/readiness stages.
func waitQualificationHealthcheck(
	ctx context.Context,
	container qualificationContainer,
	endpoint string,
	timeout time.Duration,
) error {
	if container == nil {
		return fmt.Errorf("qualification container is missing")
	}
	healthCtx, cancel := qualificationContext(ctx, timeout)
	defer cancel()
	return qualificationWait(healthCtx, time.Second, func(waitCtx context.Context) (bool, error) {
		_, checkErr := container.Exec(
			waitCtx,
			nil,
			"leapview", "healthcheck",
			"--url", endpoint,
			"--timeout", "5s",
		)
		return checkErr == nil, nil
	})
}

func verifyQualificationChecksums(root string) error {
	contents, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		return err
	}
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return fmt.Errorf("invalid SHA256SUMS line %d", lineNumber+1)
		}
		path := strings.TrimPrefix(fields[1], "*")
		path = strings.TrimPrefix(path, "./")
		path = filepath.Clean(filepath.FromSlash(path))
		if path == "." || path == ".." || filepath.IsAbs(path) ||
			strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return fmt.Errorf("SHA256SUMS line %d escapes the release root", lineNumber+1)
		}
		target := filepath.Join(root, path)
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("checksum target %s is not a regular file", path)
		}
		file, err := os.Open(target)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != fields[0] {
			return fmt.Errorf("checksum mismatch for %s", path)
		}
	}
	return nil
}

func (c *Controller) verifyQualificationRuntimeIdentity(
	ctx context.Context,
	imageReference string,
	evidenceDir string,
) error {
	runtimeOutput, err := c.qualificationDocker(
		ctx, nil, "run", "--rm", imageReference, "version", "--json",
	)
	if err != nil {
		return err
	}
	runtimePath := filepath.Join(evidenceDir, "runtime-identity.json")
	if err := os.WriteFile(runtimePath, runtimeOutput, 0o600); err != nil {
		return err
	}
	var expected, actual struct {
		Version     string `json:"version"`
		Revision    string `json:"revision"`
		BuildTime   string `json:"buildTime"`
		Dirty       bool   `json:"dirty"`
		Development bool   `json:"development"`
	}
	if err := readQualificationJSON(c.path("release-identity.json"), &expected); err != nil {
		return err
	}
	if err := json.Unmarshal(runtimeOutput, &actual); err != nil {
		return err
	}
	if expected != actual || actual.Dirty || actual.Development {
		return fmt.Errorf("runtime identity disagrees with release identity")
	}
	return nil
}

func (c *Controller) startQualificationPerformanceBrowser(
	ctx context.Context,
	composeProject string,
	credentialsPath string,
	evidenceDir string,
) (string, error) {
	if _, err := c.qualificationDocker(ctx, nil, "pull", qualificationBrowserImage); err != nil {
		return "", err
	}
	container := normalizedQualificationName(composeProject + "-browser")
	qualificationRoot := c.path("qualification")
	browser, err := c.qualificationContainers.Start(ctx, qualificationContainerRequest{
		Name:        container,
		Image:       qualificationBrowserImage,
		NetworkMode: "host",
		Volumes: []qualificationContainerVolume{
			{Source: qualificationRoot, Target: "/qualification", ReadOnly: true},
			{Source: credentialsPath, Target: "/run/secrets/credentials.json", ReadOnly: true},
			{Source: evidenceDir, Target: "/evidence"},
		},
		Environment: map[string]string{
			"QUALIFICATION_URL":         "https://localhost",
			"QUALIFICATION_CREDENTIALS": "/run/secrets/credentials.json",
			"QUALIFICATION_SCREENSHOT":  "/evidence/browser-failure.png",
		},
		Command: []string{"sleep", "infinity"},
	})
	if err != nil {
		return "", err
	}
	if _, err := browser.Exec(ctx, nil, "mkdir", "-p", "/work"); err != nil {
		return container, qualificationContainerOperationError(
			ctx, browser, "prepare browser work directory", err,
		)
	}
	for _, name := range []string{
		"package.json",
		"browser.mjs",
		"performance.mjs",
		"performance-policy.json",
	} {
		if _, err := browser.CopyTo(
			ctx, filepath.Join(qualificationRoot, name), "/work/"+name,
		); err != nil {
			return container, qualificationContainerOperationError(
				ctx, browser, "copy browser qualification asset "+name, err,
			)
		}
	}
	if _, err := browser.Exec(
		ctx, nil,
		"npm", "install", "--prefix", "/work", "--no-audit", "--no-fund", "--silent",
	); err != nil {
		return container, qualificationContainerOperationError(
			ctx, browser, "install browser qualification dependencies", err,
		)
	}
	if _, err := browser.Exec(ctx, nil, "node", "/work/browser.mjs"); err != nil {
		return container, qualificationContainerOperationError(
			ctx, browser, "run browser qualification", err,
		)
	}
	return container, nil
}

func (c *Controller) runQualificationPerformance(
	ctx context.Context,
	browserContainer string,
	appContainer string,
	evidenceDir string,
	imageReference string,
	metricsToken string,
) error {
	diskBefore, err := c.qualificationDiskUsage(
		ctx,
		appContainer,
		"performance disk before",
	)
	if err != nil {
		return err
	}
	var policy qualificationPerformancePolicy
	if err := readQualificationJSON(
		c.path(filepath.Join("qualification", "performance-policy.json")),
		&policy,
	); err != nil {
		return err
	}
	if policy.Assumptions.Samples.ColdDashboardLoads <= 0 {
		return fmt.Errorf("performance policy requires cold dashboard samples")
	}
	if failures := validateQualificationPerformancePolicy(policy); len(failures) > 0 {
		return fmt.Errorf(
			"invalid performance policy: %s",
			strings.Join(failures, "; "),
		)
	}
	coldPaths := make([]string, 0, policy.Assumptions.Samples.ColdDashboardLoads)
	for index := 1; index <= policy.Assumptions.Samples.ColdDashboardLoads; index++ {
		if _, err := c.qualificationContainers.Existing(appContainer).Restart(ctx); err != nil {
			return err
		}
		if err := c.waitQualificationHealthy(ctx, appContainer, "cold performance sample"); err != nil {
			return err
		}
		path := fmt.Sprintf("/evidence/performance-cold-%d.json", index)
		coldPaths = append(coldPaths, path)
		if _, err := c.qualificationContainers.Existing(browserContainer).Exec(
			ctx, nil,
			"env",
			"QUALIFICATION_METRICS_TOKEN="+metricsToken,
			"node", "/work/performance.mjs", "cold", path,
		); err != nil {
			return qualificationContainerOperationError(
				ctx,
				c.qualificationContainers.Existing(browserContainer),
				"capture cold performance sample",
				err,
			)
		}
	}
	coldJSON, _ := json.Marshal(coldPaths)
	if _, err := c.qualificationContainers.Existing(browserContainer).Exec(
		ctx, nil,
		"env",
		"QUALIFICATION_METRICS_TOKEN="+metricsToken,
		"QUALIFICATION_COLD_RESULTS="+string(coldJSON),
		"node", "/work/performance.mjs", "workload", "/evidence/performance-report.json",
	); err != nil {
		return qualificationContainerOperationError(
			ctx,
			c.qualificationContainers.Existing(browserContainer),
			"capture performance workload",
			err,
		)
	}
	diskAfter, err := c.qualificationDiskUsage(
		ctx,
		appContainer,
		"performance disk after",
	)
	if err != nil {
		return err
	}
	serverVersion, err := c.qualificationDocker(
		ctx, nil, "version", "--format", "{{.Server.Version}}",
	)
	if err != nil {
		return err
	}
	cpuOutput, err := c.qualificationDocker(ctx, nil, "info", "--format", "{{.NCPU}}")
	if err != nil {
		return err
	}
	cpus, err := firstQualificationInteger(cpuOutput, "Docker CPUs")
	if err != nil {
		return err
	}
	memoryOutput, err := c.qualificationDocker(ctx, nil, "info", "--format", "{{.MemTotal}}")
	if err != nil {
		return err
	}
	memory, err := firstQualificationInteger(memoryOutput, "Docker memory")
	if err != nil {
		return err
	}
	rowsOutput, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil,
		"wc", "-l", "/app/evaluation/data/orders.csv",
	)
	if err != nil {
		return err
	}
	rows, err := firstQualificationInteger(rowsOutput, "evaluation order rows")
	if err != nil {
		return err
	}
	environmentJSON, _ := json.Marshal(map[string]any{
		"runtime":     "Docker Engine " + strings.TrimSpace(string(serverVersion)),
		"logicalCPUs": cpus,
		"memoryBytes": memory,
		"dataset":     map[string]int64{"orders": rows - 1},
	})
	if err := finalizeQualificationPerformanceReport(
		filepath.Join(evidenceDir, "performance-report.json"),
		policy,
		diskBefore,
		diskAfter,
		environmentJSON,
		imageReference,
		runtime.GOARCH,
		qualificationPerformanceBaseline(),
	); err != nil {
		return err
	}
	for _, path := range coldPaths {
		_ = os.Remove(filepath.Join(evidenceDir, filepath.Base(path)))
	}
	return nil
}

func (c *Controller) qualificationDiskUsage(
	ctx context.Context,
	appContainer string,
	label string,
) (int64, error) {
	output, err := c.qualificationContainers.Existing(appContainer).Exec(
		ctx, nil,
		"du",
		"-sb",
		"--exclude=*.db-wal",
		"--exclude=*.db-shm",
		"/var/lib/leapview",
	)
	if err != nil {
		return 0, err
	}
	return firstQualificationInteger(output, label)
}

func verifyQualificationDenialsAndMetrics(
	ctx context.Context,
	queryBody string,
	metricsToken string,
) error {
	client := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://localhost/api/v1/semantic-models/semantic-model:sales/query",
		strings.NewReader(queryBody),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := qualificationHTTPSClient().Do(request)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("unauthenticated governed query returned %d", response.StatusCode)
	}
	metricsRequest, err := newQualificationLoopbackRequest(
		ctx, http.MethodGet, "http://127.0.0.1:8080/metrics", nil,
	)
	if err != nil {
		return err
	}
	metricsResponse, err := client.Do(metricsRequest)
	if err != nil {
		return err
	}
	_ = metricsResponse.Body.Close()
	if metricsResponse.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("unauthenticated metrics returned %d", metricsResponse.StatusCode)
	}
	metrics, err := readQualificationMetrics(
		ctx, client, "http://127.0.0.1:8080", metricsToken,
	)
	if err != nil {
		return err
	}
	if !bytes.Contains(metrics, []byte("# HELP leapview_http_request_duration_seconds ")) {
		return fmt.Errorf("authenticated metrics omit request duration histogram")
	}
	return nil
}

func firstQualificationInteger(output []byte, label string) (int64, error) {
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return 0, fmt.Errorf("%s is empty", label)
	}
	return parseQualificationInteger(fields[0], label)
}

func containsQualificationString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
