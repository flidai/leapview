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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/qualificationbarrier"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const qualificationRecoveryDiskLimitKiB = int64(51200)
const qualificationRecoveryReleaseInterruptionDelay = 15 * time.Second
const qualificationRecoveryActivationBarrierTimeout = 2 * time.Minute
const qualificationPublicationRetryAttempts = 8
const qualificationPublicationRetryInitialBackoff = 250 * time.Millisecond
const qualificationPublicationRetryMaxBackoff = 4 * time.Second
const qualificationDeliveryInputUnavailableMarker = "(DELIVERY_INPUT_UNAVAILABLE):"
const qualificationGCStabilityTimeout = 3 * time.Minute
const qualificationGCStabilityPollInterval = 500 * time.Millisecond
const qualificationGCQueryFenceMarker = "sealed catalog query lease acquisition failed: delivery transition conflict: GC lease excludes query root"
const qualificationGCLeaseActiveReason = "gc_lease_active"
const qualificationGCSnapshotUnavailableMarker = "Delivery status is temporarily unavailable"

const (
	qualificationRecoveryFullCPUs               = "1"
	qualificationRecoveryInterruptedWorkCPUs    = "0.03"
	qualificationRecoveryReleaseCandidateKey    = "qualification-recovery-release"
	qualificationRecoveryDeploymentCandidateKey = "qualification-recovery-deployment"
	qualificationManagedConnectionID            = "connection:sample"
	qualificationRefreshPipelineID              = "pipeline:evaluation-refresh"
	qualificationRecoveryReleaseProjectName     = "recovery-release-project"
	qualificationRecoveryDeploymentProjectName  = "recovery-deployment-project"
	qualificationRecoveryClientInput            = "/work/qualification-recovery/input"
	qualificationRecoveryClientProjectA         = "/work/qualification-recovery/project-a/leapview.yaml"
	qualificationRecoveryClientProjectB         = "/work/qualification-recovery/project-b/leapview.yaml"
)

