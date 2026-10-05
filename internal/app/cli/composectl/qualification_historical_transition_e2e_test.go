package composectl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/platform/releasecontract"
	"github.com/stretchr/testify/require"
	testcontainersnetwork "github.com/testcontainers/testcontainers-go/network"
)

const (
	qualificationHistoricalProjectID         = "project:historical-transition"
	qualificationHistoricalInternal          = "http://localhost:8080"
	qualificationHistoricalOrigin            = "https://demo.leapview.dev"
	qualificationHistoricalBrowserPassword   = "HistoricallyQualifiedViewerPassword!9"
	qualificationHistoricalDuckLake154SHA256 = "00f72402c9c5d1f69c3329f38837f4abd100cddb7c69e76650f46bf35a17babe"
	qualificationHistoricalDiagnosticEnv     = "LEAPVIEW_HISTORICAL_TRANSITION_DIAGNOSTIC"
	qualificationHistoricalDiagnosticImage   = "LEAPVIEW_HISTORICAL_TRANSITION_DIAGNOSTIC_IMAGE"
	qualificationHistoricalDiagnosticRev     = "LEAPVIEW_HISTORICAL_TRANSITION_DIAGNOSTIC_REVISION"
	qualificationHistoricalStopGracePeriod   = 2 * time.Minute
	qualificationHistoricalStopWaitTimeout   = 3 * time.Minute
)

// TestQualificationHistoricalTransitionEndToEnd exercises the exact schema-32
// predecessor with real native PostgreSQL, seeded legacy principals, an
// administrator-seeded first serving snapshot, and the normal scoped
// publication client. The remaining candidate transition phases are kept in
// this required test so a passing lane always has one complete receipt.
func TestQualificationHistoricalTransitionEndToEnd(t *testing.T) {
	if os.Getenv(qualificationHistoricalTransitionRequiredEnv) != "1" {
		t.Skip("set LEAPVIEW_HISTORICAL_TRANSITION_REQUIRED=1 to run the schema-32 transition qualification")
	}
	options, err := readQualificationHistoricalTransitionOptions(environmentMap(os.Environ()))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 105*time.Minute)
	defer cancel()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)
	if err := validateHistoricalTransitionCandidate(ctx, options, repoRoot); err != nil {
		t.Fatal(err)
	}
	checks := runQualificationHistoricalTransitionScenario(t, ctx, repoRoot, options)
	require.NoError(t, writeQualificationHistoricalTransitionReceipt(options.EvidencePath, options.CandidateRevision,
		options.CandidateImage, options.CandidateRevision, checks))
}

// TestQualificationHistoricalTransitionDiagnostic exercises the same full
// historical scenario against an explicitly supplied local candidate image.
// It never checks validator identity or writes qualification evidence, so it
// cannot produce a deployable transition receipt.
func TestQualificationHistoricalTransitionDiagnostic(t *testing.T) {
	if os.Getenv(qualificationHistoricalDiagnosticEnv) != "1" {
		t.Skip("set LEAPVIEW_HISTORICAL_TRANSITION_DIAGNOSTIC=1 to run the receipt-free full scenario")
	}
	if strings.TrimSpace(os.Getenv(qualificationHistoricalTransitionFinalEnv)) == "1" {
		t.Fatal("receipt-free historical diagnostic refuses final-artifact mode")
	}
	options := qualificationHistoricalTransitionOptions{
		CandidateImage:    strings.TrimSpace(os.Getenv(qualificationHistoricalDiagnosticImage)),
		CandidateRevision: strings.TrimSpace(os.Getenv(qualificationHistoricalDiagnosticRev)),
	}
	require.Regexp(t, qualificationHistoricalLocalImagePattern, options.CandidateImage,
		"receipt-free diagnostic accepts only an exact local immutable image ID")
	require.Regexp(t, qualificationHistoricalRevisionPattern, options.CandidateRevision,
		"receipt-free diagnostic requires an explicit full source revision")
	ctx, cancel := context.WithTimeout(t.Context(), 105*time.Minute)
	defer cancel()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)
	require.NoError(t, validateHistoricalTransitionImageIdentity(ctx, options),
		"the local image must contain the explicitly requested clean source revision")
	checks := runQualificationHistoricalTransitionScenario(t, ctx, repoRoot, options)
	require.True(t, qualificationHistoricalTransitionChecksPassed(checks))
	t.Logf("receipt-free historical candidate diagnostic passed for image %s at revision %s; no transition receipt was written",
		options.CandidateImage, options.CandidateRevision)
}

