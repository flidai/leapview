package adminpostgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/app/postgresmaintenance"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type testMaintenancePool struct {
	closed bool
}

func (p *testMaintenancePool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *testMaintenancePool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (p *testMaintenancePool) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (p *testMaintenancePool) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unused")
}
func (p *testMaintenancePool) SQLDB() (*sql.DB, error) { return nil, errors.New("unused") }
func (p *testMaintenancePool) Close()                  { p.closed = true }

type testNative struct {
	preview bool
	run     bool
	policy  postgresmaintenance.Policy
	result  postgresmaintenance.Result
}

func (n *testNative) Preview(_ context.Context, policy postgresmaintenance.Policy) (postgresmaintenance.Result, error) {
	n.preview, n.policy = true, policy
	return n.result, nil
}
func (n *testNative) Run(_ context.Context, policy postgresmaintenance.Policy) (postgresmaintenance.Result, error) {
	n.run, n.policy = true, policy
	return n.result, nil
}

func validProductionMaintenanceConfig() config.Config {
	return config.Config{
		Production: true, PostgresRequireTLS: true,
		PostgresControlURL:             "postgres://runtime:secret@control/leapview?sslmode=require",
		PostgresControlMigratorURL:     "postgres://migrator:secret@control/leapview?sslmode=require",
		PostgresControlMaintenanceURL:  "postgres://maintenance:secret@control/leapview?sslmode=require",
		PostgresDuckLakeURL:            "postgres://ducklake:secret@ducklake/leapview?sslmode=require",
		PostgresDuckLakeMaintenanceURL: "postgres://ducklake-maintenance:secret@ducklake/leapview?sslmode=require",
		DeliveryPhysicalPoolID:         "pool-prod", DeliveryPhysicalPoolCompatibilityDigest: "sha256:" + strings.Repeat("a", 64),
	}
}

func TestMapMaintenanceRequestUsesExplicitBoundedDefaults(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 123, time.FixedZone("PDT", -7*60*60))
	policy, err := MapMaintenanceRequest(admincli.MaintenanceRequest{AuditDays: 10, QueryDays: 11, ArchivedAgentDays: 12, AuthStateDays: 13}, now)
	if err != nil {
		t.Fatal(err)
	}
	utc := now.UTC()
	if !policy.Operations.Before.Equal(utc) || policy.Operations.Limit != maintenanceBatchLimit {
		t.Fatalf("operations policy = %#v", policy.Operations)
	}
	if !policy.Jobs.Before.Equal(utc.Add(-30*24*time.Hour)) || !policy.ManagedData.Before.Equal(utc.Add(-30*24*time.Hour)) {
		t.Fatalf("default cutoffs = jobs %s uploads %s", policy.Jobs.Before, policy.ManagedData.Before)
	}
	if !policy.DashboardUsage.Before.Equal(utc.Add(-90*24*time.Hour)) || !policy.DashboardStreams.Now.Equal(utc) {
		t.Fatalf("dashboard defaults = usage %s streams %s", policy.DashboardUsage.Before, policy.DashboardStreams.Now)
	}
	if !policy.AccessAudit.Short.Before.Equal(utc.Add(-10*24*time.Hour)) || policy.AccessAudit.Short.Disabled || policy.QueryAudit.Disabled || policy.AgentHistory.Disabled || policy.AccessAuthState.Disabled {
		t.Fatalf("requested evidence policy = %#v", policy)
	}
}

func TestMapMaintenanceRequestZeroDisablesOnlyRequestedEvidence(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	policy, err := MapMaintenanceRequest(admincli.MaintenanceRequest{AuditDays: 0, QueryDays: 0, ArchivedAgentDays: 7, AuthStateDays: 0}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AccessAudit.Short.Disabled || !policy.AccessAudit.Standard.Disabled || !policy.AccessAudit.Security.Disabled || !policy.QueryAudit.Disabled || !policy.AccessAuthState.Disabled {
		t.Fatalf("disabled evidence windows = %#v", policy)
	}
	if policy.AgentHistory.Disabled || !policy.AgentHistory.Before.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("agent evidence unexpectedly disabled: %#v", policy.AgentHistory)
	}
	if !policy.Events.Before.Equal(now.Add(-defaultEventRetention)) {
		t.Fatalf("events cutoff = %s, want safe default %s", policy.Events.Before, now.Add(-defaultEventRetention))
	}
	if policy.Operations.Limit != maintenanceBatchLimit || policy.Jobs.Limit != maintenanceBatchLimit {
		t.Fatalf("operational limits were disabled: %#v", policy)
	}
}

