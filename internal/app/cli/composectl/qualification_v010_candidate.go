package composectl

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

const (
	v010CandidateAdminEmail = "fai-517-candidate@qualification.invalid"
	v010CandidateStateMount = "/var/lib/leapview"
	v010CandidateRunMount   = "/qualification"
)

type v010CandidateCredentials struct {
	Email          string `json:"email"`
	PublisherToken string `json:"publisherToken"`
}

func (c *Controller) qualifyV010FreshCandidate(
	ctx context.Context,
	policyDocument []byte,
	predecessor compatibility.V010ReleaseIdentityEvidence,
	predecessorVolume string,
) (result compatibility.V010FreshCandidateEvidence, runErr error) {
	policy, err := compatibility.ParsePolicy(policyDocument)
	if err != nil {
		return result, err
	}
	candidateRelease, ok := policy.ReleaseByID(policy.CandidateRelease)
	if !ok {
		return result, fmt.Errorf("transition policy candidate release %q is unavailable", policy.CandidateRelease)
	}
	candidate := candidateRelease.IdentityForPlatform(compatibility.ReleasedV010Platform)
	freshDecision := policy.Evaluate(compatibility.Request{
		Operation: compatibility.OperationFreshInstall,
		Next:      candidate,
	})
	if err := freshDecision.Err(); err != nil || freshDecision.ReasonCode != compatibility.ReasonAllowedFreshInstall {
		return result, fmt.Errorf("candidate fresh installation is not allowed by the exact transition policy: %w", err)
	}
	upgradeDenial := policy.Evaluate(compatibility.Request{
		Operation: compatibility.OperationUpgrade,
		Current:   predecessor.Identity,
		Next:      candidate,
	})
	if !errors.Is(upgradeDenial.Err(), compatibility.ErrV010FreshInstallOnly) ||
		upgradeDenial.ReasonCode != compatibility.ReasonDeniedFreshInstallOnly {
		return result, fmt.Errorf("transition policy did not deny preserved v0.1 state adoption")
	}
	directAdoptionDenial := policy.Evaluate(compatibility.Request{
		Operation: compatibility.OperationFreshInstall,
		Next:      predecessor.Identity,
	})
	if directAdoptionDenial.Allowed || directAdoptionDenial.ReasonCode != compatibility.ReasonDeniedUnknownRelease {
		return result, fmt.Errorf("transition policy did not deny direct v0.1 artifact adoption")
	}

	imageID, err := c.resolveV010CandidateImage(ctx, candidate)
	if err != nil {
		return result, err
	}
	runID, err := qualificationRandomHex(16)
	if err != nil {
		return result, err
	}
	name := "leapview-v010-candidate-" + runID
	network := name + "-network"
	volume := name + "-state"
	runDirectory, err := os.MkdirTemp(c.root, ".v010-candidate-run-"+runID+"-")
	if err != nil {
		return result, err
	}
	if err := os.Chmod(runDirectory, 0o755); err != nil {
		_ = os.RemoveAll(runDirectory)
		return result, err
	}
	var networkCreated, volumeCreated, containerCreated, cleanupComplete bool
	cleanup := func(cleanupCtx context.Context) error {
		var cleanupErr error
		if containerCreated {
			_, removeErr := c.qualificationDocker(cleanupCtx, nil, "rm", "--force", name)
			cleanupErr = errors.Join(cleanupErr, ignoreQualificationNotFound(removeErr))
		}
		if volumeCreated {
			_, removeErr := c.qualificationDocker(cleanupCtx, nil, "volume", "rm", volume)
			cleanupErr = errors.Join(cleanupErr, ignoreQualificationNotFound(removeErr))
		}
		if networkCreated {
			_, removeErr := c.qualificationDocker(cleanupCtx, nil, "network", "rm", network)
			cleanupErr = errors.Join(cleanupErr, ignoreQualificationNotFound(removeErr))
		}
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(runDirectory))
		return cleanupErr
	}
	defer func() {
		if cleanupComplete {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		runErr = errors.Join(runErr, cleanup(cleanupCtx))
	}()

	labels := []string{"--label", "com.leapview.qualification=fai-517", "--label", "com.leapview.qualification.run-id=" + runID}
	networkArgs := append([]string{"network", "create", "--driver", "bridge", "--internal"}, labels...)
	networkArgs = append(networkArgs, network)
	if _, err := c.qualificationDocker(ctx, nil, networkArgs...); err != nil {
		return result, fmt.Errorf("create isolated fresh candidate network: %w", err)
	}
	networkCreated = true
	volumeArgs := append([]string{"volume", "create"}, labels...)
	volumeArgs = append(volumeArgs, volume)
	if _, err := c.qualificationDocker(ctx, nil, volumeArgs...); err != nil {
		return result, fmt.Errorf("create isolated fresh candidate state: %w", err)
	}
	volumeCreated = true
	startedAt := c.now().UTC()
	freshBefore, freshBeforeEntries, err := c.v010QualificationVolumeChecksum(ctx, candidate.Image, volume)
	if err != nil {
		return result, fmt.Errorf("inventory clean candidate state: %w", err)
	}
	if freshBeforeEntries != 0 {
		return result, fmt.Errorf("candidate state volume was not empty before fresh installation")
	}
	credentials, err := c.initializeV010FreshCandidate(ctx, candidate.Image, volume)
	if err != nil {
		return result, err
	}
	csrfKey, err := qualificationRandomHex(32)
	if err != nil {
		return result, err
	}
	metricsToken, err := qualificationRandomHex(32)
	if err != nil {
		return result, err
	}
	runOutput, err := c.qualificationDockerWithEnvironment(ctx, map[string]string{
		"LEAPVIEW_CSRF_KEY": csrfKey, "LEAPVIEW_METRICS_BEARER_TOKEN": metricsToken,
	},
		"run", "--detach", "--name", name, "--hostname", "leapview-fai-517",
		"--network", network, "--platform", compatibility.ReleasedV010Platform,
		"--pull", "never", "--restart", "no", "--read-only",
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL", "--pids-limit", "512",
		"--env", "LEAPVIEW_PRODUCTION=1", "--env", "LEAPVIEW_ENVIRONMENT=fai-517-candidate",
		"--env", "LEAPVIEW_HOME=/var/lib/leapview/home", "--env", "LEAPVIEW_API_TOKEN_ONLY_AUTH=1",
		"--env", "LEAPVIEW_ALLOWED_HOSTS=127.0.0.1,localhost", "--env", "LEAPVIEW_PUBLIC_URL=https://localhost",
		"--env", "LEAPVIEW_CSRF_KEY", "--env", "LEAPVIEW_METRICS_BEARER_TOKEN",
		"--mount", "type=volume,source="+volume+",target="+v010CandidateStateMount,
		"--mount", "type=bind,source="+runDirectory+",target="+v010CandidateRunMount+",readonly",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
		labels[0], labels[1], labels[2], labels[3], candidate.Image,
	)
	if err != nil {
		return result, fmt.Errorf("start isolated fresh candidate: %w", err)
	}
	containerCreated = true
	containerID := strings.TrimSpace(string(runOutput))
	inspected, err := c.inspectV010Container(ctx, name)
	if err != nil {
		return result, err
	}
	if err := validateV010FreshCandidateInspect(inspected, containerID, name, network, volume, runDirectory, candidate, imageID, true); err != nil {
		return result, err
	}
	inventory, err := c.collectV010FreshCandidateInventory(ctx, name, credentials)
	if err != nil {
		return result, err
	}
	if _, err := c.qualificationDocker(ctx, nil, "stop", "--time", "30", name); err != nil {
		return result, fmt.Errorf("cleanly stop isolated fresh candidate: %w", err)
	}
	inspected, err = c.inspectV010Container(ctx, name)
	if err != nil {
		return result, err
	}
	if err := validateV010FreshCandidateInspect(inspected, containerID, name, network, volume, runDirectory, candidate, imageID, false); err != nil {
		return result, err
	}
	freshAfter, freshAfterEntries, err := c.v010QualificationVolumeChecksum(ctx, candidate.Image, volume)
	if err != nil {
		return result, fmt.Errorf("inventory initialized candidate state: %w", err)
	}
	if freshBefore == freshAfter || freshAfterEntries == 0 {
		return result, fmt.Errorf("candidate installation did not create deterministic fresh state")
	}
	predecessorBefore, predecessorEntries, err := c.v010QualificationVolumeChecksum(ctx, candidate.Image, predecessorVolume)
	if err != nil {
		return result, fmt.Errorf("inventory preserved predecessor before denial: %w", err)
	}
	if predecessorEntries == 0 {
		return result, fmt.Errorf("preserved predecessor state is empty before denial qualification")
	}

	denials := make([]compatibility.V010LegacyDenialEvidence, 0, 3)
	for _, probe := range []struct {
		scenario string
		target   string
		home     string
	}{
		{scenario: "preserved-state-mount", target: v010CandidateStateMount, home: v010CandidateStateMount},
		{scenario: "legacy-state-reference", target: "/legacy-v010", home: "/legacy-v010"},
	} {
		if err := c.attemptV010LegacyStateAdoption(ctx, candidate.Image, predecessorVolume, probe.target, probe.home); err != nil {
			return result, fmt.Errorf("%s denial: %w", probe.scenario, err)
		}
		predecessorAfterProbe, _, checksumErr := c.v010QualificationVolumeChecksum(ctx, candidate.Image, predecessorVolume)
		if checksumErr != nil {
			return result, checksumErr
		}
		if predecessorAfterProbe != predecessorBefore {
			return result, fmt.Errorf("%s changed preserved predecessor state", probe.scenario)
		}
		denials = append(denials, compatibility.V010LegacyDenialEvidence{
			Scenario: probe.scenario, Operation: upgradeDenial.Operation, ReasonCode: upgradeDenial.ReasonCode,
			BeforeSHA256: predecessorBefore, AfterSHA256: predecessorAfterProbe, DeniedBeforeMutation: true,
		})
	}
	denials = append(denials, compatibility.V010LegacyDenialEvidence{
		Scenario: "direct-legacy-artifact-adoption", Operation: directAdoptionDenial.Operation,
		ReasonCode: directAdoptionDenial.ReasonCode, BeforeSHA256: freshAfter, AfterSHA256: freshAfter,
		DeniedBeforeMutation: true,
	})
	candidateAfterDenials, _, err := c.v010QualificationVolumeChecksum(ctx, candidate.Image, volume)
	if err != nil {
		return result, err
	}
	predecessorAfterDenials, _, err := c.v010QualificationVolumeChecksum(ctx, candidate.Image, predecessorVolume)
	if err != nil {
		return result, err
	}
	if candidateAfterDenials != freshAfter || predecessorAfterDenials != predecessorBefore {
		return result, fmt.Errorf("legacy adoption denial changed candidate or predecessor state")
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
	cleanupErr := cleanup(cleanupCtx)
	cancel()
	if cleanupErr != nil {
		return result, fmt.Errorf("clean up isolated fresh candidate: %w", cleanupErr)
	}
	if err := c.verifyV010Cleanup(ctx, name, network, volume, runDirectory); err != nil {
		return result, err
	}
	cleanupComplete = true
	return compatibility.V010FreshCandidateEvidence{
		RunID: runID, ContainerID: containerID, ContainerName: name, StateVolumeName: volume, NetworkName: network,
		Predecessor: predecessor.Identity, Candidate: candidate, CandidateImageID: imageID,
		PolicyVersion: policy.PolicyVersion, PolicySHA256: predecessor.PolicySHA256, FreshInstallDecision: freshDecision,
		FreshStateBeforeSHA256: freshBefore, FreshStateAfterSHA256: freshAfter,
		CandidateBeforeDenialsSHA256: freshAfter, CandidateAfterDenialsSHA256: candidateAfterDenials,
		PredecessorBeforeDenialsSHA256: predecessorBefore, PredecessorAfterDenialsSHA256: predecessorAfterDenials,
		CandidateInventory: inventory, Denials: denials, StartedAt: startedAt, CompletedAt: c.now().UTC(),
		CleanStateVerified: true, LegacyStateUnavailable: true, MutationFree: true, CleanupVerified: true,
	}, nil
}

func (c *Controller) resolveV010CandidateImage(ctx context.Context, candidate compatibility.ReleaseIdentity) (string, error) {
	if candidate.Platform != compatibility.ReleasedV010Platform || !strings.Contains(candidate.Image, "@sha256:") {
		return "", fmt.Errorf("candidate must use the policy-admitted immutable linux/amd64 artifact")
	}
	if _, err := c.qualificationDocker(ctx, nil, "pull", "--platform", candidate.Platform, candidate.Image); err != nil {
		return "", fmt.Errorf("pull exact admitted candidate artifact: %w", err)
	}
	document, err := c.qualificationDocker(ctx, nil, "image", "inspect", "--format", "{{json .}}", candidate.Image)
	if err != nil {
		return "", fmt.Errorf("inspect admitted candidate artifact: %w", err)
	}
	var image v010DockerImageInspect
	if err := decodeV010RegistryDocument(document, &image); err != nil {
		return "", err
	}
	if image.OS+"/"+image.Architecture != candidate.Platform || !containsQualificationString(image.RepoDigests, candidate.Image) ||
		image.Config.Labels["org.opencontainers.image.version"] != candidate.Version ||
		image.Config.Labels["org.opencontainers.image.revision"] != candidate.SourceRevision ||
		!strings.HasPrefix(image.ID, "sha256:") {
		return "", fmt.Errorf("candidate runtime identity does not match the admitted policy artifact")
	}
	return image.ID, nil
}

func (c *Controller) initializeV010FreshCandidate(ctx context.Context, image, volume string) (v010CandidateCredentials, error) {
	output, err := c.qualificationDocker(ctx, nil,
		"run", "--rm", "--platform", compatibility.ReleasedV010Platform,
		"--env", "LEAPVIEW_PRODUCTION=1", "--env", "LEAPVIEW_ENVIRONMENT=fai-517-candidate",
		"--env", "LEAPVIEW_HOME=/var/lib/leapview/home",
		"--env", "LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL="+v010CandidateAdminEmail,
		"--mount", "type=volume,source="+volume+",target="+v010CandidateStateMount,
		image, "admin", "initialize", "--format", "json",
	)
	if err != nil {
		return v010CandidateCredentials{}, fmt.Errorf("initialize isolated fresh candidate through supported CLI: %w", err)
	}
	var credentials v010CandidateCredentials
	if err := decodeV010JourneyJSON(output, &credentials); err != nil || credentials.Email != v010CandidateAdminEmail ||
		strings.TrimSpace(credentials.PublisherToken) == "" {
		return v010CandidateCredentials{}, fmt.Errorf("fresh candidate initialization credentials are incomplete")
	}
	return credentials, nil
}

func (c *Controller) collectV010FreshCandidateInventory(
	ctx context.Context,
	container string,
	credentials v010CandidateCredentials,
) (compatibility.V010FreshCandidateInventory, error) {
	call := func(arguments ...string) ([]byte, error) {
		dockerArguments := []string{"exec", "--env", "LEAPVIEW_API_TOKEN", container, "leapview"}
		dockerArguments = append(dockerArguments, arguments...)
		return c.qualificationDockerWithEnvironment(ctx, map[string]string{"LEAPVIEW_API_TOKEN": credentials.PublisherToken}, dockerArguments...)
	}
	listCount := func(operation string, arguments ...string) (int, error) {
		command := []string{"api", "call", operation, "--target", "http://127.0.0.1:8080"}
		command = append(command, arguments...)
		output, err := call(command...)
		if err != nil {
			return 0, err
		}
		var response struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := decodeV010JourneyJSON(output, &response); err != nil {
			return 0, err
		}
		return len(response.Items), nil
	}
	readyCtx, cancel := qualificationContext(ctx, v010ReadinessTimeout)
	defer cancel()
	var adminOutput []byte
	var lastErr error
	if err := qualificationWait(readyCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		var callErr error
		adminOutput, callErr = func() ([]byte, error) {
			dockerArguments := []string{"exec", "--env", "LEAPVIEW_API_TOKEN", container, "leapview", "api", "call", "listPrincipals",
				"--target", "http://127.0.0.1:8080", "--query", "email=" + v010CandidateAdminEmail}
			return c.qualificationDockerWithEnvironment(requestCtx, map[string]string{"LEAPVIEW_API_TOKEN": credentials.PublisherToken}, dockerArguments...)
		}()
		lastErr = callErr
		return callErr == nil, nil
	}); err != nil {
		return compatibility.V010FreshCandidateInventory{}, fmt.Errorf("fresh candidate API readiness: %w", errors.Join(err, lastErr))
	}
	var adminResponse struct {
		Items []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"items"`
	}
	if err := decodeV010JourneyJSON(adminOutput, &adminResponse); err != nil || len(adminResponse.Items) != 1 ||
		adminResponse.Items[0].ID == "" || adminResponse.Items[0].Email != v010CandidateAdminEmail {
		return compatibility.V010FreshCandidateInventory{}, fmt.Errorf("fresh candidate administrator identity is invalid")
	}
	legacyPrincipals := 0
	for _, email := range []string{v010AdminEmail, v010UserEmail} {
		count, err := listCount("listPrincipals", "--query", "email="+email)
		if err != nil {
			return compatibility.V010FreshCandidateInventory{}, err
		}
		legacyPrincipals += count
	}
	dashboards, err := listCount("listDashboards")
	if err != nil {
		return compatibility.V010FreshCandidateInventory{}, err
	}
	semanticModels, err := listCount("listSemanticModels")
	if err != nil {
		return compatibility.V010FreshCandidateInventory{}, err
	}
	if legacyPrincipals != 0 || dashboards != 0 || semanticModels != 0 {
		return compatibility.V010FreshCandidateInventory{}, fmt.Errorf("fresh candidate exposed preserved v0.1 principals, project, or managed data")
	}
	return compatibility.V010FreshCandidateInventory{
		AdminEmail: credentials.Email, AdminPrincipalID: adminResponse.Items[0].ID,
		LegacyPrincipalCount: legacyPrincipals, DashboardCount: dashboards, SemanticModelCount: semanticModels,
		LegacyProjectVisible: false, LegacyManagedDataVisible: false,
	}, nil
}

func (c *Controller) attemptV010LegacyStateAdoption(ctx context.Context, image, volume, target, home string) error {
	_, err := c.qualificationDocker(ctx, nil,
		"run", "--rm", "--platform", compatibility.ReleasedV010Platform,
		"--env", "LEAPVIEW_PRODUCTION=1", "--env", "LEAPVIEW_ENVIRONMENT=fai-517-candidate",
		"--env", "LEAPVIEW_HOME="+home, "--env", "LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL="+v010CandidateAdminEmail,
		"--mount", "type=volume,source="+volume+",target="+target,
		image, "admin", "initialize", "--format", "json",
	)
	if err == nil || !strings.Contains(err.Error(), compatibility.ErrV010FreshInstallOnly.Error()) {
		return fmt.Errorf("candidate did not reject preserved v0.1 state before initialization: %v", err)
	}
	return nil
}

func (c *Controller) v010QualificationVolumeChecksum(ctx context.Context, image, volume string) (string, int, error) {
	document, err := c.qualificationDocker(ctx, nil,
		"run", "--rm", "--read-only", "--user", "0:0", "--entrypoint", "tar",
		"--mount", "type=volume,source="+volume+",target=/state,readonly",
		image, "--sort=name", "--mtime=UTC 1970-01-01", "--owner=0", "--group=0", "--numeric-owner", "-C", "/state", "-cf", "-", ".",
	)
	if err != nil {
		return "", 0, fmt.Errorf("read deterministic state archive: %w", err)
	}
	entries := 0
	reader := tar.NewReader(bytes.NewReader(document))
	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, fmt.Errorf("decode deterministic state archive: %w", readErr)
		}
		name := filepath.Clean(header.Name)
		if name == "." || header.Typeflag == tar.TypeDir {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", 0, fmt.Errorf("state archive contains an unsafe path")
		}
		entries++
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), entries, nil
}

func validateV010FreshCandidateInspect(
	inspected v010ContainerInspect,
	containerID, name, network, volume, runDirectory string,
	candidate compatibility.ReleaseIdentity,
	imageID string,
	running bool,
) error {
	wantedState := "exited"
	if running {
		wantedState = "running"
	}
	if inspected.ID != containerID || inspected.Name != "/"+name || inspected.Image != imageID ||
		inspected.Config.Image != candidate.Image || inspected.State.Running != running || inspected.State.Status != wantedState ||
		inspected.HostConfig.NetworkMode != network || len(inspected.NetworkSettings.Networks) != 1 {
		return fmt.Errorf("fresh candidate container identity or lifecycle does not match admitted artifact")
	}
	if _, ok := inspected.NetworkSettings.Networks[network]; !ok {
		return fmt.Errorf("fresh candidate is not attached only to its isolated network")
	}
	var stateMount, runMount bool
	for _, mount := range inspected.Mounts {
		switch mount.Destination {
		case v010CandidateStateMount:
			stateMount = mount.Type == "volume" && mount.Name == volume && mount.RW
		case v010CandidateRunMount:
			runMount = mount.Type == "bind" && filepath.Clean(mount.Source) == filepath.Clean(runDirectory) && !mount.RW
		case v010StateMount:
			return fmt.Errorf("fresh candidate can access preserved v0.1 state")
		}
	}
	if !stateMount || !runMount {
		return fmt.Errorf("fresh candidate does not use its isolated state and run directory")
	}
	return nil
}
