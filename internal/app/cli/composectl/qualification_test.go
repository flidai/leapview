package composectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/channel"
	"github.com/creachadair/jrpc2/handler"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	"github.com/stretchr/testify/require"
)

func TestQualificationCommandSurfaceBelongsToLeapviewctl(t *testing.T) {
	controller, err := New(Options{
		Root:   t.TempDir(),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	command := Command(context.Background(), controller)
	command.SetArgs([]string{"qualify", "--help"})
	var output bytes.Buffer
	command.SetOut(&output)

	if err := command.Execute(); err != nil {
		t.Fatalf("qualify help: %v", err)
	}
	for _, required := range []string{"image", "site-image", "installed-candidate"} {
		if !strings.Contains(output.String(), required) {
			t.Errorf("qualification help missing %q:\n%s", required, output.String())
		}
	}
}

func TestQualificationPlanEvidenceUsesSharedCandidateIdentityDomains(t *testing.T) {
	candidate := QualificationCandidate{
		PlanDigest: "sha256:plan", ArtifactDigest: "sha256:artifact",
		ProvenanceDigest: "sha256:candidate-provenance", TargetID: "target-prod",
	}
	plan := deploymentgen.DeliveryPlanPreviewResponse{
		PlanDigest: "sha256:plan", SourceDigest: "sha256:artifact",
		ProvenanceDigest: "sha256:delivery-plan-provenance", TargetId: "target-prod",
	}
	if !qualificationPlanMatchesCandidate(plan, candidate) {
		t.Fatal("distinct candidate and delivery-plan provenance domains were treated as an identity mismatch")
	}
	plan.SourceDigest = "sha256:other-artifact"
	if qualificationPlanMatchesCandidate(plan, candidate) {
		t.Fatal("mismatched source artifact was accepted")
	}
}

func TestInstalledQualificationAcceptsExplicitReleaseBundle(t *testing.T) {
	controller, err := New(Options{
		Root: t.TempDir(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	command := Command(t.Context(), controller)
	command.SetArgs([]string{"qualify", "installed-candidate", "--help"})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "--bundle") {
		t.Fatalf("installed-candidate help = %s", output.String())
	}
}

func TestQualificationTransientDeploymentErrorRecognizesStructuredAndPlainErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "structured problem", err: &apigenclient.ProblemError{Response: apigenclient.Response{StatusCode: http.StatusServiceUnavailable}, Problem: apigenclient.ProblemDetails{Status: http.StatusServiceUnavailable}}, want: true},
		{name: "plain middleware response", err: errors.New("GET https://localhost/api/v1/deployments/deployment_1: Service Unavailable"), want: true},
		{name: "plain rate limit response", err: errors.New("GET https://localhost/api/v1/deployments/deployment_1: Too Many Requests"), want: true},
		{name: "structured rate limit response", err: &apigenclient.ProblemError{Response: apigenclient.Response{StatusCode: http.StatusTooManyRequests}, Problem: apigenclient.ProblemDetails{Status: http.StatusTooManyRequests}}, want: true},
		{name: "other transport error", err: errors.New("GET https://localhost/api/v1/deployments/deployment_1: connection reset"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := qualificationTransientDeploymentError(test.err); got != test.want {
				t.Fatalf("qualificationTransientDeploymentError() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestVerifyExactAuthoringCandidate(t *testing.T) {
	candidate := QualificationCandidate{
		ID:               "cand_1",
		Revision:         7,
		TargetID:         "target_1",
		PrincipalID:      "principal_1",
		ArtifactDigest:   "sha256:" + strings.Repeat("a", 64),
		ProvenanceDigest: "sha256:" + strings.Repeat("b", 64),
		SourceRevision:   "sha256:" + strings.Repeat("c", 64),
		PlanID:           "plan_1",
		PlanDigest:       "sha256:" + strings.Repeat("e", 64),
	}
	publication := QualificationPublication{
		CandidateID:       candidate.ID,
		CandidateRevision: candidate.Revision,
		TargetID:          candidate.TargetID,
		PrincipalID:       candidate.PrincipalID,
		ArtifactDigest:    "sha256:" + strings.Repeat("d", 64),
		ReleaseDigest:     candidate.ProvenanceDigest,
		SourceRevision:    candidate.SourceRevision,
		GenerationID:      "generation_1",
		PlanID:            candidate.PlanID,
		PlanDigest:        candidate.PlanDigest,
		Status:            "pending",
	}
	deployment := QualificationDeployment{
		CandidateID:       candidate.ID,
		CandidateRevision: candidate.Revision,
		TargetID:          candidate.TargetID,
		PrincipalID:       candidate.PrincipalID,
		ArtifactDigest:    publication.ArtifactDigest,
		ReleaseDigest:     candidate.ProvenanceDigest,
		GenerationID:      publication.GenerationID,
		PlanID:            candidate.PlanID,
		PlanDigest:        candidate.PlanDigest,
		Status:            "active",
	}

	if err := verifyExactAuthoringCandidate(candidate, publication, deployment); err != nil {
		t.Fatalf("verify exact candidate: %v", err)
	}
	deployment.CandidateRevision++
	if err := verifyExactAuthoringCandidate(candidate, publication, deployment); err == nil ||
		!strings.Contains(err.Error(), "revision") {
		t.Fatalf("revision mismatch error = %v", err)
	}
}

func TestQualificationRedactorBoundsAndRemovesCredentials(t *testing.T) {
	input := strings.Repeat("ordinary line\n", 600) +
		"Authorization: Bearer secret-token\n" +
		"LEAPVIEW_API_TOKEN=environment-secret\n" +
		`{"accessToken":"access","publisherToken":"publisher","workloadToken":"workload","projectDataToken":"project-data","recoveryControlToken":"recovery-control","auditToken":"audit","temporaryPassword":"temporary","qualificationPassword":"qualification"}` +
		"\n"
	redacted := redactQualificationLog([]byte(input), 500)
	text := string(redacted)
	for _, secret := range []string{
		"secret-token",
		"environment-secret",
		`"access"`,
		`"publisher"`,
		`"workload"`,
		`"project-data"`,
		`"recovery-control"`,
		`"audit"`,
		`"temporary"`,
		`"qualification"`,
	} {
		if strings.Contains(text, secret) {
			t.Errorf("redacted log retains %q", secret)
		}
	}
	if lines := strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1; lines != 500 {
		t.Fatalf("bounded log lines = %d, want 500", lines)
	}
}

func TestQualificationWorkloadTokenNeverFallsBackToPublisher(t *testing.T) {
	credentials := qualificationCredentials{
		PublisherToken: "publisher-secret",
	}
	if _, err := credentials.workloadToken(); err == nil {
		t.Fatal("workloadToken() error = nil, want a dedicated workload credential")
	}
	credentials.WorkloadToken = "workload-secret"
	got, err := credentials.workloadToken()
	require.NoError(t, err)
	if got != credentials.WorkloadToken {
		t.Fatalf("workloadToken() = %q, want dedicated workload token", got)
	}
}

func TestQualificationProjectDataTokenNeverFallsBackToPublisher(t *testing.T) {
	credentials := qualificationCredentials{
		PublisherToken: "publisher-secret",
	}
	if _, err := credentials.projectDataToken(); err == nil {
		t.Fatal("projectDataToken() error = nil, want a dedicated project-data credential")
	}
	credentials.ProjectDataToken = "project-data-secret"
	got, err := credentials.projectDataToken()
	require.NoError(t, err)
	if got != credentials.ProjectDataToken {
		t.Fatalf("projectDataToken() = %q, want dedicated project-data token", got)
	}
}

func TestQualificationRecoveryControlTokenNeverFallsBackToPublisher(t *testing.T) {
	credentials := qualificationCredentials{
		PublisherToken: "publisher-secret",
	}
	if _, err := credentials.recoveryControlToken(); err == nil {
		t.Fatal("recoveryControlToken() error = nil, want a dedicated control credential")
	}
	credentials.RecoveryControlToken = "recovery-control-secret"
	got, err := credentials.recoveryControlToken()
	require.NoError(t, err)
	if got != credentials.RecoveryControlToken {
		t.Fatalf("recoveryControlToken() = %q, want dedicated control token", got)
	}
}

func TestQualificationWorkloadCapabilitiesAreReadAndExecuteOnly(t *testing.T) {
	privileges := qualificationWorkloadCapabilities()
	for _, required := range []string{
		"RESOURCE_USE",
		"RESOURCE_READ",
		"RESOURCE_EDIT",
		"RESOURCE_PUBLISH",
	} {
		if !slices.Contains(privileges, required) {
			t.Errorf("workload privileges omit %s: %v", required, privileges)
		}
	}
	for _, forbidden := range []string{
		"PROJECT_ADMIN",
		"RESOURCE_MANAGE",
		"RESOURCE_SHARE",
	} {
		if slices.Contains(privileges, forbidden) {
			t.Errorf("workload privileges unexpectedly include %s: %v", forbidden, privileges)
		}
	}
}

func TestQualificationProjectDataCapabilitiesAreReadOnly(t *testing.T) {
	if got, want := qualificationProjectDataCapabilities(), []string{"RESOURCE_READ"}; !slices.Equal(got, want) {
		t.Fatalf("project-data capabilities = %v, want %v", got, want)
	}
}

func TestQualificationLoopbackRequestUsesProductionAllowedHost(t *testing.T) {
	request, err := newQualificationLoopbackRequest(
		t.Context(),
		http.MethodGet,
		"http://127.0.0.1:8080/metrics",
		nil,
	)
	require.NoError(t, err)
	if request.Host != "localhost" {
		t.Fatalf("request Host = %q, want localhost", request.Host)
	}
}

func TestQualificationAPIUsesProductionAllowedHostForLoopback(t *testing.T) {
	var gotHost string
	client := &http.Client{Transport: qualificationRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			gotHost = request.Host
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		},
	)}
	if err := qualificationAPI(
		t.Context(),
		client,
		http.MethodGet,
		"http://127.0.0.1:8080/api/v1/instance",
		"token",
		nil,
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if gotHost != "localhost" {
		t.Fatalf("API request Host = %q, want localhost", gotHost)
	}
}

func TestQualificationMetricIntegerParsesPrometheusExposition(t *testing.T) {
	for name, test := range map[string]struct {
		exposition string
		want       int64
		wantError  string
	}{
		"gauge": {
			exposition: "# HELP go_goroutines Number of goroutines.\n# TYPE go_goroutines gauge\ngo_goroutines 42\n",
			want:       42,
		},
		"untyped": {
			exposition: "go_goroutines 7\n",
			want:       7,
		},
		"missing": {
			exposition: "# TYPE another gauge\nanother 1\n",
			wantError:  "metrics omit go_goroutines",
		},
		"malformed": {
			exposition: "go_goroutines definitely-not-a-number\n",
			wantError:  "parse Prometheus metrics",
		},
		"fractional": {
			exposition: "# TYPE go_goroutines gauge\ngo_goroutines 1.5\n",
			wantError:  "is not an integer",
		},
		"labelled": {
			exposition: "go_goroutines{worker=\"one\"} 1\n",
			wantError:  "must contain one unlabelled sample",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := qualificationMetricInteger([]byte(test.exposition), "go_goroutines")
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("qualificationMetricInteger() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("qualificationMetricInteger() = %d, %v; want %d", got, err, test.want)
			}
		})
	}
}

type qualificationRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip qualificationRoundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return roundTrip(request)
}

func TestQualificationProcessRedactsSecretsFromArgumentsAndOutput(t *testing.T) {
	executor := &recordingQualificationExecutor{
		output: []byte(`{"accessToken":"response-secret"}`),
		err:    errors.New("exit 1"),
	}
	_, err := (qualificationProcess{
		dir: t.TempDir(), executable: "docker",
	}).Run(
		t.Context(),
		nil,
		executor,
		"exec",
		"--env",
		"LEAPVIEW_API_TOKEN=argument-secret",
		"container",
	)
	if err == nil {
		t.Fatal("qualification process unexpectedly succeeded")
	}
	for _, secret := range []string{"argument-secret", "response-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("process error leaked %q: %v", secret, err)
		}
	}
}

func TestQualificationPerformancePolicyAndEvaluationAreOwnedByGo(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	if failures := validateQualificationPerformancePolicy(policy); len(failures) != 0 {
		t.Fatalf("valid policy failures = %v", failures)
	}
	report := qualificationPerformanceReport{
		Latency: map[string]qualificationDurationSummary{},
	}
	for _, phase := range qualificationLatencyPhases {
		report.Latency[phase.Field] = qualificationDurationSummary{
			Samples: 1,
			P50:     phase.Budget(policy) + 1,
			P95:     phase.Budget(policy) + 1,
			Max:     phase.Budget(policy) + 1,
		}
	}
	report.Reliability.Requests = 100
	report.Reliability.Errors = 1
	report.Resources.PeakResidentMemoryBytes =
		policy.Budgets.PeakResidentMemoryBytes + 1
	report.Resources.CPUSeconds = policy.Budgets.CPUSecondsMax + 1
	report.Resources.TemporaryDiskGrowthBytes =
		policy.Budgets.TemporaryDiskGrowthBytesMax + 1
	report.Resources.GoroutinesAfter =
		policy.Budgets.GoroutineGrowthMax + 1
	report.Resources.PeakOpenConnections =
		policy.Budgets.OpenConnectionsMax + 1
	failures := evaluateQualificationPerformance(report, policy)
	if got, want := len(failures), 13; got != want {
		t.Fatalf("performance failures = %d, want %d: %v", got, want, failures)
	}

	baseline := qualificationPerformanceReport{
		Latency: map[string]qualificationDurationSummary{
			"coldDashboardReadyMs": {P95: 1000},
		},
	}
	candidate := qualificationPerformanceReport{
		Latency: map[string]qualificationDurationSummary{
			"coldDashboardReadyMs": {P95: 1260},
		},
	}
	comparison := compareQualificationPerformance(candidate, baseline, policy)
	if len(comparison) != 1 ||
		!strings.Contains(comparison[0], "cold dashboard readiness") {
		t.Fatalf("comparison failures = %v", comparison)
	}
}

func TestFinalizeQualificationPerformanceReportWritesFailureEvidence(t *testing.T) {
	policy := validQualificationPerformancePolicy()
	path := filepath.Join(t.TempDir(), "performance-report.json")
	report := qualificationPerformanceReport{
		SchemaVersion: 1,
		Latency:       map[string]qualificationDurationSummary{},
	}
	for _, phase := range qualificationLatencyPhases {
		report.Latency[phase.Field] = qualificationDurationSummary{
			Samples: 1,
			P50:     1,
			P95:     1,
			Max:     1,
		}
	}
	report.Reliability.Requests = 1
	if err := writeQualificationJSON(path, report); err != nil {
		t.Fatal(err)
	}
	environment, err := json.Marshal(map[string]any{
		"logicalCPUs": policy.Assumptions.MinimumLogicalCPUs - 1,
		"memoryBytes": policy.Assumptions.MinimumMemoryBytes,
	})
	require.NoError(t, err)
	err = finalizeQualificationPerformanceReport(
		path,
		policy,
		100,
		200,
		environment,
		"leapview:test",
		"amd64",
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "logical CPUs") {
		t.Fatalf("finalize error = %v", err)
	}
	var evidence qualificationPerformanceReport
	if err := readQualificationJSON(path, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Result != "failure" ||
		evidence.Resources.TemporaryDiskGrowthBytes != 100 ||
		evidence.Assertions.Environment {
		t.Fatalf("performance evidence = %+v", evidence)
	}
}

func validQualificationPerformancePolicy() qualificationPerformancePolicy {
	var policy qualificationPerformancePolicy
	policy.SchemaVersion = 1
	policy.Workload = "qualification"
	policy.Assumptions.MinimumLogicalCPUs = 2
	policy.Assumptions.MinimumMemoryBytes = 1024
	policy.Assumptions.Samples.ColdDashboardLoads = 1
	policy.Assumptions.Samples.WarmDashboardLoads = 1
	policy.Assumptions.Samples.FilterInteractions = 1
	policy.Assumptions.Samples.TableInteractions = 1
	policy.Assumptions.Samples.GovernedQueries = 1
	policy.Assumptions.Samples.RefreshRuns = 1
	policy.Assumptions.Samples.ConcurrentReaders = 1
	policy.Budgets.ColdDashboardReadyP95Ms = 10
	policy.Budgets.WarmDashboardReadyP95Ms = 10
	policy.Budgets.FilterToSettleP95Ms = 10
	policy.Budgets.TableInteractionP95Ms = 10
	policy.Budgets.GovernedQueryP95Ms = 10
	policy.Budgets.RefreshP95Ms = 10
	policy.Budgets.ConcurrentQueryP95Ms = 10
	policy.Budgets.PeakResidentMemoryBytes = 1024
	policy.Budgets.CPUSecondsMax = 10
	policy.Budgets.TemporaryDiskGrowthBytesMax = 1024
	policy.Budgets.GoroutineGrowthMax = 1
	policy.Budgets.OpenConnectionsMax = 1
	policy.Comparison.MaxRegressionRatio = 1.25
	policy.Comparison.MinimumMeaningfulLatencyDeltaMs = 50
	return policy
}

func TestQualificationCleanupRunsInReverseAndJoinsErrors(t *testing.T) {
	var order []string
	cleanup := qualificationCleanup{}
	cleanup.Add(func(context.Context) error {
		order = append(order, "first")
		return errors.New("first failed")
	})
	cleanup.Add(func(context.Context) error {
		order = append(order, "second")
		return errors.New("second failed")
	})

	err := cleanup.Run(context.Background())
	if strings.Join(order, ",") != "second,first" {
		t.Fatalf("cleanup order = %v", order)
	}
	if err == nil || !strings.Contains(err.Error(), "first failed") || !strings.Contains(err.Error(), "second failed") {
		t.Fatalf("cleanup error = %v", err)
	}
}

func TestQualificationPhaseTrackerRecordsTypedFailureAndDuration(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tracker := newQualificationPhaseTracker(func() time.Time { return now })
	phaseContext := tracker.Begin(t.Context(), "protected publish", time.Minute)
	now = now.Add(1500 * time.Millisecond)
	err := tracker.Finish(errors.New("approval denied"))
	var phaseErr *qualificationPhaseError
	if !errors.As(err, &phaseErr) {
		t.Fatalf("phase error = %T %v", err, err)
	}
	if phaseErr.Code != "PROTECTED_PUBLISH_FAILED" {
		t.Fatalf("failure code = %q", phaseErr.Code)
	}
	if phaseContext.Err() != context.Canceled {
		t.Fatalf("finished phase context error = %v", phaseContext.Err())
	}
	evidence := tracker.Evidence()
	if len(evidence) != 1 ||
		evidence[0].Result != "failure" ||
		evidence[0].DurationMillis != 1500 ||
		evidence[0].TimeoutSeconds != 60 ||
		!evidence[0].CleanupGuaranteed {
		t.Fatalf("phase evidence = %+v", evidence)
	}
}

func TestQualificationPhaseTrackerClassifiesTimeout(t *testing.T) {
	now := time.Now()
	tracker := newQualificationPhaseTracker(func() time.Time { return now })
	ctx := tracker.Begin(t.Context(), "browser journey", time.Nanosecond)
	<-ctx.Done()
	err := tracker.Finish(ctx.Err())
	var phaseErr *qualificationPhaseError
	if !errors.As(err, &phaseErr) ||
		phaseErr.Code != qualificationFailureTimeout {
		t.Fatalf("timeout error = %#v", err)
	}
}

type recordingQualificationExecutor struct {
	requests []qualificationCommandRequest
	output   []byte
	err      error
}

func (e *recordingQualificationExecutor) Execute(
	_ context.Context,
	request qualificationCommandRequest,
) ([]byte, error) {
	request.Arguments = append([]string(nil), request.Arguments...)
	request.Environment = append([]string(nil), request.Environment...)
	e.requests = append(e.requests, request)
	return append([]byte(nil), e.output...), e.err
}

func TestQualificationDockerExecutorPreservesExactArgumentsWithoutShell(t *testing.T) {
	root := t.TempDir()
	executor := &recordingQualificationExecutor{output: []byte("ok")}
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe",
		qualificationExecutor: executor,
	})
	require.NoError(t, err)
	stdin := strings.NewReader("payload")
	arguments := []string{
		"run",
		"--env", "LITERAL=$(touch /tmp/must-not-run)",
		"--name", "name with spaces",
		"image@sha256:" + strings.Repeat("a", 64),
	}
	output, err := controller.qualificationDocker(
		t.Context(),
		stdin,
		arguments...,
	)
	require.NoError(t, err)
	if string(output) != "ok" || len(executor.requests) != 1 {
		t.Fatalf("execution = %q, requests %d", output, len(executor.requests))
	}
	request := executor.requests[0]
	if request.Executable != "docker-probe" ||
		request.Directory != root ||
		!slices.Equal(request.Arguments, arguments) ||
		request.Stdin != stdin {
		t.Fatalf("request = %#v", request)
	}
}

func TestQualificationRegistryPushRetriesTransientStartupFailure(t *testing.T) {
	attempts := 0
	output, err := retryQualificationRegistryPush(
		t.Context(),
		3,
		0,
		func() ([]byte, error) {
			attempts++
			if attempts < 3 {
				return []byte("registry returned EOF"), errors.New("push failed")
			}
			return []byte("digest: sha256:" + strings.Repeat("a", 64)), nil
		},
	)
	require.NoError(t, err)
	if attempts != 3 || !bytes.Contains(output, []byte("digest: sha256:")) {
		t.Fatalf("attempts = %d, output = %q", attempts, output)
	}
}

func TestQualificationRegistryPushStopsAfterBoundedAttempts(t *testing.T) {
	attempts := 0
	_, err := retryQualificationRegistryPush(
		t.Context(),
		3,
		0,
		func() ([]byte, error) {
			attempts++
			return nil, errors.New("push failed")
		},
	)
	if err == nil || attempts != 3 {
		t.Fatalf("attempts = %d, error = %v", attempts, err)
	}
}

func TestQualificationComposeBuildsExactDockerArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, deploymentEnvName),
		[]byte("COMPOSE_HTTPS=1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	executor := &recordingQualificationExecutor{}
	controller, err := New(Options{
		Root: root, DockerBin: "docker",
		qualificationExecutor: executor,
	})
	require.NoError(t, err)
	if _, err := controller.qualificationCompose(
		t.Context(),
		root,
		"up",
		"--detach",
	); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"compose",
		"--project-directory", root,
		"--env-file", filepath.Join(root, deploymentEnvName),
		"--file", filepath.Join(root, "compose.yaml"),
		"--file", filepath.Join(root, "compose.https.yaml"),
		"up",
		"--detach",
	}
	if len(executor.requests) != 1 ||
		!slices.Equal(executor.requests[0].Arguments, want) {
		t.Fatalf("Compose arguments = %v, want %v", executor.requests, want)
	}
}