func runQualificationHistoricalTransitionScenario(
	t *testing.T,
	ctx context.Context,
	repoRoot string,
	options qualificationHistoricalTransitionOptions,
) qualificationHistoricalTransitionChecks {
	t.Helper()
	fixture := startQualificationHistoricalPredecessorFixture(t, ctx, repoRoot)
	require.Equal(t, qualificationHistoricalPredecessorRevision, fixture.LegacyPublication.RuntimeRevision)
	candidate := runQualificationHistoricalCandidateTransition(t, ctx, repoRoot, options, fixture)
	intent := candidate.Request.AccessTransition
	require.NotNil(t, intent, "the native maintenance request must carry the actual typed access intent")
	require.Equal(t, fixture.Seed.TargetID, intent.TargetID)
	require.Equal(t, fixture.Seed.Environment, intent.Environment)
	require.Equal(t, fixture.Seed.ProjectID, intent.ProjectID)
	require.Equal(t, candidate.Inventory.PolicyRevision, intent.ExpectedPolicyRevision)
	require.Equal(t, candidate.Inventory.PolicyDigest, intent.ExpectedPolicyDigest)
	require.Equal(t, candidate.Inventory.ServingGenerationID, intent.ExpectedServingGeneration)
	require.Equal(t, candidate.Inventory.ServingPolicySnapshotDigest, intent.ExpectedServingPolicyDigest)
	require.Equal(t, fixture.Seed.PublisherPrincipalID, intent.PublisherPrincipalID)
	require.Equal(t, fixture.Seed.ReviewerPrincipalID, intent.ReviewerPrincipalID)
	require.NotEqual(t, intent.PublisherPrincipalID, intent.ReviewerPrincipalID)
	require.Equal(t, releasecontract.TypedPermissions, candidate.Request.Plan.SourceAfter.PermissionProfile,
		"the admitted candidate plan must move the project to the current typed profile")
	require.Len(t, intent.RoleBindings, 2, "only distinct operator and approver roles are required")
	roles := map[string]string{}
	for _, binding := range intent.RoleBindings {
		roles[binding.Principal] = binding.Role
	}
	require.Equal(t, map[string]string{
		fixture.Seed.PublisherPrincipalID: "release_operator",
		fixture.Seed.ReviewerPrincipalID:  "release_approver",
	}, roles)
	expectedGrants := qualificationHistoricalTransitionGrants(t, fixture.Seed)
	require.Len(t, intent.Grants, len(expectedGrants), "typed access must contain only exact grants for the retained CFO dependency graph and viewer policy")
	require.Equal(t, expectedGrants, intent.Grants,
		"typed access must preserve exact CFO delivery dependencies, publisher/reviewer separation, and viewer permissions")
	require.Equal(t, http.StatusOK, candidate.PostTransitionDashboardStatus)
	require.Equal(t, http.StatusForbidden, candidate.DeniedSourcesStatus)
	require.Equal(t, fixture.Seed.PublisherPrincipalID, candidate.ApprovalRequestedBy)
	require.Equal(t, fixture.Seed.ReviewerPrincipalID, candidate.ApprovalDecidedBy)
	require.Equal(t, "approved", candidate.ApprovalDecision)
	require.NotEqual(t, candidate.ApprovalRequestedBy, candidate.ApprovalDecidedBy)
	require.Equal(t, "workload", candidate.ApprovalRequestCredential)
	require.Equal(t, "workload", candidate.ApprovalDecisionCredential)

	// Recreate the candidate service from the same immutable image and only then
	// run the ordinary publication adapter against the already migrated project.
	// This proves subsequent replacement compatibility; it does not claim a
	// second image digest was tested.
	require.NoError(t, runQualificationHistoricalBrowserValidation(ctx, t, repoRoot, fixture.Seed, candidate.Proxy),
		"the newly activated typed generation must render the real CFO query proof")
	require.NoError(t, verifyQualificationHistoricalTransitionAuthorization(ctx, candidate, fixture.Seed),
		"typed workload roles and the real viewer must stay within their exact authority")
	require.NoError(t, stopQualificationHistoricalContainerGracefully(ctx, candidate.Candidate),
		"gracefully stop and verify the candidate before starting its replacement")
	_, err := candidate.Candidate.Remove(ctx)
	require.NoError(t, err, "remove the stopped candidate before starting its replacement")
	replacementRuntime := newTestcontainersQualificationRuntime()
	replacementCandidate, replacementEndpoint := startQualificationHistoricalServer(t, ctx, replacementRuntime,
		fixture.Network, fixture.StateVolume, options.CandidateImage, fixture.ApplicationEnv,
		fixture.ComposeProject+"-candidate-redeployment")
	replacementProxy := startQualificationHistoricalTransport(t, ctx, repoRoot, replacementEndpoint)
	replacementViewer := newQualificationHistoricalBrowser(t, replacementProxy.ProxyURL, replacementProxy.CACert)
	require.NoError(t, replacementViewer.loginExisting(ctx, fixture.Seed.ViewerEmail, qualificationHistoricalBrowserPassword))
	replacementSeed := fixture.Seed
	replacementSeed.SourceRoot = historicalQualificationCFOSourceAt(t, repoRoot, options.CandidateRevision)
	nextPublication, err := runQualificationHistoricalPublication(ctx, t, repoRoot, replacementSeed,
		replacementProxy, options.CandidateRevision, releasecontract.TypedPermissions)
	if err != nil {
		t.Logf("replacement publication candidate HTTP failures (method/path/status only):\n%s",
			replacementProxy.target.failureTail(16))
		serverLogs, logsErr := replacementCandidate.Logs(ctx, 128)
		t.Logf("replacement publication candidate server diagnostics: %s",
			qualificationHistoricalCandidateFailureLogs(serverLogs, logsErr,
				fixture.Seed.PublisherClientSecret, fixture.Seed.ReleaseClientSecret, fixture.Seed.ViewerPassword))
	}
	require.NoError(t, err, "the normal publication adapter must work after same-image candidate replacement")
	require.Equal(t, options.CandidateRevision, nextPublication.RuntimeRevision)
	require.NotEqual(t, candidate.Transition.PublicationID, nextPublication.PublicationID,
		"the subsequent normal deployment must create its own publication")
	require.NotEqual(t, candidate.Transition.GenerationID, nextPublication.GenerationID,
		"the subsequent normal deployment must activate its own serving generation")
	require.NoError(t, replacementViewer.checkDashboard(ctx,
		qualificationHistoricalDashboardOverviewURL(fixture.Seed.DashboardID)))
	require.NoError(t, runQualificationHistoricalBrowserValidation(ctx, t, repoRoot, replacementSeed, replacementProxy),
		"the subsequent normal deployment must retain the real CFO query proof")

	checks := qualificationHistoricalTransitionChecks{
		LegacyPublication:       fixture.LegacyPublication.PublicationID != "",
		LegacyViewer:            true, // The predecessor fixture returned only after authenticated page and query proof.
		TypedPolicyCaptured:     true, // Exact intent, candidate typed profile, active dashboard and denied catalog are asserted above.
		IndependentApproval:     candidate.ApprovalRequestedBy == fixture.Seed.PublisherPrincipalID && candidate.ApprovalDecidedBy == fixture.Seed.ReviewerPrincipalID && candidate.ApprovalDecision == "approved",
		PublisherNoSelfApproval: candidate.ApprovalRequestedBy != candidate.ApprovalDecidedBy,
		ViewerLeastPrivilege:    candidate.PostTransitionDashboardStatus == http.StatusOK && candidate.DeniedSourcesStatus == http.StatusForbidden,
		RealPublicationAdapter:  nextPublication.PublicationID != "" && nextPublication.GenerationID != "",
		SubsequentDeploy:        nextPublication.RuntimeRevision == options.CandidateRevision && nextPublication.PublicationID != candidate.Transition.PublicationID,
	}
	return checks
}

func qualificationHistoricalCandidateFailureLogs(contents []byte, err error, secrets ...string) string {
	if err != nil {
		contents = append(append([]byte(nil), contents...), []byte("\nlog retrieval failed: "+err.Error())...)
	}
	redacted := redactQualificationBytes(contents)
	filtered := make([]string, 0, 24)
	for _, line := range strings.Split(string(redacted), "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "warn") || strings.Contains(lower, "fail") ||
			strings.Contains(lower, "managed-data") || strings.Contains(lower, "panic") || strings.Contains(lower, "exception") {
			for _, secret := range secrets {
				if secret != "" {
					line = strings.ReplaceAll(line, secret, "[REDACTED]")
				}
			}
			filtered = append(filtered, line)
			if len(filtered) > 24 {
				filtered = filtered[len(filtered)-24:]
			}
		}
	}
	if len(filtered) == 0 {
		return "no matching error, warning, failure, or managed-data log lines"
	}
	return qualificationHistoricalDiagnosticTail([]byte(strings.Join(filtered, "\n")), 8<<10)
}

