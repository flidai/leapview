package app

import (
	"context"

	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sourceCredentialMutations interface {
	deploymentmodule.NativeDeliveryMutationPort
	deploymentmodule.NativeDeliveryCommandCompleter
}
type sourceCredentialEvidence interface {
	BindingEvidence(context.Context, string, string) ([]analyticsmodule.ActiveRuntimeBindingEvidence, error)
}

// sourceCredentialConfig contains only process-owned capabilities. No request
// selects a database, runtime, publication authority or local credential pin.
type sourceCredentialConfig struct {
	Pool        *pgxpool.Pool
	Credentials *credentialmodule.ActivationRepository
	TargetID    string
	Environment string
	Mutations   sourceCredentialMutations
	Reader      deploymentmodule.NativeDeliveryReader
	Delivery    *deploymentpostgres.Repository
	// PublicationRepository installs operation-specific admission while retaining
	// ordinary publication's audit, events and lineage verification.
	PublicationRepository  func(string, credentialmodule.ActivationCommitAuthorizer) (*deploymentpostgres.Repository, error)
	BeforeActivationCommit deploymentpostgres.ActivationPreCommitHook
	Authorize              func(context.Context, string, credentialmodule.ValidationResource) error
	AuthorizeTx            func(context.Context, pgx.Tx, string, credentialmodule.ValidationReceipt) error
	CurrentAuthorityTx     func(context.Context, pgx.Tx, credentialmodule.ValidationReceipt) error
	Evidence               sourceCredentialEvidence
	Install                func(context.Context, credentialmodule.ActivationRecord) error
	Restore                func(context.Context) error
}
type sourceCredentialActivation struct{ config sourceCredentialConfig }

func newSourceCredentialActivation(config sourceCredentialConfig) (*sourceCredentialActivation, error) {
	if config.Pool == nil || config.Credentials == nil || config.TargetID == "" || config.Environment == "" || config.Mutations == nil || config.Reader == nil || config.Delivery == nil || config.PublicationRepository == nil || config.BeforeActivationCommit == nil || config.Authorize == nil || config.AuthorizeTx == nil || config.CurrentAuthorityTx == nil || config.Evidence == nil || config.Install == nil || config.Restore == nil {
		return nil, credentialmodule.ErrValidationUnavailable
	}
	return &sourceCredentialActivation{config: config}, nil
}
func (a *sourceCredentialActivation) AuthorizeMutation(ctx context.Context, actor string, resource credentialmodule.ValidationResource) error {
	if a == nil || resource.Validate() != nil || resource.ScopeKind != "connection" || resource.TargetID != a.config.TargetID || resource.Environment != a.config.Environment {
		return credentialmodule.ErrInvalidValidation
	}
	return a.config.Authorize(ctx, actor, resource)
}
func (a *sourceCredentialActivation) transaction(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := a.config.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (a *sourceCredentialActivation) exact(ctx context.Context, actor string, resource credentialmodule.ValidationResource, id string) (credentialmodule.ActivationRequestRecord, error) {
	if err := a.AuthorizeMutation(ctx, actor, resource); err != nil {
		return credentialmodule.ActivationRequestRecord{}, err
	}
	row, err := a.config.Credentials.GetActivationRequest(ctx, a.config.TargetID, id)
	if err == nil && (row.Resource() != resource || row.Receipt.ActorID != actor) {
		return credentialmodule.ActivationRequestRecord{}, credentialmodule.ErrValidationNotFound
	}
	return row, err
}
func (a *sourceCredentialActivation) Read(ctx context.Context, actor string, resource credentialmodule.ValidationResource, id string) (credentialmodule.ActivationRecord, error) {
	row, err := a.exact(ctx, actor, resource, id)
	return row.ActivationRecord(), err
}
func (a *sourceCredentialActivation) Pending(ctx context.Context) (credentialmodule.ActivationRecord, error) {
	row, err := a.config.Credentials.GetPendingActivationRequest(ctx, a.config.TargetID)
	if err == nil && row.Resource().ScopeKind != "connection" {
		return credentialmodule.ActivationRecord{}, credentialmodule.ErrValidationNotFound
	}
	return row.ActivationRecord(), err
}
func (a *sourceCredentialActivation) auth(row credentialmodule.ActivationRequestRecord, actor string) credentialmodule.ActivationRequestAuthorizer {
	return func(ctx context.Context, tx pgx.Tx) error { return a.config.AuthorizeTx(ctx, tx, actor, row.Receipt) }
}
func (a *sourceCredentialActivation) InstallCommitted(ctx context.Context, record credentialmodule.ActivationRecord) error {
	row, err := a.config.Credentials.GetActivationRequest(ctx, a.config.TargetID, record.Request.OperationID)
	if err != nil {
		return err
	}
	if row.ActivationRecord() != record || (row.State != "committed" && row.State != "completed") {
		return credentialmodule.ErrValidationConflict
	}
	if err = a.transaction(ctx, func(tx pgx.Tx) error { return a.committed(ctx, tx, row) }); err != nil {
		return err
	}
	return a.config.Install(ctx, record)
}
func (a *sourceCredentialActivation) RestoreCurrent(ctx context.Context) error {
	return a.config.Restore(ctx)
}
func (a *sourceCredentialActivation) CheckCurrent(ctx context.Context, record credentialmodule.ActivationRecord) error {
	row, err := a.config.Credentials.GetActivationRequest(ctx, a.config.TargetID, record.Request.OperationID)
	if err != nil {
		return err
	}
	if row.ActivationRecord() != record || row.State != "completed" {
		return credentialmodule.ErrValidationConflict
	}
	return a.transaction(ctx, func(tx pgx.Tx) error { return a.committed(ctx, tx, row) })
}
func (a *sourceCredentialActivation) committed(ctx context.Context, tx pgx.Tx, row credentialmodule.ActivationRequestRecord) error {
	if err := a.config.CurrentAuthorityTx(ctx, tx, row.Receipt); err != nil {
		return err
	}
	target, err := a.config.Delivery.TargetForUpdateTx(ctx, tx, a.config.TargetID)
	if err != nil {
		return err
	}
	publication, err := a.config.Delivery.PublicationTx(ctx, tx, row.PublicationID)
	if err != nil {
		return err
	}
	if target.ActiveGenerationID != row.GenerationID || publication.State != "committed" || publication.GenerationID != row.GenerationID || publication.CandidateID != row.CandidateID || publication.TargetID != a.config.TargetID || target.TargetRevision != publication.ExpectedTargetRevision+1 {
		return credentialmodule.ErrValidationConflict
	}
	return nil
}