func TestParseQualificationPoolBootstrapResult(t *testing.T) {
	poolID := "sha256:" + strings.Repeat("a", 64)
	compatibility := "sha256:" + strings.Repeat("b", 64)
	gotPool, gotCompatibility, err := parseQualificationPoolBootstrapResult([]byte(
		"pool_id: " + poolID + "\n" +
			"compatibility_digest: " + compatibility + "\n" +
			"evidence_digest: sha256:" + strings.Repeat("c", 64) + "\n" +
			"conformance_version: lea-406/v1\n" +
			"applied: true\n",
	))
	require.NoError(t, err)
	if gotPool != poolID || gotCompatibility != compatibility {
		t.Fatalf("parsed bootstrap = %q/%q", gotPool, gotCompatibility)
	}
	for _, invalid := range [][]byte{
		[]byte("pool_id: " + poolID + "\ncompatibility_digest: " + compatibility + "\napplied: false\n"),
		[]byte("pool_id: invalid\ncompatibility_digest: " + compatibility + "\napplied: true\n"),
		[]byte("pool_id: " + poolID + "\ncompatibility_digest: invalid\napplied: true\n"),
	} {
		if _, _, err := parseQualificationPoolBootstrapResult(invalid); err == nil {
			t.Fatalf("accepted invalid bootstrap output %q", invalid)
		}
	}
}

