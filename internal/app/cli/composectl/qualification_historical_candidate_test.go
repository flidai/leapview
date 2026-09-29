package composectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type qualificationHistoricalCandidateResult struct {
	Request                       qualificationHistoricalNativeRequestWire
	Inventory                     adminpostgres.AccessTransitionInventory
	Transition                    app.AccessTransitionExecutionResult
	Candidate                     qualificationContainer
	Endpoint                      string
	Proxy                         *qualificationHistoricalTransport
	Viewer                        *qualificationHistoricalBrowser
	PreTransitionDashboardStatus  int
	PostTransitionDashboardStatus int
	DeniedSourcesStatus           int
	ApprovalRequestedBy           string
	ApprovalDecidedBy             string
	ApprovalDecision              string
	ApprovalRequestCredential     string
	ApprovalDecisionCredential    string
	ApprovalDecisionRevision      int64
}

// These wire types intentionally mirror hostinstall.NativeRequest and its
// nested JSON structs. composectl is imported by hostinstall's CLI config, so
// importing hostinstall from this in-package test would create a test cycle.
// The candidate's real CLI still strictly decodes and validates this request;
// the CLI's own plan command derives the fenced operation digest.
type qualificationHistoricalNativeRequestWire struct {
	AccessTransition             *admincli.AccessTransitionIntent     `json:"accessTransition,omitempty"`
	PreparationDigest            string                               `json:"preparationDigest,omitempty"`
	Profile                      qualificationHistoricalProfile       `json:"profile"`
	DeploymentRunID              string                               `json:"deploymentRunId"`
	DeploymentAttempt            string                               `json:"deploymentAttempt"`
	Version                      int                                  `json:"version"`
	PredecessorImage             string                               `json:"predecessorImage"`
	PredecessorRevision          string                               `json:"predecessorRevision"`
	CandidateImage               string                               `json:"candidateImage"`
	CandidateRevision            string                               `json:"candidateRevision"`
	CandidateAttestationRevision string                               `json:"candidateAttestationRevision,omitempty"`
	Qualification                qualificationHistoricalQualification `json:"qualification"`
	Admission                    json.RawMessage                      `json:"admission"`
	Plan                         qualificationHistoricalNativePlan    `json:"plan"`
}

type qualificationHistoricalProfile struct {
	ControlMigratorURLFile string            `json:"controlMigratorUrlFile,omitempty"`
	Version                int               `json:"version"`
	ID                     string            `json:"id"`
	Hostname               string            `json:"hostname"`
	Root                   string            `json:"root"`
	StateRoot              string            `json:"stateRoot"`
	Project                string            `json:"project"`
	AppService             string            `json:"appService"`
	ProxyService           string            `json:"proxyService"`
	Postgres               string            `json:"postgres"`
	PostgresImage          string            `json:"postgresImage"`
	Network                string            `json:"network"`
	Origin                 string            `json:"origin"`
	HTTPBinding            string            `json:"httpBinding"`
	HTTPSBinding           string            `json:"httpsBinding"`
	RehearsalBinding       string            `json:"rehearsalBinding"`
	Volumes                map[string]string `json:"volumes"`
}

type qualificationHistoricalQualification struct {
	Image      string `json:"image"`
	Revision   string `json:"revision"`
	RunID      string `json:"runId"`
	RunAttempt string `json:"runAttempt"`
	Qualified  bool   `json:"qualified"`
}

type qualificationHistoricalSourceCompatibility struct {
	PermissionProfile string            `json:"permissionProfile"`
	Schema            int               `json:"schema"`
	Migrations        map[string]string `json:"migrations"`
	Engines           map[string]string `json:"engines"`
	RolePolicy        string            `json:"rolePolicy"`
}

type qualificationHistoricalNativePlan struct {
	SourceBefore                 qualificationHistoricalSourceCompatibility `json:"sourceBefore"`
	SourceAfter                  qualificationHistoricalSourceCompatibility `json:"sourceAfter"`
	RolePolicyChanged            bool                                       `json:"rolePolicyChanged"`
	Mode                         string                                     `json:"mode"`
	CurrentSchema                int                                        `json:"currentSchema"`
	CandidateSchema              int                                        `json:"candidateSchema"`
	PendingMigrations            []string                                   `json:"pendingMigrations"`
	PendingMigrationDigests      map[string]string                          `json:"pendingMigrationDigests"`
	CompatibilityChanges         []string                                   `json:"compatibilityChanges"`
	PredecessorRevision          string                                     `json:"predecessorRevision"`
	CandidateRevision            string                                     `json:"candidateRevision"`
	ImageOnlyEligible            bool                                       `json:"imageOnlyEligible"`
	MigrationExecutionAuthorized bool                                       `json:"migrationExecutionAuthorized"`
}

