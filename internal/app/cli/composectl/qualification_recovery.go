package composectl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const qualificationRecoveryDiskLimitKiB = int64(51200)
const qualificationRecoveryReleaseInterruptionDelay = 15 * time.Second

const (
	qualificationRecoveryFullCPUs               = "1"
	qualificationRecoveryReleaseCPUs            = "0.03"
	qualificationRecoveryReleaseCandidateKey    = "qualification-recovery-release"
	qualificationRecoveryDeploymentCandidateKey = "qualification-recovery-deployment"
)

type qualificationRecoveryOptions struct {
	BundleRoot           string
	EvidenceDir          string
	PublisherToken       string
	WorkloadToken        string
	ProjectDataToken     string
	RecoveryControlToken string
	MetricsToken         string
	ContainerID          string
	ComposeProject       string
	ProjectID            string
	Image                string
}

type qualificationRecoveryReport struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Result        string                       `json:"result"`
	Stage         string                       `json:"stage"`
	Image         string                       `json:"image"`
	Phases        []qualificationPhaseEvidence `json:"phases"`
	Assertions    struct {
		ManagedUpload        bool `json:"managedUpload"`
		ReleaseFinalization  bool `json:"releaseFinalization"`
		DeploymentActivation bool `json:"deploymentActivation"`
		RefreshRecovery      bool `json:"refreshRecovery"`
		QueryStreamReconnect bool `json:"queryStreamReconnect"`
		BackupInterruption   bool `json:"backupInterruption"`
		RestorePreflight     bool `json:"restorePreflight"`
		BoundedDisk          bool `json:"boundedDisk"`
	} `json:"assertions"`
	BoundedState struct {
		DiskBeforeKiB          int64 `json:"diskBeforeKiB"`
		DiskAfterKiB           int64 `json:"diskAfterKiB"`
		DiskGrowthKiB          int64 `json:"diskGrowthKiB"`
		DiskGrowthLimitKiB     int64 `json:"diskGrowthLimitKiB"`
		StaleRecoveryEntries   int64 `json:"staleRecoveryEntries"`
		StaleRestoreEntries    int64 `json:"staleRestoreEntries"`
		StaleBackupEntries     int64 `json:"staleBackupEntries"`
		StaleCheckpointEntries int64 `json:"staleCheckpointEntries"`
	} `json:"boundedState"`
}

type qualificationRecoveryEvents struct {
	ManagedUpload        json.RawMessage              `json:"managedUpload"`
	ReleaseFinalization  json.RawMessage              `json:"releaseFinalization"`
	DeploymentActivation json.RawMessage              `json:"deploymentActivation"`
	RefreshRecovery      json.RawMessage              `json:"refreshRecovery"`
	Timeline             []qualificationRecoveryEvent `json:"timeline"`
}

type qualificationRecoveryEvent struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
}

type qualificationUploadSessions struct {
	Items []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Files  []struct {
			File struct {
				Size int64 `json:"size"`
			} `json:"file"`
			Negotiation struct {
				TUS struct {
					Offset int64 `json:"offset"`
				} `json:"tus"`
			} `json:"negotiation"`
		} `json:"files"`
	} `json:"items"`
}

type qualificationReleaseList struct {
	Items []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"items"`
}

type qualificationDeploymentList struct {
	Items []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"items"`
}

type qualificationEventList struct {
	Items []struct {
		Event string `json:"event"`
	} `json:"items"`
}

type qualificationRunningCommand struct {
	command *exec.Cmd
	output  *os.File
}

