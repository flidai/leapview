package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/releasecontract"
	"github.com/stretchr/testify/require"
)

// historicalQualificationClientTree executes the real current publication
// adapter using the exact predecessor Go source and CLI. Only the public
// publication script and its direct client-contract helper are overlaid from
// this candidate checkout; predecessor generation/build tooling remains from
// the pinned source tree.
type qualificationHistoricalClientCheckout struct {
	Root string
	CLI  string
}

func historicalQualificationClientTree(t *testing.T, ctx context.Context, repoRoot, revision string, predecessor qualificationContainer) qualificationHistoricalClientCheckout {
	t.Helper()
	clientRoot := filepath.Join(t.TempDir(), "predecessor-client")
	command := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", clientRoot, revision)
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create exact predecessor client checkout: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		remove := exec.CommandContext(cleanupCtx, "git", "worktree", "remove", "--force", clientRoot)
		remove.Dir = repoRoot
		if output, err := remove.CombinedOutput(); err != nil {
			t.Logf("remove owned predecessor client checkout: %v (%s)", err, strings.TrimSpace(string(output)))
		}
	})
	generate := exec.CommandContext(ctx, "sh", filepath.Join(clientRoot, "scripts", "generate_build_sources.sh"))
	generate.Dir = clientRoot
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate exact predecessor CLI sources before publication: %v (%s)", err,
			qualificationHistoricalDiagnosticTail(output, 64<<10))
	}
	prebuilt := filepath.Join(t.TempDir(), "predecessor-leapview")
	var predecessorIdentity struct {
		Version     string `json:"version"`
		Revision    string `json:"revision"`
		BuildTime   string `json:"buildTime"`
		Dirty       bool   `json:"dirty"`
		Development bool   `json:"development"`
	}
	identityOutput := qualificationHistoricalContainerVersionOutput(t, ctx, predecessor)
	require.NoError(t, json.Unmarshal(identityOutput, &predecessorIdentity))
	require.Equal(t, revision, predecessorIdentity.Revision)
	require.NotEmpty(t, predecessorIdentity.Version)
	_, err := time.Parse(time.RFC3339, predecessorIdentity.BuildTime)
	require.NoError(t, err, "pinned predecessor image must expose a valid immutable build time")
	require.False(t, predecessorIdentity.Dirty, "the pinned predecessor image must have been built from a clean tree")
	linkerFlags := strings.Join([]string{
		"-X=github.com/flidai/leapview/internal/platform/buildinfo.version=" + predecessorIdentity.Version,
		"-X=github.com/flidai/leapview/internal/platform/buildinfo.revision=" + predecessorIdentity.Revision,
		"-X=github.com/flidai/leapview/internal/platform/buildinfo.buildTime=" + predecessorIdentity.BuildTime,
		"-X=github.com/flidai/leapview/internal/platform/buildinfo.dirty=" + strconv.FormatBool(predecessorIdentity.Dirty),
		"-X=github.com/flidai/leapview/internal/platform/buildinfo.release=" + strconv.FormatBool(!predecessorIdentity.Development),
	}, " ")
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", linkerFlags, "-o", prebuilt, "./cmd/leapview")
	build.Dir = clientRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the exact predecessor Go CLI before clone-only networking: %v (%s)", err,
			qualificationHistoricalDiagnosticTail(output, 64<<10))
	}
	identityOutput, err = exec.CommandContext(ctx, prebuilt, "version", "--json").CombinedOutput()
	require.NoError(t, err, "read exact predecessor Go CLI identity")
	var cliIdentity struct {
		Revision string `json:"revision"`
		Dirty    bool   `json:"dirty"`
	}
	require.NoError(t, json.Unmarshal(identityOutput, &cliIdentity))
	require.Equal(t, revision, cliIdentity.Revision)
	require.False(t, cliIdentity.Dirty, "the predecessor CLI must be built before current script overlays")
	for _, name := range []string{"deploy_demo.sh", "demo_client_contract.py"} {
		sourcePath := filepath.Join(repoRoot, "scripts", name)
		destinationPath := filepath.Join(clientRoot, "scripts", name)
		contents, err := os.ReadFile(sourcePath)
		require.NoError(t, err)
		mode := os.FileMode(0o644)
		if name == "deploy_demo.sh" {
			mode = 0o755
		}
		require.NoError(t, os.WriteFile(destinationPath, contents, mode))
	}
	head, err := exec.CommandContext(ctx, "git", "-C", clientRoot, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	require.Equal(t, revision, strings.TrimSpace(string(head)), "the legacy publication CLI source must be the exact predecessor revision")
	return qualificationHistoricalClientCheckout{Root: clientRoot, CLI: prebuilt}
}