type qualificationHistoricalUpgradePlan struct {
	Mode                         string                                     `json:"mode"`
	CurrentSchema                int                                        `json:"currentSchema"`
	CandidateSchema              int                                        `json:"candidateSchema"`
	PendingMigrations            []string                                   `json:"pendingMigrations"`
	PendingMigrationDigests      map[string]string                          `json:"pendingMigrationDigests"`
	CompatibilityChanges         []string                                   `json:"compatibilityChanges"`
	RolePolicyChanged            bool                                       `json:"rolePolicyChanged"`
	ImageOnlyEligible            bool                                       `json:"imageOnlyEligible"`
	MigrationExecutionAuthorized bool                                       `json:"migrationExecutionAuthorized"`
	PredecessorRevision          string                                     `json:"predecessorRevision"`
	CandidateRevision            string                                     `json:"candidateRevision"`
	SourceBefore                 qualificationHistoricalSourceCompatibility `json:"sourceBefore"`
	SourceAfter                  qualificationHistoricalSourceCompatibility `json:"sourceAfter"`
}

// runQualificationHistoricalCandidateTransition migrates the real schema-32
// fixture with the supplied candidate binary, then invokes that binary's
// fenced access-transition CLI. It restarts the candidate and waits for the
// exact committed generation/publication/policy pointer before checking a
// dashboard the viewer may read and the catalog the viewer must not read.
func runQualificationHistoricalCandidateTransition(
	t *testing.T,
	ctx context.Context,
	repoRoot string,
	options qualificationHistoricalTransitionOptions,
	fixture qualificationHistoricalPredecessorFixture,
) qualificationHistoricalCandidateResult {
	t.Helper()
	require.NotNil(t, fixture.Predecessor)
	require.NotNil(t, fixture.Topology)
	_, err := fixture.Predecessor.Kill(ctx, "KILL")
	require.NoError(t, err, "stop the exact schema-32 predecessor before migrating its database")

	runtime := newTestcontainersQualificationRuntime()
	project := fixture.ComposeProject
	transitionDir := filepath.Join("/tmp", project+"-access-transition")
	requestPath := filepath.Join(transitionDir, "request.json")
	journalPath := filepath.Join(transitionDir, "journal.json")
	migratorPath := filepath.Join(transitionDir, "migrator.url")
	publisherPath := filepath.Join(transitionDir, "publisher.secret")
	reviewerPath := filepath.Join(transitionDir, "reviewer.secret")
	recoveryDigest := qualificationHistoricalRecoveryDigest(fixture)

	requestImage, admission := qualificationHistoricalCandidateAdmission(t, options)
	utility := startQualificationHistoricalTransitionUtility(t, ctx, runtime, fixture.Topology.ComposeNetwork,
		fixture.StateVolume, options.CandidateImage, requestImage, options.FinalArtifact, fixture.ApplicationEnv, project+"-candidate-transition")
	hostnameOutput, err := utility.Exec(ctx, nil, "hostname")
	require.NoError(t, err, "read candidate maintenance utility hostname")
	maintenanceHostname := strings.TrimSpace(string(hostnameOutput))
	require.NotEmpty(t, maintenanceHostname)
	_, err = utility.Exec(ctx, nil, "mkdir", "-p", transitionDir)
	require.NoError(t, err)

	inventoryOutput, err := utility.Exec(ctx, nil, "leapview", "admin", "transition-access-inventory", "--project", fixture.Seed.ProjectID)
	require.NoError(t, err, "read candidate inventory from the untouched schema-32 target")
	var inventory adminpostgres.AccessTransitionInventory
	require.NoError(t, json.Unmarshal(inventoryOutput, &inventory), "decode pre-migration candidate inventory")
	require.Equal(t, fixture.Seed.TargetID, inventory.TargetID)
	require.Equal(t, fixture.Seed.ProjectID, inventory.ProjectID)
	require.Equal(t, fixture.Seed.Environment, inventory.Environment)
	require.NotEmpty(t, inventory.ServingGenerationID)
	requireHistoricalDigest(t, inventory.PolicyDigest)
	requireHistoricalDigest(t, inventory.ServingPolicySnapshotDigest)

	upgradePlan := inspectQualificationHistoricalTransition(t, ctx, repoRoot, options.CandidateRevision)
	request := qualificationHistoricalNativeRequest(t, fixture, options, inventory, upgradePlan, requestImage, admission, maintenanceHostname)
	requestBytes, err := json.Marshal(request)
	require.NoError(t, err)
	privateRoot := t.TempDir()
	migratorHostPath := writeQualificationHistoricalPrivateFile(t, privateRoot, "migrator.url", []byte(fixture.Topology.ControlMigratorURL))
	copyQualificationHistoricalPrivateFile(t, ctx, utility, requestPath, requestBytes)
	identityOutput, err := utility.Exec(ctx, nil, "/usr/local/share/leapview/deployment/leapviewctl", "host", "upgrade", "plan", "--request", requestPath)
	if err != nil {
		diagnostic := qualificationHistoricalCommandDiagnostic(identityOutput, err,
			fixture.Seed.PublisherClientSecret, fixture.Seed.ReleaseClientSecret, fixture.Topology.ControlMigratorURL)
		t.Fatalf("candidate CLI must validate the exact native request before migration: %s", diagnostic)
	}
	var planResult struct {
		OperationDigest string `json:"operationDigest"`
	}
	require.NoError(t, json.Unmarshal(identityOutput, &planResult))
	requireHistoricalDigest(t, planResult.OperationDigest)
	journal := struct {
		Version int `json:"version"`
		State   struct {
			Identity struct {
				Target                  string `json:"target"`
				Predecessor             string `json:"predecessor"`
				Candidate               string `json:"candidate"`
				ArtifactAdmissionDigest string `json:"artifactAdmissionDigest"`
			} `json:"identity"`
			Phase           string `json:"phase"`
			RecoveryDigest  string `json:"recoveryDigest,omitempty"`
			RestoreRequired bool   `json:"restoreRequired"`
		} `json:"state"`
	}{
		Version: 1,
	}
	journal.State.Identity.Target = request.Profile.ID
	journal.State.Identity.Predecessor = request.PredecessorImage
	journal.State.Identity.Candidate = request.CandidateImage
	journal.State.Identity.ArtifactAdmissionDigest = planResult.OperationDigest
	journal.State.Phase = "migrating"
	journal.State.RecoveryDigest = recoveryDigest
	journal.State.RestoreRequired = true
	journalBytes, err := json.Marshal(journal)
	require.NoError(t, err)

	copyQualificationHistoricalPrivateFile(t, ctx, utility, journalPath, journalBytes)
	copyQualificationHistoricalPrivateFile(t, ctx, utility, migratorPath, mustReadQualificationHistoricalPrivateFile(t, migratorHostPath))

	_, migrationErr := utility.Exec(ctx, nil, "/usr/local/share/leapview/deployment/leapviewctl", "host", "upgrade", "migrate",
		"--request", requestPath, "--journal", journalPath,
		"--credential", migratorPath, "--recovery-digest", recoveryDigest)
	if migrationErr != nil {
		t.Fatalf("candidate-owned migration failed: %v", migrationErr)
	}
	require.Equal(t, int64(migrations.CurrentRevision), qualificationHistoricalPostgresRevision(t, ctx, fixture.Topology),
		"candidate host upgrade migrate must apply the exact embedded schema chain")

	postMigrationOutput, err := utility.Exec(ctx, nil, "leapview", "admin", "transition-access-inventory", "--project", fixture.Seed.ProjectID)
	require.NoError(t, err, "read candidate inventory again after schema migration")
	var postMigrationInventory adminpostgres.AccessTransitionInventory
	require.NoError(t, json.Unmarshal(postMigrationOutput, &postMigrationInventory))
	require.Equal(t, inventory.TargetID, postMigrationInventory.TargetID)
	require.Equal(t, inventory.ProjectID, postMigrationInventory.ProjectID)
	require.Equal(t, inventory.Environment, postMigrationInventory.Environment)
	require.Equal(t, inventory.PolicyRevision, postMigrationInventory.PolicyRevision, "schema migration must preserve the target policy head")
	require.Equal(t, inventory.PolicyDigest, postMigrationInventory.PolicyDigest, "schema migration must preserve the policy digest")
	require.Equal(t, inventory.ServingGenerationID, postMigrationInventory.ServingGenerationID, "schema migration must preserve the active generation")
	require.Equal(t, inventory.ServingPolicySnapshotDigest, postMigrationInventory.ServingPolicySnapshotDigest,
		"schema migration must preserve the legacy serving snapshot digest")

	candidate, endpoint := startQualificationHistoricalServer(t, ctx, runtime, fixture.Topology.ComposeNetwork,
		fixture.StateVolume, options.CandidateImage, fixture.ApplicationEnv, project+"-candidate")
	proxy := startQualificationHistoricalTransport(t, ctx, repoRoot, endpoint)
	viewer := newQualificationHistoricalBrowser(t, proxy.ProxyURL, proxy.CACert)
	require.NoError(t, viewer.loginExisting(ctx, fixture.Seed.ViewerEmail, qualificationHistoricalBrowserPassword))
	preTransitionStatus := qualificationHistoricalStatus(t, ctx, viewer, qualificationHistoricalDashboardOverviewURL(fixture.Seed.DashboardID))
	currentInventoryOutput, err := utility.Exec(ctx, nil, "leapview", "admin", "transition-access-inventory", "--project", fixture.Seed.ProjectID)
	require.NoError(t, err, "independently re-read the native active pointer and current policy after observing candidate pre-transition access")
	var currentInventory adminpostgres.AccessTransitionInventory
	require.NoError(t, json.Unmarshal(currentInventoryOutput, &currentInventory))
	require.Equal(t, inventory.TargetID, currentInventory.TargetID)
	require.Equal(t, inventory.ProjectID, currentInventory.ProjectID)
	require.Equal(t, inventory.Environment, currentInventory.Environment)
	require.Equal(t, inventory.PolicyRevision, currentInventory.PolicyRevision, "pre-transition dashboard access must not silently advance the target policy head")
	require.Equal(t, inventory.PolicyDigest, currentInventory.PolicyDigest, "pre-transition dashboard access must not silently change the target policy head")
	require.Equal(t, inventory.ServingGenerationID, currentInventory.ServingGenerationID, "pre-transition dashboard access must still use the immutable schema-32 serving generation")
	require.Equal(t, inventory.ServingPolicySnapshotDigest, currentInventory.ServingPolicySnapshotDigest,
		"pre-transition dashboard access must still use the preserved schema-32 serving policy snapshot")
	t.Logf("candidate pre-transition viewer overview returned HTTP %d while the preserved schema-32 generation %s remains active with serving policy %s (current policy revision %d digest %s)",
		preTransitionStatus, currentInventory.ServingGenerationID, currentInventory.ServingPolicySnapshotDigest,
		currentInventory.PolicyRevision, currentInventory.PolicyDigest)
	require.Equal(t, http.StatusForbidden, preTransitionStatus,
		"the migrated but not-yet-transitioned schema-32 policy must reproduce the original viewer denial")
	// Match NativeEffects.transitionAccess: artifacts must be created by the
	// candidate runtime identity, otherwise its private object-store files are
	// unreadable when the non-root server resumes.
	uidOutput, err := candidate.Exec(ctx, nil, "id", "-u")
	require.NoError(t, err)
	gidOutput, err := candidate.Exec(ctx, nil, "id", "-g")
	require.NoError(t, err)
	uid, err := strconv.Atoi(strings.TrimSpace(string(uidOutput)))
	require.NoError(t, err)
	gid, err := strconv.Atoi(strings.TrimSpace(string(gidOutput)))
	require.NoError(t, err)
	require.Positive(t, uid, "the application must run as a non-root user")
	require.Positive(t, gid)
	runtimeUser := fmt.Sprintf("%d:%d", uid, gid)
	_, err = candidate.Kill(ctx, "KILL")
	require.NoError(t, err, "stop the candidate server before invoking the offline maintenance command")

	publisherHostPath := writeQualificationHistoricalPrivateFile(t, privateRoot, "publisher.secret", []byte(fixture.Seed.PublisherClientSecret))
	reviewerHostPath := writeQualificationHistoricalPrivateFile(t, privateRoot, "reviewer.secret", []byte(fixture.Seed.ReleaseClientSecret))
	copyQualificationHistoricalPrivateFile(t, ctx, utility, publisherPath, mustReadQualificationHistoricalPrivateFile(t, publisherHostPath))
	copyQualificationHistoricalPrivateFile(t, ctx, utility, reviewerPath, mustReadQualificationHistoricalPrivateFile(t, reviewerHostPath))
	_, err = utility.Exec(ctx, nil, "chown", "-R", runtimeUser, transitionDir)
	require.NoError(t, err, "stage private transition inputs for the candidate runtime identity")
	transitionCommand := exec.CommandContext(ctx, "docker", "exec", "--user", runtimeUser,
		project+"-candidate-transition", "leapview", "admin", "transition-access",
		"--request", requestPath, "--journal", journalPath, "--recovery-digest", recoveryDigest,
		"--mode", "live", "--publisher-credential-file", publisherPath, "--reviewer-credential-file", reviewerPath)
	transitionOutput, err := transitionCommand.CombinedOutput()
	if err != nil {
		diagnostic := qualificationHistoricalCommandDiagnostic(transitionOutput, err,
			fixture.Seed.PublisherClientSecret, fixture.Seed.ReleaseClientSecret, fixture.Topology.ControlMigratorURL)
		t.Fatalf("candidate fenced access transition failed: %s", diagnostic)
	}
	var transition app.AccessTransitionExecutionResult
	require.NoError(t, json.Unmarshal(transitionOutput, &transition), "decode the redacted native transition receipt")
	intentPlan, err := request.AccessTransition.Plan()
	require.NoError(t, err)
	require.Equal(t, "access-transition:"+strings.TrimPrefix(planResult.OperationDigest, "sha256:"), transition.OperationID)
	require.Equal(t, intentPlan.IntentDigest, transition.IntentDigest)
	require.NotEmpty(t, transition.PlanID)
	require.NotEmpty(t, transition.CandidateID)
	require.NotEmpty(t, transition.GenerationID)
	require.NotEmpty(t, transition.PublicationID)
	require.NotEmpty(t, transition.ApprovalRequestID)
	requireHistoricalDigest(t, transition.PlanPolicySnapshotDigest)
	requireHistoricalDigest(t, transition.ServingPolicySnapshotDigest)
	require.NotEqual(t, transition.PlanPolicySnapshotDigest, transition.ServingPolicySnapshotDigest,
		"the candidate-policy planning fingerprint must remain distinct from the generation-bound serving snapshot digest")
	require.Contains(t, []string{"pending", "committed"}, transition.Status)
	_, err = utility.Remove(ctx)
	require.NoError(t, err, "remove the transition-only candidate utility")

	_, err = candidate.Start(ctx)
	require.NoError(t, err, "restart the candidate server to process the durable publication workflow")
	endpoint = qualificationHistoricalServerEndpoint(t, ctx, candidate, fixture.Topology.ComposeNetwork)
	require.NoError(t, proxy.setEndpoint(endpoint), "retarget the viewer transport to the restarted candidate address")
	startupCtx, startupCancel := context.WithTimeout(ctx, 2*time.Minute)
	require.NoError(t, waitQualificationHistoricalHealth(startupCtx, endpoint))
	startupCancel()
	require.NoError(t, waitQualificationHistoricalTransitionActivation(ctx, fixture.Topology,
		inventory.TargetID, inventory.ProjectID, inventory.Environment, transition))
	approval := qualificationHistoricalApprovalEvidenceFor(t, ctx, fixture.Topology, inventory.TargetID, transition)
	require.Equal(t, fixture.Seed.PublisherPrincipalID, approval.RequestedBy)
	require.Equal(t, fixture.Seed.ReviewerPrincipalID, approval.DecidedBy)
	require.Equal(t, "approved", approval.Decision)
	require.Equal(t, "workload", approval.RequestCredentialClass)
	require.Equal(t, "workload", approval.DecisionCredentialClass)
	require.NotEqual(t, approval.RequestedBy, approval.DecidedBy, "publication approval must have an independent reviewer")

	postDashboardStatus := qualificationHistoricalStatus(t, ctx, viewer, qualificationHistoricalDashboardOverviewURL(fixture.Seed.DashboardID))
	deniedSourcesStatus := qualificationHistoricalStatus(t, ctx, viewer, qualificationHistoricalOrigin+"/sources")
	require.Equal(t, 200, postDashboardStatus, "the exact typed dashboard grant must remain usable after activation")
	require.Equal(t, 403, deniedSourcesStatus, "the dashboard-only viewer must not acquire the broader source catalog")
	return qualificationHistoricalCandidateResult{
		Request: request, Inventory: inventory, Transition: transition, Candidate: candidate,
		Endpoint: endpoint, Proxy: proxy, Viewer: viewer,
		PreTransitionDashboardStatus: preTransitionStatus, PostTransitionDashboardStatus: postDashboardStatus,
		DeniedSourcesStatus: deniedSourcesStatus, ApprovalRequestedBy: approval.RequestedBy,
		ApprovalDecidedBy: approval.DecidedBy, ApprovalDecision: approval.Decision,
		ApprovalRequestCredential: approval.RequestCredentialClass, ApprovalDecisionCredential: approval.DecisionCredentialClass,
		ApprovalDecisionRevision: approval.DecisionRevision,
	}
}

