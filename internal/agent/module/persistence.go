package module

import (
	"database/sql"

	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
)

func newRepository(database *sql.DB, workflow jobplatform.WorkflowRecorder) agent.Repository {
	return agentsqlite.NewRepositoryWithWorkflow(database, jobsqlite.NewRepository(database), workflow)
}