func (c *Controller) runQualificationRecovery(
	ctx context.Context,
	options qualificationRecoveryOptions,
) (report qualificationRecoveryReport, runErr error) {
	rootContext := ctx
	if options.ProjectID == "" {
		options.ProjectID = "project:leapview-evaluation"
	}
	for label, value := range map[string]string{
		"bundle root":            options.BundleRoot,
		"evidence directory":     options.EvidenceDir,
		"publisher token":        options.PublisherToken,
		"workload token":         options.WorkloadToken,
		"project data token":     options.ProjectDataToken,
		"recovery control token": options.RecoveryControlToken,
		"metrics token":          options.MetricsToken,
		"container":              options.ContainerID,
		"Compose project":        options.ComposeProject,
		"project":                options.ProjectID,
		"image":                  options.Image,
	} {
		if strings.TrimSpace(value) == "" {
			return report, fmt.Errorf("recovery qualification %s is required", label)
		}
	}
	appEnv, err := os.ReadFile(filepath.Join(options.BundleRoot, appEnvName))
	if err != nil {
		return report, err
	}
	if !strings.Contains(string(appEnv), "LEAPVIEW_REFRESH_JOB_LEASE_TIMEOUT=15s") {
		return report, fmt.Errorf("recovery qualification requires a 15s refresh job lease")
	}
	workDir, err := os.MkdirTemp(options.BundleRoot, ".qualification-recovery-*")
	if err != nil {
		return report, err
	}
	report.SchemaVersion = qualificationEvidenceSchema
	report.Result = "failure"
	report.Stage = "initialize"
	report.Image = options.Image
	report.BoundedState.DiskGrowthLimitKiB = qualificationRecoveryDiskLimitKiB
	phases := newQualificationPhaseTracker(c.now)
	ctx = phases.Begin(rootContext, report.Stage, 5*time.Minute)
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
		if runErr != nil {
			_ = writeQualificationJSON(
				filepath.Join(options.EvidenceDir, "recovery-report.json"),
				report,
			)
		}
	}()
	cleanup.Add(func(context.Context) error { return os.RemoveAll(workDir) })
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationDocker(
			cleanupCtx,
			nil,
			"update", "--cpus", qualificationRecoveryFullCPUs, options.ContainerID,
		)
		return ignoreQualificationNotFound(err)
	})
	recoveryClient, err := c.startQualificationRecoveryClient(ctx, options, workDir)
	if err != nil {
		return report, err
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationContainers.Existing(recoveryClient).Remove(cleanupCtx)
		return ignoreQualificationNotFound(err)
	})
	client := &http.Client{Timeout: 30 * time.Second}
	apiRoot := "http://127.0.0.1:8080"

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "managed upload interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	var active struct {
		Revision struct {
			ID string `json:"id"`
		} `json:"revision"`
	}
	if err := qualificationAPI(
		ctx, client, http.MethodGet,
		apiRoot+"/api/v1/projects/"+urlPath(options.ProjectID)+"/connections/sample/active-revision",
		options.ProjectDataToken, nil, "", &active,
	); err != nil {
		return report, err
	}
	baselineRevision := active.Revision.ID
	if baselineRevision == "" {
		return report, fmt.Errorf("active managed-data revision is empty")
	}
	if err := c.prepareQualificationRecoveryData(ctx, options, workDir); err != nil {
		return report, err
	}
	if _, err := c.qualificationDocker(
		ctx, nil, "update", "--cpus", "0.25", options.ContainerID,
	); err != nil {
		return report, err
	}
	syncCommand, err := c.startQualificationClientCommand(
		ctx,
		recoveryClient,
		options.PublisherToken,
		filepath.Join(options.EvidenceDir, "recovery-managed-upload.log"),
		"leapview", "data", "sync",
		"--project", "/work/project-a/leapview.yaml",
		"--connection", "sample",
		"--from", "/work/input",
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	var interruptedSession string
	var interruptedOffset int64
	uploadCtx, cancelUpload := qualificationContext(ctx, 10*time.Minute)
	err = qualificationWait(uploadCtx, 500*time.Millisecond, func(waitCtx context.Context) (bool, error) {
		var sessions qualificationUploadSessions
		if apiErr := qualificationAPI(
			waitCtx, client, http.MethodGet,
			apiRoot+"/api/v1/projects/"+urlPath(options.ProjectID)+"/connections/sample/upload-sessions?limit=100",
			options.PublisherToken, nil, "", &sessions,
		); apiErr != nil {
			return false, nil
		}
		for _, session := range sessions.Items {
			if session.Status != "open" {
				continue
			}
			for _, file := range session.Files {
				offset := file.Negotiation.TUS.Offset
				if file.File.Size > 50_000_000 && offset > 0 && offset < file.File.Size {
					interruptedSession = session.ID
					if offset > interruptedOffset {
						interruptedOffset = offset
					}
				}
			}
		}
		return interruptedSession != "", nil
	})
	cancelUpload()
	if err != nil {
		_ = syncCommand.Stop()
		return report, err
	}
	if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
		_ = syncCommand.Stop()
		return report, err
	}
	_ = syncCommand.Stop()
	var sessionObject struct {
		Status string `json:"status"`
		Files  []struct {
			Negotiation struct {
				TUS struct {
					Offset int64 `json:"offset"`
				} `json:"tus"`
			} `json:"negotiation"`
		} `json:"files"`
	}
	if err := qualificationAPI(
		ctx, client, http.MethodGet,
		fmt.Sprintf(
			"%s/api/v1/projects/%s/connections/sample/upload-sessions/%s",
			apiRoot, urlPath(options.ProjectID), urlPath(interruptedSession),
		),
		options.PublisherToken, nil, "", &sessionObject,
	); err != nil {
		return report, err
	}
	maxOffset := int64(0)
	for _, file := range sessionObject.Files {
		if file.Negotiation.TUS.Offset > maxOffset {
			maxOffset = file.Negotiation.TUS.Offset
		}
	}
	if sessionObject.Status != "open" || maxOffset < interruptedOffset {
		return report, fmt.Errorf("managed upload did not preserve its durable offset")
	}
	syncOutput, err := c.runQualificationClientCommand(
		ctx,
		recoveryClient,
		options.PublisherToken,
		"leapview", "data", "sync",
		"--project", "/work/project-a/leapview.yaml",
		"--connection", "sample",
		"--from", "/work/input",
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	faultRevision, err := parseStagedQualificationRevision(string(syncOutput))
	if err != nil || faultRevision == baselineRevision {
		return report, fmt.Errorf("managed upload did not create a distinct staged revision")
	}
	if err := qualificationAPI(
		ctx, client, http.MethodGet,
		apiRoot+"/api/v1/projects/"+urlPath(options.ProjectID)+"/connections/sample/active-revision",
		options.ProjectDataToken, nil, "", &active,
	); err != nil {
		return report, err
	}
	if active.Revision.ID != baselineRevision {
		return report, fmt.Errorf("interrupted upload changed the active revision")
	}
	managedEvents, err := waitForQualificationEvents(
		ctx,
		client,
		apiRoot+fmt.Sprintf(
			"/api/v1/projects/%s/connections/sample/upload-sessions/%s/events?limit=100",
			urlPath(options.ProjectID), urlPath(interruptedSession),
		),
		options.PublisherToken,
		[]string{"upload_session.created", "upload_session.finalizing", "upload_session.completed"},
	)
	if err != nil {
		return report, err
	}
	report.Assertions.ManagedUpload = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "release finalization interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	releaseLog := filepath.Join(options.EvidenceDir, "recovery-release-finalization.log")
	if _, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken,
		"leapview", "dev", "--once", "--no-browser",
		"--project", "/work/project-a/leapview.yaml",
		"--candidate-key", qualificationRecoveryReleaseCandidateKey,
		"--format", "json",
	); err != nil {
		return report, err
	}
	var releasesBefore qualificationReleaseList
	releaseListURL := apiRoot + "/api/v1/projects/" + urlPath(options.ProjectID) + "/releases?limit=100"
	if err := qualificationAPI(
		ctx, client, http.MethodGet, releaseListURL,
		options.PublisherToken, nil, "", &releasesBefore,
	); err != nil {
		return report, err
	}
	existingReleases := make(map[string]bool, len(releasesBefore.Items))
	for _, release := range releasesBefore.Items {
		existingReleases[release.ID] = true
	}
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"update", "--cpus", qualificationRecoveryReleaseCPUs, options.ContainerID,
	); err != nil {
		return report, err
	}
	releaseCommand, err := c.startQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken, releaseLog,
		"leapview", "publish",
		"--project", "/work/project-a/leapview.yaml",
		"--candidate-key", qualificationRecoveryReleaseCandidateKey,
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	if err := sleepContext(ctx, qualificationRecoveryReleaseInterruptionDelay); err != nil {
		_ = releaseCommand.Stop()
		return report, fmt.Errorf("wait for release finalization boundary: %w", err)
	}
	if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
		_ = releaseCommand.Stop()
		return report, err
	}
	_ = releaseCommand.Stop()
	if _, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken,
		"leapview", "publish",
		"--project", "/work/project-a/leapview.yaml",
		"--candidate-key", qualificationRecoveryReleaseCandidateKey,
		"--format", "json",
	); err != nil {
		return report, err
	}
	var releasesAfter qualificationReleaseList
	if err := qualificationAPI(
		ctx, client, http.MethodGet, releaseListURL,
		options.PublisherToken, nil, "", &releasesAfter,
	); err != nil {
		return report, err
	}
	interruptedRelease := ""
	for _, release := range releasesAfter.Items {
		if existingReleases[release.ID] {
			continue
		}
		if interruptedRelease != "" {
			return report, fmt.Errorf("release retry created more than one release")
		}
		interruptedRelease = release.ID
	}
	if interruptedRelease == "" {
		return report, fmt.Errorf("release retry did not retain a release")
	}
	releaseURL := apiRoot + "/api/v1/projects/" + urlPath(options.ProjectID) + "/releases/" + urlPath(interruptedRelease)
	if err := waitForQualificationStatus(
		ctx, client, releaseURL, options.PublisherToken, "ready",
	); err != nil {
		return report, err
	}
	releaseEvents, err := waitForQualificationEvents(
		ctx, client, releaseURL+"/events?limit=100", options.PublisherToken,
		[]string{"release.ready"},
	)
	if err != nil {
		return report, err
	}
	report.Assertions.ReleaseFinalization = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "deployment activation interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	if _, err := c.qualificationDocker(ctx, nil, "update", "--cpus", "0.25", options.ContainerID); err != nil {
		return report, err
	}
	if _, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken,
		"leapview", "dev", "--once", "--no-browser",
		"--project", "/work/project-b/leapview.yaml",
		"--candidate-key", qualificationRecoveryDeploymentCandidateKey,
		"--format", "json",
	); err != nil {
		return report, err
	}
	deploymentListURL := apiRoot + "/api/v1/projects/" + urlPath(options.ProjectID) + "/deployments?limit=100"
	var deploymentsBefore qualificationDeploymentList
	if err := qualificationAPI(
		ctx,
		client,
		http.MethodGet,
		deploymentListURL,
		options.PublisherToken,
		nil,
		"",
		&deploymentsBefore,
	); err != nil {
		return report, err
	}
	existingDeployments := make(map[string]bool, len(deploymentsBefore.Items))
	for _, deployment := range deploymentsBefore.Items {
		existingDeployments[deployment.ID] = true
	}
	deploymentLog := filepath.Join(options.EvidenceDir, "recovery-deployment-activation.log")
	deploymentCommand, err := c.startQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken, deploymentLog,
		"leapview", "publish",
		"--project", "/work/project-b/leapview.yaml",
		"--candidate-key", qualificationRecoveryDeploymentCandidateKey,
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	var interruptedDeployment string
	deploymentCtx, cancelDeployment := qualificationContext(ctx, 10*time.Minute)
	err = qualificationWait(deploymentCtx, 500*time.Millisecond, func(waitCtx context.Context) (bool, error) {
		var deployments qualificationDeploymentList
		if apiErr := qualificationAPI(
			waitCtx, client, http.MethodGet, deploymentListURL,
			options.PublisherToken, nil, "", &deployments,
		); apiErr != nil {
			return false, nil
		}
		for _, deployment := range deployments.Items {
			if existingDeployments[deployment.ID] {
				continue
			}
			if deployment.Status == "queued" || deployment.Status == "running" {
				interruptedDeployment = deployment.ID
				return true, nil
			}
		}
		return false, nil
	})
	cancelDeployment()
	if err != nil {
		_ = deploymentCommand.Stop()
		return report, fmt.Errorf("observe deployment activation boundary: %w", err)
	}
	if err := startQualificationDeploymentActivation(
		ctx,
		client,
		apiRoot,
		options.ProjectID,
		interruptedDeployment,
		options.RecoveryControlToken,
		options.ComposeProject,
	); err != nil {
		_ = deploymentCommand.Stop()
		return report, err
	}
	if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
		_ = deploymentCommand.Stop()
		return report, err
	}
	_ = deploymentCommand.Stop()
	deploymentURL := apiRoot + "/api/v1/projects/" + urlPath(options.ProjectID) +
		"/deployments/" + urlPath(interruptedDeployment)
	if err := waitForQualificationStatus(
		ctx, client, deploymentURL, options.PublisherToken, "active",
	); err != nil {
		return report, err
	}
	deploymentEvents, err := waitForQualificationEvents(
		ctx, client, deploymentURL+"/events?limit=100", options.PublisherToken,
		[]string{"deployment.activation_requested", "deployment.active"},
	)
	if err != nil {
		return report, err
	}
	report.Assertions.DeploymentActivation = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "refresh materialization interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	var refresh struct {
		ID string `json:"id"`
	}
	refreshIDKey := fmt.Sprintf("qualification-refresh-%d", time.Now().Unix())
	if err := qualificationAPI(
		ctx, client, http.MethodPost,
		apiRoot+"/api/v1/projects/"+urlPath(options.ProjectID)+"/refresh-runs",
		options.WorkloadToken,
		map[string]string{"pipelineId": "evaluation-refresh"},
		refreshIDKey,
		&refresh,
	); err != nil {
		return report, err
	}
	refreshURL := apiRoot + "/api/v1/projects/" + urlPath(options.ProjectID) + "/refresh-runs/" + urlPath(refresh.ID)
	if err := waitForQualificationStatus(
		ctx, client, refreshURL, options.WorkloadToken, "running",
	); err != nil {
		return report, err
	}
	if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
		return report, err
	}
	if err := waitForQualificationStatus(
		ctx, client, refreshURL, options.WorkloadToken, "succeeded",
	); err != nil {
		return report, err
	}
	refreshEvents, err := waitForQualificationEvents(
		ctx, client, refreshURL+"/events?limit=100", options.WorkloadToken,
		[]string{"refresh.queued", "refresh.succeeded"},
	)
	if err != nil {
		return report, err
	}
	report.Assertions.RefreshRecovery = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "query and SSE reconnect"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	diskBefore, err := c.qualificationContainerDiskKiB(ctx, options.ContainerID)
	if err != nil {
		return report, err
	}
	report.BoundedState.DiskBeforeKiB = diskBefore
	metricsBefore, err := readQualificationMetrics(ctx, client, apiRoot, options.MetricsToken)
	if err != nil {
		return report, err
	}
	goroutinesBefore, err := qualificationMetricInteger(metricsBefore, "go_goroutines")
	if err != nil {
		return report, err
	}
	queryBody := map[string]any{
		"dimensions": []map[string]string{{"field": "state"}},
		"measures": []map[string]string{
			{"field": "order_count"},
			{"field": "revenue"},
		},
		"limit": 10,
	}
	for cycle := 1; cycle <= 3; cycle++ {
		if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
			return report, err
		}
		var queryResult struct {
			Rows []json.RawMessage `json:"rows"`
		}
		if err := qualificationAPI(
			ctx, client, http.MethodPost,
			apiRoot+"/api/v1/semantic-models/semantic-model:sales/query",
			options.WorkloadToken, queryBody, "", &queryResult,
		); err != nil {
			return report, err
		}
		if len(queryResult.Rows) != 4 {
			return report, fmt.Errorf("recovery query returned %d rows", len(queryResult.Rows))
		}
		if err := observeQualificationSSE(
			ctx,
			client,
			apiRoot+"/updates?route=dashboard&dashboard=dashboard:sales-overview&page=overview",
			options.WorkloadToken,
		); err != nil {
			return report, fmt.Errorf("SSE reconnect cycle %d: %w", cycle, err)
		}
	}
	metricsAfter, err := readQualificationMetrics(ctx, client, apiRoot, options.MetricsToken)
	if err != nil {
		return report, err
	}
	goroutinesAfter, err := qualificationMetricInteger(metricsAfter, "go_goroutines")
	if err != nil {
		return report, err
	}
	if goroutinesAfter > goroutinesBefore+25 {
		return report, fmt.Errorf("goroutines grew from %d to %d", goroutinesBefore, goroutinesAfter)
	}
	report.Assertions.QueryStreamReconnect = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "backup interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	if _, err := c.qualificationDocker(ctx, nil, "update", "--cpus", "0.25", options.ContainerID); err != nil {
		return report, err
	}
	for _, name := range []string{"interrupted.tar.gz", "interrupted.tar.gz.sha256"} {
		_ = os.Remove(filepath.Join(options.BundleRoot, "backups", name))
	}
	backupCommand, err := startQualificationControllerCommand(
		ctx,
		options.BundleRoot,
		filepath.Join(options.EvidenceDir, "recovery-backup-interruption.log"),
		"backup", "interrupted.tar.gz",
	)
	if err != nil {
		return report, err
	}
	backupOneoff, err := c.waitForQualificationComposeOneoff(
		ctx, options.ComposeProject, "admin", "backup",
	)
	if err != nil {
		_ = backupCommand.Stop()
		return report, err
	}
	_ = backupCommand.Stop()
	backupContainer := c.qualificationContainers.Existing(backupOneoff)
	_, _ = backupContainer.Kill(ctx, "KILL")
	_, _ = backupContainer.Remove(ctx)
	if _, statErr := os.Stat(filepath.Join(options.BundleRoot, "backups", "interrupted.tar.gz")); !os.IsNotExist(statErr) {
		return report, fmt.Errorf("interrupted backup produced a completed archive")
	}
	if _, err := c.qualificationDocker(ctx, nil, "update", "--cpus", qualificationRecoveryFullCPUs, options.ContainerID); err != nil {
		return report, err
	}
	recoveryController, err := New(Options{
		Root:      options.BundleRoot,
		DockerBin: c.dockerBin,
		Stdout:    io.Discard,
		Stderr:    c.stderr,
		Now:       c.now,
		Sleep:     c.sleep,
	})
	if err != nil {
		return report, err
	}
	if err := recoveryController.Start(ctx); err != nil {
		return report, err
	}
	if err := recoveryController.Backup(ctx, "recovered.tar.gz"); err != nil {
		return report, err
	}
	options.ContainerID, err = recoveryController.containerID(ctx)
	if err != nil {
		return report, err
	}
	if err := c.waitQualificationHealthy(ctx, options.ContainerID, report.Stage); err != nil {
		return report, err
	}
	temporaryBackups, err := filepath.Glob(filepath.Join(options.BundleRoot, "backups", ".leapview-backup-*.tmp"))
	if err != nil || len(temporaryBackups) != 0 {
		return report, fmt.Errorf("interrupted backup left temporary archives")
	}
	report.Assertions.BackupInterruption = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "restore preflight interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	restoreCommand, err := startQualificationControllerCommand(
		ctx,
		options.BundleRoot,
		filepath.Join(options.EvidenceDir, "recovery-restore-preflight.log"),
		"restore", "backups/recovered.tar.gz",
	)
	if err != nil {
		return report, err
	}
	restoreOneoff, err := c.waitForQualificationComposeOneoff(
		ctx, options.ComposeProject, "admin", "restore",
	)
	if err != nil {
		_ = restoreCommand.Stop()
		return report, err
	}
	_ = restoreCommand.Stop()
	restoreContainer := c.qualificationContainers.Existing(restoreOneoff)
	_, _ = restoreContainer.Kill(ctx, "KILL")
	_, _ = restoreContainer.Remove(ctx)
	if err := recoveryController.Start(ctx); err != nil {
		return report, err
	}
	if err := recoveryController.Restore(ctx, "backups/recovered.tar.gz"); err != nil {
		return report, err
	}
	options.ContainerID, err = recoveryController.containerID(ctx)
	if err != nil {
		return report, err
	}
	if err := c.waitQualificationHealthy(ctx, options.ContainerID, report.Stage); err != nil {
		return report, err
	}
	postRestoreOutput, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.WorkloadToken,
		"leapview", "api", "call", "querySemanticModel",
		"--path", "model=semantic-model:sales", "--body-json", mustQualificationJSON(queryBody),
	)
	if err != nil {
		return report, err
	}
	var postRestore struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(postRestoreOutput, &postRestore); err != nil || len(postRestore.Rows) != 4 {
		return report, fmt.Errorf("post-restore governed query failed")
	}
	report.Assertions.RestorePreflight = true

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "bounded recovery state"
	ctx = phases.Begin(rootContext, report.Stage, 10*time.Minute)
	_, _ = c.qualificationDocker(ctx, nil, "update", "--cpus", qualificationRecoveryFullCPUs, options.ContainerID)
	diskAfter, err := c.qualificationContainerDiskKiB(ctx, options.ContainerID)
	if err != nil {
		return report, err
	}
	report.BoundedState.DiskAfterKiB = diskAfter
	report.BoundedState.DiskGrowthKiB = diskAfter - diskBefore
	report.BoundedState.StaleRestoreEntries, err = c.countQualificationContainerPaths(
		ctx, options.ContainerID, "/var/lib/leapview", ".leapview-restore-*",
	)
	if err != nil {
		return report, err
	}
	report.BoundedState.StaleBackupEntries, err = c.countQualificationContainerPaths(
		ctx, options.ContainerID, "/var/lib/leapview", ".leapview-instance-backup-*",
	)
	if err != nil {
		return report, err
	}
	firstCheckpoints, err := c.countQualificationContainerPaths(
		ctx, options.ContainerID, "/var/lib/leapview", ".leapview-current-backup-*.tar.gz",
	)
	if err != nil {
		return report, err
	}
	secondCheckpoints, err := c.countQualificationContainerPaths(
		ctx, options.ContainerID, "/var/lib/leapview", "leapview-current-backup-*.tar.gz",
	)
	if err != nil {
		return report, err
	}
	report.BoundedState.StaleCheckpointEntries = firstCheckpoints + secondCheckpoints
	report.BoundedState.StaleRecoveryEntries =
		report.BoundedState.StaleRestoreEntries +
			report.BoundedState.StaleBackupEntries +
			report.BoundedState.StaleCheckpointEntries
	if report.BoundedState.DiskGrowthKiB > qualificationRecoveryDiskLimitKiB ||
		report.BoundedState.StaleRecoveryEntries != 0 {
		return report, fmt.Errorf("recovery state is unbounded")
	}
	report.Assertions.BoundedDisk = true

	events := qualificationRecoveryEvents{
		ManagedUpload:        managedEvents,
		ReleaseFinalization:  releaseEvents,
		DeploymentActivation: deploymentEvents,
		RefreshRecovery:      refreshEvents,
		Timeline:             qualificationRecoveryTimeline(),
	}
	if err := writeQualificationJSON(
		filepath.Join(options.EvidenceDir, "recovery-events.json"),
		events,
	); err != nil {
		return report, err
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Phases = phases.Evidence()
	report.Stage = "complete"
	report.Result = "success"
	if err := writeQualificationJSON(
		filepath.Join(options.EvidenceDir, "recovery-report.json"),
		report,
	); err != nil {
		return report, err
	}
	_, err = fmt.Fprintln(c.stdout, "installed-candidate recovery qualification passed")
	return report, err
}

