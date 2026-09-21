package adminpostgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

type testAccessInitializer struct {
	initialized bool
	credentials access.InitialInstanceCredentials
	initCalls   int
	lastInput   access.InstanceInitializationInput
}

func (i *testAccessInitializer) Initialized(context.Context) (bool, error) { return i.initialized, nil }

func (i *testAccessInitializer) InitializeInstance(_ context.Context, input access.InstanceInitializationInput, prepare func(access.InitialInstanceCredentials) error) (access.InitialInstanceCredentials, error) {
	i.initCalls++
	i.lastInput = input
	if i.initialized {
		return access.InitialInstanceCredentials{}, access.ErrInstanceAlreadyInitialized
	}
	if err := prepare(i.credentials); err != nil {
		return access.InitialInstanceCredentials{}, err
	}
	i.initialized = true
	return i.credentials, nil
}

type testBootstrap struct {
	bound   string
	missing bool
	binds   int
}

func (b *testBootstrap) InstanceEnvironment(context.Context) (string, error) {
	if b.missing {
		return "", platformbootstrap.ErrNotFound
	}
	return b.bound, nil
}
func (b *testBootstrap) BindInstanceEnvironment(_ context.Context, environment string) error {
	b.binds++
	b.bound, b.missing = environment, false
	return nil
}

type testAdminLock struct{}

func (*testAdminLock) Release() error { return nil }

func productionAdminConfig(home string) config.Config {
	return config.Config{
		HomeDir: home, Production: true, Environment: "prod", BootstrapEmail: "bootstrap@example.com",
		TokenHashKey: strings.Repeat("k", 32), PostgresRequireTLS: true,
		PostgresControlURL: "postgres://runtime/control?sslmode=verify-full",
	}
}

func developmentAdminConfig(home string) config.Config {
	return config.Config{
		HomeDir: home, Environment: "dev", LocalAuth: true,
		PublicURL: "http://127.0.0.1:8080", AllowedHosts: "127.0.0.1,localhost",
		TokenHashKey: strings.Repeat("k", 32), PostgresExpectedMajor: 18,
		PostgresControlURL:             "postgres://runtime:runtime-secret@127.0.0.1:5432/leapview_control?sslmode=disable",
		PostgresControlMigratorURL:     "postgres://migrator:migrator-secret@127.0.0.1:5432/leapview_control?sslmode=disable",
		PostgresControlMaintenanceURL:  "postgres://maintenance:maintenance-secret@127.0.0.1:5432/leapview_control?sslmode=disable",
		PostgresDuckLakeURL:            "postgres://ducklake:ducklake-secret@127.0.0.1:5432/leapview_ducklake?sslmode=disable",
		PostgresDuckLakeMaintenanceURL: "postgres://ducklake-maintenance:ducklake-maintenance-secret@127.0.0.1:5432/leapview_ducklake?sslmode=disable",
	}
}

func skipProductionBaseline(context.Context, config.Config) error { return nil }

func TestProductionInitializeUsesNativeAccessAndDurableRecovery(t *testing.T) {
	home := t.TempDir()
	cfg := productionAdminConfig(home)
	initializer := &testAccessInitializer{credentials: access.InitialInstanceCredentials{
		Email: cfg.BootstrapEmail, TemporaryPassword: "temporary-password", PublisherToken: "publisher-token",
		PublisherTokenExpiresAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}}
	bootstrap := &testBootstrap{missing: true}
	var prepared, opened, verified, acquired int
	var openedConfig platformpostgres.Config
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: func(context.Context, config.Config) error { prepared++; return nil },
		OpenAccess: func(_ context.Context, openedCfg platformpostgres.Config) (AccessPool, error) {
			opened++
			openedConfig = openedCfg
			return &testMaintenancePool{}, nil
		},
		VerifyBaseline: func(context.Context, postgresbaseline.SQLDBProvider) error { verified++; return nil },
		NewAccess:      func(AccessPool, []byte) (AccessInitializer, error) { return initializer, nil },
		NewBootstrap:   func(AccessPool) Bootstrap { return bootstrap },
		AcquireLock: func(string) (adminoffline.Lock, error) {
			acquired++
			return &testAdminLock{}, nil
		},
		Now: func() time.Time { return time.Date(2026, 8, 31, 10, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60)) },
	})

	var out bytes.Buffer
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &out); err != nil {
		t.Fatal(err)
	}
	if prepared != 1 || opened != 1 || verified != 1 || acquired != 1 || openedConfig.URL != cfg.PostgresControlURL || openedConfig.Intent != platformpostgres.IntentReadWrite {
		t.Fatalf("native initialization dependencies prepared=%d opened=%d verified=%d acquired=%d config=%#v", prepared, opened, verified, acquired, openedConfig)
	}
	if bootstrap.binds != 1 || bootstrap.bound != cfg.Environment || initializer.initCalls != 1 || initializer.lastInput.Email != cfg.BootstrapEmail || initializer.lastInput.Environment != cfg.Environment {
		t.Fatalf("native state bind=%d/%q initializer=%d input=%#v", bootstrap.binds, bootstrap.bound, initializer.initCalls, initializer.lastInput)
	}
	credentials, err := adminoffline.DecodeInitialCredentials(out.Bytes())
	if err != nil || credentials.PublisherToken != "publisher-token" {
		t.Fatalf("output credentials=%#v err=%v output=%q", credentials, err, out.String())
	}
	recoveryPath := filepath.Join(home, adminoffline.CredentialRecoveryFileName)
	if _, err := os.Stat(recoveryPath); err != nil {
		t.Fatalf("credential recovery file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "leapview.db")); !os.IsNotExist(err) {
		t.Fatalf("native initialization created offline SQLite state: %v", err)
	}
}

