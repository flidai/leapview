package composectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// Revision019Bootstrap owns the disposable PostgreSQL provider used while an
// exact revision-019 predecessor is installed. The provider is deliberately
// separate from the application Compose lifecycle and stays up after Start.
type Revision019BootstrapHandle interface {
	Apply(context.Context) error
}

type revision019Bootstrap struct {
	controller *Controller
	topology   *qualificationNativePostgresTopology
	pool       qualificationNativePhysicalPoolArtifacts
}

// PrepareRevision019 establishes TLS control and DuckLake databases and the
// image-generated pool identity. It never starts the application. The init
// script is a provisioner-owned copy of the canonical deploy/postgres/init.sh.
func (c *Controller) PrepareRevision019(ctx context.Context, payloadRoot, initScript, image string) (result Revision019BootstrapHandle, resultErr error) {
	if c == nil {
		return nil, errors.New("predecessor bootstrap controller is required")
	}
	if strings.TrimSpace(payloadRoot) == "" || strings.TrimSpace(initScript) == "" {
		return nil, errors.New("predecessor payload and PostgreSQL initialization script are required")
	}
	if err := requireDigest(image); err != nil {
		return nil, fmt.Errorf("predecessor image: %w", err)
	}
	if err := requireNonEmptyFile(filepath.Join(payloadRoot, "leapview.env.example")); err != nil {
		return nil, fmt.Errorf("predecessor environment example: %w", err)
	}
	if err := requireNonEmptyFile(initScript); err != nil {
		return nil, fmt.Errorf("PostgreSQL initialization script: %w", err)
	}
	if _, err := os.Lstat(c.path(appEnvName)); err == nil {
		return nil, errors.New("predecessor bootstrap refuses an existing application environment")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	contents, err := os.ReadFile(filepath.Join(payloadRoot, "leapview.env.example"))
	if err != nil {
		return nil, err
	}
	if err := securefs.WritePrivateFileAtomic(c.path(appEnvName), contents); err != nil {
		return nil, err
	}
	if err := c.setImage(image); err != nil {
		return nil, fmt.Errorf("select predecessor image for provider setup: %w", err)
	}
	project, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_PROJECT_NAME")
	if err != nil {
		return nil, err
	}
	network, err := c.prepareQualificationNativePostgresNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("prepare predecessor Compose network: %w", err)
	}
	topology, err := c.startQualificationNativePostgresTopology(ctx, qualificationNativePostgresTopologyOptions{
		ComposeProject: project, ComposeNetwork: network, BundleRoot: c.root, InitScript: initScript,
		PersistentDataVolume: project + "_predecessor-postgres-data",
		CertificateDir:       c.path("predecessor-provider-tls"),
	})
	if err != nil {
		return nil, fmt.Errorf("start predecessor PostgreSQL topology: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, topology.Remove(context.Background()))
		}
	}()
	if err := c.writeQualificationNativePostgresEnvironment(topology); err != nil {
		return nil, err
	}
	pool, err := c.prepareQualificationNativePhysicalPool(ctx, c.path("predecessor-pool"))
	if err != nil {
		return nil, err
	}
	return &revision019Bootstrap{controller: c, topology: topology, pool: pool}, nil
}

// InitializeRevision019FromPayload delegates configuration and control-schema
// initialization to the exact predecessor distribution's operator binary.
// The current controller only prepares provider dependencies around it.
func (c *Controller) InitializeRevision019FromPayload(ctx context.Context, payloadRoot string, options InitOptions) error {
	if c == nil {
		return errors.New("predecessor bootstrap controller is required")
	}
	options, err := NormalizeInitOptions(options)
	if err != nil {
		return err
	}
	executable := filepath.Join(payloadRoot, "leapviewctl")
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 {
		return errors.New("immutable predecessor controller payload is missing or not executable")
	}
	arguments := []string{"init", "--admin-email", options.AdminEmail, "--domain", options.Domain,
		"--environment", options.Environment, "--image", options.Image}
	if options.NoHTTPS {
		arguments = append(arguments, "--no-https")
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = c.root
	command.Env = append(os.Environ(), "LEAPVIEWCTL_ROOT="+c.root, "LEAPVIEWCTL_DOCKER_BIN="+c.dockerBin)
	command.Stdin, command.Stdout, command.Stderr = c.stdin, c.stdout, c.stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("initialize using immutable predecessor controller: %w", err)
	}
	return nil
}