func (c *Controller) prepareQualificationRecoveryData(
	ctx context.Context,
	options qualificationRecoveryOptions,
	workDir string,
) error {
	sourceCSV := filepath.Join(workDir, "orders.csv")
	if _, err := c.qualificationDocker(
		ctx, nil, "cp",
		options.ContainerID+":/app/evaluation/data/orders.csv",
		sourceCSV,
	); err != nil {
		return err
	}
	sourceProject := filepath.Join(workDir, "source-project")
	if _, err := c.qualificationDocker(
		ctx, nil, "cp",
		options.ContainerID+":/app/evaluation/project",
		sourceProject,
	); err != nil {
		return err
	}
	inputDir := filepath.Join(workDir, "input")
	if err := os.MkdirAll(inputDir, 0o700); err != nil {
		return err
	}
	if err := expandQualificationCSV(
		sourceCSV,
		filepath.Join(inputDir, "orders.csv"),
		100000,
	); err != nil {
		return err
	}
	for name, title := range map[string]string{
		"project-a": "Recovery Release Project",
		"project-b": "Recovery Deployment Project",
	} {
		target := filepath.Join(workDir, name)
		if err := copyQualificationTree(sourceProject, target); err != nil {
			return err
		}
		projectPath := filepath.Join(target, "leapview.yaml")
		contents, err := os.ReadFile(projectPath)
		if err != nil {
			return err
		}
		contents = bytes.ReplaceAll(
			contents,
			[]byte("name: leapview-evaluation"),
			[]byte("name: "+title),
		)
		if err := os.WriteFile(projectPath, contents, 0o600); err != nil {
			return err
		}
	}
	candidate := c.qualificationContainers.Existing(options.ContainerID)
	_, _ = candidate.Exec(
		ctx, nil,
		"rm", "-rf", "/var/lib/leapview/qualification-recovery",
	)
	if _, err := candidate.Exec(
		ctx, nil,
		"mkdir", "-p", "/var/lib/leapview/qualification-recovery",
	); err != nil {
		return err
	}
	for _, name := range []string{"input", "project-a", "project-b"} {
		source := filepath.Join(workDir, name)
		if err := makeQualificationContainerReadable(source); err != nil {
			return err
		}
		if _, err := candidate.CopyTo(
			ctx,
			source,
			"/var/lib/leapview/qualification-recovery/"+name,
		); err != nil {
			return err
		}
	}
	return nil
}