// TestQualificationHistoricalPredecessorFixture is an opt-in diagnostic for
// the pinned old runtime. It exercises the exact predecessor setup and real
// legacy publication without validating or writing a candidate receipt.
func TestQualificationHistoricalPredecessorFixture(t *testing.T) {
	if os.Getenv("LEAPVIEW_HISTORICAL_PREDECESSOR_FIXTURE") != "1" {
		t.Skip("set LEAPVIEW_HISTORICAL_PREDECESSOR_FIXTURE=1 to exercise the pinned predecessor fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)
	fixture := startQualificationHistoricalPredecessorFixture(t, ctx, repoRoot)
	require.NotEmpty(t, fixture.LegacyPublication.GenerationID)
	require.NoError(t, fixture.Viewer.checkDashboard(ctx, qualificationHistoricalDashboardOverviewURL(fixture.Seed.DashboardID)))
}

type qualificationHistoricalPredecessorFixture struct {
	Seed              qualificationHistoricalSeed
	LegacyPublication qualificationHistoricalPublicationResult
	LegacyClientRoot  string
	Viewer            *qualificationHistoricalBrowser
	Proxy             *qualificationHistoricalTransport
	Endpoint          string
	Network           string
	Predecessor       qualificationContainer
	StateVolume       string
	ComposeProject    string
	Topology          *qualificationNativePostgresTopology
	ApplicationEnv    map[string]string
}

func startQualificationHistoricalPredecessorFixture(t *testing.T, ctx context.Context, repoRoot string) qualificationHistoricalPredecessorFixture {
	t.Helper()
	// Keep the application and database on an isolated Docker network so the
	// candidate cannot reach public services during qualification. The host
	// reaches the app through its private network address and a loopback-only
	// TLS relay; the application publishes no Docker ports.
	network, err := testcontainersnetwork.New(ctx, testcontainersnetwork.WithInternal())
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_ = network.Remove(cleanupCtx)
	})

	runtime := newTestcontainersQualificationRuntime()
	composeProject := fmt.Sprintf("lvht-%x-%x", time.Now().UTC().UnixNano(), os.Getpid())
	stateVolume := composeProject + "_leapview-state"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if output, removeErr := exec.CommandContext(cleanupCtx, "docker", "volume", "rm", "--force", stateVolume).CombinedOutput(); removeErr != nil {
			t.Logf("remove owned historical state volume %s: %v (%s)", stateVolume, removeErr, strings.TrimSpace(string(output)))
		}
	})
	oldImage := qualificationHistoricalPredecessorImage
	// Let the predecessor image initialize the shared state volume first. The
	// PostgreSQL sidecar copies its CA into that volume, while the application
	// image's original ownership keeps the non-root LeapView process able to
	// create its private home files.
	volumeInitializer := startQualificationHistoricalUtility(t, ctx, runtime, network.Name, stateVolume, oldImage,
		map[string]string{"LEAPVIEW_HOME": "/var/lib/leapview/home", "HOME": "/var/lib/leapview/home"}, composeProject+"-state-volume-init")
	_, err = volumeInitializer.Exec(ctx, nil, "mkdir", "-p", "/var/lib/leapview/home/.duckdb/extensions")
	require.NoError(t, err, "prepare the shared, private DuckDB extension cache")
	topology, err := newQualificationNativePostgresTopology(ctx, runtime, qualificationNativePostgresTopologyOptions{
		ComposeProject: composeProject,
		ComposeNetwork: network.Name,
		BundleRoot:     repoRoot,
		InitScript:     filepath.Join(repoRoot, "deploy", "postgres", "init.sh"),
		ContainerName:  composeProject + "-postgres",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_ = topology.Remove(cleanupCtx)
	})
	_, _ = volumeInitializer.Remove(ctx)

	baseEnvironment, err := qualificationHistoricalApplicationEnvironment(topology)
	require.NoError(t, err)
	// The predecessor's DuckDB 1.5.4 image can fetch its signed ducklake
	// extension on first use. Stage that exact engine extension into the shared
	// home before the application network is isolated; this short-lived setup
	// utility receives no database, project, or user credentials. Every app and
	// PostgreSQL container below stays on the internal-only network and must use
	// this local cache.
	extensionPreparationEnvironment := map[string]string{
		"LEAPVIEW_HOME": "/var/lib/leapview/home",
		"HOME":          "/var/lib/leapview/home",
	}
	poolUtility := startQualificationHistoricalUtility(t, ctx, runtime, "", stateVolume, oldImage, extensionPreparationEnvironment, composeProject+"-pool-qualify")
	poolOutput, err := poolUtility.Exec(ctx, nil, "leapview", "admin", "delivery", "pool", "qualify")
	require.NoError(t, err, "pinned predecessor pool qualification output: %s", qualificationHistoricalDiagnosticTail(poolOutput, 4096))
	extensionCacheEvidence, err := poolUtility.Exec(ctx, nil, "sh", "-ec", "test -s /var/lib/leapview/home/.duckdb/extensions/v1.5.4/linux_amd64/ducklake.duckdb_extension && sha256sum /var/lib/leapview/home/.duckdb/extensions/v1.5.4/linux_amd64/ducklake.duckdb_extension")
	require.NoError(t, err, "the pinned predecessor's DuckLake extension must be staged in the shared cache")
	extensionCacheFields := strings.Fields(string(extensionCacheEvidence))
	require.GreaterOrEqual(t, len(extensionCacheFields), 2)
	require.Equal(t, qualificationHistoricalDuckLake154SHA256, extensionCacheFields[0],
		"the offline fixture must use the exact signed DuckDB 1.5.4 ducklake extension previously admitted by this pinned predecessor")
	t.Logf("offline predecessor DuckDB extension cache evidence: %s", strings.TrimSpace(string(extensionCacheEvidence)))
	poolEnvelope, err := adminoffline.UnmarshalQualificationPoolArtifacts(poolOutput)
	require.NoError(t, err)
	poolDir := filepath.Join(t.TempDir(), "physical-pool")
	require.NoError(t, os.MkdirAll(poolDir, 0o700))
	poolArtifacts, err := qualificationNativePhysicalPoolArtifactsFromEnvelope(poolDir, poolEnvelope)
	require.NoError(t, err)
	require.NoError(t, writeQualificationNativePhysicalPoolArtifacts(poolArtifacts))
	_, err = poolUtility.Remove(ctx)
	require.NoError(t, err)

	applicationEnvironment := cloneQualificationEnvironment(baseEnvironment)
	applicationEnvironment["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"] = poolArtifacts.PoolID
	applicationEnvironment["LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST"] = poolArtifacts.CompatibilityDigest
	utility := startQualificationHistoricalUtility(t, ctx, runtime, network.Name, stateVolume, oldImage, applicationEnvironment, composeProject+"-predecessor-setup")
	copyQualificationHistoricalPoolFiles(t, ctx, utility, poolArtifacts)
	bootstrapDryRun := runQualificationHistoricalPoolBootstrap(t, ctx, utility, poolArtifacts, false, nil)
	require.NoError(t, verifyQualificationNativePhysicalPoolBootstrapResult(bootstrapDryRun, poolArtifacts, false))

	initial, err := initializeQualificationHistoricalRuntime(ctx, t, utility)
	require.NoError(t, err)
	operationEnvironment, err := qualificationNativePostgresOperationEnvironment(topology)
	require.NoError(t, err)
	bootstrapResult := runQualificationHistoricalPoolBootstrap(t, ctx, utility, poolArtifacts, true, operationEnvironment)
	require.NoError(t, verifyQualificationNativePhysicalPoolBootstrapResult(bootstrapResult, poolArtifacts, true))
	// The utility owns the shared home and captured initialization credentials;
	// acknowledge them before starting the app, which holds the same native
	// home lock for its lifetime.
	_, err = utility.Exec(ctx, nil, "leapview", "admin", "initialize", "--acknowledge-credentials")
	require.NoError(t, err)

	predecessor, endpoint := startQualificationHistoricalServer(t, ctx, runtime, network.Name, stateVolume, oldImage, applicationEnvironment, composeProject+"-predecessor")
	seedSource := historicalQualificationCFOSource(t, repoRoot)
	seed, err := seedQualificationHistoricalRuntimeWithCredentialsSkippingAcknowledgement(ctx, t, predecessor, "http://"+endpoint, qualificationHistoricalProjectID, seedSource, initial)
	require.NoError(t, err)
	predecessorRevision := qualificationHistoricalContainerRevision(t, ctx, predecessor)
	require.Equalf(t, qualificationHistoricalPredecessorRevision, predecessorRevision, "pinned predecessor version output: %s", qualificationHistoricalContainerVersionOutput(t, ctx, predecessor))
	proxy := startQualificationHistoricalTransport(t, ctx, repoRoot, endpoint)
	viewer := newQualificationHistoricalBrowser(t, proxy.ProxyURL, proxy.CACert)
	require.NoError(t, viewer.login(ctx, seed.ViewerEmail, seed.ViewerPassword))
	legacyClient := historicalQualificationClientTree(t, ctx, repoRoot, qualificationHistoricalPredecessorRevision, predecessor)
	bootstrapPublication, err := runQualificationHistoricalBootstrapPublication(ctx, t, legacyClient, seed, initial, proxy)
	if err != nil {
		serverLogs, logsErr := predecessor.Logs(ctx, 120)
		t.Logf("predecessor diagnostics after initial publication: %s", qualificationHistoricalCandidateFailureLogs(
			serverLogs, logsErr, initial.PublisherToken, seed.PublisherClientSecret, seed.ReleaseClientSecret, seed.ViewerPassword))
		readyRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/readyz", nil)
		if requestErr == nil {
			readyRequest.Host = "demo.leapview.dev"
			readyClient := &http.Client{Timeout: 3 * time.Second}
			readyResponse, readyErr := readyClient.Do(readyRequest)
			if readyErr != nil {
				t.Logf("predecessor /readyz diagnostic failed: %v", readyErr)
			} else {
				readyBody, readErr := io.ReadAll(io.LimitReader(readyResponse.Body, 4097))
				_ = readyResponse.Body.Close()
				if len(readyBody) > 4096 {
					readyBody = readyBody[:4096]
				}
				if readErr != nil {
					t.Logf("predecessor /readyz diagnostic body read failed: %v", readErr)
				}
				t.Logf("predecessor /readyz diagnostic: status=%d body=%s", readyResponse.StatusCode,
					strings.TrimSpace(string(redactQualificationBytes(readyBody))))
			}
		} else {
			t.Logf("predecessor /readyz diagnostic request failed: %v", requestErr)
		}
	}
	require.NoError(t, err, "the predecessor's real initial administrator credential must establish the first serving snapshot")
	require.NotEmpty(t, bootstrapPublication.GenerationID, "predecessor bootstrap publication must activate a real serving generation")
	legacyReceipt, err := runQualificationHistoricalPublication(ctx, t, legacyClient.Root, seed, proxy, qualificationHistoricalPredecessorRevision, releasecontract.LegacyPermissions)
	require.NoError(t, err)
	require.NotEmpty(t, legacyReceipt.PublicationID, "legacy deployment script must publish a real generation")
	require.NoError(t, viewer.checkDashboard(ctx, qualificationHistoricalDashboardOverviewURL(seed.DashboardID)))
	require.NoError(t, runQualificationHistoricalBrowserValidation(ctx, t, repoRoot, seed, proxy))
	return qualificationHistoricalPredecessorFixture{
		Seed: seed, LegacyPublication: legacyReceipt, LegacyClientRoot: legacyClient.Root, Viewer: viewer, Proxy: proxy, Endpoint: endpoint,
		Network: network.Name, Predecessor: predecessor,
		StateVolume: stateVolume, ComposeProject: composeProject, Topology: topology,
		ApplicationEnv: cloneQualificationEnvironment(applicationEnvironment),
	}
}

func environmentMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, entry, ok := strings.Cut(value, "=")
		if ok {
			result[key] = entry
		}
	}
	return result
}

func validateHistoricalTransitionCandidate(ctx context.Context, options qualificationHistoricalTransitionOptions, repoRoot string) error {
	revisionCommand := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	revisionCommand.Dir = repoRoot
	validatorRevisionBytes, err := revisionCommand.Output()
	if err != nil || strings.TrimSpace(string(validatorRevisionBytes)) != options.CandidateRevision {
		return errors.New("historical transition validator checkout must match the exact candidate source revision")
	}
	statusCommand := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "--untracked-files=all")
	statusCommand.Dir = repoRoot
	status, err := statusCommand.Output()
	if err != nil || len(status) != 0 {
		return errors.New("historical transition receipt requires a clean candidate validator checkout")
	}
	if options.FinalArtifact {
		if err := validateHistoricalTransitionAdmission(options.AdmissionPath, options); err != nil {
			return err
		}
	}
	return validateHistoricalTransitionImageIdentity(ctx, options)
}

func validateHistoricalTransitionImageIdentity(ctx context.Context, options qualificationHistoricalTransitionOptions) error {
	if (!qualificationHistoricalLocalImagePattern.MatchString(options.CandidateImage) &&
		!qualificationHistoricalOCIImagePattern.MatchString(options.CandidateImage)) ||
		!qualificationHistoricalRevisionPattern.MatchString(options.CandidateRevision) {
		return errors.New("historical transition image validation requires an immutable image and full source revision")
	}
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", options.CandidateImage, "--format", "{{.Id}}")
	imageID, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect the supplied historical-transition candidate image: %w (%s)", err, strings.TrimSpace(string(imageID)))
	}
	if qualificationHistoricalLocalImagePattern.MatchString(options.CandidateImage) && strings.TrimSpace(string(imageID)) != options.CandidateImage {
		return errors.New("local historical transition candidate image ID does not resolve to the supplied immutable image")
	}
	version := exec.CommandContext(ctx, "docker", "run", "--rm", options.CandidateImage, "version", "--json")
	output, err := version.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read candidate image build identity: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	var identity struct {
		Revision string `json:"revision"`
		Dirty    bool   `json:"dirty"`
	}
	if err := json.Unmarshal(output, &identity); err != nil || identity.Revision != options.CandidateRevision || identity.Dirty {
		return errors.New("historical transition candidate image does not contain the exact clean supplied source revision")
	}
	return nil
}