func TestDevelopmentInitializeReplayAndAcknowledgeUsesNativeAccess(t *testing.T) {
	home := t.TempDir()
	cfg := developmentAdminConfig(home)
	initializer := &testAccessInitializer{credentials: access.InitialInstanceCredentials{
		Email: "admin@localhost", TemporaryPassword: "temporary-password", PublisherToken: "publisher-token",
		PublisherTokenExpiresAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}}
	bootstrap := &testBootstrap{missing: true}
	var openedConfig platformpostgres.Config
	var acquired int
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess: func(_ context.Context, opened platformpostgres.Config) (AccessPool, error) {
			openedConfig = opened
			return &testMaintenancePool{}, nil
		},
		VerifyBaseline: func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewAccess:      func(AccessPool, []byte) (AccessInitializer, error) { return initializer, nil },
		NewBootstrap:   func(AccessPool) Bootstrap { return bootstrap },
		AcquireLock: func(string) (adminoffline.Lock, error) {
			acquired++
			return &testAdminLock{}, nil
		},
	})

	var first bytes.Buffer
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &first); err != nil {
		t.Fatalf("initialize local target: %v", err)
	}
	var replay bytes.Buffer
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &replay); err != nil {
		t.Fatalf("replay local initialization: %v", err)
	}
	if replay.String() != first.String() || initializer.initCalls != 1 {
		t.Fatalf("replay changed credentials or reran mutation: calls=%d first=%q replay=%q", initializer.initCalls, first.String(), replay.String())
	}
	if openedConfig.RequireTLS || openedConfig.URL != cfg.PostgresControlURL {
		t.Fatalf("local access config = %#v, want loopback development runtime config", openedConfig)
	}
	if bootstrap.binds != 1 || bootstrap.bound != cfg.Environment || initializer.lastInput.Environment != cfg.Environment || initializer.lastInput.Email != adminoffline.DefaultDevelopmentBootstrapEmail {
		t.Fatalf("local native state bind=%d/%q initializer input=%#v", bootstrap.binds, bootstrap.bound, initializer.lastInput)
	}
	if err := ops.AcknowledgeInitialCredentials(t.Context()); err != nil {
		t.Fatalf("acknowledge local credentials: %v", err)
	}
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{}); !errors.Is(err, adminoffline.ErrInstanceAlreadyInitialized) {
		t.Fatalf("initialize after local acknowledgement error = %v", err)
	}
	if acquired != 4 {
		t.Fatalf("lock acquisitions = %d, want one per local service operation", acquired)
	}
}

