// Package adminpostgres composes production Admin initialization from the
// canonical PostgreSQL migration, platform-bootstrap, and Access authorities.
// All other Admin operations remain delegated to the retained local adapter.
package adminpostgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	appadminoffline "github.com/flidai/leapview/internal/app/adminoffline"
	"github.com/flidai/leapview/internal/app/config"
	bootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
)

type controlPool interface {
	platformpostgres.PoolHandle
}

type Bootstrap interface {
	InstanceID(context.Context) (string, error)
	InstanceEnvironment(context.Context) (string, error)
	BindInstanceEnvironment(context.Context, string) error
}

type Dependencies struct {
	LoadConfig       func() (config.Config, error)
	PrepareSchema    func(context.Context, platformpostgres.Config) error
	OpenRuntime      func(context.Context, platformpostgres.Config) (controlPool, error)
	VerifyMigrations func(context.Context, migrations.RevisionReader) error
	NewAccess        func(platformpostgres.DBTX, []byte) (accessmodule.PostgresInstanceInitializer, error)
	NewBootstrap     func(platformpostgres.DBTX) Bootstrap
	AcquireLock      func(string) (adminoffline.Lock, error)
	Now              func() time.Time
}

type Operations struct {
	appadminoffline.Operations
	Dependencies Dependencies
}

func New(dependencies Dependencies) Operations {
	return Operations{Dependencies: dependencies.withDefaults()}
}

