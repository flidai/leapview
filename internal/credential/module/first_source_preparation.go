package module

import (
	"context"

	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FirstSourcePreparationIntent = credential.FirstSourcePreparationIntent
type FirstSourcePreparation = credential.FirstSourcePreparation
type FirstSourcePlanLink = credential.FirstSourcePlanLink
type FirstSourceStoredPreparation = credential.FirstSourceStoredPreparation
type FirstSourceStoredPlanLink = credential.FirstSourceStoredPlanLink
type FirstSourcePreparationAuthorizer func(context.Context, pgx.Tx, FirstSourcePreparationIntent) error
type FirstSourcePlanAuthorizer func(context.Context, pgx.Tx, FirstSourcePreparationIntent, FirstSourcePlanLink) error

// The read port makes the distinction between active preparation and stored
// historical identity explicit. Neither branch grants runtime authority.
type FirstSourcePreparationReader interface {
	PreparationTx(context.Context, pgx.Tx, string, string) (FirstSourcePreparation, error)
	PlanLinkTx(context.Context, pgx.Tx, string, string) (FirstSourcePlanLink, error)
	StoredPreparationTx(context.Context, pgx.Tx, string, string) (FirstSourceStoredPreparation, error)
	StoredPlanLinkTx(context.Context, pgx.Tx, string, string) (FirstSourceStoredPlanLink, error)
}

// Mutations remain transaction-bound and require an explicit live authorizer.
// The read methods retain the active/historical distinction of the read port.
type FirstSourcePreparationMutations interface {
	FirstSourcePreparationReader
	Preparation(context.Context, string, string) (FirstSourcePreparation, error)
	PrepareTx(context.Context, pgx.Tx, FirstSourcePreparationIntent, FirstSourcePreparationAuthorizer) (FirstSourcePreparation, error)
	RenewReceiptTx(context.Context, pgx.Tx, string, string, string, FirstSourcePreparationAuthorizer) (ValidationReceipt, error)
	LinkPlanTx(context.Context, pgx.Tx, FirstSourcePlanLink, FirstSourcePlanAuthorizer) error
}

type firstSourcePreparationMutations struct {
	FirstSourcePreparationReader
	journal *credentialpostgres.FirstSourcePreparations
}

func NewFirstSourcePreparationMutations(pool *pgxpool.Pool, audit AuditRecorder) (FirstSourcePreparationMutations, error) {
	if pool == nil || audit == nil {
		return nil, credential.ErrUnavailable
	}
	journal, err := credentialpostgres.NewFirstSourcePreparations(pool, credentialAuditAdapter{record: audit})
	if err != nil {
		return nil, err
	}
	return firstSourcePreparationMutations{FirstSourcePreparationReader: journal, journal: journal}, nil
}

func (m firstSourcePreparationMutations) Preparation(ctx context.Context, target, id string) (FirstSourcePreparation, error) {
	return m.journal.Preparation(ctx, target, id)
}

func (m firstSourcePreparationMutations) PrepareTx(ctx context.Context, tx pgx.Tx, intent FirstSourcePreparationIntent, authorize FirstSourcePreparationAuthorizer) (FirstSourcePreparation, error) {
	return m.journal.PrepareTx(ctx, tx, intent, credentialpostgres.FirstSourcePreparationAuthorizer(authorize))
}

func (m firstSourcePreparationMutations) RenewReceiptTx(ctx context.Context, tx pgx.Tx, target, id, receipt string, authorize FirstSourcePreparationAuthorizer) (ValidationReceipt, error) {
	return m.journal.RenewReceiptTx(ctx, tx, target, id, receipt, credentialpostgres.FirstSourcePreparationAuthorizer(authorize))
}

func (m firstSourcePreparationMutations) LinkPlanTx(ctx context.Context, tx pgx.Tx, link FirstSourcePlanLink, authorize FirstSourcePlanAuthorizer) error {
	return m.journal.LinkPlanTx(ctx, tx, link, credentialpostgres.FirstSourcePlanAuthorizer(authorize))
}