func TestDevelopmentInitializeRejectsUnsafeConfigurationBeforeOpeningAccess(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{name: "local auth disabled", mutate: func(cfg *config.Config) { cfg.LocalAuth = false }, want: "LEAPVIEW_LOCAL_AUTH=true"},
		{name: "development auth bypass", mutate: func(cfg *config.Config) { cfg.DevAuthBypass = true }, want: "LEAPVIEW_DEV_AUTH_BYPASS=false"},
		{name: "remote public URL", mutate: func(cfg *config.Config) { cfg.PublicURL = "http://analytics.example.com:8080" }, want: "loopback"},
		{name: "remote allowed host", mutate: func(cfg *config.Config) { cfg.AllowedHosts = "analytics.example.com" }, want: "loopback"},
		{name: "remote plaintext PostgreSQL", mutate: func(cfg *config.Config) {
			cfg.PostgresControlURL = "postgres://runtime:runtime-secret@postgres.internal:5432/leapview_control?sslmode=disable"
		}, want: "loopback"},
		{name: "remote TLS PostgreSQL", mutate: func(cfg *config.Config) {
			cfg.PostgresRequireTLS = true
			cfg.PostgresControlURL = "postgres://runtime:runtime-secret@postgres.internal:5432/leapview_control?sslmode=verify-full"
		}, want: "LEAPVIEW_POSTGRES_CONTROL_URL to target loopback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := developmentAdminConfig(t.TempDir())
			test.mutate(&cfg)
			opened, acquired := false, false
			ops := New(Dependencies{
				LoadConfig: func() (config.Config, error) { return cfg, nil },
				OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
					opened = true
					return &testMaintenancePool{}, nil
				},
				AcquireLock: func(string) (adminoffline.Lock, error) {
					acquired = true
					return &testAdminLock{}, nil
				},
			})
			err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe local config error = %v, want %q", err, test.want)
			}
			if opened || acquired {
				t.Fatalf("unsafe local config opened access or acquired lock: opened=%t acquired=%t", opened, acquired)
			}
		})
	}
}

func TestDevelopmentInitializeMigrationFailureRemainsRecoverableBeforeAccessMutation(t *testing.T) {
	cfg := developmentAdminConfig(t.TempDir())
	var opened, acquired bool
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: func(context.Context, config.Config) error {
			return errors.New("migration interrupted")
		},
		OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
			opened = true
			return &testMaintenancePool{}, nil
		},
		AcquireLock: func(string) (adminoffline.Lock, error) {
			acquired = true
			return &testAdminLock{}, nil
		},
	})
	err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "migration interrupted") || opened || acquired {
		t.Fatalf("migration failure error=%v opened=%t acquired=%t", err, opened, acquired)
	}
}

func TestProductionInitializeReplayAndAcknowledgeRedactsRecovery(t *testing.T) {
	home := t.TempDir()
	cfg := productionAdminConfig(home)
	initializer := &testAccessInitializer{credentials: access.InitialInstanceCredentials{
		Email: cfg.BootstrapEmail, TemporaryPassword: "temporary-password", PublisherToken: "publisher-token",
		PublisherTokenExpiresAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}}
	bootstrap := &testBootstrap{bound: cfg.Environment}
	var acquired int
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess:      func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewAccess:       func(AccessPool, []byte) (AccessInitializer, error) { return initializer, nil },
		NewBootstrap:    func(AccessPool) Bootstrap { return bootstrap },
		AcquireLock:     func(string) (adminoffline.Lock, error) { acquired++; return &testAdminLock{}, nil },
	})
	var first bytes.Buffer
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &first); err != nil {
		t.Fatal(err)
	}
	var replay bytes.Buffer
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &replay); err != nil {
		t.Fatalf("replay initialization: %v", err)
	}
	if replay.String() != first.String() || initializer.initCalls != 1 {
		t.Fatalf("replay changed credentials or reran mutation: calls=%d first=%q replay=%q", initializer.initCalls, first.String(), replay.String())
	}
	if err := ops.AcknowledgeInitialCredentials(t.Context()); err != nil {
		t.Fatalf("acknowledge credentials: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, adminoffline.CredentialRecoveryFileName)); !os.IsNotExist(err) {
		t.Fatalf("acknowledged recovery file still present: %v", err)
	}
	if err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{}); !errors.Is(err, adminoffline.ErrInstanceAlreadyInitialized) {
		t.Fatalf("initialize after acknowledgement error = %v", err)
	}
	if acquired != 4 {
		t.Fatalf("lock acquisitions = %d, want one per service operation", acquired)
	}
}