func validateHistoricalTransitionAdmission(path string, options qualificationHistoricalTransitionOptions) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 1<<20 {
		return errors.New("final historical transition requires a bounded regular OCI admission artifact")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read exact OCI admission evidence: %w", err)
	}
	var admission struct {
		SchemaVersion  int    `json:"schemaVersion"`
		Image          string `json:"image"`
		Digest         string `json:"digest"`
		RegistryDigest string `json:"registryDigest"`
		Attestation    struct {
			Verified       bool   `json:"verified"`
			Repository     string `json:"repository"`
			Workflow       string `json:"workflow"`
			SourceRevision string `json:"sourceRevision"`
		} `json:"attestation"`
		SBOM struct {
			Discoverable  bool   `json:"discoverable"`
			PredicateType string `json:"predicateType"`
		} `json:"sbom"`
		Vulnerability struct {
			Passed   bool   `json:"passed"`
			SHA256   string `json:"sha256"`
			Scanner  string `json:"scanner"`
			Platform string `json:"platform"`
		} `json:"vulnerabilityPolicy"`
	}
	if err := json.Unmarshal(encoded, &admission); err != nil {
		return errors.New("decode exact OCI admission evidence")
	}
	digest := strings.TrimPrefix(options.CandidateImage, "ghcr.io/flidai/leapview@")
	if admission.SchemaVersion != 1 || admission.Image != options.CandidateImage || admission.Digest != digest || admission.RegistryDigest != digest ||
		!admission.Attestation.Verified || admission.Attestation.Repository != "flidai/leapview" ||
		admission.Attestation.Workflow != "flidai/leapview/.github/workflows/artifacts.yml" || admission.Attestation.SourceRevision != options.AdmissionSourceRevision ||
		!admission.SBOM.Discoverable || admission.SBOM.PredicateType != "https://spdx.dev/Document/v2.3" ||
		!admission.Vulnerability.Passed || admission.Vulnerability.Scanner != "trivy" ||
		(admission.Vulnerability.Platform != "" && admission.Vulnerability.Platform != "linux/amd64") || !qualificationHistoricalSHA256Pattern.MatchString(admission.Vulnerability.SHA256) {
		return errors.New("OCI admission evidence does not bind the exact final candidate image and revision")
	}
	return nil
}

func qualificationHistoricalApplicationEnvironment(topology *qualificationNativePostgresTopology) (map[string]string, error) {
	environment, err := qualificationNativePostgresServingEnvironment(topology)
	if err != nil {
		return nil, err
	}
	for key, value := range map[string]string{
		"LEAPVIEW_PRODUCTION": "1", "LEAPVIEW_ENVIRONMENT": "prod", "LEAPVIEW_ADDR": ":8080",
		"LEAPVIEW_HOME": "/var/lib/leapview/home", "HOME": "/var/lib/leapview/home", "LEAPVIEW_LOCAL_AUTH": "1",
		"LEAPVIEW_COOKIE_SECURE": "true", "LEAPVIEW_TRUST_PROXY_HEADERS": "true",
		"LEAPVIEW_MANAGED_DATA_BACKEND": "local", "LEAPVIEW_MANAGED_DATA_DIR": "/var/lib/leapview/home/managed-data",
		"LEAPVIEW_PUBLIC_URL":            qualificationHistoricalOrigin,
		"LEAPVIEW_ALLOWED_HOSTS":         "demo.leapview.dev,localhost,127.0.0.1",
		"LEAPVIEW_CSRF_KEY":              "historical-transition-qualification-csrf-key-v1",
		"LEAPVIEW_METRICS_BEARER_TOKEN":  "historical-transition-qualification-metrics-token",
		"LEAPVIEW_AGENT_CREDENTIAL_KEY":  strings.Repeat("a", 64),
		"LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL": "admin@qualification.invalid",
	} {
		environment[key] = value
	}
	return environment, nil
}

