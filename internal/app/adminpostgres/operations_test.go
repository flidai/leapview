package adminpostgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/app/postgresmaintenance"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type testMaintenancePool struct {
	revision platformpostgres.SchemaRevision
	closed   bool
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
func (p *testMaintenancePool) SchemaRevision(context.Context, int64) (platformpostgres.SchemaRevision, error) {
	return p.revision, nil
}
func (p *testMaintenancePool) Close() { p.closed = true }

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

func TestMapMaintenanceRequestUsesExplicitBoundedDefaults(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 123, time.FixedZone("PDT", -7*60*60))
	policy, err := MapMaintenanceRequest(adminoffline.MaintenanceRequest{AuditDays: 10, QueryDays: 11, ArchivedAgentDays: 12, AuthStateDays: 13}, now)
	if err != nil {
		t.Fatal(err)
	}
	utc := now.UTC()
	if !policy.Operations.Before.Equal(utc) || policy.Operations.Limit != maintenanceBatchLimit {
		t.Fatalf("operations policy = %#v", policy.Operations)
	}
	if !policy.Jobs.Before.Equal(utc.Add(-30*24*time.Hour)) || !policy.Cache.Before.Equal(utc.Add(-24*time.Hour)) || !policy.ManagedData.Before.Equal(utc.Add(-30*24*time.Hour)) {
		t.Fatalf("default cutoffs = jobs %s cache %s uploads %s", policy.Jobs.Before, policy.Cache.Before, policy.ManagedData.Before)
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
	policy, err := MapMaintenanceRequest(adminoffline.MaintenanceRequest{AuditDays: 0, QueryDays: 0, ArchivedAgentDays: 7, AuthStateDays: 0}, now)
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
	if policy.Operations.Limit != maintenanceBatchLimit || policy.Jobs.Limit != maintenanceBatchLimit || policy.Cache.Limit != maintenanceBatchLimit {
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
			pool := &testMaintenancePool{revision: platformpostgres.SchemaRevision{Revision: postgresbaseline.BaselineRevision, MigrationID: postgresbaseline.BaselineMigrationID, Checksum: postgresbaseline.Checksum()}}
			native := &testNative{result: postgresmaintenance.Result{OperationsRemoved: 2}}
			var opened platformpostgres.Config
			var verified bool
			var constructed postgresmaintenance.NativeDB
			ops := New(Dependencies{
				LoadConfig: func() (config.Config, error) {
					return config.Config{Production: true, PostgresControlMaintenanceURL: "postgres://maintenance/control"}, nil
				},
				OpenMaintenance: func(_ context.Context, cfg platformpostgres.Config) (MaintenancePool, error) {
					opened = cfg
					return pool, nil
				},
				VerifyBaseline: func(_ context.Context, _ postgresbaseline.RevisionReader) error {
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
			err := ops.Maintenance(t.Context(), adminoffline.MaintenanceRequest{Apply: test.apply, AuditDays: 10, QueryDays: 11, ArchivedAgentDays: 12, AuthStateDays: 13}, &out)
			if err != nil {
				t.Fatal(err)
			}
			if opened.URL != "postgres://maintenance/control" || opened.MinConns != 1 || opened.MaxConns != 1 || opened.Intent != platformpostgres.IntentReadWrite {
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
			return config.Config{Production: true, PostgresControlMaintenanceURL: "postgres://maintenance/control"}, nil
		},
		OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) { return pool, nil },
		VerifyBaseline:  func(context.Context, postgresbaseline.RevisionReader) error { return errors.New("baseline mismatch") },
		NewNative: func(postgresmaintenance.NativeDB) (Native, error) {
			constructed = true
			return &testNative{}, nil
		},
		Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	})
	err := ops.Maintenance(t.Context(), adminoffline.MaintenanceRequest{}, &bytes.Buffer{})
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
	err := ops.Maintenance(t.Context(), adminoffline.MaintenanceRequest{QueryDays: -1}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "zero or greater") {
		t.Fatalf("negative retention error = %v", err)
	}
	if loaded {
		t.Fatal("negative retention loaded production config")
	}
}