func TestQualificationDiskUsageExcludesTransientSQLiteSidecars(t *testing.T) {
	root := t.TempDir()
	executor := &recordingQualificationExecutor{output: []byte("39996109\t/var/lib/leapview\n")}
	controller, err := New(Options{
		Root: root, DockerBin: "docker",
		qualificationExecutor: executor,
	})
	require.NoError(t, err)

	got, err := controller.qualificationDiskUsage(
		t.Context(),
		"leapview-app",
		"performance disk",
	)
	require.NoError(t, err)
	wantArguments := []string{
		"exec",
		"leapview-app",
		"du",
		"-sb",
		"--exclude=*.db-wal",
		"--exclude=*.db-shm",
		"/var/lib/leapview",
	}
	if got != 39996109 || len(executor.requests) != 1 ||
		!slices.Equal(executor.requests[0].Arguments, wantArguments) {
		t.Fatalf(
			"disk usage = %d, request = %#v",
			got,
			executor.requests,
		)
	}
}

func TestQualificationRecoveryDataIsReadableByHardenedRuntimeUser(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recovery")
	nested := filepath.Join(root, "project", "workspace.yaml")
	if err := os.MkdirAll(filepath.Dir(nested), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("title: Recovery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := makeQualificationContainerReadable(root); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		root:                 0o755,
		filepath.Dir(nested): 0o755,
		nested:               0o644,
	} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestQualificationRecoveryProjectDirCanBeTraversedByHardenedClient(t *testing.T) {
	projectDir, err := os.MkdirTemp(t.TempDir(), ".qualification-recovery-*")
	require.NoError(t, err)
	if err := makeQualificationProjectDirTraversable(projectDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(projectDir)
	require.NoError(t, err)
	if got, want := info.Mode().Perm(), os.FileMode(0o755); got != want {
		t.Fatalf("project directory mode = %o, want %o", got, want)
	}
}

func TestQualificationRecoveryUsesIsolatedCandidateKeys(t *testing.T) {
	keys := []string{
		qualificationRecoveryReleaseCandidateKey,
		qualificationRecoveryDeploymentCandidateKey,
	}
	if keys[0] == keys[1] {
		t.Fatalf("recovery candidate keys must be distinct: %v", keys)
	}
	for _, key := range keys {
		if key == "" || key == "default" {
			t.Fatalf("recovery candidate key is not isolated: %q", key)
		}
	}
	if qualificationRecoveryFullCPUs == "" || qualificationRecoveryFullCPUs == "0" {
		t.Fatalf(
			"recovery full-speed CPU setting does not remove the fault throttle: %q",
			qualificationRecoveryFullCPUs,
		)
	}
}

func TestQualificationRecoveryProjectNamesSatisfyResourceSchema(t *testing.T) {
	pattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	for _, name := range []string{qualificationRecoveryReleaseProjectName, qualificationRecoveryDeploymentProjectName} {
		if !pattern.MatchString(name) {
			t.Errorf("recovery project name %q does not satisfy the resource-name schema", name)
		}
	}
}

func TestQualificationRecoveryArmsActivationBarrierWithDockerCopy(t *testing.T) {
	root := t.TempDir()
	executor := &recordingQualificationExecutor{output: []byte("ok")}
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe", qualificationExecutor: executor,
	})
	require.NoError(t, err)
	workDir := t.TempDir()
	if err := controller.armQualificationActivationBarrier(t.Context(), "app-container", workDir); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 2 {
		t.Fatalf("docker requests = %d, want clear + cp", len(executor.requests))
	}
	if got, want := executor.requests[0].Arguments, []string{
		"exec", "app-container", "rm", "-f",
		qualificationActivationBarrierContainerPath(sealedcontrol.QualificationActivationBarrierArmedMarker),
		qualificationActivationBarrierContainerPath(sealedcontrol.QualificationActivationBarrierReachedMarker),
	}; !slices.Equal(got, want) {
		t.Fatalf("clear arguments = %v, want %v", got, want)
	}
	cp := executor.requests[1].Arguments
	if len(cp) != 3 || cp[0] != "cp" || cp[2] != "app-container:"+qualificationActivationBarrierContainerPath(sealedcontrol.QualificationActivationBarrierArmedMarker) {
		t.Fatalf("arm copy arguments = %v", cp)
	}
	if contents, err := os.ReadFile(cp[1]); err != nil || string(contents) != "qualification-recovery\n" {
		t.Fatalf("arm marker contents = %q, err = %v", contents, err)
	}
}

func TestQualificationRunningCommandCanBeCheckedAndStoppedOnce(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "running-command-*.log")
	require.NoError(t, err)
	command := exec.Command("sh", "-c", "sleep 10")
	require.NoError(t, command.Start())
	running := newQualificationRunningCommand(command, output)
	if !running.Running() {
		t.Fatal("newly started qualification command is not running")
	}
	if err := running.Stop(); err != nil {
		t.Fatal(err)
	}
	if running.Running() {
		t.Fatal("stopped qualification command still reports running")
	}
	if err := running.Stop(); err != nil {
		t.Fatalf("second Stop() = %v, want idempotent success", err)
	}
}