func makeQualificationContainerReadable(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("qualification recovery data contains symlink %s", path)
		}
		mode := os.FileMode(0o644)
		if entry.IsDir() {
			mode = 0o755
		} else if !entry.Type().IsRegular() {
			return fmt.Errorf("qualification recovery data contains non-regular file %s", path)
		}
		return os.Chmod(path, mode)
	})
}

func expandQualificationCSV(source, destination string, iterations int) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	reader := csv.NewReader(bufio.NewReader(input))
	rows, err := reader.ReadAll()
	if err != nil {
		return err
	}
	if len(rows) < 2 {
		return fmt.Errorf("qualification CSV has no data rows")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriterSize(output, 1<<20)
	writer := csv.NewWriter(buffered)
	if err := writer.Write(rows[0]); err != nil {
		_ = output.Close()
		return err
	}
	for _, row := range rows[1:] {
		original := row[0]
		for iteration := 1; iteration <= iterations; iteration++ {
			row[0] = original + "-" + strconv.Itoa(iteration)
			if err := writer.Write(row); err != nil {
				_ = output.Close()
				return err
			}
		}
	}
	writer.Flush()
	writeErr := writer.Error()
	flushErr := buffered.Flush()
	closeErr := output.Close()
	if writeErr != nil {
		return writeErr
	}
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (c *Controller) startQualificationRecoveryClient(
	ctx context.Context,
	options qualificationRecoveryOptions,
	workDir string,
) (string, error) {
	caddyOutput, err := c.qualificationCompose(
		ctx,
		options.BundleRoot,
		"ps", "--quiet", "caddy",
	)
	if err != nil {
		return "", err
	}
	caddyContainer := strings.TrimSpace(string(caddyOutput))
	if caddyContainer == "" {
		return "", fmt.Errorf("recovery qualification Caddy container is not running")
	}
	certificateFile := filepath.Join(workDir, "caddy-root.crt")
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"cp",
		caddyContainer+":/data/caddy/pki/authorities/local/root.crt",
		certificateFile,
	); err != nil {
		return "", err
	}
	if err := os.Chmod(certificateFile, 0o644); err != nil {
		return "", err
	}
	clientHome := filepath.Join(workDir, "client-home")
	if err := os.MkdirAll(clientHome, 0o777); err != nil {
		return "", err
	}
	if err := os.Chmod(clientHome, 0o777); err != nil {
		return "", err
	}
	container := normalizedQualificationName(
		options.ComposeProject + "-recovery-client",
	)
	if _, err := c.qualificationContainers.Start(ctx, qualificationContainerRequest{
		Name:        container,
		Image:       options.Image,
		NetworkMode: "host",
		NoHealth:    true,
		Volumes: []qualificationContainerVolume{
			{Source: workDir, Target: "/work", ReadOnly: true},
			{Source: clientHome, Target: "/client-home"},
			{Source: certificateFile, Target: "/run/certs/caddy-root.crt", ReadOnly: true},
		},
		Environment: map[string]string{
			"SSL_CERT_FILE": "/run/certs/caddy-root.crt",
		},
		Entrypoint: []string{"sleep"},
		Command:    []string{"infinity"},
	}); err != nil {
		return "", err
	}
	return container, nil
}