func (dependencies Dependencies) withDefaults() Dependencies {
	if dependencies.LoadConfig == nil {
		dependencies.LoadConfig = config.Load
	}
	if dependencies.PrepareSchema == nil {
		dependencies.PrepareSchema = prepareSchema
	}
	if dependencies.OpenRuntime == nil {
		dependencies.OpenRuntime = func(ctx context.Context, cfg platformpostgres.Config) (controlPool, error) {
			return platformpostgres.OpenControl(ctx, cfg)
		}
	}
	if dependencies.VerifyMigrations == nil {
		dependencies.VerifyMigrations = migrations.Verify
	}
	if dependencies.NewAccess == nil {
		dependencies.NewAccess = func(db platformpostgres.DBTX, key []byte) (accessmodule.PostgresInstanceInitializer, error) {
			return accessmodule.NewPostgresInstanceInitializer(db, key)
		}
	}
	if dependencies.NewBootstrap == nil {
		dependencies.NewBootstrap = func(db platformpostgres.DBTX) Bootstrap { return bootstrappostgres.New(db) }
	}
	if dependencies.AcquireLock == nil {
		dependencies.AcquireLock = func(home string) (adminoffline.Lock, error) { return instancelock.Acquire(home) }
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	return dependencies
}

func (operations Operations) Initialize(ctx context.Context, request adminoffline.InitializeRequest, out io.Writer) error {
	dependencies := operations.Dependencies.withDefaults()
	cfg, err := dependencies.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production || cfg.EvaluationMode {
		return operations.Operations.Initialize(ctx, request, out)
	}
	if request.Format != "json" {
		return fmt.Errorf("admin initialize supports only --format json")
	}
	if out == nil {
		return errors.New("admin initialize output is required")
	}
	if err := cfg.ValidatePostgresControlInitialization(); err != nil {
		return err
	}
	fingerprintKey, err := postgresFingerprintKey(cfg)
	if err != nil {
		return err
	}
	if err := dependencies.PrepareSchema(ctx, cfg.PostgresControlMigratorConfig()); err != nil {
		return err
	}
	return operations.withNativeService(ctx, dependencies, cfg, fingerprintKey, func(service *adminoffline.Service) error {
		return service.Initialize(ctx, request, out)
	})
}

func (operations Operations) AcknowledgeInitialCredentials(ctx context.Context) error {
	dependencies := operations.Dependencies.withDefaults()
	cfg, err := dependencies.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production || cfg.EvaluationMode {
		return operations.Operations.AcknowledgeInitialCredentials(ctx)
	}
	if err := cfg.ValidatePostgresControlInitialization(); err != nil {
		return err
	}
	fingerprintKey, err := postgresFingerprintKey(cfg)
	if err != nil {
		return err
	}
	return operations.withNativeService(ctx, dependencies, cfg, fingerprintKey, func(service *adminoffline.Service) error {
		return service.AcknowledgeInitialCredentials(ctx)
	})
}

func prepareSchema(ctx context.Context, cfg platformpostgres.Config) error {
	pool, err := platformpostgres.OpenControl(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open PostgreSQL control migrator: %w", err)
	}
	if nilInterface(pool) {
		return errors.New("open PostgreSQL control migrator returned nil pool")
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL control migration: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := migrations.Apply(ctx, tx); err != nil {
		return fmt.Errorf("apply PostgreSQL control migrations: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL control migrations: %w", err)
	}
	return nil
}

func (operations Operations) withNativeService(ctx context.Context, dependencies Dependencies, cfg config.Config, fingerprintKey []byte, run func(*adminoffline.Service) error) error {
	pool, err := dependencies.OpenRuntime(ctx, cfg.PostgresControlRuntimeConfig())
	if err != nil {
		return fmt.Errorf("open PostgreSQL control runtime: %w", err)
	}
	if nilInterface(pool) {
		return errors.New("open PostgreSQL control runtime returned nil pool")
	}
	defer pool.Close()
	if err := dependencies.VerifyMigrations(ctx, pool); err != nil {
		return fmt.Errorf("verify PostgreSQL control migrations: %w", err)
	}
	initializer, err := dependencies.NewAccess(pool, fingerprintKey)
	if err != nil {
		return fmt.Errorf("construct PostgreSQL access initializer: %w", err)
	}
	if nilInterface(initializer) {
		return errors.New("construct PostgreSQL access initializer returned nil authority")
	}
	bootstrap := dependencies.NewBootstrap(pool)
	if nilInterface(bootstrap) {
		return errors.New("construct PostgreSQL platform bootstrap returned nil authority")
	}
	if _, err := bootstrap.InstanceID(ctx); err != nil {
		return fmt.Errorf("establish PostgreSQL instance identity: %w", err)
	}
	service := adminoffline.New(adminoffline.Config{
		HomeDir: cfg.HomeDir, Environment: cfg.Environment, Production: true, BootstrapEmail: cfg.BootstrapEmail,
	}, adminoffline.Dependencies{
		Locker:      nativeLocker{home: cfg.HomeDir, acquire: dependencies.AcquireLock},
		State:       nativeState{bootstrap: bootstrap, initializer: initializer},
		Initializer: nativeInitializer{initializer: initializer},
		Recovery:    nativeRecovery{path: filepath.Join(cfg.HomeDir, adminoffline.CredentialRecoveryFileName)},
		Now:         dependencies.Now,
	})
	return run(service)
}

func postgresFingerprintKey(cfg config.Config) ([]byte, error) {
	key := []byte(strings.TrimSpace(cfg.TokenHashKey))
	if len(key) < 32 {
		key = []byte(strings.TrimSpace(cfg.CSRFKey))
	}
	if len(key) < 32 {
		return nil, errors.New("PostgreSQL access fingerprint key is required")
	}
	return key, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

type nativeLocker struct {
	home    string
	acquire func(string) (adminoffline.Lock, error)
}

func (locker nativeLocker) Acquire(context.Context) (adminoffline.Lock, error) {
	return locker.acquire(locker.home)
}

type nativeState struct {
	bootstrap   Bootstrap
	initializer accessmodule.PostgresInstanceInitializer
}

func (state nativeState) Environment(ctx context.Context) (string, error) {
	environment, err := state.bootstrap.InstanceEnvironment(ctx)
	if errors.Is(err, bootstrappostgres.ErrNotFound) {
		return "", adminoffline.ErrStateNotFound
	}
	return environment, err
}

func (state nativeState) ExistingEnvironment(ctx context.Context) (string, bool, error) {
	environment, err := state.Environment(ctx)
	if errors.Is(err, adminoffline.ErrStateNotFound) {
		return "", false, nil
	}
	return environment, err == nil, err
}

func (state nativeState) BindEnvironment(ctx context.Context, environment string) error {
	return state.bootstrap.BindInstanceEnvironment(ctx, environment)
}

func (state nativeState) Initialized(ctx context.Context) (bool, error) {
	return state.initializer.Initialized(ctx)
}

type nativeInitializer struct {
	initializer accessmodule.PostgresInstanceInitializer
}

func (initializer nativeInitializer) Initialize(ctx context.Context, input adminoffline.InitializationInput, prepare func(adminoffline.InitialCredentials) error) (adminoffline.InitialCredentials, error) {
	result, err := initializer.initializer.InitializeInstance(ctx, access.InstanceInitializationInput{
		Email: input.Email, Environment: input.Environment, Now: input.Now,
	}, func(credentials access.InitialInstanceCredentials) error {
		if prepare == nil {
			return nil
		}
		return prepare(adminoffline.InitialCredentials{
			Email: credentials.Email, TemporaryPassword: credentials.TemporaryPassword,
			PublisherToken: credentials.PublisherToken, PublisherTokenExpiresAt: credentials.PublisherTokenExpiresAt.Format(time.RFC3339),
		})
	})
	if errors.Is(err, access.ErrInstanceAlreadyInitialized) {
		err = adminoffline.ErrInstanceAlreadyInitialized
	}
	return adminoffline.InitialCredentials{
		Email: result.Email, TemporaryPassword: result.TemporaryPassword,
		PublisherToken: result.PublisherToken, PublisherTokenExpiresAt: result.PublisherTokenExpiresAt.Format(time.RFC3339),
	}, err
}

type nativeRecovery struct{ path string }

func (recovery nativeRecovery) Read() ([]byte, error) { return securefs.ReadPrivateFile(recovery.path) }
func (recovery nativeRecovery) Write(contents []byte) error {
	return securefs.WritePrivateFileAtomic(recovery.path, contents)
}
func (recovery nativeRecovery) Remove() error { return os.Remove(recovery.path) }
