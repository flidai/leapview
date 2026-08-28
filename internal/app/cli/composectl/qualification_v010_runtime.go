package composectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

const (
	v010OCIIndexMediaType    = "application/vnd.oci.image.index.v1+json"
	v010OCIManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	v010OCIConfigMediaType   = "application/vnd.oci.image.config.v1+json"
	v010StateMount           = "/var/lib/libredash"
	v010QualificationMount   = "/qualification"
)

type dockerV010ArtifactResolver struct {
	process    qualificationProcess
	executor   qualificationCommandExecutor
	configPath string
}

type v010OCIDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform,omitempty"`
}

type v010OCIIndex struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType"`
	Manifests     []v010OCIDescriptor `json:"manifests"`
}

type v010OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        v010OCIDescriptor `json:"config"`
}

type v010DockerImageInspect struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	Architecture string   `json:"Architecture"`
	OS           string   `json:"Os"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func newDockerV010ArtifactResolver(root, dockerBin string, executor qualificationCommandExecutor) *dockerV010ArtifactResolver {
	return &dockerV010ArtifactResolver{
		process:  qualificationProcess{dir: root, executable: dockerBin, environment: os.Environ()},
		executor: executor,
	}
}

func (resolver *dockerV010ArtifactResolver) ResolveExact(
	ctx context.Context,
	request compatibility.V010ArtifactResolutionRequest,
) (compatibility.V010ResolvedArtifact, error) {
	if request.Image != compatibility.ReleasedV010Image || request.Platform != compatibility.ReleasedV010Platform ||
		!request.RequireAuthentication {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("v0.1 resolver accepts only the authenticated exact policy-selected linux/amd64 artifact")
	}
	if err := resolver.requireRegistryCredentials(); err != nil {
		return compatibility.V010ResolvedArtifact{}, err
	}

	indexDocument, err := resolver.run(ctx, "buildx", "imagetools", "inspect", "--raw", request.Image)
	if err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("resolve authenticated v0.1 OCI index: %w", err)
	}
	indexDigest := qualificationSHA256Digest(indexDocument)
	if indexDigest != request.Image[strings.LastIndex(request.Image, "@")+1:] {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("v0.1 registry bytes do not match the policy-selected immutable digest")
	}
	var index v010OCIIndex
	if err := decodeV010RegistryDocument(indexDocument, &index); err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("decode v0.1 OCI index: %w", err)
	}
	if index.SchemaVersion != 2 || index.MediaType != v010OCIIndexMediaType {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("v0.1 registry object is not the reviewed OCI image index")
	}
	platformDescriptor, err := selectV010PlatformManifest(index.Manifests)
	if err != nil {
		return compatibility.V010ResolvedArtifact{}, err
	}
	platformReference := compatibility.ReleasedV010Repository + "@" + platformDescriptor.Digest
	manifestDocument, err := resolver.run(ctx, "buildx", "imagetools", "inspect", "--raw", platformReference)
	if err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("resolve authenticated v0.1 linux/amd64 manifest: %w", err)
	}
	if qualificationSHA256Digest(manifestDocument) != platformDescriptor.Digest || int64(len(manifestDocument)) != platformDescriptor.Size {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("v0.1 linux/amd64 manifest bytes do not match the OCI index descriptor")
	}
	var manifest v010OCIManifest
	if err := decodeV010RegistryDocument(manifestDocument, &manifest); err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("decode v0.1 linux/amd64 manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != v010OCIManifestMediaType ||
		manifest.Config.MediaType != v010OCIConfigMediaType || manifest.Config.Digest == "" || manifest.Config.Size <= 0 {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("v0.1 linux/amd64 manifest has an invalid OCI config descriptor")
	}

	if _, err := resolver.run(ctx, "pull", "--platform", compatibility.ReleasedV010Platform, request.Image); err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("pull authenticated exact v0.1 artifact: %w", err)
	}
	inspectDocument, err := resolver.run(ctx, "image", "inspect", "--format", "{{json .}}", request.Image)
	if err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("inspect exact v0.1 artifact: %w", err)
	}
	var image v010DockerImageInspect
	if err := decodeV010RegistryDocument(inspectDocument, &image); err != nil {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("decode v0.1 image inspection: %w", err)
	}
	if image.ID != manifest.Config.Digest || image.OS+"/"+image.Architecture != compatibility.ReleasedV010Platform ||
		(!containsQualificationString(image.RepoDigests, request.Image) &&
			!containsQualificationString(image.RepoDigests, platformReference)) {
		return compatibility.V010ResolvedArtifact{}, fmt.Errorf("local v0.1 image identity does not match the exact pulled registry artifact")
	}
	labels := image.Config.Labels
	return compatibility.V010ResolvedArtifact{
		Image: request.Image, ResolvedDigest: indexDigest, Platform: compatibility.ReleasedV010Platform,
		PlatformManifestDigest: platformDescriptor.Digest, ConfigDigest: manifest.Config.Digest,
		Authenticated: true, SourceRepository: labels["org.opencontainers.image.source"],
		SourceTag: compatibility.ReleasedV010ID, Version: labels["org.opencontainers.image.version"],
		SourceRevision: labels["org.opencontainers.image.revision"],
	}, nil
}

func (resolver *dockerV010ArtifactResolver) run(ctx context.Context, arguments ...string) ([]byte, error) {
	return resolver.process.Run(ctx, nil, resolver.executor, arguments...)
}

func (resolver *dockerV010ArtifactResolver) requireRegistryCredentials() error {
	path := strings.TrimSpace(resolver.configPath)
	if path == "" {
		var err error
		path, err = v010DockerConfigPath()
		if err != nil {
			return err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("v0.1 authenticated registry credentials are required in %s: %w", path, err)
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return fmt.Errorf("read Docker credential configuration: %w", err)
	}
	if len(document) == 0 || len(document) > 1<<20 {
		return fmt.Errorf("Docker credential configuration is empty or exceeds 1 MiB")
	}
	var config struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
		CredHelpers map[string]string `json:"credHelpers"`
		CredsStore  string            `json:"credsStore"`
	}
	if err := json.Unmarshal(document, &config); err != nil {
		return fmt.Errorf("decode Docker credential configuration: %w", err)
	}
	for _, registry := range []string{"ghcr.io", "https://ghcr.io", "https://ghcr.io/v1/"} {
		entry, configured := config.Auths[registry]
		helper := strings.TrimSpace(config.CredHelpers[registry])
		if helper != "" || validV010DockerAuth(entry.Auth) || strings.TrimSpace(entry.IdentityToken) != "" ||
			(configured && strings.TrimSpace(config.CredsStore) != "") {
			return nil
		}
	}
	return fmt.Errorf("v0.1 authenticated registry credentials for ghcr.io are not configured")
}

func validV010DockerAuth(value string) bool {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	username, secret, found := strings.Cut(string(decoded), ":")
	return found && strings.TrimSpace(username) != "" && strings.TrimSpace(secret) != ""
}

func v010DockerConfigPath() (string, error) {
	if root := strings.TrimSpace(os.Getenv("DOCKER_CONFIG")); root != "" {
		return filepath.Join(root, "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Docker credential configuration: %w", err)
	}
	return filepath.Join(home, ".docker", "config.json"), nil
}

func selectV010PlatformManifest(manifests []v010OCIDescriptor) (v010OCIDescriptor, error) {
	var selected v010OCIDescriptor
	count := 0
	for _, descriptor := range manifests {
		if descriptor.Platform == nil || descriptor.Platform.OS != "linux" || descriptor.Platform.Architecture != "amd64" {
			continue
		}
		if descriptor.MediaType != v010OCIManifestMediaType || descriptor.Digest == "" || descriptor.Size <= 0 {
			return v010OCIDescriptor{}, fmt.Errorf("v0.1 linux/amd64 OCI descriptor is invalid")
		}
		selected = descriptor
		count++
	}
	if count != 1 {
		return v010OCIDescriptor{}, fmt.Errorf("v0.1 OCI index must contain exactly one linux/amd64 runtime manifest, found %d", count)
	}
	return selected, nil
}

func decodeV010RegistryDocument(document []byte, target any) error {
	if len(document) == 0 || len(document) > 1<<20 {
		return fmt.Errorf("registry document is empty or exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("registry document contains trailing data")
	}
	return nil
}

func qualificationSHA256Digest(document []byte) string {
	digest := sha256.Sum256(document)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type v010ContainerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

// qualifyV010ArtifactExecution verifies the exact registry artifact, executes
// the supported v0.1 application journey, and then cleanly stops it in an
// isolated run directory, network, volume, and container.
func (c *Controller) qualifyV010ArtifactExecution(
	ctx context.Context,
	policyDocument []byte,
) (evidence compatibility.V010ReleaseIdentityEvidence, runErr error) {
	resolver := newDockerV010ArtifactResolver(c.root, c.dockerBin, c.qualificationExecutor)
	evidence, err := compatibility.VerifyReleasedV010Artifact(ctx, compatibility.V010ArtifactVerificationOptions{
		PolicyDocument: policyDocument, Resolver: resolver, Now: c.now,
	})
	if err != nil {
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	runID, err := qualificationRandomHex(16)
	if err != nil {
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	name := "leapview-v010-" + runID
	network := name + "-network"
	volume := name + "-state"
	runDirectory, err := os.MkdirTemp(c.root, ".v010-run-"+runID+"-")
	if err != nil {
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	// The directory contains only checked-in deterministic fixtures. It must be
	// traversable by the released image's unprivileged libredash user.
	if err := os.Chmod(runDirectory, 0o755); err != nil {
		_ = os.RemoveAll(runDirectory)
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	if err := prepareV010QualificationProject(runDirectory); err != nil {
		_ = os.RemoveAll(runDirectory)
		return compatibility.V010ReleaseIdentityEvidence{}, fmt.Errorf("prepare deterministic v0.1 project: %w", err)
	}
	csrfKey, err := qualificationRandomHex(32)
	if err != nil {
		_ = os.RemoveAll(runDirectory)
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	metricsToken, err := qualificationRandomHex(32)
	if err != nil {
		_ = os.RemoveAll(runDirectory)
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	var networkCreated, volumeCreated, containerCreated, cleanupComplete bool
	cleanup := func(cleanupCtx context.Context) error {
		var cleanupErr error
		if containerCreated {
			_, err := c.qualificationDocker(cleanupCtx, nil, "rm", "--force", name)
			cleanupErr = errors.Join(cleanupErr, ignoreQualificationNotFound(err))
		}
		if volumeCreated {
			_, err := c.qualificationDocker(cleanupCtx, nil, "volume", "rm", volume)
			cleanupErr = errors.Join(cleanupErr, ignoreQualificationNotFound(err))
		}
		if networkCreated {
			_, err := c.qualificationDocker(cleanupCtx, nil, "network", "rm", network)
			cleanupErr = errors.Join(cleanupErr, ignoreQualificationNotFound(err))
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
	networkArguments := append([]string{"network", "create", "--driver", "bridge", "--internal"}, labels...)
	networkArguments = append(networkArguments, network)
	if _, err := c.qualificationDocker(ctx, nil, networkArguments...); err != nil {
		return evidence, fmt.Errorf("create isolated v0.1 network: %w", err)
	}
	networkCreated = true
	volumeArguments := append([]string{"volume", "create"}, labels...)
	volumeArguments = append(volumeArguments, volume)
	if _, err := c.qualificationDocker(ctx, nil, volumeArguments...); err != nil {
		return evidence, fmt.Errorf("create isolated v0.1 state volume: %w", err)
	}
	volumeCreated = true
	startedAt := c.now().UTC()
	runOutput, err := c.qualificationDockerWithEnvironment(ctx, map[string]string{
		"LIBREDASH_CSRF_KEY": csrfKey, "LIBREDASH_METRICS_BEARER_TOKEN": metricsToken,
	},
		"run", "--detach", "--name", name, "--hostname", "libredash-v010",
		"--network", network, "--platform", compatibility.ReleasedV010Platform,
		"--pull", "never", "--restart", "no", "--read-only",
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL", "--pids-limit", "512",
		"--env", "LIBREDASH_LOCAL_AUTH=1",
		"--env", "LIBREDASH_COOKIE_SECURE=1",
		"--env", "LIBREDASH_ALLOWED_HOSTS=127.0.0.1,localhost",
		"--env", "LIBREDASH_BOOTSTRAP_ADMIN_EMAIL="+v010AdminEmail,
		"--env", "LIBREDASH_DATA_DIR="+v010QualificationMount+"/data",
		"--env", "LIBREDASH_CSRF_KEY", "--env", "LIBREDASH_METRICS_BEARER_TOKEN",
		"--mount", "type=volume,source="+volume+",target="+v010StateMount,
		"--mount", "type=bind,source="+runDirectory+",target="+v010QualificationMount+",readonly",
		"--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
		labels[0], labels[1], labels[2], labels[3], evidence.Identity.Image,
	)
	if err != nil {
		return evidence, fmt.Errorf("start isolated exact v0.1 artifact: %w", err)
	}
	containerCreated = true
	containerID := strings.TrimSpace(string(runOutput))
	running, err := c.inspectV010Container(ctx, name)
	if err != nil {
		return evidence, err
	}
	if err := validateV010ContainerInspect(running, containerID, name, network, volume, runDirectory, evidence, true); err != nil {
		return evidence, err
	}
	journeyResult, err := c.runV010ApplicationJourney(ctx, name)
	if err != nil {
		return evidence, err
	}
	beforeInventory, beforeDigest, err := c.collectV010StateInventory(
		ctx, name, containerID, journeyResult.token, evidence, journeyResult.evidence,
	)
	if err != nil {
		return evidence, fmt.Errorf("collect v0.1 inventory before clean shutdown: %w", err)
	}
	observedBeforeShutdownAt := c.now().UTC()
	if _, err := c.qualificationDocker(ctx, nil, "stop", "--time", "30", name); err != nil {
		return evidence, fmt.Errorf("cleanly stop isolated v0.1 container: %w", err)
	}
	shutdownAt := c.now().UTC()
	stopped, err := c.inspectV010Container(ctx, name)
	if err != nil {
		return evidence, err
	}
	if err := validateV010ContainerInspect(stopped, containerID, name, network, volume, runDirectory, evidence, false); err != nil {
		return evidence, err
	}
	if _, err := c.qualificationDocker(ctx, nil, "start", name); err != nil {
		return evidence, fmt.Errorf("restart preserved v0.1 container for stopped-state inspection: %w", err)
	}
	restartedAt := c.now().UTC()
	restarted, err := c.inspectV010Container(ctx, name)
	if err != nil {
		return evidence, err
	}
	if err := validateV010ContainerInspect(restarted, containerID, name, network, volume, runDirectory, evidence, true); err != nil {
		return evidence, fmt.Errorf("verify restarted v0.1 application identity: %w", err)
	}
	if err := c.waitV010Readiness(ctx, name); err != nil {
		return evidence, fmt.Errorf("wait for preserved v0.1 state inspection: %w", err)
	}
	afterInventory, afterDigest, err := c.collectV010StateInventory(
		ctx, name, containerID, journeyResult.token, evidence, journeyResult.evidence,
	)
	if err != nil {
		return evidence, fmt.Errorf("collect v0.1 inventory after clean shutdown and restart: %w", err)
	}
	observedAfterRestartAt := c.now().UTC()
	if beforeDigest != afterDigest || !reflect.DeepEqual(beforeInventory, afterInventory) {
		return evidence, fmt.Errorf("v0.1 state inventory changed across clean shutdown and restart")
	}
	if _, err := c.qualificationDocker(ctx, nil, "stop", "--time", "30", name); err != nil {
		return evidence, fmt.Errorf("cleanly stop inspected v0.1 container: %w", err)
	}
	stoppedAt := c.now().UTC()
	stopped, err = c.inspectV010Container(ctx, name)
	if err != nil {
		return evidence, err
	}
	if err := validateV010ContainerInspect(stopped, containerID, name, network, volume, runDirectory, evidence, false); err != nil {
		return evidence, err
	}
	freshCandidate, err := c.qualifyV010FreshCandidate(ctx, policyDocument, evidence, volume)
	if err != nil {
		return evidence, fmt.Errorf("qualify isolated fresh candidate and legacy-state denial: %w", err)
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
	cleanupErr := cleanup(cleanupCtx)
	cancel()
	if cleanupErr != nil {
		return evidence, fmt.Errorf("clean up isolated v0.1 execution resources: %w", cleanupErr)
	}
	if err := c.verifyV010Cleanup(ctx, name, network, volume, runDirectory); err != nil {
		return evidence, err
	}
	cleanupComplete = true
	evidence.Execution = &compatibility.V010ContainerExecution{
		RunID: runID, ContainerID: containerID, ContainerName: name,
		Image: evidence.Identity.Image, ImageID: evidence.Artifact.ConfigDigest,
		Platform: compatibility.ReleasedV010Platform, NetworkName: network, StateVolumeName: volume,
		StartedAt: startedAt, StoppedAt: stoppedAt, StartedState: "running", StoppedState: "exited",
		CleanShutdown: true, CleanupVerified: true, Journey: &journeyResult.evidence,
		Preservation: &compatibility.V010PreservationEvidence{
			ObservedBeforeShutdownAt: observedBeforeShutdownAt, ShutdownAt: shutdownAt,
			RestartedAt: restartedAt, ObservedAfterRestartAt: observedAfterRestartAt,
			Inventory: beforeInventory, BeforeSHA256: beforeDigest, AfterSHA256: afterDigest,
			RestartIdentityVerified: true, StatePreserved: true,
		},
		FreshCandidate: &freshCandidate,
	}
	if _, err := compatibility.MarshalV010ReleaseIdentityEvidence(evidence, policyDocument); err != nil {
		return compatibility.V010ReleaseIdentityEvidence{}, err
	}
	return evidence, nil
}

func (c *Controller) verifyV010Cleanup(ctx context.Context, name, network, volume, runDirectory string) error {
	checks := []struct {
		resource  string
		arguments []string
	}{
		{resource: "container", arguments: []string{"ps", "--all", "--quiet", "--filter", "name=^/" + name + "$"}},
		{resource: "volume", arguments: []string{"volume", "ls", "--quiet", "--filter", "name=^" + volume + "$"}},
		{resource: "network", arguments: []string{"network", "ls", "--quiet", "--filter", "name=^" + network + "$"}},
	}
	for _, check := range checks {
		output, err := c.qualificationDocker(ctx, nil, check.arguments...)
		if err != nil {
			return fmt.Errorf("verify isolated v0.1 %s cleanup: %w", check.resource, err)
		}
		if strings.TrimSpace(string(output)) != "" {
			return fmt.Errorf("isolated v0.1 %s still exists after cleanup", check.resource)
		}
	}
	if _, err := os.Lstat(runDirectory); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("isolated v0.1 run directory still exists after cleanup")
		}
		return fmt.Errorf("verify isolated v0.1 run directory cleanup: %w", err)
	}
	return nil
}

func (c *Controller) inspectV010Container(ctx context.Context, name string) (v010ContainerInspect, error) {
	document, err := c.qualificationDocker(ctx, nil, "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return v010ContainerInspect{}, fmt.Errorf("inspect isolated v0.1 container: %w", err)
	}
	var inspected v010ContainerInspect
	if err := decodeV010RegistryDocument(document, &inspected); err != nil {
		return v010ContainerInspect{}, fmt.Errorf("decode isolated v0.1 container inspection: %w", err)
	}
	return inspected, nil
}

func validateV010ContainerInspect(
	inspected v010ContainerInspect,
	containerID, name, network, volume, runDirectory string,
	evidence compatibility.V010ReleaseIdentityEvidence,
	running bool,
) error {
	wantedState := "exited"
	if running {
		wantedState = "running"
	}
	if inspected.ID != containerID || inspected.Name != "/"+name || inspected.Image != evidence.Artifact.ConfigDigest ||
		inspected.Config.Image != evidence.Identity.Image || inspected.State.Running != running || inspected.State.Status != wantedState ||
		inspected.HostConfig.NetworkMode != network {
		return fmt.Errorf("isolated v0.1 container identity or lifecycle state does not match the verified artifact")
	}
	if len(inspected.NetworkSettings.Networks) != 1 {
		return fmt.Errorf("isolated v0.1 container must have exactly one network")
	}
	if _, ok := inspected.NetworkSettings.Networks[network]; !ok {
		return fmt.Errorf("isolated v0.1 container is not attached to its private network")
	}
	var stateMount, qualificationMount bool
	for _, mount := range inspected.Mounts {
		switch mount.Destination {
		case v010StateMount:
			stateMount = mount.Type == "volume" && mount.Name == volume && mount.RW
		case v010QualificationMount:
			qualificationMount = mount.Type == "bind" && mount.Source == runDirectory && !mount.RW
		}
	}
	if !stateMount || !qualificationMount {
		return fmt.Errorf("isolated v0.1 container does not use the dedicated state volume and read-only run directory")
	}
	return nil
}