// runQualificationHistoricalBootstrapPublication uses the exact schema-32
// CLI and the one-time administrator token to establish the first immutable
// serving snapshot. The predecessor deliberately permits only platform-admin
// credentials to create upload sessions before a generation exists; the
// following ordinary deploy_demo.sh run uses the separately scoped publisher
// and reviewer principals after this snapshot captures their legacy grants.
func runQualificationHistoricalBootstrapPublication(
	ctx context.Context,
	t *testing.T,
	checkout qualificationHistoricalClientCheckout,
	seed qualificationHistoricalSeed,
	initial qualificationHistoricalInitialCredentials,
	transport *qualificationHistoricalTransport,
) (qualificationHistoricalPublicationResult, error) {
	var result qualificationHistoricalPublicationResult
	if checkout.Root == "" || checkout.CLI == "" || seed.SourceRoot == "" || seed.DataPath == "" ||
		seed.ProjectID == "" || initial.PublisherToken == "" || transport == nil || transport.ProxyURL == "" {
		return result, errors.New("historical bootstrap publication inputs are incomplete")
	}
	clientEnvironment, err := exec.CommandContext(ctx, "python3", "-c", `import os,sys
sys.path.insert(0, sys.argv[1])
from demo_client_contract import clone_only_environment
import json
print(json.dumps(clone_only_environment(dict(os.environ), sys.argv[2])))`, filepath.Join(checkout.Root, "scripts"), transport.ProxyURL).Output()
	if err != nil {
		return result, fmt.Errorf("derive predecessor bootstrap clone-only environment: %w", err)
	}
	environment := environmentMap(os.Environ())
	if err := json.Unmarshal(clientEnvironment, &environment); err != nil {
		return result, fmt.Errorf("decode predecessor bootstrap clone-only environment: %w", err)
	}
	privateHome, err := os.MkdirTemp("", "leapview-historical-bootstrap-home-")
	if err != nil {
		return result, fmt.Errorf("create private predecessor CLI home: %w", err)
	}
	defer os.RemoveAll(privateHome)
	if err := os.Chmod(privateHome, 0o700); err != nil {
		return result, fmt.Errorf("protect private predecessor CLI home: %w", err)
	}
	environment["HOME"] = privateHome
	environment["XDG_CONFIG_HOME"] = filepath.Join(privateHome, ".config")
	environment["GOPROXY"] = "off"
	environment["GOSUMDB"] = "off"
	environment["SSL_CERT_FILE"] = transport.CACert
	environment["CURL_CA_BUNDLE"] = transport.CACert
	environment["REQUESTS_CA_BUNDLE"] = transport.CACert
	commandEnv := sortedEnvironment(environment)
	runID := fmt.Sprintf("%x-%x", time.Now().UTC().UnixNano(), os.Getpid())
	run := func(label string, arguments ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, checkout.CLI, arguments...)
		command.Dir = checkout.Root
		command.Env = commandEnv
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			return nil, fmt.Errorf("predecessor %s failed: %s", label,
				qualificationHistoricalCommandDiagnostic(output, commandErr, initial.PublisherToken,
					seed.PublisherClientSecret, seed.ReleaseClientSecret, seed.ViewerPassword))
		}
		return output, nil
	}
	projectArgs := []string{"--source-root", seed.SourceRoot, "--target", qualificationHistoricalOrigin,
		"--project-id", seed.ProjectID, "--token", initial.PublisherToken}
	t.Log("historical predecessor baseline: staging synthetic CFO data with the original administrator credential")
	if _, err := run("initial managed-data sync", append([]string{"data", "sync", "--connection", "finance_files", "--from", seed.DataPath}, append(projectArgs, "--format", "json")...)...); err != nil {
		return result, err
	}
	t.Log("historical predecessor baseline: creating and sealing the first real source candidate")
	planArgs := append([]string{"plan"}, projectArgs...)
	planArgs = append(planArgs, "--candidate-key", "historical-bootstrap-"+runID, "--format", "json")
	planOutput, err := run("initial delivery plan", planArgs...)
	if err != nil {
		return result, err
	}
	var plan struct {
		PlanID    string `json:"planId"`
		ProjectID string `json:"projectId"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(planOutput, &plan); err != nil || plan.PlanID == "" || plan.ProjectID != seed.ProjectID || plan.Status != "planned" {
		return result, errors.New("predecessor initial delivery plan did not bind the expected project")
	}
	buildOutput, err := run("initial delivery build", "build", plan.PlanID, "--token", initial.PublisherToken, "--format", "json")
	if err != nil {
		return result, err
	}
	var build struct {
		PlanID      string `json:"planId"`
		CandidateID string `json:"candidateId"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(buildOutput, &build); err != nil || build.PlanID != plan.PlanID || build.CandidateID == "" || build.Status != "sealed" {
		return result, errors.New("predecessor initial delivery build did not seal the expected candidate")
	}
	publishOutput, err := run("initial delivery publication", "publish", build.CandidateID,
		"--token", initial.PublisherToken, "--format", "json")
	if err != nil {
		return result, err
	}
	var publication struct {
		PublicationID string `json:"publicationId"`
		GenerationID  string `json:"generationId"`
		CandidateID   string `json:"candidateId"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(publishOutput, &publication); err != nil || publication.PublicationID == "" ||
		publication.GenerationID == "" || publication.CandidateID != build.CandidateID ||
		(publication.Status != "pending" && publication.Status != "committed") {
		return result, errors.New("predecessor initial publication returned incompatible durable evidence")
	}
	generationReadToken := ""
	if publication.Status == "pending" {
		t.Log("historical predecessor baseline: approving the protected initial publication with the independent reviewer")
		reviewerToken, err := qualificationHistoricalLegacyReviewerToken(ctx, seed, transport)
		if err != nil {
			return result, err
		}
		generationReadToken = reviewerToken
		requestOutput, err := run("initial publication approval request",
			"api", "call", "requestDeliveryPublicationApproval",
			"--target", qualificationHistoricalOrigin, "--token", initial.PublisherToken,
			"--path", "project="+seed.ProjectID, "--path", "publication="+publication.PublicationID,
			"--idempotency-key", "historical-bootstrap-request-"+runID)
		if err != nil {
			return result, err
		}
		var approval struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
			Status   string `json:"status"`
		}
		if err := json.Unmarshal(requestOutput, &approval); err != nil || approval.ID == "" || approval.Status != "pending" || approval.Revision < 0 {
			return result, errors.New("predecessor initial publication approval request returned incompatible evidence")
		}
		approvedOutput, err := run("initial publication approval decision",
			"api", "call", "approveDeliveryPublicationApproval",
			"--target", qualificationHistoricalOrigin, "--token", reviewerToken,
			"--path", "project="+seed.ProjectID, "--path", "publication="+publication.PublicationID,
			"--path", "approval="+approval.ID,
			"--body-json", fmt.Sprintf(`{"expectedRevision":%d}`, approval.Revision),
			"--idempotency-key", "historical-bootstrap-approve-"+runID)
		if err != nil {
			return result, err
		}
		var decision struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(approvedOutput, &decision); err != nil || decision.Status != "approved" {
			return result, errors.New("independent reviewer did not approve the predecessor initial publication")
		}
		for attempt := 0; attempt < 120; attempt++ {
			evidenceOutput, err := run("initial publication evidence read",
				"api", "call", "getDeliveryPublicationEvidence",
				"--target", qualificationHistoricalOrigin, "--token", initial.PublisherToken,
				"--path", "project="+seed.ProjectID, "--path", "publication="+publication.PublicationID)
			if err != nil {
				return result, err
			}
			var evidence struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(evidenceOutput, &evidence); err != nil {
				return result, fmt.Errorf("decode predecessor initial publication evidence: %w", err)
			}
			if evidence.Status == "committed" {
				publication.Status = evidence.Status
				break
			}
			if evidence.Status == "rejected" || evidence.Status == "indeterminate" {
				return result, fmt.Errorf("predecessor initial publication ended in %s", evidence.Status)
			}
			if attempt == 119 {
				return result, errors.New("predecessor initial publication did not become committed")
			}
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	if publication.Status != "committed" {
		return result, errors.New("predecessor initial publication is not committed")
	}
	if generationReadToken == "" {
		generationReadToken, err = qualificationHistoricalLegacyReviewerToken(ctx, seed, transport)
		if err != nil {
			return result, err
		}
	}
	generationEnvironment := environmentMap(commandEnv)
	generationEnvironment["DEMO_GENERATION_TOKEN"] = generationReadToken
	generationEnvironment["DEMO_GENERATION_CA_CERT"] = transport.CACert
	generationEnvironment["DEMO_GENERATION_PROXY"] = transport.ProxyURL
	generationCommand := exec.CommandContext(ctx, "python3", filepath.Join(checkout.Root, "scripts", "demo_client_contract.py"),
		"--wait-generation", "--target", qualificationHistoricalOrigin, "--project", seed.ProjectID,
		"--generation", publication.GenerationID, "--candidate", publication.CandidateID,
		"--target-id", seed.TargetID, "--environment", seed.Environment, "--timeout", "90", "--poll-interval", "2")
	generationCommand.Dir = checkout.Root
	generationCommand.Env = sortedEnvironment(generationEnvironment)
	generationOutput, err := generationCommand.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("predecessor generation status poll failed: %s", qualificationHistoricalDiagnosticTail(generationOutput, 8<<10))
	}
	var generation struct {
		ID          string `json:"id"`
		ProjectID   string `json:"projectId"`
		CandidateID string `json:"candidateId"`
		TargetID    string `json:"targetId"`
		Environment string `json:"environment"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(generationOutput, &generation); err != nil || generation.ID != publication.GenerationID ||
		generation.ProjectID != seed.ProjectID || generation.CandidateID != publication.CandidateID ||
		generation.TargetID != seed.TargetID || generation.Environment != seed.Environment || generation.Status != "active" {
		return result, errors.New("predecessor generation status poll did not return the exact active project, target, candidate, and environment")
	}
	result = qualificationHistoricalPublicationResult{
		ProjectID: seed.ProjectID, CandidateID: publication.CandidateID, PublicationID: publication.PublicationID,
		GenerationID: publication.GenerationID, Status: publication.Status,
		SourceRevision:    qualificationHistoricalPredecessorRevision,
		RuntimeRevision:   qualificationHistoricalPredecessorRevision,
		PermissionProfile: releasecontract.LegacyPermissions, Target: qualificationHistoricalOrigin,
	}
	t.Logf("historical predecessor baseline activated generation %s from publication %s", result.GenerationID, result.PublicationID)
	return result, nil
}

func qualificationHistoricalLegacyReviewerToken(
	ctx context.Context,
	seed qualificationHistoricalSeed,
	transport *qualificationHistoricalTransport,
) (string, error) {
	client, err := qualificationHistoricalWorkloadHTTPClient(transport)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"client_credentials"}, "client_id": {seed.ReleaseClientID},
		"client_secret": {seed.ReleaseClientSecret}, "project_id": {seed.ProjectID},
		"scope": {"PROJECT_ADMIN RESOURCE_READ"}, "lifetime_seconds": {"1800"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, qualificationHistoricalOrigin+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("construct predecessor reviewer OAuth exchange: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("exchange predecessor reviewer workload identity: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("read predecessor reviewer OAuth response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("predecessor reviewer OAuth exchange returned HTTP %d: %s",
			response.StatusCode, qualificationHistoricalCommandDiagnostic(body, nil, seed.ReleaseClientSecret))
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.Scope != "PROJECT_ADMIN RESOURCE_READ" || token.ExpiresIn < 300 {
		return "", errors.New("predecessor reviewer OAuth response did not bind the expected legacy scope and lifetime")
	}
	return token.AccessToken, nil
}
