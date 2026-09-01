package module

import (
	"database/sql"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticssqlite "github.com/flidai/leapview/internal/analytics/sqlite"
)

// NewSQLiteConnectionBindings constructs the local development/evaluation
// connection-binding authority. Build consumes the resulting capability and
// never selects a database engine from a generic database handle.
func NewSQLiteConnectionBindings(database *sql.DB, audit access.AuditIntentRecorder) (connectionbinding.BindingCatalog, error) {
	if database == nil {
		return nil, errors.New("SQLite analytics database is required")
	}
	if audit != nil {
		return analyticssqlite.NewConnectionBindingRepositoryWithAudit(database, audit), nil
	}
	return analyticssqlite.NewConnectionBindingRepository(database), nil
}