func TestQualificationRecoveryClientUsesPublicTarget(t *testing.T) {
	got := qualificationClientExecArguments(
		"recovery-client",
		"publisher-secret",
		"leapview", "dev",
	)
	want := []string{
		"exec",
		"--env", "LEAPVIEW_API_TOKEN=publisher-secret",
		"--env", "LEAPVIEW_TARGET=https://localhost",
		"--env", "LEAPVIEW_HOME=/client-home",
		"recovery-client",
		"leapview", "dev",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("client arguments = %v, want %v", got, want)
	}
}

func TestQualificationClientParsesTypedCLIResults(t *testing.T) {
	candidate, err := parseQualificationCandidate(fmt.Sprintf(
		`{"schemaVersion":1,"candidateId":"cand_1","revision":7,"targetId":"target_1",`+
			`"principalId":"principal_1","artifactDigest":"sha256:%s","provenanceDigest":"sha256:%s",`+
			`"planId":"plan_1","planDigest":"sha256:%s","previewUrl":"https://localhost/candidates/cand_1"}`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		strings.Repeat("e", 64),
	), "sha256:"+strings.Repeat("c", 64))
	require.NoError(t, err)
	if candidate.ID != "cand_1" || candidate.Revision != 7 ||
		candidate.TargetID != "target_1" || candidate.PrincipalID != "principal_1" {
		t.Fatalf("candidate = %+v", candidate)
	}

	publication, err := parseQualificationPublication(fmt.Sprintf(
		`{"schemaVersion":1,"publicationId":"publication_1","generationId":"generation_1",`+
			`"planId":"plan_1","planDigest":"sha256:%s","status":"pending","candidateId":"cand_1"}`,
		strings.Repeat("e", 64),
	), candidate)
	require.NoError(t, err)
	if publication.DeploymentID != "publication_1" ||
		publication.CandidateID != candidate.ID ||
		publication.ReleaseDigest != candidate.ProvenanceDigest {
		t.Fatalf("publication = %+v", publication)
	}
}

