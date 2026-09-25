package postgresbaseline

import (
	"context"
	"database/sql"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyDemoUpgrade retains the product ACL policy inside the same migration
// fence as the bounded Goose upgrade. Admission must bind the provider request and recovery frontier;
// this primitive must never be invoked from serving startup.
func ApplyDemoUpgrade(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, admit func(context.Context) error) error {
	return migrations.ApplyDemoUpgrade(ctx, pool, db, admit, func(ctx context.Context, db *sql.DB) error {
		return migrations.ReconcileRolePolicy(ctx, db, rolePolicySQL)
	})
}
