package composectl

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestQualificationHistoricalCandidatePlanDiagnostic reproduces the exact
// candidate CLI plan-validation path using the same NativeRequest constructor
// as the full fixture, but without creating a PostgreSQL instance or legacy
// publication. On failure it leaves a mode-0600 request JSON under /tmp for
// standalone inspection; it never writes qualification evidence.
func TestQualificationHistoricalCandidatePlanDiagnostic(t *testing.T) {
	if os.Getenv("LEAPVIEW_HISTORICAL_CANDIDATE_PLAN_DIAGNOSTIC") != "1" {
		t.Skip("set LEAPVIEW_HISTORICAL_CANDIDATE_PLAN_DIAGNOSTIC=1 to diagnose candidate plan validation")
	}
	image := strings.TrimSpace(os.Getenv(qualificationHistoricalDiagnosticImage))
	revision := strings.TrimSpace(os.Getenv(qualificationHistoricalDiagnosticRev))
	require.Regexp(t, qualificationHistoricalLocalImagePattern, image)
	require.Regexp(t, qualificationHistoricalRevisionPattern, revision)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)

	volumeName := "leapview-historical-plan-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	volumeOutput, err := exec.CommandContext(ctx, "docker", "volume", "create", volumeName).CombinedOutput()
	require.NoError(t, err, "create a private disposable home volume: %s", qualificationHistoricalDiagnosticTail(volumeOutput, 2048))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = exec.CommandContext(cleanupCtx, "docker", "volume", "rm", volumeName).Run()
	})

	options := qualificationHistoricalTransitionOptions{CandidateImage: image, CandidateRevision: revision}
	requestImage, admission := qualificationHistoricalCandidateAdmission(t, options)
	compatibility := inspectQualificationHistoricalTransition(t, ctx, repoRoot, revision)
	fixture := qualificationHistoricalPredecessorFixture{
		ComposeProject: "historical-plan-diagnostic",
		Topology:       &qualificationNativePostgresTopology{ContainerName: "historical-postgres", ComposeNetwork: "bridge"},
		Seed: qualificationHistoricalSeed{
			TargetID: "lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProjectID: qualificationHistoricalProjectID,
			SourceRoot:           historicalQualificationCFOSource(t, repoRoot),
			PublisherPrincipalID: "018f3f83-7c00-7000-8000-000000000001",
			ReviewerPrincipalID:  "018f3f83-7c00-7000-8000-000000000002",
			ViewerPrincipalID:    "018f3f83-7c00-7000-8000-000000000003",
			DashboardID:          "dashboard:cfo-command-center",
		},
	}
	inventory := adminpostgres.AccessTransitionInventory{
		TargetID: fixture.Seed.TargetID, Environment: "production", ProjectID: fixture.Seed.ProjectID,
		PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64),
		ServingGenerationID:         "018f3f83-7c00-7000-8000-000000000004",
		ServingPolicySnapshotDigest: "sha256:" + strings.Repeat("b", 64),
	}
	runtime := newTestcontainersQualificationRuntime()
	utility := startQualificationHistoricalTransitionUtility(t, ctx, runtime, "bridge", volumeName,
		image, requestImage, false, map[string]string{}, "historical-plan-diagnostic-"+volumeName)
	hostnameOutput, err := utility.Exec(ctx, nil, "hostname")
	require.NoError(t, err)
	request := qualificationHistoricalNativeRequest(t, fixture, options, inventory, compatibility,
		requestImage, admission, strings.TrimSpace(string(hostnameOutput)))
	requestBytes, err := json.Marshal(request)
	require.NoError(t, err)

	debugDirectory, err := os.MkdirTemp("/tmp", "leapview-historical-candidate-plan-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(debugDirectory, 0o700))
	debugPath := filepath.Join(debugDirectory, "request.json")
	require.NoError(t, os.WriteFile(debugPath, requestBytes, 0o600))
	copyQualificationHistoricalPrivateFile(t, ctx, utility, "/tmp/qualification-request.json", requestBytes)
	hostCLI := "/usr/local/share/leapview/deployment/leapviewctl"
	helpOutput, err := utility.Exec(ctx, nil, hostCLI, "host", "upgrade", "--help")
	require.NoError(t, err, "candidate host CLI must expose host upgrade commands: %s", qualificationHistoricalCommandDiagnostic(helpOutput, err))
	require.Contains(t, string(helpOutput), "plan", "candidate host CLI must expose plan action")
	output, err := utility.Exec(ctx, nil, hostCLI, "host", "upgrade", "plan", "--request", "/tmp/qualification-request.json")
	if err != nil {
		t.Logf("private request retained at %s", debugPath)
		t.Fatalf("candidate plan validation failed: %s", qualificationHistoricalCommandDiagnostic(output, err))
	}
	require.NoError(t, os.Remove(debugPath))
	require.NoError(t, os.Remove(debugDirectory))
	t.Logf("candidate plan validation passed for the synthetic exact-request reproduction (%s)", strings.TrimSpace(string(output)))
}
