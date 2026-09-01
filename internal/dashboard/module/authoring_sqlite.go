package module

import (
	"database/sql"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
)

// SQLiteAuthoringPersistence is the explicit local development/evaluation
// authoring authority. Its repository remains opaque outside the dashboard
// module so shared composition cannot substitute SQLite from a generic handle.
type SQLiteAuthoringPersistence struct {
	repository authoring.Repository
}

func NewSQLiteAuthoringPersistence(database *sql.DB, audit access.AuditIntentRecorder) (*SQLiteAuthoringPersistence, error) {
	if database == nil {
		return nil, fmt.Errorf("dashboard authoring SQLite database is required")
	}
	if audit == nil {
		return nil, fmt.Errorf("dashboard authoring SQLite audit intent recorder is required")
	}
	return &SQLiteAuthoringPersistence{repository: authoringsqlite.NewRepositoryWithAudit(database, audit)}, nil
}

func (p *SQLiteAuthoringPersistence) valid() bool {
	return p != nil && p.repository != nil
}