func TestQualificationBootstrapCandidateAllowsMissingDeliveryPlan(t *testing.T) {
	output := fmt.Sprintf(
		`{"schemaVersion":1,"candidateId":"cand_bootstrap","revision":7,"targetId":"target_1",`+
			`"principalId":"principal_1","artifactDigest":"sha256:%s","provenanceDigest":"sha256:%s",`+
			`"previewUrl":"https://localhost/candidates/cand_bootstrap"}`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	if _, err := parseQualificationCandidate(output, ""); err == nil {
		t.Fatal("normal candidate parser accepted a missing delivery plan")
	}
	candidate, err := parseQualificationCandidateBootstrap(output, "")
	require.NoError(t, err)
	if candidate.ID != "cand_bootstrap" || candidate.PlanID != "" || candidate.PlanDigest != "" {
		t.Fatalf("bootstrap candidate = %+v", candidate)
	}
}

type qualificationTestWriteCloser struct{ bytes.Buffer }

func (*qualificationTestWriteCloser) Close() error { return nil }

type qualificationTestRoundTrip struct {
	response io.Reader
	request  chan struct{}
	once     sync.Once
}

func newQualificationTestRoundTrip(response string) *qualificationTestRoundTrip {
	return &qualificationTestRoundTrip{
		response: strings.NewReader(response),
		request:  make(chan struct{}),
	}
}

func (t *qualificationTestRoundTrip) Read(contents []byte) (int, error) {
	<-t.request
	return t.response.Read(contents)
}

func (t *qualificationTestRoundTrip) Write(contents []byte) (int, error) {
	t.once.Do(func() { close(t.request) })
	return len(contents), nil
}

func (*qualificationTestRoundTrip) Close() error { return nil }

func TestQualificationJSONWorkerRejectsMalformedAndMismatchedResponses(t *testing.T) {
	for name, response := range map[string]struct {
		response string
		wanted   string
	}{
		"malformed": {
			"not-json\n",
			"invalid request",
		},
		"mismatched": {
			`{"jsonrpc":"2.0","id":42,"result":{}}` + "\n",
			"context canceled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			transport := newQualificationTestRoundTrip(response.response)
			worker := &qualificationJSONWorker{
				stderr: &boundedQualificationBuffer{maxBytes: 1024},
			}
			worker.client = jrpc2.NewClient(
				newQualificationRPCChannel(transport, transport),
				&jrpc2.ClientOptions{
					OnNotify:   worker.handleNotification,
					OnCallback: worker.handleCallback,
				},
			)
			err := worker.Call("inspect", nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), response.wanted) {
				t.Fatalf("worker error = %v", err)
			}
		})
	}
}

