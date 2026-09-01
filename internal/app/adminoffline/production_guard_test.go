package adminoffline

import (
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
			name: "bootstrap physical pool",
			call: func(ctx context.Context, out io.Writer) error {
				return (Operations{}).BootstrapPhysicalPool(ctx, adminoffline.PhysicalPoolBootstrapRequest{}, out)
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
