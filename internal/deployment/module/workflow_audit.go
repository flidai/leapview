package module

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flidai/leapview/internal/access"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/pkg/jobs"
)

// workflowAuditCommitter owns the deployment transaction that accepts an
// activation workflow and records its Access audit intent. The generic jobs
// module remains capability-neutral and participates through its transaction-
// bound WorkflowRecorder port.
type workflowAuditCommitter struct {
	database *sql.DB
	workflow jobplatform.WorkflowRecorder
	audit    access.AuditIntentRecorder
}

func (committer workflowAuditCommitter) CommitWorkflow(ctx context.Context, intent jobs.WorkflowIntent) error {
	return committer.commit(ctx, intent, nil)
}

func (committer workflowAuditCommitter) CommitWorkflowWithAudit(ctx context.Context, intent jobs.WorkflowIntent, audit access.AuditIntent) error {
	return committer.commit(ctx, intent, &audit)
}

func (committer workflowAuditCommitter) commit(ctx context.Context, intent jobs.WorkflowIntent, audit *access.AuditIntent) error {
	if committer.database == nil || committer.workflow == nil {
		return jobs.ErrStoreRequired
	}
	if audit != nil && committer.audit == nil {
		return errors.New("deployment audit intent recorder is required")
	}
	tx, err := committer.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := committer.workflow.RecordWorkflow(ctx, tx, intent); err != nil {
		return err
	}
	if audit != nil {
		if err := committer.audit.RecordAuditIntent(ctx, tx, *audit); err != nil {
			return err
		}
	}
	return tx.Commit()
}