func qualificationClientExecArguments(
	container string,
	token string,
	arguments ...string,
) []string {
	result := []string{
		"exec",
		"--env", "LEAPVIEW_API_TOKEN=" + token,
		"--env", "LEAPVIEW_TARGET=https://localhost",
		"--env", "LEAPVIEW_HOME=/client-home",
		container,
	}
	return append(result, arguments...)
}

func (c *Controller) startQualificationClientCommand(
	ctx context.Context,
	clientContainer string,
	token string,
	logPath string,
	arguments ...string,
) (*qualificationRunningCommand, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	output, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	dockerArguments := qualificationClientExecArguments(
		clientContainer,
		token,
		arguments...,
	)
	command := exec.CommandContext(ctx, c.dockerBin, dockerArguments...)
	command.Dir = c.root
	command.Env = os.Environ()
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		_ = output.Close()
		return nil, err
	}
	return &qualificationRunningCommand{command: command, output: output}, nil
}

func (c *Controller) runQualificationClientCommand(
	ctx context.Context,
	clientContainer string,
	token string,
	arguments ...string,
) ([]byte, error) {
	return c.qualificationContainers.Existing(clientContainer).Exec(
		ctx, nil,
		append(
			[]string{
				"env",
				"LEAPVIEW_API_TOKEN=" + token,
				"LEAPVIEW_TARGET=https://localhost",
				"LEAPVIEW_HOME=/client-home",
			},
			arguments...,
		)...,
	)
}

