package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/config"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type identityAuthorityPoolStub struct {
	closed bool
}

func (*identityAuthorityPoolStub) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("identity authority pool stub cannot begin transactions")
}

func (*identityAuthorityPoolStub) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("identity authority pool stub cannot begin transactions")
}

func (*identityAuthorityPoolStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("identity authority pool stub cannot execute queries")
}

func (*identityAuthorityPoolStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("identity authority pool stub cannot query")
}

func (*identityAuthorityPoolStub) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (p *identityAuthorityPoolStub) Close() { p.closed = true }

func TestBuildIdentityAuthorityMapsControlPoolConfig(t *testing.T) {
	configured := config.Config{
		Production:                            true,
		PostgresControlURL:                    "postgres://identity.example/control?sslmode=verify-full",
		PostgresExpectedMajor:                 17,
		PostgresControlRuntimeRole:            "identity_runtime",
		PostgresControlIntent:                 "read-write",
		PostgresRequireTLS:                    true,
		PostgresControlPoolMinConns:           2,
		PostgresControlPoolMaxConns:           11,
		PostgresControlAcquireTimeout:         2 * time.Second,
		PostgresControlStatementTimeout:       3 * time.Second,
		PostgresControlLockTimeout:            4 * time.Second,
		PostgresControlIdleTransactionTimeout: 5 * time.Second,
	}
	want := platformpostgres.Config{
		URL:                    configured.PostgresControlURL,
		ExpectedMajor:          configured.PostgresExpectedMajor,
		RuntimeRole:            configured.PostgresControlRuntimeRole,
		Intent:                 platformpostgres.IntentReadWrite,
		RequireTLS:             configured.PostgresRequireTLS,
		MinConns:               int32(configured.PostgresControlPoolMinConns),
		MaxConns:               int32(configured.PostgresControlPoolMaxConns),
		AcquireTimeout:         configured.PostgresControlAcquireTimeout,
		StatementTimeout:       configured.PostgresControlStatementTimeout,
		LockTimeout:            configured.PostgresControlLockTimeout,
		IdleTransactionTimeout: configured.PostgresControlIdleTransactionTimeout,
	}
	pool := &identityAuthorityPoolStub{}
	var got platformpostgres.Config
	bundle, err := buildIdentityAuthorityWithOpener(context.Background(), configured, func(_ context.Context, cfg platformpostgres.Config) (identityControlPool, error) {
		got = cfg
		return pool, nil
	})
	if err != nil {
		t.Fatalf("buildIdentityAuthorityWithOpener() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opened PostgreSQL config = %#v, want %#v", got, want)
	}
	if bundle.Repository == nil || bundle.Cleanup == nil {
		t.Fatalf("identity authority bundle = %#v, want repository and cleanup", bundle)
	}
	if bundle.Pool == nil {
		t.Fatal("identity authority bundle did not expose the shared PostgreSQL pool")
	}
	if got, ok := bundle.Pool.(*identityAuthorityPoolStub); !ok || got != pool {
		t.Fatalf("identity authority shared pool = %T/%p, want exact opener pool %T/%p", bundle.Pool, bundle.Pool, pool, pool)
	}
	if err := bundle.Cleanup(context.Background()); err != nil {
		t.Fatalf("identity authority cleanup error = %v", err)
	}
	if !pool.closed {
		t.Fatal("identity authority cleanup did not close the control pool")
	}
}

func TestBuildIdentityAuthorityRejectsControlOnlyPool(t *testing.T) {
	pool := &controlOnlyIdentityPool{}
	_, err := buildIdentityAuthorityWithOpener(context.Background(), config.Config{
		Production: true, PostgresControlURL: "postgres://identity.example/control?sslmode=require",
		PostgresControlRuntimeRole: "identity_runtime", PostgresControlIntent: "read-write", PostgresRequireTLS: true,
		PostgresControlPoolMaxConns: 1,
	}, func(context.Context, platformpostgres.Config) (identityControlPool, error) {
		return pool, nil
	})
	if err == nil {
		t.Fatal("control-only identity pool was accepted")
	}
	if !pool.closed {
		t.Fatal("control-only identity pool was not closed after rejection")
	}
}

type controlOnlyIdentityPool struct{ closed bool }

func (*controlOnlyIdentityPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("control-only pool")
}

func (p *controlOnlyIdentityPool) Close() { p.closed = true }

func TestBuildIdentityAuthorityLocalDoesNotOpenOrFallback(t *testing.T) {
	called := false
	bundle, err := buildIdentityAuthorityWithOpener(context.Background(), config.Config{
		PostgresControlURL:    "not a PostgreSQL URL",
		PostgresControlIntent: "read-only",
	}, func(context.Context, platformpostgres.Config) (identityControlPool, error) {
		called = true
		return nil, errors.New("opener must not be called")
	})
	if err != nil {
		t.Fatalf("local identity authority build error = %v", err)
	}
	if called {
		t.Fatal("local identity authority build opened PostgreSQL")
	}
	if bundle.Repository != nil || bundle.Cleanup != nil {
		t.Fatalf("local identity authority bundle = %#v, want empty bundle", bundle)
	}
}

func TestBuildIdentityAuthorityProductionFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
	}{
		{name: "missing URL", cfg: config.Config{Production: true, PostgresControlIntent: "read-write", PostgresRequireTLS: true}},
		{name: "non read-write intent", cfg: config.Config{Production: true, PostgresControlURL: "postgres://identity.example/control", PostgresControlIntent: "read-only", PostgresRequireTLS: true}},
		{name: "TLS disabled", cfg: config.Config{Production: true, PostgresControlURL: "postgres://identity.example/control", PostgresControlIntent: "read-write"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			_, err := buildIdentityAuthorityWithOpener(context.Background(), tt.cfg, func(context.Context, platformpostgres.Config) (identityControlPool, error) {
				called = true
				return nil, nil
			})
			if err == nil {
				t.Fatal("production identity authority build succeeded")
			}
			if called {
				t.Fatal("production identity authority build opened PostgreSQL after invalid configuration")
			}
		})
	}
}

func TestBuildIdentityAuthorityWrapsOpenError(t *testing.T) {
	wantErr := errors.New("connection unavailable")
	_, err := buildIdentityAuthorityWithOpener(context.Background(), config.Config{
		Production: true, PostgresControlURL: "postgres://identity.example/control?sslmode=require", PostgresControlRuntimeRole: "identity_runtime", PostgresControlIntent: "read-write", PostgresRequireTLS: true,
	}, func(context.Context, platformpostgres.Config) (identityControlPool, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("production identity authority error = %v, want wrapped %v", err, wantErr)
	}
}
