package adminpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	bindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deliverypostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// AdmitFirstSource is an offline installation-operator boundary. The stopped
// instance lock and native unpublished-target fence protect one exact initial
// binding and staged operator grant. It publishes no credentials or authority.
func (o Operations) AdmitFirstSource(ctx context.Context, request admincli.FirstSourceAdmissionRequest, out io.Writer) error {
	if err := request.Intent.Validate(); err != nil {
		return err
	}
	if _, err := firstSourceBinding(request.Intent, time.Unix(1, 0).UTC()); err != nil {
		return err
	}
	if out == nil {
		return errors.New("first-source admission output is required")
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production {
		return errors.New("offline first-source admission requires production")
	}
	if cfg.CredentialKeyringFile == "" {
		return errors.New("first-source admission requires configured customer credential keys")
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
		return errors.New("first-source admission requires the instance lock")
	}
	defer lock.Release()
	pool, err := deps.OpenAccess(ctx, configuration)
	if err != nil {
		return err
	}
	if nilAccessPool(pool) {
		return errors.New("first-source admission requires the control pool")
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return err
	}
	key, err := accessFingerprintKey(cfg)
	if err != nil {
		return err
	}
	initializer, err := deps.NewAccess(pool, key)
	if err != nil {
		return err
	}
	if nilAccessInitializer(initializer) {
		return errors.New("initialized access authority is unavailable")
	}
	initialized, err := initializer.Initialized(ctx)
	if err != nil {
		return err
	}
	if !initialized {
		return errors.New("run admin initialize before first-source admission")
	}
	instanceID, err := platformbootstrap.New(pool).ExistingInstanceID(ctx)
	if err != nil {
		return err
	}
	keys, err := encryption.Load(cfg.CredentialKeyringFile)
	if err != nil {
		return errors.New("first-source admission requires a valid private customer keyring")
	}
	if keys.DeploymentID() != instanceID || request.Intent.TargetID != instanceID || request.Intent.Environment != cfg.Environment {
		return credential.ErrForbidden
	}
	var admitted credential.FirstSourceAdmission
	err = deliverypostgres.New(pool).WithUnpublishedTarget(ctx, instanceID, request.Intent.ProjectID, cfg.Environment, func(ctx context.Context) error {
		var err error
		admitted, err = stageFirstSourceAdmission(ctx, pool, key, request, deps.Now())
		return err
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(firstSourceAdmissionReceipt{
		OperationID: admitted.Intent.OperationID, TargetID: admitted.Intent.TargetID,
		ProjectID: admitted.Intent.ProjectID, Environment: admitted.Intent.Environment,
		OperatorPrincipalID: admitted.Intent.OperatorPrincipalID, BindingID: admitted.Intent.BindingID,
		BindingRevision: 1, IntentDigest: admitted.IntentDigest, BindingDigest: admitted.BindingDigest,
		PolicyRevision: admitted.PolicyRevision, PolicyDigest: admitted.PolicyDigest,
		Applied: request.Apply, RequiresPublication: true,
	})
}

type firstSourceAdmissionReceipt struct {
	OperationID         string `json:"operationId"`
	TargetID            string `json:"targetId"`
	ProjectID           string `json:"projectId"`
	Environment         string `json:"environment"`
	OperatorPrincipalID string `json:"operatorPrincipalId"`
	BindingID           string `json:"bindingId"`
	BindingRevision     int64  `json:"bindingRevision"`
	IntentDigest        string `json:"intentDigest"`
	BindingDigest       string `json:"bindingDigest"`
	PolicyRevision      int64  `json:"policyRevision"`
	PolicyDigest        string `json:"policyDigest"`
	Applied             bool   `json:"applied"`
	RequiresPublication bool   `json:"requiresPublication"`
}

type firstSourceAudit struct {
	repository *accesspostgres.AuditRepository
}

func (a firstSourceAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	_, err := a.repository.RecordAuditEvent(ctx, tx, intent)
	return err
}

// Caller holds the native unpublished-target fence across this transaction.
// Every business write, both audits and the durable authority row commit once.
func stageFirstSourceAdmission(ctx context.Context, pool AccessPool, key []byte, request admincli.FirstSourceAdmissionRequest, now time.Time) (credential.FirstSourceAdmission, error) {
	i := request.Intent
	tx, err := pool.Begin(ctx)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	accessRepo, err := accesspostgres.NewAccess(tx, accesspostgres.FingerprintConfig{Key: key})
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	audit := firstSourceAudit{repository: accesspostgres.New()}
	bindings, err := bindingpostgres.NewProduction(pool, audit)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	bindings = bindings.WithTx(tx)
	admissions, err := credentialpostgres.NewFirstSourceAdmissions(pool, audit)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	if err := validateFirstSourceInstallation(ctx, tx, accessRepo, i); err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	scope := access.AuthorizationPolicyScope{TargetID: i.TargetID, ProjectID: i.ProjectID, Environment: i.Environment}
	grant, err := i.Grant()
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	digest, err := i.Digest()
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	bindingDigest, err := i.BindingDigest()
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	stored, err := admissions.AdmissionTx(ctx, tx, i.TargetID, i.OperationID)
	if err == nil {
		if stored.IntentDigest != digest || stored.BindingDigest != bindingDigest {
			return credential.FirstSourceAdmission{}, credential.ErrConflict
		}
		if err := validateFirstSourceStagedAuthority(ctx, tx, accessRepo, bindings, stored); err != nil {
			return credential.FirstSourceAdmission{}, err
		}
		return stored, nil
	}
	if !errors.Is(err, credential.ErrNotFound) {
		return credential.FirstSourceAdmission{}, err
	}
	policy, err := accesspostgres.ValidateAuthorizationPolicyRevisionTx(ctx, tx, scope, i.ExpectedPolicyRevision, i.ExpectedPolicyDigest)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	for _, existing := range policy.Grants {
		if existing.ID == grant.ID {
			return credential.FirstSourceAdmission{}, credential.ErrConflict
		}
	}
	binding, err := firstSourceBinding(i, now)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	_, err = bindings.Binding(ctx, binding.Scope, binding.TargetID, binding.ConnectionID)
	if err == nil {
		return credential.FirstSourceAdmission{}, credential.ErrConflict
	}
	if !errors.Is(err, connectionbinding.ErrBindingNotFound) {
		return credential.FirstSourceAdmission{}, err
	}
	plannedDigest, err := access.AuthorizationPolicyDigest(scope, policy.RoleBindings, append(append([]access.AuthorizationGrant(nil), policy.Grants...), grant)...)
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	admitted := credential.FirstSourceAdmission{Intent: i, IntentDigest: digest, BindingDigest: bindingDigest,
		PolicyRevision: policy.Revision + 1, PolicyDigest: plannedDigest, CreatedAt: now.UTC()}
	if !request.Apply {
		return admitted, nil
	}
	intent, err := connectionbinding.BuildAdministrationAuditIntent(connectionbinding.AdministrationAuditInvocation{}, connectionbinding.AdministrationAuditEvent{
		ProjectID: binding.Scope.ProjectID, BindingID: binding.ID, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID,
		Action: connectionbinding.AuditBindingCreated, Outcome: connectionbinding.AdministrationAuditSucceeded, Revision: 1,
	})
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	intent.ScopeID, intent.ActorID, intent.PrincipalID = i.TargetID, "offline_operator", ""
	if err := bindings.CreateTx(connectionbinding.WithAuditIntent(ctx, intent), tx, binding); err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	policy, err = accessRepo.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
		Scope: scope, Grant: grant, ExpectedRevision: i.ExpectedPolicyRevision, IdempotencyKey: "first-source-" + i.OperationID,
	})
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	if policy.Revision != admitted.PolicyRevision || policy.Digest != admitted.PolicyDigest {
		return credential.FirstSourceAdmission{}, credential.ErrConflict
	}
	admitted, err = admissions.InsertAdmissionTx(ctx, tx, admitted, func(ctx context.Context, tx pgx.Tx, record credential.FirstSourceAdmission) error {
		if err := validateFirstSourceInstallation(ctx, tx, accessRepo, i); err != nil {
			return err
		}
		return validateFirstSourceStagedAuthority(ctx, tx, accessRepo, bindings, record)
	})
	if err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return credential.FirstSourceAdmission{}, err
	}
	return admitted, nil
}