func TestProductionMaintenanceUsesOnePoolBaselineAndPreviewOrApply(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		apply bool
	}{
		{name: "preview"},
		{name: "apply", apply: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := &testMaintenancePool{}
			native := &testNative{result: postgresmaintenance.Result{OperationsRemoved: 2}}
			var opened platformpostgres.Config
			var verified bool
			var constructed postgresmaintenance.NativeDB
			ops := New(Dependencies{
				LoadConfig: func() (config.Config, error) {
					return validProductionMaintenanceConfig(), nil
				},
				OpenMaintenance: func(_ context.Context, cfg platformpostgres.Config) (MaintenancePool, error) {
					opened = cfg
					return pool, nil
				},
				VerifyBaseline: func(_ context.Context, _ postgresbaseline.SQLDBProvider) error {
					verified = true
					return nil
				},
				NewNative: func(db postgresmaintenance.NativeDB) (Native, error) {
					constructed = db
					return native, nil
				},
				Now: func() time.Time { return now },
			})
			var out bytes.Buffer
			err := ops.Maintenance(t.Context(), admincli.MaintenanceRequest{Apply: test.apply, AuditDays: 10, QueryDays: 11, ArchivedAgentDays: 12, AuthStateDays: 13}, &out)
			if err != nil {
				t.Fatal(err)
			}
			if opened.URL != validProductionMaintenanceConfig().PostgresControlMaintenanceURL || opened.MinConns != 1 || opened.MaxConns != 1 || opened.Intent != platformpostgres.IntentReadWrite {
				t.Fatalf("opened maintenance config = %#v", opened)
			}
			if !verified || constructed == nil || !pool.closed {
				t.Fatalf("pool lifecycle: verified=%t constructed=%t closed=%t", verified, constructed != nil, pool.closed)
			}
			if native.preview == test.apply || native.run != test.apply {
				t.Fatalf("preview/run calls = %t/%t, apply=%t", native.preview, native.run, test.apply)
			}
			wantMode := "preview"
			if test.apply {
				wantMode = "apply"
			}
			if !strings.Contains(out.String(), "mode: "+wantMode) || strings.Contains(out.String(), "secret") {
				t.Fatalf("evidence output = %q", out.String())
			}
		})
	}
}

func TestProductionMaintenanceBaselineMismatchFailsClosed(t *testing.T) {
	pool := &testMaintenancePool{}
	constructed := false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) {
			return validProductionMaintenanceConfig(), nil
		},
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) { return pool, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.SQLDBProvider) error { return errors.New("baseline mismatch") },
		NewNative: func(postgresmaintenance.NativeDB) (Native, error) {
			constructed = true
			return &testNative{}, nil
		},
		Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	})
	err := ops.Maintenance(t.Context(), admincli.MaintenanceRequest{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("baseline mismatch error = %v", err)
	}
	if constructed || !pool.closed {
		t.Fatalf("baseline failure did not stop construction/close: constructed=%t closed=%t", constructed, pool.closed)
	}
}

func TestMaintenanceRejectsNegativeRetentionBeforeLoadingProductionConfig(t *testing.T) {
	loaded := false
	ops := Operations{Dependencies: Dependencies{LoadConfig: func() (config.Config, error) {
		loaded = true
		return config.Config{Production: true}, nil
	}}}
	err := ops.Maintenance(t.Context(), admincli.MaintenanceRequest{QueryDays: -1}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "zero or greater") {
		t.Fatalf("negative retention error = %v", err)
	}
	if loaded {
		t.Fatal("negative retention loaded production config")
	}
}

func TestMaintenanceRejectsOverflowingRetentionBeforeLoadingProductionConfig(t *testing.T) {
	loaded := false
	ops := Operations{Dependencies: Dependencies{LoadConfig: func() (config.Config, error) {
		loaded = true
		return validProductionMaintenanceConfig(), nil
	}}}
	err := ops.Maintenance(t.Context(), admincli.MaintenanceRequest{AuditDays: maxRetentionDays + 1}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("overflowing retention error = %v", err)
	}
	if loaded {
		t.Fatal("overflowing retention loaded production config")
	}
}

func TestMaintenanceRejectsInsecureOrAliasedProductionConfigurationBeforeOpeningPool(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "TLS disabled", mutate: func(cfg *config.Config) { cfg.PostgresRequireTLS = false }},
		{name: "maintenance aliases runtime", mutate: func(cfg *config.Config) { cfg.PostgresControlMaintenanceURL = cfg.PostgresControlURL }},
		{name: "unsupported maintenance role", mutate: func(cfg *config.Config) { cfg.PostgresControlMaintenanceRole = "provider_maintenance" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionMaintenanceConfig()
			test.mutate(&cfg)
			opened := false
			ops := New(Dependencies{
				LoadConfig: func() (config.Config, error) { return cfg, nil },
				OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) {
					opened = true
					return &testMaintenancePool{}, nil
				},
			})
			err := ops.Maintenance(t.Context(), admincli.MaintenanceRequest{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "validate production PostgreSQL") {
				t.Fatalf("invalid production maintenance error = %v", err)
			}
			if opened {
				t.Fatal("invalid production maintenance opened the pool")
			}
		})
	}
}

func TestMaintenanceRejectsLocalTargetsWithoutOpeningSQLite(t *testing.T) {
	loaded := false
	ops := New(Dependencies{LoadConfig: func() (config.Config, error) {
		loaded = true
		return config.Config{Production: false}, nil
	}})
	err := ops.Maintenance(t.Context(), admincli.MaintenanceRequest{}, &bytes.Buffer{})
	if !errors.Is(err, ErrNativeMaintenanceUnavailable) {
		t.Fatalf("local maintenance error = %v, want ErrNativeMaintenanceUnavailable", err)
	}
	if !loaded {
		t.Fatal("local maintenance did not load configuration")
	}
}
