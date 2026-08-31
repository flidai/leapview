package adminoffline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
)

func TestProductionRejectsEveryLegacyAdminOperationBeforeOpeningState(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		call func(context.Context, io.Writer) error
	}{
		{
			name: "initialize",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).Initialize(ctx, adminoffline.InitializeRequest{Format: "json"}, out)
			},
		},
		{
			name: "acknowledge initial credentials",
			call: func(ctx context.Context, _ io.Writer) error {
				return (Operations{}).AcknowledgeInitialCredentials(ctx)
			},
		},
		{
			name: "storage cleanup",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).StorageCleanup(ctx, adminoffline.StorageCleanupRequest{}, out)
			},
		},
		{
			name: "maintenance",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).Maintenance(ctx, adminoffline.MaintenanceRequest{}, out)
			},
		},
		{
			name: "audit outbox",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).AuditOutbox(ctx, adminoffline.AuditOutboxRequest{}, out)
			},
		},
		{
			name: "recovery ledger status",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).RecoveryLedgerStatus(ctx, out)
			},
		},
		{
			name: "backup",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).Backup(ctx, adminoffline.BackupRequest{Out: filepath.Join(t.TempDir(), "backup.tar.gz")}, out)
			},
		},
		{
			name: "restore",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).Restore(ctx, adminoffline.RestoreRequest{}, nil, out)
			},
		},
		{
			name: "bootstrap physical pool",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).BootstrapPhysicalPool(ctx, adminoffline.PhysicalPoolBootstrapRequest{}, out)
			},
		},
		{
			name: "bootstrap qualification pool",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).BootstrapQualificationLocalPhysicalPool(ctx, out)
			},
		},
		{
			name: "audit delivery roots",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).AuditDeliveryRoots(ctx, adminoffline.DeliveryAuditRequest{PhysicalPoolID: "pool"}, out)
			},
		},
		{
			name: "repair delivery root",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).RepairDeliveryRoot(ctx, adminoffline.DeliveryRepairRequest{}, out)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("LEAPVIEW_HOME", home)
			t.Setenv("LEAPVIEW_PRODUCTION", "1")
			t.Setenv("LEAPVIEW_ENVIRONMENT", "prod")
			// An invalid extension-supply path proves the guard runs before
			// extension loading as well as before SQLite/filesystem adapters.
			t.Setenv("LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH", filepath.Join(home, "missing-extension-supply"))

			err := test.call(ctx, io.Discard)
			if !errors.Is(err, ErrProductionUnavailable) {
				t.Fatalf("error = %v, want ErrProductionUnavailable", err)
			}
			entries, readErr := os.ReadDir(home)
			if readErr != nil {
				t.Fatalf("read home after rejected command: %v", readErr)
			}
			if len(entries) != 0 {
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				t.Fatalf("rejected production command created state: %v", names)
			}
		})
	}
}

func TestProductionGuardRunsBeforeExtensionSupplyLoading(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_PRODUCTION", "1")
	t.Setenv("LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH", filepath.Join(home, "missing-extension-supply"))

	_, err := newService()
	if !errors.Is(err, ErrProductionUnavailable) {
		t.Fatalf("newService error = %v, want ErrProductionUnavailable", err)
	}
}

func TestNonProductionServiceConstructorRemainsAvailable(t *testing.T) {
	t.Setenv("LEAPVIEW_HOME", t.TempDir())
	t.Setenv("LEAPVIEW_PRODUCTION", "0")
	t.Setenv("LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH", "")

	service, err := newService()
	if err != nil {
		t.Fatalf("newService non-production: %v", err)
	}
	if service == nil {
		t.Fatal("newService returned nil service")
	}

	// Keep a representative operation on the legacy path to demonstrate that
	// the guard is scoped to production rather than disabling offline Admin.
	var out bytes.Buffer
	if err := service.Maintenance(context.Background(), adminoffline.MaintenanceRequest{}, &out); err != nil {
		t.Fatalf("maintenance on non-production service: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("mode: dry-run")) {
		t.Fatalf("maintenance output = %q", out.String())
	}
}

func TestEvaluationModeRetainsIsolatedOfflineAuthority(t *testing.T) {
	t.Setenv("LEAPVIEW_HOME", t.TempDir())
	t.Setenv("LEAPVIEW_PRODUCTION", "1")
	t.Setenv("LEAPVIEW_EVALUATION_MODE", "true")
	t.Setenv("LEAPVIEW_ENVIRONMENT", "evaluation")
	t.Setenv("LEAPVIEW_LOCAL_AUTH", "true")
	t.Setenv("LEAPVIEW_PUBLIC_URL", "http://localhost:8080")
	t.Setenv("LEAPVIEW_COOKIE_SECURE", "false")
	t.Setenv("LEAPVIEW_TRUST_PROXY_HEADERS", "false")
	t.Setenv("LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH", "")

	service, err := newService()
	if err != nil {
		t.Fatalf("newService evaluation mode: %v", err)
	}
	if service == nil {
		t.Fatal("newService returned nil evaluation service")
	}
}
