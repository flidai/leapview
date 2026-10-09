package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

func setupPostgresOnboardingCustomerCredentials(t *testing.T, cfg *config.Config) {
	t.Helper()
	pool, err := platformpostgres.Open(t.Context(), cfg.PostgresControlPlaneConfig().Runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	instance, err := postgresauthority.ResolveInstanceIdentity(t.Context(), pool, cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyring, err := json.Marshal(map[string]any{"format": "credential-keyring-v1", "deployment_id": instance, "active_write_key_id": "onboarding-key", "keys": []map[string]string{{"key_id": "onboarding-key", "key_base64": base64.StdEncoding.EncodeToString(key), "state": "active_write"}}})
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CredentialKeyringFile = filepath.Join(cfg.HomeDir, "customer-keyring.json")
	if err = os.WriteFile(cfg.CredentialKeyringFile, keyring, 0600); err != nil {
		t.Fatal(err)
	}
	clear(keyring)
	operations := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return *cfg, nil }})
	if err = operations.SetupCredentials(t.Context(), admincli.CredentialSetupRequest{OwnerID: "onboarding-customer"}, io.Discard); err != nil {
		t.Fatal(err)
	}
}