func (r *qualificationRunningCommand) Stop() error {
	if r == nil {
		return nil
	}
	if r.command.Process != nil {
		_ = r.command.Process.Kill()
	}
	waitErr := r.command.Wait()
	closeErr := r.output.Close()
	if waitErr != nil && !strings.Contains(waitErr.Error(), "signal: killed") {
		return errorsJoin(waitErr, closeErr)
	}
	return closeErr
}

func startQualificationControllerCommand(
	ctx context.Context,
	bundleRoot string,
	logPath string,
	arguments ...string,
) (*qualificationRunningCommand, error) {
	output, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(
		ctx,
		filepath.Join(bundleRoot, "leapviewctl"),
		arguments...,
	)
	command.Dir = bundleRoot
	command.Env = os.Environ()
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		_ = output.Close()
		return nil, err
	}
	return &qualificationRunningCommand{command: command, output: output}, nil
}

func (c *Controller) killAndRecoverQualificationCandidate(
	ctx context.Context,
	containerID string,
	stage string,
) error {
	container := c.qualificationContainers.Existing(containerID)
	if _, err := container.Kill(ctx, "KILL"); err != nil {
		return err
	}
	if _, err := container.Start(ctx); err != nil {
		return err
	}
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"update", "--cpus", qualificationRecoveryFullCPUs, containerID,
	); err != nil {
		return err
	}
	return c.waitQualificationHealthy(ctx, containerID, stage)
}

