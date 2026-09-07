// Package releasejobs composes the Release-owned workflow port with the
// platform jobs PostgreSQL authority. The adapter deliberately forwards the
// caller-owned transaction unchanged so release state, event, audit, and
// follow-up job commit or roll back together.
package releasejobs

import (
	"context"
	"fmt"

	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/pkg/jobs"
)

type Adapter struct {
	jobs *jobspostgres.Repository
}

var _ releasepostgres.WorkflowAppender = (*Adapter)(nil)

func New(repository *jobspostgres.Repository) *Adapter {
	return &Adapter{jobs: repository}
}

func (a *Adapter) RecordWorkflow(ctx context.Context, tx releasepostgres.Tx, intent jobs.WorkflowIntent) error {
	if a == nil || a.jobs == nil {
		return fmt.Errorf("release workflow adapter is not configured")
	}
	return a.jobs.RecordWorkflow(ctx, tx, intent)
}
