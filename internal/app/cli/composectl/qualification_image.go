package composectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const qualificationRegistryImage = "registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"

var qualificationPushedDigestPattern = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`)
var qualificationImmutableImagePattern = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)

func qualificationLoopbackPort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate qualification loopback port: %w", err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("release qualification loopback port: %w", err)
	}
	return port, nil
}

func retryQualificationRegistryPush(
	ctx context.Context,
	attempts int,
	delay time.Duration,
	push func() ([]byte, error),
) ([]byte, error) {
	if attempts < 1 {
		return nil, fmt.Errorf("qualification registry push requires at least one attempt")
	}
	var output []byte
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		output, err = push()
		if err == nil {
			return output, nil
		}
		if attempt == attempts {
			break
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return output, ctx.Err()
		case <-timer.C:
		}
	}
	return output, fmt.Errorf("push qualification image after %d attempts: %w", attempts, err)
}

type qualificationImageReport struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Result        string                       `json:"result"`
	Image         string                       `json:"image"`
	Phases        []qualificationPhaseEvidence `json:"phases"`
}

func (c *Controller) QualifyImage(
	ctx context.Context,
	options QualificationImageOptions,
) (runErr error) {
	rootContext := ctx
	options.Image = strings.TrimSpace(options.Image)
	if options.Image == "" {
		options.Image = "leapview:ci"
	}
	if options.RequireImmutable && !qualificationImmutableImagePattern.MatchString(options.Image) {
		return errors.New("production qualification requires an immutable repository@sha256 digest")
	}
	if _, err := c.qualifyProductionImageRuntime(ctx, options.Image); err != nil {
		return err
	}
	if options.EvidenceDir == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return err
		}
		options.EvidenceDir = filepath.Join(
			workingDirectory,
			"qualification-evidence",
			"authoring-ci",
		)
	}
	evidenceDir, err := filepath.Abs(options.EvidenceDir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(evidenceDir); err != nil {
		return err
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		return err
	}
	report := qualificationImageReport{
		SchemaVersion: qualificationEvidenceSchema,
		Result:        "failure",
		Image:         options.Image,
	}
	phases := newQualificationPhaseTracker(c.now)
	ctx = phases.Begin(rootContext, "image bundle", 20*time.Minute)
	bundleRoot, err := os.MkdirTemp("", "leapview-authoring-image-*")
	if err != nil {
		return err
	}
	registryContainer := normalizedQualificationName(
		"leapview-authoring-registry-" + strconv.Itoa(os.Getpid()),
	)
	composeProject := normalizedQualificationName(
		fmt.Sprintf(
			"leapview-authoring-ci-%s-%d",
			os.Getenv("GITHUB_RUN_ID"),
			os.Getpid(),
		),
	)
	cleanup := qualificationCleanup{}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		runErr = joinQualificationError(runErr, cleanup.Run(cleanupCtx))
		runErr = phases.Finish(runErr)
		if runErr != nil {
			report.Result = "failure"
		}
		report.Phases = phases.Evidence()
		_ = writeQualificationJSON(
			filepath.Join(evidenceDir, "image-qualification-report.json"),
			report,
		)
		if runErr != nil {
			_, _ = fmt.Fprintf(
				c.stderr,
				"enterprise authoring image qualification failed; evidence: %s\n",
				evidenceDir,
			)
		}
	}()
	cleanup.Add(func(context.Context) error { return os.RemoveAll(bundleRoot) })

	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"run", "--detach",
		"--name", registryContainer,
		"--publish", "127.0.0.1::5000",
		qualificationRegistryImage,
	); err != nil {
		return fmt.Errorf("start qualification registry: %w", err)
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationDocker(cleanupCtx, nil, "rm", "--force", registryContainer)
		return ignoreQualificationNotFound(err)
	})
	portOutput, err := c.qualificationDocker(
		ctx,
		nil,
		"inspect",
		"--format", `{{(index (index .NetworkSettings.Ports "5000/tcp") 0).HostPort}}`,
		registryContainer,
	)
	if err != nil {
		return err
	}
	registryPort := strings.TrimSpace(string(portOutput))
	if _, err := strconv.Atoi(registryPort); err != nil {
		return fmt.Errorf("invalid qualification registry port %q", registryPort)
	}
	registryTag := "127.0.0.1:" + registryPort + "/leapview:authoring-ci"
	if _, err := c.qualificationDocker(ctx, nil, "tag", options.Image, registryTag); err != nil {
		return err
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationDocker(cleanupCtx, nil, "image", "rm", "--force", registryTag)
		return ignoreQualificationNotFound(err)
	})
	pushOutput, err := retryQualificationRegistryPush(
		ctx,
		3,
		500*time.Millisecond,
		func() ([]byte, error) {
			return c.qualificationDocker(ctx, nil, "push", registryTag)
		},
	)
	if err != nil {
		return err
	}
	digestMatch := qualificationPushedDigestPattern.FindSubmatch(pushOutput)
	if len(digestMatch) != 2 {
		return fmt.Errorf("Docker push did not return an immutable digest")
	}
	imageReference := "127.0.0.1:" + registryPort + "/leapview@" + string(digestMatch[1])
	if _, err := c.qualificationDocker(ctx, nil, "pull", imageReference); err != nil {
		return err
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationDocker(cleanupCtx, nil, "image", "rm", "--force", imageReference)
		return ignoreQualificationNotFound(err)
	})

	for _, name := range []string{
		"Caddyfile",
		"compose.yaml",
		"compose.https.yaml",
		"deployment.env.example",
		"leapview.env.example",
	} {
		if err := copyQualificationFile(
			filepath.Join(c.root, name),
			filepath.Join(bundleRoot, name),
			0o644,
		); err != nil {
			return err
		}
	}
	if err := copyQualificationTree(
		filepath.Join(c.root, "qualification"),
		filepath.Join(bundleRoot, "qualification"),
	); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := copyQualificationFile(
		executable,
		filepath.Join(bundleRoot, "leapviewctl"),
		0o755,
	); err != nil {
		return err
	}
	if err := copyQualificationFile(
		filepath.Join(bundleRoot, "deployment.env.example"),
		filepath.Join(bundleRoot, deploymentEnvName),
		0o600,
	); err != nil {
		return err
	}
	httpPort, err := qualificationLoopbackPort()
	if err != nil {
		return err
	}
	httpsPort, err := qualificationLoopbackPort()
	if err != nil {
		return err
	}
	if err := updateEnvFile(filepath.Join(bundleRoot, deploymentEnvName), map[string]string{
		"COMPOSE_PROJECT_NAME": composeProject,
		"LEAPVIEW_IMAGE":       imageReference,
		"CADDY_DOMAIN":         "localhost",
		"CADDY_HTTP_BIND":      "127.0.0.1:" + httpPort,
		"CADDY_HTTPS_BIND":     "127.0.0.1:" + httpsPort,
		"CADDY_HTTPS_UDP_BIND": "127.0.0.1:" + httpsPort,
	}); err != nil {
		return err
	}

	var controllerOutput bytes.Buffer
	instanceController, err := New(Options{
		Root:                  bundleRoot,
		DockerBin:             c.dockerBin,
		Stdin:                 c.stdin,
		Stdout:                &controllerOutput,
		Stderr:                c.stderr,
		Now:                   c.now,
		Sleep:                 c.sleep,
		qualificationExecutor: c.qualificationExecutor,
	})
	if err != nil {
		return err
	}
	instanceStarted := false
	cleanup.Add(func(cleanupCtx context.Context) error {
		if !instanceStarted {
			return nil
		}
		logOutput, _ := c.qualificationCompose(
			cleanupCtx,
			bundleRoot,
			"logs", "--no-color", "--tail", "500",
		)
		_ = os.WriteFile(
			filepath.Join(evidenceDir, "compose.log"),
			redactQualificationLog(logOutput, 500),
			0o600,
		)
		_, downErr := c.qualificationCompose(
			cleanupCtx,
			bundleRoot,
			"down", "--volumes", "--remove-orphans",
		)
		return ignoreQualificationNotFound(downErr)
	})
	if err := phases.Finish(nil); err != nil {
		return err
	}
	ctx = phases.Begin(rootContext, "target bootstrap", 20*time.Minute)
	if err := instanceController.Initialize(ctx, InitOptions{
		AdminEmail:  "admin@localhost",
		Domain:      "localhost",
		Environment: "evaluation",
		Image:       imageReference,
	}); err != nil {
		return err
	}
	target := "https://localhost:" + httpsPort
	if err := updateEnvFile(filepath.Join(bundleRoot, appEnvName), map[string]string{
		"LEAPVIEW_PUBLIC_URL": target,
	}); err != nil {
		return err
	}
	instanceStarted = true
	if err := instanceController.bootstrapQualificationLocalPhysicalPool(ctx); err != nil {
		return err
	}
	if err := instanceController.startQualificationBootstrap(ctx); err != nil {
		return err
	}
	credentialsPath := filepath.Join(bundleRoot, ".qualification-credentials.json")
	var credentialsOutput bytes.Buffer
	instanceController.stdout = &credentialsOutput
	if err := instanceController.FirstLogin(); err != nil {
		return err
	}
	if err := os.WriteFile(credentialsPath, credentialsOutput.Bytes(), 0o600); err != nil {
		return err
	}
	var credentials qualificationCredentials
	if err := json.Unmarshal(credentialsOutput.Bytes(), &credentials); err != nil {
		return fmt.Errorf("decode initial credentials: %w", err)
	}
	credentials.QualificationPassword, err = randomHex(24)
	if err != nil {
		return err
	}
	if err := writeQualificationJSON(credentialsPath, credentials); err != nil {
		return err
	}

	containerID, err := instanceController.containerID(ctx)
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

	authoringReport, err := c.runQualificationAuthoring(ctx, qualificationAuthoringOptions{
		BundleRoot:      bundleRoot,
		Image:           imageReference,
		ClientBaseImage: options.Image,
		CredentialsFile: credentialsPath,
		ComposeProject:  composeProject,
		EvidenceDir:     evidenceDir,
		SourceRevision:  sourceRevision,
		Target:          target,
	})
	if err != nil {
		return err
	}
	if err := instanceController.waitQualificationReadiness(ctx); err != nil {
		return fmt.Errorf("production image did not become ready after sealed publication: %w", err)
	}
	if authoringReport.Result != "success" ||
		!authoringReport.Assertions.BrowserApprovedLogin ||
		!authoringReport.Assertions.NativeKeyring ||
		!authoringReport.Assertions.PrivatePreview ||
		!authoringReport.Assertions.ExactCandidateActivated {
		return fmt.Errorf("enterprise authoring report is incomplete")
	}
	if err := phases.Finish(nil); err != nil {
		return err
	}
	report.Result = "success"
	report.Phases = phases.Evidence()
	_, err = fmt.Fprintln(c.stdout, "production image passed enterprise authoring qualification")
	return err
}

func parseStagedQualificationRevision(output string) (string, error) {
	var result struct {
		SchemaVersion int    `json:"schemaVersion"`
		RevisionID    string `json:"revisionId"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", fmt.Errorf("decode managed data sync result: %w", err)
	}
	if result.SchemaVersion != 1 {
		return "", fmt.Errorf(
			"unsupported managed data sync result schema %d",
			result.SchemaVersion,
		)
	}
	if !strings.HasPrefix(result.RevisionID, "sha256:") ||
		len(result.RevisionID) != 71 {
		return "", fmt.Errorf("managed data sync returned an invalid revision")
	}
	return result.RevisionID, nil
}

func ignoreQualificationNotFound(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "no such container") ||
		strings.Contains(lower, "no such image") ||
		strings.Contains(lower, "not found") {
		return nil
	}
	return err
}

func qualificationStartedAt(now time.Time) string {
	return now.UTC().Format(time.RFC3339)
}
