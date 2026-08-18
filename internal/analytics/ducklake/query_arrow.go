//go:build duckdb_arrow

package ducklake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
)

const nativeArrowEnabled = true

// QueryArrow streams native DuckDB Arrow batches while the admitted operation's
// physical connection remains pinned. The sink is invoked synchronously and
// must not retain DuckDB-owned buffers.
func (e *Environment) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	if sink == nil {
		return fmt.Errorf("Arrow sink is required")
	}
	current, ok := ctx.Value(leaseContextKey{}).(*leaseState)
	if !ok || current == nil {
		return ErrUnadmitted
	}
	if current.env != e {
		return ErrConflictingLease
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return queryArrow(ctx, conn, plan, sink)
}

func queryArrow(ctx context.Context, conn *sql.Conn, plan semanticquery.Plan, sink arrowquery.Sink) error {
	err := conn.Raw(func(raw any) error {
		driverConn, ok := raw.(driver.Conn)
		if !ok {
			return fmt.Errorf("analytical connection does not expose a database driver connection")
		}
		if guarded, ok := driverConn.(*guardedConn); ok {
			unwrapped, unwrapErr := guarded.arrowDriverConn(plan.SQL)
			if unwrapErr != nil {
				return unwrapErr
			}
			driverConn = unwrapped
		}
		arrowConn, err := duckdb.NewArrowFromConn(driverConn)
		if err != nil {
			return err
		}
		reader, err := arrowConn.QueryContext(ctx, plan.SQL, plan.Args...)
		if err != nil {
			return err
		}
		defer reader.Release()
		if err := arrowquery.ConsumeSchemaBudget(ctx, reader.Schema()); err != nil {
			return err
		}
		if err := sink.WriteSchema(reader.Schema()); err != nil {
			return err
		}
		for reader.Next() {
			record := reader.RecordBatch()
			if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
				return err
			}
			if err := sink.WriteRecord(record); err != nil {
				return err
			}
		}
		return reader.Err()
	})
	return analyticsresource.Classify(err)
}
