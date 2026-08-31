// Package deploymentworkflow composes deployment's transaction-bound workflow
// port with the platform jobs PostgreSQL authority.
package deploymentworkflow

import (
	"context"
	"errors"
	"fmt"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/pkg/jobs"
)

// Adapter is stateless and safe to share between deployment requests.
type Adapter struct {
	jobs     *jobspostgres.Repository
	delivery *deploymentpostgres.Repository
}

var _ deploymentmodule.NativeDeliveryWorkflowRecorder = (*Adapter)(nil)

// New returns an adapter backed by the supplied jobs authority.
func New(repository *jobspostgres.Repository) *Adapter {
	return &Adapter{jobs: repository}
}

// NewWithRepository wires the delivery authority needed to resolve the
// immutable publication actor while enqueuing approval activation. The lookup
// runs on the caller-owned transaction, preserving one commit boundary.
func NewWithRepository(delivery *deploymentpostgres.Repository, jobs *jobspostgres.Repository) *Adapter {
	return &Adapter{delivery: delivery, jobs: jobs}
}

// RecordWorkflow forwards the caller-owned transaction unchanged. Neither
// this adapter nor the jobs repository opens, commits, or rolls back tx.
func (a *Adapter) RecordWorkflow(ctx context.Context, tx deploymentpostgres.Tx, intent jobs.WorkflowIntent) error {
	if a == nil || a.jobs == nil {
		return fmt.Errorf("%w: deployment workflow adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return fmt.Errorf("%w: deployment workflow transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := a.jobs.RecordWorkflow(ctx, tx, intent); err != nil {
		if errors.Is(err, jobs.ErrConflict) {
			return fmt.Errorf("%w: deployment workflow identity differs", deploymentpostgres.ErrConflict)
		}
		return err
	}
	return nil
}
