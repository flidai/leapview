package app

import (
	"database/sql"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
)

// auditRuntime is the app-owned durable audit composition for one platform
// database. The underlying store is constructed exactly once; consumers are
// handed only the narrow facet they require.
type auditRuntime struct {
	recorder access.AuditIntentRecorder
	delivery access.AuditOutboxDeliveryStore
	stats    access.AuditOutboxStatsReader
	operator access.AuditOutboxOperator
}

func newAuditRuntime(database *sql.DB) (*auditRuntime, error) {
	if database == nil {
		return nil, fmt.Errorf("audit runtime database is required")
	}
	store := accessmodule.NewAuditStore(database)
	if store == nil {
		return nil, fmt.Errorf("audit runtime store is required")
	}
	return &auditRuntime{
		recorder: store, delivery: store, stats: store, operator: store,
	}, nil
}