func validateFirstSourceInstallation(ctx context.Context, tx pgx.Tx, repo *accesspostgres.Repository, i credential.FirstSourceAdmissionIntent) error {
	bootstrap := platformbootstrap.New(tx)
	instance, err := bootstrap.ExistingInstanceID(ctx)
	if err != nil {
		return err
	}
	environment, err := bootstrap.InstanceEnvironment(ctx)
	if err != nil {
		return err
	}
	owner, err := bootstrap.CustomerOwner(ctx)
	if err != nil {
		return err
	}
	claim, err := bootstrap.GetProjectClaim(ctx)
	if err != nil {
		return err
	}
	if instance != i.TargetID || owner != i.CustomerOwnerID || validateGrantClaim(claim, i.ProjectID, environment, i.Environment) != nil {
		return credential.ErrForbidden
	}
	principal, err := repo.PrincipalByIDForUpdate(ctx, i.OperatorPrincipalID)
	if err != nil {
		return err
	}
	if principal.Kind != access.PrincipalKindUser || principal.AccessDisabled() {
		return credential.ErrForbidden
	}
	return nil
}

func validateFirstSourceStagedAuthority(ctx context.Context, tx pgx.Tx, repo *accesspostgres.Repository, bindings *bindingpostgres.Repository, record credential.FirstSourceAdmission) error {
	i := record.Intent
	scope := access.AuthorizationPolicyScope{TargetID: i.TargetID, ProjectID: i.ProjectID, Environment: i.Environment}
	current, err := repo.AuthorizationPolicy(ctx, scope)
	if err != nil {
		return err
	}
	current, err = accesspostgres.ValidateAuthorizationPolicyRevisionTx(ctx, tx, scope, current.Revision, current.Digest)
	if err != nil {
		return err
	}
	grant, err := i.Grant()
	if err != nil {
		return err
	}
	found := false
	for _, existing := range current.Grants {
		if existing.ID == grant.ID && reflect.DeepEqual(existing, grant) {
			found = true
			break
		}
	}
	if !found {
		return credential.ErrForbidden
	}
	expected, err := firstSourceBinding(i, record.CreatedAt)
	if err != nil {
		return err
	}
	currentBinding, err := bindings.Binding(ctx, expected.Scope, expected.TargetID, expected.ConnectionID)
	if err != nil {
		return err
	}
	if currentBinding.ID != expected.ID || currentBinding.Revision != 1 || !currentBinding.Enabled ||
		currentBinding.ConnectorKind != expected.ConnectorKind || currentBinding.AuthenticationMode != expected.AuthenticationMode ||
		currentBinding.CredentialReference != expected.CredentialReference || !reflect.DeepEqual(currentBinding.Endpoint, expected.Endpoint) {
		return credential.ErrForbidden
	}
	return nil
}

// Conversion and validation remain at composition, retaining the complete
// existing binding policy without widening credential's capability edges.
func firstSourceBinding(i credential.FirstSourceAdmissionIntent, now time.Time) (connectionbinding.TargetBinding, error) {
	return connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{
		ID: connectionbinding.BindingID(i.BindingID), TargetID: connectionbinding.TargetID(i.TargetID),
		ConnectionID: projectgraph.ResourceID(i.ConnectionID), ConnectorKind: "postgres",
		AuthenticationMode: connectionbinding.AuthenticationExternalBundle,
		Scope:              connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(i.ProjectID), Environment: i.Environment},
		Endpoint:           connectionbinding.EndpointConfig(i.Endpoint), CredentialReference: connectionbinding.CredentialReference(i.CredentialReference), Enabled: true, Now: now,
	})
}
