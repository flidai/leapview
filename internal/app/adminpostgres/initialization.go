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
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// Initialize performs production bootstrap through the native PostgreSQL
// access authority. Evaluation and development continue to use the existing
// offline service, including its local credential recovery behavior.
func (o Operations) Initialize(ctx context.Context, request adminoffline.InitializeRequest, out io.Writer) error {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production || cfg.EvaluationMode {
		return o.Operations.Initialize(ctx, request, out)
	}
	// Match the offline service's admission checks before opening a native
	// connection or taking the instance lock. A nil writer must not allow a
	// committed bootstrap whose credentials can no longer be handed off.
	if request.Format != "json" {
		return fmt.Errorf("admin initialize supports only --format json")
	}
	if out == nil {
		return errors.New("admin initialize output is required")
	}
	accessConfig, err := productionAccessConfig(cfg)
	if err != nil {
		return err
	}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	pool, err := deps.OpenAccess(ctx, accessConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL access pool: %w", err)
	}
	if nilAccessPool(pool) {
		return errors.New("open PostgreSQL access pool returned nil pool")
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return fmt.Errorf("verify PostgreSQL control baseline before initialization: %w", err)
	}
	bootstrap := deps.NewBootstrap(pool)
	if nilBootstrap(bootstrap) {
		return errors.New("construct PostgreSQL bootstrap authority returned nil authority")
	}
	initializer, err := deps.NewAccess(pool, key)
	if err != nil {
		return fmt.Errorf("construct PostgreSQL access initializer: %w", err)
	}
	if nilAccessInitializer(initializer) {
		return errors.New("construct PostgreSQL access initializer returned nil initializer")
	}
	service := adminoffline.New(adminoffline.Config{
		HomeDir: cfg.HomeDir, Environment: cfg.Environment, Production: cfg.Production,
		BootstrapEmail: cfg.BootstrapEmail,
	}, adminoffline.Dependencies{
		Locker:      nativeLocker{home: cfg.HomeDir, acquire: deps.AcquireLock},
		State:       nativeState{bootstrap: bootstrap, initialized: initializer},
		Initializer: nativeInitializer{initializer: initializer},
		Recovery:    nativeRecovery{path: filepath.Join(cfg.HomeDir, adminoffline.CredentialRecoveryFileName)},
		Now:         deps.Now,
	})
	return service.Initialize(ctx, request, out)
}

// AcknowledgeInitialCredentials removes the recoverable production bootstrap
// bundle only after confirming that the native instance marker exists.
// Evaluation and development retain the offline implementation unchanged.
func (o Operations) AcknowledgeInitialCredentials(ctx context.Context) error {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production || cfg.EvaluationMode {
		return o.Operations.AcknowledgeInitialCredentials(ctx)
	}
	accessConfig, err := productionAccessConfig(cfg)
	if err != nil {
		return err
	}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	pool, err := deps.OpenAccess(ctx, accessConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL access pool: %w", err)
	}
	if nilAccessPool(pool) {
		return errors.New("open PostgreSQL access pool returned nil pool")
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return fmt.Errorf("verify PostgreSQL control baseline before credential acknowledgement: %w", err)
	}
	bootstrap := deps.NewBootstrap(pool)
	if nilBootstrap(bootstrap) {
		return errors.New("construct PostgreSQL bootstrap authority returned nil authority")
	}
	initializer, err := deps.NewAccess(pool, key)
	if err != nil {
		return fmt.Errorf("construct PostgreSQL access initializer: %w", err)
	}
	if nilAccessInitializer(initializer) {
		return errors.New("construct PostgreSQL access initializer returned nil initializer")
	}
	initialized, err := initializer.Initialized(ctx)
	if err != nil {
		return fmt.Errorf("check PostgreSQL instance initialization marker: %w", err)
	}
	if !initialized {
		return fmt.Errorf("LeapView instance has not been initialized")
	}
	service := adminoffline.New(adminoffline.Config{
		HomeDir: cfg.HomeDir, Environment: cfg.Environment, Production: cfg.Production,
		BootstrapEmail: cfg.BootstrapEmail,
	}, adminoffline.Dependencies{
		Locker:   nativeLocker{home: cfg.HomeDir, acquire: deps.AcquireLock},
		State:    nativeState{bootstrap: bootstrap, initialized: initializer},
		Recovery: nativeRecovery{path: filepath.Join(cfg.HomeDir, adminoffline.CredentialRecoveryFileName)},
		Now:      deps.Now,
	})
	return service.AcknowledgeInitialCredentials(ctx)
}

