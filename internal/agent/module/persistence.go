package module

import (
	"database/sql"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
)

func newRepository(database *sql.DB, workflow jobplatform.WorkflowRecorder, audits ...access.AuditIntentRecorder) agent.Repository {
	var audit access.AuditIntentRecorder
	if len(audits) > 0 {
		audit = audits[0]
	}
	return agentsqlite.NewRepositoryWithWorkflowAndAudit(database, jobsqlite.NewRepository(database), workflow, audit)
}
