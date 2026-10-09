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

// The read port makes the distinction between active preparation and stored
// historical identity explicit. Neither branch grants runtime authority.
type FirstSourcePreparationReader interface {
	PreparationTx(context.Context, pgx.Tx, string, string) (FirstSourcePreparation, error)
	PlanLinkTx(context.Context, pgx.Tx, string, string) (FirstSourcePlanLink, error)
	StoredPreparationTx(context.Context, pgx.Tx, string, string) (FirstSourceStoredPreparation, error)
	StoredPlanLinkTx(context.Context, pgx.Tx, string, string) (FirstSourceStoredPlanLink, error)
}

func NewFirstSourcePreparationReader(pool *pgxpool.Pool, audit AuditRecorder) (FirstSourcePreparationReader, error) {
	if pool == nil || audit == nil {
		return nil, credential.ErrUnavailable
	}
	return credentialpostgres.NewFirstSourcePreparations(pool, credentialAuditAdapter{record: audit})
}