func startQualificationHistoricalUtility(
	t *testing.T,
	ctx context.Context,
	runtime *testcontainersQualificationRuntime,
	network, volume, image string,
	environment map[string]string,
	name string,
) qualificationContainer {
	t.Helper()
	container, err := runtime.Start(ctx, qualificationContainerRequest{
		Name: name, Image: image, NetworkMode: network,
		Volumes:     []qualificationContainerVolume{{Source: volume, Target: "/var/lib/leapview"}},
		Environment: environment, Entrypoint: []string{"sh"},
		Command: []string{"-ec", "while :; do sleep 60; done"}, NoHealth: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { qualificationHistoricalStopContainer(t, container) })
	return container
}

func copyQualificationHistoricalPoolFiles(t *testing.T, ctx context.Context, container qualificationContainer, artifacts qualificationNativePhysicalPoolArtifacts) {
	t.Helper()
	const target = "/tmp/leapview-historical-transition-pool"
	_, err := container.Exec(ctx, nil, "mkdir", "-p", target)
	require.NoError(t, err)
	_, err = container.CopyTo(ctx, artifacts.PoolPath, target+"/pool-identity.json")
	require.NoError(t, err)
	_, err = container.CopyTo(ctx, artifacts.EvidencePath, target+"/shared-pool-evidence.json")
	require.NoError(t, err)
}

func runQualificationHistoricalPoolBootstrap(
	t *testing.T,
	ctx context.Context,
	container qualificationContainer,
	artifacts qualificationNativePhysicalPoolArtifacts,
	apply bool,
	operationEnvironment map[string]string,
) qualificationNativePhysicalPoolBootstrapResult {
	t.Helper()
	args := []string{"leapview", "admin", "delivery", "pool", "bootstrap",
		"--pool", "/tmp/leapview-historical-transition-pool/pool-identity.json",
		"--evidence", "/tmp/leapview-historical-transition-pool/shared-pool-evidence.json"}
	if apply {
		args = append(args, "--apply")
	}
	if len(operationEnvironment) > 0 {
		command := []string{"env"}
		for name, value := range operationEnvironment {
			command = append(command, name+"="+value)
		}
		args = append(command, args...)
	}
	output, err := container.Exec(ctx, nil, args...)
	require.NoError(t, err)
	result, err := parseQualificationNativePhysicalPoolBootstrapResult(output)
	require.NoError(t, err)
	return result
}

func startQualificationHistoricalServer(
	t *testing.T,
	ctx context.Context,
	runtime *testcontainersQualificationRuntime,
	network, volume, image string,
	environment map[string]string,
	name string,
) (qualificationContainer, string) {
	t.Helper()
	container, err := runtime.Start(ctx, qualificationContainerRequest{
		Name: name, Image: image, NetworkMode: network,
		Volumes:     []qualificationContainerVolume{{Source: volume, Target: "/var/lib/leapview"}},
		Environment: environment, Command: []string{"serve", "--production"}, NoHealth: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { qualificationHistoricalStopContainer(t, container) })
	endpoint := qualificationHistoricalServerEndpoint(t, ctx, container, network)
	startupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	require.NoError(t, waitQualificationHistoricalHealth(startupCtx, endpoint))
	return container, endpoint
}

func qualificationHistoricalServerEndpoint(
	t *testing.T,
	ctx context.Context,
	container qualificationContainer,
	network string,
) string {
	t.Helper()
	inspection, err := container.(*testcontainersQualificationContainer).container.Inspect(ctx)
	require.NoError(t, err, "inspect candidate address on the owned Docker network")
	if inspection.NetworkSettings == nil {
		t.Fatal("isolated predecessor container has no Docker network settings")
	}
	attachedNetwork, ok := inspection.NetworkSettings.Networks[network]
	if !ok || attachedNetwork == nil || !attachedNetwork.IPAddress.IsValid() || !attachedNetwork.IPAddress.IsPrivate() {
		t.Fatalf("predecessor container has no private address on owned network %q", network)
	}
	return net.JoinHostPort(attachedNetwork.IPAddress.String(), "8080")
}

func waitQualificationHistoricalHealth(ctx context.Context, endpoint string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/healthz", nil)
		if err == nil {
			request.Host = "demo.leapview.dev"
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("predecessor did not become ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func qualificationHistoricalStopContainer(t *testing.T, container qualificationContainer) {
	t.Helper()
	if container == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = container.Remove(cleanupCtx)
}

// stopQualificationHistoricalContainerGracefully drains application workers
// before an offline operation can take a database lease. The stop request is
// bounded, and only exit code zero proves the application completed its
// shutdown hooks; Docker may report a successful stop after falling back from
// SIGTERM to SIGKILL, while exit code 143 may bypass application draining.
func stopQualificationHistoricalContainerGracefully(
	ctx context.Context,
	container qualificationContainer,
) error {
	if container == nil {
		return fmt.Errorf("qualification container is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stopCtx, cancel := context.WithTimeout(ctx, qualificationHistoricalStopWaitTimeout)
	defer cancel()
	if _, err := container.Kill(stopCtx, "TERM"); err != nil {
		return fmt.Errorf("send graceful termination signal to %s: %w", container.Name(), err)
	}
	if err := waitQualificationContainerValue(
		stopCtx,
		container,
		"{{.State.Status}}",
		"exited",
		qualificationHistoricalStopWaitTimeout,
	); err != nil {
		return fmt.Errorf("wait for %s to finish graceful shutdown: %w", container.Name(), err)
	}
	exitOutput, err := container.Inspect(stopCtx, "{{.State.ExitCode}}")
	if err != nil {
		return fmt.Errorf("inspect %s exit code after graceful shutdown: %w", container.Name(), err)
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(exitOutput)))
	if err != nil {
		return fmt.Errorf("parse %s exit code %q after graceful shutdown: %w", container.Name(), exitOutput, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%s did not exit gracefully after SIGTERM (exit code %d)", container.Name(), exitCode)
	}
	return nil
}

type qualificationHistoricalStopFixture struct {
	qualificationContainer
	signals     []string
	inspected   []string
	state       string
	exitCode    string
	stopErr     error
	hadDeadline bool
}

func (container *qualificationHistoricalStopFixture) Name() string { return "candidate-fixture" }

func (container *qualificationHistoricalStopFixture) Kill(ctx context.Context, signal string) ([]byte, error) {
	container.signals = append(container.signals, signal)
	_, container.hadDeadline = ctx.Deadline()
	if container.stopErr != nil {
		return nil, container.stopErr
	}
	container.state = "exited"
	return nil, nil
}

func (container *qualificationHistoricalStopFixture) Inspect(_ context.Context, format string) ([]byte, error) {
	container.inspected = append(container.inspected, format)
	switch format {
	case "{{.State.Status}}":
		return []byte(container.state), nil
	case "{{.State.ExitCode}}":
		return []byte(container.exitCode), nil
	default:
		return nil, fmt.Errorf("unexpected inspect format %q", format)
	}
}

func TestStopQualificationHistoricalContainerGracefullyVerifiesExit(t *testing.T) {
	container := &qualificationHistoricalStopFixture{exitCode: "0"}
	require.NoError(t, stopQualificationHistoricalContainerGracefully(t.Context(), container))
	require.Equal(t, []string{"TERM"}, container.signals, "normal shutdown must never send SIGKILL")
	require.True(t, container.hadDeadline, "graceful stop must be bounded")
	require.Equal(t, []string{"{{.State.Status}}", "{{.State.ExitCode}}"}, container.inspected,
		"stop must wait for process exit and verify the exit code")
}

func TestStopQualificationHistoricalContainerRejectsExitWithoutCompletedDrain(t *testing.T) {
	for _, exitCode := range []string{"137", "143"} {
		t.Run(exitCode, func(t *testing.T) {
			container := &qualificationHistoricalStopFixture{exitCode: exitCode}
			err := stopQualificationHistoricalContainerGracefully(t.Context(), container)
			require.ErrorContains(t, err, "did not exit gracefully")
			require.Equal(t, []string{"TERM"}, container.signals, "the shutdown path must never request SIGKILL")
		})
	}
}

func historicalQualificationCFOSource(t *testing.T, repoRoot string) string {
	return historicalQualificationCFOSourceAt(t, repoRoot, qualificationHistoricalPredecessorRevision)
}

func historicalQualificationCFOSourceAt(t *testing.T, repoRoot, revision string) string {
	t.Helper()
	directory := t.TempDir()
	archive := exec.Command("git", "archive", revision, "dashboards/experiments/cfo-demo")
	archive.Dir = repoRoot
	contents, err := archive.Output()
	require.NoError(t, err)
	command := exec.Command("tar", "-xf", "-", "-C", directory)
	command.Stdin = bytes.NewReader(contents)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	source := filepath.Join(directory, "dashboards", "experiments", "cfo-demo")
	require.DirExists(t, source)
	return source
}

func qualificationHistoricalDiagnosticTail(contents []byte, maxBytes int) string {
	redacted := redactQualificationBytes(contents)
	if maxBytes > 0 && len(redacted) > maxBytes {
		redacted = redacted[len(redacted)-maxBytes:]
	}
	return strings.TrimSpace(string(redacted))
}

type qualificationHistoricalTransport struct {
	ProxyURL string
	CACert   string
	SPKIPin  string
	target   *qualificationHistoricalProxyTarget
	server   *http.Server
	listener net.Listener
	process  *exec.Cmd
}

type qualificationHistoricalProxyTarget struct {
	mu        sync.RWMutex
	url       *url.URL
	failureMu sync.Mutex
	failures  []string
}

func newQualificationHistoricalProxyTarget(endpoint string) (*qualificationHistoricalProxyTarget, error) {
	target := &qualificationHistoricalProxyTarget{}
	if err := target.setEndpoint(endpoint); err != nil {
		return nil, err
	}
	return target, nil
}

func (target *qualificationHistoricalProxyTarget) setEndpoint(endpoint string) error {
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid historical candidate endpoint %q", endpoint)
	}
	target.mu.Lock()
	target.url = parsed
	target.mu.Unlock()
	return nil
}

func (target *qualificationHistoricalProxyTarget) current() *url.URL {
	target.mu.RLock()
	defer target.mu.RUnlock()
	copy := *target.url
	return &copy
}

func (target *qualificationHistoricalProxyTarget) recordFailure(response *http.Response) {
	if response == nil || response.StatusCode < http.StatusBadRequest || response.Request == nil {
		return
	}
	entry := fmt.Sprintf("%s %s -> HTTP %d", response.Request.Method, response.Request.URL.EscapedPath(), response.StatusCode)
	target.failureMu.Lock()
	target.failures = append(target.failures, entry)
	if len(target.failures) > 64 {
		target.failures = append([]string(nil), target.failures[len(target.failures)-64:]...)
	}
	target.failureMu.Unlock()
}

func (target *qualificationHistoricalProxyTarget) failureTail(limit int) string {
	target.failureMu.Lock()
	defer target.failureMu.Unlock()
	if len(target.failures) == 0 {
		return "no non-2xx candidate responses were recorded"
	}
	if limit <= 0 || limit > len(target.failures) {
		limit = len(target.failures)
	}
	return strings.Join(target.failures[len(target.failures)-limit:], "\n")
}

func qualificationHistoricalReverseProxy(target *qualificationHistoricalProxyTarget) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			proxyTarget := target.current()
			request.URL.Scheme = proxyTarget.Scheme
			request.URL.Host = proxyTarget.Host
			request.Host = "demo.leapview.dev"
			request.Header.Set("X-Forwarded-Proto", "https")
		},
		ModifyResponse: func(response *http.Response) error {
			target.recordFailure(response)
			return nil
		},
	}
}

func (transport *qualificationHistoricalTransport) setEndpoint(endpoint string) error {
	if transport == nil || transport.target == nil {
		return errors.New("historical candidate transport has no proxy target")
	}
	return transport.target.setEndpoint(endpoint)
}

func startQualificationHistoricalTransport(t *testing.T, ctx context.Context, repoRoot, endpoint string) *qualificationHistoricalTransport {
	t.Helper()
	target, err := newQualificationHistoricalProxyTarget(endpoint)
	require.NoError(t, err)
	proxy := qualificationHistoricalReverseProxy(target)
	certificate, caPath := qualificationHistoricalTLSCertificate(t)
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	require.NoError(t, err)
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	require.NoError(t, err)
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()

	python := `import sys,socketserver
sys.path.insert(0, sys.argv[2])
from demo_upgrade_transport import DemoTunnelProxy
class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True
server = Server(('127.0.0.1', 0), DemoTunnelProxy)
server.upstream_port = int(sys.argv[1])
print(server.server_address[1], flush=True)
server.serve_forever()`
	process := exec.CommandContext(ctx, "python3", "-u", "-c", python, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), filepath.Join(repoRoot, "scripts"))
	stdout, err := process.StdoutPipe()
	require.NoError(t, err)
	process.Stderr = os.Stderr
	require.NoError(t, process.Start())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	proxyPort, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err)
	transport := &qualificationHistoricalTransport{
		ProxyURL: fmt.Sprintf("http://127.0.0.1:%d", proxyPort), CACert: caPath,
		SPKIPin: base64.StdEncoding.EncodeToString(spki[:]), target: target,
		server: server, listener: listener, process: process,
	}
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
		if process.Process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	})
	return transport
}

func qualificationHistoricalTLSCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "historical-transition-test-ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCertificate, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	if len(caCertificate.SubjectKeyId) == 0 {
		t.Fatal("historical TLS test CA is missing its generated subject key identifier")
	}
	caTemplate.SubjectKeyId = append([]byte(nil), caCertificate.SubjectKeyId...)
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "demo.leapview.dev"},
		DNSNames: []string{"demo.leapview.dev"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		AuthorityKeyId: append([]byte(nil), caCertificate.SubjectKeyId...),
		KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	require.NoError(t, err)
	caPath := filepath.Join(t.TempDir(), "historical-transition-ca.pem")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))
	return certificate, caPath
}

type qualificationHistoricalPublicationResult struct {
	ProjectID         string `json:"projectId"`
	CandidateID       string `json:"candidateId"`
	PublicationID     string `json:"publicationId"`
	GenerationID      string `json:"generationId"`
	Status            string `json:"status"`
	SourceRevision    string `json:"sourceRevision"`
	RuntimeRevision   string `json:"runtimeRevision"`
	PermissionProfile string `json:"permissionProfile"`
	Target            string `json:"target"`
}

func runQualificationHistoricalPublication(
	ctx context.Context,
	t *testing.T,
	repoRoot string,
	seed qualificationHistoricalSeed,
	transport *qualificationHistoricalTransport,
	revision, permissionProfile string,
) (qualificationHistoricalPublicationResult, error) {
	var result qualificationHistoricalPublicationResult
	// Resolve all Go inputs before the clone-only proxy is installed. The actual
	// deployment script still builds and runs the CLI from this source tree, but
	// GOPROXY=off below guarantees that no fallback dependency download can
	// escape through the public network during publication.
	prebuilt := filepath.Join(t.TempDir(), "qualification-leapview")
	build := exec.CommandContext(ctx, "go", "build", "-o", prebuilt, "./cmd/leapview")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		return result, fmt.Errorf("prebuild exact publication CLI before clone-only transport: %w (%s)", err,
			qualificationHistoricalDiagnosticTail(output, 16<<10))
	}
	configgen := exec.CommandContext(ctx, "go", "run", "./internal/app/tools/configgen")
	configgen.Dir = repoRoot
	if output, err := configgen.CombinedOutput(); err != nil {
		return result, fmt.Errorf("prewarm exact source generation before clone-only transport: %w (%s)", err,
			qualificationHistoricalDiagnosticTail(output, 16<<10))
	}
	base := os.Environ()
	clientEnvironment, err := exec.Command("python3", "-c", `import os,sys
sys.path.insert(0, sys.argv[1])
from demo_client_contract import clone_only_environment
import json
print(json.dumps(clone_only_environment(dict(os.environ), sys.argv[2])))`, filepath.Join(repoRoot, "scripts"), transport.ProxyURL).Output()
	if err != nil {
		return result, fmt.Errorf("derive canonical clone-only publication environment: %w", err)
	}
	environment := environmentMap(base)
	if err := json.Unmarshal(clientEnvironment, &environment); err != nil {
		return result, err
	}
	environment["GOPROXY"] = "off"
	environment["GOSUMDB"] = "off"
	environment["DEMO_DATASET"] = "cfo"
	environment["DEMO_PERMISSION_PROFILE"] = permissionProfile
	environment["DEMO_SOURCE_REVISION"] = revision
	environment["DEMO_PROJECT_ID"] = seed.ProjectID
	environment["DEMO_PUBLISHER_CLIENT_ID"] = seed.PublisherClientID
	environment["DEMO_PUBLISHER_CLIENT_SECRET"] = seed.PublisherClientSecret
	environment["DEMO_RELEASE_CLIENT_ID"] = seed.ReleaseClientID
	environment["DEMO_RELEASE_CLIENT_SECRET"] = seed.ReleaseClientSecret
	environment["DEMO_FIXTURE_SOURCE_ROOT"] = seed.SourceRoot
	environment["DEMO_FIXTURE_DATA_PATH"] = seed.DataPath
	receiptDirectory, err := os.MkdirTemp(t.TempDir(), "publication-receipt-")
	if err != nil {
		return result, err
	}
	if err := os.Chmod(receiptDirectory, 0o700); err != nil {
		return result, err
	}
	receiptPath := filepath.Join(receiptDirectory, "publication.json")
	environment["DEMO_PUBLICATION_RECEIPT"] = receiptPath
	// Pin urllib trust explicitly: Nix OpenSSL may prefer NIX_SSL_CERT_FILE
	// over SSL_CERT_FILE when loading the default certificate store.
	environment["DEMO_GENERATION_CA_CERT"] = transport.CACert
	environment["DEMO_GENERATION_PROXY"] = transport.ProxyURL
	environment["SSL_CERT_FILE"] = transport.CACert
	environment["CURL_CA_BUNDLE"] = transport.CACert
	environment["REQUESTS_CA_BUNDLE"] = transport.CACert
	command := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot, "scripts", "deploy_demo.sh"))
	command.Dir = repoRoot
	command.Env = sortedEnvironment(environment)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err = command.Run()
	for _, key := range []string{"DEMO_PUBLISHER_CLIENT_SECRET", "DEMO_RELEASE_CLIENT_SECRET"} {
		environment[key] = ""
	}
	if err != nil {
		return result, fmt.Errorf("run actual demo publication adapter: %w", err)
	}
	encoded, err := os.ReadFile(receiptPath)
	if err != nil {
		return result, fmt.Errorf("read actual demo publication receipt: %w", err)
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, err
	}
	info, err := os.Stat(receiptPath)
	if err != nil || info.Mode().Perm() != 0o600 || result.ProjectID != seed.ProjectID || result.Status != "committed" ||
		result.SourceRevision != revision || result.RuntimeRevision != revision || result.PermissionProfile != permissionProfile || result.Target != qualificationHistoricalOrigin ||
		result.PublicationID == "" || result.GenerationID == "" || result.CandidateID == "" {
		return qualificationHistoricalPublicationResult{}, errors.New("actual demo publication receipt does not bind the committed fixture deployment")
	}
	return result, nil
}