// Apply admits the same pool after Initialize has created the control schema.
// It must finish successfully before the predecessor is allowed to start.
func (b *revision019Bootstrap) Apply(ctx context.Context) error {
	if b == nil || b.controller == nil || b.topology == nil {
		return errors.New("predecessor bootstrap is unavailable")
	}
	if err := b.controller.applyQualificationNativePhysicalPool(ctx, b.topology, b.pool); err != nil {
		return err
	}
	return b.topology.AssertBootstrapOpen(ctx, "predecessor pool admission")
}

// VerifyRevision019Prerequisites reads the real provider after initial setup
// and on later install retries. A live process alone cannot establish that
// the exact predecessor image, revision and pool admission still exist.
func (c *Controller) VerifyRevision019Prerequisites(ctx context.Context, image string) error {
	if c == nil || c.qualificationContainers == nil {
		return errors.New("predecessor provider verification is unavailable")
	}
	configured, err := c.ConfiguredImage()
	if err != nil || configured != image {
		return errors.New("configured predecessor image differs from the exact installation image")
	}
	project, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_PROJECT_NAME")
	if err != nil || validateQualificationNativePostgresIdentifier(project, "predecessor Compose project") != nil || normalizedQualificationName(project) != project {
		return errors.New("predecessor Compose project is invalid")
	}
	poolID, err := envFileValue(c.path(appEnvName), "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID")
	if err != nil || poolID == "" {
		return errors.New("predecessor physical pool identity is missing")
	}
	compatibility, err := envFileValue(c.path(appEnvName), "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST")
	if err != nil || compatibility == "" {
		return errors.New("predecessor physical pool compatibility digest is missing")
	}
	container := c.qualificationContainers.Existing(normalizedQualificationName(project + "-postgres"))
	if container == nil {
		return errors.New("predecessor PostgreSQL provider is unavailable")
	}
	control, err := container.Exec(ctx, nil, "sh", "-ec", `export PGPASSWORD="$POSTGRES_PASSWORD"
psql --username "$POSTGRES_USER" --dbname leapview_control --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT version_id FROM public.goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1; SELECT pool_id || '|' || compatibility_digest FROM physical_pool.physical_pool_admissions; SELECT count(*) FROM ducklake.catalog_identity"`)
	if err != nil {
		return fmt.Errorf("verify predecessor control database and pool admission: %w", err)
	}
	if strings.TrimSpace(string(control)) != "19\n"+poolID+"|"+compatibility+"\n1" {
		return errors.New("predecessor control revision, pool admission or catalog identity differs from the configured authority")
	}
	duckLake, err := container.Exec(ctx, nil, "sh", "-ec", `export PGPASSWORD="$POSTGRES_PASSWORD"
psql --username "$POSTGRES_USER" --dbname leapview_ducklake --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'ducklake'"`)
	if err != nil {
		return fmt.Errorf("verify predecessor DuckLake database: %w", err)
	}
	if strings.TrimSpace(string(duckLake)) != "1" {
		return errors.New("predecessor DuckLake catalog schema is missing")
	}
	return nil
}

// StartRevision019Bootstrap checks process liveness while the new target still
// has no published serving generation. Full readiness belongs to the later
// real-host qualification, after predecessor state has been established.
func (c *Controller) StartRevision019Bootstrap(ctx context.Context, noHTTPS bool) error {
	if c == nil {
		return errors.New("predecessor bootstrap controller is required")
	}
	if _, err := c.qualificationCompose(ctx, c.root, "up", "-d", "leapview"); err != nil {
		return fmt.Errorf("start predecessor application: %w", err)
	}
	if err := c.waitQualificationBootstrapLiveness(ctx); err != nil {
		return err
	}
	if !noHTTPS {
		if _, err := c.qualificationCompose(ctx, c.root, "up", "-d", "--no-deps", "caddy"); err != nil {
			return fmt.Errorf("start predecessor HTTPS proxy: %w", err)
		}
	}
	return nil
}
