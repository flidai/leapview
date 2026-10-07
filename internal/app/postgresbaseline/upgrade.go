package postgresbaseline

import (
	"context"
	"database/sql"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyUpgrade retains the product ACL policy inside the same migration
// fence as the preflight-bound Goose upgrade. Admission must bind the provider request and recovery frontier;
// this primitive must never be invoked from serving startup.
func ApplyUpgrade(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, expected int64, admit func(context.Context) error) error {
	return migrations.ApplyUpgrade(ctx, pool, db, expected, admit, func(ctx context.Context, db *sql.DB) error {
		return migrations.ReconcileRolePolicy(ctx, db, rolePolicySQL)
	})
}
