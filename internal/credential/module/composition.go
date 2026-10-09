package module

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditRecorder is wired to the shared Access audit repository. The caller
// passes the transaction created by the credential repository, preserving
// the draft/audit atomicity boundary.
type AuditRecorder func(context.Context, pgx.Tx, access.AuditIntent) error

// CredentialAuthorityRechecker refreshes the authenticated request credential
// before a validation receipt is committed. The connection permission callback
// separately resolves current principal/group grants and applies the request's
// permission ceiling.
type CredentialAuthorityRechecker func(context.Context, string, access.PermissionPair) error

// Services are the public operations composed over one encrypted credential
// repository. Validation is available only when the server-owned probe and
// live caller-authority rechecker are both configured.
type Services struct {
	Drafts     *credential.Service
	Validation *credential.ValidationService
	Runtime    *credential.RuntimeCredentials
	repository *credentialpostgres.Repository
}

// Config contains process-owned dependencies. Request data cannot select the
// target, environment, customer owner, binding, keyring or authorization
// authority.
type Config struct {
	Pool                *pgxpool.Pool
	Audit               AuditRecorder
	KeyringPath         string
	InstanceID          string
	Environment         string
	CustomerOwner       CustomerOwnerReader
	Bindings            TargetConnectionBindingReader
	CurrentProject      func(context.Context) (projectgraph.ResourceID, error)
	AuthorizeConnection func(context.Context, string, string, string, access.Action) (bool, error)
	ValidationProbe     credential.ValidationProbe
	RecheckCredential   CredentialAuthorityRechecker
}

// Build composes the encrypted draft service. Credentials remain an optional
// installation feature until setup is selected; partial setup is rejected.
func Build(ctx context.Context, config Config) (*Services, error) {
	if ctx == nil || typednil.IsNil(config.CustomerOwner) {
		return nil, errors.New("customer credential setup authority is unavailable")
	}
	keys, configured, err := configuredKeyring(ctx, config.CustomerOwner, config.InstanceID, config.KeyringPath)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}
	if config.Pool == nil || config.Audit == nil || typednil.IsNil(config.Bindings) ||
		config.CurrentProject == nil || config.AuthorizeConnection == nil ||
		typednil.IsNil(config.ValidationProbe) || config.RecheckCredential == nil ||
		!canonicalCredentialValue(config.InstanceID) || !canonicalCredentialValue(config.Environment) {
		return nil, credential.ErrUnavailable
	}
	repository, err := credentialpostgres.New(config.Pool, credentialAuditAdapter{record: config.Audit})
	if err != nil {
		return nil, fmt.Errorf("build customer credential repository: %w", err)
	}
	if err := repository.CheckKeyring(ctx, keys); err != nil {
		return nil, fmt.Errorf("verify customer credential key coverage: %w", err)
	}
	scopes := connectionCredentialScopeResolver{
		instanceID: config.InstanceID, environment: config.Environment,
		ownerReader: config.CustomerOwner, currentProject: config.CurrentProject,
		bindings: config.Bindings,
	}
	draftAuthorizer := connectionCredentialAuthorizer{authorize: config.AuthorizeConnection}
	drafts, err := credential.NewService(repository, keys, scopes, draftAuthorizer)
	if err != nil {
		return nil, fmt.Errorf("build customer credential service: %w", err)
	}
	validationAuthorizer := connectionCredentialAuthorizer{
		authorize: config.AuthorizeConnection, recheck: config.RecheckCredential,
	}
	validation, err := credential.NewValidationService(repository, keys, scopes, validationAuthorizer, config.ValidationProbe, time.Now)
	if err != nil {
		return nil, fmt.Errorf("build customer credential validation service: %w", err)
	}
	runtime, err := credential.NewRuntimeCredentials(repository, keys, scopes)
	if err != nil {
		return nil, err
	}
	return &Services{
		Drafts: drafts, Validation: validation, Runtime: runtime, repository: repository,
	}, nil
}

type credentialAuditAdapter struct{ record AuditRecorder }

func (adapter credentialAuditAdapter) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	if adapter.record == nil {
		return credential.ErrUnavailable
	}
	return adapter.record(ctx, tx, intent)
}

type connectionCredentialAuthorizer struct {
	authorize func(context.Context, string, string, string, access.Action) (bool, error)
	recheck   CredentialAuthorityRechecker
}

func (authorizer connectionCredentialAuthorizer) RequirePermission(ctx context.Context, actorID string, pair access.PermissionPair) error {
	if ctx == nil || !canonicalCredentialValue(actorID) || authorizer.authorize == nil || pair.Validate() != nil ||
		pair.Profile != access.PermissionCatalogProfile || pair.Target.Scope != access.PermissionScopeResource ||
		pair.Target.InstanceID != "" || !pair.Target.ProjectID.Valid() || pair.Target.ResourceKind != projectgraph.KindConnection ||
		!pair.Target.ResourceID.Valid() || pair.Target.IncludeFuture {
		return credential.ErrForbidden
	}
	if pair.Action != access.ActionConnectionRead && pair.Action != access.ActionConnectionManage && pair.Action != access.ActionConnectionUse {
		return credential.ErrForbidden
	}
	if authorizer.recheck != nil {
		if err := authorizer.recheck(ctx, actorID, pair); err != nil {
			return err
		}
	}
	allowed, err := authorizer.authorize(ctx, actorID, pair.Target.ProjectID.String(), pair.Target.ResourceID.String(), pair.Action)
	if err != nil {
		return err
	}
	if !allowed {
		return credential.ErrForbidden
	}
	return nil
}
