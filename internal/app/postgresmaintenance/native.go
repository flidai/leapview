package postgresmaintenance

import (
	"context"
	"errors"
	"fmt"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	cursorsigningpostgres "github.com/flidai/leapview/internal/platform/http/cursorsigning/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/jackc/pgx/v5"
)

// NativeDB is the exact pgx surface required to assemble every native
// retention authority. Production supplies the separately authenticated,
// one-connection maintenance pool; Preview supplies one outer transaction.
type NativeDB interface {
	operationpostgres.MaintenanceDBTX
	Begin(context.Context) (pgx.Tx, error)
}

// Native owns the canonical PostgreSQL retention composition. Keeping the
// database handle private prevents callers from borrowing maintenance
// credentials for arbitrary SQL.
type Native struct {
	db          NativeDB
	coordinator *Coordinator
}

// NewNative constructs all destructive facades over one exact maintenance
// authority. It performs no database I/O.
func NewNative(db NativeDB) (*Native, error) {
	if nilAuthority(db) {
		return nil, errors.New("PostgreSQL native maintenance database is required")
	}
	eventTransactions, err := NewPgxEventTxRunner(db.Begin)
	if err != nil {
		return nil, err
	}
	coordinator, err := New(Options{
		Operations:        operationpostgres.NewMaintenance(db),
		CursorSigning:     cursorsigningpostgres.NewMaintenance(db),
		Jobs:              jobspostgres.NewMaintenance(db),
		Events:            eventspostgres.New(),
		EventTransactions: eventTransactions,
		DashboardSession:  dashboardsessionpostgres.NewMaintenance(db),
		DashboardUsage:    dashboardusagepostgres.NewMaintenance(db),
		DashboardStreams:  dashboardpublicationpostgres.NewMaintenance(db),
		ManagedData:       manageddatapostgres.NewMaintenance(db),
		AccessAudit:       accesspostgres.NewMaintenance(db),
		AccessAuthState:   accesspostgres.NewMaintenance(db),
		QueryAudit:        queryauditpostgres.NewMaintenance(db),
		AgentHistory:      agentpostgres.NewMaintenance(db),
	})
	if err != nil {
		return nil, err
	}
	return &Native{db: db, coordinator: coordinator}, nil
}

// Run applies one bounded batch per capability.
func (n *Native) Run(ctx context.Context, policy Policy) (Result, error) {
	if n == nil || n.coordinator == nil {
		return Result{}, errors.New("PostgreSQL native maintenance is unavailable")
	}
	return n.coordinator.Run(ctx, policy)
}

// Preview executes the exact same owner functions inside one outer control-
// database transaction and always rolls it back. Capability methods that use
// nested pgx transactions create savepoints, so their reported counts and
// floors are real while no retention mutation commits.
func (n *Native) Preview(ctx context.Context, policy Policy) (Result, error) {
	if n == nil || nilAuthority(n.db) {
		return Result{}, errors.New("PostgreSQL native maintenance is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin PostgreSQL maintenance preview: %w", err)
	}
	if nilAuthority(tx) {
		return Result{}, errors.New("PostgreSQL maintenance preview returned nil transaction")
	}
	preview, err := NewNative(tx)
	if err != nil {
		_ = tx.Rollback(context.Background())
		return Result{}, err
	}
	result, runErr := preview.Run(ctx, policy)
	rollbackErr := tx.Rollback(context.Background())
	if runErr != nil {
		return result, errors.Join(runErr, rollbackErr)
	}
	if rollbackErr != nil {
		return result, fmt.Errorf("rollback PostgreSQL maintenance preview: %w", rollbackErr)
	}
	return result, nil
}