type qualificationRecoveryOptions struct {
	BundleRoot           string
	EvidenceDir          string
	PublisherToken       string
	WorkloadToken        string
	ProjectDataToken     string
	RecoveryControlToken string
	MetricsToken         string
	OperatorToken        string
	ContainerID          string
	ComposeProject       string
	ProjectID            string
	Image                string
	ClientImage          string
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

type qualificationEventList struct {
	Items []struct {
		Event string `json:"event"`
	} `json:"items"`
}

type qualificationRunningCommand struct {
	command *exec.Cmd
	output  *os.File
	done    chan struct{}
	mu      sync.Mutex
	waitErr error
	close   sync.Once
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
		"operator token":         options.OperatorToken,
		"container":              options.ContainerID,
		"Compose project":        options.ComposeProject,
		"project":                options.ProjectID,
		"image":                  options.Image,
		"client image":           options.ClientImage,
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
	// The hardened recovery client bind-mounts this project directory and runs as the
	// non-root LeapView user. MkdirTemp creates 0700 directories, so the mount
	// root must be made traversable before the client starts; individual files
	// remain explicitly read-only below.
	if err := makeQualificationProjectDirTraversable(workDir); err != nil {
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
	cleanup.Add(func(cleanupCtx context.Context) error {
		return ignoreQualificationNotFound(c.clearQualificationActivationBarrier(cleanupCtx, options.ContainerID))
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
		apiRoot+qualificationManagedConnectionPath(options.ProjectID)+"/active-revision",
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
		"--project", qualificationRecoveryClientProjectA,
		"--connection", "sample",
		"--from", qualificationRecoveryClientInput,
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
			apiRoot+qualificationManagedConnectionPath(options.ProjectID)+"/upload-sessions?limit=100",
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
			"%s%s/upload-sessions/%s",
			apiRoot, qualificationManagedConnectionPath(options.ProjectID), urlPath(interruptedSession),
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
		"--project", qualificationRecoveryClientProjectA,
		"--connection", "sample",
		"--from", qualificationRecoveryClientInput,
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
		apiRoot+qualificationManagedConnectionPath(options.ProjectID)+"/active-revision",
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
			"%s/upload-sessions/%s/events?limit=100",
			qualificationManagedConnectionPath(options.ProjectID), urlPath(interruptedSession),
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
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"update", "--cpus", qualificationRecoveryInterruptedWorkCPUs, options.ContainerID,
	); err != nil {
		return report, err
	}
	releaseCommand, err := c.startQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken, releaseLog,
		"leapview", "dev", "--once", "--no-browser",
		"--project", qualificationRecoveryClientProjectA,
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
	if !releaseCommand.Running() {
		_ = releaseCommand.Stop()
		return report, fmt.Errorf("release finalization command completed before interruption boundary")
	}
	if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
		_ = releaseCommand.Stop()
		return report, err
	}
	_ = releaseCommand.Stop()
	releaseOutput, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken,
		"leapview", "dev", "--once", "--no-browser",
		"--project", qualificationRecoveryClientProjectA,
		"--candidate-key", qualificationRecoveryReleaseCandidateKey,
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	releaseCandidate, err := parseQualificationCandidate(string(releaseOutput), "")
	if err != nil {
		return report, err
	}
	releaseEvidence, err := waitForQualificationCandidateEvidence(
		ctx, client, apiRoot, options.ProjectID, options.PublisherToken,
		releaseCandidate,
	)
	if err != nil {
		return report, err
	}
	releaseEvents, err := json.Marshal(releaseEvidence)
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
	deploymentCandidateOutput, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken,
		"leapview", "dev", "--once", "--no-browser",
		"--project", qualificationRecoveryClientProjectB,
		"--candidate-key", qualificationRecoveryDeploymentCandidateKey,
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	deploymentCandidate, err := parseQualificationCandidate(string(deploymentCandidateOutput), "")
	if err != nil {
		return report, err
	}
	pendingOutput, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken,
		"leapview", "publish", deploymentCandidate.ID,
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	pendingPublication, err := parseQualificationPublication(string(pendingOutput), deploymentCandidate)
	if err != nil {
		return report, err
	}
	if pendingPublication.Status != "pending" {
		return report, fmt.Errorf("recovery publication status %q is not pending", pendingPublication.Status)
	}
	if err := approveQualificationPublication(
		ctx,
		client,
		qualificationAuthoringOptions{Target: apiRoot, ProjectID: options.ProjectID},
		options.PublisherToken,
		options.RecoveryControlToken,
		pendingPublication,
		options.ComposeProject+"-recovery",
	); err != nil {
		return report, err
	}
	if err := c.armQualificationActivationBarrier(ctx, options.ContainerID, workDir); err != nil {
		return report, err
	}
	deploymentLog := filepath.Join(options.EvidenceDir, "recovery-deployment-activation.log")
	deploymentCommand, err := c.startQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken, deploymentLog,
		"leapview", "publish", deploymentCandidate.ID,
		"--format", "json",
	)
	if err != nil {
		return report, err
	}
	barrierCtx, cancelBarrier := qualificationContext(ctx, qualificationRecoveryActivationBarrierTimeout)
	err = c.waitForQualificationActivationBarrier(barrierCtx, options.ContainerID, workDir)
	cancelBarrier()
	if err != nil {
		_ = deploymentCommand.Stop()
		return report, fmt.Errorf("wait for canonical publication activation barrier: %w", err)
	}
	if err := c.killAndRecoverQualificationCandidate(
		ctx,
		options.ContainerID,
		report.Stage,
		func() { _ = deploymentCommand.Stop() },
	); err != nil {
		_ = deploymentCommand.Stop()
		return report, err
	}
	recoveredEvidence, err := qualificationPublicationEvidence(
		ctx, client, apiRoot, options.ProjectID, options.PublisherToken,
		deploymentCandidate, pendingPublication,
	)
	if err != nil {
		return report, err
	}
	if recoveredEvidence.Status != deploymentgen.DeliveryPublicationStatusPending {
		return report, fmt.Errorf(
			"canonical publication reached %q instead of preserving the interrupted pending request",
			recoveredEvidence.Status,
		)
	}
	committedOutput, err := c.runQualificationPublicationRetry(
		ctx,
		recoveryClient,
		options.PublisherToken,
		deploymentCandidate.ID,
	)
	if err != nil {
		return report, err
	}
	committedPublication, err := parseQualificationPublication(string(committedOutput), deploymentCandidate)
	if err != nil {
		return report, err
	}
	if committedPublication.Status != "committed" ||
		committedPublication.DeploymentID != pendingPublication.DeploymentID {
		return report, fmt.Errorf("canonical publication retry did not commit the exact interrupted publication")
	}
	deploymentEvidence, err := waitForQualificationPublicationEvidence(
		ctx, client, apiRoot, options.ProjectID, options.PublisherToken,
		deploymentCandidate, pendingPublication,
	)
	if err != nil {
		return report, err
	}
	if deploymentEvidence.GenerationId != committedPublication.GenerationID {
		return report, fmt.Errorf("canonical publication retry returned a different generation")
	}
	deploymentEvents, err := json.Marshal(deploymentEvidence)
	if err != nil {
		return report, err
	}
	report.Assertions.DeploymentActivation = true
	if err := c.clearQualificationActivationBarrier(ctx, options.ContainerID); err != nil {
		return report, err
	}

	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	report.Stage = "refresh materialization interruption"
	ctx = phases.Begin(rootContext, report.Stage, 15*time.Minute)
	// Capture the durable GC cycle set before throttling and starting refresh.
	// Once the refresh writer is running, the operator snapshot may be
	// intentionally unavailable while its delivery transaction is in flight.
	refreshGCBaseline, err := c.captureQualificationGCBaseline(
		ctx, client, apiRoot, options.ProjectID, options.OperatorToken,
		options.ContainerID, report.Stage,
	)
	if err != nil {
		return report, err
	}
	// Make the execution interval observable before killing the process. On a
	// fast or warm target the refresh can otherwise move from queued directly
	// to succeeded between one-second status polls, leaving the recovery gate
	// waiting for a transient state that already passed.
	if _, err := c.qualificationDocker(ctx, nil, "update", "--cpus", qualificationRecoveryInterruptedWorkCPUs, options.ContainerID); err != nil {
		return report, err
	}
	var refresh struct {
		ID string `json:"id"`
	}
	refreshIDKey := fmt.Sprintf("qualification-refresh-%d", time.Now().Unix())
	if err := qualificationAPI(
		ctx, client, http.MethodPost,
		apiRoot+"/api/v1/projects/"+urlPath(options.ProjectID)+"/refresh-runs",
		options.WorkloadToken,
		map[string]string{"pipelineId": qualificationRefreshPipelineID},
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
	queryBody := map[string]any{
		"dimensions": []map[string]string{{"field": "state"}},
		"metrics": []map[string]string{
			{"field": "order_count"},
			{"field": "revenue"},
		},
		"limit": 10,
	}
	gcProbeSequence := uint64(0)
	if _, err := c.waitQualificationGCStable(
		ctx, client, apiRoot, options.ProjectID, options.OperatorToken,
		options.WorkloadToken, refreshGCBaseline, &gcProbeSequence,
		options.ContainerID, "refresh recovery",
	); err != nil {
		return report, err
	}
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
	for cycle := 1; cycle <= 3; cycle++ {
		gcBaseline, err := c.captureQualificationGCBaseline(
			ctx, client, apiRoot, options.ProjectID, options.OperatorToken,
			options.ContainerID, fmt.Sprintf("%s cycle %d", report.Stage, cycle),
		)
		if err != nil {
			return report, err
		}
		if err := c.killAndRecoverQualificationCandidate(ctx, options.ContainerID, report.Stage); err != nil {
			return report, err
		}
		queryResult, err := c.waitQualificationGCStable(
			ctx, client, apiRoot, options.ProjectID, options.OperatorToken,
			options.WorkloadToken, gcBaseline, &gcProbeSequence,
			options.ContainerID, fmt.Sprintf("%s cycle %d", report.Stage, cycle),
		)
		if err != nil {
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
	sourceCSV, cleanupCSV, err := c.qualificationCopyFromContainer(
		ctx,
		options.ContainerID,
		"/app/evaluation/data/orders.csv",
	)
	if err != nil {
		return err
	}
	defer cleanupCSV()
	sourceProject, cleanupProject, err := c.qualificationCopyFromContainer(
		ctx,
		options.ContainerID,
		"/app/evaluation/project",
	)
	if err != nil {
		return err
	}
	defer cleanupProject()
	recoveryRoot := filepath.Join(workDir, "qualification-recovery")
	inputDir := filepath.Join(recoveryRoot, "input")
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
		"project-a": qualificationRecoveryReleaseProjectName,
		"project-b": qualificationRecoveryDeploymentProjectName,
	} {
		target := filepath.Join(recoveryRoot, name)
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
		// Container archives preserve the hardened read-only project file mode.
		// This is a private host-side recovery fixture, so make only the copied
		// fixture owner-writable before changing its qualification-only name.
		if err := os.Chmod(projectPath, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(projectPath, contents, 0o600); err != nil {
			return err
		}
	}
	candidate := c.qualificationContainers.Existing(options.ContainerID)
	exists, err := c.qualificationContainerPathExists(
		ctx,
		options.ContainerID,
		"/var/lib/leapview/qualification-recovery",
	)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("qualification recovery destination already exists")
	}
	if err := makeQualificationContainerReadable(recoveryRoot); err != nil {
		return err
	}
	if _, err := candidate.CopyTo(
		ctx,
		recoveryRoot,
		"/var/lib/leapview/qualification-recovery",
	); err != nil {
		return err
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

func makeQualificationProjectDirTraversable(root string) error {
	return os.Chmod(root, 0o755)
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
	if _, err := c.qualificationContainers.Start(ctx, qualificationRecoveryClientRequest(
		options,
		container,
		workDir,
		clientHome,
		certificateFile,
	)); err != nil {
		return "", err
	}
	return container, nil
}

func qualificationRecoveryClientRequest(
	options qualificationRecoveryOptions,
	container string,
	workDir string,
	clientHome string,
	certificateFile string,
) qualificationContainerRequest {
	return qualificationContainerRequest{
		Name:        container,
		Image:       options.ClientImage,
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
	}
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
	return newQualificationRunningCommand(command, output), nil
}

func (c *Controller) runQualificationClientCommand(
	ctx context.Context,
	clientContainer string,
	token string,
	arguments ...string,
) ([]byte, error) {
	return c.qualificationContainers.Existing(clientContainer).ExecEnvironment(
		ctx, nil,
		map[string]string{
			"LEAPVIEW_API_TOKEN": token,
			"LEAPVIEW_TARGET":    "https://localhost",
			"LEAPVIEW_HOME":      "/client-home",
		},
		arguments...,
	)
}

// runQualificationPublicationRetry retries only the idempotent publication
// replay used after the qualification harness intentionally interrupts target
// activation. A restarted target may briefly hold its physical-pool GC fence;
// every other failure remains immediate and unchanged.
func (c *Controller) runQualificationPublicationRetry(
	ctx context.Context,
	clientContainer string,
	token string,
	candidateID string,
) ([]byte, error) {
	return retryQualificationPublication(ctx, c.sleep, func(attemptCtx context.Context) ([]byte, error) {
		return c.runQualificationClientCommand(
			attemptCtx,
			clientContainer,
			token,
			"leapview", "publish", candidateID,
			"--format", "json",
		)
	})
}

func retryQualificationPublication(
	ctx context.Context,
	sleep func(context.Context, time.Duration) error,
	publish func(context.Context) ([]byte, error),
) ([]byte, error) {
	backoff := qualificationPublicationRetryInitialBackoff
	for attempt := 1; attempt <= qualificationPublicationRetryAttempts; attempt++ {
		output, err := publish(ctx)
		if err == nil ||
			!strings.Contains(err.Error(), qualificationDeliveryInputUnavailableMarker) ||
			attempt == qualificationPublicationRetryAttempts {
			return output, err
		}
		if err := sleep(ctx, backoff); err != nil {
			return nil, err
		}
		backoff = min(backoff*2, qualificationPublicationRetryMaxBackoff)
	}
	panic("unreachable qualification publication retry")
}

type qualificationGCCycleObservation struct {
	ID     string
	PoolID string
	Epoch  int64
	Status string
}

type qualificationGCStabilityObservation struct {
	DegradedReasons []string
	Cycles          []qualificationGCCycleObservation
}

func (observation qualificationGCStabilityObservation) gcLeaseActive() bool {
	return slices.Contains(observation.DegradedReasons, qualificationGCLeaseActiveReason)
}

func (observation qualificationGCStabilityObservation) containsCycle(id string) bool {
	for _, cycle := range observation.Cycles {
		if cycle.ID == id {
			return true
		}
	}
	return false
}

func (observation qualificationGCStabilityObservation) cycleSummary() []string {
	summary := make([]string, 0, len(observation.Cycles))
	for _, cycle := range observation.Cycles {
		summary = append(summary, fmt.Sprintf(
			"%s(pool=%s epoch=%d status=%s)",
			cycle.ID, cycle.PoolID, cycle.Epoch, cycle.Status,
		))
	}
	return summary
}

func qualificationGCObservation(
	ctx context.Context,
	httpClient *http.Client,
	target string,
	projectID string,
	token string,
) (qualificationGCStabilityObservation, error) {
	client := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		target,
		token,
		httpClient,
	))
	response, err := client.GetDeliveryOperatorSnapshot(
		ctx,
		deploymentgen.GenGetDeliveryOperatorSnapshotClientRequest{Project: projectID},
	)
	if err != nil {
		return qualificationGCStabilityObservation{}, err
	}
	observation := qualificationGCStabilityObservation{
		DegradedReasons: append([]string(nil), response.Body.DegradedReasons...),
		Cycles:          make([]qualificationGCCycleObservation, 0, len(response.Body.GcCycles)),
	}
	for _, cycle := range response.Body.GcCycles {
		observation.Cycles = append(observation.Cycles, qualificationGCCycleObservation{
			ID: cycle.Id, PoolID: cycle.PoolId, Epoch: cycle.Epoch, Status: string(cycle.Status),
		})
	}
	return observation, nil
}

func (c *Controller) captureQualificationGCBaseline(
	ctx context.Context,
	httpClient *http.Client,
	target string,
	projectID string,
	token string,
	containerID string,
	stage string,
) (qualificationGCStabilityObservation, error) {
	waitCtx, cancel := qualificationContext(ctx, qualificationGCStabilityTimeout)
	defer cancel()
	observation, err := waitForQualificationGCBaseline(
		waitCtx,
		c.sleep,
		func(probeCtx context.Context) (qualificationGCStabilityObservation, error) {
			return qualificationGCObservation(probeCtx, httpClient, target, projectID, token)
		},
	)
	if err == nil {
		return observation, nil
	}
	return qualificationGCStabilityObservation{}, qualificationContainerOperationError(
		ctx,
		c.qualificationContainers.Existing(containerID),
		"capture physical-pool GC baseline before "+stage,
		err,
	)
}

func waitForQualificationGCBaseline(
	ctx context.Context,
	sleep func(context.Context, time.Duration) error,
	observe func(context.Context) (qualificationGCStabilityObservation, error),
) (qualificationGCStabilityObservation, error) {
	observations := 0
	var last qualificationGCStabilityObservation
	var lastObservationErr error
	for {
		observations++
		observation, err := observe(ctx)
		if err == nil {
			last = observation
			lastObservationErr = nil
			if !observation.gcLeaseActive() {
				return observation, nil
			}
		} else {
			if !strings.Contains(err.Error(), qualificationGCSnapshotUnavailableMarker) {
				return qualificationGCStabilityObservation{}, fmt.Errorf(
					"physical-pool GC baseline snapshot failed: %w", err,
				)
			}
			lastObservationErr = err
		}
		if sleepErr := sleep(ctx, qualificationGCStabilityPollInterval); sleepErr != nil {
			if lastObservationErr != nil {
				return qualificationGCStabilityObservation{}, fmt.Errorf(
					"physical-pool GC baseline was not observable after %d snapshots (lastSnapshotError=%v): %w",
					observations, lastObservationErr, sleepErr,
				)
			}
			return qualificationGCStabilityObservation{}, fmt.Errorf(
				"physical-pool GC baseline did not become idle after %d snapshots (observedCycles=%v observedLeaseActive=%t degradedReasons=%v): %w",
				observations, last.cycleSummary(), last.gcLeaseActive(), last.DegradedReasons, sleepErr,
			)
		}
	}
}

func (c *Controller) waitQualificationGCStable(
	ctx context.Context,
	httpClient *http.Client,
	target string,
	projectID string,
	operatorToken string,
	workloadToken string,
	baseline qualificationGCStabilityObservation,
	probeSequence *uint64,
	containerID string,
	stage string,
) (struct {
	Rows []json.RawMessage `json:"rows"`
}, error) {
	var queryResult struct {
		Rows []json.RawMessage `json:"rows"`
	}
	waitCtx, cancel := qualificationContext(ctx, qualificationGCStabilityTimeout)
	defer cancel()
	err := waitForQualificationGCStability(
		waitCtx,
		c.sleep,
		baseline,
		func(probeCtx context.Context) (qualificationGCStabilityObservation, error) {
			return qualificationGCObservation(
				probeCtx, httpClient, target, projectID, operatorToken,
			)
		},
		func(probeCtx context.Context) error {
			queryResult.Rows = nil
			*probeSequence = *probeSequence + 1
			return qualificationAPI(
				probeCtx, httpClient, http.MethodPost,
				target+"/api/v1/semantic-models/semantic-model:sales/query",
				workloadToken, qualificationGCProbeQuery(*probeSequence), "", &queryResult,
			)
		},
	)
	if err == nil {
		return queryResult, nil
	}
	return queryResult, qualificationContainerOperationError(
		ctx,
		c.qualificationContainers.Existing(containerID),
		"observe physical-pool GC stability after "+stage,
		err,
	)
}

// qualificationGCProbeQuery gives every probe a distinct result-cache identity.
// A cached 200 response does not acquire a sealed-catalog query lease and cannot
// establish that the startup GC fence has been released.
func qualificationGCProbeQuery(sequence uint64) map[string]any {
	return map[string]any{
		"dimensions": []map[string]string{{"field": "state"}},
		"metrics": []map[string]string{
			{"field": "order_count", "alias": fmt.Sprintf("gc_probe_%d", sequence)},
			{"field": "revenue"},
		},
		"limit": 10,
	}
}

func waitForQualificationGCStability(
	ctx context.Context,
	sleep func(context.Context, time.Duration) error,
	baseline qualificationGCStabilityObservation,
	observe func(context.Context) (qualificationGCStabilityObservation, error),
	acquireQueryLease func(context.Context) error,
) error {
	observations := 0
	queryProbes := 0
	var last qualificationGCStabilityObservation
	var lastObservationErr error
	var lastQueryErr error
	for {
		observations++
		observation, err := observe(ctx)
		if err == nil {
			last = observation
			lastObservationErr = nil
			if !observation.gcLeaseActive() && qualificationHasNewCompletedGCCycle(baseline, observation) {
				queryProbes++
				queryErr := acquireQueryLease(ctx)
				if queryErr == nil {
					return nil
				}
				lastQueryErr = queryErr
				if !strings.Contains(queryErr.Error(), qualificationGCQueryFenceMarker) {
					return fmt.Errorf("physical-pool GC governed-query lease probe failed: %w", queryErr)
				}
			}
		} else {
			lastObservationErr = err
		}
		if sleepErr := sleep(ctx, qualificationGCStabilityPollInterval); sleepErr != nil {
			if lastObservationErr != nil {
				return fmt.Errorf(
					"physical-pool GC stability was not observable after %d snapshots (baselineCycles=%v lastSnapshotError=%v): %w",
					observations, baseline.cycleSummary(), lastObservationErr, sleepErr,
				)
			}
			return fmt.Errorf(
				"physical-pool GC did not stabilize after %d snapshots and %d governed-query probes (baselineCycles=%v baselineLeaseActive=%t observedCycles=%v observedLeaseActive=%t degradedReasons=%v lastQueryError=%v): %w",
				observations, queryProbes, baseline.cycleSummary(), baseline.gcLeaseActive(),
				last.cycleSummary(), last.gcLeaseActive(), last.DegradedReasons, lastQueryErr, sleepErr,
			)
		}
	}
}

func qualificationHasNewCompletedGCCycle(
	baseline qualificationGCStabilityObservation,
	observation qualificationGCStabilityObservation,
) bool {
	for _, cycle := range observation.Cycles {
		if !baseline.containsCycle(cycle.ID) && cycle.Status == string(deploymentgen.DeliveryGCStatusComplete) {
			return true
		}
	}
	return false
}

func (r *qualificationRunningCommand) Stop() error {
	if r == nil {
		return nil
	}
	if r.command != nil && r.Running() && r.command.Process != nil {
		_ = r.command.Process.Kill()
	}
	if r.done != nil {
		<-r.done
	}
	r.mu.Lock()
	waitErr := r.waitErr
	r.mu.Unlock()
	var closeErr error
	r.close.Do(func() {
		if r.output != nil {
			closeErr = r.output.Close()
		}
	})
	if waitErr != nil && !strings.Contains(waitErr.Error(), "signal: killed") {
		return errorsJoin(waitErr, closeErr)
	}
	return closeErr
}

func newQualificationRunningCommand(command *exec.Cmd, output *os.File) *qualificationRunningCommand {
	running := &qualificationRunningCommand{command: command, output: output, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		running.mu.Lock()
		running.waitErr = err
		running.mu.Unlock()
		close(running.done)
	}()
	return running
}

// Running reports whether the command has completed. The process is reaped by
// the constructor's single waiter, so callers can inspect state without racing
// a second Wait (the old implementation used to double-Wait during recovery
// cleanup).
func (r *qualificationRunningCommand) Running() bool {
	if r == nil || r.command == nil {
		return false
	}
	if r.done == nil {
		return r.command.ProcessState == nil
	}
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func qualificationActivationBarrierContainerPath(marker string) string {
	return "/var/lib/leapview/home/" + marker
}

func (c *Controller) armQualificationActivationBarrier(
	ctx context.Context,
	containerID string,
	workDir string,
) error {
	// Reached evidence is durable across a restart. Remove both sides before
	// arming so a stale marker can never satisfy this run's bounded wait.
	if err := c.clearQualificationActivationBarrier(ctx, containerID); err != nil {
		return fmt.Errorf("clear qualification activation barrier markers: %w", err)
	}
	armedFile := filepath.Join(workDir, qualificationbarrier.ArmedMarker)
	if err := os.WriteFile(armedFile, []byte("qualification-recovery\n"), 0o600); err != nil {
		return fmt.Errorf("write qualification activation barrier marker: %w", err)
	}
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"cp", armedFile,
		containerID+":"+qualificationActivationBarrierContainerPath(qualificationbarrier.ArmedMarker),
	); err != nil {
		return fmt.Errorf("arm qualification activation barrier: %w", err)
	}
	return nil
}

func (c *Controller) clearQualificationActivationBarrier(ctx context.Context, containerID string) error {
	return c.removeQualificationContainerPathsWithTooling(ctx, containerID,
		qualificationActivationBarrierContainerPath(qualificationbarrier.ArmedMarker),
		qualificationActivationBarrierContainerPath(qualificationbarrier.ReachedMarker),
	)
}

func (c *Controller) waitForQualificationActivationBarrier(
	ctx context.Context,
	containerID string,
	workDir string,
) error {
	reachedFile := filepath.Join(workDir, qualificationbarrier.ReachedMarker)
	return qualificationWait(ctx, 250*time.Millisecond, func(waitCtx context.Context) (bool, error) {
		_ = os.Remove(reachedFile)
		_, err := c.qualificationDocker(
			waitCtx,
			nil,
			"cp",
			containerID+":"+qualificationActivationBarrierContainerPath(qualificationbarrier.ReachedMarker),
			reachedFile,
		)
		if err != nil {
			return false, nil
		}
		_, statErr := os.Stat(reachedFile)
		return statErr == nil, statErr
	})
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
	return newQualificationRunningCommand(command, output), nil
}

func (c *Controller) killAndRecoverQualificationCandidate(
	ctx context.Context,
	containerID string,
	stage string,
	afterKill ...func(),
) error {
	container := c.qualificationContainers.Existing(containerID)
	if _, err := container.Kill(ctx, "KILL"); err != nil {
		return err
	}
	for _, callback := range afterKill {
		if callback != nil {
			callback()
		}
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
		if expected == "running" {
			switch response.Status {
			case "prepared":
				// Canonical refresh claims move immediately from running to
				// prepared before the delivery build begins. Both states prove
				// that durable execution is in flight and can be interrupted.
				return true, nil
			case "succeeded", "failed", "cancelled", "canceled", "superseded":
				return false, fmt.Errorf("qualification operation reached terminal status %q before running was observed", response.Status)
			}
		}
		return response.Status == expected, nil
	})
}

func waitForQualificationCandidateEvidence(
	ctx context.Context,
	httpClient *http.Client,
	target string,
	projectID string,
	token string,
	candidate QualificationCandidate,
) (deploymentgen.DeliveryCandidateStatusResponse, error) {
	client := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		target,
		token,
		httpClient,
	))
	waitCtx, cancel := qualificationContext(ctx, 10*time.Minute)
	defer cancel()
	var evidence deploymentgen.DeliveryCandidateStatusResponse
	err := qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		response, err := client.GetDeliveryCandidateStatus(
			requestCtx,
			deploymentgen.GenGetDeliveryCandidateStatusClientRequest{
				Project: projectID, Candidate: candidate.ID,
			},
		)
		if err != nil {
			return false, nil
		}
		evidence = response.Body
		if evidence.Status == deploymentgen.DeliveryCandidateStatusFailed ||
			evidence.Status == deploymentgen.DeliveryCandidateStatusRetired {
			return false, fmt.Errorf("canonical recovery candidate reached terminal status %q", evidence.Status)
		}
		return evidence.Status == deploymentgen.DeliveryCandidateStatusReady, nil
	})
	if err != nil {
		return deploymentgen.DeliveryCandidateStatusResponse{}, err
	}
	if evidence.Id != candidate.ID ||
		evidence.PlanId != candidate.PlanID ||
		evidence.PlanDigest != candidate.PlanDigest ||
		evidence.SourceDigest != candidate.ArtifactDigest ||
		evidence.TargetId != candidate.TargetID {
		return deploymentgen.DeliveryCandidateStatusResponse{}, fmt.Errorf(
			"canonical recovery candidate evidence does not match the resumed candidate",
		)
	}
	return evidence, nil
}