func inspectQualificationHistoricalTransition(t *testing.T, ctx context.Context, repoRoot, candidateRevision string) qualificationHistoricalUpgradePlan {
	t.Helper()
	const script = `import json,sys
sys.path.insert(0, sys.argv[1])
from demo_upgrade_plan import inspect_transition
print(json.dumps(inspect_transition(sys.argv[2], sys.argv[3])))`
	command := exec.CommandContext(ctx, "python3", "-c", script, filepath.Join(repoRoot, "scripts"),
		qualificationHistoricalPredecessorRevision, candidateRevision)
	command.Dir = repoRoot
	output, err := command.CombinedOutput()
	require.NoError(t, err, "inspect exact predecessor-to-candidate compatibility: %s", strings.TrimSpace(string(output)))
	var plan qualificationHistoricalUpgradePlan
	require.NoError(t, json.Unmarshal(output, &plan))
	require.Equal(t, qualificationHistoricalPredecessorRevision, plan.PredecessorRevision)
	require.Equal(t, candidateRevision, plan.CandidateRevision)
	require.Equal(t, qualificationHistoricalPredecessorSchema, plan.CurrentSchema)
	require.Equal(t, int(migrations.CurrentRevision), plan.CandidateSchema)
	require.Equal(t, "database-upgrade-required", plan.Mode)
	require.False(t, plan.MigrationExecutionAuthorized)
	require.False(t, plan.ImageOnlyEligible)
	return plan
}