func TestQualificationJSONWorkerSupportsResultsEventsAndCancellation(t *testing.T) {
	clientChannel, serverChannel := channel.Direct()
	worker := &qualificationJSONWorker{
		stderr: &boundedQualificationBuffer{maxBytes: 1024},
	}
	worker.client = jrpc2.NewClient(
		clientChannel,
		&jrpc2.ClientOptions{
			OnNotify:   worker.handleNotification,
			OnCallback: worker.handleCallback,
		},
	)
	server := jrpc2.NewServer(handler.Map{
		"inspect": handler.New(func(ctx context.Context) (map[string]string, error) {
			if _, err := jrpc2.ServerFromContext(ctx).Callback(
				ctx,
				"progress",
				map[string]int{"percent": 50},
			); err != nil {
				return nil, err
			}
			return map[string]string{"status": "ready"}, nil
		}),
		"block": handler.New(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	}, &jrpc2.ServerOptions{AllowPush: true}).Start(serverChannel)
	t.Cleanup(func() {
		_ = worker.client.Close()
		server.Stop()
		_ = server.Wait()
	})

	var result map[string]string
	var event json.RawMessage
	err := worker.CallContext(
		t.Context(),
		"inspect",
		nil,
		&result,
		func(name string, params json.RawMessage) error {
			if name != "progress" {
				return fmt.Errorf("unexpected event %q", name)
			}
			event = append(event[:0], params...)
			return nil
		},
	)
	if err != nil || result["status"] != "ready" ||
		!strings.Contains(string(event), `"percent":50`) {
		t.Fatalf("inspect result = %v, event = %s, error = %v", result, event, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err = worker.CallContext(ctx, "block", nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("block error = %v, want deadline exceeded", err)
	}
}

func TestQualificationRPCChannelRejectsOversizedMessages(t *testing.T) {
	oversized := strings.Repeat("x", qualificationRPCMaxMessageBytes+1) + "\n"
	channel := newQualificationRPCChannel(
		strings.NewReader(oversized),
		&qualificationTestWriteCloser{},
	)
	if _, err := channel.Recv(); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized receive error = %v", err)
	}
	if err := channel.Send(bytes.Repeat(
		[]byte("x"),
		qualificationRPCMaxMessageBytes+1,
	)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized send error = %v", err)
	}
}

func TestExpandQualificationCSVFlushesEveryGeneratedRow(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.csv")
	destination := filepath.Join(root, "expanded.csv")
	if err := os.WriteFile(source, []byte("id,value\norder,42\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := expandQualificationCSV(source, destination, 3); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(destination)
	require.NoError(t, err)
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)
	if got := len(rows); got != 4 {
		t.Fatalf("expanded row count = %d, want 4", got)
	}
	for index, want := range []string{"order-1", "order-2", "order-3"} {
		if got := rows[index+1][0]; got != want {
			t.Errorf("expanded row %d id = %q, want %q", index, got, want)
		}
	}
}

func TestQualificationURLPathEscapesOpaqueIdentifiers(t *testing.T) {
	if got, want := urlPath("project/with space"), "project%2Fwith%20space"; got != want {
		t.Fatalf("escaped path = %q, want %q", got, want)
	}
}

func TestQualificationRecoveryUsesCanonicalManagedConnectionID(t *testing.T) {
	if got, want := qualificationManagedConnectionPath("project:leapview-evaluation"), "/api/v1/projects/project:leapview-evaluation/connections/connection:sample"; got != want {
		t.Fatalf("managed connection path = %q, want %q", got, want)
	}
	if got, want := qualificationRefreshPipelineID, "pipeline:evaluation-refresh"; got != want {
		t.Fatalf("refresh pipeline ID = %q, want %q", got, want)
	}
}

func TestQualificationRunningWaitRejectsAlreadyTerminalOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"succeeded"}`)
	}))
	defer server.Close()

	err := waitForQualificationStatus(t.Context(), server.Client(), server.URL, "token", "running")
	if err == nil || !strings.Contains(err.Error(), `terminal status "succeeded" before running was observed`) {
		t.Fatalf("running wait error = %v", err)
	}
}

func TestQualificationRunningWaitAcceptsCanonicalPreparedOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"prepared"}`)
	}))
	defer server.Close()

	if err := waitForQualificationStatus(t.Context(), server.Client(), server.URL, "token", "running"); err != nil {
		t.Fatalf("prepared operation was not treated as in flight: %v", err)
	}
}