func runQualificationHistoricalBrowserValidation(
	ctx context.Context,
	t *testing.T,
	repoRoot string,
	seed qualificationHistoricalSeed,
	transport *qualificationHistoricalTransport,
) error {
	t.Helper()
	if transport == nil || transport.ProxyURL == "" || transport.SPKIPin == "" {
		return errors.New("historical browser validation requires the fixed pinned TLS relay")
	}
	clientEnvironment, err := exec.CommandContext(ctx, "python3", "-c", `import os,sys
sys.path.insert(0, sys.argv[1])
from demo_client_contract import clone_only_environment
import json
print(json.dumps(clone_only_environment(dict(os.environ), sys.argv[2])))`, filepath.Join(repoRoot, "scripts"), transport.ProxyURL).Output()
	if err != nil {
		return fmt.Errorf("derive clone-only browser environment: %w", err)
	}
	environment := environmentMap(os.Environ())
	if err := json.Unmarshal(clientEnvironment, &environment); err != nil {
		return err
	}
	environment["DEMO_BROWSER_PROXY"] = transport.ProxyURL
	environment["DEMO_HISTORICAL_BROWSER_SPKI"] = transport.SPKIPin
	environment["DEMO_VIEWER_EMAIL"] = seed.ViewerEmail
	environment["DEMO_VIEWER_PASSWORD"] = qualificationHistoricalBrowserPassword
	browserCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(browserCtx, "node", filepath.Join("internal", "app", "cli", "composectl", "testdata", "historical_browser.mjs"))
	command.Dir = repoRoot
	command.Env = sortedEnvironment(environment)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run actual historical CFO browser query validator: %w", err)
	}
	return nil
}

func sortedEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func qualificationHistoricalContainerRevision(t *testing.T, ctx context.Context, app qualificationContainer) string {
	t.Helper()
	output := qualificationHistoricalContainerVersionOutput(t, ctx, app)
	var identity struct {
		Revision string `json:"revision"`
	}
	require.NoError(t, json.Unmarshal(output, &identity))
	return identity.Revision
}

func qualificationHistoricalContainerVersionOutput(t *testing.T, ctx context.Context, app qualificationContainer) []byte {
	t.Helper()
	output, err := app.Exec(ctx, nil, "leapview", "version", "--json")
	require.NoError(t, err)
	return output
}