func qualificationHistoricalNativeRequest(
	t *testing.T,
	fixture qualificationHistoricalPredecessorFixture,
	options qualificationHistoricalTransitionOptions,
	inventory adminpostgres.AccessTransitionInventory,
	compatibility qualificationHistoricalUpgradePlan,
	candidateImage string,
	admission []byte,
	hostname string,
) qualificationHistoricalNativeRequestWire {
	t.Helper()
	request := qualificationHistoricalNativeRequestWire{
		Version: 1, DeploymentRunID: strconv.FormatInt(time.Now().UTC().UnixNano(), 10), DeploymentAttempt: "1",
		PredecessorImage: qualificationHistoricalPredecessorImage, PredecessorRevision: qualificationHistoricalPredecessorRevision,
		CandidateImage: candidateImage, CandidateRevision: options.CandidateRevision, Admission: admission,
	}
	if options.FinalArtifact {
		request.CandidateAttestationRevision = options.AdmissionSourceRevision
	}
	request.Profile = qualificationHistoricalProfile{
		Version: 1, ID: inventory.TargetID, Hostname: hostname,
		Root: "/qualification/instance", StateRoot: "/qualification/maintenance",
		Project: fixture.ComposeProject, AppService: fixture.ComposeProject + "-leapview",
		ProxyService: fixture.ComposeProject + "-proxy", Postgres: fixture.Topology.ContainerName,
		PostgresImage: qualificationPostgreSQL18Image, Network: fixture.Topology.ComposeNetwork,
		Origin: qualificationHistoricalOrigin, HTTPBinding: "127.0.0.1:18082", HTTPSBinding: "127.0.0.1:18443",
		RehearsalBinding: "127.0.0.1:18444",
		Volumes: map[string]string{
			"postgres": fixture.ComposeProject + "-postgres-state", "home": fixture.ComposeProject + "-home-state",
			"caddy-data": fixture.ComposeProject + "-proxy-data", "caddy-config": fixture.ComposeProject + "-proxy-config",
		},
	}
	request.Qualification = qualificationHistoricalQualification{
		Image: candidateImage, Revision: options.CandidateRevision,
		RunID: "1000000000", RunAttempt: "1", Qualified: true,
	}
	request.Plan.SourceBefore = compatibility.SourceBefore
	request.Plan.SourceAfter = compatibility.SourceAfter
	request.Plan.RolePolicyChanged = compatibility.RolePolicyChanged
	request.Plan.Mode = compatibility.Mode
	request.Plan.CurrentSchema = compatibility.CurrentSchema
	request.Plan.CandidateSchema = compatibility.CandidateSchema
	request.Plan.PendingMigrations = append([]string(nil), compatibility.PendingMigrations...)
	request.Plan.PendingMigrationDigests = cloneHistoricalStringMap(compatibility.PendingMigrationDigests)
	request.Plan.CompatibilityChanges = append([]string(nil), compatibility.CompatibilityChanges...)
	request.Plan.PredecessorRevision = compatibility.PredecessorRevision
	request.Plan.CandidateRevision = compatibility.CandidateRevision
	request.Plan.ImageOnlyEligible = compatibility.ImageOnlyEligible
	request.Plan.MigrationExecutionAuthorized = false
	request.AccessTransition = &admincli.AccessTransitionIntent{
		TargetID: inventory.TargetID, Environment: inventory.Environment, ProjectID: inventory.ProjectID,
		ExpectedPolicyRevision: inventory.PolicyRevision, ExpectedPolicyDigest: inventory.PolicyDigest,
		ExpectedServingGeneration:   inventory.ServingGenerationID,
		ExpectedServingPolicyDigest: inventory.ServingPolicySnapshotDigest,
		PublisherPrincipalID:        fixture.Seed.PublisherPrincipalID, ReviewerPrincipalID: fixture.Seed.ReviewerPrincipalID,
		RoleBindings: []admincli.AccessTransitionRoleIntent{
			{BindingID: "typed-historical-publisher", Name: "Historical release operator", Principal: fixture.Seed.PublisherPrincipalID, Role: "release_operator"},
			{BindingID: "typed-historical-reviewer", Name: "Historical release approver", Principal: fixture.Seed.ReviewerPrincipalID, Role: "release_approver"},
		},
		Grants: qualificationHistoricalTransitionGrants(t, fixture.Seed),
	}
	return request
}

