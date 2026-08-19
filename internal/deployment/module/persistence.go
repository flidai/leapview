package module

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flidai/leapview/internal/deployment"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
)

type ActivationHooks struct {
}

// NewBootstrapPersistence constructs the durable bootstrap policy and project
// claim ports owned by the deployment module. Callers receive contracts only;
// the SQLite adapter never crosses the module boundary.
func NewBootstrapPersistence(database *sql.DB) (BootstrapPersistence, error) {
	if database == nil {
		return nil, errors.New("deployment database is required")
	}
	return deploymentsqlite.NewRepositoryWithHooks(database, deploymentsqlite.ActivationHooks{}), nil
}

func newPersistence(
	database *sql.DB,
	hooks ActivationHooks,
	releases ReleasePort,
	workflow jobplatform.WorkflowRecorder,
) (
	deployment.Repository,
	deployment.ActivationUnitOfWork,
	deployment.CandidateRepository,
	deployment.ApprovalRepository,
) {
	sqliteHooks := deploymentsqlite.ActivationHooks{}
	if releases != nil {
		sqliteHooks.LinkRelease = func(ctx context.Context, tx transaction.Transaction, input deployment.CreateInput) error {
			return releases.LinkDeploymentTx(ctx, tx, input.ServingIdentity.ProjectID.String(), input.ID, input.ReleaseID, input.RollbackOf)
		}
	}
	sqliteHooks.RecordWorkflow = workflow
	owned := deploymentsqlite.NewRepositoryWithHooks(database, sqliteHooks)
	return owned, owned, owned, owned
}