func TestProductionInitializeBaselineMismatchStopsBeforeNativeMutation(t *testing.T) {
	home := t.TempDir()
	cfg := productionAdminConfig(home)
	p := &testMaintenancePool{}
	var constructed, acquired bool
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess:      func(context.Context, platformpostgres.Config) (AccessPool, error) { return p, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return errors.New("baseline mismatch") },
		NewAccess: func(AccessPool, []byte) (AccessInitializer, error) {
			constructed = true
			return &testAccessInitializer{}, nil
		},
		AcquireLock: func(string) (adminoffline.Lock, error) { acquired = true; return &testAdminLock{}, nil },
	})
	err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "baseline") || constructed || acquired || !p.closed {
		t.Fatalf("baseline failure error=%v constructed=%t acquired=%t closed=%t", err, constructed, acquired, p.closed)
	}
	if entries, readErr := os.ReadDir(home); readErr != nil || len(entries) != 0 {
		t.Fatalf("baseline failure created local state: entries=%v err=%v", entries, readErr)
	}
}

func TestProductionInitializeEnvironmentConflictStopsBeforeNativeMutation(t *testing.T) {
	cfg := productionAdminConfig(t.TempDir())
	bootstrap := &testBootstrap{bound: "staging"}
	initializer := &testAccessInitializer{}
	var acquired bool
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess:      func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewAccess:       func(AccessPool, []byte) (AccessInitializer, error) { return initializer, nil },
		NewBootstrap:    func(AccessPool) Bootstrap { return bootstrap },
		AcquireLock:     func(string) (adminoffline.Lock, error) { acquired = true; return &testAdminLock{}, nil },
	})
	err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "bound to environment") || !acquired || initializer.initCalls != 0 || bootstrap.binds != 0 {
		t.Fatalf("environment conflict error=%v acquired=%t initCalls=%d binds=%d", err, acquired, initializer.initCalls, bootstrap.binds)
	}
}

func TestProductionInitializeOpenFailureDoesNotConstructNativeState(t *testing.T) {
	cfg := productionAdminConfig(t.TempDir())
	var constructed bool
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
			return nil, errors.New("database unavailable")
		},
		NewAccess: func(AccessPool, []byte) (AccessInitializer, error) {
			constructed = true
			return &testAccessInitializer{}, nil
		},
	})
	err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "open PostgreSQL access pool") || constructed {
		t.Fatalf("access open failure error=%v constructed=%t", err, constructed)
	}
}

func TestProductionInitializeRejectsPlaintextBeforeOpeningAccess(t *testing.T) {
	cfg := productionAdminConfig(t.TempDir())
	cfg.PostgresRequireTLS = false
	opened := false
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess: func(context.Context, platformpostgres.Config) (AccessPool, error) {
			opened = true
			return &testMaintenancePool{}, nil
		},
	})

	err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "REQUIRE_TLS=true") || opened {
		t.Fatalf("plaintext initialization error=%v opened=%t", err, opened)
	}
}

func TestProductionAcknowledgeUninitializedDoesNotBindEnvironment(t *testing.T) {
	cfg := productionAdminConfig(t.TempDir())
	bootstrap := &testBootstrap{missing: true}
	initializer := &testAccessInitializer{}
	var acquired bool
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess:      func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewAccess:       func(AccessPool, []byte) (AccessInitializer, error) { return initializer, nil },
		NewBootstrap:    func(AccessPool) Bootstrap { return bootstrap },
		AcquireLock:     func(string) (adminoffline.Lock, error) { acquired = true; return &testAdminLock{}, nil },
	})
	err := ops.AcknowledgeInitialCredentials(t.Context())
	if err == nil || !strings.Contains(err.Error(), "has not been initialized") {
		t.Fatalf("uninitialized acknowledgement error = %v", err)
	}
	if acquired || bootstrap.binds != 0 {
		t.Fatalf("uninitialized acknowledgement side effects: acquired=%t binds=%d", acquired, bootstrap.binds)
	}
}

func TestProductionInitializeRejectsTypedNilBootstrap(t *testing.T) {
	cfg := productionAdminConfig(t.TempDir())
	var bootstrap *testBootstrap
	ops := New(Dependencies{
		LoadConfig:      func() (config.Config, error) { return cfg, nil },
		PrepareBaseline: skipProductionBaseline,
		OpenAccess:      func(context.Context, platformpostgres.Config) (AccessPool, error) { return &testMaintenancePool{}, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return nil },
		NewBootstrap:    func(AccessPool) Bootstrap { return bootstrap },
	})
	err := ops.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "bootstrap authority") {
		t.Fatalf("typed nil bootstrap error = %v", err)
	}
}