func qualificationHistoricalCandidateAdmission(t *testing.T, options qualificationHistoricalTransitionOptions) (string, []byte) {
	t.Helper()
	if options.FinalArtifact {
		admission, err := os.ReadFile(options.AdmissionPath)
		require.NoError(t, err)
		return options.CandidateImage, admission
	}
	sum := sha256.Sum256([]byte("leapview/historical-transition-local-image/v1\n" + options.CandidateImage + "\n" + options.CandidateRevision))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	image := "ghcr.io/flidai/leapview@" + digest
	vulnerability := sha256.Sum256([]byte("leapview/historical-transition-local-vulnerability-policy/v1\n" + options.CandidateRevision))
	admission, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "image": image, "digest": digest, "registryDigest": digest,
		"attestation": map[string]any{"verified": true, "repository": "flidai/leapview",
			"workflow": "flidai/leapview/.github/workflows/artifacts.yml", "sourceRevision": options.CandidateRevision},
		"sbom":                map[string]any{"discoverable": true, "predicateType": "https://spdx.dev/Document/v2.3"},
		"vulnerabilityPolicy": map[string]any{"passed": true, "scanner": "trivy", "sha256": hex.EncodeToString(vulnerability[:]), "platform": "linux/amd64"},
	})
	require.NoError(t, err)
	return image, admission
}

