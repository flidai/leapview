package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/credential/encryption"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SetupCredentials is an offline installation-operator boundary. The same
// instance lock as serve protects setup; ownership is never inferred from the
// operator, administrator, Project, or instance identity.
func (o Operations) SetupCredentials(ctx context.Context, request admincli.CredentialSetupRequest, out io.Writer) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if out == nil {
		return errors.New("customer credential setup output is required")
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.CredentialKeyringFile == "" {
		return errors.New("LEAPVIEW_CREDENTIAL_KEYRING_FILE is required; setup does not generate encryption keys")
	}
	configuration, err := accessConfigForAdmin(cfg)
	if err != nil {
		return err
	}
	lock, err := deps.AcquireLock(cfg.HomeDir)
	if err != nil {
		return err
	}
	if typednil.IsNil(lock) {
		return errors.New("customer credential setup requires the instance lock")
	}
	defer lock.Release()
	pool, err := deps.OpenAccess(ctx, configuration)
	if err != nil {
		return err
	}
	if nilAccessPool(pool) {
		return errors.New("customer credential setup requires the control pool")
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return err
	}
	fingerprintKey, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	initializer, err := deps.NewAccess(pool, fingerprintKey)
	if err != nil {
		return err
	}
	if nilAccessInitializer(initializer) {
		return errors.New("customer credential setup requires initialized access authority")
	}
	initialized, err := initializer.Initialized(ctx)
	if err != nil {
		return err
	}
	if !initialized {
		return errors.New("run admin initialize before customer credential setup")
	}
	instanceID, err := platformbootstrap.New(pool).ExistingInstanceID(ctx)
	if err != nil {
		return fmt.Errorf("read existing instance identity before credential setup: %w", err)
	}
	keys, err := encryption.Load(cfg.CredentialKeyringFile)
	if err != nil {
		return fmt.Errorf("load customer credential keyring: %w", err)
	}
	if keys.DeploymentID() != instanceID {
		return errors.New("customer credential keyring deployment does not match the durable instance identity")
	}
	audit := accesspostgres.New()
	if err := declareCustomerOwner(ctx, pool, instanceID, request.OwnerID, func(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
		_, err := audit.RecordAuditEvent(ctx, tx, intent)
		return err
	}); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		InstanceID string `json:"instanceId"`
		OwnerID    string `json:"customerOwnerId"`
	}{instanceID, request.OwnerID})
}

type customerOwnerTransactionStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}
type customerOwnerAudit func(context.Context, pgx.Tx, access.AuditIntent) error

func declareCustomerOwner(ctx context.Context, pool customerOwnerTransactionStarter, instanceID, ownerID string, audit customerOwnerAudit) error {
	if audit == nil {
		return errors.New("customer ownership requires atomic audit")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	inserted, err := platformbootstrap.New(tx).DeclareCustomerOwner(ctx, ownerID)
	if err != nil {
		return err
	}
	if inserted {
		metadata, err := json.Marshal(struct {
			Surface string `json:"surface"`
			OwnerID string `json:"customer_owner_id"`
		}{"offline_operator", ownerID})
		if err != nil {
			return err
		}
		intent := access.AuditIntent{
			EventID: uuid.NewString(), ScopeID: instanceID, ActorID: "offline_operator",
			Source: "platform", Operation: "setupCustomerCredentials", Action: "credential.owner.declared",
			ResourceKind: "instance", ResourceID: instanceID, Outcome: "success",
			AggregateKey: "credential-owner:" + instanceID, AggregateSequence: 1, MetadataJSON: string(metadata),
		}
		if err := audit(ctx, tx, intent); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
