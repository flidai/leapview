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
	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	bootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeControlPool struct{ closed bool }

func (*fakeControlPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}
func (*fakeControlPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}
func (*fakeControlPool) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (*fakeControlPool) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected Begin")
}
func (*fakeControlPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("unexpected BeginTx")
}
func (pool *fakeControlPool) Close() { pool.closed = true }

type fakeBootstrap struct {
	id          string
	environment string
}

func (bootstrap *fakeBootstrap) InstanceID(context.Context) (string, error) {
	if bootstrap.id == "" {
		bootstrap.id = "lvinst_0123456789abcdefghijklmnopqrstuv"
	}
	return bootstrap.id, nil
}
func (bootstrap *fakeBootstrap) InstanceEnvironment(context.Context) (string, error) {
	if bootstrap.environment == "" {
		return "", bootstrappostgres.ErrNotFound
	}
	return bootstrap.environment, nil
}
func (bootstrap *fakeBootstrap) BindInstanceEnvironment(_ context.Context, environment string) error {
	bootstrap.environment = environment
	return nil
}

type fakeInitializer struct {
	initialized bool
	calls       int
}

func (initializer *fakeInitializer) Initialized(context.Context) (bool, error) {
	return initializer.initialized, nil
}
func (initializer *fakeInitializer) InitializeInstance(_ context.Context, input access.InstanceInitializationInput, prepare func(access.InitialInstanceCredentials) error) (access.InitialInstanceCredentials, error) {
	initializer.calls++
	credentials := access.InitialInstanceCredentials{
		Email: input.Email, TemporaryPassword: "temporary-password", PublisherToken: "publisher-token",
		PublisherTokenExpiresAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
	}
	if err := prepare(credentials); err != nil {
		return access.InitialInstanceCredentials{}, err
	}
	initializer.initialized = true
	return credentials, nil
}

type fakeLock struct{}

func (fakeLock) Release() error { return nil }

func productionConfig(home string) config.Config {
	return config.Config{
		HomeDir: home, Production: true, Environment: "prod", BootstrapEmail: "admin@example.com",
		PostgresControlURL:         "postgres://runtime:runtime-secret@db.example/control?sslmode=require",
		PostgresControlMigratorURL: "postgres://migrator:migrator-secret@db.example/control?sslmode=require",
		PostgresControlRuntimeRole: "leapview_control_runtime", PostgresControlMigratorRole: "leapview_control_migrator",
		PostgresControlIntent: "read-write", PostgresRequireTLS: true, PostgresExpectedMajor: 18,
		PostgresControlPoolMinConns: 1, PostgresControlPoolMaxConns: 8,
		TokenHashKey: strings.Repeat("k", 32),
	}
}

func TestProductionInitializeUsesNativeAuthoritiesAndRecoveryReplay(t *testing.T) {
	home := t.TempDir()
	cfg := productionConfig(home)
	pool := &fakeControlPool{}
	bootstrap := &fakeBootstrap{}
	initializer := &fakeInitializer{}
	prepareCalls, verifyCalls := 0, 0
	operations := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return cfg, nil },
		PrepareSchema: func(_ context.Context, got platformpostgres.Config) error {
			prepareCalls++
			if got.URL != cfg.PostgresControlMigratorURL || got.RuntimeRole != "leapview_control_migrator" || got.MaxConns != 1 {
				t.Fatalf("migrator config = %#v", got)
			}
			return nil
		},
		OpenRuntime: func(_ context.Context, got platformpostgres.Config) (controlPool, error) {
			if got.URL != cfg.PostgresControlURL || got.RuntimeRole != "leapview_control_runtime" {
				t.Fatalf("runtime config = %#v", got)
			}
			return pool, nil
		},
		VerifyMigrations: func(context.Context, migrations.RevisionReader) error {
			verifyCalls++
			return nil
		},
		NewAccess: func(platformpostgres.DBTX, []byte) (accessmodule.PostgresInstanceInitializer, error) {
			return initializer, nil
		},
		NewBootstrap: func(platformpostgres.DBTX) Bootstrap { return bootstrap },
		AcquireLock:  func(string) (adminoffline.Lock, error) { return fakeLock{}, nil },
	})

	var first bytes.Buffer
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &first); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if prepareCalls != 1 || verifyCalls != 1 || initializer.calls != 1 || bootstrap.environment != "prod" || !pool.closed {
		t.Fatalf("native calls = prepare:%d verify:%d initialize:%d environment:%q closed:%t", prepareCalls, verifyCalls, initializer.calls, bootstrap.environment, pool.closed)
	}
	if _, err := os.Stat(filepath.Join(home, "leapview.db")); !os.IsNotExist(err) {
		t.Fatalf("production initialization touched SQLite: %v", err)
	}

	pool.closed = false
	var replay bytes.Buffer
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &replay); err != nil {
		t.Fatalf("Initialize() replay error = %v", err)
	}
	if replay.String() != first.String() || initializer.calls != 1 {
		t.Fatalf("replay = %q calls=%d, want exact %q and one mutation", replay.String(), initializer.calls, first.String())
	}
	if err := operations.AcknowledgeInitialCredentials(t.Context()); err != nil {
		t.Fatalf("AcknowledgeInitialCredentials() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, adminoffline.CredentialRecoveryFileName)); !os.IsNotExist(err) {
		t.Fatalf("credential recovery remains after acknowledgement: %v", err)
	}
}

func TestProductionInitializeValidatesOutputBeforeSchemaMutation(t *testing.T) {
	cfg := productionConfig(t.TempDir())
	called := false
	operations := New(Dependencies{
		LoadConfig:    func() (config.Config, error) { return cfg, nil },
		PrepareSchema: func(context.Context, platformpostgres.Config) error { called = true; return nil },
	})
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, nil); err == nil {
		t.Fatal("Initialize() accepted nil output")
	}
	if called {
		t.Fatal("schema preparation ran before output validation")
	}
}
