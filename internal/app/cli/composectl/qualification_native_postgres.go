package composectl

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// qualificationPostgreSQL18Image is intentionally kept with the
// qualification code. The image is an evidence input and must not float with
// the development Compose file or a test dependency.
const qualificationPostgreSQL18Image = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

const (
	qualificationNativePostgresControlDatabase  = "leapview_control"
	qualificationNativePostgresDuckLakeDatabase = "leapview_ducklake"
	qualificationNativePostgresBootstrapRole    = "leapview_bootstrap"

	qualificationNativePostgresControlRuntimeRole      = "leapview_control_runtime"
	qualificationNativePostgresControlReadonlyRole     = "leapview_control_readonly"
	qualificationNativePostgresControlMigratorRole     = "leapview_control_migrator"
	qualificationNativePostgresControlUpgradeRole      = "leapview_control_upgrade_coordinator"
	qualificationNativePostgresControlMaintenanceRole  = "leapview_control_maintenance"
	qualificationNativePostgresDuckLakeRuntimeRole     = "leapview_ducklake_runtime"
	qualificationNativePostgresDuckLakeMigratorRole    = "leapview_ducklake_migrator"
	qualificationNativePostgresDuckLakeMaintenanceRole = "leapview_ducklake_maintenance"

	qualificationNativePostgresReadyTimeout = 2 * time.Minute
	qualificationNativePostgresRootCertPath = "/var/lib/leapview/home/postgres-root.crt"
)

var qualificationNativePostgresIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// qualificationNativePostgresTopologyOptions identifies the already-running
// Compose project that the sidecar must join. The sidecar never creates or
// tears down this network: the caller owns the primary Compose lifecycle.
type qualificationNativePostgresTopologyOptions struct {
	ComposeProject string
	ComposeNetwork string
	BundleRoot     string
	// InitScript is an optional explicit path used by source-tree tests and
	// packaging checks. Installed bundles default to qualification/postgres-init.sh.
	InitScript    string
	ContainerName string
}

// qualificationNativePostgresOptions is retained as a concise alias for
// callers that do not need to mention the topology implementation detail.
type qualificationNativePostgresOptions = qualificationNativePostgresTopologyOptions

// qualificationNativePostgresTopology contains the disposable sidecar and
// the exact credentials used to configure the production-shaped application.
// URLs are intentionally returned only to the in-process caller; diagnostics
// always pass through the qualification redactors.
type qualificationNativePostgresTopology struct {
	Container      qualificationContainer
	ContainerName  string
	ComposeProject string
	ComposeNetwork string

	ControlURL                   string
	ControlReadonlyURL           string
	ControlMaintenanceURL        string
	ControlMigratorURL           string
	ControlUpgradeCoordinatorURL string
	DuckLakeURL                  string
	DuckLakeMaintenanceURL       string
	DuckLakeMigratorURL          string

	ControlRuntimeRole            string
	ControlReadonlyRole           string
	ControlMaintenanceRole        string
	ControlMigratorRole           string
	ControlUpgradeCoordinatorRole string
	DuckLakeRuntimeRole           string
	DuckLakeMaintenanceRole       string
	DuckLakeMigratorRole          string

	secretDir string
}

// Remove tears down only the sidecar and its private certificate directory.
// It is safe to call repeatedly, including after a partial startup.
func (topology *qualificationNativePostgresTopology) Remove(ctx context.Context) error {
	if topology == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result error
	if topology.Container != nil {
		_, err := topology.Container.Remove(ctx)
		result = ignoreQualificationNotFound(err)
		if result != nil {
			// Keep both handles intact while Docker removal is pending. The
			// sidecar may still be using the mounted TLS files; preserving the
			// ownership state also makes an explicit removal retry possible.
			return result
		}
		topology.Container = nil
	}
	if topology.secretDir != "" {
		if err := os.RemoveAll(topology.secretDir); err != nil {
			result = errors.Join(result, err)
		} else {
			topology.secretDir = ""
		}
	}
	return result
}

