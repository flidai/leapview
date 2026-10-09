package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RotateCredentials never modifies the operator's keyring. The stopped-instance
// lock and independently authenticated maintenance pool are both mandatory.
func (o Operations) RotateCredentials(ctx context.Context, request admincli.CredentialRotationRequest, out io.Writer) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if out == nil {
		return errors.New("credential rotation output is required")
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Production {
		if err = cfg.ValidatePostgresProduction(); err != nil {
			return err
		}
	} else if err = validateLocalAdminConfiguration(cfg); err != nil {
		return err
	}
	if cfg.CredentialKeyringFile == "" {
		return errors.New("LEAPVIEW_CREDENTIAL_KEYRING_FILE is required; rotation never creates or removes keys")
	}
	lock, err := deps.AcquireLock(cfg.HomeDir)
	if err != nil {
		return err
	}
	if typednil.IsNil(lock) {
		return errors.New("credential rotation requires the stopped-instance lock")
	}
	defer lock.Release()
	maintenance := cfg.PostgresControlMaintenanceConfig()
	if maintenance.URL == "" || maintenance.RuntimeRole != "leapview_control_maintenance" {
		return errors.New("credential rotation requires independent control maintenance credentials")
	}
	pool, err := deps.OpenMaintenance(ctx, maintenance)
	if err != nil {
		return err
	}
	if nilMaintenancePool(pool) {
		return errors.New("credential rotation requires the maintenance pool")
	}
	defer pool.Close()
	if err = deps.VerifyBaseline(ctx, pool); err != nil {
		return err
	}
	native, ok := pool.(interface{ NativePool() *pgxpool.Pool })
	if !ok || native.NativePool() == nil {
		return errors.New("credential rotation requires the native maintenance pool")
	}
	identity := platformbootstrap.New(native.NativePool())
	instance, err := identity.ExistingInstanceID(ctx)
	if err != nil {
		return err
	}
	if _, err = identity.CustomerOwner(ctx); err != nil {
		return err
	}
	keys, err := encryption.Load(cfg.CredentialKeyringFile)
	if err != nil {
		return err
	}
	if keys.DeploymentID() != instance {
		return errors.New("credential keyring does not match the durable installation")
	}
	repository, err := credentialpostgres.NewRotationRepository(ctx, native.NativePool())
	if err != nil {
		return err
	}
	progress, err := credential.RotateEnvelopes(ctx, repository, keys, request.BatchSize)
	if writeErr := json.NewEncoder(out).Encode(progress); writeErr != nil {
		return writeErr
	}
	return err
}
