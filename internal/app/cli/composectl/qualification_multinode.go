package composectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	qualificationMultiNodeRootCertificate = "/etc/ssl/certs/leapview-qualification-postgres-ca.pem"
	qualificationMultiNodeStateTmpfs      = "/var/lib/leapview:rw,exec,nosuid,nodev,mode=0700,uid=999,gid=999,size=512m"
	qualificationMultiNodePoolDirectory   = "/var/lib/leapview/home/data"
	qualificationMultiNodeExtensionCache  = "/var/lib/leapview/home/duckdb-extension-cache"
	qualificationMultiNodeObjectStore     = "/var/lib/leapview/home/artifacts/object-store"
)

var qualificationMultiNodeScopeIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,254}$`)

// qualificationMultiNodeOptions describes an already initialized target. The
// first application process belongs to the Compose lifecycle; the second is a
// disposable process on the same network and is deliberately given a separate
// state directory so the test cannot pass by sharing an in-process lock or
// filesystem cache.
type qualificationMultiNodeOptions struct {
	Image          string
	ComposeProject string
	ComposeNetwork string
	TargetID       string
	GenerationID   string
	WorkloadToken  string
	Topology       *qualificationNativePostgresTopology
	Primary        qualificationContainer
}

type qualificationMultiNodeReport struct {
	NodeCount          int  `json:"nodeCount"`
	AbruptNodeLoss     bool `json:"abruptNodeLoss"`
	Recovery           bool `json:"recovery"`
	RollingRestart     bool `json:"rollingRestart"`
	DurableConvergence bool `json:"durableConvergence"`
	DataPlaneQueries   bool `json:"dataPlaneQueries"`
}

type qualificationMultiNodeInstance struct {
	ID              string `json:"id"`
	CanonicalOrigin string `json:"canonicalOrigin"`
	Environment     string `json:"environment"`
}

// runQualificationMultiNode is the process-level FAI-572 qualification. It
// intentionally runs only after the normal installed-candidate journey has
// produced a sealed active generation. The two processes share the native
// PostgreSQL authority but never share local application state.
func (c *Controller) runQualificationMultiNode(
	ctx context.Context,
	options qualificationMultiNodeOptions,
) (report qualificationMultiNodeReport, runErr error) {
	if c == nil {
		return report, errors.New("controller is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateQualificationMultiNodeOptions(options); err != nil {
		return report, err
	}
	if c.qualificationContainers == nil {
		return report, errors.New("multi-node qualification container runtime is required")
	}
	environment, err := qualificationMultiNodeEnvironment(c.path(appEnvName))
	if err != nil {
		return report, err
	}
	volumes, err := qualificationMultiNodeVolumes(options, environment)
	if err != nil {
		return report, err
	}
	nodeName := qualificationMultiNodeContainerName(options.ComposeProject)
	secondary, err := c.qualificationContainers.Start(ctx, qualificationContainerRequest{
		Name:        nodeName,
		Image:       strings.TrimSpace(options.Image),
		NetworkMode: strings.TrimSpace(options.ComposeNetwork),
		ReadOnly:    true,
		Volumes:     volumes,
		Tmpfs: []string{
			"/tmp:rw,nosuid,nodev,mode=1777,size=64m",
			// The image runs as uid/gid 999. A host bind created by the
			// unprivileged qualification CLI is root-owned inside Docker and
			// cannot be hardened by that user. A private, uid-owned tmpfs gives
			// the disposable node isolated writable state without sharing the
			// primary filesystem or weakening directory permissions.
			qualificationMultiNodeStateTmpfs,
		},
		Environment: environment,
	})
	if err != nil {
		return report, fmt.Errorf("start second qualification application node: %w", err)
	}
	if secondary == nil {
		return report, errors.New("start second qualification application node returned a nil container")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		_, cleanupErr := secondary.Remove(cleanupCtx)
		runErr = errors.Join(runErr, ignoreQualificationNotFound(cleanupErr))
	}()

	if err := qualificationWaitMultiNodeReady(ctx, options.Primary, "primary node before multi-node fault"); err != nil {
		return report, err
	}
	if err := qualificationWaitMultiNodeReady(ctx, secondary, "secondary node startup"); err != nil {
		return report, qualificationContainerOperationError(
			ctx,
			secondary,
			"wait for secondary node startup readiness",
			err,
		)
	}
	if err := assertQualificationMultiNodeIdentity(ctx, options.Primary, secondary, options.WorkloadToken); err != nil {
		return report, err
	}
	if err := assertQualificationMultiNodeQuery(ctx, secondary, options.WorkloadToken, "secondary node startup"); err != nil {
		return report, err
	}
	if err := options.Topology.AssertDurableActivePointer(ctx, options.TargetID, options.GenerationID); err != nil {
		return report, fmt.Errorf("verify durable active pointer before multi-node fault: %w", err)
	}
	report.NodeCount = 2

	// Compose's service has an unless-stopped policy. Disable it for the
	// faulted process before sending SIGKILL; otherwise Docker could recreate
	// the node during the loss window and turn this into a single-process test.
	if _, err := c.qualificationDocker(ctx, nil, "update", "--restart=no", options.Primary.Name()); err != nil {
		return report, fmt.Errorf("disable primary qualification node restart policy: %w", err)
	}
	if _, err := options.Primary.Kill(ctx, "KILL"); err != nil {
		return report, fmt.Errorf("abruptly kill primary qualification node: %w", err)
	}
	if err := waitQualificationContainerValue(ctx, options.Primary, "{{.State.Status}}", "exited", time.Minute); err != nil {
		return report, fmt.Errorf("wait for primary qualification node loss: %w", err)
	}
	if err := qualificationWaitMultiNodeReady(ctx, secondary, "secondary node after primary loss"); err != nil {
		return report, fmt.Errorf("secondary qualification node did not survive primary loss: %w", err)
	}
	if err := assertQualificationMultiNodeQuery(ctx, secondary, options.WorkloadToken, "secondary node after primary loss"); err != nil {
		return report, err
	}
	if err := options.Topology.AssertDurableActivePointer(ctx, options.TargetID, options.GenerationID); err != nil {
		return report, fmt.Errorf("verify durable active pointer after primary loss: %w", err)
	}
	report.AbruptNodeLoss = true

	if _, err := options.Primary.Start(ctx); err != nil {
		return report, fmt.Errorf("recover primary qualification node: %w", err)
	}
	if err := qualificationWaitMultiNodeReady(ctx, options.Primary, "primary node recovery"); err != nil {
		return report, err
	}
	if err := qualificationWaitMultiNodeReady(ctx, secondary, "secondary node during primary recovery"); err != nil {
		return report, err
	}
	if err := options.Topology.AssertDurableActivePointer(ctx, options.TargetID, options.GenerationID); err != nil {
		return report, fmt.Errorf("verify durable active pointer after primary recovery: %w", err)
	}
	report.Recovery = true

	// Restart one node at a time while the other remains ready. Repeating the
	// durable pointer assertion after each restart catches a process that only
	// appears healthy because it retained an in-memory serving generation.
	if _, err := options.Primary.Restart(ctx); err != nil {
		return report, fmt.Errorf("rolling restart primary qualification node: %w", err)
	}
	if err := qualificationWaitMultiNodeReady(ctx, options.Primary, "primary node rolling restart"); err != nil {
		return report, err
	}
	if err := assertQualificationMultiNodeQuery(ctx, options.Primary, options.WorkloadToken, "primary node rolling restart"); err != nil {
		return report, err
	}
	if err := qualificationWaitMultiNodeReady(ctx, secondary, "secondary node during primary rolling restart"); err != nil {
		return report, err
	}
	if err := options.Topology.AssertDurableActivePointer(ctx, options.TargetID, options.GenerationID); err != nil {
		return report, fmt.Errorf("verify durable active pointer after primary rolling restart: %w", err)
	}

	if _, err := secondary.Restart(ctx); err != nil {
		return report, fmt.Errorf("rolling restart secondary qualification node: %w", err)
	}
	if err := qualificationWaitMultiNodeReady(ctx, secondary, "secondary node rolling restart"); err != nil {
		return report, err
	}
	if err := assertQualificationMultiNodeQuery(ctx, secondary, options.WorkloadToken, "secondary node rolling restart"); err != nil {
		return report, err
	}
	if err := qualificationWaitMultiNodeReady(ctx, options.Primary, "primary node during secondary rolling restart"); err != nil {
		return report, err
	}
	if err := assertQualificationMultiNodeIdentity(ctx, options.Primary, secondary, options.WorkloadToken); err != nil {
		return report, err
	}
	if err := options.Topology.AssertDurableActivePointer(ctx, options.TargetID, options.GenerationID); err != nil {
		return report, fmt.Errorf("verify durable active pointer after secondary rolling restart: %w", err)
	}
	report.RollingRestart = true
	report.DurableConvergence = true
	report.DataPlaneQueries = true
	return report, nil
}

func qualificationMultiNodeVolumes(
	options qualificationMultiNodeOptions,
	environment map[string]string,
) ([]qualificationContainerVolume, error) {
	volumes := []qualificationContainerVolume{
		{Source: filepath.Join(options.Topology.secretDir, "ca.pem"), Target: qualificationMultiNodeRootCertificate, ReadOnly: true},
		{
			Source: strings.TrimSpace(options.ComposeProject) + "_leapview-state",
			Target: qualificationMultiNodePoolDirectory, Subpath: "home/data",
		},
		{
			Source: strings.TrimSpace(options.ComposeProject) + "_leapview-state",
			Target: qualificationMultiNodeExtensionCache, Subpath: "home/duckdb-extension-cache",
		},
	}
	objectStoreBackend := strings.ToLower(strings.TrimSpace(environment["LEAPVIEW_OBJECT_STORE_BACKEND"]))
	if objectStoreBackend == "" || objectStoreBackend == "filesystem" {
		volumes = append(volumes, qualificationContainerVolume{
			Source: strings.TrimSpace(options.ComposeProject) + "_leapview-state",
			Target: qualificationMultiNodeObjectStore, Subpath: "home/artifacts/object-store",
		})
	} else if objectStoreBackend != "s3" {
		return nil, fmt.Errorf("multi-node qualification object-store backend %q is unsupported", objectStoreBackend)
	}
	backend := strings.ToLower(strings.TrimSpace(environment["LEAPVIEW_MANAGED_DATA_BACKEND"]))
	if backend == "s3" {
		return volumes, nil
	}
	if backend != "" && backend != "local" {
		return nil, fmt.Errorf("multi-node qualification managed-data backend %q is unsupported", backend)
	}
	directory := strings.TrimSpace(environment["LEAPVIEW_MANAGED_DATA_DIR"])
	if directory == "" {
		directory = "/var/lib/leapview/home/managed-data"
	}
	const stateRoot = "/var/lib/leapview"
	directory = filepath.ToSlash(filepath.Clean(directory))
	if !strings.HasPrefix(directory, stateRoot+"/") {
		return nil, errors.New("multi-node qualification local managed-data directory must be inside the LeapView state volume")
	}
	subpath := strings.TrimPrefix(directory, stateRoot+"/")
	if subpath == "" || subpath == "." || strings.HasPrefix(subpath, "../") {
		return nil, errors.New("multi-node qualification local managed-data directory is invalid")
	}
	// Keep each process's locks, cache, and home private. Only the managed-data
	// backing referenced by the active generation is shared. It must remain
	// writable because module startup re-applies restrictive directory modes.
	volumes = append(volumes, qualificationContainerVolume{
		Source:  strings.TrimSpace(options.ComposeProject) + "_leapview-state",
		Target:  directory,
		Subpath: subpath,
	})
	return volumes, nil
}

func validateQualificationMultiNodeOptions(options qualificationMultiNodeOptions) error {
	for label, value := range map[string]string{
		"image":           options.Image,
		"Compose project": options.ComposeProject,
		"Compose network": options.ComposeNetwork,
		"target ID":       options.TargetID,
		"generation ID":   options.GenerationID,
		"workload token":  options.WorkloadToken,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("multi-node qualification %s is required", label)
		}
	}
	if !qualificationMultiNodeScopeIdentifier.MatchString(strings.TrimSpace(options.TargetID)) {
		return errors.New("multi-node qualification target ID contains unsupported characters")
	}
	if !qualificationMultiNodeScopeIdentifier.MatchString(strings.TrimSpace(options.GenerationID)) {
		return errors.New("multi-node qualification generation ID contains unsupported characters")
	}
	if err := validateQualificationNativePostgresIdentifier(options.ComposeProject, "qualification Compose project"); err != nil {
		return err
	}
	if err := validateQualificationNativePostgresIdentifier(options.ComposeNetwork, "qualification Compose network"); err != nil {
		return err
	}
	if options.Topology == nil || options.Topology.Container == nil {
		return errors.New("multi-node qualification PostgreSQL topology is required")
	}
	if options.Primary == nil {
		return errors.New("multi-node qualification primary container is required")
	}
	if strings.TrimSpace(options.Primary.Name()) == "" {
		return errors.New("multi-node qualification primary container name is required")
	}
	if strings.TrimSpace(options.Topology.secretDir) == "" {
		return errors.New("multi-node qualification PostgreSQL CA directory is required")
	}
	caPath := filepath.Join(options.Topology.secretDir, "ca.pem")
	info, err := os.Stat(caPath)
	if err != nil {
		return fmt.Errorf("stat multi-node qualification PostgreSQL CA: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("multi-node qualification PostgreSQL CA is not a regular file")
	}
	return nil
}

func assertQualificationMultiNodeQuery(
	ctx context.Context,
	container qualificationContainer,
	token string,
	stage string,
) error {
	if container == nil {
		return errors.New("qualification application container is required")
	}
	output, err := container.Exec(
		ctx,
		nil,
		"env", "LEAPVIEW_TARGET=http://127.0.0.1:8080", "LEAPVIEW_API_TOKEN="+strings.TrimSpace(token),
		"leapview", "api", "call", "querySemanticModel",
		"--path", "model=semantic-model:sales",
		"--body-json", `{"dimensions":[{"field":"state"}],"metrics":[{"field":"order_count"},{"field":"revenue"}],"limit":10}`,
	)
	if err != nil {
		return fmt.Errorf("query through %s: %w", stage, err)
	}
	var result struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("decode governed query through %s: %w", stage, err)
	}
	if len(result.Rows) != 4 {
		return fmt.Errorf("governed query through %s returned %d rows, want 4", stage, len(result.Rows))
	}
	return nil
}

func qualificationMultiNodeEnvironment(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read multi-node qualification application environment: %w", err)
	}
	values := environmentValues(string(contents))
	if len(values) == 0 {
		return nil, errors.New("multi-node qualification application environment is empty")
	}
	for key, value := range values {
		if strings.HasPrefix(key, "LEAPVIEW_POSTGRES_") && strings.HasSuffix(key, "_URL") && strings.TrimSpace(value) != "" {
			values[key], err = qualificationMultiNodeURL(value)
			if err != nil {
				return nil, fmt.Errorf("rewrite multi-node PostgreSQL CA path for %s: %w", key, err)
			}
		}
	}
	// A separate process must use a separate listen/socket home while keeping
	// the production identity and environment bound to the same PostgreSQL
	// instance. The certificate itself is mounted read-only below /etc.
	values["LEAPVIEW_ADDR"] = ":8080"
	values["LEAPVIEW_HOME"] = "/var/lib/leapview"
	values["LEAPVIEW_DUCKDB_EXTENSION_CACHE_DIR"] = qualificationMultiNodeExtensionCache
	objectStoreBackend := strings.ToLower(strings.TrimSpace(values["LEAPVIEW_OBJECT_STORE_BACKEND"]))
	if objectStoreBackend == "" || objectStoreBackend == "filesystem" {
		values["LEAPVIEW_OBJECT_STORE_FILESYSTEM_ROOT"] = qualificationMultiNodeObjectStore
	}
	return values, nil
}

func qualificationMultiNodeURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Hostname() == "" || parsed.User == nil || parsed.Fragment != "" {
		return "", errors.New("PostgreSQL URL is malformed")
	}
	if _, present := parsed.User.Password(); !present {
		return "", errors.New("PostgreSQL URL is missing a password")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", errors.New("PostgreSQL URL query is malformed")
	}
	query.Set("sslrootcert", qualificationMultiNodeRootCertificate)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func qualificationMultiNodeContainerName(project string) string {
	name := normalizedQualificationName(strings.TrimSpace(project) + "-node-b")
	if len(name) > 63 {
		name = strings.Trim(name[:63], "-")
	}
	return name
}

func qualificationWaitMultiNodeReady(ctx context.Context, container qualificationContainer, stage string) error {
	if err := waitQualificationHealthcheck(ctx, container, "http://127.0.0.1:8080/readyz", 3*time.Minute); err != nil {
		return fmt.Errorf("wait for %s readiness: %w", stage, err)
	}
	return nil
}

func assertQualificationMultiNodeIdentity(ctx context.Context, first, second qualificationContainer, token string) error {
	firstIdentity, err := qualificationMultiNodeIdentity(ctx, first, token)
	if err != nil {
		return fmt.Errorf("read primary node identity: %w", err)
	}
	secondIdentity, err := qualificationMultiNodeIdentity(ctx, second, token)
	if err != nil {
		return fmt.Errorf("read secondary node identity: %w", err)
	}
	if firstIdentity.ID == "" || secondIdentity.ID == "" || firstIdentity.ID != secondIdentity.ID {
		return errors.New("multi-node application processes did not converge on the durable instance identity")
	}
	if firstIdentity.Environment == "" || firstIdentity.Environment != secondIdentity.Environment {
		return errors.New("multi-node application processes did not converge on the serving environment")
	}
	if firstIdentity.CanonicalOrigin == "" || firstIdentity.CanonicalOrigin != secondIdentity.CanonicalOrigin {
		return errors.New("multi-node application processes did not converge on the canonical origin")
	}
	return nil
}

func qualificationMultiNodeIdentity(ctx context.Context, container qualificationContainer, token string) (qualificationMultiNodeInstance, error) {
	if container == nil {
		return qualificationMultiNodeInstance{}, errors.New("qualification application container is required")
	}
	output, err := container.Exec(
		ctx,
		nil,
		"env", "LEAPVIEW_TARGET=http://127.0.0.1:8080", "LEAPVIEW_API_TOKEN="+strings.TrimSpace(token),
		"leapview", "api", "call", "getInstance",
	)
	if err != nil {
		return qualificationMultiNodeInstance{}, err
	}
	var identity qualificationMultiNodeInstance
	if err := json.Unmarshal(output, &identity); err != nil {
		return qualificationMultiNodeInstance{}, fmt.Errorf("decode application instance identity: %w", err)
	}
	return identity, nil
}