// newQualificationNativePostgresTopology starts one TLS-enabled PostgreSQL
// 18 sidecar on an existing Compose network for installed-candidate
// qualification.
func newQualificationNativePostgresTopology(
	ctx context.Context,
	runtime qualificationContainerRuntime,
	options qualificationNativePostgresTopologyOptions,
) (*qualificationNativePostgresTopology, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateQualificationNativePostgresTopologyOptions(options); err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, errors.New("qualification PostgreSQL container runtime is required")
	}

	initScript, err := qualificationNativePostgresInitScript(options)
	if err != nil {
		return nil, err
	}
	if err := ensureQualificationNativePostgresInitScriptExecutable(initScript); err != nil {
		return nil, err
	}
	containerName := strings.TrimSpace(options.ContainerName)
	if containerName == "" {
		containerName = normalizedQualificationName(options.ComposeProject + "-postgres")
	}
	if err := validateQualificationNativePostgresIdentifier(containerName, "qualification PostgreSQL container name"); err != nil {
		return nil, err
	}
	secretDir, tlsFiles, err := createQualificationNativePostgresTLSFiles(containerName)
	if err != nil {
		return nil, err
	}
	topology := &qualificationNativePostgresTopology{
		ContainerName: containerName, ComposeProject: strings.TrimSpace(options.ComposeProject),
		ComposeNetwork: strings.TrimSpace(options.ComposeNetwork), secretDir: secretDir,
		ControlRuntimeRole:            qualificationNativePostgresControlRuntimeRole,
		ControlReadonlyRole:           qualificationNativePostgresControlReadonlyRole,
		ControlMaintenanceRole:        qualificationNativePostgresControlMaintenanceRole,
		ControlMigratorRole:           qualificationNativePostgresControlMigratorRole,
		ControlUpgradeCoordinatorRole: qualificationNativePostgresControlUpgradeRole,
		DuckLakeRuntimeRole:           qualificationNativePostgresDuckLakeRuntimeRole,
		DuckLakeMaintenanceRole:       qualificationNativePostgresDuckLakeMaintenanceRole,
		DuckLakeMigratorRole:          qualificationNativePostgresDuckLakeMigratorRole,
	}
	credentials, err := newQualificationNativePostgresCredentials()
	if err != nil {
		_ = topology.Remove(context.Background())
		return nil, err
	}
	request := qualificationContainerRequest{
		Name: containerName, Image: qualificationPostgreSQL18Image,
		NetworkMode: topology.ComposeNetwork,
		Volumes: []qualificationContainerVolume{
			{Source: initScript, Target: "/docker-entrypoint-initdb.d/10-leapview-roles.sh", ReadOnly: true},
			{Source: tlsFiles.ca, Target: "/run/secrets/leapview-postgres-ca.pem", ReadOnly: true},
			{Source: tlsFiles.cert, Target: "/run/secrets/leapview-postgres-server.pem", ReadOnly: true},
			{Source: tlsFiles.key, Target: "/run/secrets/leapview-postgres-server.key", ReadOnly: true},
			// Compose creates the application state volume when the service is
			// materialized. Sharing it lets the disposable sidecar provide its
			// private CA to the application without exposing a host path in the
			// production template.
			{Source: strings.TrimSpace(options.ComposeProject) + "_leapview-state", Target: "/var/lib/leapview"},
		},
		Tmpfs: []string{
			"/var/lib/postgresql:rw,exec,nosuid,nodev,size=512m",
			"/tmp:rw,nosuid,nodev,mode=1777,size=64m",
		},
		Environment: map[string]string{
			"POSTGRES_DB":       "postgres",
			"POSTGRES_USER":     qualificationNativePostgresBootstrapRole,
			"POSTGRES_PASSWORD": credentials.bootstrap,
			"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD":             credentials.controlRuntime,
			"LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD":            credentials.controlReadonly,
			"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD":            credentials.duckLakeRuntime,
			"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD":            credentials.controlMigrator,
			"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD": credentials.controlUpgrade,
			"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD":         credentials.controlMaintenance,
			"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD":           credentials.duckLakeMigrator,
			"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD":        credentials.duckLakeMaintenance,
		},
		Entrypoint: []string{"sh"},
		Command:    []string{"-ec", qualificationNativePostgresEntrypointScript},
		NoHealth:   true,
	}
	container, err := runtime.Start(ctx, request)
	if err != nil {
		_ = topology.Remove(context.Background())
		return nil, fmt.Errorf("start qualification PostgreSQL sidecar: %w", err)
	}
	if container == nil {
		_ = topology.Remove(context.Background())
		return nil, errors.New("start qualification PostgreSQL sidecar returned a nil container")
	}
	topology.Container = container
	host := containerName
	topology.ControlURL = qualificationNativePostgresURL(host, qualificationNativePostgresControlDatabase, topology.ControlRuntimeRole, credentials.controlRuntime)
	topology.ControlReadonlyURL = qualificationNativePostgresURL(host, qualificationNativePostgresControlDatabase, topology.ControlReadonlyRole, credentials.controlReadonly)
	topology.ControlMaintenanceURL = qualificationNativePostgresURL(host, qualificationNativePostgresControlDatabase, topology.ControlMaintenanceRole, credentials.controlMaintenance)
	topology.ControlMigratorURL = qualificationNativePostgresURL(host, qualificationNativePostgresControlDatabase, topology.ControlMigratorRole, credentials.controlMigrator)
	topology.ControlUpgradeCoordinatorURL = qualificationNativePostgresURL(host, qualificationNativePostgresControlDatabase, topology.ControlUpgradeCoordinatorRole, credentials.controlUpgrade)
	topology.DuckLakeURL = qualificationNativePostgresURL(host, qualificationNativePostgresDuckLakeDatabase, topology.DuckLakeRuntimeRole, credentials.duckLakeRuntime)
	topology.DuckLakeMaintenanceURL = qualificationNativePostgresURL(host, qualificationNativePostgresDuckLakeDatabase, topology.DuckLakeMaintenanceRole, credentials.duckLakeMaintenance)
	topology.DuckLakeMigratorURL = qualificationNativePostgresURL(host, qualificationNativePostgresDuckLakeDatabase, topology.DuckLakeMigratorRole, credentials.duckLakeMigrator)

	if err := waitQualificationNativePostgresTopology(ctx, topology.Container, credentials); err != nil {
		operationErr := qualificationContainerOperationError(ctx, topology.Container, "wait for qualification PostgreSQL final-role readiness", err)
		cleanupErr := topology.Remove(context.Background())
		return nil, errors.Join(operationErr, cleanupErr)
	}
	return topology, nil
}