func TestIgnoreQualificationNotFoundOnlySuppressesMissingDockerObjects(t *testing.T) {
	for _, message := range []string{
		"Error response from daemon: No such image",
		"Error response from daemon: No such container",
		"manifest not found",
	} {
		if err := ignoreQualificationNotFound(errors.New(message)); err != nil {
			t.Errorf("ignoreQualificationNotFound(%q) = %v", message, err)
		}
	}
	permissionErr := errors.New("permission denied")
	if err := ignoreQualificationNotFound(permissionErr); !errors.Is(err, permissionErr) {
		t.Fatalf("permission error = %v, want original", err)
	}
}

func TestVerifyQualificationChecksumsRejectsPathsOutsideRelease(t *testing.T) {
	root := t.TempDir()
	contents := []byte("release")
	if err := os.WriteFile(filepath.Join(root, "release.txt"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(contents)
	if err := os.WriteFile(
		filepath.Join(root, "SHA256SUMS"),
		[]byte(fmt.Sprintf("%x  release.txt\n", checksum)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyQualificationChecksums(root); err != nil {
		t.Fatalf("verify valid release checksums: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "SHA256SUMS"),
		[]byte(fmt.Sprintf("%x  ../outside\n", checksum)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyQualificationChecksums(root); err == nil ||
		!strings.Contains(err.Error(), "escapes the release root") {
		t.Fatalf("path traversal error = %v", err)
	}
}

func TestQualificationReleaseIdentityRejectsUnknownProvenance(t *testing.T) {
	clean := false
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	valid := qualificationReleaseIdentity{
		Version: "1.0.0", Revision: strings.Repeat("b", 40), Image: image,
		Dirty: &clean, Development: &clean,
	}
	if _, err := valid.transitionIdentity(image, "linux/amd64"); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	for _, test := range []struct {
		name     string
		identity qualificationReleaseIdentity
	}{
		{name: "missing provenance", identity: qualificationReleaseIdentity{Version: valid.Version, Revision: valid.Revision, Image: image}},
		{name: "mismatched admitted image", identity: qualificationReleaseIdentity{Version: valid.Version, Revision: valid.Revision, Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64), Dirty: &clean, Development: &clean}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.identity.transitionIdentity(image, "linux/amd64"); err == nil {
				t.Fatal("unknown release provenance was accepted")
			}
		})
	}
}