func startQualificationHistoricalTransitionUtility(
	t *testing.T,
	ctx context.Context,
	runtime *testcontainersQualificationRuntime,
	network, volume, localCandidateImage, admittedCandidateImage string,
	finalArtifact bool,
	environment map[string]string,
	name string,
) qualificationContainer {
	t.Helper()
	dockerPath, err := exec.LookPath("docker")
	require.NoError(t, err, "candidate plan validation needs the local Docker client")
	dockerPath, err = filepath.Abs(dockerPath)
	require.NoError(t, err)
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Fatalf("candidate plan validation needs the local Docker socket: %v", err)
	}
	env := make(map[string]string, len(environment)+2)
	for key, value := range environment {
		env[key] = value
	}
	dockerTarget := "/usr/local/bin/docker"
	if !finalArtifact {
		dockerTarget = "/usr/local/bin/docker-real"
		env["LEAPVIEW_HISTORICAL_ADMITTED_CANDIDATE_IMAGE"] = admittedCandidateImage
		env["LEAPVIEW_HISTORICAL_LOCAL_CANDIDATE_IMAGE"] = localCandidateImage
	}
	container, err := runtime.Start(ctx, qualificationContainerRequest{
		Name: name, Image: localCandidateImage, NetworkMode: network, User: "0:0",
		Volumes: []qualificationContainerVolume{
			{Source: volume, Target: "/var/lib/leapview"},
			{Source: dockerPath, Target: dockerTarget, ReadOnly: true},
			{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		},
		Environment: env, Entrypoint: []string{"sh"}, Command: []string{"-ec", "while :; do sleep 60; done"}, NoHealth: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { qualificationHistoricalStopContainer(t, container) })
	if finalArtifact {
		return container
	}
	dockerShim := `#!/busybox/sh
if [ "$1" = "create" ] && [ "$2" = "$LEAPVIEW_HISTORICAL_ADMITTED_CANDIDATE_IMAGE" ]; then
  shift 2
  exec /usr/local/bin/docker-real create "$LEAPVIEW_HISTORICAL_LOCAL_CANDIDATE_IMAGE" "$@"
fi
exec /usr/local/bin/docker-real "$@"
`
	shimHostPath := writeQualificationHistoricalPrivateFile(t, t.TempDir(), "docker", []byte(dockerShim))
	_, err = container.CopyTo(ctx, shimHostPath, "/usr/local/bin/docker")
	require.NoError(t, err)
	_, err = container.Exec(ctx, nil, "chmod", "0755", "/usr/local/bin/docker")
	require.NoError(t, err)
	return container
}

func qualificationHistoricalRecoveryDigest(fixture qualificationHistoricalPredecessorFixture) string {
	sum := sha256.Sum256([]byte("leapview/historical-transition-disposable-recovery-point/v1\n" +
		fixture.ComposeProject + "\n" + fixture.Topology.ContainerName + "\n" + fixture.Seed.TargetID + "\n" + fixture.LegacyPublication.GenerationID))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func copyQualificationHistoricalPrivateFile(t *testing.T, ctx context.Context, container qualificationContainer, destination string, contents []byte) {
	t.Helper()
	require.NotEmpty(t, contents, "private qualification input is present")
	localDir := t.TempDir()
	localPath := writeQualificationHistoricalPrivateFile(t, localDir, filepath.Base(destination), contents)
	_, err := container.Exec(ctx, nil, "mkdir", "-p", filepath.Dir(destination))
	require.NoError(t, err)
	_, err = container.CopyTo(ctx, localPath, destination)
	require.NoError(t, err)
	_, err = container.Exec(ctx, nil, "chmod", "0600", destination)
	require.NoError(t, err)
}

func writeQualificationHistoricalPrivateFile(t *testing.T, directory, name string, contents []byte) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadQualificationHistoricalPrivateFile(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}

func qualificationHistoricalPostgresRevision(t *testing.T, ctx context.Context, topology *qualificationNativePostgresTopology) int64 {
	t.Helper()
	output, err := topology.Container.Exec(ctx, nil, "sh", "-ec", `export PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem
psql --host localhost --username leapview_control_readonly --dbname leapview_control --no-psqlrc --tuples-only --no-align --command 'SELECT version_id FROM public.goose_db_version ORDER BY id DESC LIMIT 1'`)
	require.NoError(t, err)
	revision, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	require.NoError(t, err)
	return revision
}

func waitQualificationHistoricalTransitionActivation(
	ctx context.Context,
	topology *qualificationNativePostgresTopology,
	targetID, projectID, environment string,
	transition app.AccessTransitionExecutionResult,
) error {
	if ctx == nil || topology == nil || topology.Container == nil || strings.TrimSpace(targetID) == "" ||
		strings.TrimSpace(projectID) == "" || strings.TrimSpace(environment) == "" {
		return errors.New("candidate activation check is incomplete")
	}
	for name, value := range map[string]string{
		"generation": transition.GenerationID, "publication": transition.PublicationID,
		"plan": transition.PlanID,
	} {
		if err := requireCanonicalHistoricalUUID(value); err != nil {
			return fmt.Errorf("invalid transition %s identity: %w", name, err)
		}
	}
	if !canonicalHistoricalTransitionDigest(transition.PlanPolicySnapshotDigest) ||
		!canonicalHistoricalTransitionDigest(transition.ServingPolicySnapshotDigest) ||
		transition.PlanPolicySnapshotDigest == transition.ServingPolicySnapshotDigest {
		return errors.New("invalid transition planning or generation-bound authorization snapshot digest")
	}
	targetHex := hex.EncodeToString([]byte(targetID))
	projectHex := hex.EncodeToString([]byte(projectID))
	environmentHex := hex.EncodeToString([]byte(environment))
	query := fmt.Sprintf(`SELECT EXISTS (
SELECT 1 FROM delivery.delivery_active_pointer a
JOIN delivery.delivery_publication p ON p.publication_id = a.publication_id
JOIN delivery.delivery_generation g ON g.generation_id = a.generation_id
JOIN delivery.delivery_plan n ON n.plan_id = g.plan_id
JOIN delivery.delivery_target t ON t.target_id = a.target_id
JOIN access.authorization_snapshot s
 ON s.project_id = t.project_id
 AND s.environment = t.environment
 AND s.generation_id = a.generation_id::text
WHERE a.target_id = convert_from(decode('%s','hex'),'UTF8')
AND t.project_id = convert_from(decode('%s','hex'),'UTF8')
AND t.environment = convert_from(decode('%s','hex'),'UTF8')
AND a.generation_id = '%s'::uuid AND a.publication_id = '%s'::uuid
AND p.state = 'committed' AND p.target_id = a.target_id
AND p.generation_id = a.generation_id AND g.target_id = a.target_id
AND g.plan_id = '%s'::uuid AND n.target_id = a.target_id
AND n.plan_document->'authorization'->>'snapshotDigest' = '%s'
AND s.digest = '%s')`,
		targetHex, projectHex, environmentHex, transition.GenerationID, transition.PublicationID,
		transition.PlanID, transition.PlanPolicySnapshotDigest, transition.ServingPolicySnapshotDigest)
	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		output, err := topology.Container.Exec(waitCtx, nil, "sh", "-ec", `export PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem
psql --host localhost --username leapview_control_readonly --dbname leapview_control --no-psqlrc --tuples-only --no-align --command "$1"`, "--", query)
		if err != nil {
			if waitCtx.Err() != nil {
				return fmt.Errorf("wait for exact typed transition activation: %w", waitCtx.Err())
			}
			return fmt.Errorf("read exact typed transition activation: %w", err)
		}
		switch strings.TrimSpace(string(output)) {
		case "t":
			return nil
		case "f":
		default:
			return fmt.Errorf("invalid exact typed transition activation result %q", strings.TrimSpace(string(output)))
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("typed transition did not activate before the qualification deadline: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

type qualificationHistoricalApprovalEvidence struct {
	RequestedBy             string
	DecidedBy               string
	Decision                string
	RequestCredentialClass  string
	DecisionCredentialClass string
	DecisionRevision        int64
}

func qualificationHistoricalApprovalEvidenceFor(
	t *testing.T,
	ctx context.Context,
	topology *qualificationNativePostgresTopology,
	targetID string,
	transition app.AccessTransitionExecutionResult,
) qualificationHistoricalApprovalEvidence {
	t.Helper()
	if topology == nil || topology.Container == nil || strings.TrimSpace(targetID) == "" {
		t.Fatal("approval evidence scope is incomplete")
	}
	for name, value := range map[string]string{
		"request": transition.ApprovalRequestID, "publication": transition.PublicationID,
		"candidate": transition.CandidateID, "generation": transition.GenerationID,
	} {
		if err := requireCanonicalHistoricalUUID(value); err != nil {
			t.Fatalf("approval %s identity is not canonical: %v", name, err)
		}
	}
	targetHex := hex.EncodeToString([]byte(targetID))
	query := fmt.Sprintf(`SELECT r.requested_by || '|' || d.decided_by || '|' || d.decision || '|' || r.request_credential_class || '|' || d.decision_credential_class || '|' || d.decision_revision::text
FROM delivery.delivery_approval_request r
JOIN delivery.delivery_approval_decision d ON d.request_id = r.request_id
WHERE r.request_id = '%s'::uuid AND r.publication_id = '%s'::uuid
AND r.target_id = convert_from(decode('%s','hex'),'UTF8')
AND r.candidate_id = '%s'::uuid AND r.generation_id = '%s'::uuid
ORDER BY d.decision_revision DESC LIMIT 1`, transition.ApprovalRequestID, transition.PublicationID, targetHex, transition.CandidateID, transition.GenerationID)
	output, err := topology.Container.Exec(ctx, nil, "sh", "-ec", `export PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem
psql --host localhost --username leapview_control_readonly --dbname leapview_control --no-psqlrc --tuples-only --no-align --command "$1"`, "--", query)
	require.NoError(t, err, "read persisted independent reviewer evidence")
	values := strings.Split(strings.TrimSpace(string(output)), "|")
	require.Len(t, values, 6)
	decisionRevision, err := strconv.ParseInt(values[5], 10, 64)
	require.NoError(t, err)
	return qualificationHistoricalApprovalEvidence{
		RequestedBy: values[0], DecidedBy: values[1], Decision: values[2],
		RequestCredentialClass: values[3], DecisionCredentialClass: values[4], DecisionRevision: decisionRevision,
	}
}

func requireCanonicalHistoricalUUID(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return errors.New("canonical lowercase UUID required")
	}
	return nil
}

func canonicalHistoricalTransitionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func qualificationHistoricalStatus(t *testing.T, ctx context.Context, browser *qualificationHistoricalBrowser, address string) int {
	t.Helper()
	response, err := browser.get(ctx, address)
	require.NoError(t, err)
	defer response.Body.Close()
	return response.StatusCode
}

func requireHistoricalDigest(t *testing.T, digest string) {
	t.Helper()
	require.Len(t, digest, len("sha256:")+64)
	require.True(t, strings.HasPrefix(digest, "sha256:"))
	if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
		t.Fatal(err)
	}
}

func cloneHistoricalStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