// AssertBootstrapOpen proves that qualification setup has not fabricated an
// active delivery before the candidate lifecycle publishes one. The readonly
// role is sufficient for this invariant and its password is expanded only by
// the sidecar shell, so neither argv nor qualification diagnostics contain the
// credential value.
func (topology *qualificationNativePostgresTopology) AssertBootstrapOpen(ctx context.Context, stage string) error {
	if topology == nil || topology.Container == nil {
		return errors.New("qualification PostgreSQL topology is unavailable")
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return errors.New("qualification bootstrap stage is required")
	}
	// sqlc-exception: analyzer-incompatible. Qualification executes psql through
	// a sidecar container command, outside the generated PostgreSQL query boundary.
	output, err := topology.Container.Exec(
		ctx,
		nil,
		"sh", "-ec",
		`export PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem
psql --host localhost --username leapview_control_readonly --dbname leapview_control --no-psqlrc --tuples-only --no-align --command 'SELECT count(*) FROM delivery.delivery_active_pointer'`,
	)
	if err != nil {
		return qualificationContainerOperationError(ctx, topology.Container, "verify qualification bootstrap state after "+stage, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || count < 0 {
		return fmt.Errorf("verify qualification bootstrap state after %s: invalid active-pointer count %q", stage, strings.TrimSpace(string(redactQualificationBytes(output))))
	}
	if count != 0 {
		return fmt.Errorf("qualification bootstrap closed before candidate publication after %s: found %d active delivery pointers", stage, count)
	}
	return nil
}

// AssertNativeDeliveryReads proves that the exact long-running control role
// retains and can exercise the canonical delivery/serving-state reads
// required before a native build. DuckLake no longer owns a duplicate
// generation-binding lifecycle table; delivery seals and serving-state bundles
// are the authoritative hand-off evidence.
// Keeping this at the qualification boundary catches role-policy drift before
// browser authoring turns it into an opaque delivery-plan failure.
func (topology *qualificationNativePostgresTopology) AssertNativeDeliveryReads(ctx context.Context) error {
	if topology == nil || topology.Container == nil {
		return errors.New("qualification PostgreSQL topology is unavailable")
	}
	// sqlc-exception: analyzer-incompatible. Qualification executes psql through
	// a sidecar container command, outside the generated PostgreSQL query boundary.
	output, err := topology.Container.Exec(
		ctx,
		nil,
		"sh", "-ec",
		`export PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem
psql --host localhost --username leapview_control_runtime --dbname leapview_control --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT current_user::text || '|' || current_database() || '|' || has_table_privilege(current_user, 'ducklake.catalog_identity', 'SELECT')::text || '|' || has_table_privilege(current_user, 'delivery.delivery_build_attempt', 'SELECT')::text || '|' || has_table_privilege(current_user, 'delivery.delivery_snapshot_seal', 'SELECT')::text || '|' || has_table_privilege(current_user, 'serving_state.bundle', 'SELECT')::text" --command "SELECT physical_pool_id FROM ducklake.catalog_identity LIMIT 0" --command "SELECT attempt_id FROM delivery.delivery_build_attempt LIMIT 0" --command "SELECT seal_id FROM delivery.delivery_snapshot_seal LIMIT 0" --command "SELECT generation_id FROM serving_state.bundle LIMIT 0"`,
	)
	if err != nil {
		return qualificationContainerOperationError(ctx, topology.Container, "verify native delivery PostgreSQL reads", err)
	}
	const expected = "leapview_control_runtime|leapview_control|true|true|true|true"
	actual := strings.TrimSpace(string(redactQualificationLog(output, 1)))
	if actual != expected {
		return fmt.Errorf("native delivery PostgreSQL read boundary = %q, want %q", actual, expected)
	}
	return nil
}

// AssertDurableActivePointer verifies the serving generation selected by the
// application processes through the native PostgreSQL authority. It is used
// around process-loss and rolling-restart boundaries so a node cannot pass
// merely by retaining an in-memory serving snapshot.
func (topology *qualificationNativePostgresTopology) AssertDurableActivePointer(
	ctx context.Context,
	targetID string,
	generationID string,
) error {
	if topology == nil || topology.Container == nil {
		return errors.New("qualification PostgreSQL topology is unavailable")
	}
	if !qualificationMultiNodeScopeIdentifier.MatchString(strings.TrimSpace(targetID)) {
		return errors.New("qualification durable pointer target ID contains unsupported characters")
	}
	if !qualificationMultiNodeScopeIdentifier.MatchString(strings.TrimSpace(generationID)) {
		return errors.New("qualification durable pointer generation ID contains unsupported characters")
	}
	query := fmt.Sprintf(
		"SELECT count(*) FROM delivery.delivery_active_pointer ap JOIN delivery.delivery_publication p ON p.publication_id = ap.publication_id AND p.target_id = ap.target_id AND p.generation_id = ap.generation_id WHERE ap.target_id = '%s' AND ap.generation_id::text = '%s' AND p.state = 'committed'",
		targetID,
		generationID,
	)
	// sqlc-exception: analyzer-incompatible. Qualification executes psql
	// through a sidecar container command, outside generated query code.
	output, err := topology.Container.Exec(
		ctx,
		nil,
		"sh", "-ec",
		`export PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD" PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem
psql --host localhost --username leapview_control_readonly --dbname leapview_control --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "`+query+`"`,
	)
	if err != nil {
		return qualificationContainerOperationError(ctx, topology.Container, "verify durable active PostgreSQL pointer", err)
	}
	if strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("durable active PostgreSQL pointer did not select the expected committed generation: %q", strings.TrimSpace(string(redactQualificationBytes(output))))
	}
	return nil
}

// startQualificationNativePostgresTopology is the Controller seam used by
// future installed-candidate wiring. Keeping the runtime injected preserves
// deterministic unit tests and avoids testcontainers in production code.
func (c *Controller) startQualificationNativePostgresTopology(
	ctx context.Context,
	options qualificationNativePostgresTopologyOptions,
) (*qualificationNativePostgresTopology, error) {
	if c == nil {
		return nil, errors.New("controller is required")
	}
	if strings.TrimSpace(options.BundleRoot) == "" {
		options.BundleRoot = c.root
	}
	return newQualificationNativePostgresTopology(ctx, c.qualificationContainers, options)
}

type qualificationNativePostgresCredentials struct {
	bootstrap, controlRuntime, controlReadonly, duckLakeRuntime string
	controlMigrator, controlUpgrade, controlMaintenance         string
	duckLakeMigrator, duckLakeMaintenance                       string
}

func newQualificationNativePostgresCredentials() (qualificationNativePostgresCredentials, error) {
	values := make([]*string, 9)
	credentials := qualificationNativePostgresCredentials{}
	values[0], values[1], values[2], values[3] = &credentials.bootstrap, &credentials.controlRuntime, &credentials.controlReadonly, &credentials.duckLakeRuntime
	values[4], values[5], values[6] = &credentials.controlMigrator, &credentials.controlUpgrade, &credentials.controlMaintenance
	values[7], values[8] = &credentials.duckLakeMigrator, &credentials.duckLakeMaintenance
	for _, value := range values {
		secret, err := qualificationRandomHex(24)
		if err != nil {
			return qualificationNativePostgresCredentials{}, err
		}
		*value = secret
	}
	return credentials, nil
}

func validateQualificationNativePostgresTopologyOptions(options qualificationNativePostgresTopologyOptions) error {
	if err := validateQualificationNativePostgresIdentifier(options.ComposeProject, "qualification Compose project"); err != nil {
		return err
	}
	if err := validateQualificationNativePostgresIdentifier(options.ComposeNetwork, "qualification Compose network"); err != nil {
		return err
	}
	bundleRoot := strings.TrimSpace(options.BundleRoot)
	if bundleRoot == "" {
		return errors.New("qualification PostgreSQL bundle root is required")
	}
	bundleRoot, err := filepath.Abs(bundleRoot)
	if err != nil {
		return fmt.Errorf("resolve qualification PostgreSQL bundle root: %w", err)
	}
	info, err := os.Stat(bundleRoot)
	if err != nil {
		return fmt.Errorf("stat qualification PostgreSQL bundle root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("qualification PostgreSQL bundle root %q is not a directory", bundleRoot)
	}
	initScript, err := qualificationNativePostgresInitScript(options)
	if err != nil {
		return err
	}
	initInfo, err := os.Stat(initScript)
	if err != nil {
		return fmt.Errorf("qualification PostgreSQL canonical init script: %w", err)
	}
	if !initInfo.Mode().IsRegular() {
		return fmt.Errorf("qualification PostgreSQL canonical init script %q is not a regular file", initScript)
	}
	return nil
}

func qualificationNativePostgresInitScript(options qualificationNativePostgresTopologyOptions) (string, error) {
	bundleRoot := strings.TrimSpace(options.BundleRoot)
	if bundleRoot == "" {
		return "", errors.New("qualification PostgreSQL bundle root is required")
	}
	bundleRoot, err := filepath.Abs(bundleRoot)
	if err != nil {
		return "", fmt.Errorf("resolve qualification PostgreSQL bundle root: %w", err)
	}
	initScript := strings.TrimSpace(options.InitScript)
	if initScript == "" {
		initScript = filepath.Join(bundleRoot, "qualification", "postgres-init.sh")
	} else {
		initScript, err = filepath.Abs(initScript)
		if err != nil {
			return "", fmt.Errorf("resolve qualification PostgreSQL init script: %w", err)
		}
	}
	return initScript, nil
}

// ensureQualificationNativePostgresInitScriptExecutable normalizes the mode
// of the host-mounted init hook before the PostgreSQL image starts. Release
// archives can cross artifact boundaries (or be assembled under umask 0077)
// that retain only owner permissions. The official entrypoint executes .sh
// hooks after dropping to the unprivileged postgres user, so owner-only modes
// fail with EACCES even though the invoking qualification process can read
// the file. The hook contains no credentials and is already part of the
// extracted release bundle, making 0755 the reviewed mode for this mount.
func ensureQualificationNativePostgresInitScriptExecutable(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("qualification PostgreSQL init script path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat qualification PostgreSQL init script: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("qualification PostgreSQL init script %q is not a regular file", path)
	}
	const reviewedMode os.FileMode = 0o755
	if info.Mode().Perm() == reviewedMode {
		return nil
	}
	if err := os.Chmod(path, reviewedMode); err != nil {
		return fmt.Errorf("make qualification PostgreSQL init script executable: %w", err)
	}
	return nil
}

func validateQualificationNativePostgresIdentifier(value, label string) error {
	value = strings.TrimSpace(value)
	if !qualificationNativePostgresIdentifier.MatchString(value) {
		return fmt.Errorf("%s must match %s", label, qualificationNativePostgresIdentifier.String())
	}
	return nil
}

func qualificationNativePostgresURL(host, database, role, password string) string {
	connectionURL := &url.URL{Scheme: "postgres", Host: host, Path: "/" + database, RawQuery: "sslmode=verify-full&sslrootcert=" + url.QueryEscape(qualificationNativePostgresRootCertPath)}
	connectionURL.User = url.UserPassword(role, password)
	return connectionURL.String()
}

const qualificationNativePostgresEntrypointScript = `set -eu
mkdir -p /tmp/leapview-postgres-tls
cp /run/secrets/leapview-postgres-ca.pem /tmp/leapview-postgres-tls/ca.pem
mkdir -p /var/lib/leapview/home
cp /run/secrets/leapview-postgres-ca.pem /var/lib/leapview/home/postgres-root.crt
chmod 0644 /var/lib/leapview/home/postgres-root.crt
cp /run/secrets/leapview-postgres-server.pem /tmp/leapview-postgres-tls/server.pem
cp /run/secrets/leapview-postgres-server.key /tmp/leapview-postgres-tls/server.key
chown -R postgres:postgres /tmp/leapview-postgres-tls
chmod 0644 /tmp/leapview-postgres-tls/ca.pem /tmp/leapview-postgres-tls/server.pem
chmod 0600 /tmp/leapview-postgres-tls/server.key
exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_ca_file=/tmp/leapview-postgres-tls/ca.pem -c ssl_cert_file=/tmp/leapview-postgres-tls/server.pem -c ssl_key_file=/tmp/leapview-postgres-tls/server.key -c log_line_prefix='%m [%p] %u@%d '`

type qualificationNativePostgresTLSFiles struct{ ca, cert, key string }

func createQualificationNativePostgresTLSFiles(serverHost string) (string, qualificationNativePostgresTLSFiles, error) {
	dir, err := os.MkdirTemp("", "leapview-qualification-postgres-")
	if err != nil {
		return "", qualificationNativePostgresTLSFiles{}, fmt.Errorf("create qualification PostgreSQL TLS directory: %w", err)
	}
	removeOnError := func(err error) (string, qualificationNativePostgresTLSFiles, error) {
		_ = os.RemoveAll(dir)
		return "", qualificationNativePostgresTLSFiles{}, err
	}
	caKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return removeOnError(fmt.Errorf("generate qualification PostgreSQL CA key: %w", err))
	}
	caSerial, err := qualificationNativePostgresSerial()
	if err != nil {
		return removeOnError(fmt.Errorf("generate qualification PostgreSQL CA serial: %w", err))
	}
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject:      pkix.Name{CommonName: "leapview-qualification-postgres-ca"},
		IsCA:         true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(cryptorand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return removeOnError(fmt.Errorf("create qualification PostgreSQL CA certificate: %w", err))
	}
	serverKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return removeOnError(fmt.Errorf("generate qualification PostgreSQL server key: %w", err))
	}
	serverSerial, err := qualificationNativePostgresSerial()
	if err != nil {
		return removeOnError(fmt.Errorf("generate qualification PostgreSQL server serial: %w", err))
	}
	serverHost = strings.TrimSpace(serverHost)
	dnsNames := []string{"postgres", "localhost"}
	if serverHost != "" && serverHost != dnsNames[0] && serverHost != dnsNames[1] {
		dnsNames = append(dnsNames, serverHost)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject:      pkix.Name{CommonName: "postgres"},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(cryptorand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return removeOnError(fmt.Errorf("create qualification PostgreSQL server certificate: %w", err))
	}
	files := qualificationNativePostgresTLSFiles{
		ca: filepath.Join(dir, "ca.pem"), cert: filepath.Join(dir, "server.pem"), key: filepath.Join(dir, "server.key"),
	}
	for _, file := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{files.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644},
		{files.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0o644},
		{files.key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}), 0o600},
	} {
		if err := os.WriteFile(file.path, file.data, file.mode); err != nil {
			return removeOnError(fmt.Errorf("write qualification PostgreSQL TLS file: %w", err))
		}
	}
	return dir, files, nil
}

