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

const (
	qualificationRecoveryFullCPUs               = "1"
	qualificationRecoveryInterruptedWorkCPUs    = "0.03"
	qualificationRecoveryReleaseCandidateKey    = "qualification-recovery-release"
	qualificationRecoveryDeploymentCandidateKey = "qualification-recovery-deployment"
	qualificationManagedConnectionID            = "connection:sample"
	qualificationRefreshPipelineID              = "pipeline:evaluation-refresh"
	qualificationRecoveryReleaseProjectName     = "recovery-release-project"
	qualificationRecoveryDeploymentProjectName  = "recovery-deployment-project"
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
	Target               string
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
		BoundedDisk          bool `json:"boundedDisk"`
	} `json:"assertions"`
	BoundedState struct {
		DiskBeforeKiB        int64 `json:"diskBeforeKiB"`
		DiskAfterKiB         int64 `json:"diskAfterKiB"`
		DiskGrowthKiB        int64 `json:"diskGrowthKiB"`
		DiskGrowthLimitKiB   int64 `json:"diskGrowthLimitKiB"`
		StaleRecoveryEntries int64 `json:"staleRecoveryEntries"`
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
		"container":              options.ContainerID,
		"Compose project":        options.ComposeProject,
		"project":                options.ProjectID,
		"image":                  options.Image,
		"target":                 options.Target,
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
	// A clean installation legitimately has no active managed-data revision
	// until its first deployment selects a staged revision. The interruption
	// invariant is still exact: the partial upload must leave that empty active
	// pointer unchanged, just as it must preserve an existing revision.
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
		options.Target,
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
		options.Target,
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
		ctx, recoveryClient, options.PublisherToken, options.Target, releaseLog,
		"leapview", "dev", "--once", "--no-browser",
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
		ctx, recoveryClient, options.PublisherToken, options.Target,
		"leapview", "dev", "--once", "--no-browser",
		"--project", "/work/project-a/leapview.yaml",
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
		ctx, recoveryClient, options.PublisherToken, options.Target,
		"leapview", "dev", "--once", "--no-browser",
		"--project", "/work/project-b/leapview.yaml",
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
		ctx, recoveryClient, options.PublisherToken, options.Target,
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
		ctx, recoveryClient, options.PublisherToken, options.Target, deploymentLog,
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
	committedOutput, err := c.runQualificationClientCommand(
		ctx, recoveryClient, options.PublisherToken, options.Target,
		"leapview", "publish", deploymentCandidate.ID,
		"--format", "json",
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
		"metrics": []map[string]string{
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
	report.Stage = "bounded recovery state"
	ctx = phases.Begin(rootContext, report.Stage, 10*time.Minute)
	_, _ = c.qualificationDocker(ctx, nil, "update", "--cpus", qualificationRecoveryFullCPUs, options.ContainerID)
	diskAfter, err := c.qualificationContainerDiskKiB(ctx, options.ContainerID)
	if err != nil {
		return report, err
	}
	report.BoundedState.DiskAfterKiB = diskAfter
	report.BoundedState.DiskGrowthKiB = diskAfter - diskBefore
	if report.BoundedState.DiskGrowthKiB > qualificationRecoveryDiskLimitKiB {
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
	for name, variant := range map[string]struct {
		title string
		alias string
	}{
		"project-a": {title: qualificationRecoveryReleaseProjectName, alias: "qualification_release_orders"},
		"project-b": {title: qualificationRecoveryDeploymentProjectName, alias: "qualification_deployment_orders"},
	} {
		target := filepath.Join(workDir, name)
		if err := copyQualificationTree(sourceProject, target); err != nil {
			return err
		}
		if err := rewriteQualificationRecoveryProject(target, variant.title, variant.alias); err != nil {
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

func rewriteQualificationRecoveryProject(root, title, modelAlias string) error {
	projectPath := filepath.Join(root, "leapview.yaml")
	projectContents, err := os.ReadFile(projectPath)
	if err != nil {
		return err
	}
	projectName := []byte("name: leapview-evaluation")
	if bytes.Count(projectContents, projectName) != 1 {
		return fmt.Errorf("qualification recovery project name marker is not unique")
	}
	projectContents = bytes.Replace(projectContents, projectName, []byte("name: "+title), 1)
	if err := os.WriteFile(projectPath, projectContents, 0o600); err != nil {
		return err
	}

	// The recovery journey must exercise a real native build on both sides of
	// each interruption. A metadata-only rename is eligible for whole-candidate
	// reuse and can complete before the interruption boundary. Give each copy a
	// semantically equivalent but distinct model execution identity instead.
	modelPath := filepath.Join(root, "models", "orders.yaml")
	modelContents, err := os.ReadFile(modelPath)
	if err != nil {
		return err
	}
	from := []byte(`      FROM source."sample.orders"`)
	if bytes.Count(modelContents, from) != 1 {
		return fmt.Errorf("qualification recovery model source marker is not unique")
	}
	modelContents = bytes.Replace(modelContents, from, []byte(`      FROM source."sample.orders" AS `+modelAlias), 1)
	return os.WriteFile(modelPath, modelContents, 0o600)
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
			"SSL_CERT_FILE":   "/run/certs/caddy-root.crt",
			"LEAPVIEW_TARGET": options.Target,
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
	target string,
	arguments ...string,
) []string {
	result := []string{
		"exec",
		"--env", "LEAPVIEW_API_TOKEN=" + token,
		"--env", "LEAPVIEW_TARGET=" + target,
		"--env", "LEAPVIEW_HOME=/client-home",
		container,
	}
	return append(result, arguments...)
}

func (c *Controller) startQualificationClientCommand(
	ctx context.Context,
	clientContainer string,
	token string,
	target string,
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
		target,
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
	target string,
	arguments ...string,
) ([]byte, error) {
	return c.qualificationContainers.Existing(clientContainer).Exec(
		ctx, nil,
		append(
			[]string{
				"env",
				"LEAPVIEW_API_TOKEN=" + token,
				"LEAPVIEW_TARGET=" + target,
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
	_, err := c.qualificationContainers.Existing(containerID).Exec(ctx, nil, "rm", "-f",
		qualificationActivationBarrierContainerPath(qualificationbarrier.ArmedMarker),
		qualificationActivationBarrierContainerPath(qualificationbarrier.ReachedMarker),
	)
	return err
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