func (c *Controller) waitQualificationHealthy(
	ctx context.Context,
	containerID string,
	stage string,
) error {
	healthCtx, cancel := qualificationContext(ctx, 3*time.Minute)
	defer cancel()
	err := qualificationWait(healthCtx, time.Second, func(waitCtx context.Context) (bool, error) {
		output, inspectErr := c.qualificationContainers.Existing(containerID).
			Inspect(waitCtx, "{{.State.Health.Status}}")
		if inspectErr != nil {
			return false, nil
		}
		return strings.TrimSpace(string(output)) == "healthy", nil
	})
	if err != nil {
		return qualificationContainerOperationError(
			ctx,
			c.qualificationContainers.Existing(containerID),
			"recover candidate health after "+stage,
			err,
		)
	}
	return nil
}

func waitForQualificationStatus(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	token string,
	expected string,
) error {
	waitCtx, cancel := qualificationContext(ctx, 10*time.Minute)
	defer cancel()
	return qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		var response struct {
			Status string `json:"status"`
		}
		if err := qualificationAPI(
			requestCtx, client, http.MethodGet, endpoint, token, nil, "", &response,
		); err != nil {
			return false, nil
		}
		return response.Status == expected, nil
	})
}

func startQualificationDeploymentActivation(
	ctx context.Context,
	client *http.Client,
	apiRoot string,
	projectID string,
	deploymentID string,
	token string,
	idempotencySuffix string,
) error {
	deployments := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		apiRoot,
		token,
		client,
	))
	deployment, err := deployments.GetDeployment(
		ctx,
		deploymentgen.GenGetDeploymentClientRequest{
			Project: projectID, Deployment: deploymentID,
		},
	)
	if err != nil {
		return err
	}
	if deployment.Body.Approval == nil ||
		deployment.Body.Approval.Status != "pending" {
		return fmt.Errorf("recovery deployment approval is not pending")
	}
	approval, err := deployments.ApproveDeployment(
		ctx,
		deploymentgen.GenApproveDeploymentClientRequest{
			Project: projectID, Deployment: deploymentID,
			Approval: deployment.Body.Approval.Id,
			Headers: deploymentgen.GenApproveDeploymentClientHeaders{
				IdempotencyKey: "recovery-approve-" + idempotencySuffix,
			},
			Body: deploymentgen.GenSchemaDeploymentApprovalDecisionRequest{
				ExpectedRevision: deployment.Body.Approval.Revision,
			},
		},
	)
	if err != nil {
		return mapQualificationApproveDeploymentFailure(err)
	}
	if approval.Body.Status != "approved" {
		return fmt.Errorf(
			"recovery deployment approval transitioned to %q",
			approval.Body.Status,
		)
	}
	_, err = deployments.ActivateDeployment(
		ctx,
		deploymentgen.GenActivateDeploymentClientRequest{
			Project: projectID, Deployment: deploymentID,
			Headers: deploymentgen.GenActivateDeploymentClientHeaders{
				IdempotencyKey: "recovery-activate-" + idempotencySuffix,
			},
		},
	)
	if err != nil {
		return mapQualificationActivateDeploymentFailure(err)
	}
	return nil
}

