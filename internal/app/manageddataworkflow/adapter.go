// Package manageddataworkflow bridges the managed-data PostgreSQL transition
// port to the jobs capability without making either storage package depend on
// the other.
package manageddataworkflow

import (
	"context"
	"errors"

	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/pkg/jobs"
)

// Adapter forwards workflow/event persistence through the caller-owned pgx
// transaction. It never opens, commits, or rolls back a transaction.
type Adapter struct {
	jobs *jobspostgres.Repository
}

var _ manageddatapostgres.WorkflowRecorder = (*Adapter)(nil)

func New(repository *jobspostgres.Repository) *Adapter {
	return &Adapter{jobs: repository}
}

func (a *Adapter) RecordWorkflow(ctx context.Context, tx manageddatapostgres.Tx, intent jobs.WorkflowIntent) error {
	if a == nil || a.jobs == nil {
		return errors.New("managed-data jobs workflow adapter is unavailable")
	}
	return a.jobs.RecordWorkflow(ctx, tx, intent)
}
