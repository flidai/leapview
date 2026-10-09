package adminpostgres

import (
	"context"
	"errors"
	"io"
	"testing"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/config"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

func TestCredentialRotationRequiresStoppedInstanceLockBeforeDatabaseAccess(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "missing lock"}[unavailable], func(t *testing.T) {
			cfg := validProductionMaintenanceConfig()
			cfg.HomeDir = t.TempDir()
			cfg.CredentialKeyringFile = "/private/keyring.json"
			locked := errors.New("instance is running")
			operations := New(Dependencies{
				LoadConfig: func() (config.Config, error) { return cfg, nil },
				AcquireLock: func(home string) (adminoffline.Lock, error) {
					if home != cfg.HomeDir {
						t.Fatal("rotation used another instance home")
					}
					if unavailable {
						return nil, nil
					}
					return nil, locked
				},
				OpenMaintenance: func(context.Context, platformpostgres.Config) (MaintenancePool, error) {
					t.Fatal("opened database before acquiring stopped-instance lock")
					return nil, nil
				},
			})
			err := operations.RotateCredentials(t.Context(), admincli.CredentialRotationRequest{BatchSize: 1}, io.Discard)
			if err == nil || (!unavailable && !errors.Is(err, locked)) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