func waitForQualificationEvents(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	token string,
	required []string,
) (json.RawMessage, error) {
	waitCtx, cancel := qualificationContext(ctx, 10*time.Minute)
	defer cancel()
	var raw json.RawMessage
	err := qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		var events qualificationEventList
		var buffer json.RawMessage
		if err := qualificationAPI(
			requestCtx, client, http.MethodGet, endpoint, token, nil, "", &buffer,
		); err != nil {
			return false, nil
		}
		if err := json.Unmarshal(buffer, &events); err != nil {
			return false, err
		}
		observed := make(map[string]bool, len(events.Items))
		for _, event := range events.Items {
			observed[event.Event] = true
		}
		for _, event := range required {
			if !observed[event] {
				return false, nil
			}
		}
		raw = append(raw[:0], buffer...)
		return true, nil
	})
	return raw, err
}

func readQualificationMetrics(
	ctx context.Context,
	client *http.Client,
	apiRoot string,
	token string,
) ([]byte, error) {
	request, err := newQualificationLoopbackRequest(
		ctx,
		http.MethodGet,
		apiRoot+"/metrics",
		nil,
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics returned %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

func qualificationMetricInteger(metrics []byte, name string) (int64, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(metrics))
	if err != nil {
		return 0, fmt.Errorf("parse Prometheus metrics: %w", err)
	}
	family, ok := families[name]
	if !ok {
		return 0, fmt.Errorf("metrics omit %s", name)
	}
	if len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 {
		return 0, fmt.Errorf("metric %s must contain one unlabelled sample", name)
	}
	metric := family.Metric[0]
	var value float64
	switch {
	case metric.Gauge != nil:
		value = metric.Gauge.GetValue()
	case metric.Counter != nil:
		value = metric.Counter.GetValue()
	case metric.Untyped != nil:
		value = metric.Untyped.GetValue()
	default:
		return 0, fmt.Errorf("metric %s is not a scalar", name)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, fmt.Errorf("metric %s value %v is not an integer", name, value)
	}
	return parseQualificationInteger(strconv.FormatFloat(value, 'f', -1, 64), name)
}

func observeQualificationSSE(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	token string,
) error {
	sseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := newQualificationLoopbackRequest(
		sseCtx,
		http.MethodGet,
		endpoint,
		nil,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if scanner.Text() == "event: datastar-patch-signals" {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("SSE stream ended before a signal patch")
}

func (c *Controller) qualificationContainerDiskKiB(
	ctx context.Context,
	containerID string,
) (int64, error) {
	output, err := c.qualificationContainers.Existing(containerID).
		Exec(ctx, nil, "du", "-sk", "/var/lib/leapview")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return 0, fmt.Errorf("container disk usage is empty")
	}
	return parseQualificationInteger(fields[0], "container disk usage")
}

func (c *Controller) countQualificationContainerPaths(
	ctx context.Context,
	containerID string,
	root string,
	pattern string,
) (int64, error) {
	output, err := c.qualificationContainers.Existing(containerID).Exec(
		ctx, nil,
		"find", root, "-maxdepth", "1", "-name", pattern, "-print",
	)
	if err != nil {
		return 0, err
	}
	count := int64(0)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

func (c *Controller) waitForQualificationComposeOneoff(
	ctx context.Context,
	project string,
	commandParts ...string,
) (string, error) {
	waitCtx, cancel := qualificationContext(ctx, time.Minute)
	defer cancel()
	var found string
	err := qualificationWait(waitCtx, 50*time.Millisecond, func(requestCtx context.Context) (bool, error) {
		output, listErr := c.qualificationDocker(
			requestCtx, nil,
			"ps",
			"--filter", "label=com.docker.compose.project="+project,
			"--filter", "label=com.docker.compose.oneoff=True",
			"--quiet",
		)
		if listErr != nil {
			return false, nil
		}
		for _, candidate := range strings.Fields(string(output)) {
			commandOutput, inspectErr := c.qualificationContainers.Existing(candidate).
				Inspect(requestCtx, "{{json .Config.Cmd}}")
			if inspectErr != nil {
				continue
			}
			var command []string
			if err := json.Unmarshal(bytes.TrimSpace(commandOutput), &command); err != nil {
				continue
			}
			joined := strings.Join(command, "\x00")
			match := true
			for _, part := range commandParts {
				if !strings.Contains(joined, part) {
					match = false
				}
			}
			if match {
				found = candidate
				return true, nil
			}
		}
		return false, nil
	})
	return found, err
}

func expandQualificationEventTimeline(operation string) []qualificationRecoveryEvent {
	return []qualificationRecoveryEvent{
		{Operation: operation, Status: "attempted"},
		{Operation: operation, Status: "interrupted"},
		{Operation: operation, Status: "resumed"},
		{Operation: operation, Status: "completed"},
	}
}

func qualificationRecoveryTimeline() []qualificationRecoveryEvent {
	var result []qualificationRecoveryEvent
	for _, operation := range []string{
		"managedUpload",
		"releaseFinalization",
		"deploymentActivation",
		"refreshRecovery",
	} {
		result = append(result, expandQualificationEventTimeline(operation)...)
	}
	for _, operation := range []string{"backupInterruption", "restorePreflight"} {
		result = append(result,
			qualificationRecoveryEvent{Operation: operation, Status: "interrupted"},
			qualificationRecoveryEvent{Operation: operation, Status: "completed"},
		)
	}
	return result
}

func mustQualificationJSON(value any) string {
	contents, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(contents)
}

func urlPath(value string) string {
	return url.PathEscape(value)
}

func errorsJoin(values ...error) error {
	var result error
	for _, value := range values {
		if value != nil {
			if result == nil {
				result = value
			} else {
				result = fmt.Errorf("%v; %w", result, value)
			}
		}
	}
	return result
}