func qualificationPublicationEvidence(
	ctx context.Context,
	httpClient *http.Client,
	target string,
	projectID string,
	token string,
	candidate QualificationCandidate,
	publication QualificationPublication,
) (deploymentgen.DeliveryPublicationEvidenceResponse, error) {
	client := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		target,
		token,
		httpClient,
	))
	response, err := client.GetDeliveryPublicationEvidence(
		ctx,
		deploymentgen.GenGetDeliveryPublicationEvidenceClientRequest{
			Project: projectID, Publication: publication.DeploymentID,
		},
	)
	if err != nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, err
	}
	evidence := response.Body
	if evidence.Id != publication.DeploymentID ||
		evidence.CandidateId != candidate.ID ||
		evidence.GenerationId != publication.GenerationID ||
		evidence.PlanId != candidate.PlanID ||
		evidence.PlanDigest != candidate.PlanDigest ||
		evidence.TargetId != candidate.TargetID {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, fmt.Errorf(
			"canonical recovery publication evidence does not match the interrupted publication",
		)
	}
	return evidence, nil
}

func waitForQualificationPublicationEvidence(
	ctx context.Context,
	httpClient *http.Client,
	target string,
	projectID string,
	token string,
	candidate QualificationCandidate,
	publication QualificationPublication,
) (deploymentgen.DeliveryPublicationEvidenceResponse, error) {
	waitCtx, cancel := qualificationContext(ctx, 10*time.Minute)
	defer cancel()
	var evidence deploymentgen.DeliveryPublicationEvidenceResponse
	err := qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		observed, err := qualificationPublicationEvidence(
			requestCtx,
			httpClient,
			target,
			projectID,
			token,
			candidate,
			publication,
		)
		if err != nil {
			return false, nil
		}
		evidence = observed
		if evidence.Status == deploymentgen.DeliveryPublicationStatusRejected {
			return false, fmt.Errorf("canonical recovery publication was rejected")
		}
		return evidence.Status == deploymentgen.DeliveryPublicationStatusCommitted, nil
	})
	if err != nil {
		return deploymentgen.DeliveryPublicationEvidenceResponse{}, err
	}
	return evidence, nil
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
	bytesUsed, err := c.qualificationContainerTreeBytes(
		ctx,
		containerID,
		"/var/lib/leapview",
	)
	if err != nil {
		return 0, err
	}
	if bytesUsed == 0 {
		return 0, nil
	}
	return (bytesUsed + 1023) / 1024, nil
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

func qualificationManagedConnectionPath(projectID string) string {
	return "/api/v1/projects/" + urlPath(projectID) + "/connections/" + urlPath(qualificationManagedConnectionID)
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