func qualificationNativePostgresSerial() (*big.Int, error) {
	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}
	return serial, nil
}

func waitQualificationNativePostgresTopology(
	ctx context.Context,
	container qualificationContainer,
	credentials qualificationNativePostgresCredentials,
) error {
	if container == nil {
		return errors.New("qualification PostgreSQL container is missing")
	}
	waitCtx, cancel := qualificationContext(ctx, qualificationNativePostgresReadyTimeout)
	defer cancel()
	var lastErr error
	err := qualificationWait(waitCtx, time.Second, func(requestCtx context.Context) (bool, error) {
		for _, probe := range []struct {
			database string
			role     string
			password string
		}{
			{qualificationNativePostgresControlDatabase, qualificationNativePostgresControlRuntimeRole, credentials.controlRuntime},
			{qualificationNativePostgresDuckLakeDatabase, qualificationNativePostgresDuckLakeRuntimeRole, credentials.duckLakeRuntime},
		} {
			// sqlc-exception: analyzer-incompatible. Readiness probes execute psql
			// through a sidecar container command, outside generated query code.
			if _, probeErr := container.Exec(requestCtx, nil, "sh", "-ec", qualificationNativePostgresProbe(probe.database, probe.role, probe.password)); probeErr != nil {
				lastErr = probeErr
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return errors.Join(err, lastErr)
	}
	return nil
}

func qualificationNativePostgresProbe(database, role, password string) string {
	return fmt.Sprintf("PGSSLMODE=verify-full PGSSLROOTCERT=/tmp/leapview-postgres-tls/ca.pem PGPASSWORD=%s psql --host localhost --port 5432 --username %s --dbname %s --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command 'SELECT 1'", password, role, database)
}