func nilAccessPool(pool AccessPool) bool {
	if pool == nil {
		return true
	}
	rv := reflect.ValueOf(pool)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func nilAccessInitializer(initializer AccessInitializer) bool {
	if initializer == nil {
		return true
	}
	rv := reflect.ValueOf(initializer)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func nilBootstrap(bootstrap Bootstrap) bool {
	if bootstrap == nil {
		return true
	}
	rv := reflect.ValueOf(bootstrap)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func accessFingerprintKey(cfg config.Config) ([]byte, error) {
	key := []byte(strings.TrimSpace(cfg.TokenHashKey))
	if len(key) < 32 {
		key = []byte(strings.TrimSpace(cfg.CSRFKey))
	}
	if len(key) < 32 {
		return nil, errors.New("PostgreSQL access fingerprint key is required")
	}
	return key, nil
}

func productionAccessConfig(cfg config.Config) (platformpostgres.Config, error) {
	if !cfg.PostgresRequireTLS {
		return platformpostgres.Config{}, errors.New("production admin initialization requires LEAPVIEW_POSTGRES_REQUIRE_TLS=true")
	}
	accessConfig := cfg.PostgresControlPlaneConfig().Runtime
	if err := accessConfig.Validate(); err != nil {
		return platformpostgres.Config{}, fmt.Errorf("invalid PostgreSQL control runtime configuration: %w", err)
	}
	return accessConfig, nil
}

type nativeLocker struct {
	home    string
	acquire func(string) (adminoffline.Lock, error)
}

func (l nativeLocker) Acquire(_ context.Context) (adminoffline.Lock, error) {
	if l.acquire == nil {
		return nil, errors.New("instance initialization locker is unavailable")
	}
	return l.acquire(l.home)
}

type nativeState struct {
	bootstrap   Bootstrap
	initialized AccessInitializer
}

func (s nativeState) Environment(ctx context.Context) (string, error) {
	if nilBootstrap(s.bootstrap) {
		return "", errors.New("PostgreSQL bootstrap authority is unavailable")
	}
	environment, err := s.bootstrap.InstanceEnvironment(ctx)
	if errors.Is(err, platformbootstrap.ErrNotFound) {
		return "", adminoffline.ErrStateNotFound
	}
	return environment, err
}

func (s nativeState) ExistingEnvironment(ctx context.Context) (string, bool, error) {
	environment, err := s.Environment(ctx)
	if errors.Is(err, adminoffline.ErrStateNotFound) {
		return "", false, nil
	}
	return environment, err == nil, err
}

func (s nativeState) BindEnvironment(ctx context.Context, environment string) error {
	if nilBootstrap(s.bootstrap) {
		return errors.New("PostgreSQL bootstrap authority is unavailable")
	}
	return s.bootstrap.BindInstanceEnvironment(ctx, environment)
}

func (s nativeState) Initialized(ctx context.Context) (bool, error) {
	if s.initialized == nil {
		return false, errors.New("PostgreSQL access initializer is unavailable")
	}
	return s.initialized.Initialized(ctx)
}

type nativeInitializer struct {
	initializer AccessInitializer
}

func (n nativeInitializer) Initialize(ctx context.Context, input adminoffline.InitializationInput, prepare func(adminoffline.InitialCredentials) error) (adminoffline.InitialCredentials, error) {
	if n.initializer == nil {
		return adminoffline.InitialCredentials{}, errors.New("PostgreSQL access initializer is unavailable")
	}
	result, err := n.initializer.InitializeInstance(ctx, access.InstanceInitializationInput{
		Email: input.Email, Environment: input.Environment, Now: input.Now,
	}, func(credentials access.InitialInstanceCredentials) error {
		if prepare == nil {
			return nil
		}
		return prepare(adminoffline.InitialCredentials{
			Email: credentials.Email, TemporaryPassword: credentials.TemporaryPassword,
			PublisherToken:          credentials.PublisherToken,
			PublisherTokenExpiresAt: credentials.PublisherTokenExpiresAt.Format(time.RFC3339),
		})
	})
	if errors.Is(err, access.ErrInstanceAlreadyInitialized) {
		err = adminoffline.ErrInstanceAlreadyInitialized
	}
	return adminoffline.InitialCredentials{
		Email: result.Email, TemporaryPassword: result.TemporaryPassword,
		PublisherToken:          result.PublisherToken,
		PublisherTokenExpiresAt: result.PublisherTokenExpiresAt.Format(time.RFC3339),
	}, err
}

type nativeRecovery struct{ path string }

func (r nativeRecovery) Read() ([]byte, error) { return securefs.ReadPrivateFile(r.path) }
func (r nativeRecovery) Write(contents []byte) error {
	return securefs.WritePrivateFileAtomic(r.path, contents)
}
func (r nativeRecovery) Remove() error { return os.Remove(r.path) }
