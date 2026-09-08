package module

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/pkg/jobs"
)

// AuditedWorkflowCommitter is the topology-neutral workflow port for callers
// that own an audit transaction. Native deployment mutation paths use the
// PostgreSQL consequence authorities directly.
type AuditedWorkflowCommitter interface {
	jobs.WorkflowCommitter
	CommitWorkflowWithAudit(context.Context, jobs.WorkflowIntent, access.AuditIntent) error
}
