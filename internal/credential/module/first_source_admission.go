package module

import (
	"context"

	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FirstSourceAdmission = credential.FirstSourceAdmission
type FirstSourceAdmissionIntent = credential.FirstSourceAdmissionIntent
type FirstSourceEndpoint = credential.FirstSourceEndpoint
type FirstSourceCredentialReference = credential.FirstSourceCredentialReference

// FirstSourceAdmissionReader returns installation intent, never a credential
// version or runtime authority. Composition must recheck current authority.
type FirstSourceAdmissionReader interface {
	AdmissionForTarget(context.Context, string) (FirstSourceAdmission, error)
	AdmissionForTargetTx(context.Context, pgx.Tx, string) (FirstSourceAdmission, error)
}

func NewFirstSourceAdmissionReader(pool *pgxpool.Pool, audit AuditRecorder) (FirstSourceAdmissionReader, error) {
	if pool == nil || audit == nil {
		return nil, credential.ErrUnavailable
	}
	return credentialpostgres.NewFirstSourceAdmissions(pool, credentialAuditAdapter{record: audit})
}
